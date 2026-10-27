// Screenshot capture for the vision fallback (agent.js): raw
// `adb exec-out screencap` bytes → downscaled RGB → minimal PNG, sent to
// TypeSafe as an image asset only when the view hierarchy is empty. Zero
// dependencies: the PNG is encoded with node:zlib and a CRC32 table.
import { deflateSync } from 'node:zlib';
import { execOut } from './adb.js';
import { DroidJevError } from './util.js';

// TypeSafe image limits (docs.typesafe.ai/images.md): each side 280..1568 px,
// total pixels 78,400..2,458,624 — an image outside them is rejected, never
// resized. Longest side ≤1568 keeps every phone screenshot inside both caps
// (an unresized 1080×2340 is already over the pixel cap); the floor only
// matters for tiny screens.
const MAX_SIDE = 1568;
const MIN_SIDE = 280;
const MIN_PIXELS = 78400;

// Raw screencap output: uint32 LE width, height, format — plus a fourth
// dataspace word on API 27+. No magic, no version field; the header size is
// identified by the total payload length. Pixels are tightly packed (no
// stride padding). Only RGBA_8888 (format 1) comes off the display-capture
// path; anything else fails the fallback and the caller degrades to blocked.
export function parseScreencap(buf) {
  if (!Buffer.isBuffer(buf) || buf.length < 16) throw new DroidJevError('screencap output too short');
  const w = buf.readUInt32LE(0);
  const h = buf.readUInt32LE(4);
  const format = buf.readUInt32LE(8);
  const bpp = format === 1 || format === 2 || format === 5 ? 4 : format === 3 ? 3 : format === 4 ? 2 : 0;
  if (!bpp || w === 0 || h === 0 || w > 20000 || h > 20000) {
    throw new DroidJevError(`unexpected screencap header ${w}x${h} format ${format}`);
  }
  const pixels = w * h * bpp;
  const offset = buf.length === 16 + pixels ? 16 : buf.length === 12 + pixels ? 12 : 0;
  if (!offset) {
    // A failed exec-out merges device stderr into stdout — that text is why
    // the length does not add up.
    throw new DroidJevError(`screencap payload is ${buf.length} bytes, expected ${pixels}+header`);
  }
  return { w, h, format, bpp, offset };
}

// Aspect-preserving target size inside the API limits.
export function scaleTarget(w, h) {
  const sMax = Math.min(1, MAX_SIDE / Math.max(w, h));
  const sMin = Math.max(MIN_SIDE / w, MIN_SIDE / h, Math.sqrt(MIN_PIXELS / (w * h)));
  const s = sMax >= sMin ? sMax : sMin;
  const clamp = (v) => Math.max(MIN_SIDE, Math.min(MAX_SIDE, v));
  return { w: clamp(Math.round(w * s)), h: clamp(Math.round(h * s)) };
}

// Box-average RGBA→RGB resample: each destination pixel is the mean of the
// source rectangle it covers (nearest-ish when upscaling). Alpha is dropped —
// display captures are opaque.
export function downscaleRgba(buf, src, dst) {
  const out = new Uint8Array(dst.w * dst.h * 3);
  const sx = src.w / dst.w;
  const sy = src.h / dst.h;
  for (let y = 0; y < dst.h; y++) {
    const y0 = Math.floor(y * sy);
    const y1 = Math.min(src.h, Math.max(y0 + 1, Math.floor((y + 1) * sy)));
    for (let x = 0; x < dst.w; x++) {
      const x0 = Math.floor(x * sx);
      const x1 = Math.min(src.w, Math.max(x0 + 1, Math.floor((x + 1) * sx)));
      let r = 0;
      let g = 0;
      let b = 0;
      let n = 0;
      for (let yy = y0; yy < y1; yy++) {
        for (let xx = x0; xx < x1; xx++) {
          const i = src.offset + (yy * src.w + xx) * 4;
          r += buf[i];
          g += buf[i + 1];
          b += buf[i + 2];
          n++;
        }
      }
      const o = (y * dst.w + x) * 3;
      out[o] = Math.round(r / n);
      out[o + 1] = Math.round(g / n);
      out[o + 2] = Math.round(b / n);
    }
  }
  return out;
}

const CRC_TABLE = (() => {
  const t = new Uint32Array(256);
  for (let n = 0; n < 256; n++) {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    t[n] = c >>> 0;
  }
  return t;
})();

const crc32 = (buf) => {
  let c = 0xffffffff;
  for (const b of buf) c = CRC_TABLE[(c ^ b) & 0xff] ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
};

function chunk(type, data) {
  const len = Buffer.alloc(4);
  len.writeUInt32BE(data.length, 0);
  const body = Buffer.concat([Buffer.from(type, 'ascii'), data]);
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(body), 0);
  return Buffer.concat([len, body, crc]);
}

// Minimal valid PNG: signature + IHDR + one IDAT + IEND, 8-bit truecolor RGB,
// filter None per scanline, single zlib stream. (node:zlib.crc32 exists only
// from Node 20.15 — engines say ≥20, so the table stays.)
export function encodePng(rgb, w, h) {
  const stride = w * 3;
  const scan = Buffer.alloc((stride + 1) * h);
  for (let y = 0; y < h; y++) {
    scan[y * (stride + 1)] = 0; // filter type None
    scan.set(rgb.subarray(y * stride, (y + 1) * stride), y * (stride + 1) + 1);
  }
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(w, 0);
  ihdr.writeUInt32BE(h, 4);
  ihdr[8] = 8; // bit depth
  ihdr[9] = 2; // color type: truecolor RGB
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk('IHDR', ihdr),
    chunk('IDAT', deflateSync(scan)),
    chunk('IEND', Buffer.alloc(0)),
  ]);
}

// Full pipeline: raw capture → API-sized PNG.
export async function screenshotPng(serial) {
  const t0 = performance.now();
  // maxBytes covers 4K-class displays (2160×3840×4 ≈ 33 MB raw).
  const r = await execOut(serial, ['screencap'], { timeoutMs: 15000, maxBytes: 64 * 1024 * 1024 });
  if (r.code !== 0) throw new DroidJevError(`screencap exited ${r.code}`);
  const src = parseScreencap(r.stdout);
  if (src.format !== 1) throw new DroidJevError(`screencap pixel format ${src.format} is not RGBA_8888`);
  const dst = scaleTarget(src.w, src.h);
  const rgb = downscaleRgba(r.stdout, src, dst);
  return { png: encodePng(rgb, dst.w, dst.h), w: src.w, h: src.h, tookMs: performance.now() - t0 };
}
