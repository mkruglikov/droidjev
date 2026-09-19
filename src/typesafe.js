// TypeSafe (System One / jev) client. Security rules:
// - the API key is read from TYPESAFE_API_KEY at runtime and never appears in
//   any message, trace, or error (errors carry status + redacted body only)
// - responses are strictly validated before any answer is trusted; on any
//   validation failure nothing downstream executes
import { DroidJevError, redact, sleep, run } from './util.js';

const API_URL = 'https://api.typesafe.ai/v1/systemone';
const MODEL = 'jev-latest';
const RETRY_STATUS = new Set([429, 529, 503]);
const MAX_ATTEMPTS = 3;

let cachedKey = null;

// A usable bearer token: single line, printable ASCII, sane length. Validating
// BEFORE fetch() matters: an invalid header value makes fetch throw an error
// that quotes the full value verbatim — the one realistic key-leak path.
function looksLikeToken(s) {
  return typeof s === 'string' && s.length >= 8 && s.length <= 4096 && /^[\x20-\x7E]+$/.test(s.trim());
}

// Key resolution: real env first; if absent (non-interactive shells, IDE
// launches), read it from the user's login shell environment. The value is
// kept in memory only — never printed, logged, or written to disk.
// ponytail: shells other than zsh/bash aren't probed; upgrade path is a
// keychain integration.
export async function requireApiKey() {
  if (cachedKey) return cachedKey;
  const fromEnv = process.env.TYPESAFE_API_KEY;
  if (looksLikeToken(fromEnv)) {
    cachedKey = fromEnv.trim();
    return cachedKey;
  }
  for (const [shell, flag] of [
    ['zsh', '-lc'],
    ['bash', '-lc'],
  ]) {
    try {
      const r = await run(shell, [flag, 'printenv TYPESAFE_API_KEY'], { timeoutMs: 5000 });
      const key = r.stdout.trim();
      // A login shell can echo extra lines (profile MOTD, tool init); the key
      // is a single token, so take the last line that looks like one.
      const candidates = key.split(/\r?\n/).filter(looksLikeToken);
      if (r.code === 0 && candidates.length) {
        cachedKey = candidates[candidates.length - 1];
        return cachedKey;
      }
    } catch {
      // shell missing; try the next one
    }
  }
  throw new DroidJevError(
    'TYPESAFE_API_KEY is not set (or not a usable single-line token); droidjev needs it to ask jev where to click',
  );
}

// Body read with a byte cap, still under the abort timer: a server that
// trickles or floods the body must neither hang us nor grow memory unbounded.
const MAX_BODY_BYTES = 2 * 1024 * 1024;

async function readCapped(res, ctrl) {
  const reader = res.body.getReader();
  const chunks = [];
  let total = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    total += value.byteLength;
    if (total > MAX_BODY_BYTES) {
      ctrl.abort();
      throw new DroidJevError(`typesafe api response exceeded ${MAX_BODY_BYTES} bytes`);
    }
    chunks.push(value);
  }
  return new TextDecoder().decode(concat(chunks));
}

function concat(chunks) {
  const len = chunks.reduce((n, c) => n + c.byteLength, 0);
  const out = new Uint8Array(len);
  let off = 0;
  for (const c of chunks) {
    out.set(c, off);
    off += c.byteLength;
  }
  return out;
}

// The first fetch() in a process pays TLS + connection setup (~0.5s) that
// every fresh `droidjev act` would otherwise eat on its first request. A
// key-less HEAD warms the pooled connection while the initial layout dump
// (~1s) runs; its response is ignored entirely.
export function prewarm() {
  return fetch(API_URL, { method: 'HEAD' }).catch(() => {});
}

// One request with N parallel questions. Returns {answers, usage, latencyMs}.
export async function ask({ state, questions, timeoutMs = 20000 }) {
  const key = await requireApiKey();
  const body = JSON.stringify({ model: MODEL, state, questions });
  let lastErr = null;
  for (let attempt = 0; attempt < MAX_ATTEMPTS; attempt++) {
    if (attempt > 0) await sleep(500 * 2 ** (attempt - 1));
    const t0 = performance.now();
    let retriable = false;
    try {
      const ctrl = new AbortController();
      const timer = setTimeout(() => ctrl.abort(), timeoutMs);
      let res, text;
      try {
        res = await fetch(API_URL, {
          method: 'POST',
          headers: { Authorization: `Bearer ${key}`, 'Content-Type': 'application/json' },
          body,
          signal: ctrl.signal,
        });
        if (RETRY_STATUS.has(res.status)) {
          retriable = true;
          lastErr = new DroidJevError(`typesafe api ${res.status}`);
          continue;
        }
        text = await readCapped(res, ctrl); // timer still armed through body read
      } finally {
        clearTimeout(timer);
      }
      if (!res.ok) {
        throw new DroidJevError(`typesafe api ${res.status}: ${redact(text).slice(0, 300)}`);
      }
      let json;
      try {
        json = JSON.parse(text);
      } catch {
        throw new DroidJevError('typesafe api returned non-JSON body');
      }
      if (!json?.answers || typeof json.answers !== 'object') {
        throw new DroidJevError('typesafe api response missing answers');
      }
      return {
        answers: json.answers,
        usage: json.usage ?? null,
        model: json.model ?? MODEL,
        latencyMs: performance.now() - t0,
      };
    } catch (e) {
      // Only retry rate-limit/overload statuses and network-level failures
      // (fetch aborts and connection errors arrive as TypeError/DOMException).
      const networkish = e instanceof TypeError || e?.name === 'AbortError';
      if (!retriable && !networkish) throw e;
      lastErr = e;
    }
  }
  throw new DroidJevError(`typesafe api failed after ${MAX_ATTEMPTS} attempts: ${lastErr?.message || 'unknown'}`);
}

// --- strict answer validation (model output never becomes coordinates/commands) ---

export function validatedChoice(answer, validIds, questionId) {
  const bad = (why) => new DroidJevError(`invalid typesafe answer for "${questionId}" (${why}); no action executed`);
  if (!answer || typeof answer !== 'object') throw bad('missing answer');
  const { choice, probabilities } = answer;
  if (!validIds.includes(choice)) throw bad(`choice "${String(choice)}" not in offered options`);
  if (!probabilities || typeof probabilities !== 'object') throw bad('missing probabilities');
  const seen = new Set(validIds);
  for (const k of Object.keys(probabilities)) {
    if (!seen.has(k)) throw bad('probabilities contain options that were not offered');
    seen.delete(k);
  }
  if (seen.size > 0) throw bad('probabilities do not cover exactly the offered options');
  let sum = 0;
  for (const id of validIds) {
    const p = probabilities[id];
    if (!Number.isFinite(p) || p < 0 || p > 1) throw bad(`probability ${p} for "${id}" out of range`);
    sum += p;
  }
  if (Math.abs(sum - 1) > 0.02) throw bad(`probabilities sum to ${sum.toFixed(3)}`);
  const maxP = Math.max(...validIds.map((id) => probabilities[id]));
  if (probabilities[choice] < maxP - 1e-6) throw bad('chosen option is not the probability argmax');
  // Confidence must be a usable number if present; fall back to p(choice)
  // otherwise. A non-numeric value must never satisfy (or dodge) a threshold.
  const conf = Number(answer.confidence);
  const confidence = Number.isFinite(conf) && conf >= 0 && conf <= 1 ? conf : probabilities[choice];
  return { choice, probabilities, confidence };
}

export function validatedNoul(answer, questionId) {
  const bad = (why) => new DroidJevError(`invalid typesafe answer for "${questionId}" (${why})`);
  if (!answer || typeof answer !== 'object') throw bad('missing answer');
  const n = answer.noul;
  if (!Number.isFinite(n) || n < 0 || n > 1) throw bad(`noul ${n} out of range`);
  return { noul: n };
}
