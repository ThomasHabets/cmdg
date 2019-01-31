package dialog

import (
	"fmt"
	"strings"

	"github.com/ThomasHabets/cmdg/pkg/display"
	"github.com/ThomasHabets/cmdg/pkg/input"
)

var (
	ErrAborted = fmt.Errorf("dialog aborted")
)

type Option struct {
	Key    string
	KeyInt int
	Label  string
}

func (o *Option) String() string {
	if o.Label == "" {
		return fmt.Sprintf("%s", o.Key)
	}
	return o.Label
}

// Question asks the user a multiple-choice question.
// ^C is always a valid option.
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
	prefix := strings.Repeat(" ", (screen.Width-widest)/2-4)

	start := (screen.Height-len(opts))/2 - 2
	screen.Printlnf(start, "%s", strings.Repeat("—", screen.Width))
	screen.Printlnf(start+1, "%s%s", strings.Repeat(" ", (screen.Width-len(title))/2), title)
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

func filterSubmatch(opts []*Option, filter string) []*Option {
	var ret []*Option
	for _, o := range opts {
		if strings.Contains(strings.ToLower(o.String()), strings.ToLower(filter)) {
			ret = append(ret, o)
		}
	}
	return ret
}

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

// Entry asks for a free-form input (e.g. Search).
func Entry(prompt string, keys *input.Input) (string, error) {
	screen, err := display.NewScreen()
	if err != nil {
		return "", err
	}
	cur := ""
	prefix := "    "
	for {
		start := 3
		screen.Printlnf(start+2, "%s%s%s%s%s", prefix, display.Bold, prompt, display.Reset, cur)
		screen.Draw()
		select {
		case key := <-keys.Chan():
			switch key {
			case input.Enter:
				return cur, nil
			case input.Backspace, input.CtrlH:
				if len(cur) > 0 {
					cur = cur[:len(cur)-1]
				}
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
	for {
		start := 3
		prefix := "    "
		screen.Printlnf(2, "%s%s%s", prefix, prompt, cur)
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
			if len(cur) > 0 {
				cur = cur[:len(cur)-1]
			}
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
