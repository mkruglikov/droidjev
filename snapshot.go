// One-screen capture shared by snapshot/find/act: screen size + focused app +
// layout dump + element table.
//

package main

// Snapshot is one screen's capture. Screen is nil when `wm size` reported
// nothing; App is nil when no component is focused; Table is the built element
// table (Count is len(Table.Rows)).
type Snapshot struct {
	Serial string
	Screen *Screen
	App    *FocusedApp
	TookMs int64
	Count  int
	Table  *Table
}

// TakeSnapshot captures the current screen, focused app, and element table.
//
// `wm size`, `dumpsys window`, and the layout dump could run concurrently;
// here they run sequentially — the extra ~50ms
// on the cold path is not worth a goroutine error fan-in (controller-approved
// lazy default).
func TakeSnapshot(serial string) (*Snapshot, error) {
	screen, err := ScreenSize(serial)
	if err != nil {
		return nil, err
	}
	app, err := CurrentApp(serial)
	if err != nil {
		return nil, err
	}
	layout, err := GetLayout(serial)
	if err != nil {
		return nil, err
	}
	// The focused app's package rides along: the table builder tells an IME's
	// keys (foreign package) from an in-app dial pad (same package).
	appPkg := ""
	if app != nil {
		appPkg = app.Pkg
	}
	scr := &Screen{App: appPkg}
	if screen != nil {
		scr.W, scr.H = screen.W, screen.H
	}
	table := BuildTable(layout.Elements, scr)
	return &Snapshot{
		Serial: serial,
		Screen: screen,
		App:    app,
		TookMs: layout.TookMs,
		Count:  len(table.Rows),
		Table:  table,
	}, nil
}
