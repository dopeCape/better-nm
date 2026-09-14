//! Drives the client, auto-start, restart and the stream reader against a real
//! `bnmd --fake` built from the Go tree. Skips when `go` is not on PATH.

use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use bnm_desktop_lib::daemon::{self, Client};
use bnm_desktop_lib::stream::{Sink, StreamRunner, EV_CHANGE, EV_STREAM};
use serde_json::Value;
use tokio::sync::mpsc;

struct ChanSink {
    tx: mpsc::UnboundedSender<(String, Value)>,
    seen: Mutex<Vec<(String, Value)>>,
}

impl Sink for ChanSink {
    fn emit(&self, event: &str, payload: Value) {
        self.seen
            .lock()
            .unwrap()
            .push((event.to_string(), payload.clone()));
        let _ = self.tx.send((event.to_string(), payload));
    }
}

fn repo_root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("..")
        .join("..")
        .canonicalize()
        .expect("repo root")
}

fn have_go() -> bool {
    std::process::Command::new("go")
        .arg("version")
        .output()
        .map(|o| o.status.success())
        .unwrap_or(false)
}

fn build_bnmd(out: &Path) -> PathBuf {
    let bin = out.join("bnmd");
    let st = std::process::Command::new("go")
        .args(["build", "-o"])
        .arg(&bin)
        .arg("./cmd/bnmd")
        .current_dir(repo_root())
        .status()
        .expect("run go build");
    assert!(st.success(), "go build ./cmd/bnmd failed");
    bin
}

async fn wait_for<F: Fn(&[(String, Value)]) -> bool>(
    sink: &ChanSink,
    rx: &mut mpsc::UnboundedReceiver<(String, Value)>,
    timeout: Duration,
    pred: F,
) -> bool {
    let deadline = tokio::time::Instant::now() + timeout;
    loop {
        if pred(&sink.seen.lock().unwrap()) {
            return true;
        }
        match tokio::time::timeout_at(deadline, rx.recv()).await {
            Ok(Some(_)) => continue,
            _ => return pred(&sink.seen.lock().unwrap()),
        }
    }
}

#[tokio::test(flavor = "multi_thread")]
async fn fake_daemon_round_trip() {
    if !have_go() {
        eprintln!("skipping: go not on PATH");
        return;
    }
    // Short socket paths: Unix sockets are limited to ~107 bytes.
    let dir = tempfile::Builder::new()
        .prefix("bnmdt")
        .tempdir_in(std::env::temp_dir())
        .unwrap();
    let root = dir.path().to_path_buf();
    // Isolate the fake daemon's own state and config, and our unit-file lookup.
    std::env::set_var("XDG_STATE_HOME", root.join("state"));
    std::env::set_var("XDG_CONFIG_HOME", root.join("config"));
    std::env::set_var("BNM_DAEMON", "/nonexistent/bnmd"); // must be overridden by with_daemon_path
    let bin = build_bnmd(&root);
    let socket = root.join("run").join("bnmd.sock");
    std::fs::create_dir_all(socket.parent().unwrap()).unwrap();

    let client = Arc::new(
        Client::new(socket.clone())
            .with_daemon_path(Some(bin.clone()))
            .with_daemon_args(vec!["--fake".into()]),
    );

    // Absent socket: the first request auto-starts the daemon, then checks the API version.
    assert!(daemon::reachable(&socket).await.is_err());
    let (status, body) = client
        .request("GET", "/v1/status", None)
        .await
        .expect("status");
    assert_eq!(status, 200, "{body}");
    let v: Value = serde_json::from_str(&body).unwrap();
    assert_eq!(v["api_version"], 1);
    assert_eq!(
        v["nm_version"], "1.46.0-fake",
        "the test must talk to bnmd --fake"
    );
    assert!(daemon::reachable(&socket).await.is_ok());
    let pid = daemon::read_pid(&socket).expect("pid file beside the socket");
    assert!(daemon::process_alive(pid));
    assert!(
        daemon::log_path().exists(),
        "auto-start logs to $XDG_STATE_HOME/bnm/bnmd.log"
    );

    let (status, body) = client.request("GET", "/v1/wifi", None).await.unwrap();
    assert_eq!(status, 200, "{body}");
    let wifi: Value = serde_json::from_str(&body).unwrap();
    assert!(wifi.is_array());

    // A POST with a JSON body.
    let (status, body) = client
        .request("POST", "/v1/wifi/scan", Some("{}"))
        .await
        .unwrap();
    assert_eq!(status, 200, "{body}");
    assert_eq!(serde_json::from_str::<Value>(&body).unwrap()["ok"], true);

    // 404 passes through with the error body intact.
    let (status, body) = client
        .request(
            "GET",
            "/v1/profiles/00000000-0000-0000-0000-000000000000",
            None,
        )
        .await
        .unwrap();
    assert_eq!(status, 404, "{body}");
    let err: Value = serde_json::from_str(&body).unwrap();
    assert_eq!(err["code"], "not-found");
    assert!(err["error"]
        .as_str()
        .map(|s| !s.is_empty())
        .unwrap_or(false));

    // Unknown JSON fields are a 400 from the daemon, not a transport error.
    let (status, _) = client
        .request("POST", "/v1/wifi/scan", Some(r#"{"bogus":1}"#))
        .await
        .unwrap();
    assert_eq!(status, 400);

    // daemon_status
    let st = client.status().await;
    assert!(st.running);
    assert_eq!(st.pid, Some(pid));
    assert!(st.version.is_some());
    assert!(!st.unit_installed);

    // The event stream: connected, then a change hint after a scan.
    let (tx, mut rx) = mpsc::unbounded_channel();
    let sink = Arc::new(ChanSink {
        tx,
        seen: Mutex::new(Vec::new()),
    });
    let stream = StreamRunner::new(client.clone(), sink.clone());
    stream.start().await;
    stream.start().await; // idempotent
    assert!(stream.is_running().await);
    assert!(
        wait_for(&sink, &mut rx, Duration::from_secs(5), |seen| {
            seen.iter()
                .any(|(e, p)| e == EV_STREAM && p["connected"] == true)
        })
        .await,
        "stream never connected: {:?}",
        sink.seen.lock().unwrap()
    );
    let (status, _) = client
        .request("POST", "/v1/wifi/scan", Some("{}"))
        .await
        .unwrap();
    assert_eq!(status, 200);
    assert!(
        wait_for(&sink, &mut rx, Duration::from_secs(5), |seen| {
            seen.iter()
                .any(|(e, p)| e == EV_CHANGE && p["kind"].is_string())
        })
        .await,
        "no change hint arrived: {:?}",
        sink.seen.lock().unwrap()
    );

    // Restart: SIGTERM the pid, spawn again, wait for the socket; the stream reconnects.
    client.restart().await.expect("restart");
    let pid2 = daemon::read_pid(&socket).expect("pid after restart");
    assert_ne!(pid, pid2);
    assert!(
        wait_for(&sink, &mut rx, Duration::from_secs(15), |seen| {
            let mut disconnected = false;
            for (e, p) in seen {
                if e == EV_STREAM && p["connected"] == false {
                    disconnected = true;
                } else if disconnected && e == EV_STREAM && p["connected"] == true {
                    return true;
                }
            }
            false
        })
        .await,
        "stream did not reconnect: {:?}",
        sink.seen.lock().unwrap()
    );
    let (status, _) = client.request("GET", "/v1/status", None).await.unwrap();
    assert_eq!(status, 200);

    stream.stop().await;
    assert!(!stream.is_running().await);

    // Tear down.
    unsafe {
        libc::kill(pid2 as i32, libc::SIGTERM);
    }
    let deadline = std::time::Instant::now() + Duration::from_secs(5);
    while daemon::process_alive(pid2) && std::time::Instant::now() < deadline {
        tokio::time::sleep(Duration::from_millis(50)).await;
    }
    assert!(!daemon::process_alive(pid2), "fake daemon did not exit");
}
