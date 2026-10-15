'use strict';
// Stdio transport benchmark: JSON replies vs binary frames, measured the way
// the desktop host receives them.
//
//   node gocore/bench/transport_stdio.cjs <owio-core> <library> [reps]
//
// JSON mode parses replies like the previous desktop/main.cjs (StringDecoder,
// one JSON.parse per line); binary mode uses desktop/frames.cjs. "ipc" adds a
// v8 serialize/deserialize round trip, the structured clone that Electron's
// IPC applies before the renderer sees the reply. Times are medians.

const { spawn } = require('node:child_process');
const { StringDecoder } = require('node:string_decoder');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const v8 = require('node:v8');
const { ReplyReader } = require('../../desktop/frames.cjs');

const cases = library => ({
  entity: path.join(library, 'Heroes/Big/Skin0/Entities/Gameplay3P/Gameplay3P.owentity'),
  clip: path.join(library, 'Heroes/Big/Skin0/Animations/000_Big.owanimclip'),
  map: path.join(library, 'Maps/Bench/Bench/Default/Bench.owmap'),
  mapRoot: path.join(library, 'Maps/Bench'),
});

class Client {
  constructor(core, library, binary) {
    this.binary = binary;
    this.pending = new Map();
    this.nextId = 1;
    this.bytes = 0;
    this.firstChunk = 0;
    const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'avolition-transport-'));
    this.child = spawn(core, ['--stdio', '--data-dir', dataDir, '--root', library], { stdio: ['pipe', 'pipe', 'inherit'] });
    const deliver = message => {
      const entry = this.pending.get(message.id);
      this.pending.delete(message.id);
      entry.resolve(message.result);
    };
    if (binary) {
      const reader = new ReplyReader(deliver, 1 << 30);
      this.child.stdout.on('data', chunk => { this.count(chunk); reader.push(chunk); });
    } else {
      // The previous desktop/main.cjs reader.
      const decoder = new StringDecoder('utf8');
      let fragments = [];
      this.child.stdout.on('data', chunk => {
        this.count(chunk);
        const text = decoder.write(chunk);
        let start = 0;
        while (start < text.length) {
          const newline = text.indexOf('\n', start);
          const segment = text.slice(start, newline < 0 ? undefined : newline);
          if (segment) fragments.push(segment);
          if (newline < 0) return;
          const line = fragments.join('');
          fragments = [];
          start = newline + 1;
          if (line.trim()) deliver(JSON.parse(line));
        }
      });
    }
  }

  count(chunk) {
    if (!this.firstChunk) this.firstChunk = performance.now();
    this.bytes += chunk.length;
  }

  call(method, ...args) {
    this.firstChunk = 0;
    const id = this.nextId++;
    const request = this.binary ? { id, method, args, binary: true } : { id, method, args };
    return new Promise(resolve => {
      this.pending.set(id, { resolve });
      this.child.stdin.write(JSON.stringify(request) + '\n');
    });
  }

  close() { this.child.stdin.end(); }
}

const median = values => [...values].sort((a, b) => a - b)[Math.floor(values.length / 2)];

async function measure(client, label, method, args) {
  const times = [], withIpc = [], first = [];
  let bytes = 0, result;
  for (let rep = 0; rep < reps; rep++) {
    const before = client.bytes;
    const start = performance.now();
    result = await client.call(method, ...args);
    const received = performance.now();
    if (!result.ok) throw new Error(`${method}: ${result.error}`);
    v8.deserialize(v8.serialize(result));
    times.push(received - start);
    first.push(client.firstChunk - start);
    withIpc.push(performance.now() - start);
    bytes = client.bytes - before;
  }
  return { label, firstMs: median(first), ms: median(times), ipcMs: median(withIpc), bytes, result };
}

module.exports = { Client, cases };
if (require.main !== module) return;

const [core, library, repsText] = process.argv.slice(2);
if (!core || !library) { console.error('usage: transport_stdio.cjs <owio-core> <library> [reps]'); process.exit(2); }
const reps = Number(repsText) || 5;
const { entity, clip, map, mapRoot } = cases(library);

(async () => {
  const rows = [];
  for (const binary of [false, true]) {
    const client = new Client(core, library, binary);
    await client.call('bootstrap');
    const loaded = await measure(client, 'load_entity Big', 'load_entity', [entity]);
    const texture = Object.keys(loaded.result.data.textures || {})[0];
    const results = [loaded, await measure(client, 'load_animation Big', 'load_animation', [clip]),
      await measure(client, 'load_map Bench', 'load_map', [map, mapRoot])];
    if (texture) results.push(await measure(client, 'load_texture 2048', 'load_texture', [texture, 2048]));
    for (const row of results) rows.push({ mode: binary ? 'binary' : 'json', ...row });
    client.close();
  }
  console.log('| reply | mode | bytes | first byte (ms) | reply object (ms) | + IPC clone (ms) |');
  console.log('|---|---|---:|---:|---:|---:|');
  for (const row of rows) {
    console.log(`| ${row.label} | ${row.mode} | ${(row.bytes / 1e6).toFixed(1)} MB | ${row.firstMs.toFixed(1)} | ${row.ms.toFixed(1)} | ${row.ipcMs.toFixed(1)} |`);
  }
})().catch(error => { console.error(error); process.exit(1); });
