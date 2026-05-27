// Package display has displaying functions.
package display

import (
	"flag"
	"fmt"
	"regexp"
	"strings"

	"github.com/mattn/go-runewidth"
	log "github.com/sirupsen/logrus"
	//nolint:staticcheck
	"golang.org/x/crypto/ssh/terminal"
)

var (
	useSuspend = flag.Bool("atomic_screen_updates", true, "Use atomic screen updates. Not supported on Windows, possibly other terminals too.")
)

// 8 Color mode colors.
const (
	Red8   = "\033[31m"
	BgRed8 = "\033[41m"
	White8 = "\033[37m"
)

// 16 bit color mode bright colors.
const (
	BrightRed   = "\033[31;1m"
	BgBrightRed = "\033[41;1m"
)

// 256 color mode colors.
const (
	Black   = "\033[38;5;0m"
	Red     = "\033[38;5;1m"
	Green   = "\033[38;5;2m"
	Yellow  = "\033[38;5;3m"
	Blue    = "\033[38;5;4m"
	Magenta = "\033[38;5;5m"
	Cyan    = "\033[38;5;6m"
	Grey    = "\033[38;5;7m"
	White   = "\033[38;5;15m"

	BgBlack = "\033[48;5;232m"

	//
	// 256 color backgrounds.
	//

	BgRed256  = "\033[48;5;1m"
	BgGrey256 = "\033[48;5;7m"

	//
	// Style
	//

	Bold          = "\033[1m"
	Underline     = "\033[4m"
	Reverse       = "\033[7m"
	Reset         = "\033[0m"
	NoWrap        = "\033[?7l"
	DoWrap        = "\033[?7h"
	ResetScroll   = "\033[r"
	SaveCursor    = "\033[s"         // TODO: actually this may not be supported
	RestoreCursor = "\033[u"         // by some terminals. Find some other way?
	HideCursor    = "\033[?25l"      // TODO: actually this may not be supported
	ShowCursor    = "\033[?25h"      // by some terminals. Find some other way?
	Suspend       = "\033P=1s\033\\" // https://gitlab.freedesktop.org/terminal-wg/specifications/-/merge_requests/2
	Resume        = "\033P=2s\033\\"

	// Normal is not the same as Reset, because Reset resets Bold/Underline/Reverse.
	Normal = White + BgBlack
)

type cursor struct {
	x, y int
}

// Color returns ANSI escape for a given color index.
func Color(n int) string {
	return fmt.Sprintf("\033[38;5;%dm", n)
}

// TerminalTitle returns ANSI sequence to change the terminal title.
func TerminalTitle(s string) string {
	return fmt.Sprintf("\033]0;%s\007", s)
}

// TermSize returns the terminal size.
func TermSize() (int, int, error) {
	return terminal.GetSize(0)
}

// Screen is a screen.
type Screen struct {
	Width      int
	Height     int
	buffer     []string
	prevBuffer []string
	useCache   bool
	cursor     *cursor
	PostDraw   string
}

// NewScreen creates a new screen.
func NewScreen() (*Screen, error) {
	w, h, err := TermSize()
	if err != nil {
		return nil, err
	}
	return NewScreen2(w, h), nil
}

// Copy copies a screen.
func (s *Screen) Copy() *Screen {
	r := &Screen{
		Width:  s.Width,
		Height: s.Height,
		buffer: make([]string, len(s.buffer)),
	}
	copy(r.buffer, s.buffer)
	return r
}

// NewScreen2 creates a new screen with given dimensions.
func NewScreen2(w int, h int) *Screen {
	return &Screen{
		Width:  w,
		Height: h,
		buffer: make([]string, h),
	}
}

// Clear clears the screen.
func (s *Screen) Clear() {
	s.buffer = make([]string, s.Height)
}

// findScroll return the scroll offset (lines) and index of first line to scroll
func findScroll(prev, cur []string) (int, int) {
	if len(prev) != len(cur) {
		return 0, 0
	}

	win := 0
	start := 0
	score := 0
	for ofs := -len(prev); ofs < len(prev); ofs++ {
		first := -1
		cnt := 0
		for i := 0; i < len(cur); i++ {
			if i+ofs >= 0 && i+ofs < len(prev) && prev[i+ofs] == cur[i] {
				cnt++
				if first == -1 {
					first = i
				}
			}
		}
		if score < cnt {
			win = ofs
			score = cnt
			start = first
		}
	}
	//log.Infof("Line diff score: ofs=%d score=%d", win, score)
	return win, start
}

// UseCache tells screen to use the cache.
func (s *Screen) UseCache() {
	s.useCache = true
}

// Draw redraws the screen.
func (s *Screen) Draw() {
	var o []string
	if s.useCache {
		ofs, start := findScroll(s.prevBuffer, s.buffer)
		if ofs != 0 {
			head := s.prevBuffer[:start]
			if ofs > 0 {
				// Scroll down.
				log.Debugf("Scroll %d First: %d", ofs, start)
				o = append(o, fmt.Sprintf("\033[%d;%dr\033[%dS", start+1, len(s.buffer)-1, ofs))
				// TODO: Don't needlessly redraw bottom.
				s.prevBuffer = append(head, s.prevBuffer[start+ofs:]...)
			} else {
				// Scroll up.
				log.Debugf("Scroll %d, first %d", ofs, start)
				o = append(o, fmt.Sprintf("\033[%d;%dr\033[%dT", start, len(s.buffer)-1, -ofs))
				head := s.prevBuffer[:start+ofs]
				mid := make([]string, -ofs)
				rest := s.prevBuffer[start+ofs:]
				s.prevBuffer = append(head, append(mid, rest...)...)
			}
		}
	} else {
		s.prevBuffer = nil
	}
	saved := 0
	for n, l := range s.buffer {
		if n < len(s.prevBuffer) && s.prevBuffer[n] == s.buffer[n] {
			saved++
			continue
		}
		log.Debugf("Line redraw miss: %d %q", n, l)
		l = FixedANSIWidthRight(l, s.Width)
		o = append(o, fmt.Sprintf("\033[%d;%dH%s%s%s", n+1, 1, NoWrap, l, Reset))
	}
	s.prevBuffer = s.buffer
	s.buffer = s.Copy().buffer

	// Place the cursor at the end and reset scroll.
	o = append(o, fmt.Sprintf("\033[%d;%dH", len(s.buffer), s.Width))
	// Reset scroll.
	o = append(o, SaveCursor+ResetScroll+RestoreCursor)

	// Place cursor
	if s.cursor != nil {
		o = append(o, fmt.Sprintf("\033[%d;%dH", s.cursor.y+1, s.cursor.x))
		s.cursor = nil
	}

	os := HideCursor + strings.Join(o, "") + ShowCursor + s.PostDraw
	if *useSuspend {
		os = Suspend + os + Resume
	}
	fmt.Print(os)
	s.PostDraw = ""
	log.Debugf("Saved %d out of %d line while drawing. %d bytes", saved, len(s.buffer), len(os))
	s.useCache = false
}

// SetCursor sets the cursor position.
func (s *Screen) SetCursor(y, x int) {
	s.cursor = &cursor{x: x, y: y}
}

var (
	stripANSIRE = regexp.MustCompile(`\x1b(?:\[[0-9;]*[a-zA-Z]|_G.*?\x1b\\|].*?(?:\x1b\\|\x07))?`)
)

func stripANSI(s string) string {
	return stripANSIRE.ReplaceAllString(s, "")
}

// StringWidth returns the render width of a string.
func StringWidth(s string) int {
	return runewidth.StringWidth(stripANSI(s))
}

// FixedWidth returns a fixed width version of a string.
func FixedWidth(s string, w int) string {
	return runewidth.FillLeft(runewidth.Truncate(s, w, ""), w)
}

// FixedANSIWidthRight returns a fixed width version of a string, padding on the right.
// The function will not strip ANSI codes, nor count them as "length".
func FixedANSIWidthRight(s string, w int) string {
	runes := []rune(s)
	currentWidth := 0
	breakIdx := 0
	inANSI := false

	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '\x1b' {
			inANSI = true
		}
		if inANSI {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '\\' || r == '\x07' {
				if r == '\\' && i > 0 && runes[i-1] == '\x1b' {
					inANSI = false
				} else if r == '\x07' {
					inANSI = false
				} else if r != ';' && r != '?' && r != '[' && !(r >= '0' && r <= '9') {
					inANSI = false
				}
			}
			breakIdx = i + 1
			continue
		}

		rw := runewidth.RuneWidth(r)
		if currentWidth+rw > w {
			break
		}
		currentWidth += rw
		breakIdx = i + 1
	}

	res := string(runes[:breakIdx])
	if currentWidth < w {
		res += strings.Repeat(" ", w-currentWidth)
	}
	return res
}

// Printlnf sets the content of a line to be a printfed string
func (s *Screen) Printlnf(y int, fmts string, args ...interface{}) {
	if y >= s.Height {
		log.Warningf("Print off screen. %d>=%d", y, s.Height)
		return
	}
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

// Exit resets the output for exit.
func Exit() {
	fmt.Println(SaveCursor + Reset + DoWrap + ResetScroll + RestoreCursor)
}

// Wrap wraps a string to a given width, being aware of ANSI escape sequences.
func Wrap(s string, w int) []string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if len(l) == 0 {
			lines = append(lines, "")
			continue
		}
		for StringWidth(l) > 0 {
			if StringWidth(l) <= w {
				lines = append(lines, l)
				break
			}

			// Find the break point that results in visible width <= w.
			// We iterate through runes to be safe with multi-byte chars and ANSI.
			breakIdx := 0
			currentWidth := 0
			inANSI := false
			runes := []rune(l)
			for i := 0; i < len(runes); i++ {
				r := runes[i]
				if r == '\x1b' {
					inANSI = true
				}
				if inANSI {
					if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '\\' || r == '\x07' {
						// Potential end of sequence.
						// Check for Kitty/OSC terminations.
						if r == '\\' && i > 0 && runes[i-1] == '\x1b' {
							inANSI = false
						} else if r == '\x07' {
							inANSI = false
						} else if r != ';' && r != '?' && r != '[' && !(r >= '0' && r <= '9') {
							// Standard CSI/other ends on a letter.
							inANSI = false
						}
					}
					breakIdx = i + 1
					continue
				}

				rw := runewidth.RuneWidth(r)
				if currentWidth+rw > w {
					break
				}
				currentWidth += rw
				breakIdx = i + 1
			}
			lines = append(lines, string(runes[:breakIdx]))
			l = string(runes[breakIdx:])
		}
	}
	return lines
}
