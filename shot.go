// Screenshot capture for the vision fallback: raw `adb exec-out screencap`
// bytes → downscaled RGB → PNG, sent to TypeSafe as an image asset only when the
// view hierarchy is empty.
//
// Ported from src/shot.js. The PNG is stdlib image/png: validity and dimensions
// are the contract, not byte-equality with node:zlib.

package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/png"
	"math"
	"time"
)

// TypeSafe image limits: each side 280..1568 px, total pixels
// 78,400..2,458,624 — an image outside them is rejected, never resized.
// Longest side ≤1568 keeps every phone screenshot inside both caps (an
// unresized 1080×2340 is already over the pixel cap); the floor only matters
// for tiny screens.
const (
	maxShotSide   = 1568
	minShotSide   = 280
	minShotPixels = 78400
)

// Screencap is a parsed raw screencap header. Offset is 12 or 16 (API 27+ adds
// a dataspace word); the size is identified by the total payload length.
type Screencap struct {
	W, H, Format, Bpp, Offset int
}

// ShotResult is the vision-fallback capture: a PNG sized for TypeSafe plus the
// source display dimensions and the capture cost.
type ShotResult struct {
	Png    []byte
	W, H   int
	TookMs int64
}

// ParseScreencap validates a raw screencap payload. Only RGBA_8888 (format 1)
// comes off the display-capture path; anything else still parses, and
// ScreenshotPng rejects it.
func ParseScreencap(b []byte) (Screencap, error) {
	if len(b) < 16 {
		return Screencap{}, NewDroidJevError("screencap output too short")
	}
	w := int(binary.LittleEndian.Uint32(b[0:]))
	h := int(binary.LittleEndian.Uint32(b[4:]))
	format := int(binary.LittleEndian.Uint32(b[8:]))
	bpp := 0
	switch format {
	case 1, 2, 5:
		bpp = 4
	case 3:
		bpp = 3
	case 4:
		bpp = 2
	}
	if bpp == 0 || w == 0 || h == 0 || w > 20000 || h > 20000 {
		return Screencap{}, NewDroidJevError(fmt.Sprintf("unexpected screencap header %dx%d format %d", w, h, format))
	}
	pixels := w * h * bpp
	offset := 0
	switch len(b) {
	case 16 + pixels:
		offset = 16
	case 12 + pixels:
		offset = 12
	}
	if offset == 0 {
		// A failed exec-out merges device stderr into stdout — that text is why
		// the length does not add up.
		return Screencap{}, NewDroidJevError(fmt.Sprintf("screencap payload is %d bytes, expected %d+header", len(b), pixels))
	}
	return Screencap{W: w, H: h, Format: format, Bpp: bpp, Offset: offset}, nil
}

// ScaleTarget is the aspect-preserving target size inside the API limits.
func ScaleTarget(w, h int) (int, int) {
	sMax := math.Min(1, float64(maxShotSide)/float64(max(w, h)))
	sMin := math.Max(
		math.Max(float64(minShotSide)/float64(w), float64(minShotSide)/float64(h)),
		math.Sqrt(float64(minShotPixels)/float64(w*h)),
	)
	s := sMax
	if sMin > sMax {
		s = sMin
	}
	clamp := func(v int) int { return min(maxShotSide, max(minShotSide, v)) }
	return clamp(int(math.Round(float64(w) * s))), clamp(int(math.Round(float64(h) * s)))
}

// DownscaleRGBA is a box-average RGBA→RGB resample: each destination pixel is
// the mean of the source rectangle it covers (nearest-ish when upscaling).
// Alpha is dropped — display captures are opaque.
//
// src must be a 4-byte-per-pixel capture (format 1): four bytes are read per
// source pixel, exactly as in the Node code, which ScreenshotPng guarantees by
// rejecting other formats before calling this.
func DownscaleRGBA(buf []byte, src Screencap, dw, dh int) []byte {
	out := make([]byte, dw*dh*3)
	sx := float64(src.W) / float64(dw)
	sy := float64(src.H) / float64(dh)
	for y := 0; y < dh; y++ {
		y0 := int(math.Floor(float64(y) * sy))
		y1 := min(src.H, max(y0+1, int(math.Floor(float64(y+1)*sy))))
		for x := 0; x < dw; x++ {
			x0 := int(math.Floor(float64(x) * sx))
			x1 := min(src.W, max(x0+1, int(math.Floor(float64(x+1)*sx))))
			r, g, b, n := 0, 0, 0, 0
			for yy := y0; yy < y1; yy++ {
				for xx := x0; xx < x1; xx++ {
					i := src.Offset + (yy*src.W+xx)*4
					r += int(buf[i])
					g += int(buf[i+1])
					b += int(buf[i+2])
					n++
				}
			}
			o := (y*dw + x) * 3
			out[o] = byte(math.Round(float64(r) / float64(n)))
			out[o+1] = byte(math.Round(float64(g) / float64(n)))
			out[o+2] = byte(math.Round(float64(b) / float64(n)))
		}
	}
	return out
}

// EncodePng wraps packed RGB triples in a PNG. image.NRGBA wants four bytes per
// pixel, so alpha is filled with 255 and image/png handles the rest.
func EncodePng(rgb []byte, w, h int) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		img.Pix[i*4] = rgb[i*3]
		img.Pix[i*4+1] = rgb[i*3+1]
		img.Pix[i*4+2] = rgb[i*3+2]
		img.Pix[i*4+3] = 255
	}
	var buf bytes.Buffer
	// A bytes.Buffer write cannot fail; the only png.Encode error is an I/O one.
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// ScreenshotPng is the full pipeline: raw capture → API-sized PNG.
func ScreenshotPng(serial string) (ShotResult, error) {
	t0 := time.Now()
	// maxBytes covers 4K-class displays (2160×3840×4 ≈ 33 MB raw).
	r, err := ExecOut(serial, []string{"screencap"}, RunOpts{TimeoutMs: 15000, MaxBytes: 64 * 1024 * 1024})
	if err != nil {
		return ShotResult{}, err
	}
	if r.Code != 0 {
		return ShotResult{}, NewDroidJevError(fmt.Sprintf("screencap exited %d", r.Code))
	}
	src, err := ParseScreencap(r.Stdout)
	if err != nil {
		return ShotResult{}, err
	}
	if src.Format != 1 {
		return ShotResult{}, NewDroidJevError(fmt.Sprintf("screencap pixel format %d is not RGBA_8888", src.Format))
	}
	dw, dh := ScaleTarget(src.W, src.H)
	rgb := DownscaleRGBA(r.Stdout, src, dw, dh)
	return ShotResult{
		Png:    EncodePng(rgb, dw, dh),
		W:      src.W,
		H:      src.H,
		TookMs: time.Since(t0).Milliseconds(),
	}, nil
}
