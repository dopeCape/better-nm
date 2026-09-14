//! The StatusNotifier tray: built only when a host owns
//! `org.kde.StatusNotifierWatcher` on the session bus (or `tray: on`), refreshed
//! from change hints, acting through the daemon client. The menu model
//! (`TrayModel::from_api`) is pure over the `/v1/status`, `/v1/vpn` and
//! `/v1/monitor` JSON and tested below; the Tauri wiring is exercised live.

use std::sync::Mutex;

use serde_json::Value;
use tauri::image::Image;
use tauri::menu::{CheckMenuItem, Menu, MenuItem, PredefinedMenuItem};
use tauri::tray::{TrayIcon, TrayIconBuilder};
use tauri::{AppHandle, Manager};
use tracing::{debug, info, warn};

use crate::AppState;

pub const TRAY_ID: &str = "bnm";

/// One VPN row.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct VpnEntry {
    pub id: String,
    pub name: String,
    pub kind: String,
    pub state: String,
    pub writable: bool,
}

impl VpnEntry {
    pub fn connected(&self) -> bool {
        self.state == "connected"
    }
    pub fn busy(&self) -> bool {
        self.state == "connecting"
    }
    /// The check item is enabled when bnm can drive this VPN.
    pub fn actionable(&self) -> bool {
        self.writable
            && matches!(
                self.state.as_str(),
                "connected" | "disconnected" | "error" | "needs-auth"
            )
    }
    pub fn label(&self) -> String {
        if self.busy() {
            format!("{} (connecting)", self.name)
        } else if self.state == "needs-setup" || self.state == "unavailable" {
            format!("{} ({})", self.name, self.state)
        } else {
            self.name.clone()
        }
    }
}

/// What the menu shows.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct TrayModel {
    pub connection: String,
    pub reachable: bool,
    pub wifi_enabled: bool,
    pub wifi_hardware: bool,
    pub vpns: Vec<VpnEntry>,
    pub quality: String,
}

impl Default for TrayModel {
    fn default() -> Self {
        TrayModel {
            connection: "bnmd unreachable".into(),
            reachable: false,
            wifi_enabled: false,
            wifi_hardware: false,
            vpns: Vec::new(),
            quality: "idle".into(),
        }
    }
}

fn str_of<'a>(v: &'a Value, key: &str) -> &'a str {
    v.get(key).and_then(Value::as_str).unwrap_or("")
}

impl TrayModel {
    /// Builds the model from the three API answers; `None` means that call failed.
    pub fn from_api(
        status: Option<&Value>,
        vpns: Option<&Value>,
        monitor: Option<&Value>,
    ) -> TrayModel {
        let mut m = TrayModel::default();
        if let Some(st) = status {
            m.reachable = true;
            m.wifi_enabled = st
                .get("wifi_enabled")
                .and_then(Value::as_bool)
                .unwrap_or(false);
            m.wifi_hardware = st
                .get("wifi_hardware")
                .and_then(Value::as_bool)
                .unwrap_or(false);
            m.connection = match st.get("primary").filter(|p| !p.is_null()) {
                Some(p) => {
                    let name = str_of(p, "profile_name");
                    let ip = p
                        .get("ipv4")
                        .and_then(Value::as_array)
                        .and_then(|a| a.first())
                        .and_then(Value::as_str)
                        .map(|s| s.split('/').next().unwrap_or(s))
                        .unwrap_or("");
                    let conn = str_of(st, "connectivity");
                    let mut line = if ip.is_empty() {
                        format!("Connected to {name}")
                    } else {
                        format!("Connected to {name} ({ip})")
                    };
                    if matches!(conn, "portal" | "limited" | "none") {
                        line.push_str(" · no internet");
                    }
                    line
                }
                None => {
                    if !st
                        .get("networking")
                        .and_then(Value::as_bool)
                        .unwrap_or(true)
                    {
                        "Networking off".into()
                    } else {
                        "Not connected".into()
                    }
                }
            };
        }
        if let Some(list) = vpns.and_then(Value::as_array) {
            m.vpns = list
                .iter()
                .filter(|v| !str_of(v, "id").is_empty())
                .map(|v| VpnEntry {
                    id: str_of(v, "id").to_string(),
                    name: {
                        let n = str_of(v, "name");
                        if n.is_empty() {
                            str_of(v, "id").to_string()
                        } else {
                            n.to_string()
                        }
                    },
                    kind: str_of(v, "kind").to_string(),
                    state: str_of(v, "state").to_string(),
                    writable: v.get("writable").and_then(Value::as_bool).unwrap_or(false),
                })
                .collect();
        }
        if let Some(mon) = monitor {
            m.quality = match str_of(mon, "state") {
                "ok" => "within baseline".into(),
                "" => "idle".into(),
                other => other.to_string(),
            };
        }
        m
    }

    pub fn vpn(&self, id: &str) -> Option<&VpnEntry> {
        self.vpns.iter().find(|v| v.id == id)
    }
}

// --- host detection ---------------------------------------------------------------------

/// Whether a StatusNotifier host owns the watcher name on the session bus. Uses
/// `dbus-send`, then `busctl`; with neither installed there is no host as far as
/// we can tell.
pub fn has_status_notifier_host() -> bool {
    const NAMES: [&str; 2] = [
        "org.kde.StatusNotifierWatcher",
        "org.freedesktop.StatusNotifierWatcher",
    ];
    for name in NAMES {
        if let Some(v) = name_has_owner_dbus_send(name) {
            if v {
                return true;
            }
            continue;
        }
        if let Some(v) = name_has_owner_busctl(name) {
            if v {
                return true;
            }
        }
    }
    false
}

fn name_has_owner_dbus_send(name: &str) -> Option<bool> {
    let out = std::process::Command::new("dbus-send")
        .args([
            "--session",
            "--print-reply",
            "--dest=org.freedesktop.DBus",
            "/org/freedesktop/DBus",
            "org.freedesktop.DBus.NameHasOwner",
            &format!("string:{name}"),
        ])
        .output()
        .ok()?;
    if !out.status.success() {
        return None;
    }
    let text = String::from_utf8_lossy(&out.stdout);
    Some(text.contains("boolean true"))
}

fn name_has_owner_busctl(name: &str) -> Option<bool> {
    let out = std::process::Command::new("busctl")
        .args(["--user", "--no-pager", "status", name])
        .output()
        .ok()?;
    Some(out.status.success())
}

/// The `tray:` setting plus the host decide whether a tray is built.
pub fn wanted(setting: &str) -> bool {
    match setting {
        "off" => false,
        "on" => true,
        _ => {
            let host = has_status_notifier_host();
            if !host {
                info!("no StatusNotifier host on the session bus; tray not built (tray: auto)");
            }
            host
        }
    }
}

// --- icon ------------------------------------------------------------------------------

/// A monochrome variant of the app icon: the arcs kept as white with their alpha,
/// the dark tile dropped. Falls back to the app icon itself if decoding fails.
pub fn tray_icon() -> Image<'static> {
    const PNG: &[u8] = include_bytes!("../icons/128x128.png");
    let Ok(img) = Image::from_bytes(PNG) else {
        return Image::from_bytes(PNG).unwrap_or_else(|_| Image::new_owned(vec![0; 4], 1, 1));
    };
    let (w, h) = (img.width(), img.height());
    let src = img.rgba();
    let mut out = Vec::with_capacity(src.len());
    for px in src.as_chunks::<4>().0 {
        let (r, g, b, a) = (px[0] as u32, px[1] as u32, px[2] as u32, px[3]);
        // Perceived brightness; the tile is ~#14182a, the arcs are teal/white.
        let lum = (r * 299 + g * 587 + b * 114) / 1000;
        if a > 0 && lum > 90 {
            out.extend_from_slice(&[0xf0, 0xf2, 0xf5, a]);
        } else {
            out.extend_from_slice(&[0, 0, 0, 0]);
        }
    }
    Image::new_owned(out, w, h)
}

// --- Tauri wiring ------------------------------------------------------------------------

/// The live tray: the icon plus the model behind its menu.
pub struct Tray {
    pub icon: TrayIcon<tauri::Wry>,
    pub model: Mutex<TrayModel>,
}

fn build_menu(app: &AppHandle, m: &TrayModel) -> tauri::Result<Menu<tauri::Wry>> {
    let menu = Menu::new(app)?;
    menu.append(&MenuItem::with_id(
        app,
        "conn",
        &m.connection,
        false,
        None::<&str>,
    )?)?;
    menu.append(&CheckMenuItem::with_id(
        app,
        "wifi",
        if m.wifi_enabled {
            "Wi-Fi on"
        } else {
            "Wi-Fi off"
        },
        m.reachable && m.wifi_hardware,
        m.wifi_enabled,
        None::<&str>,
    )?)?;
    for v in &m.vpns {
        menu.append(&CheckMenuItem::with_id(
            app,
            format!("vpn:{}", v.id),
            v.label(),
            m.reachable && v.actionable(),
            v.connected(),
            None::<&str>,
        )?)?;
    }
    menu.append(&MenuItem::with_id(
        app,
        "quality",
        format!("Quality: {}", m.quality),
        false,
        None::<&str>,
    )?)?;
    menu.append(&PredefinedMenuItem::separator(app)?)?;
    menu.append(&MenuItem::with_id(
        app,
        "open",
        "Open bnm",
        true,
        None::<&str>,
    )?)?;
    menu.append(&MenuItem::with_id(app, "quit", "Quit", true, None::<&str>)?)?;
    Ok(menu)
}

/// Creates the tray icon with an initial (unreachable) menu. Menu events are
/// delivered to `on_menu` through the one global listener `lib.rs` registers at
/// setup: a listener per build would stack up as `tray:` is toggled and fire an
/// action once per past tray (two Wi-Fi toggles cancel out).
pub fn build(app: &AppHandle) -> tauri::Result<Tray> {
    let model = TrayModel::default();
    let menu = build_menu(app, &model)?;
    let icon = TrayIconBuilder::with_id(TRAY_ID)
        .icon(tray_icon())
        .icon_as_template(true)
        .tooltip("bnm")
        .menu(&menu)
        .show_menu_on_left_click(true)
        .build(app)?;
    info!("tray created");
    Ok(Tray {
        icon,
        model: Mutex::new(model),
    })
}

/// Handles a tray menu item by id. Runs on the GTK main thread, so anything that
/// touches the daemon is spawned onto the runtime and the tray lock is taken there.
pub fn on_menu(app: &AppHandle, id: String) {
    match id.as_str() {
        "open" => crate::show_main_window(app),
        "quit" => {
            info!("quit from tray");
            app.exit(0);
        }
        "wifi" => {
            let app = app.clone();
            tauri::async_runtime::spawn(async move {
                let state = app.state::<AppState>();
                let on = state
                    .tray
                    .lock()
                    .ok()
                    .and_then(|t| {
                        t.as_ref()
                            .map(|t| t.model.lock().map(|m| m.wifi_enabled).unwrap_or(false))
                    })
                    .unwrap_or(false);
                let body = serde_json::json!({ "on": !on }).to_string();
                match state
                    .client
                    .request("POST", "/v1/wifi/enabled", Some(&body))
                    .await
                {
                    Ok((status, resp)) if !(200..300).contains(&status) => {
                        warn!(status, resp, "tray: wifi toggle")
                    }
                    Err(e) => warn!(error = %e, "tray: wifi toggle"),
                    _ => {}
                }
                refresh(&app).await;
            });
        }
        other => {
            if let Some(vpn_id) = other.strip_prefix("vpn:") {
                let vpn_id = vpn_id.to_string();
                let app = app.clone();
                tauri::async_runtime::spawn(async move {
                    let state = app.state::<AppState>();
                    let connected = state
                        .tray
                        .lock()
                        .ok()
                        .and_then(|t| {
                            t.as_ref().and_then(|t| {
                                t.model.lock().ok().map(|m| {
                                    m.vpn(&vpn_id).map(VpnEntry::connected).unwrap_or(false)
                                })
                            })
                        })
                        .unwrap_or(false);
                    let action = if connected { "disconnect" } else { "connect" };
                    let path = format!("/v1/vpn/{vpn_id}/{action}");
                    match state.client.request("POST", &path, None).await {
                        Ok((status, resp)) if !(200..300).contains(&status) => {
                            warn!(status, resp, path, "tray: vpn")
                        }
                        Err(e) => warn!(error = %e, path, "tray: vpn"),
                        _ => {}
                    }
                    refresh(&app).await;
                });
            }
        }
    }
}

/// Re-reads status, VPNs and the monitor and rebuilds the menu.
pub async fn refresh(app: &AppHandle) {
    let state = app.state::<AppState>();
    let exists = state.tray.lock().map(|t| t.is_some()).unwrap_or(false);
    if !exists {
        return;
    }
    let client = state.client.clone();
    let (status, vpns, monitor) = tokio::join!(
        client.get_json("/v1/status"),
        client.get_json("/v1/vpn"),
        client.get_json("/v1/monitor")
    );
    for (what, r) in [("status", &status), ("vpn", &vpns), ("monitor", &monitor)] {
        if let Err(e) = r {
            debug!(what, error = %e, "tray refresh");
        }
    }
    let model = TrayModel::from_api(
        status.as_ref().ok(),
        vpns.as_ref().ok(),
        monitor.as_ref().ok(),
    );
    let menu = match build_menu(app, &model) {
        Ok(m) => m,
        Err(e) => {
            warn!(error = %e, "tray: build menu");
            return;
        }
    };
    // `set_menu` and `set_tooltip` block until the GTK main thread runs them. That
    // thread also takes `state.tray` (window close, menu events), so the lock must
    // not be held across the call: clone the handle out, release, then apply.
    let icon = state
        .tray
        .lock()
        .ok()
        .and_then(|g| g.as_ref().map(|t| t.icon.clone()));
    let Some(icon) = icon else {
        return;
    };
    if let Err(e) = icon.set_menu(Some(menu)) {
        warn!(error = %e, "tray: set menu");
    }
    let _ = icon.set_tooltip(Some(&model.connection));
    let guard = state.tray.lock();
    if let Ok(g) = guard {
        if let Some(t) = g.as_ref() {
            if let Ok(mut m) = t.model.lock() {
                *m = model;
            }
        }
    }
}

/// Builds or removes the tray according to `setting` (`auto|on|off`).
pub fn apply_setting(app: &AppHandle, setting: &str) {
    let state = app.state::<AppState>();
    let want = wanted(setting);
    let have = state.tray.lock().map(|t| t.is_some()).unwrap_or(false);
    if want && !have {
        match build(app) {
            Ok(t) => {
                if let Ok(mut g) = state.tray.lock() {
                    *g = Some(t);
                }
                let app = app.clone();
                tauri::async_runtime::spawn(async move { refresh(&app).await });
            }
            Err(e) => warn!(error = %e, "tray: cannot create"),
        }
    } else if !want && have {
        let old = state.tray.lock().ok().and_then(|mut g| g.take());
        // The app's tray registry holds its own handle; dropping ours alone leaves
        // the icon on screen. Remove it there, and let the GTK objects die on the
        // main thread.
        let removed = app.remove_tray_by_id(TRAY_ID);
        let _ = app.run_on_main_thread(move || {
            drop(removed);
            drop(old);
        });
        // A window hidden "to the tray" has no way back once the tray is gone.
        if let Some(w) = app.get_webview_window(crate::MAIN_WINDOW) {
            if !w.is_visible().unwrap_or(true) {
                crate::show_main_window(app);
            }
        }
        info!("tray removed (tray: {setting})");
    }
}

/// Whether a tray icon exists right now (the frontend offers "Hide to tray" only then).
pub fn present(app: &AppHandle) -> bool {
    app.state::<AppState>()
        .tray
        .lock()
        .map(|t| t.is_some())
        .unwrap_or(false)
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn sample_vpn() -> Value {
        json!([
            {"id": "tailscale", "name": "Tailscale", "backend": "tailscale", "kind": "Tailscale", "state": "connected", "writable": true},
            {"id": "6f1c-uuid", "name": "tejas", "backend": "nm-vpn", "kind": "OpenVPN", "state": "disconnected", "writable": true},
            {"id": "wg-uuid", "name": "office", "backend": "wireguard", "kind": "WireGuard", "state": "connecting", "writable": true},
            {"id": "ro", "name": "locked", "backend": "nm-vpn", "kind": "L2TP", "state": "needs-setup", "detail": "x", "writable": false}
        ])
    }

    #[test]
    fn model_from_sample_json() {
        let status = json!({
            "nm_state": "connected-global", "connectivity": "full", "networking": true,
            "wifi_enabled": true, "wifi_hardware": true,
            "primary": {"path": "/x", "profile_name": "ALHN-F832-5", "type": "wifi", "ipv4": ["192.168.1.10/24"]},
            "version": "0.1", "api_version": 1
        });
        let monitor = json!({"network_key": "wifi:ALHN-F832-5", "state": "ok", "anchors": []});
        let m = TrayModel::from_api(Some(&status), Some(&sample_vpn()), Some(&monitor));
        assert!(m.reachable);
        assert_eq!(m.connection, "Connected to ALHN-F832-5 (192.168.1.10)");
        assert!(m.wifi_enabled && m.wifi_hardware);
        assert_eq!(m.quality, "within baseline");
        assert_eq!(m.vpns.len(), 4);
        let ts = m.vpn("tailscale").unwrap();
        assert!(ts.connected() && ts.actionable());
        assert_eq!(ts.label(), "Tailscale");
        let wg = m.vpn("wg-uuid").unwrap();
        assert!(wg.busy() && !wg.actionable());
        assert_eq!(wg.label(), "office (connecting)");
        let ro = m.vpn("ro").unwrap();
        assert!(!ro.actionable());
        assert_eq!(ro.label(), "locked (needs-setup)");
        assert!(m.vpn("6f1c-uuid").unwrap().actionable());
    }

    #[test]
    fn model_when_disconnected_or_unreachable() {
        let status = json!({"networking": true, "wifi_enabled": false, "wifi_hardware": true, "connectivity": "none"});
        let m = TrayModel::from_api(Some(&status), None, Some(&json!({"state": "learning"})));
        assert_eq!(m.connection, "Not connected");
        assert_eq!(m.quality, "learning");
        assert!(m.vpns.is_empty());

        let portal = json!({"networking": true, "connectivity": "portal", "primary": {"profile_name": "Cafe", "ipv4": []}});
        let m = TrayModel::from_api(Some(&portal), None, None);
        assert_eq!(m.connection, "Connected to Cafe · no internet");
        assert_eq!(m.quality, "idle");

        let m = TrayModel::from_api(None, None, None);
        assert!(!m.reachable);
        assert_eq!(m.connection, "bnmd unreachable");
    }

    #[test]
    fn tray_icon_is_monochrome_with_transparent_tile() {
        let img = tray_icon();
        assert_eq!(img.width(), 128);
        let px = img.rgba();
        let opaque: Vec<&[u8; 4]> = px.as_chunks::<4>().0.iter().filter(|p| p[3] > 0).collect();
        assert!(!opaque.is_empty());
        assert!(opaque
            .iter()
            .all(|p| p[0] == 0xf0 && p[1] == 0xf2 && p[2] == 0xf5));
        assert!(
            opaque.len() < px.len() / 4 / 2,
            "the dark tile must be dropped"
        );
    }

    #[test]
    fn wanted_respects_on_off() {
        assert!(wanted("on"));
        assert!(!wanted("off"));
    }
}
