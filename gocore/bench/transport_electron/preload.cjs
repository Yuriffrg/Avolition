'use strict';
const { contextBridge, ipcRenderer } = require('electron');

contextBridge.exposeInMainWorld('bench', {
  call: (mode, method, args) => ipcRenderer.invoke('bench:call', mode, method, args),
  bytes: mode => ipcRenderer.invoke('bench:bytes', mode),
  done: rows => ipcRenderer.send('bench:done', rows),
  error: message => ipcRenderer.send('bench:error', message),
});
