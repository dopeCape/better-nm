//! Server-Sent Events from bnmd: the parser, the long-lived `/v1/events/stream`
//! reader (reconnects with backoff, forwards frames as Tauri events through a
//! `Sink`) and the one-at-a-time speed-test runner over `POST /v1/speed`.
//!
//! The parser is unit-tested below; the readers run against `bnmd --fake` in
//! `tests/daemon_live.rs` with a channel-backed `Sink`.

use std::sync::Arc;
use std::time::Duration;

use http_body_util::BodyExt;
use serde_json::{json, Value};
use tokio::sync::{broadcast, Mutex};
use tokio::task::JoinHandle;
use tracing::{debug, info, warn};

use crate::daemon::{Client, DaemonError};

/// Event names the shell emits (CONTRACT.md).
pub const EV_CHANGE: &str = "bnm://change";
pub const EV_EVENT: &str = "bnm://event";
pub const EV_STREAM: &str = "bnm://stream";
pub const EV_SPEED: &str = "bnm://speed";
pub const EV_CONFIG: &str = "bnm://config";

const BACKOFF_MIN: Duration = Duration::from_millis(500);
const BACKOFF_MAX: Duration = Duration::from_secs(10);
/// The daemon pings every 15 s; silence beyond this means the connection is dead.
const IDLE_TIMEOUT: Duration = Duration::from_secs(45);

/// Where emitted events go: the Tauri app in production, a channel in tests.
pub trait Sink: Send + Sync + 'static {
    fn emit(&self, event: &str, payload: Value);
}

// --- parser ------------------------------------------------------------------------

/// One thing the parser produced.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum SseItem {
    /// A `: comment` line (bnmd sends `: connected` and `: ping`).
    Comment(String),
    /// A dispatched event: `name` defaults to `message`, `data` joins multi-line data with `\n`.
    Event { name: String, data: String },
}

/// Incremental `text/event-stream` parser (WHATWG algorithm, minus `id`/`retry`).
#[derive(Debug, Default)]
pub struct SseParser {
    buf: Vec<u8>,
    event: String,
    data: Vec<String>,
}

impl SseParser {
    pub fn new() -> Self {
        Self::default()
    }

    /// Feeds bytes and returns every item completed by them.
    pub fn feed(&mut self, bytes: &[u8]) -> Vec<SseItem> {
        self.buf.extend_from_slice(bytes);
        let mut out = Vec::new();
        while let Some(pos) = self.buf.iter().position(|&b| b == b'\n') {
            let mut line: Vec<u8> = self.buf.drain(..=pos).collect();
            line.pop(); // the \n
            if line.last() == Some(&b'\r') {
                line.pop();
            }
            let line = String::from_utf8_lossy(&line).into_owned();
            if let Some(item) = self.line(&line) {
                out.push(item);
            }
        }
        out
    }

    fn line(&mut self, line: &str) -> Option<SseItem> {
        if line.is_empty() {
            if self.data.is_empty() {
                self.event.clear();
                return None;
            }
            let name = if self.event.is_empty() {
                "message".to_string()
            } else {
                std::mem::take(&mut self.event)
            };
            let data = std::mem::take(&mut self.data).join("\n");
            return Some(SseItem::Event { name, data });
        }
        if let Some(rest) = line.strip_prefix(':') {
            return Some(SseItem::Comment(
                rest.strip_prefix(' ').unwrap_or(rest).to_string(),
            ));
        }
        let (field, value) = match line.split_once(':') {
            Some((f, v)) => (f, v.strip_prefix(' ').unwrap_or(v)),
            None => (line, ""),
        };
        match field {
            "event" => self.event = value.to_string(),
            "data" => self.data.push(value.to_string()),
            _ => {}
        }
        None
    }
}

// --- change hints ------------------------------------------------------------------

/// A `core.Change` as it arrives on the stream; the tray subscribes to these.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Change {
    pub kind: String,
}

// --- event stream reader -----------------------------------------------------------

/// The `/v1/events/stream` reader. `start` is idempotent; `stop` aborts.
pub struct StreamRunner {
    client: Arc<Client>,
    sink: Arc<dyn Sink>,
    task: Mutex<Option<JoinHandle<()>>>,
    changes: broadcast::Sender<Change>,
}

impl StreamRunner {
    pub fn new(client: Arc<Client>, sink: Arc<dyn Sink>) -> Self {
        let (changes, _) = broadcast::channel(64);
        Self {
            client,
            sink,
            task: Mutex::new(None),
            changes,
        }
    }

    /// In-process subscription to change hints (the tray uses it).
    pub fn subscribe(&self) -> broadcast::Receiver<Change> {
        self.changes.subscribe()
    }

    /// Starts the reader unless it is already running.
    pub async fn start(&self) {
        let mut task = self.task.lock().await;
        if task.as_ref().map(|t| !t.is_finished()).unwrap_or(false) {
            return;
        }
        let client = self.client.clone();
        let sink = self.sink.clone();
        let changes = self.changes.clone();
        *task = Some(tokio::spawn(async move {
            run_stream(client, sink, changes).await
        }));
    }

    pub async fn stop(&self) {
        if let Some(t) = self.task.lock().await.take() {
            t.abort();
        }
    }

    pub async fn is_running(&self) -> bool {
        self.task
            .lock()
            .await
            .as_ref()
            .map(|t| !t.is_finished())
            .unwrap_or(false)
    }
}

async fn run_stream(client: Arc<Client>, sink: Arc<dyn Sink>, changes: broadcast::Sender<Change>) {
    let mut attempt: u64 = 0;
    let mut backoff = BACKOFF_MIN;
    loop {
        let err = match client
            .open("GET", "/v1/events/stream", None, "text/event-stream")
            .await
        {
            Ok(resp) if resp.status().is_success() => {
                attempt = 0;
                backoff = BACKOFF_MIN;
                info!("stream connected");
                sink.emit(EV_STREAM, json!({ "connected": true }));
                let _ = changes.send(Change {
                    kind: "stream".into(),
                });
                let reason = read_event_stream(resp, &sink, &changes).await;
                let _ = changes.send(Change {
                    kind: "stream".into(),
                });
                reason
            }
            Ok(resp) => {
                let status = resp.status();
                let body = crate::daemon::collect(resp).await.unwrap_or_default();
                format!("GET /v1/events/stream answered {status}: {body}")
            }
            Err(e) => e.to_string(),
        };
        attempt += 1;
        warn!(attempt, error = %err, retry_in = ?backoff, "stream disconnected");
        sink.emit(
            EV_STREAM,
            json!({ "connected": false, "error": err, "attempt": attempt }),
        );
        tokio::time::sleep(backoff).await;
        backoff = (backoff * 2).min(BACKOFF_MAX);
    }
}

/// Reads frames until the body ends or errors; returns the reason.
async fn read_event_stream(
    resp: hyper::Response<hyper::body::Incoming>,
    sink: &Arc<dyn Sink>,
    changes: &broadcast::Sender<Change>,
) -> String {
    let mut body = resp.into_body();
    let mut parser = SseParser::new();
    loop {
        let frame = match tokio::time::timeout(IDLE_TIMEOUT, body.frame()).await {
            Err(_) => return format!("no data for {IDLE_TIMEOUT:?}"),
            Ok(None) => return "stream closed by the daemon".into(),
            Ok(Some(Err(e))) => return format!("read: {e}"),
            Ok(Some(Ok(f))) => f,
        };
        let Some(data) = frame.data_ref() else {
            continue;
        };
        for item in parser.feed(data) {
            match item {
                SseItem::Comment(c) => debug!(comment = %c, "stream comment"),
                SseItem::Event { name, data } => {
                    let payload: Value = match serde_json::from_str(&data) {
                        Ok(v) => v,
                        Err(e) => {
                            warn!(event = %name, error = %e, "stream: bad JSON");
                            continue;
                        }
                    };
                    match name.as_str() {
                        "change" => {
                            let kind = payload
                                .get("kind")
                                .and_then(Value::as_str)
                                .unwrap_or("")
                                .to_string();
                            debug!(kind = %kind, "change");
                            let _ = changes.send(Change { kind });
                            sink.emit(EV_CHANGE, payload);
                        }
                        "event" => sink.emit(EV_EVENT, payload),
                        other => debug!(event = %other, "stream: ignored event type"),
                    }
                }
            }
        }
    }
}

// --- speed test --------------------------------------------------------------------

/// Runs one `POST /v1/speed` SSE at a time and emits `bnm://speed`.
pub struct SpeedRunner {
    client: Arc<Client>,
    sink: Arc<dyn Sink>,
    task: Mutex<Option<JoinHandle<()>>>,
}

impl SpeedRunner {
    pub fn new(client: Arc<Client>, sink: Arc<dyn Sink>) -> Self {
        Self {
            client,
            sink,
            task: Mutex::new(None),
        }
    }

    /// Starts a test; rejects with `speed-running` while one is in flight.
    pub async fn start(&self, opts: Value) -> Result<(), String> {
        let mut task = self.task.lock().await;
        if task.as_ref().map(|t| !t.is_finished()).unwrap_or(false) {
            return Err("speed-running".into());
        }
        let client = self.client.clone();
        let sink = self.sink.clone();
        *task = Some(tokio::spawn(
            async move { run_speed(client, sink, opts).await },
        ));
        Ok(())
    }

    /// Aborts the running test (dropping the connection cancels it daemon-side).
    pub async fn cancel(&self) {
        if let Some(t) = self.task.lock().await.take() {
            if !t.is_finished() {
                t.abort();
                self.sink
                    .emit(EV_SPEED, json!({ "phase": "error", "error": "cancelled" }));
            }
        }
    }
}

fn error_text(body: &str) -> String {
    serde_json::from_str::<Value>(body)
        .ok()
        .and_then(|v| v.get("error").and_then(Value::as_str).map(str::to_owned))
        .unwrap_or_else(|| body.trim().to_string())
}

async fn run_speed(client: Arc<Client>, sink: Arc<dyn Sink>, opts: Value) {
    let body = opts.to_string();
    let resp = match client
        .open("POST", "/v1/speed", Some(&body), "text/event-stream")
        .await
    {
        Ok(r) => r,
        Err(e) => {
            sink.emit(
                EV_SPEED,
                json!({ "phase": "error", "error": e.to_string() }),
            );
            return;
        }
    };
    if !resp.status().is_success() {
        let status = resp.status();
        let text = crate::daemon::collect(resp).await.unwrap_or_default();
        let err = if text.is_empty() {
            status.to_string()
        } else {
            error_text(&text)
        };
        sink.emit(EV_SPEED, json!({ "phase": "error", "error": err }));
        return;
    }
    let mut body = resp.into_body();
    let mut parser = SseParser::new();
    let mut finished = false;
    loop {
        let frame = match body.frame().await {
            None => break,
            Some(Err(e)) => {
                if !finished {
                    sink.emit(
                        EV_SPEED,
                        json!({ "phase": "error", "error": format!("read: {e}") }),
                    );
                }
                return;
            }
            Some(Ok(f)) => f,
        };
        let Some(data) = frame.data_ref() else {
            continue;
        };
        for item in parser.feed(data) {
            let SseItem::Event { name, data } = item else {
                continue;
            };
            let payload: Value = serde_json::from_str(&data).unwrap_or(Value::String(data.clone()));
            match name.as_str() {
                "progress" => sink.emit(EV_SPEED, json!({ "phase": "progress", "data": payload })),
                "result" => {
                    finished = true;
                    sink.emit(EV_SPEED, json!({ "phase": "result", "data": payload }));
                }
                "error" => {
                    finished = true;
                    sink.emit(
                        EV_SPEED,
                        json!({ "phase": "error", "error": error_text(&data) }),
                    );
                }
                other => debug!(event = %other, "speed: ignored event type"),
            }
        }
    }
    if !finished {
        sink.emit(
            EV_SPEED,
            json!({ "phase": "error", "error": "speed test stream ended without a result" }),
        );
    }
}

impl From<DaemonError> for String {
    fn from(e: DaemonError) -> Self {
        e.to_string()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_bnmd_frames() {
        let mut p = SseParser::new();
        let items = p.feed(b": connected\n\nevent: change\ndata: {\"kind\":\"wifi\"}\n\n");
        assert_eq!(
            items,
            vec![
                SseItem::Comment("connected".into()),
                SseItem::Event {
                    name: "change".into(),
                    data: "{\"kind\":\"wifi\"}".into()
                }
            ]
        );
    }

    #[test]
    fn multi_line_data_and_crlf() {
        let mut p = SseParser::new();
        let items = p.feed(b"event: event\r\ndata: {\"a\":1,\r\ndata: \"b\":2}\r\n\r\n");
        assert_eq!(
            items,
            vec![SseItem::Event {
                name: "event".into(),
                data: "{\"a\":1,\n\"b\":2}".into()
            }]
        );
    }

    #[test]
    fn heartbeat_and_split_chunks() {
        let mut p = SseParser::new();
        assert_eq!(p.feed(b": pi"), vec![]);
        assert_eq!(p.feed(b"ng\n"), vec![SseItem::Comment("ping".into())]);
        assert_eq!(p.feed(b"data: hel"), vec![]);
        assert_eq!(p.feed(b"lo\n"), vec![]);
        assert_eq!(
            p.feed(b"\n"),
            vec![SseItem::Event {
                name: "message".into(),
                data: "hello".into()
            }]
        );
        // A blank line with no data dispatches nothing and resets the event name.
        assert_eq!(p.feed(b"event: x\n\n"), vec![]);
        assert_eq!(
            p.feed(b"data: y\n\n"),
            vec![SseItem::Event {
                name: "message".into(),
                data: "y".into()
            }]
        );
    }

    #[test]
    fn ignores_id_retry_and_unknown_fields() {
        let mut p = SseParser::new();
        let items = p.feed(b"id: 7\nretry: 100\nfoo: bar\ndata:no-space\n\n");
        assert_eq!(
            items,
            vec![SseItem::Event {
                name: "message".into(),
                data: "no-space".into()
            }]
        );
    }

    #[test]
    fn error_text_extracts_error_field() {
        assert_eq!(error_text(r#"{"error":"busy","code":"conflict"}"#), "busy");
        assert_eq!(error_text("plain text\n"), "plain text");
    }
}
