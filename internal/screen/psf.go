package screen

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/binary"
	"io"
	"unicode/utf8"

	"github.com/go-faster/errors"
)

// Terminus Font 6x12 (Latin, Cyrillic, Greek), SIL OFL 1.1, see font/Terminus-LICENSE.txt.
//
//go:embed font/Uni2-Terminus12x6.psf.gz
var terminus12x6 []byte

// Font is a monospace bitmap font.
type Font struct {
	Width, Height int
	rowBytes      int
	glyphs        [][]byte
	index         map[rune]int
	fallback      int
}

// DefaultFont returns the embedded Terminus 6x12 font.
func DefaultFont() *Font {
	f, err := ParsePSF(terminus12x6)
	if err != nil {
		panic(err)
	}
	return f
}

// ParsePSF parses a PSF2 font, optionally gzip-compressed.
func ParsePSF(data []byte) (*Font, error) {
	if len(data) > 2 && data[0] == 0x1f && data[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, errors.Wrap(err, "gzip")
		}
		raw, err := io.ReadAll(zr)
		if err != nil {
			return nil, errors.Wrap(err, "gunzip")
		}
		data = raw
	}
	if len(data) < 32 || !bytes.Equal(data[:4], []byte{0x72, 0xb5, 0x4a, 0x86}) {
		return nil, errors.New("not a PSF2 font")
	}
	le := binary.LittleEndian
	var (
		headerSize = int(le.Uint32(data[8:]))
		flags      = le.Uint32(data[12:])
		count      = int(le.Uint32(data[16:]))
		charSize   = int(le.Uint32(data[20:]))
		height     = int(le.Uint32(data[24:]))
		width      = int(le.Uint32(data[28:]))
	)
	end := headerSize + count*charSize
	if end > len(data) {
		return nil, errors.New("truncated PSF2 font")
	}
	f := &Font{
		Width:    width,
		Height:   height,
		rowBytes: (width + 7) / 8,
		index:    make(map[rune]int),
	}
	for i := range count {
		off := headerSize + i*charSize
		f.glyphs = append(f.glyphs, data[off:off+charSize])
	}
	if flags&1 != 0 {
		// Unicode table: per glyph, UTF-8 runes, 0xFE starts a sequence (ignored), 0xFF ends glyph entry.
		tbl := data[end:]
		for g := 0; g < count && len(tbl) > 0; g++ {
			seq := false
			for len(tbl) > 0 {
				b := tbl[0]
				if b == 0xFF {
					tbl = tbl[1:]
					break
				}
				if b == 0xFE {
					seq = true
					tbl = tbl[1:]
					continue
				}
				r, n := utf8.DecodeRune(tbl)
				tbl = tbl[n:]
				if !seq {
					if _, ok := f.index[r]; !ok {
						f.index[r] = g
					}
				}
			}
		}
	} else {
		for i := range count {
			f.index[rune(i)] = i
		}
	}
	if g, ok := f.index['?']; ok {
		f.fallback = g
	}
	return f, nil
}

// Has reports whether the font has a glyph for r.
func (f *Font) Has(r rune) bool {
	_, ok := f.index[r]
	return ok
}

func (f *Font) glyph(r rune) []byte {
	if g, ok := f.index[r]; ok {
		return f.glyphs[g]
	}
	return f.glyphs[f.fallback]
}
