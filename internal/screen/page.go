package screen

import (
	"fmt"
	"time"
)

// Level of a notification; selects the title icon.
type Level int

const (
	LevelInfo Level = iota
	LevelOK
	LevelFail
)

// ParseLevel parses "info", "ok", "fail".
func ParseLevel(s string) (Level, error) {
	switch s {
	case "", "info":
		return LevelInfo, nil
	case "ok", "success":
		return LevelOK, nil
	case "fail", "error", "err":
		return LevelFail, nil
	}
	return 0, fmt.Errorf("unknown level %q", s)
}

func (l Level) String() string {
	switch l {
	case LevelOK:
		return "ok"
	case LevelFail:
		return "fail"
	default:
		return "info"
	}
}

var icons = map[Level][]string{
	LevelInfo: {
		"....#....",
		"...###...",
		"..#####..",
		"..#####..",
		"..#####..",
		".#######.",
		"#########",
		".........",
		"...###...",
	},
	LevelOK: {
		"........#",
		".......##",
		"......##.",
		"#....##..",
		"##..##...",
		".####....",
		"..##.....",
		".........",
		".........",
	},
	LevelFail: {
		"##.....##",
		".##...##.",
		"..##.##..",
		"...###...",
		"..##.##..",
		".##...##.",
		"##.....##",
		".........",
		".........",
	},
}

// Page is one notification screen.
type Page struct {
	Title  string
	Body   string
	Level  Level
	Index  int // 1-based position among pending notifications
	Total  int
	Age    time.Duration
	Scroll int // first visible body line
}

const (
	titleBar  = 13
	bodyLines = 4
)

// BodyCols is the number of text columns available for the body.
func BodyCols(f *Font) int { return (Width - 2) / f.Width }

// MaxScroll returns the maximum Scroll value for the page body.
func MaxScroll(f *Font, body string) int {
	n := len(Wrap(body, BodyCols(f))) - bodyLines
	if n < 0 {
		return 0
	}
	return n
}

// FormatAge formats a duration compactly: 5s, 3m, 2h, 4d.
func FormatAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// Render draws a notification page.
func Render(f *Font, p Page) *Canvas {
	c := &Canvas{}
	// Title bar: black background, white text.
	c.Fill(0, 0, Width, titleBar-1, true)
	c.Bitmap(1, 1, icons[p.Level], false)

	right := FormatAge(p.Age)
	if p.Total > 1 {
		right += fmt.Sprintf(" %d/%d", p.Index, p.Total)
	}
	rx := Width - 1 - len([]rune(right))*f.Width
	c.Text(f, rx, 0, right, false)

	titleX := 12
	cols := (rx - 3 - titleX) / f.Width
	title := []rune(p.Title)
	if len(title) > cols {
		title = append(title[:max(cols-1, 0)], '…')
		if !f.Has('…') {
			title[len(title)-1] = '~'
		}
	}
	c.Text(f, titleX, 0, string(title), false)

	// Body.
	lines := Wrap(p.Body, BodyCols(f))
	scroll := min(max(p.Scroll, 0), max(len(lines)-bodyLines, 0))
	for i := 0; i < bodyLines && scroll+i < len(lines); i++ {
		c.Text(f, 0, titleBar+1+i*f.Height, lines[scroll+i], true)
	}
	// Scrollbar on the right edge when the body does not fit.
	if len(lines) > bodyLines {
		top, h := titleBar+1, Height-titleBar-1
		for y := top; y < top+h; y += 2 {
			c.Set(Width-1, y, true)
		}
		thumb := max(h*bodyLines/len(lines), 3)
		pos := top + (h-thumb)*scroll/(len(lines)-bodyLines)
		c.Fill(Width-2, pos, 2, thumb, true)
	}
	return c
}
