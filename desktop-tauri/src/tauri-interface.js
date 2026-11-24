/**
 * LoopForge Tauri Interface
 * This file provides the bridge between the web frontend and Tauri backend
 * It ensures all features work identically in Tauri as in the normal web app
 */

import { invoke } from '@tauri-apps/api';
import { open } from '@tauri-apps/api/shell';
import { writeText, readText } from '@tauri-apps/api/clipboard';
import { Notification } from '@tauri-apps/api/notification';

/**
 * Tauri API wrapper with fallback
 */
export const tauriApi = {
    invoke: async (command, args) => {
        try {
            if (typeof window.__TAURI__ !== 'undefined') {
                return await invoke(command, args);
            }
            if (window.tauriInvoke) {
                return await window.tauriInvoke(command, args);
            }
            console.warn(`Tauri command not available: ${command}`);
            return null;
        } catch (error) {
            console.error(`Tauri command ${command} failed:`, error);
            throw error;
        }
    }
};

/**
 * Check if we're running in Tauri
 */
export function isTauri() {
    return typeof window !== 'undefined' && typeof window.__TAURI__ !== 'undefined';
}

/**
 * Check if we're running in a browser
 */
export function isBrowser() {
    return !isTauri();
}

// ============================================================================
// BACKEND MANAGEMENT
// ============================================================================

export const backend = {
    /**
     * Start the Go backend server
     */
    async start() {
        return await tauriApi.invoke('start_backend');
    },
    
    /**
     * Stop the Go backend server
     */
    async stop() {
        return await tauriApi.invoke('stop_backend');
    },
    
    /**
     * Restart the Go backend server
     */
    async restart() {
        return await tauriApi.invoke('restart_backend');
    },
    
    /**
     * Check if backend is running
     */
    async isRunning() {
        return await tauriApi.invoke('get_backend_status');
    },
    
    /**
     * Get backend URL
     */
    async getUrl() {
        return await tauriApi.invoke('get_backend_url_cmd');
    },
    
    /**
     * Get web app URL
     */
    async getWebAppUrl() {
        return await tauriApi.invoke('get_web_app_url_cmd');
    },
    
    /**
     * Get backend logs
     */
    async getLogs() {
        return await tauriApi.invoke('get_backend_logs');
    }
};

// ============================================================================
// SYSTEM INFORMATION
// ============================================================================

export const system = {
    /**
     * Get app version
     */
    async getVersion() {
        return await tauriApi.invoke('get_app_version');
    },
    
    /**
     * Get platform info
     */
    async getPlatform() {
        return await tauriApi.invoke('get_platform_info');
    },
    
    /**
     * Get data directory
     */
    async getDataDirectory() {
        return await tauriApi.invoke('get_data_directory');
    },
    
    /**
     * Check if Docker is running
     */
    async checkDockerRunning() {
        return await tauriApi.invoke('check_docker_running');
    }
};

// ============================================================================
// NOTIFICATIONS
// ============================================================================

export const notifications = {
    /**
     * Show a system notification
     */
    async show(title, options = {}) {
        if (isTauri()) {
            try {
                await tauriApi.invoke('show_notification', {
                    title: title,
                    body: options.body || ''
                });
                return true;
            } catch (e) {
                console.warn('Failed to show Tauri notification, falling back');
            }
        }
        
        // Fallback to browser Notification API
        if (window.Notification) {
            if (Notification.permission === 'granted') {
                new Notification(title, options);
                return true;
            } else if (Notification.permission !== 'denied') {
                await Notification.requestPermission();
                if (Notification.permission === 'granted') {
                    new Notification(title, options);
                    return true;
                }
            }
        }
        
        // Fallback to toast
        if (typeof toast === 'function') {
            toast(options.body || title, options.type || 'info');
        }
        
        return false;
    },
    
    /**
     * Request notification permission
     */
    async requestPermission() {
        if (isTauri()) {
            return true; // Tauri always has permission
        }
        if (window.Notification) {
            return await Notification.requestPermission() === 'granted';
        }
        return false;
    }
};

// ============================================================================
// UTILITIES
// ============================================================================

export const utils = {
    /**
     * Open external URL
     */
    async openUrl(url) {
        if (isTauri()) {
            try {
                await open(url);
                return true;
            } catch (e) {
                console.warn('Failed to open URL with Tauri, falling back');
            }
        }
        
        // Fallback to window.open
        window.open(url, '_blank');
        return true;
    },
    
    /**
     * Copy text to clipboard
     */
    async copyText(text) {
        if (isTauri()) {
            try {
                await writeText(text);
                return true;
            } catch (e) {
                console.warn('Failed to copy with Tauri, falling back');
            }
        }
        
        // Fallback to navigator.clipboard
        if (navigator.clipboard) {
            try {
                await navigator.clipboard.writeText(text);
                return true;
            } catch (e) {
                console.warn('Failed to copy with navigator.clipboard');
            }
        }
        
        // Fallback to document.execCommand
        try {
            const textarea = document.createElement('textarea');
            textarea.value = text;
            textarea.style.position = 'fixed';
            textarea.style.opacity = '0';
            document.body.appendChild(textarea);
            textarea.select();
            const result = document.execCommand('copy');
            document.body.removeChild(textarea);
            return result;
        } catch (e) {
            console.error('Failed to copy text');
            return false;
        }
    },
    
    /**
     * Read text from clipboard
     */
    async readClipboard() {
        if (isTauri()) {
            try {
                return await readText();
            } catch (e) {
                console.warn('Failed to read clipboard with Tauri, falling back');
            }
        }
        
        if (navigator.clipboard) {
            try {
                return await navigator.clipboard.readText();
            } catch (e) {
                console.warn('Failed to read clipboard');
            }
        }
        
        return '';
    }
};

// ============================================================================
// FILE OPERATIONS
// ============================================================================

export const filesystem = {
    /**
     * Select a file for upload
     */
    async selectFile(options = {}) {
        if (isTauri()) {
            try {
                const { open } = await import('@tauri-apps/api/dialog');
                const selected = await open({
                    title: options.title || 'Select File',
                    filters: options.filters,
                    multiple: options.multiple || false,
                    directory: options.directory || false
                });
                return selected;
            } catch (e) {
                console.warn('Failed to select file with Tauri, falling back');
            }
        }
        
        // Fallback to <input type="file">
        return new Promise((resolve) => {
            const input = document.createElement('input');
            input.type = 'file';
            if (options.multiple) {
                input.multiple = true;
            }
            if (options.accept) {
                input.accept = options.accept;
            }
            input.style.display = 'none';
            input.onchange = () => {
                const files = Array.from(input.files || []);
                resolve(files.length > 0 ? files : null);
                document.body.removeChild(input);
            };
            document.body.appendChild(input);
            input.click();
        });
    },
    
    /**
     * Select a directory
     */
    async selectDirectory() {
        if (isTauri()) {
            try {
                const { open } = await import('@tauri-apps/api/dialog');
                const selected = await open({
                    title: 'Select Directory',
                    directory: true
                });
                return selected;
            } catch (e) {
                console.warn('Failed to select directory with Tauri');
            }
        }
        
        // Browser doesn't support directory selection
        console.warn('Directory selection not supported in browser');
        return null;
    },
    
    /**
     * Save a file
     */
    async saveFile(content, filename, options = {}) {
        if (isTauri()) {
            try {
                const { save } = await import('@tauri-apps/api/dialog');
                const { writeTextFile, writeBinaryFile } = await import('@tauri-apps/api/fs');
                
                const path = await save({
                    title: options.title || 'Save File',
                    defaultPath: filename,
                    filters: options.filters
                });
                
                if (path) {
                    if (typeof content === 'string') {
                        await writeTextFile(path, content);
                    } else if (content instanceof Uint8Array || content instanceof ArrayBuffer) {
                        await writeBinaryFile(path, content);
                    }
                    return path;
                }
                return null;
            } catch (e) {
                console.warn('Failed to save file with Tauri, falling back');
            }
        }
        
        // Fallback to download
        const blob = typeof content === 'string' ? new Blob([content]) : new Blob([content]);
        const url = URL.createObjectURL(blob);
        const a = document.createElement('a');
        a.href = url;
        a.download = filename;
        a.click();
        URL.revokeObjectURL(url);
        return null;
    }
};

// ============================================================================
// WINDOW MANAGEMENT
// ============================================================================

export const windowManager = {
    /**
     * Minimize the window
     */
    async minimize() {
        if (isTauri()) {
            try {
                const { appWindow } = await import('@tauri-apps/api/window');
                await appWindow.minimize();
                return true;
            } catch (e) {
                console.warn('Failed to minimize window');
            }
        }
        return false;
    },
    
    /**
     * Maximize the window
     */
    async maximize() {
        if (isTauri()) {
            try {
                const { appWindow } = await import('@tauri-apps/api/window');
                await appWindow.maximize();
                return true;
            } catch (e) {
                console.warn('Failed to maximize window');
            }
        }
        return false;
    },
    
    /**
     * Restore the window
     */
    async restore() {
        if (isTauri()) {
            try {
                const { appWindow } = await import('@tauri-apps/api/window');
                await appWindow.unmaximize();
                return true;
            } catch (e) {
                console.warn('Failed to restore window');
            }
        }
        return false;
    },
    
    /**
     * Close the window
     */
    async close() {
        if (isTauri()) {
            try {
                const { appWindow } = await import('@tauri-apps/api/window');
                await appWindow.close();
                return true;
            } catch (e) {
                console.warn('Failed to close window');
            }
        }
        return false;
    }
};

// ============================================================================
// PATH MANAGEMENT
// ============================================================================

export const path = {
    /**
     * Join path segments
     */
    join(...segments) {
        if (isTauri()) {
            try {
                const { join } = require('@tauri-apps/api/path');
                return join(...segments);
            } catch (e) {
                // Fallback to simple join
            }
        }
        return segments.join('/').replace(/\/+/g, '/');
    },
    
    /**
     * Get the base directory
     */
    async baseDir() {
        if (isTauri()) {
            try {
                const { appDir } = await import('@tauri-apps/api/path');
                return await appDir();
            } catch (e) {
                console.warn('Failed to get base directory');
            }
        }
        return '/';
    }
};

// ============================================================================
// INITIALIZATION
// ============================================================================

/**
 * Initialize the Tauri interface
 * This should be called when the app starts
 */
export async function initializeTauri() {
    if (!isTauri()) {
        console.log('Not running in Tauri - using fallback mode');
        return;
    }
    
    console.log('Initializing Tauri interface...');
    
    // Ensure backend is running
    try {
        const isRunning = await backend.isRunning();
        console.log(`Backend is ${isRunning ? 'running' : 'not running'}`);
        
        if (!isRunning) {
            console.log('Starting backend...');
            await backend.start();
            // Wait for backend to be ready
            await new Promise(resolve => setTimeout(resolve, 3000));
        }
    } catch (error) {
        console.error('Failed to check backend status:', error);
    }
    
    // Check Docker
    try {
        const dockerRunning = await system.checkDockerRunning();
        console.log(`Docker is ${dockerRunning ? 'running' : 'not running'}`);
        
        if (!dockerRunning) {
            // Warn user that Docker is required
            console.warn('Docker is required for sandbox execution');
        }
    } catch (error) {
        console.warn('Failed to check Docker status:', error);
    }
}

/**
 * Register Tauri commands on window object for debugging
 */
export function registerTauriCommands() {
    if (typeof window === 'undefined') return;
    
    window.tauriBackend = backend;
    window.tauriSystem = system;
    window.tauriNotifications = notifications;
    window.tauriUtils = utils;
    window.tauriFilesystem = filesystem;
    window.tauriWindow = windowManager;
    window.tauriPath = path;
    window.tauriInitialize = initializeTauri;
    window.isTauri = isTauri;
    window.isBrowser = isBrowser;
}

// Auto-register commands when this module is loaded
registerTauriCommands();

// Auto-initialize when DOM is ready
if (typeof document !== 'undefined') {
    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', initializeTauri);
    } else {
        initializeTauri();
    }
}

// Export everything
export default {
    tauriApi,
    isTauri,
    isBrowser,
    backend,
    system,
    notifications,
    utils,
    filesystem,
    windowManager,
    path,
    initializeTauri,
    registerTauriCommands
};
