// The own-instrumentation layout provider: a ~13KB APK (instrument/) holding
// the UiAutomation connection as a resident instrumentation, serving hierarchy
// dumps and clipboard writes over an abstract unix socket
// (localabstract:droidjev-instrument) reached through an adb-forwarded local
// port. This replaces the appium-uiautomator2-server stack (21MB, three
// third-party APKs) with first-party code whose whole source lives in this
// repository — the security story (docs/security.md).
//
// UiAutomation is exclusive device-wide. Entering this provider force-stops
// every other resident instrumentation (the android CLI's, and appium's server
// from earlier droidjev versions — an operator upgrading mid-session has it
// resident) or ours cannot connect. Ours is kept resident ACROSS droidjev
// invocations (the ~1.5s start is too costly per command); Stop (also used
// internally for restarts) hands UiAutomation back.
//
// Unlike the HTTP/WebDriver provider it replaced, there are no session ids and
// no JSON: one line protocol (PING/DUMP/CLIP) with length-prefixed DUMP
// bodies, one fresh connection per request so a restarted instrumentation can
// never be reached through a stale socket.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net"
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
	instrPkg    = "dev.droidjev.instrument"
	instrRunner = instrPkg + "/" + instrPkg + ".Runner"
	// The abstract socket the instrumentation listens on; only the LOCAL side
	// of the adb forward is per-serial, so two devices cannot collide.
	instrSocket = "localabstract:droidjev-instrument"

	uia2DevicePort = 6790 // the default LOCAL forward port (first serial)

	instrMaxBodyBytes   = 16 << 20  // hierarchy dumps are ~40KB; cap runaway bodies
	uia2MaxApkBytes     = 256 << 20 // generous cap; the real APK is ~13KB
	instrDefaultRpcMs   = int64(15000)
	instrReadyTimeout   = 20000
	instrPollMs         = 250
	instrDroidjevEnvApk = "DROIDJEV_INSTRUMENT_APK" // dev override: local .apk path
)

// Other resident instrumentations that must be gone before ours can hold
// UiAutomation: the android CLI's, and appium's server from earlier droidjev
// versions.
var instrDisplace = []string{
	"com.android.cli.interact.instrumentation",
	"io.appium.uiautomator2.server",
}

// Session is one serial's local-side state: the forwarded port.
type Session struct {
	Port int
}

var (
	instrMu       sync.Mutex
	instrSessions = map[string]*Session{}
	instrEnsured  = map[string]bool{} // serials whose forward + server are known-good
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
// The returned pointer's fields are only safe to read outside instrMu for the
// single-threaded CLI path; internal mutations hold the lock.
func SessionFor(serial string) *Session {
	instrMu.Lock()
	defer instrMu.Unlock()
	if s, ok := instrSessions[serial]; ok {
		return s
	}
	used := make([]int, 0, len(instrSessions))
	for _, s := range instrSessions {
		used = append(used, s.Port)
	}
	s := &Session{Port: PickPort(used, serial)}
	instrSessions[serial] = s
	return s
}

func instrIsEnsured(serial string) bool {
	instrMu.Lock()
	defer instrMu.Unlock()
	return instrEnsured[serial]
}

func instrMarkEnsured(serial string) {
	instrMu.Lock()
	defer instrMu.Unlock()
	instrEnsured[serial] = true
}

// instrRpc sends one request line and reads the reply: the first response line
// always, plus the length-prefixed DUMP body when wantBody is set. A fresh
// connection per request self-heals across instrumentation restarts.
func instrRpc(serial, line string, timeoutMs int64, wantBody bool) (string, []byte, error) {
	if timeoutMs <= 0 {
		timeoutMs = instrDefaultRpcMs
	}
	port := SessionFor(serial).Port
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		time.Duration(timeoutMs)*time.Millisecond)
	if err != nil {
		return "", nil, NewDroidJevError(fmt.Sprintf("instrument connect failed: %v", err))
	}
	defer conn.Close()
	deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
	_ = conn.SetDeadline(deadline)
	if _, err := conn.Write([]byte(line + "\n")); err != nil {
		return "", nil, NewDroidJevError(fmt.Sprintf("instrument write failed: %v", err))
	}
	rd := io.LimitedReader{R: conn, N: 128}
	head, err := readLine(&rd)
	if err != nil {
		return "", nil, NewDroidJevError(fmt.Sprintf("instrument read failed: %v", err))
	}
	if !wantBody {
		return head, nil, nil
	}
	n, err := strconv.ParseInt(strings.TrimSpace(head), 10, 64)
	if err != nil || n < 0 || n > instrMaxBodyBytes {
		return "", nil, NewDroidJevError(fmt.Sprintf("instrument bad dump header %q", UTF16Truncate(JSTrim(head), 40)))
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(conn, body); err != nil {
		return "", nil, NewDroidJevError(fmt.Sprintf("instrument dump read failed: %v", err))
	}
	return head, body, nil
}

// readLine reads one \n-terminated line from r.
func readLine(r io.Reader) (string, error) {
	var sb strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if buf[0] == '\n' {
				return sb.String(), nil
			}
			sb.WriteByte(buf[0])
		}
		if err != nil {
			if sb.Len() > 0 {
				return sb.String(), nil
			}
			return "", err
		}
	}
}

func instrPing(serial string, timeoutMs int64) bool {
	head, _, err := instrRpc(serial, "PING", timeoutMs, false)
	return err == nil && strings.HasPrefix(head, "OK")
}

// PinnedAsset is one release asset plus its SHA-256, verified before anything
// reaches a device. An empty Sha256 is allowed only for first-party assets
// (our own GitHub Releases): their trust chain is the release itself, same as
// the droidjev binary, and it lets a tag's binary reference that same tag's
// APK before the release sha could be known.
type PinnedAsset struct {
	URL    string
	Sha256 string
}

// PinnedSet is the pin table: the single instrumentation APK. Bump
// deliberately (edit url and sha together); a cached or tampered file must
// never install silently.
type PinnedSet struct {
	Tag        string
	Instrument PinnedAsset
}

// Pinned version of the instrumentation APK.
var Pinned = PinnedSet{
	Tag: "v0.6.0",
	Instrument: PinnedAsset{
		URL: "https://github.com/mkruglikov/droidjev/releases/download/v0.6.0/droidjev-instrument.apk",
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

// assetVersionKey picks the cache-key version half: the pinned sha prefix when
// present, else the release tag from the URL (first-party assets pin the tag,
// and the tag changes every release, so a stale cache file can never match).
func assetVersionKey(asset PinnedAsset) string {
	if len(asset.Sha256) >= 8 {
		return asset.Sha256[:8]
	}
	parts := strings.Split(asset.URL, "/")
	if len(parts) >= 2 {
		if tag := parts[len(parts)-2]; strings.HasPrefix(tag, "v") {
			return tag
		}
	}
	return "0"
}

func uia2ShaPrefix(sha string, n int) string {
	if len(sha) < n {
		return sha
	}
	return sha[:n]
}

func instrCacheDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".cache", "droidjev", "instrument")
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
// installs it. The cache key carries the sha (or the release tag for
// first-party assets), so a new pin can never pick up a stale file; a corrupt
// cache entry is re-downloaded instead of failing the run.
func InstallApk(serial string, asset PinnedAsset, deps InstallDeps) (string, error) {
	fetch := deps.Fetch
	if fetch == nil {
		fetch = uia2FetchURL
	}
	cacheDir := deps.CacheDir
	if cacheDir == "" {
		cacheDir = instrCacheDir()
	}
	adbInstall := deps.AdbInstall
	if adbInstall == nil {
		adbInstall = func(args []string) (RunResult, error) {
			return Run(adb, args, RunOpts{TimeoutMs: 180000})
		}
	}

	base := uia2AssetBase(asset.URL)
	file := filepath.Join(cacheDir, base+"-"+assetVersionKey(asset)+".apk")

	var buf []byte
	if cached, err := os.ReadFile(file); err == nil &&
		(asset.Sha256 == "" || Sha256Matches(cached, asset.Sha256)) {
		buf = cached
	}
	if buf == nil {
		downloaded, err := fetch(asset.URL)
		if err != nil {
			return "", err
		}
		if asset.Sha256 != "" && !Sha256Matches(downloaded, asset.Sha256) {
			got := sha256.Sum256(downloaded)
			return "", NewDroidJevError(fmt.Sprintf(
				"download of %s.apk failed sha256: expected %s, got %s",
				base, uia2ShaPrefix(asset.Sha256, 12), uia2ShaPrefix(hex.EncodeToString(got[:]), 12),
			))
		}
		if err := os.MkdirAll(cacheDir, 0o755); err != nil {
			return "", NewDroidJevError(fmt.Sprintf("instrument cache write failed: %v", err))
		}
		if err := os.WriteFile(file, downloaded, 0o644); err != nil {
			return "", NewDroidJevError(fmt.Sprintf("instrument cache write failed: %v", err))
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

func instrInstalled(serial string) (bool, error) {
	r, err := Shell(serial, []string{"pm", "path", instrPkg}, RunOpts{TimeoutMs: 8000})
	if err != nil {
		return false, err
	}
	return r.Code == 0 && strings.Contains(string(r.Stdout), "package:"), nil
}

// instrStartInstrument spawns `am instrument -w` detached: it blocks for the
// server's lifetime, and the server must outlive this process (Stop
// force-stops it). Run cannot be used here (it waits); a Start failure is
// swallowed and surfaces as a readiness timeout, like the appium provider did.
func instrStartInstrument(serial string) {
	cmd := exec.Command(adb, "-s", serial, "shell", "am", "instrument", "-w", instrRunner)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err == nil {
		_ = cmd.Process.Release()
	}
}

func instrWaitReady(serial string, timeoutMs int64) error {
	deadline := time.Now().UnixMilli() + timeoutMs
	port := SessionFor(serial).Port
	for {
		if instrPing(serial, 1500) {
			return nil
		}
		if time.Now().UnixMilli() > deadline {
			return NewDroidJevError(fmt.Sprintf(
				"instrument did not become ready on port %d within %dms (another resident instrumentation may hold UiAutomation)",
				port, timeoutMs))
		}
		Sleep(instrPollMs)
	}
}

// Ensure makes the forward and the resident instrumentation ready for this
// serial. DROIDJEV_INSTRUMENT_APK installs a local APK instead of the pinned
// release asset (dev builds against unsigned/local keystore APKs).
func Ensure(serial string) error {
	if instrIsEnsured(serial) {
		return nil
	}
	port := SessionFor(serial).Port
	// The local side is per-serial; the device side is the abstract socket.
	if _, err := Run(adb, []string{
		"-s", serial, "forward",
		fmt.Sprintf("tcp:%d", port), instrSocket,
	}, RunOpts{TimeoutMs: 8000}); err != nil {
		return err
	}
	if instrPing(serial, 1500) {
		instrMarkEnsured(serial)
		return nil // already resident from a previous step/run
	}
	if apk := os.Getenv(instrDroidjevEnvApk); apk != "" {
		if r, err := Run(adb, []string{"-s", serial, "install", "-r", "-t", apk},
			RunOpts{TimeoutMs: 180000}); err != nil {
			return err
		} else if !strings.Contains(string(r.Stdout)+r.Stderr, "Success") {
			return NewDroidJevError("adb install of $DROIDJEV_INSTRUMENT_APK failed: " +
				UTF16Truncate(JSTrim(string(r.Stderr)+string(r.Stdout)), 200))
		}
	} else {
		ok, err := instrInstalled(serial)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintf(os.Stderr, "# instrument: installing the pinned droidjev instrumentation APK (%s; one-time, ~13KB)\n",
				Pinned.Tag)
			if _, err := InstallApk(serial, Pinned.Instrument, InstallDeps{}); err != nil {
				return err
			}
		}
	}
	// Other resident instrumentations own UiAutomation; they must be gone
	// before ours starts, or ours binds a dead connection.
	for _, pkg := range instrDisplace {
		_, _ = Shell(serial, []string{"am", "force-stop", pkg}, RunOpts{TimeoutMs: 8000})
	}
	instrStartInstrument(serial)
	if err := instrWaitReady(serial, instrReadyTimeout); err != nil {
		return err
	}
	instrMarkEnsured(serial)
	return nil
}

// instrDumpOnce fetches the hierarchy XML.
func instrDumpOnce(serial string) (string, error) {
	_, body, err := instrRpc(serial, "DUMP", 20000, true)
	if err != nil {
		return "", err
	}
	xml := string(body)
	if !strings.Contains(xml, "<hierarchy") {
		return "", NewDroidJevError("instrument source returned no hierarchy xml")
	}
	return xml, nil
}

// Source fetches the hierarchy XML. One full restart on failure: the server
// may have died mid-run (or another tool stole UiAutomation).
func Source(serial string) (string, error) {
	for attempt := 0; ; attempt++ {
		if err := Ensure(serial); err != nil {
			return "", err
		}
		xml, err := instrDumpOnce(serial)
		if err == nil {
			return xml, nil
		}
		if attempt >= 1 {
			return "", err
		}
		Stop(serial)
	}
}

// SetClipboard writes any UTF-8 text as base64 over the socket — no shell, no
// charset limits. The instrumentation writes it directly; AOSP allows
// clipboard writes without focus (only reads are IME/focus-gated), the same
// mechanism appium's server used. One full restart on failure.
func SetClipboard(serial, text string) error {
	line := "CLIP " + base64.StdEncoding.EncodeToString([]byte(text))
	for attempt := 0; ; attempt++ {
		if err := Ensure(serial); err != nil {
			return err
		}
		head, _, err := instrRpc(serial, line, instrDefaultRpcMs, false)
		if err == nil {
			if head == "OK" {
				return nil
			}
			err = NewDroidJevError("instrument set_clipboard failed: " +
				UTF16Truncate(JSTrim(strings.TrimPrefix(head, "ERR ")), 200))
		}
		if attempt >= 1 {
			return err
		}
		Stop(serial)
	}
}

// Stop hands UiAutomation back: forget the serial's forward state and
// force-stop ours and (defensively) the other resident instrumentations.
func Stop(serial string) {
	instrMu.Lock()
	delete(instrSessions, serial)
	delete(instrEnsured, serial)
	instrMu.Unlock()
	for _, pkg := range append([]string{instrPkg}, instrDisplace...) {
		_, _ = Shell(serial, []string{"am", "force-stop", pkg}, RunOpts{TimeoutMs: 8000})
	}
}
