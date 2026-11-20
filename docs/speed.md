# Speed

| cost | typical (Pixel-class AVD) |
|---|---|
| `adb devices` | ~14 ms |
| first dump after server boot (start + session) | ~1.2 s, once — the server stays resident |
| layout dump (resident uia2 server) | **~20–50 ms** |
| TypeSafe request (connection pre-warmed) | ~0.3–0.4 s |
| `adb shell input tap` | ~0.1–0.3 s |
| post-action settle | adaptive: ~2 dumps (~120 ms) on a still screen, up to 600 ms under entry animations |
| **typical act step** | **~1.2 s** (default) / ~1.1 s (`--no-animations`) |
| "open Settings" from home | **~5.3 s** (default) / ~4.6–5.7 s (`--no-animations`, path-dependent) |
| `find` in a long list | ~0.6 s per iteration, no tokens |
| vision fallback step (blind screen only) | screenshot + downscale ~0.2–0.5 s, request larger/slower (image tokens) |

The hot loop never spawns anything but `adb`; AVD auto-boots (cold path)
spawn the SDK `emulator` binary directly — no `android` CLI anywhere. Layout
dumps come from one engine: a resident
[appium-uiautomator2-server](https://github.com/appium/appium-uiautomator2-server)
— a persistent on-device instrumentation holding the accessibility
connection, queried over an adb-forwarded HTTP port, so a dump is one local
call (~50 ms) instead of a ~1.1 s CLI spawn. The three APKs (server, test,
settings companion) are downloaded and installed on first use (~21 MB, cached
in `~/.cache/droidjev`); the first droidjev command starts the server and
leaves it **resident across commands** — later commands skip the ~1.2 s boot.
UiAutomation is exclusive device-wide: while the server lives, other resident
instrumentations (e.g. the `android` CLI's layout tool, if you still use it)
stay displaced (`adb shell am force-stop
io.appium.uiautomator2.server` hands it back).
