//! bnm desktop, the Tauri 2 shell. It owns the OS side of `desktop/CONTRACT.md`:
//! the Unix socket to bnmd (`daemon`), the event and speed streams (`stream`),
//! the YAML config file (`config`), the tray (`tray`) and the `invoke` commands
//! (`commands`). The React frontend in `desktop/src` owns everything visible.
//!
//! Unit tests live next to each module; `tests/daemon_live.rs` drives the client
//! and stream reader against a real `bnmd --fake`.

pub mod commands;
pub mod config;
pub mod daemon;
pub mod stream;
pub mod tray;

use std::sync::{Arc, Mutex};

use serde_json::Value;
use tauri::{AppHandle, Emitter, Manager, WindowEvent};
use tracing::{info, warn};
use tracing_subscriber::EnvFilter;

use config::ConfigStore;
use daemon::Client;
use stream::{Sink, SpeedRunner, StreamRunner};

/// The window label from `tauri.conf.json`.
pub const MAIN_WINDOW: &str = "main";

/// Everything the commands and the tray share.
pub struct AppState {
    pub client: Arc<Client>,
    pub stream: StreamRunner,
    pub speed: SpeedRunner,
    pub config: ConfigStore,
    pub tray: Mutex<Option<tray::Tray>>,
    watcher: Mutex<Option<notify::RecommendedWatcher>>,
}

impl AppState {
    fn new(app: AppHandle) -> Self {
        let client = Arc::new(Client::new(daemon::socket_path()));
        let sink: Arc<dyn Sink> = Arc::new(app);
        AppState {
            stream: StreamRunner::new(client.clone(), sink.clone()),
            speed: SpeedRunner::new(client.clone(), sink),
            client,
            config: ConfigStore::open(config::default_path()),
            tray: Mutex::new(None),
            watcher: Mutex::new(None),
        }
    }
}

impl Sink for AppHandle {
    fn emit(&self, event: &str, payload: Value) {
        if let Err(e) = Emitter::emit(self, event, payload) {
            warn!(event, error = %e, "emit failed");
        }
    }
}

/// Shows, unminimises and focuses the main window.
pub fn show_main_window(app: &AppHandle) {
    if let Some(w) = app.get_webview_window(MAIN_WINDOW) {
        let _ = w.show();
        let _ = w.unminimize();
        let _ = w.set_focus();
    }
}

fn init_tracing() {
    let filter =
        EnvFilter::try_from_env("BNM_DESKTOP_LOG").unwrap_or_else(|_| EnvFilter::new("info"));
    let _ = tracing_subscriber::fmt()
        .with_env_filter(filter)
        .with_ansi(std::io::IsTerminal::is_terminal(&std::io::stderr()))
        .with_writer(std::io::stderr)
        .with_target(false)
        .try_init();
}

pub fn run() {
    init_tracing();
    info!(version = env!("CARGO_PKG_VERSION"), "bnm desktop starting");
    daemon::log_socket_discovery();

    tauri::Builder::default()
        .plugin(tauri_plugin_single_instance::init(|app, argv, _cwd| {
            info!(?argv, "second instance; focusing the window");
            show_main_window(app);
        }))
        .plugin(tauri_plugin_window_state::Builder::new().build())
        .plugin(tauri_plugin_dialog::init())
        .plugin(tauri_plugin_opener::init())
        .plugin(tauri_plugin_notification::init())
        .invoke_handler(tauri::generate_handler![
            commands::api_request,
            commands::stream_start,
            commands::stream_stop,
            commands::speed_start,
            commands::speed_cancel,
            commands::config_get,
            commands::config_set,
            commands::config_path,
            commands::pick_vpn_file,
            commands::open_url,
            commands::daemon_status,
            commands::daemon_install,
            commands::daemon_restart,
            commands::window_show,
            commands::notify,
        ])
        .setup(|app| {
            let handle = app.handle().clone();
            app.manage(AppState::new(handle.clone()));
            let state = handle.state::<AppState>();
            let cfg = state.config.get();
            info!(path = %state.config.path().display(), theme = %cfg.theme, tray = %cfg.tray, "config loaded");

            // Config watcher: parent directory, 150 ms debounce, emits bnm://config.
            {
                let h = handle.clone();
                match config::watch(state.config.path().to_path_buf(), move || {
                    let st = h.state::<AppState>();
                    let cfg = st.config.reload();
                    info!(theme = %cfg.theme, density = %cfg.density, errors = cfg.errors.len(), "config changed");
                    match serde_json::to_value(&cfg) {
                        Ok(v) => Sink::emit(&h, stream::EV_CONFIG, v),
                        Err(e) => warn!(error = %e, "config: encode"),
                    }
                    tray::apply_setting(&h, &cfg.tray);
                }) {
                    Ok(w) => {
                        if let Ok(mut g) = state.watcher.lock() {
                            *g = Some(w);
                        }
                    }
                    Err(e) => warn!(error = %e, "config watcher not started"),
                }
            }

            // Tray, per config and the presence of a StatusNotifier host.
            tray::apply_setting(&handle, &cfg.tray);

            // The event stream runs for the life of the app; stream_start is idempotent.
            let h = handle.clone();
            tauri::async_runtime::spawn(async move {
                let st = h.state::<AppState>();
                st.stream.start().await;
            });
            let h = handle.clone();
            tauri::async_runtime::spawn(async move {
                let mut rx = h.state::<AppState>().stream.subscribe();
                loop {
                    match rx.recv().await {
                        Ok(c) => {
                            if matches!(c.kind.as_str(), "status" | "wifi" | "vpn" | "monitor" | "stream") {
                                // Coalesce a burst before hitting the API.
                                tokio::time::sleep(std::time::Duration::from_millis(250)).await;
                                while rx.try_recv().is_ok() {}
                                tray::refresh(&h).await;
                            }
                        }
                        Err(tokio::sync::broadcast::error::RecvError::Lagged(_)) => tray::refresh(&h).await,
                        Err(tokio::sync::broadcast::error::RecvError::Closed) => break,
                    }
                }
            });
            Ok(())
        })
        .on_window_event(|window, event| {
            if let WindowEvent::CloseRequested { api, .. } = event {
                let app = window.app_handle();
                let state = app.state::<AppState>();
                let has_tray = state.tray.lock().map(|t| t.is_some()).unwrap_or(false);
                if has_tray && state.config.get().close_to_tray {
                    info!("close requested; hiding to tray");
                    api.prevent_close();
                    let _ = window.hide();
                }
            }
        })
        .run(tauri::generate_context!())
        .expect("error while running bnm desktop");
}
