package screen

import (
	"strings"
	"testing"
	"time"
)

func TestFont(t *testing.T) {
	f := DefaultFont()
	if f.Width != 6 || f.Height != 12 {
		t.Fatalf("size %dx%d", f.Width, f.Height)
	}
	for _, r := range "Aa09Яяё…" {
		if !f.Has(r) {
			t.Errorf("missing glyph %q", r)
		}
	}
}

func TestWrap(t *testing.T) {
	got := Wrap("hello world foo\nbar", 11)
	want := []string{"hello world", "foo", "bar"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", got)
	}
	if got := Wrap(strings.Repeat("x", 25), 10); len(got) != 3 {
		t.Fatalf("hard wrap: %q", got)
	}
}

func TestXBM(t *testing.T) {
	c := &Canvas{}
	c.Set(0, 0, true)
	c.Set(9, 1, true)
	b := c.XBM()
	if len(b) != 1024 || b[0] != 0x01 || b[16+1] != 0x02 {
		t.Fatalf("bad xbm: %x %x", b[0], b[17])
	}
}

func TestRender(t *testing.T) {
	f := DefaultFont()
	c := Render(f, Page{
		Title: "make build", Body: strings.Repeat("Сборка завершена ", 10),
		Level: LevelOK, Index: 1, Total: 2, Age: 3 * time.Minute,
	})
	if !c.Get(0, 0) {
		t.Fatal("title bar not drawn")
	}
	if MaxScroll(f, strings.Repeat("Сборка завершена ", 10)) == 0 {
		t.Fatal("expected scroll")
	}
}
