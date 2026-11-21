//! HTTP Proxy module for routing requests to the Go backend

use std::collections::HashMap;
use std::net::{IpAddr, Ipv4Addr, SocketAddr};
use std::sync::Arc;
use tokio::sync::Mutex;

/// Proxy configuration
pub struct ProxyConfig {
    pub backend_host: String,
    pub backend_port: u16,
    pub listen_host: String,
    pub listen_port: u16,
}

impl Default for ProxyConfig {
    fn default() -> Self {
        Self {
            backend_host: "127.0.0.1".to_string(),
            backend_port: 8090,
            listen_host: "127.0.0.1".to_string(),
            listen_port: 0, // 0 means random port
        }
    }
}

/// Proxy state
pub struct ProxyState {
    pub config: ProxyConfig,
    pub backend_url: String,
}

impl ProxyState {
    pub fn new(config: ProxyConfig) -> Self {
        let backend_url = format!(
            "http://{}:{}",
            config.backend_host, config.backend_port
        );
        Self { config, backend_url }
    }
}

/// Route requests based on path
pub fn get_route_target(path: &str, backend_url: &str) -> String {
    // Routes that should go to the backend
    let backend_routes = vec![
        "/api",
        "/auth",
        "/internal",
        "/ws",
        "/socket.io",
        "/v1",
        "/agents",
        "/runs",
        "/projects",
        "/issues",
        "/sops",
        "/users",
        "/files",
        "/static",
        "/favicon",
        "/bot-bind",
        "/gongfeng",
        "/mcp",
        "/_next",
        "/assets",
    ];

    for route in backend_routes {
        if path.starts_with(route) || path.contains(route) {
            return format!("{}{}", backend_url, path);
        }
    }

    // Default: serve from web directory
    path.to_string()
}

/// Check if path is a static asset that should be served locally
pub fn is_static_asset(path: &str) -> bool {
    let static_extensions = vec![
        ".js", ".css", ".html", ".png", ".jpg", ".jpeg", ".gif", ".svg", ".ico", ".woff", 
        ".woff2", ".ttf", ".eot", ".json", ".webp", ".avif"
    ];
    
    for ext in static_extensions {
        if path.ends_with(ext) {
            return true;
        }
    }
    
    false
}

/// Get the base URL for serving static files
pub fn get_static_base() -> String {
    "../web".to_string()
}
