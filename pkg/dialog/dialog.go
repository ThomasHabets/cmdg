// Package dialog provides a set of interactive dialogs for user.
package dialog

import (
	"fmt"
	"strings"

	"github.com/mattn/go-runewidth"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/ThomasHabets/cmdg/pkg/display"
	"github.com/ThomasHabets/cmdg/pkg/input"
)

var (
	// ErrAborted is returned when user pressed ^C.
	ErrAborted = fmt.Errorf("dialog aborted")
)

// Option is one option in a multiple-choice dialog.
type Option struct {
	Key    string
	KeyInt int
	Label  string
}

// String gives string representation usable for showing to the user.
func (o *Option) String() string {
	if o.Label == "" {
		return fmt.Sprintf("%s", o.Key)
	}
	return o.Label
}

// Message shows a message that's dismissed by pressing enter.
// Should not fail, but if it's important checking error value is optional.
// Any errors are logged.
func Message(title, message string, keys *input.Input) error {
	err := messageErr(title, message, keys)
	log.Errorf("Showing message: %v", err)
	return err
}

func printBox(screen *display.Screen, title, message string) (int, []string) {
	lines := strings.Split(message, "\n")
	widest := -1
	for _, l := range lines {
		if t := display.StringWidth(l); t > widest {
			widest = t
		}
	}

	// Decide on left padding.
	lpad := (screen.Width-widest)/2 - 4
	if lpad < 0 {
		lpad = 0
	}
	prefix := strings.Repeat(" ", lpad)
	titlePad := (screen.Width - len(title)) / 2
	if titlePad < 0 {
		titlePad = 0
	}

	startLine := (screen.Height-len(lines))/2 - 2
	if startLine < 0 {
		startLine = 0
	}
	for n := range lines {
		lines[n] = prefix + lines[n]
	}
	lines = append([]string{strings.Repeat(" ", titlePad) + title}, lines...)
	return startLine, lines
}

// messageErr shows message.
func messageErr(title, message string, keys *input.Input) error {
	log.Infof("Displaying title %q, message %q", title, message)
	screen, err := display.NewScreen()
	if err != nil {
		return errors.Wrap(err, "failed to create screen")
	}

	startLine, lines := printBox(screen, title, message)
	for n, l := range lines {
		screen.Printlnf(startLine+n, "%s", l)
	}
	screen.Draw()
	for {
		key := <-keys.Chan()
		switch key {
		case input.Enter:
			return nil
		}
	}
}

// Question asks the user a multiple-choice question.
// ^C is always a valid option, and returns ErrAborted.
// Example: `Should I send that email now?`
func Question(title string, opts []Option, keys *input.Input) (string, error) {
	screen, err := display.NewScreen()
	if err != nil {
		return "", err
	}
	widest := 0
	for _, l := range opts {
		if t := display.StringWidth(l.String()); t > widest {
			widest = t
		}
	}

	// TODO: break line if too long.
	pad := (screen.Width-widest)/2 - 4
	if pad < 0 {
		pad = 0
	}
	prefix := strings.Repeat(" ", pad)

	titlePad := (screen.Width - len(title)) / 2
	if titlePad < 0 {
		titlePad = 0
	}

	start := (screen.Height-len(opts))/2 - 2
	screen.Printlnf(start, "%s", strings.Repeat("—", screen.Width))
	screen.Printlnf(start+1, "%s%s", strings.Repeat(" ", titlePad), title)
	for n, l := range opts {
		screen.Printlnf(start+n+2, "%s%s", prefix, l.String())
	}
	screen.Printlnf(start+len(opts)+2, "%s", strings.Repeat("—", screen.Width))
	screen.Draw()
	for {
		key := <-keys.Chan()
		for _, o := range opts {
			if o.Key == string(key) {
				return o.Key, nil
			}
		}
		switch key {
		case input.CtrlC:
			return "^C", nil
		}
	}
}

// filterSubmatch filters out all options not matching input. Case insensitive.
func filterSubmatch(opts []*Option, filter string) []*Option {
	var ret []*Option
	for _, o := range opts {
		if strings.Contains(strings.ToLower(o.String()), strings.ToLower(filter)) {
			ret = append(ret, o)
		}
	}
	return ret
}

// Strings2Options takes a slice of strings and turns them into Options.
func Strings2Options(ss []string) []*Option {
	var ret []*Option
	for n, o := range ss {
		l := o
		if l == "" {
			l = "<empty>"
		}
		ret = append(ret, &Option{
			Key:    o,
			Label:  l,
			KeyInt: n,
		})
	}
	return ret
}

// TrimOneChar removes bytes until the printed size of the string is reduced.
// This is used by "backspace".
// TODO: should this use utf8.DecodeLastRuneInString to remove one codepoint at a time?
func TrimOneChar(s string) string {
	l := runewidth.StringWidth(s)
	if l == 0 {
		return s
	}
	for c := 1; c <= len(s); c++ {
		t := s[:len(s)-c]
		if runewidth.StringWidth(t) < l {
			return t
		}
	}
	log.Errorf("Can't happen: we cut away all the characters when trimming %q!", s)
	return s[:len(s)-1]
}

// Entry asks for a free-form input.
// Example: Search.
func Entry(prompt string, keys *input.Input) (string, error) {
	screen, err := display.NewScreen()
	if err != nil {
		return "", err
	}
	cur := ""
	prefix := "    "
	keys.PastePush(false)
	defer keys.PastePop()
	for {
		start := 3
		content := fmt.Sprintf("%s%s%s%s%s", prefix, display.Bold, prompt, display.Reset, cur)
		screen.Printlnf(start+2, "%s", content)
		screen.SetCursor(start+2, display.StringWidth(content)+1)
		screen.Draw()
		select {
		case key := <-keys.Chan():
			switch key {
			case input.Enter:
				return cur, nil
			case input.Backspace, input.CtrlH:
				cur = TrimOneChar(cur)
			case input.CtrlU:
				cur = ""
			case input.CtrlC:
				return "", ErrAborted
			default:
				cur += string(key)
			}
		}
	}
}

// Selection asks the user for a choice, with populated suggestions that can be searched in.
// If `free` is `true` then the user can input anything. If `false` then the options listed are the only valid ones.
// Example: Email recipient choice.
func Selection(opts []*Option, prompt string, free bool, keys *input.Input) (*Option, error) {
	screen, err := display.NewScreen()
	if err != nil {
		return nil, err
	}
	cur := ""
	last := ""
	selected := -1
	scroll := 0 // TODO, implement scrolling.
	visible := opts
	keys.PastePush(false)
	defer keys.PastePop()
	for {
		start := 3
		prefix := "    "
		content := fmt.Sprintf("%s%s%s", prefix, prompt, cur)
		screen.Printlnf(2, "%s", content)
		screen.SetCursor(2, display.StringWidth(content)+1)
		for n, o := range visible[scroll:] {
			sstr := display.Reset + " "
			if selected == n {
				sstr = display.Bold + ">"
			}
			screen.Printlnf(n+start, "%s%s %s", prefix, sstr, o)
		}

		// Clear the area.
		for n := len(visible); n < len(opts); n++ {
			screen.Printlnf(n+start, "")
		}

		screen.Draw()

		key := <-keys.Chan()
		switch key {
		case input.Enter:
			if selected < 0 {
				if !free {
					continue
				}
				return &Option{
					Key:   cur,
					Label: cur,
				}, nil
			}
			return visible[selected], nil
		case input.CtrlN:
			selected++
			if selected >= len(visible) {
				selected = len(visible) - 1
			}
		case input.CtrlP:
			selected--
			if selected < 0 && !free {
				selected = 0
			}
		case input.CtrlC:
			return nil, ErrAborted
		case input.Backspace, input.CtrlH:
			cur = TrimOneChar(cur)
		case input.CtrlU:
			cur = ""
		default:
			cur += string(key)
		}
		if last != cur {
			selected = -1
			visible = filterSubmatch(opts, cur)
			if !free && len(visible) > 0 {
				selected = 0
			}
		}
		last = cur
	}
}
