// Ports the pinHostGpuIni cases from the frozen Node implementation: an AVD
// with hw.gpu.enabled = no renders on the CPU and starves the host, so the
// config.ini text is repinned to the host GPU.
package main

import "testing"

// keyguardDumpUnlocked is the KeyguardServiceDelegate block of a live
// emulator's `dumpsys window policy` while unlocked (API 36). The near-name
// showingAndNotOccluded must not read as showing.
const keyguardDumpUnlocked = `WINDOW MANAGER POLICY STATE (sysmultiwindow)
    mAwake=true
    KeyguardServiceDelegate
      showing=false
      showingAndNotOccluded=true
      inputRestricted=false
      occluded=false
      secure=false
      dreaming=false
    mCurrentUserId=0
`

func TestParseKeyguard(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want KeyguardState
	}{
		{"unlocked emulator", keyguardDumpUnlocked, KeyguardState{}},
		{
			"secure keyguard showing (PIN)",
			`    KeyguardServiceDelegate
      showing=true
      secure=true
      dreaming=true
`,
			KeyguardState{Showing: true, Secure: true},
		},
		{
			"swipe keyguard showing, not secure",
			`    KeyguardServiceDelegate
      showing=true
      secure=false
`,
			KeyguardState{Showing: true},
		},
		{
			"secure device, keyguard dismissed",
			`    KeyguardServiceDelegate
      showing=false
      secure=true
`,
			KeyguardState{Secure: true},
		},
		{"no delegate block reads as unlocked", "mAwake=true\n", KeyguardState{}},
		{
			"the block ends at the next unindented section",
			`KeyguardServiceDelegate
      showing=true
      secure=true
NextSection showing=true
`,
			KeyguardState{Showing: true, Secure: true},
		},
	}
	for _, c := range cases {
		if got := parseKeyguard(c.in); got != c.want {
			t.Errorf("%s: parseKeyguard = %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestLockedError(t *testing.T) {
	err := lockedError("emulator-5554")
	if err.ExitCode != 2 {
		t.Errorf("lockedError ExitCode = %d, want 2 (blocked)", err.ExitCode)
	}
}

func TestPinHostGpuIni(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			"disabled GPU is repinned to host",
			"hw.gpu.enabled = no\nhw.gpu.mode = auto\n",
			"hw.gpu.enabled = yes\nhw.gpu.mode = host\n",
		},
		{
			"missing GPU keys are appended",
			"avd.ini.encoding = UTF-8\n",
			"avd.ini.encoding = UTF-8\nhw.gpu.enabled = yes\nhw.gpu.mode = host\n",
		},
		{
			"an already-pinned config is left byte-identical",
			"hw.gpu.enabled = yes\nhw.gpu.mode = host\n",
			"hw.gpu.enabled = yes\nhw.gpu.mode = host\n",
		},
		{
			"only one key present is appended after the existing line",
			"hw.gpu.enabled = no\nfoo = 1\n",
			"hw.gpu.enabled = yes\nfoo = 1\nhw.gpu.mode = host\n",
		},
		{
			"spacing around the key and equals sign does not matter",
			"hw.gpu.enabled=no\nhw.gpu.mode\t=auto\n",
			"hw.gpu.enabled = yes\nhw.gpu.mode = host\n",
		},
		{
			// JS `.` stops before \r, so a CRLF config keeps its line endings.
			"CRLF line endings survive the replace",
			"hw.gpu.enabled = no\r\nhw.gpu.mode = auto\r\n",
			"hw.gpu.enabled = yes\r\nhw.gpu.mode = host\r\n",
		},
		{
			// `${out.trimEnd()}\n...` with out=="" keeps the leading newline.
			"an empty config gets both keys",
			"",
			"\nhw.gpu.enabled = yes\nhw.gpu.mode = host\n",
		},
	}
	for _, c := range cases {
		if got := PinHostGpuIni(c.in); got != c.want {
			t.Errorf("%s: PinHostGpuIni(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}
