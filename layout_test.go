// Parser tests ported from the Node implementation's layout section. The XML
// fixtures are the same strings the original tests used: machine-generated
// uia2 output (v10 class-named nested tags, optional attrs omitted, entities).

package main

import (
	"errors"
	"strings"
	"testing"
)

// sampleXML is SAMPLE_XML from the Node tests: nested class-named tags with
// entities and a clickable row.
const sampleXML = `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?>
<hierarchy index="0" class="hierarchy" rotation="0" width="1344" height="2992">
  <android.widget.FrameLayout index="0" package="com.example" class="android.widget.FrameLayout" text="" bounds="[0,0][1344,2992]" displayed="true">
    <androidx.recyclerview.widget.RecyclerView index="0" package="com.example" class="androidx.recyclerview.widget.RecyclerView" resource-id="com.example:id/list" scrollable="true" bounds="[0,200][1344,2800]" displayed="true">
      <android.widget.TextView index="0" package="com.example" class="android.widget.TextView" text="Wi&#45;Fi &amp; Network" bounds="[100,300][1200,400]" displayed="true"/>
      <android.widget.LinearLayout index="1" package="com.example" class="android.widget.LinearLayout" resource-id="com.example:id/row" clickable="true" bounds="[100,300][1244,500]" displayed="true">
        <android.widget.TextView index="0" package="com.example" class="android.widget.TextView" text="About &lt;phone&gt;" bounds="[100,320][1200,380]" displayed="true"/>
      </android.widget.LinearLayout>
    </androidx.recyclerview.widget.RecyclerView>
  </android.widget.FrameLayout>
</hierarchy>`

// uia2XML is UIA2_XML from the Node tests: the appium uiautomator2-server v10
// shape, mixing self-closing tags with explicit close tags.
const uia2XML = `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?>
<hierarchy index="0" class="hierarchy" rotation="0" width="1344" height="2992">
  <android.widget.FrameLayout index="0" package="com.example" class="android.widget.FrameLayout" text="" bounds="[0,0][1344,2992]" displayed="true">
    <androidx.recyclerview.widget.RecyclerView index="0" package="com.example" class="androidx.recyclerview.widget.RecyclerView" resource-id="com.example:id/list" scrollable="true" bounds="[0,200][1344,2800]" displayed="true"></androidx.recyclerview.widget.RecyclerView>
    <android.widget.LinearLayout index="1" package="com.example" class="android.widget.LinearLayout" clickable="true" bounds="[100,300][1244,500]" displayed="true">
      <android.widget.TextView index="0" package="com.example" class="android.widget.TextView" text="Wi&#45;Fi &amp; Network" content-desc="Wi-Fi" bounds="[100,320][1200,380]" displayed="true"/>
    </android.widget.LinearLayout>
    <android.widget.Switch index="2" package="com.example" class="android.widget.Switch" checkable="true" checked="true" clickable="true" bounds="[1092,811][1248,955]" displayed="false"/>
  </android.widget.FrameLayout>
</hierarchy>`

func findElement(els []*Element, pred func(*Element) bool) *Element {
	for _, el := range els {
		if pred(el) {
			return el
		}
	}
	return nil
}

func TestParseUia2XmlNestingEntitiesFlags(t *testing.T) {
	els, err := ParseUia2Xml(sampleXML)
	if err != nil {
		t.Fatalf("ParseUia2Xml: %v", err)
	}
	if len(els) != 5 {
		t.Fatalf("len(els) = %d, want 5", len(els))
	}
	row := findElement(els, func(e *Element) bool { return e.ResourceID == "com.example:id/row" })
	if row == nil {
		t.Fatal("row element not found")
	}
	if !row.Clickable {
		t.Error("row.Clickable = false, want true")
	}
	if row.ParentIdx != 1 {
		t.Errorf("row.ParentIdx = %d, want 1", row.ParentIdx)
	}
	wifi := findElement(els, func(e *Element) bool { return strings.Contains(e.Text, "Wi") })
	if wifi == nil || wifi.Text != "Wi-Fi & Network" {
		t.Errorf("wifi text = %q, want %q", wifiText(wifi), "Wi-Fi & Network")
	}
	about := findElement(els, func(e *Element) bool { return strings.HasPrefix(e.Text, "About") })
	if about == nil || about.Text != "About <phone>" {
		t.Errorf("about text = %q, want %q", wifiText(about), "About <phone>")
	}
}

func wifiText(el *Element) string {
	if el == nil {
		return "<nil>"
	}
	return el.Text
}

func TestParseUia2XmlClassNamedTagsV10(t *testing.T) {
	els, err := ParseUia2Xml(uia2XML)
	if err != nil {
		t.Fatalf("ParseUia2Xml: %v", err)
	}
	if len(els) != 5 { // hierarchy root is not an element
		t.Fatalf("len(els) = %d, want 5", len(els))
	}
	list := findElement(els, func(e *Element) bool { return e.ResourceID == "com.example:id/list" })
	if list == nil {
		t.Fatal("list element not found")
	}
	if !list.Scrollable {
		t.Error("list.Scrollable = false, want true")
	}
	if list.ParentIdx != 0 {
		t.Errorf("list.ParentIdx = %d, want 0", list.ParentIdx)
	}
	wantChildren := []int{1, 2, 4} // nesting tracked through close tags
	if len(els[0].Children) != len(wantChildren) {
		t.Fatalf("root children = %v, want %v", els[0].Children, wantChildren)
	}
	for i, want := range wantChildren {
		if els[0].Children[i] != want {
			t.Fatalf("root children = %v, want %v", els[0].Children, wantChildren)
		}
	}
	label := findElement(els, func(e *Element) bool { return strings.Contains(e.Text, "Wi") })
	if label == nil {
		t.Fatal("label element not found")
	}
	if label.Text != "Wi-Fi & Network" {
		t.Errorf("label.Text = %q, want %q", label.Text, "Wi-Fi & Network")
	}
	if label.ContentDesc != "Wi-Fi" {
		t.Errorf("label.ContentDesc = %q, want %q", label.ContentDesc, "Wi-Fi")
	}
	sw := findElement(els, func(e *Element) bool { return e.Class == "android.widget.Switch" })
	if sw == nil {
		t.Fatal("switch element not found")
	}
	if !sw.Checked {
		t.Error("switch.Checked = false, want true")
	}
	if !sw.OffScreen {
		t.Error("switch.OffScreen = false, want true (displayed=false)")
	}
}

func TestParseUia2XmlSelfClosing(t *testing.T) {
	xml := `<hierarchy rotation="0">
  <android.widget.FrameLayout package="com.example" bounds="[0,0][100,100]">
    <android.widget.Button text="A" bounds="[0,0][10,10]"/>
    <android.widget.TextView text="B" class="custom.View" bounds="[0,0][10,10]"/>
  </android.widget.FrameLayout>
</hierarchy>`
	els, err := ParseUia2Xml(xml)
	if err != nil {
		t.Fatalf("ParseUia2Xml: %v", err)
	}
	if len(els) != 3 {
		t.Fatalf("len(els) = %d, want 3", len(els))
	}
	// A self-closing tag must not become the parent of the next sibling.
	for _, idx := range []int{1, 2} {
		if els[idx].ParentIdx != 0 {
			t.Errorf("els[%d].ParentIdx = %d, want 0", idx, els[idx].ParentIdx)
		}
		if len(els[idx].Children) != 0 {
			t.Errorf("els[%d].Children = %v, want none", idx, els[idx].Children)
		}
	}
	// class attribute wins over the tag name; the tag name is the fallback.
	if els[1].Class != "android.widget.Button" {
		t.Errorf("els[1].Class = %q, want tag name fallback", els[1].Class)
	}
	if els[2].Class != "custom.View" {
		t.Errorf("els[2].Class = %q, want %q", els[2].Class, "custom.View")
	}
}

func TestParseUia2XmlEntityOrder(t *testing.T) {
	xml := `<hierarchy rotation="0"><node index="0" text="A &amp;lt; B" resource-id="" class="X" content-desc="" bounds="[0,0][100,100]"/></hierarchy>`
	els, err := ParseUia2Xml(xml)
	if err != nil {
		t.Fatalf("ParseUia2Xml: %v", err)
	}
	// `&amp;` is decoded last, so `&amp;lt;` becomes the literal text `&lt;`,
	// not `<`. Decoding in the wrong order double-decodes.
	if got, want := els[0].Text, "A &lt; B"; got != want {
		t.Errorf("Text = %q, want %q", got, want)
	}

	xml = `<hierarchy rotation="0"><node index="0" text="&amp;&lt;&#65;" resource-id="" class="X" content-desc="" bounds="[0,0][100,100]"/></hierarchy>`
	els, err = ParseUia2Xml(xml)
	if err != nil {
		t.Fatalf("ParseUia2Xml: %v", err)
	}
	if got, want := els[0].Text, "&<A"; got != want {
		t.Errorf("Text = %q, want %q", got, want)
	}

	// Numeric entities: 0 is valid, out-of-range and surrogate code points
	// decode to empty instead of panicking.
	xml = `<hierarchy rotation="0"><node index="0" text="&#0;&#1114112;&#55296;&#x;" resource-id="" class="X" content-desc="" bounds="[0,0][100,100]"/></hierarchy>`
	els, err = ParseUia2Xml(xml)
	if err != nil {
		t.Fatalf("ParseUia2Xml: %v", err)
	}
	if got, want := els[0].Text, "\x00"+"&#x;"; got != want {
		t.Errorf("Text = %q, want %q", got, want)
	}
}

func TestParseUia2XmlBoundsCenter(t *testing.T) {
	xml := `<hierarchy rotation="0">
  <node index="0" text="" class="X" bounds="[10,20][110,220]"/>
  <node index="1" text="" class="X" bounds="[0,0][101,101]"/>
  <node index="2" text="" class="X" bounds="[10,10][5,5]"/>
  <node index="3" text="" class="X"/>
</hierarchy>`
	els, err := ParseUia2Xml(xml)
	if err != nil {
		t.Fatalf("ParseUia2Xml: %v", err)
	}
	if got, want := *els[0].Bounds, (Bounds{L: 10, T: 20, R: 110, B: 220}); got != want {
		t.Errorf("Bounds = %+v, want %+v", got, want)
	}
	if got, want := *els[0].Center, (Point{X: 60, Y: 120}); got != want {
		t.Errorf("Center = %+v, want %+v", got, want)
	}
	// Math.round semantics: (0+101)/2 = 50.5 rounds up, it does not truncate.
	if got, want := *els[1].Center, (Point{X: 51, Y: 51}); got != want {
		t.Errorf("Center = %+v, want %+v (round half up)", got, want)
	}
	// Inverted rectangles and missing bounds are both "no bounds".
	if els[2].Bounds != nil || els[2].Center != nil {
		t.Errorf("inverted bounds: Bounds = %+v, Center = %+v, want nil/nil", els[2].Bounds, els[2].Center)
	}
	if els[3].Bounds != nil || els[3].Center != nil {
		t.Errorf("missing bounds: Bounds = %+v, Center = %+v, want nil/nil", els[3].Bounds, els[3].Center)
	}
}

func TestParseUia2XmlDisplayedFalseOffScreen(t *testing.T) {
	xml := `<hierarchy rotation="0">
  <node index="0" text="" class="X" displayed="false" bounds="[0,0][10,10]"/>
  <node index="1" text="" class="X" displayed="true" bounds="[0,0][10,10]"/>
  <node index="2" text="" class="X" bounds="[0,0][10,10]"/>
</hierarchy>`
	els, err := ParseUia2Xml(xml)
	if err != nil {
		t.Fatalf("ParseUia2Xml: %v", err)
	}
	if !els[0].OffScreen {
		t.Error(`displayed="false": OffScreen = false, want true`)
	}
	if els[1].OffScreen || els[2].OffScreen {
		t.Error("missing/displayed=true: OffScreen = true, want false")
	}
}

func TestParseUia2XmlBoolAttrs(t *testing.T) {
	xml := `<hierarchy rotation="0">
  <node class="X" clickable="true" long-clickable="true" scrollable="true" checkable="true" checked="true" focusable="true" focused="true" selected="true" password="true"/>
  <node class="X" longClickable="true"/>
  <node class="X" clickable="false"/>
</hierarchy>`
	els, err := ParseUia2Xml(xml)
	if err != nil {
		t.Fatalf("ParseUia2Xml: %v", err)
	}
	all := els[0]
	if !all.Clickable || !all.LongClickable || !all.Scrollable || !all.Checkable || !all.Checked ||
		!all.Focusable || !all.Focused || !all.Selected || !all.Password {
		t.Errorf("bool attrs not all set: %+v", *all)
	}
	// The camelCase spelling is the alternate name for the same field.
	if !els[1].LongClickable {
		t.Error("longClickable=true: LongClickable = false, want true")
	}
	if els[2].Clickable {
		t.Error(`clickable="false": Clickable = true, want false`)
	}
}

func TestParseUia2XmlQuoteInAttribute(t *testing.T) {
	xml := `<hierarchy rotation="0"><node index="0" text="a &gt; b" resource-id="" class="android.widget.Button" content-desc="" clickable="true" bounds="[0,0][100,100]"/></hierarchy>`
	els, err := ParseUia2Xml(xml)
	if err != nil {
		t.Fatalf("ParseUia2Xml: %v", err)
	}
	if len(els) != 1 {
		t.Fatalf("len(els) = %d, want 1", len(els))
	}
	if els[0].Text != "a > b" {
		t.Errorf("Text = %q, want %q", els[0].Text, "a > b")
	}
	if !els[0].Clickable {
		t.Error("Clickable = false, want true")
	}
	if want := (Bounds{L: 0, T: 0, R: 100, B: 100}); *els[0].Bounds != want {
		t.Errorf("Bounds = %+v, want %+v", *els[0].Bounds, want)
	}

	// A literal '>' inside a quoted value (machine-generated XML tolerates it)
	// must not end the tag early either.
	xml = `<hierarchy rotation="0"><node index="0" text="a > b" class="X" bounds="[0,0][100,100]"/></hierarchy>`
	els, err = ParseUia2Xml(xml)
	if err != nil {
		t.Fatalf("ParseUia2Xml: %v", err)
	}
	if len(els) != 1 {
		t.Fatalf("len(els) = %d, want 1", len(els))
	}
	if els[0].Text != "a > b" {
		t.Errorf("Text = %q, want %q", els[0].Text, "a > b")
	}
	if want := (Bounds{L: 0, T: 0, R: 100, B: 100}); *els[0].Bounds != want {
		t.Errorf("Bounds = %+v, want %+v", *els[0].Bounds, want)
	}
}

func TestParseNoHierarchyErrors(t *testing.T) {
	for _, in := range []string{"", "<no-hierarchy/>", "<?xml version='1.0'?><foo/>"} {
		_, err := ParseUia2Xml(in)
		if err == nil {
			t.Fatalf("ParseUia2Xml(%q) = nil error, want no-hierarchy error", in)
		}
		if !strings.Contains(err.Error(), "contained no <hierarchy>") {
			t.Errorf("ParseUia2Xml(%q) error = %q, want mention of %q", in, err, "contained no <hierarchy>")
		}
	}

	// Zero nodes is an error too: the agent treats it as a blind screen.
	for _, in := range []string{`<hierarchy rotation="0"></hierarchy>`, `<hierarchy/>`} {
		_, err := ParseUia2Xml(in)
		if err == nil {
			t.Fatalf("ParseUia2Xml(%q) = nil error, want zero-nodes error", in)
		}
		if !strings.Contains(err.Error(), "zero nodes") {
			t.Errorf("ParseUia2Xml(%q) error = %q, want mention of %q", in, err, "zero nodes")
		}
	}
}

func TestGetLayout(t *testing.T) {
	orig := uia2Source
	defer func() { uia2Source = orig }()

	uia2Source = func(serial string) (string, error) {
		if serial != "emulator-5554" {
			return "", NewDroidJevError("unexpected serial " + serial)
		}
		return sampleXML, nil
	}
	layout, err := GetLayout("emulator-5554")
	if err != nil {
		t.Fatalf("GetLayout: %v", err)
	}
	if len(layout.Elements) != 5 {
		t.Errorf("len(Elements) = %d, want 5", len(layout.Elements))
	}
	if layout.Xml != sampleXML {
		t.Error("Xml is not the raw dump")
	}
	// The root element's package is the focused window's package.
	if layout.RootPkg != "com.example" {
		t.Errorf("RootPkg = %q, want %q", layout.RootPkg, "com.example")
	}
	if layout.TookMs < 0 {
		t.Errorf("TookMs = %d, want >= 0", layout.TookMs)
	}

	boom := errors.New("dump failed")
	uia2Source = func(string) (string, error) { return "", boom }
	if _, err := GetLayout("emulator-5554"); !errors.Is(err, boom) {
		t.Errorf("GetLayout error = %v, want %v", err, boom)
	}
}

const settleXMLA = `<hierarchy rotation="0"><node index="0" text="A" class="X" bounds="[0,0][10,10]"/></hierarchy>`
const settleXMLB = `<hierarchy rotation="0"><node index="0" text="B" class="X" bounds="[0,0][10,10]"/></hierarchy>`

func TestSettleLayout(t *testing.T) {
	orig := uia2Source
	defer func() { uia2Source = orig }()

	// A changed screen, then two identical dumps: the second B is the settle.
	dumps := []string{settleXMLA, settleXMLB, settleXMLB}
	calls := 0
	uia2Source = func(string) (string, error) {
		if calls >= len(dumps) {
			return "", NewDroidJevError("unexpected extra dump")
		}
		xml := dumps[calls]
		calls++
		return xml, nil
	}
	layout, err := SettleLayout("emulator-5554", 5000)
	if err != nil {
		t.Fatalf("SettleLayout: %v", err)
	}
	if layout.Xml != settleXMLB {
		t.Errorf("settled Xml = %q, want the last B dump", layout.Xml)
	}
	if calls != 3 {
		t.Errorf("dump calls = %d, want 3", calls)
	}
}

func TestSettleLayoutDeadline(t *testing.T) {
	orig := uia2Source
	defer func() { uia2Source = orig }()

	calls := 0
	uia2Source = func(string) (string, error) {
		calls++
		return settleXMLA, nil
	}
	// maxMs 0: one dump, then the deadline check returns it without a second.
	layout, err := SettleLayout("emulator-5554", 0)
	if err != nil {
		t.Fatalf("SettleLayout: %v", err)
	}
	if layout.Xml != settleXMLA {
		t.Errorf("settled Xml = %q, want the first dump", layout.Xml)
	}
	if calls != 1 {
		t.Errorf("dump calls = %d, want 1", calls)
	}
}

func TestSettleLayoutError(t *testing.T) {
	orig := uia2Source
	defer func() { uia2Source = orig }()

	boom := errors.New("dump failed")
	uia2Source = func(string) (string, error) { return "", boom }
	if _, err := SettleLayout("emulator-5554", 5000); !errors.Is(err, boom) {
		t.Errorf("SettleLayout error = %v, want %v", err, boom)
	}
}

func TestXmlHash(t *testing.T) {
	a := xmlHash(settleXMLA)
	if len(a) != 16 {
		t.Errorf("len(xmlHash) = %d, want 16", len(a))
	}
	if a != xmlHash(settleXMLA) {
		t.Error("xmlHash is not deterministic")
	}
	if a == xmlHash(settleXMLB) {
		t.Error("different XML hashed equal")
	}
}
