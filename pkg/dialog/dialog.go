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
