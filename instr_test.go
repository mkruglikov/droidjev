// Tests for the resident instrumentation lifecycle. Everything here is
// offline: port allocation, hash verification, the pinned-APK install pipeline
// with its fetch/cache/adb seams injected, and the socket protocol codec.
// Ensure/Source/SetClipboard/Stop drive a real device and stay untested.
package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// --- port allocation ---

func TestPickPort(t *testing.T) {
	if got := PickPort(nil, "emulator-5554"); got != 6790 {
		t.Fatalf("first serial port = %d, want 6790", got)
	}
	if got := PickPort([]int{}, "anything"); got != 6790 {
		t.Fatalf("no used ports = %d, want 6790", got)
	}

	serial := "emulator-5556"
	sum := sha256.Sum256([]byte(serial))
	h := hex.EncodeToString(sum[:])[:8]
	n, err := strconv.ParseInt(h, 16, 64)
	if err != nil {
		t.Fatal(err)
	}
	base := 6790 + int(n%64)

	// First later serial: the hash-derived port, unless 6790 is the hash
	// result and already taken.
	want := base
	if base == 6790 {
		want = 6791
	}
	if got := PickPort([]int{6790}, serial); got != want {
		t.Fatalf("hash-derived port = %d, want %d", got, want)
	}
	if again := PickPort([]int{6790}, serial); again != want {
		t.Fatalf("port not deterministic: %d then %d", want, again)
	}

	// The hash port in use skips forward within the 6790..6853 window.
	want2 := -1
	for i := 0; i < 64; i++ {
		p := 6790 + ((base - 6790 + i) % 64)
		if p != 6790 && p != base {
			want2 = p
			break
		}
	}
	if got := PickPort([]int{6790, base}, serial); got != want2 {
		t.Fatalf("skip-used port = %d, want %d", got, want2)
	}

	// All 64 local ports in use falls back to the device port, like the JS.
	all := make([]int, 64)
	for i := range all {
		all[i] = 6790 + i
	}
	if got := PickPort(all, serial); got != 6790 {
		t.Fatalf("exhausted ports = %d, want 6790", got)
	}
}

// --- hash verification ---

func TestSha256Matches(t *testing.T) {
	// sha256("abc"), the canonical test vector.
	const abc = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if !Sha256Matches([]byte("abc"), abc) {
		t.Fatal("correct digest did not match")
	}
	if !Sha256Matches([]byte("abc"), strings.ToUpper(abc)) {
		t.Fatal("uppercase digest did not match (JS lowercases the pin)")
	}
	if Sha256Matches([]byte("abd"), abc) {
		t.Fatal("wrong content matched")
	}
	if Sha256Matches([]byte("abc"), abc[:63]) {
		t.Fatal("truncated digest matched")
	}
}

// --- pinned APK installs ---

func uia2TestAsset(t *testing.T, url, content string) PinnedAsset {
	t.Helper()
	sum := sha256.Sum256([]byte(content))
	return PinnedAsset{URL: url, Sha256: hex.EncodeToString(sum[:])}
}

// uia2CachePath is the cache key: basename minus .apk plus the sha prefix.
func uia2CachePath(dir string, asset PinnedAsset) string {
	base := strings.TrimSuffix(asset.URL[strings.LastIndex(asset.URL, "/")+1:], ".apk")
	return filepath.Join(dir, base+"-"+asset.Sha256[:8]+".apk")
}

// uia2Fetch is a plain GET used as the injected Fetch in download tests.
func uia2Fetch(url string) ([]byte, error) {
	res, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	return io.ReadAll(res.Body)
}

func TestInstallApkCacheHit(t *testing.T) {
	const content = "pinned apk bytes"
	asset := uia2TestAsset(t, "https://example.test/helper.apk", content)
	dir := t.TempDir()
	file := uia2CachePath(dir, asset)
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	fetched := 0
	var installArgs []string
	got, err := InstallApk("emulator-5554", asset, InstallDeps{
		Fetch: func(string) ([]byte, error) {
			fetched++
			return nil, fmt.Errorf("cache hit must not fetch")
		},
		CacheDir: dir,
		AdbInstall: func(args []string) (RunResult, error) {
			installArgs = args
			return RunResult{Code: 0, Stdout: []byte("Success")}, nil
		},
	})
	if err != nil {
		t.Fatalf("InstallApk: %v", err)
	}
	if fetched != 0 {
		t.Fatalf("fetched %d times on a cache hit, want 0", fetched)
	}
	if got != file {
		t.Fatalf("installed file = %q, want %q", got, file)
	}
	want := []string{"-s", "emulator-5554", "install", "-r", "-t", file}
	if strings.Join(installArgs, " ") != strings.Join(want, " ") {
		t.Fatalf("adb args = %v, want %v", installArgs, want)
	}
}

func TestInstallApkRedownloadsCorrupt(t *testing.T) {
	const content = "good apk bytes"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, content)
	}))
	defer server.Close()
	asset := uia2TestAsset(t, server.URL+"/helper.apk", content)
	dir := t.TempDir()
	file := uia2CachePath(dir, asset)
	if err := os.WriteFile(file, []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}

	fetched := 0
	installed := 0
	_, err := InstallApk("emulator-5554", asset, InstallDeps{
		Fetch: func(url string) ([]byte, error) {
			fetched++
			return uia2Fetch(url)
		},
		CacheDir: dir,
		AdbInstall: func([]string) (RunResult, error) {
			installed++
			return RunResult{Code: 0, Stdout: []byte("Success")}, nil
		},
	})
	if err != nil {
		t.Fatalf("InstallApk: %v", err)
	}
	if fetched != 1 {
		t.Fatalf("fetched %d times, want 1", fetched)
	}
	if installed != 1 {
		t.Fatalf("installed %d times, want 1", installed)
	}
	onDisk, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != content {
		t.Fatalf("cache file = %q, want %q", onDisk, content)
	}
}

func TestInstallApkBadDownload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "tampered bytes")
	}))
	defer server.Close()
	asset := uia2TestAsset(t, server.URL+"/helper.apk", "expected bytes")

	_, err := InstallApk("emulator-5554", asset, InstallDeps{
		Fetch:    uia2Fetch,
		CacheDir: t.TempDir(),
		AdbInstall: func([]string) (RunResult, error) {
			t.Fatal("a bad download must never reach adb")
			return RunResult{}, nil
		},
	})
	if err == nil {
		t.Fatal("expected a sha256 error")
	}
	wrong := sha256.Sum256([]byte("tampered bytes"))
	gotHex := hex.EncodeToString(wrong[:])
	wantSub := "expected " + asset.Sha256[:12] + ", got " + gotHex[:12]
	if !strings.Contains(err.Error(), wantSub) {
		t.Fatalf("error = %q, want it to contain %q", err.Error(), wantSub)
	}
	if !strings.Contains(err.Error(), "download of helper.apk failed sha256") {
		t.Fatalf("error = %q, want the download-of-helper.apk prefix", err.Error())
	}
}

func TestInstallApkAdbFailure(t *testing.T) {
	const content = "cached apk bytes"
	asset := uia2TestAsset(t, "https://example.test/helper.apk", content)
	dir := t.TempDir()
	if err := os.WriteFile(uia2CachePath(dir, asset), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := InstallApk("emulator-5554", asset, InstallDeps{
		Fetch:    func(string) ([]byte, error) { return nil, fmt.Errorf("unexpected fetch") },
		CacheDir: dir,
		AdbInstall: func([]string) (RunResult, error) {
			return RunResult{Code: 1, Stderr: "Failure [INSTALL_FAILED_UPDATE_INCOMPATIBLE]"}, nil
		},
	})
	if err == nil {
		t.Fatal("expected an install failure")
	}
	if !strings.Contains(err.Error(), "adb install of helper.apk failed: Failure [INSTALL_FAILED_UPDATE_INCOMPATIBLE]") {
		t.Fatalf("error = %q", err.Error())
	}
}

// --- socket protocol codec ---

// instrTestServe stands in for the on-device instrumentation: answers the
// line protocol on one accepted connection.
func instrTestServe(t *testing.T, ln net.Listener, handler func(req string) string) {
	t.Helper()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				req, err := readLine(c)
				if err != nil {
					return
				}
				if _, err := c.Write([]byte(handler(req))); err != nil {
					return
				}
			}(conn)
		}
	}()
}

// instrTestSession points a fake serial at a free local port.
func instrTestSession(t *testing.T, port int) {
	t.Helper()
	instrMu.Lock()
	instrSessions["test-serial"] = &Session{Port: port}
	instrMu.Unlock()
	t.Cleanup(func() {
		instrMu.Lock()
		delete(instrSessions, "test-serial")
		instrMu.Unlock()
	})
}

func TestInstrRpcPingDumpClip(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	instrTestSession(t, ln.Addr().(*net.TCPAddr).Port)
	instrTestServe(t, ln, func(req string) string {
		switch {
		case req == "PING":
			return "OK 1\n"
		case req == "DUMP":
			return fmt.Sprintf("%d\n%s", len("<hierarchy/>"), "<hierarchy/>")
		case strings.HasPrefix(req, "CLIP "):
			if decoded, err := base64.StdEncoding.DecodeString(req[5:]); err == nil && string(decoded) == "hi Ж" {
				return "OK\n"
			}
			return "ERR bad base64\n"
		default:
			return "ERR unknown command\n"
		}
	})

	if head, _, err := instrRpc("test-serial", "PING", 2000, false); err != nil || head != "OK 1" {
		t.Fatalf("PING = %q, %v", head, err)
	}
	head, body, err := instrRpc("test-serial", "DUMP", 2000, true)
	if err != nil || string(body) != "<hierarchy/>" {
		t.Fatalf("DUMP = %q %q, %v", head, body, err)
	}
	if head, _, err := instrRpc("test-serial", "CLIP "+base64.StdEncoding.EncodeToString([]byte("hi Ж")), 2000, false); err != nil || head != "OK" {
		t.Fatalf("CLIP = %q, %v", head, err)
	}
	if head, _, err := instrRpc("test-serial", "CLIP !!!", 2000, false); err != nil || head != "ERR bad base64" {
		t.Fatalf("bad CLIP = %q, %v", head, err)
	}
	if head, _, err := instrRpc("test-serial", "WHAT", 2000, false); err != nil || head != "ERR unknown command" {
		t.Fatalf("unknown = %q, %v", head, err)
	}
}

func TestInstrRpcBadDumpHeader(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	instrTestSession(t, ln.Addr().(*net.TCPAddr).Port)
	instrTestServe(t, ln, func(string) string { return "not-a-number\n" })
	if _, _, err := instrRpc("test-serial", "DUMP", 2000, true); err == nil {
		t.Fatal("expected a framing error")
	}
}

func TestAssetVersionKey(t *testing.T) {
	if got := assetVersionKey(PinnedAsset{URL: "https://x/y.apk", Sha256: "abcdef1234"}); got != "abcdef12" {
		t.Fatalf("sha key = %q", got)
	}
	if got := assetVersionKey(PinnedAsset{URL: "https://github.com/o/r/releases/download/v0.6.0/droidjev-instrument.apk"}); got != "v0.6.0" {
		t.Fatalf("tag key = %q", got)
	}
}
