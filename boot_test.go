// Ports the pinHostGpuIni cases from test/unit.test.mjs (src/boot.js is
// frozen): an AVD with hw.gpu.enabled = no renders on the CPU and starves the
// host, so the config.ini text is repinned to the host GPU.
package main

import "testing"

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
