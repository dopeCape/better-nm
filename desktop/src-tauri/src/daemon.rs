//! The bnmd side of the shell: socket discovery, auto-start, one HTTP/1.1
//! client over the Unix socket (hyper, no TLS, no pool: one connection per
//! request), the api_version check, and the `bnm daemon status|install|restart`
//! equivalents. Mirrors `internal/client` (client.go, spawn.go) and
//! `internal/cli/daemon.go` in the Go tree; keep the rules in sync with them.
//!
//! Tested by unit tests for the path rules and, in `tests/daemon_live.rs`, against
//! a real `bnmd --fake` built from the Go tree.

use std::io;
use std::os::unix::fs::{OpenOptionsExt, PermissionsExt};
use std::os::unix::process::CommandExt;
use std::path::{Path, PathBuf};
use std::process::Stdio;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::{Duration, Instant};

use bytes::Bytes;
use http_body_util::{BodyExt, Full};
use hyper::body::Incoming;
use hyper::client::conn::http1::SendRequest;
use hyper::header::{ACCEPT, CONTENT_TYPE, HOST};
use hyper::{Request, Response};
use hyper_util::rt::TokioIo;
use serde::Serialize;
use tokio::net::UnixStream;
use tokio::sync::Mutex;
use tracing::{debug, info, warn};

/// The API major version this shell speaks (`docs/API.md`).
pub const API_VERSION: i64 = 1;

/// How long an auto-started daemon gets to bind its socket.
pub const START_TIMEOUT: Duration = Duration::from_secs(3);

/// How long `stop` waits for a SIGTERMed daemon to go away.
const STOP_TIMEOUT: Duration = Duration::from_secs(5);

/// The executable we look for.
pub const DAEMON_BINARY: &str = "bnmd";

/// The pid/lock file bnmd keeps beside its socket.
pub const LOCK_NAME: &str = "bnmd.pid";

/// The systemd user unit `daemon_install` writes.
pub const UNIT_NAME: &str = "bnmd.service";

/// A transport-level failure. Its `Display` form is what the frontend sees as the
/// rejection string (`daemon-unreachable: ...`, `api-mismatch: ...`).
#[derive(Debug, thiserror::Error)]
pub enum DaemonError {
    #[error("daemon-unreachable: {0}")]
    Unreachable(String),
    #[error("api-mismatch: daemon speaks v{got}, app needs v{want}")]
    ApiMismatch { got: i64, want: i64 },
}

// --- paths -----------------------------------------------------------------------

fn env_nonempty(key: &str) -> Option<String> {
    std::env::var(key).ok().filter(|v| !v.is_empty())
}

/// Same rules as `paths.RuntimeDir`: `BNM_RUNTIME_DIR`, `$XDG_RUNTIME_DIR/bnm`, `/tmp/bnm-<uid>`.
pub fn runtime_dir() -> PathBuf {
    if let Some(v) = env_nonempty("BNM_RUNTIME_DIR") {
        return PathBuf::from(v);
    }
    if let Some(v) = env_nonempty("XDG_RUNTIME_DIR") {
        return PathBuf::from(v).join("bnm");
    }
    // SAFETY: getuid has no preconditions and cannot fail.
    let uid = unsafe { libc::getuid() };
    std::env::temp_dir().join(format!("bnm-{uid}"))
}

/// The daemon socket: `BNM_SOCKET` or `<runtime dir>/bnmd.sock`.
pub fn socket_path() -> PathBuf {
    if let Some(v) = env_nonempty("BNM_SOCKET") {
        return PathBuf::from(v);
    }
    runtime_dir().join("bnmd.sock")
}

pub fn home_dir() -> PathBuf {
    env_nonempty("HOME")
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from("."))
}

/// `$XDG_STATE_HOME/bnm` (default `~/.local/state/bnm`): where an auto-started bnmd logs.
pub fn state_dir() -> PathBuf {
    if let Some(v) = env_nonempty("XDG_STATE_HOME") {
        return PathBuf::from(v).join("bnm");
    }
    home_dir().join(".local").join("state").join("bnm")
}

pub fn log_path() -> PathBuf {
    state_dir().join("bnmd.log")
}

/// `$XDG_CONFIG_HOME` (default `~/.config`).
pub fn xdg_config_home() -> PathBuf {
    if let Some(v) = env_nonempty("XDG_CONFIG_HOME") {
        return PathBuf::from(v);
    }
    home_dir().join(".config")
}

/// `~/.config/systemd/user/bnmd.service`.
pub fn unit_path() -> PathBuf {
    xdg_config_home()
        .join("systemd")
        .join("user")
        .join(UNIT_NAME)
}

/// The pid file beside a socket.
pub fn pid_file(socket: &Path) -> PathBuf {
    socket
        .parent()
        .map(Path::to_path_buf)
        .unwrap_or_default()
        .join(LOCK_NAME)
}

fn is_executable(p: &Path) -> bool {
    std::fs::metadata(p)
        .map(|m| m.is_file() && m.permissions().mode() & 0o111 != 0)
        .unwrap_or(false)
}

fn look_path(name: &str) -> Option<PathBuf> {
    let path = env_nonempty("PATH")?;
    std::env::split_paths(&path)
        .map(|d| d.join(name))
        .find(|c| is_executable(c))
}

/// Locates bnmd like `client.FindDaemon`: `BNM_DAEMON`, next to this executable, `$PATH`.
pub fn find_daemon() -> Result<PathBuf, String> {
    if let Some(v) = env_nonempty("BNM_DAEMON") {
        return Ok(PathBuf::from(v));
    }
    if let Ok(exe) = std::env::current_exe() {
        let exe = std::fs::canonicalize(&exe).unwrap_or(exe);
        if let Some(dir) = exe.parent() {
            let cand = dir.join(DAEMON_BINARY);
            if is_executable(&cand) {
                return Ok(cand);
            }
        }
    }
    look_path(DAEMON_BINARY).ok_or_else(|| {
        format!(
            "{DAEMON_BINARY} not found next to the app or in PATH; install bnm (which ships bnmd)"
        )
    })
}

// --- process control ---------------------------------------------------------------

/// Starts bnmd detached (own session, cwd `/`, stdio appended to the log file) and
/// returns its pid. The child is reaped by a helper thread; it is not ours otherwise.
pub fn spawn_daemon(bin: &Path, socket: &Path, extra_args: &[String]) -> Result<u32, String> {
    let log_dir = state_dir();
    std::fs::create_dir_all(&log_dir).map_err(|e| format!("create {}: {e}", log_dir.display()))?;
    let _ = std::fs::set_permissions(&log_dir, std::fs::Permissions::from_mode(0o700));
    let log = std::fs::OpenOptions::new()
        .create(true)
        .append(true)
        .mode(0o600)
        .open(log_dir.join("bnmd.log"))
        .map_err(|e| format!("open log: {e}"))?;
    let err_log = log.try_clone().map_err(|e| format!("dup log: {e}"))?;
    let mut cmd = std::process::Command::new(bin);
    cmd.arg("--socket")
        .arg(socket)
        .args(extra_args)
        .stdin(Stdio::null())
        .stdout(Stdio::from(log))
        .stderr(Stdio::from(err_log))
        // A long-lived daemon must not pin the directory the app was launched from.
        .current_dir("/");
    // SAFETY: setsid is async-signal-safe and touches nothing shared with the parent.
    unsafe {
        cmd.pre_exec(|| {
            if libc::setsid() == -1 {
                return Err(io::Error::last_os_error());
            }
            Ok(())
        });
    }
    let mut child = cmd
        .spawn()
        .map_err(|e| format!("start {}: {e}", bin.display()))?;
    let pid = child.id();
    info!(path = %bin.display(), pid, socket = %socket.display(), "started bnmd");
    std::thread::Builder::new()
        .name("bnmd-reaper".into())
        .spawn(move || {
            let _ = child.wait();
        })
        .map_err(|e| format!("reaper thread: {e}"))?;
    Ok(pid)
}

/// The pid recorded beside the socket, when that process is alive.
pub fn read_pid(socket: &Path) -> Option<u32> {
    let text = std::fs::read_to_string(pid_file(socket)).ok()?;
    let pid: i32 = text.trim().parse().ok()?;
    if pid <= 0 {
        return None;
    }
    process_alive(pid as u32).then_some(pid as u32)
}

pub fn process_alive(pid: u32) -> bool {
    // SAFETY: kill with signal 0 only checks for existence.
    let rc = unsafe { libc::kill(pid as libc::pid_t, 0) };
    rc == 0 || io::Error::last_os_error().raw_os_error() == Some(libc::EPERM)
}

fn send_sigterm(pid: u32) -> Result<(), String> {
    // SAFETY: plain kill(2); the pid came from bnmd's own lock file.
    let rc = unsafe { libc::kill(pid as libc::pid_t, libc::SIGTERM) };
    if rc == 0 {
        Ok(())
    } else {
        Err(format!("signal pid {pid}: {}", io::Error::last_os_error()))
    }
}

/// Can we open the socket right now?
pub async fn reachable(socket: &Path) -> Result<(), String> {
    match tokio::time::timeout(Duration::from_millis(500), UnixStream::connect(socket)).await {
        Ok(Ok(_)) => Ok(()),
        Ok(Err(e)) => Err(e.to_string()),
        Err(_) => Err("connect timed out".into()),
    }
}

async fn wait_for_socket(socket: &Path, timeout: Duration) -> Result<(), String> {
    let deadline = Instant::now() + timeout;
    loop {
        match reachable(socket).await {
            Ok(()) => return Ok(()),
            Err(e) if Instant::now() >= deadline => return Err(e),
            Err(_) => tokio::time::sleep(Duration::from_millis(50)).await,
        }
    }
}

async fn wait_for_gone(socket: &Path, pid: Option<u32>, timeout: Duration) -> bool {
    let deadline = Instant::now() + timeout;
    while Instant::now() < deadline {
        let socket_gone = reachable(socket).await.is_err();
        let pid_gone = pid.map(|p| !process_alive(p)).unwrap_or(true);
        if socket_gone && pid_gone {
            return true;
        }
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
    false
}

// --- systemd -------------------------------------------------------------------------

pub fn have_systemctl() -> bool {
    look_path("systemctl").is_some()
}

/// `systemctl --user <args>`; the error carries systemctl's own text.
pub async fn systemctl(args: &[&str]) -> Result<String, String> {
    let out = tokio::process::Command::new("systemctl")
        .arg("--user")
        .args(args)
        .output()
        .await
        .map_err(|e| format!("systemctl --user {}: {e}", args.join(" ")))?;
    let text = format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    if out.status.success() {
        Ok(text.trim().to_string())
    } else {
        Err(format!(
            "systemctl --user {}: {}: {}",
            args.join(" "),
            out.status,
            text.trim()
        ))
    }
}

/// systemctl's word for the unit (`active`, `inactive`, `failed`, ...), `unknown`
/// when it says nothing, `""` when systemctl is missing.
pub async fn unit_state() -> String {
    if !have_systemctl() {
        return String::new();
    }
    let out = tokio::process::Command::new("systemctl")
        .args(["--user", "is-active", UNIT_NAME])
        .output()
        .await;
    match out {
        Ok(o) => {
            let s = String::from_utf8_lossy(&o.stdout).trim().to_string();
            if s.is_empty() {
                "unknown".into()
            } else {
                s
            }
        }
        Err(_) => "unknown".into(),
    }
}

/// The unit file `bnm daemon install` writes.
pub fn unit_file(bin: &Path) -> String {
    format!(
        "[Unit]\n\
Description=bnm network daemon (NetworkManager front end)\n\
Documentation=https://github.com/dopeCape/better-nm\n\
After=network.target\n\
\n\
[Service]\n\
Type=simple\n\
ExecStart={}\n\
Restart=on-failure\n\
RestartSec=2\n\
Slice=background.slice\n\
\n\
[Install]\n\
WantedBy=default.target\n",
        bin.display()
    )
}

// --- client --------------------------------------------------------------------------

/// One bnmd, reached over its Unix socket. Cheap to share behind an `Arc`.
pub struct Client {
    socket: PathBuf,
    auto_start: bool,
    daemon_path: Option<PathBuf>,
    /// Extra arguments for a spawned bnmd (tests pass `--fake`).
    daemon_args: Vec<String>,
    check_version: bool,
    /// Serialises start/stop/restart so two callers never spawn two daemons and the
    /// stream reader does not respawn one that `daemon_restart` is stopping.
    lifecycle: Mutex<()>,
    checked: AtomicBool,
}

impl Client {
    /// A client for `socket` with auto-start and the version check on.
    pub fn new(socket: PathBuf) -> Self {
        Self {
            socket,
            auto_start: true,
            daemon_path: None,
            daemon_args: Vec::new(),
            check_version: true,
            lifecycle: Mutex::new(()),
            checked: AtomicBool::new(false),
        }
    }

    pub fn with_auto_start(mut self, on: bool) -> Self {
        self.auto_start = on;
        self
    }

    pub fn with_daemon_path(mut self, p: Option<PathBuf>) -> Self {
        self.daemon_path = p;
        self
    }

    pub fn with_daemon_args(mut self, args: Vec<String>) -> Self {
        self.daemon_args = args;
        self
    }

    pub fn with_version_check(mut self, on: bool) -> Self {
        self.check_version = on;
        self
    }

    pub fn socket(&self) -> &Path {
        &self.socket
    }

    /// Opens one HTTP/1.1 connection. The connection task ends with the response.
    async fn connect(&self) -> Result<SendRequest<Full<Bytes>>, DaemonError> {
        let stream =
            tokio::time::timeout(Duration::from_secs(2), UnixStream::connect(&self.socket))
                .await
                .map_err(|_| {
                    DaemonError::Unreachable(format!(
                        "connect {}: timed out",
                        self.socket.display()
                    ))
                })?
                .map_err(|e| {
                    DaemonError::Unreachable(format!(
                        "bnmd is not running (socket {}): {e}",
                        self.socket.display()
                    ))
                })?;
        let (sender, conn) = hyper::client::conn::http1::handshake(TokioIo::new(stream))
            .await
            .map_err(|e| DaemonError::Unreachable(format!("handshake: {e}")))?;
        tokio::spawn(async move {
            if let Err(e) = conn.await {
                debug!(error = %e, "connection closed with error");
            }
        });
        Ok(sender)
    }

    /// Connects, starting bnmd first when the socket is absent and auto-start is on.
    async fn connect_or_start(&self) -> Result<SendRequest<Full<Bytes>>, DaemonError> {
        let first = match self.connect().await {
            Ok(s) => return Ok(s),
            Err(e) => e,
        };
        if !self.auto_start {
            return Err(first);
        }
        let _guard = self.lifecycle.lock().await;
        // Someone else may have started it while we waited for the lock.
        if let Ok(s) = self.connect().await {
            return Ok(s);
        }
        self.start_locked().await?;
        self.connect().await
    }

    /// Spawns bnmd and waits for the socket. Caller holds `lifecycle`.
    async fn start_locked(&self) -> Result<(), DaemonError> {
        let bin = match &self.daemon_path {
            Some(p) => p.clone(),
            None => find_daemon().map_err(DaemonError::Unreachable)?,
        };
        spawn_daemon(&bin, &self.socket, &self.daemon_args).map_err(DaemonError::Unreachable)?;
        wait_for_socket(&self.socket, START_TIMEOUT)
            .await
            .map_err(|e| {
                DaemonError::Unreachable(format!(
                    "started bnmd but the socket did not appear within {:?} (see {}): {e}",
                    START_TIMEOUT,
                    log_path().display()
                ))
            })
    }

    fn build_request(
        method: &str,
        path: &str,
        body: Option<&str>,
        accept: &str,
    ) -> Result<Request<Full<Bytes>>, DaemonError> {
        let mut b = Request::builder()
            .method(method)
            .uri(path)
            .header(HOST, "bnmd")
            .header(ACCEPT, accept);
        if body.is_some() {
            b = b.header(CONTENT_TYPE, "application/json");
        }
        b.body(Full::new(Bytes::from(body.unwrap_or("").to_owned())))
            .map_err(|e| DaemonError::Unreachable(format!("build request {method} {path}: {e}")))
    }

    /// Sends one request on a fresh connection. No auto-start, no version check.
    async fn send_raw(
        &self,
        method: &str,
        path: &str,
        body: Option<&str>,
        accept: &str,
    ) -> Result<Response<Incoming>, DaemonError> {
        let mut sender = self.connect().await?;
        let req = Self::build_request(method, path, body, accept)?;
        sender
            .send_request(req)
            .await
            .map_err(|e| DaemonError::Unreachable(format!("{method} {path}: {e}")))
    }

    /// Reads `api_version` from `/v1/status` once per client (and again after a restart).
    async fn ensure_version(&self) -> Result<(), DaemonError> {
        if !self.check_version || self.checked.load(Ordering::Acquire) {
            return Ok(());
        }
        let resp = self
            .send_raw("GET", "/v1/status", None, "application/json")
            .await?;
        let status = resp.status();
        let body = collect(resp).await?;
        if !status.is_success() {
            return Err(DaemonError::Unreachable(format!(
                "GET /v1/status answered {status}: {body}"
            )));
        }
        let v: serde_json::Value = serde_json::from_str(&body)
            .map_err(|e| DaemonError::Unreachable(format!("GET /v1/status: bad JSON: {e}")))?;
        let got = v.get("api_version").and_then(|x| x.as_i64()).unwrap_or(0);
        if got != API_VERSION {
            return Err(DaemonError::ApiMismatch {
                got,
                want: API_VERSION,
            });
        }
        let version = v.get("version").and_then(|x| x.as_str()).unwrap_or("?");
        info!(api = got, version, socket = %self.socket.display(), "bnmd api version ok");
        self.checked.store(true, Ordering::Release);
        Ok(())
    }

    /// Forgets the version check (after a restart the daemon may be another build).
    pub fn reset_version_check(&self) {
        self.checked.store(false, Ordering::Release);
    }

    /// Opens a request and hands back the streaming response (for SSE). Auto-starts
    /// the daemon and checks the API version first.
    pub async fn open(
        &self,
        method: &str,
        path: &str,
        body: Option<&str>,
        accept: &str,
    ) -> Result<Response<Incoming>, DaemonError> {
        let mut sender = self.connect_or_start().await?;
        self.ensure_version().await?;
        let req = Self::build_request(method, path, body, accept)?;
        sender
            .send_request(req)
            .await
            .map_err(|e| DaemonError::Unreachable(format!("{method} {path}: {e}")))
    }

    /// One JSON round trip: `(status, body)`. Non-2xx is not an error here.
    pub async fn request(
        &self,
        method: &str,
        path: &str,
        body: Option<&str>,
    ) -> Result<(u16, String), DaemonError> {
        let resp = self.open(method, path, body, "application/json").await?;
        let status = resp.status().as_u16();
        let text = collect(resp).await?;
        Ok((status, text))
    }

    /// Convenience: `request` with a serialisable body.
    pub async fn request_json<T: Serialize>(
        &self,
        method: &str,
        path: &str,
        body: &T,
    ) -> Result<(u16, String), DaemonError> {
        let text = serde_json::to_string(body)
            .map_err(|e| DaemonError::Unreachable(format!("encode body: {e}")))?;
        self.request(method, path, Some(&text)).await
    }

    /// GET and decode, treating non-2xx as an error string.
    pub async fn get_json(&self, path: &str) -> Result<serde_json::Value, String> {
        let (status, body) = self
            .request("GET", path, None)
            .await
            .map_err(|e| e.to_string())?;
        if !(200..300).contains(&status) {
            return Err(format!("GET {path}: {status}: {body}"));
        }
        serde_json::from_str(&body).map_err(|e| format!("GET {path}: bad JSON: {e}"))
    }

    /// `/v1/status` without auto-start or version check; `None` when unreachable.
    pub async fn probe_status(&self) -> Option<serde_json::Value> {
        let resp = self
            .send_raw("GET", "/v1/status", None, "application/json")
            .await
            .ok()?;
        if !resp.status().is_success() {
            return None;
        }
        let body = collect(resp).await.ok()?;
        serde_json::from_str(&body).ok()
    }

    // --- lifecycle (bnm daemon status|install|restart) ------------------------------

    /// What `daemon_status` reports.
    pub async fn status(&self) -> DaemonStatus {
        let st = self.probe_status().await;
        let unit_installed = unit_path().exists();
        let unit_active = unit_state().await == "active";
        DaemonStatus {
            running: st.is_some(),
            socket: self.socket.display().to_string(),
            pid: read_pid(&self.socket),
            version: st
                .as_ref()
                .and_then(|v| v.get("version"))
                .and_then(|v| v.as_str())
                .map(str::to_owned),
            unit_installed,
            unit_active,
        }
    }

    /// Stops the daemon: `systemctl --user stop` when it runs as the service on the
    /// default socket, else SIGTERM to the pid beside the socket. Caller holds `lifecycle`.
    async fn stop_locked(&self) -> Result<(), String> {
        let running = self.probe_status().await.is_some();
        let pid = read_pid(&self.socket);
        if !running && pid.is_none() {
            return Ok(());
        }
        if unit_state().await == "active" && self.socket == socket_path() {
            systemctl(&["stop", UNIT_NAME]).await?;
        } else {
            let pid = pid.ok_or_else(|| {
                format!(
                    "bnmd answers on {} but its pid file {} is missing or stale; kill it by hand: pkill -x bnmd",
                    self.socket.display(),
                    pid_file(&self.socket).display()
                )
            })?;
            send_sigterm(pid)?;
        }
        if wait_for_gone(&self.socket, pid, STOP_TIMEOUT).await {
            Ok(())
        } else {
            Err(format!(
                "bnmd (pid {}) did not exit within {:?}",
                pid.unwrap_or(0),
                STOP_TIMEOUT
            ))
        }
    }

    /// `bnm daemon install`: write the user unit, daemon-reload, enable --now.
    pub async fn install(&self) -> Result<(), String> {
        let _guard = self.lifecycle.lock().await;
        let bin = find_daemon()?;
        let bin = std::fs::canonicalize(&bin).unwrap_or(bin);
        let path = unit_path();
        if let Some(dir) = path.parent() {
            std::fs::create_dir_all(dir).map_err(|e| format!("create {}: {e}", dir.display()))?;
        }
        std::fs::write(&path, unit_file(&bin))
            .map_err(|e| format!("write {}: {e}", path.display()))?;
        info!(unit = %path.display(), exec = %bin.display(), "wrote user unit");
        if !have_systemctl() {
            return Err(format!(
                "systemctl not found; the unit file is in place at {}, start bnmd another way",
                path.display()
            ));
        }
        // A hand-started daemon holds the socket lock; stop it so the unit can bind.
        if self.probe_status().await.is_some() && unit_state().await != "active" {
            self.stop_locked().await?;
        }
        systemctl(&["daemon-reload"]).await?;
        systemctl(&["enable", "--now", UNIT_NAME]).await?;
        wait_for_socket(&self.socket, START_TIMEOUT)
            .await
            .map_err(|e| format!("unit enabled but the socket did not appear: {e}"))?;
        self.reset_version_check();
        Ok(())
    }

    /// `bnm daemon restart`: stop, then start via systemd when the unit is installed,
    /// else spawn detached; wait for the socket either way.
    pub async fn restart(&self) -> Result<(), String> {
        let _guard = self.lifecycle.lock().await;
        self.stop_locked().await?;
        if unit_path().exists() && have_systemctl() && self.socket == socket_path() {
            systemctl(&["start", UNIT_NAME]).await?;
            wait_for_socket(&self.socket, START_TIMEOUT)
                .await
                .map_err(|e| format!("unit started but the socket did not appear: {e}"))?;
        } else {
            self.start_locked().await.map_err(|e| e.to_string())?;
        }
        self.reset_version_check();
        Ok(())
    }
}

/// `daemon_status`'s answer (see CONTRACT.md).
#[derive(Debug, Clone, Serialize)]
pub struct DaemonStatus {
    pub running: bool,
    pub socket: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub pid: Option<u32>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub version: Option<String>,
    pub unit_installed: bool,
    pub unit_active: bool,
}

/// Reads a whole body as text (bounded at 8 MiB).
pub async fn collect(resp: Response<Incoming>) -> Result<String, DaemonError> {
    let limited = http_body_util::Limited::new(resp.into_body(), 8 << 20);
    let bytes = limited
        .collect()
        .await
        .map_err(|e| DaemonError::Unreachable(format!("read body: {e}")))?
        .to_bytes();
    Ok(String::from_utf8_lossy(&bytes).into_owned())
}

/// Warns once when a directory we rely on is missing; used at startup for diagnostics.
pub fn log_socket_discovery() {
    let s = socket_path();
    if s.exists() {
        info!(socket = %s.display(), "socket found");
    } else {
        warn!(socket = %s.display(), "socket absent; bnmd will be started on first use");
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::Mutex as StdMutex;

    // Environment variables are process-global; serialise the tests that touch them.
    static ENV: StdMutex<()> = StdMutex::new(());

    struct EnvGuard(Vec<(String, Option<String>)>);
    impl EnvGuard {
        fn set(pairs: &[(&str, Option<&str>)]) -> Self {
            let saved = pairs
                .iter()
                .map(|(k, _)| (k.to_string(), std::env::var(k).ok()))
                .collect();
            for (k, v) in pairs {
                match v {
                    Some(v) => std::env::set_var(k, v),
                    None => std::env::remove_var(k),
                }
            }
            EnvGuard(saved)
        }
    }
    impl Drop for EnvGuard {
        fn drop(&mut self) {
            for (k, v) in &self.0 {
                match v {
                    Some(v) => std::env::set_var(k, v),
                    None => std::env::remove_var(k),
                }
            }
        }
    }

    #[test]
    fn socket_rules() {
        let _l = ENV.lock().unwrap();
        {
            let _g = EnvGuard::set(&[
                ("BNM_SOCKET", None),
                ("BNM_RUNTIME_DIR", None),
                ("XDG_RUNTIME_DIR", Some("/run/user/1")),
            ]);
            assert_eq!(socket_path(), PathBuf::from("/run/user/1/bnm/bnmd.sock"));
            assert_eq!(
                pid_file(&socket_path()),
                PathBuf::from("/run/user/1/bnm/bnmd.pid")
            );
        }
        {
            let _g = EnvGuard::set(&[
                ("BNM_SOCKET", None),
                ("BNM_RUNTIME_DIR", Some("/x")),
                ("XDG_RUNTIME_DIR", Some("/run/user/1")),
            ]);
            assert_eq!(socket_path(), PathBuf::from("/x/bnmd.sock"));
        }
        {
            let _g = EnvGuard::set(&[
                ("BNM_SOCKET", None),
                ("BNM_RUNTIME_DIR", None),
                ("XDG_RUNTIME_DIR", None),
            ]);
            let uid = unsafe { libc::getuid() };
            assert_eq!(
                socket_path(),
                std::env::temp_dir()
                    .join(format!("bnm-{uid}"))
                    .join("bnmd.sock")
            );
        }
        {
            let _g = EnvGuard::set(&[("BNM_SOCKET", Some("/s"))]);
            assert_eq!(socket_path(), PathBuf::from("/s"));
        }
    }

    #[test]
    fn xdg_dirs() {
        let _l = ENV.lock().unwrap();
        let _g = EnvGuard::set(&[
            ("HOME", Some("/home/u")),
            ("XDG_STATE_HOME", None),
            ("XDG_CONFIG_HOME", None),
        ]);
        assert_eq!(state_dir(), PathBuf::from("/home/u/.local/state/bnm"));
        assert_eq!(
            log_path(),
            PathBuf::from("/home/u/.local/state/bnm/bnmd.log")
        );
        assert_eq!(
            unit_path(),
            PathBuf::from("/home/u/.config/systemd/user/bnmd.service")
        );
        let _g2 = EnvGuard::set(&[
            ("XDG_STATE_HOME", Some("/st")),
            ("XDG_CONFIG_HOME", Some("/cf")),
        ]);
        assert_eq!(state_dir(), PathBuf::from("/st/bnm"));
        assert_eq!(unit_path(), PathBuf::from("/cf/systemd/user/bnmd.service"));
    }

    #[test]
    fn unit_file_points_at_binary() {
        let u = unit_file(Path::new("/opt/bnm/bnmd"));
        assert!(u.contains("ExecStart=/opt/bnm/bnmd\n"));
        assert!(u.contains("WantedBy=default.target"));
    }

    #[test]
    fn error_strings_match_contract() {
        assert_eq!(
            DaemonError::Unreachable("x".into()).to_string(),
            "daemon-unreachable: x"
        );
        assert_eq!(
            DaemonError::ApiMismatch { got: 2, want: 1 }.to_string(),
            "api-mismatch: daemon speaks v2, app needs v1"
        );
    }

    #[test]
    fn find_daemon_prefers_env() {
        let _l = ENV.lock().unwrap();
        let _g = EnvGuard::set(&[("BNM_DAEMON", Some("/nowhere/bnmd"))]);
        assert_eq!(find_daemon().unwrap(), PathBuf::from("/nowhere/bnmd"));
    }

    #[test]
    fn read_pid_ignores_dead_and_garbage() {
        let dir = tempfile::tempdir().unwrap();
        let sock = dir.path().join("bnmd.sock");
        assert_eq!(read_pid(&sock), None);
        std::fs::write(dir.path().join(LOCK_NAME), "nope").unwrap();
        assert_eq!(read_pid(&sock), None);
        std::fs::write(
            dir.path().join(LOCK_NAME),
            format!("{}\n", std::process::id()),
        )
        .unwrap();
        assert_eq!(read_pid(&sock), Some(std::process::id()));
    }
}
