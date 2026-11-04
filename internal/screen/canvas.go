// Package screen renders 128x64 monochrome frames for the Flipper Zero display.
package screen

import (
	"image"
	"image/color"
	"strings"
	"unicode"
)

const (
	Width  = 128
	Height = 64
)

// Canvas is a 128x64 1-bit drawing surface. true = black (pixel on).
type Canvas struct {
	px [Height][Width]bool
}

// Set sets a pixel, ignoring out-of-bounds coordinates.
func (c *Canvas) Set(x, y int, on bool) {
	if x < 0 || y < 0 || x >= Width || y >= Height {
		return
	}
	c.px[y][x] = on
}

// Get returns a pixel.
func (c *Canvas) Get(x, y int) bool {
	if x < 0 || y < 0 || x >= Width || y >= Height {
		return false
	}
	return c.px[y][x]
}

// Fill fills a rectangle.
func (c *Canvas) Fill(x, y, w, h int, on bool) {
	for j := y; j < y+h; j++ {
		for i := x; i < x+w; i++ {
			c.Set(i, j, on)
		}
	}
}

// Bitmap draws rows of '#'/'.' strings at (x, y).
func (c *Canvas) Bitmap(x, y int, rows []string, on bool) {
	for j, row := range rows {
		for i, ch := range row {
			if ch == '#' {
				c.Set(x+i, y+j, on)
			}
		}
	}
}

// Text draws s with the top-left corner at (x, y), pixels set to on. Returns end x.
func (c *Canvas) Text(f *Font, x, y int, s string, on bool) int {
	for _, r := range s {
		g := f.glyph(r)
		for row := 0; row < f.Height; row++ {
			for col := 0; col < f.Width; col++ {
				b := g[row*f.rowBytes+col/8]
				if b&(0x80>>(col%8)) != 0 {
					c.Set(x+col, y+row, on)
				}
			}
		}
		x += f.Width
	}
	return x
}

// XBM encodes the canvas as used by canvas_draw_xbm: row-major, 16 bytes per row, LSB = leftmost pixel.
func (c *Canvas) XBM() []byte {
	out := make([]byte, Width*Height/8)
	for y := 0; y < Height; y++ {
		for x := 0; x < Width; x++ {
			if c.px[y][x] {
				out[y*(Width/8)+x/8] |= 1 << (x % 8)
			}
		}
	}
	return out
}

// FromFramebuffer decodes a Flipper screen stream frame (u8g2 tile layout: 8 rows per page, LSB = top).
func FromFramebuffer(data []byte) *Canvas {
	c := &Canvas{}
	for y := 0; y < Height; y++ {
		for x := 0; x < Width; x++ {
			i := (y/8)*Width + x
			if i < len(data) && data[i]&(1<<(y%8)) != 0 {
				c.px[y][x] = true
			}
		}
	}
	return c
}

// Image converts the canvas to an image (Flipper-like orange background), scaled by s.
func (c *Canvas) Image(s int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, Width*s, Height*s))
	bg := color.RGBA{R: 0xfe, G: 0x8a, B: 0x2c, A: 0xff}
	fg := color.RGBA{A: 0xff}
	for y := 0; y < Height*s; y++ {
		for x := 0; x < Width*s; x++ {
			if c.px[y/s][x/s] {
				img.Set(x, y, fg)
			} else {
				img.Set(x, y, bg)
			}
		}
	}
	return img
}

// Wrap splits text into lines of at most cols runes, breaking on spaces where possible.
func Wrap(text string, cols int) []string {
	var lines []string
	for _, para := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		para = strings.Map(func(r rune) rune {
			if r == '\t' {
				return ' '
			}
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, para)
		rs := []rune(strings.TrimRight(para, " "))
		if len(rs) == 0 {
			lines = append(lines, "")
			continue
		}
		for len(rs) > cols {
			cut := cols
			for i := cols; i > cols/2; i-- {
				if rs[i] == ' ' {
					cut = i
					break
				}
			}
			lines = append(lines, string(rs[:cut]))
			rs = rs[cut:]
			for len(rs) > 0 && rs[0] == ' ' {
				rs = rs[1:]
			}
		}
		if len(rs) > 0 {
			lines = append(lines, string(rs))
		}
	}
	return lines
}
