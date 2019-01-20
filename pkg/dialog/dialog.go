package dialog

import (
	"fmt"
	"strings"

	"github.com/ThomasHabets/cmdg/pkg/display"
	"github.com/ThomasHabets/cmdg/pkg/input"
)

type Option struct {
	Key   string
	Label string
}

func (o *Option) String() string {
	return fmt.Sprintf("%s — %s", o.Key, o.Label)
}

// ^C is always a valid option.
func Question(opts []Option, keys *input.Input) (string, error) {
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
	for n, l := range opts {
		screen.Printlnf(start+n+1, "%s%s", prefix, l.String())
	}
	screen.Printlnf(start+len(opts)+1, "%s", strings.Repeat("—", screen.Width))
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

func filterSubmatch(opts []string, filter string) []string {
	var ret []string
	for _, o := range opts {
		if strings.Contains(o, filter) {
			ret = append(ret, o)
		}
	}
	return ret
}

func Selection(opts []string, free bool, keys *input.Input) (string, error) {
	screen, err := display.NewScreen()
	if err != nil {
		return "", err
	}
	cur := ""
	selected := -1
	visible := opts
	for {
		start := 3
		prefix := "    "
		screen.Printlnf(2, "%sTo> %s", prefix, cur)
		for n, o := range visible {
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
				return cur, nil
			}
			return visible[selected], nil
		case input.CtrlN:
			selected++
			if selected >= len(visible) {
				selected = len(visible) - 1
			}
		case input.CtrlP:
			selected--
			if selected < 0 {
				selected = 0
			}
		default:
			if key == input.Backspace {
				if len(cur) > 0 {
					cur = cur[:len(cur)-1]
				}
			} else {
				cur += string(key)
			}
			selected = -1
			visible = filterSubmatch(opts, cur)
		}
	}
}
