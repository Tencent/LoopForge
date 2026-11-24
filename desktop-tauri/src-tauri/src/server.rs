//! HTTP Server module for serving web app files
//! This provides a simple file server for the web frontend

use std::fs;
use std::io;
use std::path::{Path, PathBuf};

use tauri::http::header::{CONTENT_TYPE, LOCATION};
use tauri::http::{Response, StatusCode};

/// MIME type mappings for common file extensions
fn get_content_type(path: &Path) -> &str {
    if let Some(ext) = path.extension() {
        match ext.to_string_lossy().to_lowercase().as_str() {
            "html" => "text/html; charset=utf-8",
            "css" => "text/css; charset=utf-8",
            "js" => "application/javascript; charset=utf-8",
            "json" => "application/json; charset=utf-8",
            "png" => "image/png",
            "jpg" | "jpeg" => "image/jpeg",
            "gif" => "image/gif",
            "svg" => "image/svg+xml; charset=utf-8",
            "ico" => "image/x-icon",
            "woff" => "font/woff",
            "woff2" => "font/woff2",
            "ttf" => "font/ttf",
            "eot" => "application/vnd.ms-fontobject",
            "webp" => "image/webp",
            "avif" => "image/avif",
            _ => "application/octet-stream",
        }
    } else {
        "application/octet-stream"
    }
}

/// Serve a static file
pub fn serve_static_file(path: &str, base_dir: &Path) -> Result<Response, StatusCode> {
    let file_path = base_dir.join(path);
    
    // Prevent directory traversal
    if !file_path.starts_with(base_dir) {
        return Err(StatusCode::FORBIDDEN);
    }
    
    if !file_path.exists() {
        return Err(StatusCode::NOT_FOUND);
    }
    
    if file_path.is_directory() {
        // Try to serve index.html
        let index_path = file_path.join("index.html");
        if index_path.exists() {
            return serve_static_file(&format!("{}index.html", path), base_dir);
        }
        return Err(StatusCode::NOT_FOUND);
    }
    
    let content = fs::read(&file_path).map_err(|_| StatusCode::INTERNAL_SERVER_ERROR)?;
    let content_type = get_content_type(&file_path);
    
    Ok(Response::new(
        StatusCode::OK,
        vec![(CONTENT_TYPE, content_type)],
        content,
    ))
}

/// Get the web app base directory
pub fn get_web_app_dir() -> PathBuf {
    let mut path = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    path.pop(); // src-tauri
    path.pop(); // desktop-tauri
    path.push("web");
    path
}

/// Get the chatbot app directory
pub fn get_chatbot_dir() -> PathBuf {
    let mut path = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    path.pop(); // src-tauri
    path.pop(); // desktop-tauri
    path.push("chatbot");
    path
}

/// Serve the main index.html
pub fn serve_index() -> Result<Response, StatusCode> {
    serve_static_file("index.html", &get_web_app_dir())
}

/// Serve web app files
pub fn serve_web_file(path: &str) -> Result<Response, StatusCode> {
    serve_static_file(path, &get_web_app_dir())
}

/// Serve chatbot files
pub fn serve_chatbot_file(path: &str) -> Result<Response, StatusCode> {
    serve_static_file(path, &get_chatbot_dir())
}

/// Handle API proxy requests
pub fn handle_api_proxy(path: &str, backend_url: &str) -> Result<Response, StatusCode> {
    // In production, we would make an HTTP request to the backend
    // For now, we'll return a proxy response
    // This is handled by the main.rs setup
    Err(StatusCode::NOT_IMPLEMENTED)
}
