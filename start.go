// Deterministic app launch by package: resolve the launcher activity, start
// it; fall back to monkey. Launching an app is not a judgment problem either.
//
// Ported from src/start.js; the package-name validation, the component
// preference, the monkey fallback, and the focus polling timings are kept
// exact.

package main

import (
	"fmt"
	"regexp"
	"strings"
)

var packageNameRe = regexp.MustCompile(`^[a-zA-Z][\w.]*(\.[\w]+)+$`)

// resolveActivityLineRe is the JS `^\S+/\S+$` with JS whitespace semantics.
var resolveActivityLineRe = regexp.MustCompile(
	`^[^` + JSWhitespaceClass + `]+/[^` + JSWhitespaceClass + `]+$`,
)

// StartResult is the outcome of StartApp. Component is "" when resolve-activity
// found none (the Node null); App is nil when no window is focused (the
// launcher, keyguard, or a notification shade).
type StartResult struct {
	Pkg       string
	Component string
	Via       string
	App       *FocusedApp
	Focused   bool
}

// ParseResolveActivity takes the component out of `cmd package
// resolve-activity --brief`: an apk path line may come first, and the component
// is the last package/activity-looking line. "" when there is none.
func ParseResolveActivity(out string) string {
	var lines []string
	for _, line := range strings.Split(JSTrim(out), "\n") {
		l := JSTrim(line)
		if strings.Contains(l, "/") && resolveActivityLineRe.MatchString(l) {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return lines[len(lines)-1]
}

// StartApp launches pkg by its resolved launcher component, falling back to
// monkey, then waits briefly for the window to take focus so callers can trust
// the result.
func StartApp(serial, pkg string) (StartResult, error) {
	if !packageNameRe.MatchString(pkg) {
		return StartResult{}, NewDroidJevError(
			`"` + pkg + `" does not look like an Android package name (e.g. com.google.android.deskclock)`,
		)
	}
	r, err := Shell(serial, []string{"cmd", "package", "resolve-activity", "--brief", pkg}, RunOpts{TimeoutMs: 10000})
	if err != nil {
		return StartResult{}, err
	}
	comp := ""
	if r.Code == 0 {
		comp = ParseResolveActivity(string(r.Stdout))
	}
	var via string
	if comp != "" && strings.HasPrefix(comp, pkg+"/") {
		am, err := Shell(serial, []string{"am", "start", "-n", comp}, RunOpts{TimeoutMs: 15000})
		if err != nil {
			return StartResult{}, err
		}
		if am.Code != 0 {
			return StartResult{}, NewDroidJevError(fmt.Sprintf(
				"am start %s exited %d: %s",
				comp, am.Code, UTF16Truncate(JSTrim(am.Stderr), 200),
			))
		}
		via = "am start -n " + comp
	} else {
		mk, err := Shell(serial, []string{"monkey", "-p", pkg, "-c", "android.intent.category.LAUNCHER", "1"}, RunOpts{TimeoutMs: 15000})
		if err != nil {
			return StartResult{}, err
		}
		if mk.Code != 0 {
			return StartResult{}, NewDroidJevError(fmt.Sprintf(
				"could not launch %s: resolve-activity found no launcher component and monkey exited %d (%s)",
				pkg, mk.Code, UTF16Truncate(JSTrim(mk.Stderr), 200),
			))
		}
		via = "monkey"
	}
	for i := 0; i < 6; i++ {
		if i == 0 {
			Sleep(500)
		} else {
			Sleep(400)
		}
		app, err := CurrentApp(serial)
		if err != nil {
			return StartResult{}, err
		}
		if app != nil && app.Pkg == pkg {
			return StartResult{Pkg: pkg, Component: comp, Via: via, App: app, Focused: true}, nil
		}
	}
	app, err := CurrentApp(serial)
	if err != nil {
		return StartResult{}, err
	}
	return StartResult{Pkg: pkg, Component: comp, Via: via, App: app}, nil
}
