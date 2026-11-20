# Speed

| cost | typical (Pixel-class AVD) |
|---|---|
| `adb devices` | ~14 ms |
| first dump after instrumentation start | ~1.5–2.5 s, once — it stays resident |
| layout dump (resident instrumentation) | **~18–40 ms** |
| TypeSafe request (connection pre-warmed) | ~0.3–0.4 s |
| `adb shell input tap` | ~0.1–0.3 s |
| post-action settle | adaptive: ~2 dumps (~120 ms) on a still screen, up to 600 ms under entry animations |
| **typical act step** | **~1.2 s** (default) / ~1.1 s (`--no-animations`) |
| "open Settings" from home | **~5.3 s** (default) / ~4.6–5.7 s (`--no-animations`, path-dependent) |
| `find` in a long list | ~0.6 s per iteration, no tokens |
| vision fallback step (blind screen only) | screenshot + downscale ~0.2–0.5 s, request larger/slower (image tokens) |

The hot loop never spawns anything but `adb`; AVD auto-boots (cold path)
spawn the SDK `emulator` binary directly — no `android` CLI anywhere. Layout
dumps come from one engine: a resident first-party instrumentation APK
(`instrument/` in the repo, ~13 KB, served from droidjev's own releases) —
a persistent on-device instrumentation holding the accessibility connection,
queried over an adb-forwarded abstract unix socket, so a dump is one local
call (~20 ms measured) instead of a ~1.1 s CLI spawn. The APK is downloaded
and installed on first use (cached in `~/.cache/droidjev/instrument`); the
first droidjev command starts the instrumentation and leaves it **resident
across commands** — later commands skip the ~1.5–2.5 s start.
UiAutomation is exclusive device-wide: while it lives, other resident
instrumentations (the `android` CLI's layout tool, or an older droidjev's
appium server) stay displaced — both are force-stopped on start
(`adb shell am force-stop dev.droidjev.instrument` hands it back).
