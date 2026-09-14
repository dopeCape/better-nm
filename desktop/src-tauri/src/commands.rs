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
    let meta = std::fs::metadata(&path).map_err(|e| format!("{}: {e}", path.display()))?;
    if meta.len() > MAX_VPN_FILE {
        return Err(format!("{} is larger than 1 MiB", path.display()));
    }
    let content = std::fs::read_to_string(&path).map_err(|e| format!("{}: {e}", path.display()))?;
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

#[tauri::command]
pub async fn notify(app: AppHandle, title: String, body: Option<String>) -> Result<(), String> {
    let mut b = app.notification().builder().title(title);
    if let Some(body) = body {
        b = b.body(body);
    }
    b.show().map_err(|e| e.to_string())
}
