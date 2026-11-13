package main

import (
	"bytes"
	"encoding/binary"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

// header builds a raw screencap header with the given words. The 16-byte form
// carries the dataspace word API 27+ adds.
func screencapHeader(w, h, format int, words int) []byte {
	b := make([]byte, 16)
	binary.LittleEndian.PutUint32(b[0:], uint32(w))
	binary.LittleEndian.PutUint32(b[4:], uint32(h))
	binary.LittleEndian.PutUint32(b[8:], uint32(format))
	binary.LittleEndian.PutUint32(b[12:], 1) // dataspace
	return b[:words*4]
}

// Ports the parseScreencap test from the Node implementation: the header size
// is identified by the total payload length, not by a magic or version field.
func TestParseScreencap(t *testing.T) {
	px := bytes.Repeat([]byte{0xaa}, 2*2*4)

	h16 := screencapHeader(2, 2, 1, 4)
	src, err := ParseScreencap(append(append([]byte{}, h16...), px...))
	if err != nil {
		t.Fatalf("ParseScreencap(16-byte header): %v", err)
	}
	if want := (Screencap{W: 2, H: 2, Format: 1, Bpp: 4, Offset: 16}); src != want {
		t.Fatalf("ParseScreencap(16-byte header) = %+v, want %+v", src, want)
	}

	h12 := screencapHeader(2, 2, 1, 3)
	src, err = ParseScreencap(append(append([]byte{}, h12...), px...))
	if err != nil {
		t.Fatalf("ParseScreencap(12-byte header): %v", err)
	}
	if src.Offset != 12 {
		t.Fatalf("12-byte header offset = %d, want 12", src.Offset)
	}

	// Format → bytes-per-pixel: RGBA_8888 (1), RGBX_8888 (2) and BGRA_8888 (5)
	// are 4bpp; RGB_888 (3) is 3; RGB_565 (4) is 2.
	for _, tc := range []struct{ format, bpp int }{
		{1, 4}, {2, 4}, {5, 4}, {3, 3}, {4, 2},
	} {
		payload := bytes.Repeat([]byte{0x11}, 2*2*tc.bpp)
		hdr := screencapHeader(2, 2, tc.format, 4)
		got, err := ParseScreencap(append(append([]byte{}, hdr...), payload...))
		if err != nil {
			t.Fatalf("format %d: %v", tc.format, err)
		}
		if got.Bpp != tc.bpp {
			t.Fatalf("format %d bpp = %d, want %d", tc.format, got.Bpp, tc.bpp)
		}
	}

	// Anything shorter than a 12-byte header.
	if _, err := ParseScreencap(make([]byte, 8)); err == nil || !strings.Contains(err.Error(), "too short") {
		t.Fatalf("short buffer error = %v, want a 'too short' error", err)
	}

	// The payload length must match width×height×bpp plus 12 or 16.
	truncated := append(append([]byte{}, h16...), px[:4]...)
	if _, err := ParseScreencap(truncated); err == nil || !strings.Contains(err.Error(), "payload") {
		t.Fatalf("length-mismatch error = %v, want a 'payload' error", err)
	}

	// An unknown pixel format is rejected as a header error.
	bad := screencapHeader(2, 2, 99, 3)
	if _, err := ParseScreencap(append(append([]byte{}, bad...), px...)); err == nil || !strings.Contains(err.Error(), "header") {
		t.Fatalf("bad-format error = %v, want a 'header' error", err)
	}
}

// Ports the scaleTarget test: the longest side fits 1568 and the pixel count
// stays above the 78,400 floor; a tiny screen is upscaled to the 280px floor and
// an in-range screen passes through 1:1.
func TestScaleTarget(t *testing.T) {
	w, h := ScaleTarget(1080, 2340)
	if max(w, h) > 1568 {
		t.Fatalf("longest side = %d, want ≤1568 (%dx%d)", max(w, h), w, h)
	}
	if min(w, h) < 280 {
		t.Fatalf("shortest side = %d, want ≥280 (%dx%d)", min(w, h), w, h)
	}
	if px := w * h; px < 78400 || px > 2458624 {
		t.Fatalf("pixel count = %d, want within 78400..2458624 (%dx%d)", px, w, h)
	}

	if w, h := ScaleTarget(240, 320); w != 280 || h != 373 {
		t.Fatalf("ScaleTarget(240, 320) = %dx%d, want 280x373", w, h)
	}
	if w, h := ScaleTarget(720, 1280); w != 720 || h != 1280 {
		t.Fatalf("ScaleTarget(720, 1280) = %dx%d, want 720x1280", w, h)
	}
}

// The pipeline contract: box-average downscale then a PNG that image/png reads
// back with the same dimensions and pixels (alpha is dropped, so it reads 255).
func TestDownscaleThenPngRoundtrip(t *testing.T) {
	// 4×4 source: R = x*10, G = y*10, B = 255. A 2×2 destination block average
	// is exact: R ∈ {5, 25}, G ∈ {5, 25}.
	const size = 4
	buf := make([]byte, size*size*4)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			i := (y*size + x) * 4
			buf[i] = byte(x * 10)
			buf[i+1] = byte(y * 10)
			buf[i+2] = 255
			buf[i+3] = 255
		}
	}
	src := Screencap{W: size, H: size, Format: 1, Bpp: 4, Offset: 0}

	rgb := DownscaleRGBA(buf, src, 2, 2)
	wantRGB := []byte{
		5, 5, 255, 25, 5, 255,
		5, 25, 255, 25, 25, 255,
	}
	if !bytes.Equal(rgb, wantRGB) {
		t.Fatalf("DownscaleRGBA = %v, want %v", rgb, wantRGB)
	}

	pngBytes := EncodePng(rgb, 2, 2)
	if sig := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}; !bytes.HasPrefix(pngBytes, sig) {
		t.Fatalf("EncodePng output does not start with the PNG signature")
	}
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("png.Decode: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 2 || b.Dy() != 2 {
		t.Fatalf("decoded bounds = %v, want 2x2", b)
	}
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			want := wantRGB[(y*2+x)*3:]
			got := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			if got.R != want[0] || got.G != want[1] || got.B != want[2] || got.A != 255 {
				t.Fatalf("pixel (%d,%d) = %v, want rgb(%d,%d,%d) alpha 255", x, y, got, want[0], want[1], want[2])
			}
		}
	}
}
