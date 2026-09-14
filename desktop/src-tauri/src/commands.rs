//! Every `invoke` command of `desktop/CONTRACT.md`, one function each. Errors are
//! the strings the contract names (`daemon-unreachable: ...`, `api-mismatch: ...`,
//! `speed-running`); everything else rejects with a plain message.

use serde::Serialize;
use serde_json::Value;
use tauri::{AppHandle, State};
use tauri_plugin_dialog::DialogExt;
use tauri_plugin_notification::NotificationExt;
use tauri_plugin_opener::OpenerExt;
use tracing::{debug, info};

use crate::config::DesktopConfig;
use crate::daemon::DaemonStatus;
use crate::stream::{Sink, EV_CONFIG};
use crate::AppState;

/// `api_request`'s answer.
#[derive(Debug, Serialize)]
pub struct ApiResponse {
    pub status: u16,
    pub body: String,
}

/// `pick_vpn_file`'s answer.
#[derive(Debug, Serialize)]
pub struct PickedFile {
    pub name: String,
    pub content: String,
}

const MAX_VPN_FILE: u64 = 1 << 20;

/// Reads a picked profile: regular files only (a FIFO or device would block the
/// command forever), at most 1 MiB, valid UTF-8. `metadata` follows symlinks, so a
/// link to a real file is fine.
pub fn read_vpn_file(path: &std::path::Path) -> Result<String, String> {
    use std::io::Read;
    let meta = std::fs::metadata(path).map_err(|e| format!("{}: {e}", path.display()))?;
    if !meta.is_file() {
        return Err(format!("{} is not a regular file", path.display()));
    }
    if meta.len() > MAX_VPN_FILE {
        return Err(format!("{} is larger than 1 MiB", path.display()));
    }
    let f = std::fs::File::open(path).map_err(|e| format!("{}: {e}", path.display()))?;
    let mut buf = Vec::new();
    // The size may have grown since `metadata`; never read past the limit.
    f.take(MAX_VPN_FILE + 1)
        .read_to_end(&mut buf)
        .map_err(|e| format!("{}: {e}", path.display()))?;
    if buf.len() as u64 > MAX_VPN_FILE {
        return Err(format!("{} is larger than 1 MiB", path.display()));
    }
    String::from_utf8(buf).map_err(|_| format!("{} is not UTF-8 text", path.display()))
}

#[tauri::command]
pub async fn api_request(
    state: State<'_, AppState>,
    method: String,
    path: String,
    body: Option<Value>,
) -> Result<ApiResponse, String> {
    let method = method.to_ascii_uppercase();
    if !matches!(method.as_str(), "GET" | "POST" | "PUT" | "DELETE") {
        return Err(format!("unsupported method {method}"));
    }
    if !path.starts_with("/v1/") && path != "/v1" {
        return Err(format!("path must start with /v1: {path}"));
    }
    let encoded = match body {
        None | Some(Value::Null) => None,
        Some(v) => Some(v.to_string()),
    };
    let (status, body) = state
        .client
        .request(&method, &path, encoded.as_deref())
        .await
        .map_err(|e| e.to_string())?;
    debug!(%method, %path, status, "api");
    Ok(ApiResponse { status, body })
}

#[tauri::command]
pub async fn stream_start(state: State<'_, AppState>) -> Result<(), String> {
    state.stream.start().await;
    Ok(())
}

#[tauri::command]
pub async fn stream_stop(state: State<'_, AppState>) -> Result<(), String> {
    state.stream.stop().await;
    Ok(())
}

#[tauri::command]
pub async fn speed_start(state: State<'_, AppState>, opts: Option<Value>) -> Result<(), String> {
    let opts = match opts {
        Some(v @ Value::Object(_)) => v,
        None | Some(Value::Null) => Value::Object(Default::default()),
        Some(_) => return Err("opts must be an object".into()),
    };
    state.speed.start(opts).await
}

#[tauri::command]
pub async fn speed_cancel(state: State<'_, AppState>) -> Result<(), String> {
    state.speed.cancel().await;
    Ok(())
}

#[tauri::command]
pub async fn config_get(
    app: AppHandle,
    state: State<'_, AppState>,
) -> Result<DesktopConfig, String> {
    let cfg = state.config.get();
    app.emit(
        EV_CONFIG,
        serde_json::to_value(&cfg).map_err(|e| e.to_string())?,
    );
    Ok(cfg)
}

#[tauri::command]
pub async fn config_set(
    app: AppHandle,
    state: State<'_, AppState>,
    patch: Value,
) -> Result<DesktopConfig, String> {
    let cfg = state.config.set(&patch)?;
    info!(keys = ?patch.as_object().map(|o| o.keys().cloned().collect::<Vec<_>>()), "config_set");
    app.emit(
        EV_CONFIG,
        serde_json::to_value(&cfg).map_err(|e| e.to_string())?,
    );
    crate::tray::apply_setting(&app, &cfg.tray);
    Ok(cfg)
}

#[tauri::command]
pub async fn config_path(state: State<'_, AppState>) -> Result<String, String> {
    Ok(state.config.path().display().to_string())
}

#[tauri::command]
pub async fn pick_vpn_file(app: AppHandle) -> Result<Option<PickedFile>, String> {
    let picked = app
        .dialog()
        .file()
        .set_title("Import VPN profile")
        .add_filter("VPN profiles", &["conf", "ovpn"])
        .blocking_pick_file();
    let Some(fp) = picked else {
        return Ok(None);
    };
    let path = fp.into_path().map_err(|e| e.to_string())?;
    let content = read_vpn_file(&path)?;
    let name = path
        .file_name()
        .map(|s| s.to_string_lossy().into_owned())
        .unwrap_or_default();
    Ok(Some(PickedFile { name, content }))
}

#[tauri::command]
pub async fn open_url(app: AppHandle, url: String) -> Result<(), String> {
    if !(url.starts_with("http://") || url.starts_with("https://")) {
        return Err(format!("refusing to open non-http URL {url}"));
    }
    app.opener()
        .open_url(&url, None::<&str>)
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn daemon_status(state: State<'_, AppState>) -> Result<DaemonStatus, String> {
    Ok(state.client.status().await)
}

#[tauri::command]
pub async fn daemon_install(state: State<'_, AppState>) -> Result<(), String> {
    state.client.install().await
}

#[tauri::command]
pub async fn daemon_restart(state: State<'_, AppState>) -> Result<(), String> {
    state.client.restart().await
}

#[tauri::command]
pub async fn window_show(app: AppHandle) -> Result<(), String> {
    crate::show_main_window(&app);
    Ok(())
}

/// Hides the window to the tray. Rejects with `no-tray` when there is no tray to
/// bring it back from (the frontend offers the action only when `tray_present`).
#[tauri::command]
pub async fn window_hide(app: AppHandle) -> Result<(), String> {
    if !crate::tray::present(&app) {
        return Err("no-tray".into());
    }
    info!("hide to tray (frontend)");
    crate::hide_main_window(&app);
    Ok(())
}

/// Quits the app. The window is frameless, so the palette's "Quit bnm" is the
/// close button; it ignores `close_to_tray` on purpose (that is `window_hide`).
#[tauri::command]
pub async fn window_close(app: AppHandle) -> Result<(), String> {
    info!("quit (frontend)");
    app.exit(0);
    Ok(())
}

#[tauri::command]
pub async fn tray_present(app: AppHandle) -> Result<bool, String> {
    Ok(crate::tray::present(&app))
}

/// Opens the directory holding the desktop config file in the file manager.
#[tauri::command]
pub async fn config_reveal(app: AppHandle, state: State<'_, AppState>) -> Result<(), String> {
    let dir = state
        .config
        .path()
        .parent()
        .map(|p| p.to_path_buf())
        .ok_or_else(|| "config path has no parent".to_string())?;
    app.opener()
        .open_path(dir.display().to_string(), None::<&str>)
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn notify(app: AppHandle, title: String, body: Option<String>) -> Result<(), String> {
    let mut b = app.notification().builder().title(title);
    if let Some(body) = body {
        b = b.body(body);
    }
    b.show().map_err(|e| e.to_string())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn vpn_file_rules() {
        let dir = tempfile::tempdir().unwrap();
        let ok = dir.path().join("a.conf");
        std::fs::write(&ok, "[Interface]\n").unwrap();
        assert_eq!(read_vpn_file(&ok).unwrap(), "[Interface]\n");
        // A symlink to a regular file resolves.
        let link = dir.path().join("link.conf");
        std::os::unix::fs::symlink(&ok, &link).unwrap();
        assert_eq!(read_vpn_file(&link).unwrap(), "[Interface]\n");
        // Directories and non-files are refused.
        assert!(read_vpn_file(dir.path())
            .unwrap_err()
            .contains("not a regular file"));
        assert!(read_vpn_file(&dir.path().join("missing")).is_err());
        // Over the limit.
        let big = dir.path().join("big.ovpn");
        std::fs::write(&big, vec![b'x'; (MAX_VPN_FILE + 1) as usize]).unwrap();
        assert!(read_vpn_file(&big)
            .unwrap_err()
            .contains("larger than 1 MiB"));
        // Not text.
        let bin = dir.path().join("bin.conf");
        std::fs::write(&bin, [0xff, 0xfe, 0x00]).unwrap();
        assert!(read_vpn_file(&bin).unwrap_err().contains("not UTF-8"));
    }
}
