'use strict';

// Reads the local importer's stdout: one JSON reply per line, or a binary
// frame when a request asked for one ("binary": true). A frame is a JSON line
// with a "blobs" list of [type, byteLength], padded with spaces to a multiple
// of 8 bytes, followed by each blob's little-endian bytes zero-padded to a
// multiple of 8. {"$blob":N} placeholders in its result become blob N as a
// typed array. Each typed array gets its own exact-size ArrayBuffer, so IPC
// and the context bridge copy only that array (a view would carry its whole
// buffer). Cores that do not know frames (the Python core) send JSON lines.

const BLOB_TYPES = Object.freeze({ f32: Float32Array, f64: Float64Array, u32: Uint32Array, u16: Uint16Array, u8: Uint8Array });
const NEWLINE = 10;
const padding = length => (8 - length % 8) % 8;

function invalid() { return new Error('Invalid local importer response.'); }
function tooLarge() {
  return Object.assign(new Error('Local importer response exceeds the preview size limit.'), { code: 'RESPONSE_TOO_LARGE' });
}

/** Replaces {"$blob":N} placeholders in value (in place) with arrays[N]. */
function attachArrays(value, arrays) {
  if (Array.isArray(value)) {
    for (let index = 0; index < value.length; index++) {
      const item = value[index];
      if (item && typeof item === 'object') value[index] = attachArrays(item, arrays);
    }
    return value;
  }
  if (Object.hasOwn(value, '$blob')) {
    const index = value.$blob;
    if (!Number.isInteger(index) || index < 0 || index >= arrays.length || Object.keys(value).length !== 1) throw invalid();
    return arrays[index];
  }
  for (const key in value) {
    const item = value[key];
    if (item && typeof item === 'object') value[key] = attachArrays(item, arrays);
  }
  return value;
}

class ReplyReader {
  /**
   * onMessage receives each parsed reply object. maxBytes bounds one reply,
   * frame blobs included. push() throws on a malformed or oversized reply.
   */
  constructor(onMessage, maxBytes) {
    this.onMessage = onMessage;
    this.maxBytes = maxBytes;
    this.parts = [];
    this.size = 0;
    this.frame = null;
  }

  push(chunk) {
    let offset = 0;
    while (offset < chunk.length) {
      if (this.frame) { offset = this._fill(chunk, offset); continue; }
      const newline = chunk.indexOf(NEWLINE, offset);
      const end = newline < 0 ? chunk.length : newline;
      this.size += end - offset;
      if (this.size > this.maxBytes) throw tooLarge();
      if (end > offset) this.parts.push(chunk.subarray(offset, end));
      if (newline < 0) return;
      offset = newline + 1;
      // A newline byte never occurs inside a multi-byte UTF-8 sequence.
      const line = this.parts.length === 1 ? this.parts[0].toString('utf8') : Buffer.concat(this.parts).toString('utf8');
      const lineBytes = this.size + 1;
      this.parts = [];
      this.size = 0;
      if (!line.trim()) continue;
      const message = JSON.parse(line);
      if (message && Array.isArray(message.blobs)) {
        this._start(message, lineBytes);
        offset = this._fill(chunk, offset);
      } else this.onMessage(message);
    }
  }

  _start(message, lineBytes) {
    let total = lineBytes;
    for (const blob of message.blobs) {
      const Type = Array.isArray(blob) && Object.hasOwn(BLOB_TYPES, blob[0]) ? BLOB_TYPES[blob[0]] : null;
      const length = Array.isArray(blob) ? blob[1] : -1;
      if (!Type || !Number.isSafeInteger(length) || length < 0 || length % Type.BYTES_PER_ELEMENT) throw invalid();
      total += length + padding(length);
    }
    if (total > this.maxBytes) throw tooLarge();
    this.frame = { message, index: 0, target: null, filled: 0, pad: 0, arrays: [] };
  }

  _fill(chunk, offset) {
    const frame = this.frame;
    const blobs = frame.message.blobs;
    while (frame.index < blobs.length) {
      const [type, length] = blobs[frame.index];
      if (!frame.target) {
        frame.target = new Uint8Array(new ArrayBuffer(length));
        frame.filled = 0;
        frame.pad = padding(length);
      }
      const take = Math.min(length - frame.filled, chunk.length - offset);
      if (take > 0) {
        frame.target.set(chunk.subarray(offset, offset + take), frame.filled);
        frame.filled += take;
        offset += take;
      }
      const skip = frame.filled === length ? Math.min(frame.pad, chunk.length - offset) : 0;
      frame.pad -= skip;
      offset += skip;
      if (frame.filled < length || frame.pad > 0) return offset; // Wait for more bytes.
      frame.arrays.push(new BLOB_TYPES[type](frame.target.buffer));
      frame.target = null;
      frame.index++;
    }
    this.frame = null;
    const { message } = frame;
    delete message.blobs;
    if (message.result && typeof message.result === 'object') message.result = attachArrays(message.result, frame.arrays);
    this.onMessage(message);
    return offset;
  }
}

module.exports = { BLOB_TYPES, ReplyReader, attachArrays };
