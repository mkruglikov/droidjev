// appium-uiautomator2-server as a layout provider: a resident on-device
// instrumentation holding the accessibility connection and serving hierarchy
// dumps over adb-forwarded HTTP (~50ms per dump vs ~1.1s for the android CLI).
// It also sets the device clipboard (SetClipboard): Android 10+ silently denies
// clipboard writes from unfocused processes, so the write rides a broadcast
// into the io.appium.settings companion APK, installed alongside the server.
//
// UiAutomation is exclusive device-wide. Entering this provider force-stops the
// android CLI's resident instrumentation (com.android.cli.interact.
// instrumentation) or the server cannot connect. The server is kept resident
// ACROSS droidjev invocations (~1.2s to boot is too costly per command); while
// it lives, the android CLI's own layout commands stay displaced — Stop (also
// used internally for restarts) hands UiAutomation back.
//
// Ported from the Node implementation; messages are kept word for word.
// Unlike the original event loop, Go callers can be concurrent, so the
// session/ensured maps are mutex-guarded (fields of a returned *Session are
// only mutated under it).
package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	uia2Pkg     = "io.appium.uiautomator2.server"
	uia2TestPkg = uia2Pkg + ".test"
	uia2Runner  = "androidx.test.runner.AndroidJUnitRunner"
	// The on-device server always listens here; only the LOCAL side of the adb
	// forward is per-serial, so two devices in one process cannot collide.
	uia2DevicePort         = 6790
	uia2SettingsPkg        = "io.appium.settings"
	uia2CLIInstrumentation = "com.android.cli.interact.instrumentation"

	uia2MaxBodyBytes  = 16 << 20  // hierarchy dumps are ~100KB; cap runaway bodies
	uia2MaxApkBytes   = 256 << 20 // the pinned helper APKs are ~21MB total
	uia2DefaultHTTPMs = int64(15000)
)

// Session is one serial's local-side state: the forwarded port and the current
// WebDriver session id ("" when none).
type Session struct {
	Port      int
	SessionID string
}

var (
	uia2Mu       sync.Mutex
	uia2Sessions = map[string]*Session{}
	uia2Ensured  = map[string]bool{} // serials whose forward + server are known-good
)

// PickPort allocates the LOCAL side of a serial's adb forward. The first serial
// keeps the default port (every existing setup is unchanged); later ones get a
// stable hash-derived port in 6790-6853, skipping anything in use. Per-process
// only: two separate CLI processes can still pick the same local port, which is
// harmless while each drives one device.
func PickPort(used []int, serial string) int {
	if len(used) == 0 {
		return uia2DevicePort
	}
	sum := sha256.Sum256([]byte(serial))
	h := hex.EncodeToString(sum[:])[:8]
	n, _ := strconv.ParseUint(h, 16, 64) // 8 hex chars always parse
	start := uia2DevicePort + int(n%64)
	for i := 0; i < 64; i++ {
		port := uia2DevicePort + ((start - uia2DevicePort + i) % 64)
		if !slices.Contains(used, port) {
			return port
		}
	}
	return uia2DevicePort
}

// SessionFor returns the serial's session, allocating its port on first use.
// The returned pointer's fields are only safe to read outside uia2Mu for the
// single-threaded CLI path; internal mutations hold the lock.
func SessionFor(serial string) *Session {
	uia2Mu.Lock()
	defer uia2Mu.Unlock()
	if s, ok := uia2Sessions[serial]; ok {
		return s
	}
	used := make([]int, 0, len(uia2Sessions))
	for _, s := range uia2Sessions {
		used = append(used, s.Port)
	}
	s := &Session{Port: PickPort(used, serial)}
	uia2Sessions[serial] = s
	return s
}

func uia2SessionID(serial string) string {
	uia2Mu.Lock()
	defer uia2Mu.Unlock()
	if s := uia2Sessions[serial]; s != nil {
		return s.SessionID
	}
	return ""
}

// uia2SetSessionID mirrors `sessionFor(serial).sessionId = id`; a missing entry
// needs no recreation — the next Ensure allocates one with an empty id anyway.
func uia2SetSessionID(serial, id string) {
	uia2Mu.Lock()
	defer uia2Mu.Unlock()
	if s := uia2Sessions[serial]; s != nil {
		s.SessionID = id
	}
}

func uia2IsEnsured(serial string) bool {
	uia2Mu.Lock()
	defer uia2Mu.Unlock()
	return uia2Ensured[serial]
}

func uia2MarkEnsured(serial string) {
	uia2Mu.Lock()
	defer uia2Mu.Unlock()
	uia2Ensured[serial] = true
}

// HttpJson calls the resident server over the serial's forwarded port. The
// response body is returned raw so callers extract `.value`, `.sessionId`,
// `.value.error` themselves, exactly like the JS. Non-2xx is an error.
func HttpJson(serial, path, method, body string, timeoutMs int64) (json.RawMessage, error) {
	if timeoutMs <= 0 {
		timeoutMs = uia2DefaultHTTPMs
	}
	if method == "" {
		method = http.MethodGet
	}
	url := fmt.Sprintf("http://127.0.0.1:%d%s", SessionFor(serial).Port, path)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, NewDroidJevError(fmt.Sprintf("uia2 request failed: %v", err))
	}
	if body != "" {
		req.Header.Set("content-type", "application/json")
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return nil, NewDroidJevError(fmt.Sprintf("uia2 request failed: %v", err))
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, NewDroidJevError(fmt.Sprintf("uia2 HTTP %d on %s", res.StatusCode, path))
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, uia2MaxBodyBytes))
	if err != nil {
		return nil, NewDroidJevError(fmt.Sprintf("uia2 HTTP read failed on %s: %v", path, err))
	}
	if !json.Valid(b) {
		return nil, NewDroidJevError(fmt.Sprintf("uia2 HTTP invalid json on %s", path))
	}
	return json.RawMessage(b), nil
}

// PinnedAsset is one release asset plus its SHA-256, verified before anything
// reaches a device.
type PinnedAsset struct {
	URL    string
	Sha256 string
}

// PinnedTags are the release tags the assets came from.
type PinnedTags struct{ Server, Settings string }

// PinnedAssets are the three helper APKs.
type PinnedAssets struct{ Server, Test, Settings PinnedAsset }

// PinnedSet is the whole pin table. Bump deliberately (edit url and sha
// together): `latest` is not a version, and a cached or tampered file must
// never install silently.
type PinnedSet struct {
	Tag    PinnedTags
	Assets PinnedAssets
}

// Pinned is copied verbatim from the Node implementation.
var Pinned = PinnedSet{
	Tag: PinnedTags{Server: "v10.6.6", Settings: "v8.2.1"},
	Assets: PinnedAssets{
		Server: PinnedAsset{
			URL:    "https://github.com/appium/appium-uiautomator2-server/releases/download/v10.6.6/appium-uiautomator2-server-v10.6.6.apk",
			Sha256: "8ff760a2a86b487f53090fbdcd5b0360e67d02bb811887d527a9557b0d59c80d",
		},
		Test: PinnedAsset{
			URL:    "https://github.com/appium/appium-uiautomator2-server/releases/download/v10.6.6/appium-uiautomator2-server-debug-androidTest.apk",
			Sha256: "e6f729287d72351388fd66597c35b69988b9091a7011954bf92a55d416cb1e1c",
		},
		Settings: PinnedAsset{
			URL:    "https://github.com/appium/io.appium.settings/releases/download/v8.2.1/settings_apk-debug.apk",
			Sha256: "e4024b3ee348929252bcaf5eb422bcab9e4ace96703499a84f3332281c03375f",
		},
	},
}

// Sha256Matches reports whether b hashes to the pinned digest (case-insensitive).
func Sha256Matches(b []byte, hexDigest string) bool {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]) == strings.ToLower(hexDigest)
}

// InstallDeps are the injectable seams of InstallApk; nil fields take the real
// implementations.
type InstallDeps struct {
	Fetch      func(url string) ([]byte, error)
	CacheDir   string
	AdbInstall func(args []string) (RunResult, error)
}

// uia2AssetBase is the asset filename without the .apk suffix: the cache key's
// version-independent half.
func uia2AssetBase(url string) string {
	base := url[strings.LastIndexByte(url, '/')+1:]
	if len(base) >= 4 && strings.EqualFold(base[len(base)-4:], ".apk") {
		base = base[:len(base)-4]
	}
	return base
}

func uia2ShaPrefix(sha string, n int) string {
	if len(sha) < n {
		return sha
	}
	return sha[:n]
}

func uia2CacheDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".cache", "droidjev", "uia2")
}

// uia2FetchURL is the default Fetch: one bounded GET.
func uia2FetchURL(url string) ([]byte, error) {
	base := uia2AssetBase(url)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, NewDroidJevError(fmt.Sprintf("download of %s.apk failed: %v", base, err))
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return nil, NewDroidJevError(fmt.Sprintf("download of %s.apk failed: %v", base, err))
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, NewDroidJevError(fmt.Sprintf("download of %s.apk failed: HTTP %d", base, res.StatusCode))
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, uia2MaxApkBytes))
	if err != nil {
		return nil, NewDroidJevError(fmt.Sprintf("download of %s.apk failed: %v", base, err))
	}
	return b, nil
}

// InstallApk downloads (or reuses) one pinned asset, verifies its hash, and
// installs it. The cache key carries the sha, so a new pin can never pick up a
// stale file; a corrupt cache entry is re-downloaded instead of failing the run.
func InstallApk(serial string, asset PinnedAsset, deps InstallDeps) (string, error) {
	fetch := deps.Fetch
	if fetch == nil {
		fetch = uia2FetchURL
	}
	cacheDir := deps.CacheDir
	if cacheDir == "" {
		cacheDir = uia2CacheDir()
	}
	adbInstall := deps.AdbInstall
	if adbInstall == nil {
		adbInstall = func(args []string) (RunResult, error) {
			return Run(adb, args, RunOpts{TimeoutMs: 180000})
		}
	}

	base := uia2AssetBase(asset.URL)
	file := filepath.Join(cacheDir, base+"-"+uia2ShaPrefix(asset.Sha256, 8)+".apk")

	var buf []byte
	if cached, err := os.ReadFile(file); err == nil && Sha256Matches(cached, asset.Sha256) {
		buf = cached
	}
	if buf == nil {
		downloaded, err := fetch(asset.URL)
		if err != nil {
			return "", err
		}
		got := sha256.Sum256(downloaded)
		gotHex := hex.EncodeToString(got[:])
		if !Sha256Matches(downloaded, asset.Sha256) {
			return "", NewDroidJevError(fmt.Sprintf(
				"download of %s.apk failed sha256: expected %s, got %s",
				base, uia2ShaPrefix(asset.Sha256, 12), uia2ShaPrefix(gotHex, 12),
			))
		}
		if err := os.MkdirAll(cacheDir, 0o755); err != nil {
			return "", NewDroidJevError(fmt.Sprintf("uia2 cache write failed: %v", err))
		}
		if err := os.WriteFile(file, downloaded, 0o644); err != nil {
			return "", NewDroidJevError(fmt.Sprintf("uia2 cache write failed: %v", err))
		}
	}

	r, err := adbInstall([]string{"-s", serial, "install", "-r", "-t", file})
	if err != nil {
		return "", err
	}
	if !strings.Contains(string(r.Stdout)+r.Stderr, "Success") {
		msg := r.Stderr
		if msg == "" {
			msg = string(r.Stdout)
		}
		return "", NewDroidJevError(fmt.Sprintf(
			"adb install of %s.apk failed: %s", base, UTF16Truncate(JSTrim(msg), 200),
		))
	}
	return file, nil
}

// uia2Installed requires both the server and the settings companion: the
// server "succeeds" at clipboard writes even when the companion is missing.
func uia2Installed(serial string) (bool, error) {
	for _, pkg := range []string{uia2Pkg, uia2SettingsPkg} {
		r, err := Shell(serial, []string{"pm", "path", pkg}, RunOpts{TimeoutMs: 8000})
		if err != nil {
			return false, err
		}
		if !(r.Code == 0 && strings.Contains(string(r.Stdout), "package:")) {
			return false, nil
		}
	}
	return true, nil
}

// uia2StartInstrument spawns `am instrument -w` detached: it blocks for the
// server's lifetime, and the server must outlive this process (Stop force-stops
// it). Run cannot be used here (it waits); a Start failure is swallowed — the
// JS attaches a no-op error listener — and surfaces as a readiness timeout.
func uia2StartInstrument(serial string) {
	cmd := exec.Command(
		adb, "-s", serial, "shell", "am", "instrument", "-w",
		"-e", "disableAnalytics", "true", uia2TestPkg+"/"+uia2Runner,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err == nil {
		_ = cmd.Process.Release()
	}
}

func uia2WaitReady(serial string, timeoutMs int64) error {
	deadline := time.Now().UnixMilli() + timeoutMs
	port := SessionFor(serial).Port
	for {
		if _, err := HttpJson(serial, "/wd/hub/status", "GET", "", 1500); err == nil {
			return nil
		}
		if time.Now().UnixMilli() > deadline {
			return NewDroidJevError(fmt.Sprintf("uia2 server did not become ready on port %d within 20s", port))
		}
		Sleep(250)
	}
}

// Ensure makes the forward and the resident server ready for this serial.
func Ensure(serial string) error {
	if uia2IsEnsured(serial) {
		return nil
	}
	port := SessionFor(serial).Port
	// The local side is per-serial; the device side is always 6790.
	if _, err := Run(adb, []string{
		"-s", serial, "forward",
		fmt.Sprintf("tcp:%d", port), fmt.Sprintf("tcp:%d", uia2DevicePort),
	}, RunOpts{TimeoutMs: 8000}); err != nil {
		return err
	}
	if _, err := HttpJson(serial, "/wd/hub/status", "GET", "", 1500); err == nil {
		uia2MarkEnsured(serial)
		return nil // already resident from a previous step/run
	}
	ok, err := uia2Installed(serial)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintf(os.Stderr,
			"# uia2: installing pinned helper APKs (uia2 %s, settings %s; one-time, ~21MB)\n",
			Pinned.Tag.Server, Pinned.Tag.Settings)
		for _, asset := range []PinnedAsset{Pinned.Assets.Server, Pinned.Assets.Test, Pinned.Assets.Settings} {
			if _, err := InstallApk(serial, asset, InstallDeps{}); err != nil {
				return err
			}
		}
	}
	// The CLI's resident instrumentation owns UiAutomation; it must be gone
	// before the server starts, or the server binds a dead connection.
	_, _ = Shell(serial, []string{"am", "force-stop", uia2CLIInstrumentation}, RunOpts{TimeoutMs: 8000})
	uia2StartInstrument(serial)
	if err := uia2WaitReady(serial, 20000); err != nil {
		return err
	}
	uia2MarkEnsured(serial)
	return nil
}

// uia2StringField extracts a string field from a raw JSON object; "" when the
// object or field is absent or not a string.
func uia2StringField(raw json.RawMessage, key string) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	v, ok := m[key]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(v, &s) != nil {
		return ""
	}
	return s
}

func uia2ValueString(raw json.RawMessage) string {
	var top struct {
		Value json.RawMessage `json:"value"`
	}
	if json.Unmarshal(raw, &top) != nil {
		return ""
	}
	var s string
	if json.Unmarshal(top.Value, &s) != nil {
		return ""
	}
	return s
}

func uia2ValueMessage(raw json.RawMessage) string {
	var top struct {
		Value json.RawMessage `json:"value"`
	}
	if json.Unmarshal(raw, &top) != nil {
		return ""
	}
	return uia2StringField(top.Value, "message")
}

func uia2EnsureSession(serial string) (string, error) {
	if sid := uia2SessionID(serial); sid != "" {
		return sid, nil
	}
	raw, err := HttpJson(serial, "/wd/hub/session", "POST",
		`{"capabilities":{"alwaysMatch":{"platformName":"android","appium:automationName":"UiAutomator2"}}}`,
		uia2DefaultHTTPMs)
	if err != nil {
		return "", err
	}
	var top struct {
		SessionID string          `json:"sessionId"`
		Value     json.RawMessage `json:"value"`
	}
	sid := ""
	if json.Unmarshal(raw, &top) == nil {
		sid = top.SessionID
		if sid == "" {
			sid = uia2StringField(top.Value, "sessionId")
		}
	}
	if sid == "" {
		msg := uia2ValueMessage(raw)
		if msg == "" {
			msg = "no session id"
		}
		return "", NewDroidJevError("uia2 session not created: " + UTF16Truncate(msg, 200))
	}
	uia2SetSessionID(serial, sid)
	return sid, nil
}

// uia2SourceOnce fetches the hierarchy XML for the current session.
func uia2SourceOnce(serial string) (string, error) {
	sid, err := uia2EnsureSession(serial)
	if err != nil {
		return "", err
	}
	raw, err := HttpJson(serial, "/wd/hub/session/"+sid+"/source", "GET", "", 20000)
	if err != nil {
		return "", err
	}
	xml := uia2ValueString(raw)
	if !strings.Contains(xml, "<hierarchy") {
		return "", NewDroidJevError("uia2 source returned no hierarchy xml")
	}
	return xml, nil
}

// Source fetches the hierarchy XML. One full restart on failure: the server or
// session may have died mid-run (or another tool stole UiAutomation).
func Source(serial string) (string, error) {
	for attempt := 0; ; attempt++ {
		if err := Ensure(serial); err != nil {
			return "", err
		}
		xml, err := uia2SourceOnce(serial)
		if err == nil {
			return xml, nil
		}
		uia2SetSessionID(serial, "")
		if attempt >= 1 {
			return "", err
		}
		Stop(serial)
	}
}

// uia2ClipboardBody is the set_clipboard POST payload; a struct keeps the field
// order deterministic (content, then contentType, like the JS literal).
type uia2ClipboardBody struct {
	Content     string `json:"content"`
	ContentType string `json:"contentType"`
}

// SetClipboard writes any UTF-8 text via base64 over HTTP — no shell, no
// charset limits. The server "succeeds" even when the companion APK is
// missing, so the caller cannot detect a lost write; uia2Installed therefore
// requires both APKs.
func SetClipboard(serial, text string) error {
	payload, err := json.Marshal(uia2ClipboardBody{
		Content:     base64.StdEncoding.EncodeToString([]byte(text)),
		ContentType: "plaintext",
	})
	if err != nil {
		return NewDroidJevError(fmt.Sprintf("uia2 set_clipboard failed: %v", err))
	}
	for attempt := 0; ; attempt++ {
		if err := Ensure(serial); err != nil {
			return err
		}
		err := uia2SetClipboardOnce(serial, string(payload))
		if err == nil {
			return nil
		}
		uia2SetSessionID(serial, "")
		if attempt >= 1 {
			return err
		}
		Stop(serial)
	}
}

func uia2SetClipboardOnce(serial, payload string) error {
	sid, err := uia2EnsureSession(serial)
	if err != nil {
		return err
	}
	raw, err := HttpJson(serial, "/wd/hub/session/"+sid+"/appium/device/set_clipboard", "POST", payload, 20000)
	if err != nil {
		return err
	}
	var top struct {
		Value json.RawMessage `json:"value"`
	}
	if json.Unmarshal(raw, &top) == nil {
		if e := uia2StringField(top.Value, "error"); e != "" {
			msg := uia2StringField(top.Value, "message")
			if msg == "" {
				msg = e
			}
			return NewDroidJevError("uia2 set_clipboard failed: " + UTF16Truncate(msg, 200))
		}
	}
	return nil
}

// Stop hands UiAutomation back: forget the serial's session/forward state and
// force-stop both the server and (defensively) the CLI instrumentation.
func Stop(serial string) {
	uia2Mu.Lock()
	delete(uia2Sessions, serial)
	delete(uia2Ensured, serial)
	uia2Mu.Unlock()
	for _, pkg := range []string{uia2Pkg, uia2CLIInstrumentation} {
		_, _ = Shell(serial, []string{"am", "force-stop", pkg}, RunOpts{TimeoutMs: 8000})
	}
}
