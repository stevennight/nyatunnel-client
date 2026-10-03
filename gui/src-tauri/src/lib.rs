//! NyaTunnel desktop shell: tray, single instance, deep links, autostart, and a token-holding
//! proxy to the bundled Go core. All tunnel logic lives in the core.

mod sidecar;

use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use serde_json::Value;
use tauri::menu::{Menu, MenuItem};
use tauri::tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent};
use tauri::{AppHandle, Emitter, Manager, RunEvent, State, WindowEvent};
use tauri_plugin_autostart::ManagerExt as _;
use tauri_plugin_deep_link::DeepLinkExt;

use crate::sidecar::{Core, CoreError, CoreStatus};

const AUTOSTART_ARG: &str = "--autostart";
const MAX_PENDING_LINKS: usize = 8;

/// Deep links waiting for the web view. They are only parsed and shown there; nothing here acts
/// on them.
#[derive(Default)]
struct PendingLinks {
    urls: Mutex<Vec<String>>,
    last: Mutex<Option<(String, Instant)>>,
}

fn queue_links(app: &AppHandle, urls: Vec<String>) {
    let links = app.state::<PendingLinks>();
    let mut added = Vec::new();
    for url in urls {
        if !url.to_ascii_lowercase().starts_with("nyatunnel://") || url.len() > 2048 {
            continue;
        }
        // macOS can report the launch URL both via get_current() and an open-url event.
        {
            let mut last = links.last.lock().unwrap();
            if matches!(&*last, Some((u, t)) if *u == url && t.elapsed() < Duration::from_secs(2)) {
                continue;
            }
            *last = Some((url.clone(), Instant::now()));
        }
        let mut q = links.urls.lock().unwrap();
        if q.len() >= MAX_PENDING_LINKS {
            q.remove(0);
        }
        q.push(url.clone());
        added.push(url);
    }
    if !added.is_empty() {
        show_main(app);
        let _ = app.emit("deep-link", added);
    }
}

fn show_main(app: &AppHandle) {
    if let Some(w) = app.get_webview_window("main") {
        let _ = w.show();
        let _ = w.unminimize();
        let _ = w.set_focus();
    }
}

#[tauri::command]
async fn core(
    core: State<'_, Arc<Core>>,
    method: String,
    path: String,
    body: Option<Value>,
) -> Result<Value, CoreError> {
    core.request(&method, &path, body).await
}

#[tauri::command]
fn core_status(core: State<'_, Arc<Core>>) -> CoreStatus {
    core.status()
}

#[tauri::command]
fn take_pending_links(links: State<'_, PendingLinks>) -> Vec<String> {
    std::mem::take(&mut *links.urls.lock().unwrap())
}

#[tauri::command]
fn autostart_status(app: AppHandle) -> Result<bool, String> {
    app.autolaunch().is_enabled().map_err(|e| e.to_string())
}

#[tauri::command]
fn autostart_set(app: AppHandle, enabled: bool) -> Result<bool, String> {
    let al = app.autolaunch();
    if enabled { al.enable() } else { al.disable() }.map_err(|e| e.to_string())?;
    al.is_enabled().map_err(|e| e.to_string())
}

#[tauri::command]
fn quit_app(app: AppHandle) {
    app.exit(0);
}

fn build_tray(app: &tauri::App) -> tauri::Result<()> {
    let show = MenuItem::with_id(app, "show", "显示窗口", true, None::<&str>)?;
    let quit = MenuItem::with_id(app, "quit", "退出", true, None::<&str>)?;
    let menu = Menu::with_items(app, &[&show, &quit])?;
    let mut tray = TrayIconBuilder::with_id("main")
        .tooltip("NyaTunnel")
        .menu(&menu)
        .show_menu_on_left_click(false)
        .on_menu_event(|app, event| match event.id().as_ref() {
            "show" => show_main(app),
            "quit" => app.exit(0),
            _ => {}
        })
        .on_tray_icon_event(|tray, event| {
            if let TrayIconEvent::Click {
                button: MouseButton::Left,
                button_state: MouseButtonState::Up,
                ..
            } = event
            {
                show_main(tray.app_handle());
            }
        });
    if let Some(icon) = app.default_window_icon() {
        tray = tray.icon(icon.clone());
    }
    tray.build(app)?;
    Ok(())
}

pub fn run() {
    let supervisor = Core::new();
    let app = tauri::Builder::default()
        // Must be first: a second launch (e.g. a clicked nyatunnel:// link on Windows/Linux)
        // hands its arguments to this instance and exits.
        .plugin(tauri_plugin_single_instance::init(|app, _args, _cwd| {
            show_main(app)
        }))
        .plugin(tauri_plugin_deep_link::init())
        .plugin(tauri_plugin_shell::init())
        .plugin(
            tauri_plugin_opener::Builder::new()
                .open_js_links_on_click(false)
                .build(),
        )
        .plugin(
            tauri_plugin_autostart::Builder::new()
                .arg(AUTOSTART_ARG)
                .build(),
        )
        .manage(supervisor.clone())
        .manage(PendingLinks::default())
        .invoke_handler(tauri::generate_handler![
            core,
            core_status,
            take_pending_links,
            autostart_status,
            autostart_set,
            quit_app
        ])
        .on_window_event(|window, event| {
            // Closing the window keeps the tunnels running in the tray.
            if let WindowEvent::CloseRequested { api, .. } = event {
                if window.label() == "main" {
                    api.prevent_close();
                    let _ = window.hide();
                }
            }
        })
        .setup(move |app| {
            let handle = app.handle().clone();

            #[cfg(any(windows, target_os = "linux"))]
            if let Err(e) = app.deep_link().register_all() {
                eprintln!("[nyatunnel-gui] cannot register nyatunnel:// handler: {e}");
            }
            let h = handle.clone();
            app.deep_link().on_open_url(move |event| {
                queue_links(
                    &h,
                    event.urls().into_iter().map(|u| u.to_string()).collect(),
                );
            });
            if let Ok(Some(urls)) = app.deep_link().get_current() {
                queue_links(&handle, urls.into_iter().map(|u| u.to_string()).collect());
            }

            build_tray(app)?;

            let autostarted = std::env::args().any(|a| a == AUTOSTART_ARG);
            if !autostarted {
                show_main(&handle);
            }

            tauri::async_runtime::spawn(supervisor.clone().supervise(handle));
            Ok(())
        })
        .build(tauri::generate_context!())
        .expect("error while building NyaTunnel");

    app.run(|app, event| match event {
        RunEvent::Exit => app.state::<Arc<Core>>().shutdown(),
        #[cfg(target_os = "macos")]
        RunEvent::Reopen { .. } => show_main(app),
        _ => {}
    });
}
