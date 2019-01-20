package display

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/mattn/go-runewidth"
	log "github.com/sirupsen/logrus"
	"golang.org/x/crypto/ssh/terminal"
)

const (
	// In 8 color mode.
	Red8   = "\033[31m"
	BgRed8 = "\033[41m"

	// Bright colors (16 color mode).
	BrightRed   = "\033[31;1m"
	BgBrightRed = "\033[41;1m"

	// 256 color mode.
	Black   = "\033[38;5;0m"
	Red     = "\033[38;5;1m"
	Green   = "\033[38;5;2m"
	Yellow  = "\033[38;5;3m"
	Blue    = "\033[38;5;4m"
	Magenta = "\033[38;5;5m"
	Cyan    = "\033[38;5;6m"
	Grey    = "\033[38;5;7m"

	// 256 color backgrounds.
	BgRed256  = "\033[48;5;1m"
	BgGrey256 = "\033[48;5;7m"

	// Style
	Bold      = "\033[1m"
	Underline = "\033[4m"
	Reverse   = "\033[7m"

	Reset = "\033[0m"
)

func Color(n int) string {
	return fmt.Sprintf("\033[38;5;%dm", n)
}

func TermSize() (int, int, error) {
	return terminal.GetSize(0)
}

type Screen struct {
	Width  int
	Height int
	buffer []string
}

func NewScreen() (*Screen, error) {
	w, h, err := TermSize()
	if err != nil {
		return nil, err
	}
	return NewScreen2(w, h), nil
}

func NewScreen2(w int, h int) *Screen {
	return &Screen{
		Width:  w,
		Height: h,
		buffer: make([]string, h, h),
	}
}

func (s *Screen) Draw() {
	for n, l := range s.buffer {
		pad := ""
		if padlen := s.Width - StringWidth(l); padlen > 0 {
			pad = strings.Repeat(" ", padlen)
		}
		fmt.Printf("\033[%d;%dH%s%s%s", n+1, 1, l, pad, Reset)
	}
}

var (
	stripANSIRE = regexp.MustCompile(`\033(?:\[[^a-zA-Z]*(?:[A-Za-z])?)?`)
)

func stripANSI(s string) string {
	return stripANSIRE.ReplaceAllString(s, "")
}

func StringWidth(s string) int {
	return runewidth.StringWidth(stripANSI(s))
}

func FixedWidth(s string, w int) string {
	return runewidth.FillLeft(runewidth.Truncate(s, w, ""), w)
}

func (s *Screen) Printlnf(y int, fmts string, args ...interface{}) {
	str := fmt.Sprintf(fmts, args...)
	s.buffer[y] = str
}

// Printf prints to a given point on the screen.
func (s *Screen) Printf(y, x int, fmts string, args ...interface{}) {
	str := fmt.Sprintf(fmts, args...)
	strw := StringWidth(str)
	prefix := ""
	suffix := ""
	skip := ""
	for _, ru := range s.buffer[y] {
		pw := StringWidth(prefix)
		if pw < x {
			prefix += string(ru)
			continue
		}
		skipw := StringWidth(skip)
		if skipw < strw {
			skip += string(ru)
			continue
		}
		suffix += string(ru)
	}
	for StringWidth(prefix) < x {
		prefix += " "
	}
	b := prefix + str + suffix
	if false {
		log.Printf("%q %q %q", prefix, str, suffix)
	}
	if add := x - StringWidth(b); add > 0 {
		b += strings.Repeat(" ", add)
	}
	s.buffer[y] = b
}
