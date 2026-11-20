/**
 * LoopForge Tauri Compatibility Layer
 * This file ensures all web app features work identically in Tauri desktop app as in normal web app
 * 
 * KEY PRINCIPLE: The web app should work WITHOUT any modifications
 * This layer intercepts and adapts browser APIs to work in Tauri context
 */

// ============================================================================
// POLYFILLS AND COMPATIBILITY FIXES
// ============================================================================

/**
 * Window location polyfill
 * The web app uses window.location, history.pushState, history.replaceState
 * These all work fine in Tauri webview, but we need to ensure URLs are correct
 */
(function() {
    'use strict';
    
    // Check if we're in Tauri
    const isTauri = typeof window !== 'undefined' && typeof window.__TAURI__ !== 'undefined';
    
    // If we're in Tauri and the backend URL needs to be adjusted, do it here
    // But the web app uses relative paths (/api/...) which should work fine
    // as long as the base URL is correct
    
    // Store the original location for reference
    window.__ORIGINAL_LOCATION__ = window.location;
    
    // If we're in Tauri and need to proxy through a different backend URL,
    // we can override fetch and other network APIs
})();

// ============================================================================
// FETCH POLYFILL
// ============================================================================

/**
 * Intercept fetch calls and route them to the correct backend
 * In Tauri, we need to ensure all API calls go to the embedded backend
 */
(function() {
    'use strict';
    
    const originalFetch = window.fetch;
    
    // Only intercept if we're in Tauri and have a backend URL
    if (typeof window !== 'undefined' && typeof window.__TAURI_BACKEND_URL__ !== 'undefined') {
        const backendUrl = window.__TAURI_BACKEND_URL__;
        
        window.fetch = async function(resource, init = {}) {
            // If it's a relative URL, prepend the backend URL
            let url;
            try {
                url = new URL(resource, window.location.origin);
                
                // Check if this should go to the backend
                // Backend endpoints start with /api, /auth, /internal, etc.
                const backendPaths = [
                    '/api', '/auth', '/internal', '/ws', '/socket.io', '/v1',
                    '/agents', '/runs', '/projects', '/issues', '/sops', '/users',
                    '/files', '/static', '/favicon', '/bot-bind', '/gongfeng', '/mcp'
                ];
                
                const path = url.pathname;
                const shouldProxy = backendPaths.some(p => path.startsWith(p));
                
                if (shouldProxy) {
                    // Replace the origin with the backend URL
                    const backendBase = new URL(backendUrl);
                    const newUrl = new URL(path + url.search + url.hash, backendBase);
                    
                    // Add credentials if not present
                    if (!init.credentials && resource.startsWith('/')) {
                        init = { ...init, credentials: 'include' };
                    }
                    
                    return originalFetch(newUrl.toString(), init);
                }
            } catch (e) {
                // If URL parsing fails, just use the original fetch
            }
            
            return originalFetch(resource, init);
        };
        
        // Ensure fetch is properly typed
        window.fetch = window.fetch;
    }
})();

// ============================================================================
// EVENTSOURCE POLYFILL
// ============================================================================

/**
 * Intercept EventSource calls (Server-Sent Events)
 * The web app uses EventSource for run streams
 */
(function() {
    'use strict';
    
    const originalEventSource = window.EventSource;
    
    if (typeof window !== 'undefined' && typeof window.__TAURI_BACKEND_URL__ !== 'undefined' && originalEventSource) {
        const backendUrl = window.__TAURI_BACKEND_URL__;
        
        window.EventSource = function(url, options) {
            // If it's a relative URL, prepend the backend URL
            try {
                const parsed = new URL(url, window.location.origin);
                const backendPaths = ['/api/runs', '/api/sops', '/api/stream'];
                const path = parsed.pathname;
                const shouldProxy = backendPaths.some(p => path.includes(p));
                
                if (shouldProxy) {
                    const backendBase = new URL(backendUrl);
                    const newUrl = new URL(path + parsed.search + parsed.hash, backendBase);
                    return new originalEventSource(newUrl.toString(), options);
                }
            } catch (e) {
                // If URL parsing fails, use original
            }
            
            return new originalEventSource(url, options);
        };
        
        // Ensure EventSource is properly typed
        window.EventSource = window.EventSource;
    }
})();

// ============================================================================
// WEBSOCKET POLYFILL
// ============================================================================

/**
 * Intercept WebSocket calls
 * Note: WebSocket connections in Tauri need special handling
 */
(function() {
    'use strict';
    
    const originalWebSocket = window.WebSocket;
    
    if (typeof window !== 'undefined' && typeof window.__TAURI_BACKEND_URL__ !== 'undefined' && originalWebSocket) {
        const backendUrl = window.__TAURI_BACKEND_URL__;
        
        window.WebSocket = function(url, protocols) {
            // If it's a relative URL, prepend the backend URL
            try {
                let parsed;
                if (url.startsWith('ws://') || url.startsWith('wss://')) {
                    parsed = new URL(url);
                } else {
                    parsed = new URL(url, window.location.origin);
                }
                
                const backendPaths = ['/ws', '/socket.io'];
                const path = parsed.pathname;
                const shouldProxy = backendPaths.some(p => path.startsWith(p));
                
                if (shouldProxy) {
                    const backendBase = new URL(backendUrl);
                    // Replace http:// or https:// with ws:// or wss://
                    let protocol = backendBase.protocol === 'https:' ? 'wss:' : 'ws:';
                    const newUrl = `${protocol}//${backendBase.host}${path}${parsed.search}${parsed.hash}`;
                    return new originalWebSocket(newUrl, protocols);
                }
            } catch (e) {
                // If URL parsing fails, use original
            }
            
            return new originalWebSocket(url, protocols);
        };
        
        // Ensure WebSocket is properly typed
        window.WebSocket = window.WebSocket;
    }
})();

// ============================================================================
// STORAGE COMPATIBILITY
// ============================================================================

/**
 * Storage (localStorage, sessionStorage) works fine in Tauri
 * No polyfill needed
 */

// ============================================================================
// CLIPBOARD COMPATIBILITY
// ============================================================================

/**
 * Intercept clipboard operations to use Tauri API when needed
 */
(function() {
    'use strict';
    
    if (typeof window !== 'undefined' && typeof window.__TAURI__ !== 'undefined') {
        // Import Tauri clipboard API
        Promise.resolve().then(async () => {
            try {
                const { writeText, readText } = await import('@tauri-apps/api/clipboard');
                
                // Override navigator.clipboard if available
                if (navigator.clipboard) {
                    const originalClipboard = navigator.clipboard;
                    
                    navigator.clipboard.writeText = async function(text) {
                        try {
                            // Try Tauri first
                            await writeText(text);
                        } catch (e) {
                            // Fallback to original
                            if (originalClipboard.writeText) {
                                return originalClipboard.writeText(text);
                            }
                        }
                    };
                    
                    navigator.clipboard.readText = async function() {
                        try {
                            // Try Tauri first
                            return await readText();
                        } catch (e) {
                            // Fallback to original
                            if (originalClipboard.readText) {
                                return originalClipboard.readText();
                            }
                            return '';
                        }
                    };
                }
            } catch (e) {
                // Tauri clipboard API not available, use native
                console.warn('Tauri clipboard API not available, using native clipboard');
            }
        });
    }
})();

// ============================================================================
// EXTERNAL URL OPENING
// ============================================================================

/**
 * Intercept attempts to open external URLs
 * In Tauri, we should use the tauri shell API
 */
(function() {
    'use strict';
    
    if (typeof window !== 'undefined' && typeof window.__TAURI__ !== 'undefined') {
        // Store the original location
        const originalLocation = window.location;
        
        // Override window.open to use Tauri
        const originalOpen = window.open;
        window.open = async function(url, target, features) {
            // If it's a local URL, use normal open
            if (url && typeof url === 'string') {
                try {
                    const parsed = new URL(url, window.location.origin);
                    if (parsed.origin === window.location.origin) {
                        return originalOpen.call(window, url, target, features);
                    }
                    
                    // External URL - use Tauri
                    try {
                        const { open } = await import('@tauri-apps/api/shell');
                        await open(url);
                        return null; // window.open returns null when blocked
                    } catch (e) {
                        console.warn('Failed to open URL with Tauri, falling back to browser');
                        return originalOpen.call(window, url, target, features);
                    }
                } catch (e) {
                    return originalOpen.call(window, url, target, features);
                }
            }
            
            return originalOpen.call(window, url, target, features);
        };
    }
})();

// ============================================================================
// NOTIFICATION COMPATIBILITY
// ============================================================================

/**
 * Intercept notification requests
 * Browser notifications don't work in Tauri webview
 * Use Tauri notification API instead
 */
(function() {
    'use strict';
    
    if (typeof window !== 'undefined' && typeof window.__TAURI__ !== 'undefined') {
        // Check if the app tries to use Notification API
        if (typeof Notification !== 'undefined') {
            const originalNotification = Notification;
            
            Notification = function(title, options) {
                // In Tauri, we should use tauri notification
                // But for now, just log it
                console.log('Notification:', title, options);
                
                // Try to use Tauri notification
                Promise.resolve().then(async () => {
                    try {
                        const { sendNotification } = await import('@tauri-apps/api/notification');
                        await sendNotification({
                            title: title,
                            body: options?.body || ''
                        });
                    } catch (e) {
                        console.warn('Failed to send Tauri notification, falling back to browser');
                        return new originalNotification(title, options);
                    }
                });
                
                // Return a dummy notification for compatibility
                return {
                    close: () => {},
                    onclick: () => {},
                    onclose: () => {},
                    onerror: () => {}
                };
            };
            
            Notification.permission = 'granted';
            Notification.requestPermission = async function() {
                return 'granted';
            };
        }
    }
})();

// ============================================================================
// FILE DOWNLOAD COMPATIBILITY
// ============================================================================

/**
 * Intercept file downloads to use Tauri file system API
 */
(function() {
    'use strict';
    
    if (typeof window !== 'undefined' && typeof window.__TAURI__ !== 'undefined') {
        // This is more complex - we need to intercept <a download> clicks
        // and fetch-based downloads
        
        // For now, just ensure downloads work by using proper URLs
        // The backend already serves files, so direct downloads should work
    }
})();

// ============================================================================
// INITIALIZATION
// ============================================================================

/**
 * Initialize the compatibility layer
 * This should be called when the page loads
 */
async function initializeTauriCompatibility() {
    'use strict';
    
    // Only run in Tauri
    if (typeof window === 'undefined' || typeof window.__TAURI__ === 'undefined') {
        return;
    }
    
    console.log('Initializing Tauri compatibility layer...');
    
    // Set the backend URL if not already set
    if (typeof window.__TAURI_BACKEND_URL__ === 'undefined') {
        try {
            const { invoke } = await import('@tauri-apps/api');
            const backendUrl = await invoke('get_backend_url_cmd');
            window.__TAURI_BACKEND_URL__ = backendUrl;
            console.log('Backend URL:', backendUrl);
        } catch (e) {
            console.warn('Failed to get backend URL, using default');
            window.__TAURI_BACKEND_URL__ = 'http://127.0.0.1:8090';
        }
    }
    
    // Ensure backend is running
    try {
        const { invoke } = await import('@tauri-apps/api');
        const isRunning = await invoke('get_backend_status');
        
        if (!isRunning) {
            console.log('Starting backend...');
            await invoke('start_backend');
            // Wait a bit for backend to start
            await new Promise(resolve => setTimeout(resolve, 2000));
        }
    } catch (e) {
        console.warn('Failed to start backend:', e);
    }
}

// Auto-initialize when DOM is ready
if (typeof document !== 'undefined') {
    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', initializeTauriCompatibility);
    } else {
        initializeTauriCompatibility();
    }
}

// ============================================================================
// EXPORTS
// ============================================================================

// Export for manual initialization
window.initializeTauriCompatibility = initializeTauriCompatibility;

// Export backend URL getter
window.getTauriBackendUrl = function() {
    return window.__TAURI_BACKEND_URL__ || 'http://127.0.0.1:8090';
};

// Export Tauri detection
window.isTauriEnvironment = function() {
    return typeof window !== 'undefined' && typeof window.__TAURI__ !== 'undefined';
};

// Export a flag to indicate compatibility layer is loaded
window.__TAURI_COMPAT_LOADED__ = true;

console.log('Tauri Compatibility Layer loaded');
