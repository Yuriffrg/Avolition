'use strict';
// Desktop transport benchmark: core stdio -> Electron main -> IPC -> context
// bridge -> sandboxed, context-isolated renderer, like desktop/main.cjs and
// preload.cjs. JSON mode reads replies like the previous main.cjs; binary
// mode with desktop/frames.cjs.
//
//   cd desktop && xvfb-run -a node_modules/electron/dist/electron --no-sandbox \
//     ../gocore/bench/transport_electron/main.cjs <owio-core> <library> [reps] [modes]
//
// modes defaults to json,binary; pass json for a core without binary frames
// (e.g. a script that runs python3 app.py).

const { app, BrowserWindow, ipcMain } = require('electron');
const path = require('node:path');
const { Client, cases } = require('../transport_stdio.cjs');

const args = process.argv.slice(process.argv.findIndex(arg => arg.endsWith(path.join('transport_electron', 'main.cjs'))) + 1);
const [core, library, repsText, modes = 'json,binary'] = args;
const reps = Number(repsText) || 3;

app.whenReady().then(async () => {
  const clients = { json: new Client(path.resolve(core), path.resolve(library), false), binary: new Client(path.resolve(core), path.resolve(library), true) };
  await Promise.all(Object.values(clients).map(client => client.call('bootstrap')));
  ipcMain.handle('bench:call', (_event, mode, method, callArgs) => clients[mode].call(method, ...callArgs));
  ipcMain.handle('bench:bytes', (_event, mode) => clients[mode].bytes);
  ipcMain.once('bench:done', (_event, rows) => {
    console.log('| reply | mode | stdout bytes | renderer has reply (ms) | + typed arrays (ms) |');
    console.log('|---|---|---:|---:|---:|');
    for (const row of rows) console.log(`| ${row.label} | ${row.mode} | ${(row.bytes / 1e6).toFixed(1)} MB | ${row.reply.toFixed(0)} | ${row.typed.toFixed(0)} |`);
    for (const client of Object.values(clients)) client.close();
    app.exit(0);
  });
  ipcMain.once('bench:error', (_event, message) => { console.error(message); app.exit(1); });
  const window = new BrowserWindow({ show: false, webPreferences: {
    preload: path.join(__dirname, 'preload.cjs'), contextIsolation: true, nodeIntegration: false, sandbox: true,
  } });
  await window.loadFile(path.join(__dirname, 'index.html'), { query: { cases: JSON.stringify(cases(path.resolve(library))), reps: String(reps), modes } });
});
