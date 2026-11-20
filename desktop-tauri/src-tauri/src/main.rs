#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

use std::env;
use std::fs;
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use anyhow::{Context, Result};
use lazy_static::lazy_static;
use log::*;
use once_cell::sync::Lazy;
use serde::{Deserialize, Serialize};
use tauri::Manager;
use tauri_plugin_positioner::PositionExt;

// Modules
mod notifications;
mod proxy;
mod server;

// Global state for the Go backend process
static GO_BACKEND: Lazy<Arc<Mutex<Option<Child>>>> = Lazy::new(|| Arc::new(Mutex::new(None)));
static BACKEND_PORT: Lazy<Arc<Mutex<u16>>> = Lazy::new(|| Arc::new(Mutex::new(8090)));
static BACKEND_READY: Lazy<Arc<Mutex<bool>>> = Lazy::new(|| Arc::new(Mutex::new(false)));
static DATA_DIR: Lazy<Arc<Mutex<PathBuf>>> = Lazy::new(|| Arc::new(Mutex::new(PathBuf::new())));

lazy_static! {
    static ref LOGGED: std::sync::atomic::AtomicBool = std::sync::atomic::AtomicBool::new(false);
}

/// Configuration for the LoopForge backend
#[derive(Debug, Clone, Serialize, Deserialize)]
struct BackendConfig {
    host: String,
    port: u16,
    use_embedded: bool,
}

impl Default for BackendConfig {
    fn default() -> Self {
        Self {
            host: "127.0.0.1".to_string(),
            port: 8090,
            use_embedded: true,
        }
    }
}

/// Start the Go backend server
fn start_go_backend(config: &BackendConfig) -> Result<Child> {
    info!("Starting Go backend server on port {}", config.port);
    
    // Check if we're in dev mode
    let is_dev = env::var("TAURI_ENV_DEV").is_ok();
    
    if is_dev {
        info!("Running in development mode - backend should be started separately");
        // In dev mode, return a dummy process
        return Ok(Command::new("echo")
            .arg("Dev mode - backend running separately")
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn()?);
    }
    
    // Find the Go binary
    let go_bin = find_backend_binary();
    info!("Using backend binary: {:?}", go_bin);
    
    // Get data directory
    let data_dir = get_data_directory();
    if !data_dir.exists() {
        fs::create_dir_all(&data_dir)?;
        info!("Created data directory: {:?}", data_dir);
    }
    
    // Update global data directory
    let mut dir_guard = DATA_DIR.lock().unwrap();
    *dir_guard = data_dir.clone();
    drop(dir_guard);
    
    // Build the command
    let mut cmd = Command::new(go_bin);
    
    // Set environment variables
    cmd.env("LOOPFORGE_HOST", &config.host);
    cmd.env("LOOPFORGE_PORT", config.port.to_string());
    cmd.env("LOOPFORGE_DATA_DIR", data_dir.to_string_lossy());
    cmd.env("LOOPFORGE_ENV", "production");
    cmd.env("LOOPFORGE_DISABLE_UPDATE_CHECK", "true");
    
    // In production, run in server mode
    cmd.arg("server");
    
    // Set up stdio - capture output for logging
    cmd.stdout(Stdio::piped());
    cmd.stderr(Stdio::piped());
    
    info!("Spawning Go backend process...");
    let mut child = cmd.spawn().with_context(|| {
        format!("Failed to spawn Go backend: {:?}", go_bin)
    })?;
    
    // Spawn a thread to monitor backend output
    if let Some(stdout) = child.stdout.take() {
        let backend_port = config.port;
        std::thread::spawn(move || {
            use std::io::BufRead;
            let reader = std::io::BufReader::new(stdout);
            for line in reader.lines() {
                if let Ok(line) = line {
                    info!("Backend stdout: {}", line);
                    // Check if backend is ready
                    if line.contains("server started") || line.contains("listening on") || line.contains("HTTP server") {
                        let mut ready = BACKEND_READY.lock().unwrap();
                        *ready = true;
                        info!("Backend is ready!");
                    }
                }
            }
        });
    }
    
    if let Some(stderr) = child.stderr.take() {
        std::thread::spawn(move || {
            use std::io::BufRead;
            let reader = std::io::BufReader::new(stderr);
            for line in reader.lines() {
                if let Ok(line) = line {
                    warn!("Backend stderr: {}", line);
                }
            }
        });
    }
    
    // Wait for backend to be ready (up to 15 seconds)
    info!("Waiting for backend to start...");
    let start_time = std::time::Instant::now();
    let mut is_ready = false;
    
    while start_time.elapsed() < Duration::from_secs(15) {
        {
            let ready = BACKEND_READY.lock().unwrap();
            if *ready {
                is_ready = true;
                break;
            }
        }
        std::thread::sleep(Duration::from_millis(100));
        
        // Check if process has exited
        if let Ok(status) = child.try_wait() {
            if let Some(exit_status) = status {
                error!("Backend exited with status: {:?}", exit_status);
                return Err(anyhow::anyhow!(
                    "Go backend failed to start with exit status: {:?}",
                    exit_status
                ));
            }
        }
    }
    
    if !is_ready {
        warn!("Backend may not be fully ready yet");
    }
    
    info!("Go backend started successfully on port {}", config.port);
    Ok(child)
}

/// Find the Go backend binary
fn find_backend_binary() -> PathBuf {
    // Try multiple locations
    let mut path = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    path.pop(); // src-tauri
    
    // Try in desktop-tauri/bin/
    let bin_path = path.join("bin/loopforge-backend");
    if bin_path.exists() {
        if cfg!(windows) {
            bin_path.set_extension("exe");
            if bin_path.exists() {
                return bin_path;
            }
        }
        return bin_path;
    }
    
    // Try in src-tauri/
    let src_tauri_path = path.join("src-tauri/loopforge-backend");
    if src_tauri_path.exists() {
        if cfg!(windows) {
            src_tauri_path.set_extension("exe");
            if src_tauri_path.exists() {
                return src_tauri_path;
            }
        }
        return src_tauri_path;
    }
    
    // Try in project root
    path.pop(); // desktop-tauri
    let root_path = path.join("loopforge-backend");
    if root_path.exists() {
        if cfg!(windows) {
            root_path.set_extension("exe");
            if root_path.exists() {
                return root_path;
            }
        }
        return root_path;
    }
    
    // Try in cmd/agentflow
    let agentflow_path = path.join("cmd/agentflow/agentflow");
    if agentflow_path.exists() {
        if cfg!(windows) {
            agentflow_path.set_extension("exe");
            if agentflow_path.exists() {
                return agentflow_path;
            }
        }
        return agentflow_path;
    }
    
    // Default fallback
    if cfg!(windows) {
        PathBuf::from("loopforge-backend.exe")
    } else {
        PathBuf::from("loopforge-backend")
    }
}

/// Get or create data directory
fn get_data_directory() -> PathBuf {
    // Try to use user's home directory
    if let Some(home) = dirs::home_dir() {
        return home.join("LoopForge").join("data");
    }
    
    // Fallback to current directory
    PathBuf::from("loopforge-data")
}

/// Stop the Go backend server
fn stop_go_backend() -> Result<()> {
    info!("Stopping Go backend server...");
    let mut guard = GO_BACKEND.lock().unwrap();
    if let Some(mut child) = guard.take() {
        // Try graceful shutdown first
        if let Err(e) = child.kill() {
            warn!("Failed to kill Go backend gracefully: {}", e);
        }
        
        // Wait for it to exit
        if let Err(e) = child.wait() {
            warn!("Go backend did not exit cleanly: {}", e);
        } else {
            info!("Go backend stopped successfully");
        }
    }
    
    // Reset ready flag
    let mut ready = BACKEND_READY.lock().unwrap();
    *ready = false;
    
    Ok(())
}

/// Check if the backend is running
fn is_backend_running() -> bool {
    let guard = GO_BACKEND.lock().unwrap();
    guard.is_some()
}

/// Check if the backend is ready
fn is_backend_ready() -> bool {
    let ready = BACKEND_READY.lock().unwrap();
    *ready
}

/// Get the backend URL
fn get_backend_url() -> String {
    let config = BackendConfig::default();
    format!("http://{}:{}", config.host, config.port)
}

/// Get the web app URL
fn get_web_app_url() -> String {
    get_backend_url()
}

/// Get the data directory
fn get_data_dir() -> String {
    let dir = DATA_DIR.lock().unwrap();
    dir.to_string_lossy().into_owned()
}

// Tauri commands for the frontend

/// Start the backend server
#[tauri::command]
fn start_backend() -> Result<bool, String> {
    let config = BackendConfig::default();
    let mut guard = GO_BACKEND.lock().unwrap();
    
    if guard.is_some() {
        return Ok(true); // Already running
    }
    
    match start_go_backend(&config) {
        Ok(child) => {
            *guard = Some(child);
            let port = config.port;
            *BACKEND_PORT.lock().unwrap() = port;
            info!("Backend started on port {}", port);
            Ok(true)
        }
        Err(e) => {
            Err(format!("Failed to start backend: {}", e))
        }
    }
}

/// Stop the backend server
#[tauri::command]
fn stop_backend() -> Result<bool, String> {
    match stop_go_backend() {
        Ok(_) => Ok(true),
        Err(e) => Err(format!("Failed to stop backend: {}", e)),
    }
}

/// Restart the backend server
#[tauri::command]
fn restart_backend() -> Result<bool, String> {
    stop_backend()?;
    std::thread::sleep(Duration::from_secs(2));
    start_backend()
}

/// Get backend status
#[tauri::command]
fn get_backend_status() -> Result<bool, String> {
    Ok(is_backend_running() && is_backend_ready())
}

/// Get backend URL
#[tauri::command]
fn get_backend_url_cmd() -> Result<String, String> {
    Ok(get_backend_url())
}

/// Get web app URL
#[tauri::command]
fn get_web_app_url_cmd() -> Result<String, String> {
    Ok(get_web_app_url())
}

/// Get data directory
#[tauri::command]
fn get_data_directory() -> Result<String, String> {
    Ok(get_data_dir())
}

/// Open external URL
#[tauri::command]
fn open_url(url: String) -> Result<(), String> {
    use tauri::api::shell::open;
    open(&tauri::api::shell::Scope::Default, &url, None)
        .map_err(|e| format!("Failed to open URL: {}", e))?;
    Ok(())
}

/// Get app version
#[tauri::command]
fn get_app_version() -> Result<String, String> {
    Ok(env!("CARGO_PKG_VERSION").to_string())
}

/// Get platform info
#[tauri::command]
fn get_platform_info() -> Result<String, String> {
    Ok(format!(
        "{}-{}",
        env::consts::OS,
        env::consts::ARCH
    ))
}

/// Check if Docker is running
#[tauri::command]
fn check_docker_running() -> Result<bool, String> {
    Command::new(if cfg!(windows) { "docker.exe" } else { "docker" })
        .arg("info")
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .status()
        .map(|status| status.success())
        .map_err(|e| format!("Failed to check Docker: {}", e))
}

/// Get backend logs
#[tauri::command]
fn get_backend_logs() -> Result<String, String> {
    // For debugging, we could capture logs
    Ok("Backend logs not available".to_string())
}

/// Initialize logging
fn setup_logging() {
    if LOGGED.compare_exchange(false, true, std::sync::atomic::Ordering::SeqCst).is_ok() {
        std::env::set_var("RUST_LOG", "info");
    }
}

fn main() {
    setup_logging();
    
    info!("Starting LoopForge Desktop...");
    
    // Build the Tauri application
    tauri::Builder::default()
        // Register plugins
        .plugin(tauri_plugin_positioner::init())
        
        // Setup window
        .setup(|app| {
            info!("Setting up Tauri application...");
            
            // Get the main window
            let window = app.get_webview_window("main").ok();
            
            // Start backend in production mode
            if !env::var("TAURI_ENV_DEV").is_ok() {
                info!("Starting backend in production mode...");
                let config = BackendConfig::default();
                
                // Store config for later use
                let _ = app.manage(config);
                
                // Start backend in background
                if let Ok(mut child) = start_go_backend(&config) {
                    let mut guard = GO_BACKEND.lock().unwrap();
                    *guard = Some(child);
                    let port = config.port;
                    *BACKEND_PORT.lock().unwrap() = port;
                    info!("Backend started on port {} in production mode", port);
                } else {
                    warn!("Failed to start backend in production mode");
                }
            } else {
                info!("Running in development mode - backend should be started separately");
            }
            
            Ok(())
        })
        
        // Register custom protocol handler
        .register_uri_scheme_protocol("loopforge", move |app, request| {
            info!("Handling loopforge:// URI: {:?}", request);
            // Extract the path from the URI
            let path = request.uri().path();
            let query = request.uri().query().unwrap_or("");
            
            // Route to the appropriate handler
            let backend_url = get_backend_url();
            
            Err(tauri::Error::UriSchemeRequestFailed(format!(
                "Not implemented: {}{}",
                backend_url,
                path
            )))
        })
        
        // Register commands
        .invoke_handler(tauri::generate_handler![
            start_backend,
            stop_backend,
            restart_backend,
            get_backend_status,
            get_backend_url_cmd,
            get_web_app_url_cmd,
            get_data_directory,
            open_url,
            get_app_version,
            get_platform_info,
            check_docker_running,
            get_backend_logs,
            notifications::show_notification,
            notifications::show_notification_with_options,
            notifications::request_notification_permission,
        ])
        
        // On window close, stop the backend
        .on_window_event(|window, event| {
            if let tauri::WindowEvent::CloseRequested { api, .. } = event {
                info!("Window close requested - stopping backend...");
                let _ = stop_go_backend();
                // Don't prevent the close
                api.prevent_close();
            }
        })
        
        // Handle uncaught errors
        .on_page_load(|window, _| {
            info!("Page loaded: {}", window.label());
        })
        
        .run(tauri::generate_context!())
        .expect("Failed to run Tauri application");
}
