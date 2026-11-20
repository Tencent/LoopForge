//! Tauri Notification Handler
//! This module provides system-level notifications for the LoopForge desktop app

use tauri::api::notification::Notification;

/// Show a system notification
#[tauri::command]
pub fn show_notification(title: String, body: String) -> Result<(), String> {
    Notification::new("LoopForge")
        .title(&title)
        .body(&body)
        .show()
        .map_err(|e| format!("Failed to show notification: {}", e))?;
    Ok(())
}

/// Show a notification with additional options
#[tauri::command]
pub fn show_notification_with_options(
    title: String,
    body: String,
    icon: Option<String>,
) -> Result<(), String> {
    let mut notification = Notification::new("LoopForge")
        .title(&title)
        .body(&body);
    
    if let Some(icon_path) = icon {
        notification = notification.icon(icon_path);
    }
    
    notification
        .show()
        .map_err(|e| format!("Failed to show notification: {}", e))?;
    Ok(())
}

/// Request notification permission (always granted in Tauri)
#[tauri::command]
pub fn request_notification_permission() -> Result<bool, String> {
    // In Tauri, notifications are always allowed if the permission is granted
    Ok(true)
}
