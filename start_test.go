// Ports the parseResolveActivity case from the Node implementation: `cmd package
// resolve-activity --brief` may print an apk path line before the component,
// and the component is the last package/activity-looking line.
package main

import "testing"

func TestParseResolveActivity(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			"apk path before the component",
			"/data/app/com.example-Yq==/base.apk\ncom.example/.MainActivity",
			"com.example/.MainActivity",
		},
		{"trailing whitespace and extra lines", "\n/odd/path.apk\ncom.example/.MainActivity\n\n", "com.example/.MainActivity"},
		{"full activity name", "priority=0\ncom.example/com.example.ui.Main", "com.example/com.example.ui.Main"},
		{"no activity found", "No activity found", ""},
		{"empty", "", ""},
		// The JS filter is only `includes('/') && /^\S+\/\S+$/`, so a lone
		// slash-bearing line (an apk path) is returned too; in the real output
		// the component comes after it and wins as the last such line.
		{"a lone apk path still passes the JS filter", "/data/app/com.example-Yq==/base.apk", "/data/app/com.example-Yq==/base.apk"},
	}
	for _, c := range cases {
		if got := ParseResolveActivity(c.in); got != c.want {
			t.Errorf("%s: ParseResolveActivity(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}
