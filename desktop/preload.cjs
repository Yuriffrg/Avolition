'use strict';

const { contextBridge, ipcRenderer } = require('electron');

// Expose individual capabilities, never the IPC object or arbitrary channels.
const methods = [
  'bootstrap', 'browse_root', 'browse_output', 'set_output_folder', 'set_export_settings', 'export_animation', 'export_animation_set', 'c4d_status',
  'browse_entity', 'scan_root', 'load_entity', 'preview_entity', 'release_entity_preview', 'load_texture', 'load_animation', 'save_annotation', 'ping', 'close_app',
  'workflow_settings', 'use_library_directory', 'browse_workflow_directory', 'open_toolchain_output',
  'toolchain_status', 'toolchain_check', 'toolchain_install', 'toolchain_validate', 'toolchain_list', 'toolchain_start', 'toolchain_cancel',
  'analysis_start', 'analysis_status', 'analysis_index', 'analysis_detail', 'read_clip_bindings', 'library_settings', 'export_queue_save', 'export_queue_remove', 'export_queue_run', 'open_export_output', 'maps_settings', 'scan_maps', 'load_map', 'export_map', 'export_map_start', 'export_map_status', 'browse_map', 'open_map_output', 'object_names', 'save_object_name',
];
const api = {};
for (const method of methods) {
  api[method] = (...args) => ipcRenderer.invoke('whitehound:api', method, args);
}
// The GPU block formats the renderer can sample; the host adds them to later
// load_texture requests (see main.cjs).
const setTextureFormats = formats => ipcRenderer.invoke('whitehound:texture-formats', formats);
contextBridge.exposeInMainWorld('pywebview', { api: Object.freeze(api), setTextureFormats });
window.addEventListener('DOMContentLoaded', () => {
  window.dispatchEvent(new Event('pywebviewready'));
}, { once: true });
