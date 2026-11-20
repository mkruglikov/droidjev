# Plan 2 (next phase): replace appium's APKs with our own instrumentation APK

**Status: not started. Execute in a fresh session; this file is the complete
brief.** Branch off `master` after `drop-android-cli` merges; suggested branch
`own-instrumentation-apk`.

## Motivation (decided with the operator, 2026-11)

Company security policy objects to installing third-party APKs (appium's
`uiautomator2-server` + `io.appium.settings`, ~21 MB, run as instrumentation
with accessibility to everything). The decision, weighing three options:

1. keep appium pinned by SHA-256 — rejected as still third-party;
2. `adb shell uiautomator dump` (zero installs) — rejected as default: 1–3 s
   per dump vs ~20–50 ms, settle needs ≥2 dumps/step ⇒ measured-style
   estimate ~3–6× slower flows (step ~1.2 s → ~3–7 s);
3. **own micro-instrumentation APK (chosen)** — the same architecture every
   fast Android UI test uses: a resident instrumentation holding the
   UiAutomation connection. We control the source, the build, and the release.

Non-negotiable constraint: **keep the speed** (resident connection, ~50 ms
dumps) and **keep clipboard `copy`/`paste` working** (the only
arbitrary-Unicode input path; `--text` is charset-capped by
`SanitizeInputText`).

## Non-goals

- No `uiautomator dump` fallback mode in this phase (possible follow-up flag
  behind the `uia2Source` seam, layout.go).
- No prompt/golden changes: `testdata/golden/` must stay byte-identical.
- No behavior changes to agent/skills/find/snapshot.

## What uia2 provides today (all three must be covered)

1. `Source(serial)` — hierarchy XML over adb-forwarded HTTP
   (`uia2.go`; seam `var uia2Source = Source`, layout.go:~303).
2. `SetClipboard(serial, text)` — Android 10+ denies clipboard writes from
   unfocused processes; the write rides the `io.appium.settings` companion
   (uia2.go comment block). Used by `copy_<idx>` / `clip_<idx>` / `paste`
   ops in agent.go (`dev.SetClipboard`).
3. Lifecycle — `Ensure`/`Stop` (install pinned APKs, `am instrument` detached
   spawn, adb forward, resident across invocations, defensive force-stop of
   `com.android.cli.interact.instrumentation`).

## The APK

**One APK, zero dependencies** (no androidx, no appium, no test-runner
library — plain `android.app.Instrumentation` subclass):

- Package `dev.droidjev.instrument`. Manifest declares
  `<instrumentation android:name=".Runner"
  android:targetPackage="dev.droidjev.instrument"/>` in the app APK itself
  (single-APK self-instrumentation), plus an `InputMethodService` (see
  Clipboard). `minSdk 24` unless a test image forces higher.
- `Runner extends Instrumentation`: `getUiAutomation()` in `onCreate`
  (mirror appium's `ServerInstrumentation` — read their source first), then
  `onStart` runs the server loop on a thread. Never finishes (resident), like
  today's `am instrument -w` spawn.
- **Transport — abstract unix socket, not TCP:** listen on
  `localabstract:droidjev-instrument`; host forwards with
  `adb forward tcp:<localPort> localabstract:droidjev-instrument`.
  Abstract sockets are per-device ⇒ no device-port collisions at all; the
  local-side `PickPort` machinery in uia2.go is reused as-is.
- Line protocol (ASCII request line, then payload):
  - `PING` → `OK <version>` (replaces `/wd/hub/status`; readiness poll)
  - `DUMP` → `<n>\n<n bytes>` — length-prefixed XML (newlines are legal in
    XML; framing must not rely on lines). Cap enforced host-side at
    `uia2MaxBodyBytes` (16 MiB), same as today.
  - `CLIP <base64>` → `OK` or `ERR <msg>` (see Clipboard; blocks until the
    IME is connected, host deadline 3 s).
- **XML: hand-build from the AccessibilityNodeInfo tree — do NOT use
  `UiDevice.dumpWindowHierarchy()`** (fixed attribute set is wrong and we need
  `displayed`). CRITICAL details, all load-bearing for `ParseUia2Xml` +
  `BuildTable`:
  - Iterate `UiAutomation.getWindows()` (**all** windows, not just
    `getRootInActiveWindow()`) — IME overlay keys must appear in dumps;
    agent.go distinguishes "an IME's foreign keys from an in-app dial pad"
    from the dump contents. Multiple root `<node>`s directly under
    `<hierarchy>` are what the parser already expects from uia2.
  - Attribute names exactly: `class package text resource-id content-desc
    bounds clickable long-clickable scrollable checkable checked focusable
    focused selected password displayed` (note **hyphenated**
    `long-clickable` and `resource-id`). `bounds="[l,t][r,b]"` from the node
    rect. `displayed` = `isVisibleToUser()` (feeds `OffScreen` filtering,
    elements.go).
  - Root element: `<hierarchy rotation="N">` (rotation value ignored by the
    parser; include for parity).
  - Escape attribute values (`&` `<` `>` `"` `'`) — the parser decodes
    entities in a fixed order (`&amp;` LAST; see `decodeEntities`).
  - Root node's `package` feeds `RootPkg` (focused app) — keep it accurate.

### Clipboard (Android 10+)

Background clipboard set is allowed only for: the default IME, the focused
app, device/profile owner. Instrumentation is none of those (that's why
appium delegates to a companion). Our approach, host-driven:

1. Host reads `settings get secure default_input_method` (save it).
2. Host: `settings put secure default_input_method
   dev.droidjev.instrument/.Ime` (adb shell holds WRITE_SECURE_SETTINGS).
3. Host sends `CLIP <base64>`; the APK waits for the IME's connected flag
   (`InputMethodService.onCreate`), then `ClipboardManager.setPrimaryClip`
   from the IME context (IME uid is exempt), replies `OK`.
4. Host restores the saved IME (delete the key if it was unset).

**Task 0 before implementing this dance:** read `ClipboardHelper` in
appium-uiautomator2-server + the io.appium.settings receiver (MIT). If their
companion writes the clipboard through a simpler documented exemption,
replicate that instead — do not invent a third mechanism. Whatever is chosen:
if the write fails, surface an error (agent treats it as an infrastructure
step failure — same as today's behavior when the companion is missing).

ponytail: if droidjev dies between steps 2 and 4 the device keeps our IME as
default — cosmetic (keyboard still works); `droidjev snapshot` could warn if
it detects it. Note it in known-limits, don't build recovery machinery.

## Host-side Go changes (keep the diff a swap)

New file `instr.go` implementing the **same exported seam names** — `Ensure`,
`Stop`, `Source`, `SetClipboard`, plus the `Pinned` table and `InstallApk`
machinery (pin table shrinks from 3 assets to 1). Then delete `uia2.go`.
Unchanged because names match:

- layout.go `var uia2Source = Source` (consider renaming to
  `layoutSource`; one-line ripple, do it)
- agent.go `SetClipboard: SetClipboard` device-IO wiring
- main.go `withUia2` (rename to `withLayout` for honesty; one call site)
- `PickPort` / `Session` plumbing — local forward side is unchanged
- defensive force-stop of `com.android.cli.interact.instrumentation` stays
  (operators may still run that CLI)

Changes inside the provider: no WebDriver session ids (delete that state),
`/wd/hub/*` JSON ⇒ line protocol, forward args `tcp:6790` ⇒
`localabstract:droidjev-instrument`, install = 1 APK, `-e disableAnalytics`
goes away. Every socket read carries a deadline (match today's 15–20 s caps);
everything printed still passes `Redact`; spawns stay argument-array `Run()`.

Pin the asset exactly like today:
`Pinned.Assets.Instrument = {URL: https://github.com/<repo>/releases/download/vX.Y.Z/droidjev-instrument.apk, Sha256: …}`
— same verified-install invariant (cache key carries the sha, tampered/stale
files never install). Our APK rides our own GitHub Releases: the trust chain
equals the droidjev binary's. That is the security story.

## Build & release (no Gradle)

`instrument/` in-repo: `AndroidManifest.xml`, `res/xml/method.xml` (IME
descriptor), `java/dev/droidjev/instrument/{Runner,Ime,XmlDump}.java`
(~200–300 lines total). CI (new job in ci.yml, build-tools only):

```
javac -source 8 -target 8 -classpath $SDK/platforms/android-34/android.jar instrument/java/**/*.java -d classes
d8 --release --min-api 24 --lib $SDK/platforms/android-34/android.jar --output out classes/dev/droidjev/instrument/*.class
aapt2 compile --dir instrument/res -o res.zip
aapt2 link -I $SDK/platforms/android-34/android.jar --manifest instrument/AndroidManifest.xml -R res.zip -F unsigned.apk
(cd out && zip -j ../unsigned.apk classes.dex)   # or aapt2 add
zipalign -f 4 unsigned.apk aligned.apk && apksigner sign --ks ks aligned.apk
```

(Exact flags get sorted out during implementation; the point is: **javac +
d8 + aapt2 + apksigner, no Gradle, no wrapper, no third-party deps**.)

**Signing:** keystore as a GitHub Actions secret (base64) for releases;
local dev generates a gitignored debug keystore via a small script. Do NOT
commit the private key — a committed key lets anyone sign same-package
upgrades (the signature is what pins the resident instrument to us:
`install -r` refuses signature mismatches). Same-signature continuity is why
the release keystore must be stable across releases.

release.yml: add `droidjev-instrument.apk` + `.sha256` assets (mirrors the
binary's pattern). Pin-table bump = release tag + sha edited together.

## Testing

- Java side: `am instrument -e selftest true dev.droidjev.instrument/.Runner`
  mode that dumps its own hierarchy and validates framing — the one runnable
  check (ponytail); real coverage comes from e2e.
- Go side: protocol codec tests (length-prefix framing, base64 clip arg,
  error paths) as plain unit tests; `InstallApk` tests already exist and must
  keep passing against the 1-asset pin shape; XML passthrough is already
  covered by layout_test goldens — feed one real APK dump through
  `ParseUia2Xml` in a fixture test.
- E2E (`DROIDJEV_E2E=1`): unchanged suite; add env override
  `DROIDJEV_INSTRUMENT_APK=/path/to/local.apk` so dev builds skip the release
  download.
- Parity gate before flipping the pin: on one screen each (Settings main,
  a text field with IME open, the app list), diff old-appium XML vs our XML
  attribute-by-attribute; the element tables must match row-for-row.

## Docs to update when done

docs/security.md (APK provenance: own release asset, hash-pinned, key in CI
secrets, auditable end to end), README (first-use download text: 21 MB/three
APKs ⇒ one small APK), docs/how-it-works.md (provider), docs/speed.md
(re-measure the table; expect equal-or-better dump latency), AGENTS.md
(uia2.go bullet ⇒ instr.go, invariants wording), skill/SKILL.md (the
"resident uiautomator2 server" mention), docs/known-limits.md (IME-switch
note).

## Execution order

1. Task 0: read appium sources (ServerInstrumentation lifecycle, XML
   attribute mapping incl. multi-window, ClipboardHelper mechanism).
2. Write the APK (Runner/Ime/XmlDump + manifest); build locally with the
   javac/d8/aapt2 line; `am instrument` by hand; PING/DUMP/CLIP via
   `adb forward` + nc.
3. `instr.go` host provider; swap seams; delete uia2.go.
4. Unit tests + parity gate + full e2e on API 30/33/36 images.
5. CI job + release asset + pin-table bump.
6. Docs; merge.

Estimate: ~1–2 focused days. Known risk: the clipboard IME dance on OEM
images (emulators are clean; note anything odd in known-limits).

## Research findings and execution record (2026-11-20, this branch)

Implemented on `own-instrumentation-apk`. Four parallel research passes
(appium lifecycle, XML format, clipboard mechanism, build/IME pitfalls — all
verified against appium and AOSP sources, plus live probes on the test
emulator) produced corrections the brief above did not anticipate:

1. **The clipboard premise was wrong — no IME needed.** AOSP's
   `ClipboardService` allows `OP_WRITE_CLIPBOARD` **unconditionally** (only
   reads are IME/focus-gated; verified in android-14 `clipboardAccessAllowed`,
   constant through android-15, and empirically by appium's own
   `SetClipboard` writing directly from the instrumentation process — the
   io.appium.settings companion implements *reads* only). Per this file's own
   Task-0 rule ("replicate the simpler mechanism"), the whole IME dance was
   dropped: `CLIP` is a direct `ClipboardManager.setPrimaryClip` in the
   Runner, and the manifest carries no service at all. The IME *read* recipe
   (`ime enable`/`ime set` + bound receiver; binding is client-driven, works
   with hw.keyboard) is documented here for a future read need.
2. **Plain Instrumentation must call `start()` itself.** Nothing schedules
   `onStart` otherwise (AndroidJUnitRunner does it for appium). Symptom
   without it: process alive, `onCreate` ran, no server, no crash. Also:
   `am instrument -w` is mandatory (`getUiAutomation()` returns null without
   a watcher), and the `-w` adb client must stay alive or UiAutomation dies
   with it — the detached spawn does exactly that.
3. **`getWindows()` is empty by default**: the UiAutomation service info lacks
   `FLAG_RETRIEVE_INTERACTIVE_WINDOWS`; the Runner sets it.
4. **Appium's default source is single-window** (`getRootInActiveWindow`),
   not multi-window — our captures confirm one root per dump. Ours emits ALL
   windows with the active one first (RootPkg stays the focused app): the
   deliberate improvement this plan wanted (IME overlay keys), verified not
   to change the element tables on parity screens.
5. **Parity details adopted from appium v10.6.6 sources**: invisible children
   skipped (roots never); bounds = node ∩ real display ∩ window ∩ scrollable
   ancestor; depth cap 70; slider `text` from `RangeInfo` (`"197.0"`-style);
   captured toasts injected as root-level `android.widget.Toast` nodes;
   class-named tags; parser-irrelevant extras omitted. **Display rect must be
   the real panel size** — `DisplayMetrics` reports the bar-inset app area
   (2761 vs 2992 px) and would clip every bounds attribute; `getRealSize`
   does not.
6. **Build**: javac+d8+aapt2+zipalign+apksigner with no Gradle works; aapt2
   36 dropped `-F` (use `-o`); `LocalSocket` lives in `android.net`.

### Verification on this branch

- **Parity gate**: same-minute captures (home, Settings main) through both
  providers → identical element tables row-for-row; 65 unique-key elements
  bounds-compared, 0 diffs.
- **Live**: first-use install path (env override), `snapshot` at **18 ms per
  dump** (appium: 20–50 ms), full `snapshot` invocation 0.17 s; both e2e
  scenarios (`TestE2EImeiPaste` copy→paste through the new CLIP,
  `TestE2ECreateContact`) PASS. APK: **12.7 KB**, one APK, no activities, no
  services.
- **Host swap**: `instr.go` (same seam names: `Ensure`/`Stop`/`Source`/
  `SetClipboard`; `PickPort`/`InstallApk` machinery unchanged), `uia2.go`
  deleted, `uia2Source`→`layoutSource`, `withUia2`→`withLayout`, forward is
  `localabstract:` per-device,WebDriver sessions gone. `Ensure` defensively
  displaces the android-CLI instrumentation *and leftover appium servers*
  (upgrading operators have them resident).

### Release coordination (operator TODO before tagging v0.6.0)

1. Generate a long-lived release keystore; add repo secrets
   `DROIDJEV_KEYSTORE_B64` (base64 keystore), `DROIDJEV_KEYSTORE_PASSWORD`,
   `DROIDJEV_KEY_ALIAS`. The private key never enters the repo — same-package
   upgrades require the same signature, so this key must be stable forever.
2. Tag `v0.6.0`: release.yml builds the APK with that keystore and attaches
   `droidjev-instrument.apk` + `.sha256` alongside the binary. The pin table
   (`instr.go` `Pinned`) already points at the v0.6.0 asset URL.
3. First-party assets carry no hardcoded sha (empty `Sha256` allowed): the
   release itself is the trust root — the same channel that delivers the
   droidjev binary — and it sidesteps the self-reference (a tag's pin cannot
   know the sha of an APK built from that same tag). The cache key uses the
   release tag, so stale files never install. Third-party assets keep the
   mandatory sha256.
4. `DROIDJEV_INSTRUMENT_APK=/path/to.apk` installs a local APK for dev/CI
   builds against debug-keystore APKs.
