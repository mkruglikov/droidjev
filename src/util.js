// Shared helpers. Security rules live here so every caller inherits them:
// - subprocesses are spawned as argument arrays (never through a shell)
// - every subprocess has a timeout and bounded output collection
// - anything headed for logs/errors passes through redact()
import { spawn } from 'node:child_process';

export class DroidJevError extends Error {
  constructor(message, { exitCode = 3 } = {}) {
    super(message);
    this.name = 'DroidJevError';
    this.exitCode = exitCode;
  }
}

export const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// Defense in depth: no string we ever print may contain a bearer token.
// Over-redacts (may eat a few following words) rather than under-redact;
// handles multi-line header values that fetch quotes in error messages.
export function redact(s) {
  return String(s).replace(/Bearer\s+[\s\S]*?(?="|$)/gi, 'Bearer [REDACTED]');
}

// Human-readable install hints for missing external tools.
const INSTALL_HINTS = {
  adb: '"adb" is not installed or not in PATH — install Android platform-tools:\n  https://developer.android.com/tools/releases/platform-tools',
  android:
    'the "android" CLI is not installed or not in PATH — install it:\n  https://developer.android.com/cli/install\n(only auto-booting an AVD needs it; pass --device to target a running emulator)',
  zsh: '"zsh" not found',
  bash: '"bash" not found',
};

export function installHint(file) {
  return INSTALL_HINTS[file] || null;
}

// Run a file with an argument array. Resolves with {code, stdout, stderr};
// never throws on non-zero exit (callers decide), only on spawn/timeout failure.
export function run(file, args, { timeoutMs = 15000, maxBytes = 32 * 1024 * 1024 } = {}) {
  return new Promise((resolve, reject) => {
    if (typeof file !== 'string' || !Array.isArray(args) || args.some((a) => typeof a !== 'string')) {
      reject(new DroidJevError(`internal: run() requires a file and a string[] args array`));
      return;
    }
    const child = spawn(file, args, { stdio: ['ignore', 'pipe', 'pipe'] });
    const out = [],
      err = [];
    let outBytes = 0,
      errBytes = 0,
      settled = false;
    const timer = setTimeout(() => {
      child.kill('SIGKILL');
      fail(new DroidJevError(`"${file}" timed out after ${timeoutMs}ms`));
    }, timeoutMs);
    const fail = (e) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      reject(e);
    };
    child.stdout.on('data', (c) => {
      outBytes += c.length;
      if (outBytes <= maxBytes) out.push(c);
    });
    child.stderr.on('data', (c) => {
      errBytes += c.length;
      if (errBytes <= maxBytes) err.push(c);
    });
    child.on('error', (e) => {
      const hint = e?.code === 'ENOENT' ? installHint(file) : null;
      fail(new DroidJevError(hint || `failed to start "${file}": ${e.message}`));
    });
    child.on('close', (code) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      resolve({
        code,
        stdout: Buffer.concat(out).toString('utf8'),
        stderr: Buffer.concat(err).toString('utf8'),
      });
    });
  });
}
