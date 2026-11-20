// Live-emulator end-to-end scenarios for the act loop. Opt-in: they spend
// real TypeSafe tokens and need a booted emulator (plus Chrome for the paste
// scenario). Run with: DROIDJEV_E2E=1 go test -run TestE2E -v .
// Assertions unchanged since the suite was first written.

package main

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

// e2eGate skips unless the operator opted into the live suite.
func e2eGate(t *testing.T) {
	t.Helper()
	if os.Getenv("DROIDJEV_E2E") != "1" {
		t.Skip("set DROIDJEV_E2E=1 (boots/uses an emulator, spends TypeSafe tokens)")
	}
}

// e2eTrail renders the step actions for assertion messages.
func e2eTrail(steps []StepRecord) string {
	actions := make([]string, len(steps))
	for i, s := range steps {
		actions[i] = s.Action
	}
	return strings.Join(actions, " | ")
}

// e2eFinalApp is the JS `res.finalApp ?? '?'`.
func e2eFinalApp(r GoalResult) string {
	if r.FinalApp == nil {
		return "?"
	}
	return *r.FinalApp
}

func TestE2EImeiPaste(t *testing.T) {
	e2eGate(t)
	dev, err := EnsureDevice("")
	if err != nil {
		t.Fatalf("ensure device: %v", err)
	}
	serial := dev.Serial
	// A fresh tab keeps the destination field empty, so the paste is exercised
	// even when a previous run left the IMEI in the address bar.
	if _, err := ShellOk(serial, []string{
		"am",
		"start",
		"-a",
		"android.intent.action.VIEW",
		"-d",
		"about:blank",
		"-p",
		"com.android.chrome",
	}, RunOpts{}); err != nil {
		t.Fatalf("open about:blank in Chrome: %v", err)
	}
	if _, err := ShellOk(serial, []string{"input", "keyevent", "3"}, RunOpts{}); err != nil { // start from home, like a user
		t.Fatalf("home key: %v", err)
	}
	res, err := RunGoal(GoalOpts{
		Serial: serial,
		Goal:   "copy the IMEI from settings and paste it into chrome search bar",
		Vision: true,
		Log:    func(m string) { t.Logf("# %s", m) },
	})
	if err != nil {
		t.Fatalf("run goal: %v", err)
	}
	t.Logf("status=%s steps=%d %.1fs app=%s", res.Status, len(res.Steps), float64(res.TookMs)/1000, e2eFinalApp(res))
	trail := e2eTrail(res.Steps)
	copyRe := regexp.MustCompile(`^copy (".*") from `)
	var copyStep *StepRecord
	for i := range res.Steps {
		if copyRe.MatchString(res.Steps[i].Action) {
			copyStep = &res.Steps[i]
			break
		}
	}
	if copyStep == nil {
		t.Fatalf("no copy step ran: %s", trail)
	}
	pasted := false
	for _, s := range res.Steps {
		if strings.HasPrefix(s.Action, "paste") {
			pasted = true
			break
		}
	}
	if !pasted {
		t.Fatalf("no paste step ran: %s", trail)
	}
	if res.Status != "done" {
		last := ""
		if n := len(res.Steps); n > 0 {
			last = res.Steps[n-1].Action
		}
		t.Fatalf("goal not done (last: %s)", last)
	}
	if e2eFinalApp(res) != "com.android.chrome" {
		t.Fatalf("ended in %s", e2eFinalApp(res))
	}
	var copied string
	if err := json.Unmarshal([]byte(copyRe.FindStringSubmatch(copyStep.Action)[1]), &copied); err != nil {
		t.Fatalf("parse copied value from %q: %v", copyStep.Action, err)
	}
	visible := false
	for _, r := range res.FinalRows {
		if strings.Contains(r, copied) {
			visible = true
			break
		}
	}
	if !visible {
		t.Fatalf("pasted %q not visible in final rows: %s", copied, strings.Join(res.FinalRows, " | "))
	}
}

func TestE2ECreateContact(t *testing.T) {
	e2eGate(t)
	dev, err := EnsureDevice("")
	if err != nil {
		t.Fatalf("ensure device: %v", err)
	}
	serial := dev.Serial
	if _, err := ShellOk(serial, []string{"input", "keyevent", "3"}, RunOpts{}); err != nil {
		t.Fatalf("home key: %v", err)
	}
	res, err := RunGoal(GoalOpts{
		Serial: serial,
		Goal:   "open the Contacts app and create a new contact named Maria, phone +79001234567",
		Texts:  []string{"Maria", "+79001234567"},
		Vision: true,
		Log:    func(m string) { t.Logf("# %s", m) },
	})
	if err != nil {
		t.Fatalf("run goal: %v", err)
	}
	t.Logf("status=%s steps=%d %.1fs app=%s", res.Status, len(res.Steps), float64(res.TookMs)/1000, e2eFinalApp(res))
	trail := e2eTrail(res.Steps)
	if res.Status != "done" {
		t.Fatalf("goal not done: %s", trail)
	}
	// The reported regression: the phone value must land in the Phone field,
	// not be typed over the First name like the single --text era did.
	var nameStep, phoneStep *StepRecord
	for i := range res.Steps {
		s := &res.Steps[i]
		if strings.HasPrefix(s.Action, `type "Maria"`) {
			nameStep = s
		}
		if strings.HasPrefix(s.Action, `type "+79001234567"`) {
			phoneStep = s
		}
	}
	// A contact that already exists with the right data is a valid done: the
	// provider check below is the acceptance gate. When the run did type, the
	// values must have gone into different fields, phone after name.
	if nameStep != nil || phoneStep != nil {
		if nameStep == nil || phoneStep == nil {
			t.Fatalf("a provided value was typed without the other: %s", trail)
		}
		if regexp.MustCompile(`(?i)first name`).MatchString(phoneStep.Action) {
			t.Fatalf("phone typed into the wrong field: %s", phoneStep.Action)
		}
		if phoneStep.Step <= nameStep.Step {
			t.Fatalf("phone must be typed after the name: %s", trail)
		}
	}
	mariaVisible := false
	for _, r := range res.FinalRows {
		if strings.Contains(r, "Maria") {
			mariaVisible = true
			break
		}
	}
	if !mariaVisible {
		t.Fatalf("saved contact not visible: %s", strings.Join(res.FinalRows, " | "))
	}
	// The screen can show a value that landed in the wrong field: check the
	// contact provider itself, so a phone typed over the name fails here.
	data, err := Shell(serial, []string{
		"content",
		"query",
		"--uri",
		"content://com.android.contacts/data",
		"--projection",
		"display_name:mimetype:data1",
	}, RunOpts{})
	if err != nil {
		t.Fatalf("query the contacts provider: %v", err)
	}
	stdout := string(data.Stdout)
	rows := strings.Split(stdout, "\n")
	nameRowRe := regexp.MustCompile(`display_name=Maria\b`)
	nameTypeRe := regexp.MustCompile(`mimetype=vnd\.android\.cursor\.item/name\b`)
	phoneTypeRe := regexp.MustCompile(`mimetype=vnd\.android\.cursor\.item/phone_v2\b`)
	data1Re := regexp.MustCompile(`data1=(.*)$`)
	digits := func(row string) string {
		m := data1Re.FindStringSubmatch(row)
		if m == nil {
			return ""
		}
		return strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, m[1])
	}
	foundName, foundPhone := false, false
	for _, r := range rows {
		if nameRowRe.MatchString(r) && nameTypeRe.MatchString(r) {
			foundName = true
		}
		if phoneTypeRe.MatchString(r) && strings.Contains(digits(r), "79001234567") {
			foundPhone = true
		}
	}
	if !foundName {
		t.Fatalf("Maria was not saved as a contact name: %s", stdout)
	}
	if !foundPhone {
		t.Fatalf("the phone was not saved as a phone number: %s", stdout)
	}
}
