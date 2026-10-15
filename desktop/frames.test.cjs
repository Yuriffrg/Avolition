'use strict';
// node --test desktop/frames.test.cjs
const test = require('node:test');
const assert = require('node:assert/strict');
const { ReplyReader } = require('./frames.cjs');

const pad = length => Buffer.alloc((8 - length % 8) % 8);

function frame(id, data, arrays) {
  const blobs = arrays.map(array => [{ Float32Array: 'f32', Float64Array: 'f64', Uint32Array: 'u32', Uint16Array: 'u16', Uint8Array: 'u8' }[array.constructor.name], array.byteLength]);
  let line = JSON.stringify({ id, result: { ok: true, data }, blobs });
  line += ' '.repeat((8 - (Buffer.byteLength(line) + 1) % 8) % 8) + '\n';
  const parts = [Buffer.from(line)];
  for (const array of arrays) parts.push(Buffer.from(array.buffer, array.byteOffset, array.byteLength), pad(array.byteLength));
  return Buffer.concat(parts);
}

function read(stream, split, maxBytes = 1 << 20) {
  const messages = [];
  const reader = new ReplyReader(message => messages.push(message), maxBytes);
  for (let offset = 0; offset < stream.length; offset += split) reader.push(stream.subarray(offset, offset + split));
  return messages;
}

test('JSON lines and binary frames decode identically at any chunking', () => {
  const positions = new Float32Array([0.5, -1, 2.25, 3e-7, 4, 5]);
  const indices = new Uint16Array([0, 1, 2]);
  const values = new Float64Array([0.1, 0.2, 0.3]);
  const empty = new Uint32Array(0);
  const stream = Buffer.concat([
    Buffer.from('{"id":1,"result":{"ok":true,"data":"日本語"}}\n'),
    frame(2, { models: [{ meshes: [{ positions: { $blob: 0 }, indices: { $blob: 1 }, uvs: null }] }], clip: { values: { $blob: 2 }, frames: { $blob: 3 } } },
      [positions, indices, values, empty]),
    Buffer.from('\n{"id":3,"result":{"ok":false,"error":"x"}}\n'),
  ]);
  for (const split of [1, 3, 7, 8, 64, stream.length]) {
    const messages = read(stream, split);
    assert.equal(messages.length, 3, `split ${split}`);
    assert.equal(messages[0].result.data, '日本語');
    const data = messages[1].result.data;
    assert.equal(messages[1].blobs, undefined);
    const mesh = data.models[0].meshes[0];
    assert.ok(mesh.positions instanceof Float32Array);
    assert.deepEqual([...mesh.positions], [...positions]);
    assert.ok(mesh.indices instanceof Uint16Array);
    assert.deepEqual([...mesh.indices], [0, 1, 2]);
    assert.equal(mesh.uvs, null);
    assert.ok(data.clip.values instanceof Float64Array);
    assert.deepEqual([...data.clip.values], [0.1, 0.2, 0.3]);
    assert.ok(data.clip.frames instanceof Uint32Array && data.clip.frames.length === 0);
    // Each array owns an exact-size buffer, so IPC copies only its bytes.
    assert.equal(mesh.positions.buffer.byteLength, positions.byteLength);
    assert.equal(mesh.indices.byteOffset, 0);
    assert.equal(messages[2].result.error, 'x');
  }
});

test('a frame whose last blob is empty completes without more input', () => {
  const messages = read(frame(1, { a: { $blob: 0 }, b: { $blob: 1 } }, [new Uint8Array([1, 2, 3]), new Float32Array(0)]), 1);
  assert.equal(messages.length, 1);
  assert.deepEqual([...messages[0].result.data.a], [1, 2, 3]);
});

test('malformed or oversized frames are rejected', () => {
  const reject = (stream, pattern, maxBytes) => assert.throws(() => read(Buffer.from(stream), 1 << 20, maxBytes), pattern);
  reject('{"id":1,"result":{"ok":true},"blobs":[["f16",2]]}\n', /Invalid/);
  reject('{"id":1,"result":{"ok":true},"blobs":[["f32",3]]}\n', /Invalid/);
  reject('{"id":1,"result":{"ok":true},"blobs":[["u8",-1]]}\n', /Invalid/);
  reject('{"id":1,"result":{"ok":true},"blobs":"x"}\nnot json\n', /JSON/);
  reject(frame(1, { a: { $blob: 1 } }, [new Uint8Array(1)]), /Invalid/);
  reject(frame(1, { a: { $blob: 0, extra: 1 } }, [new Uint8Array(1)]), /Invalid/);
  const big = frame(1, { a: { $blob: 0 } }, [new Uint8Array(4096)]);
  assert.throws(() => read(big, 64, 1024), error => error.code === 'RESPONSE_TOO_LARGE');
  assert.throws(() => read(Buffer.from(`"${'x'.repeat(2000)}"\n`), 64, 1024), error => error.code === 'RESPONSE_TOO_LARGE');
});
