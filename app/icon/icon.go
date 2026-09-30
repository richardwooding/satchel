// Package icon draws satchel's icons in code — a bag with a handle — so the
// tray icon, the app icon and the favicon cannot drift apart, and no binary
// asset needs regenerating when the shape changes.
package icon

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
)

var (
	body   = color.NRGBA{0xa3, 0x71, 0xf7, 0xff} // gloam --gl-accent
	handle = color.NRGBA{0xbd, 0x93, 0xff, 0xff} // gloam --gl-accent-2
	clasp  = color.NRGBA{0x0d, 0x11, 0x17, 0xff} // gloam --gl-bg
	live   = color.NRGBA{0x3f, 0xb9, 0x50, 0xff} // gloam --gl-green
	panel  = color.NRGBA{0x0d, 0x11, 0x17, 0xff}
)

// Tray is a 64px transparent tray icon; active adds a green dot, shown while
// shares or receives are open.
func Tray(active bool) []byte { return encode(draw(64, false, active)) }

// App is a square app icon on a dark rounded tile.
func App(size int) []byte { return encode(draw(size, true, false)) }

// draw renders in a 32-unit design space, like the favicon, supersampled 4x
// per pixel for smooth edges.
func draw(size int, tile, active bool) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	scale := 32 / float64(size)
	const ss = 4
	for py := range size {
		for px := range size {
			var r, g, b, a float64
			for sy := range ss {
				for sx := range ss {
					x := (float64(px) + (float64(sx)+0.5)/ss) * scale
					y := (float64(py) + (float64(sy)+0.5)/ss) * scale
					c, ok := shade(x, y, tile, active)
					if !ok {
						continue
					}
					r += float64(c.R)
					g += float64(c.G)
					b += float64(c.B)
					a += 255
				}
			}
			n := float64(ss * ss)
			if a == 0 {
				continue
			}
			k := a / 255 // samples covered
			img.SetNRGBA(px, py, color.NRGBA{
				R: uint8(r / k), G: uint8(g / k), B: uint8(b / k), A: uint8(a / n),
			})
		}
	}
	return img
}

// shade reports the colour at a point in design space, topmost shape first.
// The shape is a bag, not a lock: a wide, low handle, a body wider than it
// is tall, and a flap across its top with the clasp on the flap's edge.
func shade(x, y float64, tile, active bool) (color.NRGBA, bool) {
	switch {
	case active && math.Hypot(x-26.5, y-7) <= 4.5:
		return live, true
	case inRoundRect(x, y, 14, 16, 18, 19.5, 1):
		return clasp, true
	case inRoundRect(x, y, 4.5, 12, 27.5, 18, 3) && y <= 17.5:
		return handle, true // the flap
	case inRoundRect(x, y, 4.5, 12, 27.5, 26.5, 3):
		return body, true
	case onHandle(x, y):
		return handle, true
	case tile && inRoundRect(x, y, 0, 0, 32, 32, 7):
		return panel, true
	}
	return color.NRGBA{}, false
}

func inRoundRect(x, y, x0, y0, x1, y1, r float64) bool {
	if x < x0 || x > x1 || y < y0 || y > y1 {
		return false
	}
	cx := math.Max(x0+r, math.Min(x, x1-r))
	cy := math.Max(y0+r, math.Min(y, y1-r))
	return math.Hypot(x-cx, y-cy) <= r
}

// onHandle is a half-ellipse ring above the bag, wider than it is tall.
func onHandle(x, y float64) bool {
	const cx, cy, rx, ry, w = 16, 12.5, 7, 5, 1.3
	if y > cy {
		return false
	}
	// Distance to the ellipse, approximated by scaling to a unit circle.
	d := math.Hypot((x-cx)/rx, (y-cy)/ry)
	return math.Abs(d-1)*math.Min(rx, ry) <= w
}

func encode(img image.Image) []byte {
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}
