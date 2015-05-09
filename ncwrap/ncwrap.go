// Package ncwrap wraps ncurses to provide a race free interface to the UI.
package ncwrap

/*
 *  Copyright (C) 2015 Thomas Habets <thomas@habets.se>
 *
 *  This program is free software; you can redistribute it and/or modify
 *  it under the terms of the GNU General Public License as published by
 *  the Free Software Foundation; either version 2 of the License, or
 *  (at your option) any later version.
 *
 *  This program is distributed in the hope that it will be useful,
 *  but WITHOUT ANY WARRANTY; without even the implied warranty of
 *  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 *  GNU General Public License for more details.
 *
 *  You should have received a copy of the GNU General Public License along
 *  with this program; if not, write to the Free Software Foundation, Inc.,
 *  51 Franklin Street, Fifth Floor, Boston, MA 02110-1301 USA.
 */

import (
	"fmt"
	"log"
	"regexp"
	"strings"

	gc "github.com/rthornton128/goncurses"
)

// NCWrap wraps ncurses with cmdg-specific windows and thread safe input handling.
type NCWrap struct {
	root    *gc.Window
	wmain   *gc.Window
	whr     *gc.Window
	wstatus *gc.Window

	status chan string
	main   chan func(*gc.Window)
	redraw chan bool
	done   chan chan bool

	Input chan gc.Key
}

const (
	esc = "_!*(/"
)

func formatEscape(s string) string {
	s = strings.Replace(s, esc, esc+esc, -1)
	s = strings.Replace(s, "[", esc+"(", -1)
	s = strings.Replace(s, "]", esc+")", -1)
	return s
}

func formatUnescape(s string) string {
	s = strings.Replace(s, esc+"(", "[", -1)
	s = strings.Replace(s, esc+")", "]", -1)
	s = strings.Replace(s, esc+esc, esc, -1)
	return s
}

// ColorPrint prints something with color codes embedded.
func ColorPrint(w *gc.Window, f string, args ...interface{}) {
	newargs := []interface{}{}
	for n := range args {
		if s, ok := args[n].(*Preformated); ok {
			newargs = append(newargs, s.s)
		} else if s, ok := args[n].(string); ok {
			newargs = append(newargs, formatEscape(s))
		} else {
			newargs = append(newargs, args[n])
		}
	}
	s := fmt.Sprintf(f, newargs...)
	//colorRE := regexp.MustCompile(`(.*?)(\[color (\w+)\]([^a-z. ]+)?:)?(.*)`)
	colorRE := regexp.MustCompile(`(?s)(.*?)\[(\w+)\]([^[]*)`)
	w.ColorOn(1)
	w.AttrOff(gc.A_BOLD)
	for {
		m := colorRE.FindStringSubmatch(s)
		if len(m) == 0 {
			w.Print(formatUnescape(s))
			break
		}
		w.Print(formatUnescape(m[1]))
		switch m[2] {
		case "green":
			w.ColorOn(2)
		case "red":
			w.ColorOn(3)
		case "bold":
			w.AttrOn(gc.A_BOLD)
		case "reverse":
			w.ColorOn(4)
		case "unbold":
			w.AttrOff(gc.A_BOLD)
		default:
			w.ColorOn(1)
		}
		w.Printf("%s", formatUnescape(m[3]))
		s = s[len(m[0]):]
	}
}

// Preformated data is already formatted, and shouldn't be inspected for escape codes.
type Preformated struct {
	s string
}

// Preformat tells the output routines that something is already formatted.
func Preformat(s string) *Preformated {
	return &Preformated{s}
}

// Start sets up ncurses and takes over the terminal.
func Start() (*NCWrap, error) {
	nc := &NCWrap{}
	var err error
	nc.root, err = gc.Init()
	if err != nil {
		return nil, err
	}
	if err := gc.StartColor(); err != nil {
		return nil, err
	}

	gc.Echo(false)
	gc.Raw(true)
	gc.Cursor(1)
	gc.Cursor(0)

	if false {
		if err := gc.InitColor(100, 0, 255, 255); err != nil {
			return nil, fmt.Errorf("failed to set color: %v", err)
		}
	}

	h, w := nc.root.MaxYX()
	nc.wmain, err = gc.NewWindow(h-2, w, 0, 0)
	if err != nil {
		return nil, err
	}
	nc.whr, err = gc.NewWindow(1, w, h-2, 0)
	if err != nil {
		return nil, err
	}
	nc.wstatus, err = gc.NewWindow(1, w, h-1, 0)
	if err != nil {
		return nil, err
	}

	// Set up colors.
	if true {
		for n, c := range []struct{ fg, bg int16 }{
			{gc.C_WHITE, gc.C_BLACK},
			{gc.C_GREEN, gc.C_BLACK},
			{gc.C_RED, gc.C_BLACK},
			{gc.C_BLACK, gc.C_WHITE},
		} {
			log.Printf("InitPair(%v,%v,%v)", n+1, c.fg, c.bg)
			if err := gc.InitPair(int16(n+1), c.fg, c.bg); err != nil {
				return nil, fmt.Errorf("InitPair(%v,%v,%v) failed: %v", n+1, c.fg, c.bg, err)
			}
			nc.wmain.ColorOn(int16(n + 1))
			nc.wstatus.ColorOn(int16(n + 1))
			nc.whr.ColorOn(int16(n + 1))
		}
	}
	nc.wmain.Color(0)
	nc.wstatus.Color(0)
	nc.whr.Color(0)

	// Only StdScr is needed, I think.
	nc.wmain.Keypad(true)
	nc.whr.Keypad(true)
	nc.wstatus.Keypad(true)
	gc.StdScr().Keypad(true)

	nc.status = make(chan string, 100)
	nc.main = make(chan func(*gc.Window), 100)
	nc.redraw = make(chan bool, 100)
	nc.done = make(chan chan bool)
	nc.Input = make(chan gc.Key, 100)
	nc.whr.Print(strings.Repeat("-", w))
	go func() {
		// Output goroutine.
		for {
			select {
			case d := <-nc.done:
				d <- true
				return
			case s := <-nc.status:
				nc.wstatus.Clear()
				ColorPrint(nc.wstatus, "%s", Preformat(s))
				nc.wstatus.Refresh()
			case f := <-nc.main:
				f(nc.wmain)
				nc.wmain.Refresh()
			case <-nc.redraw:
				h, w := nc.root.MaxYX()
				nc.wmain.Resize(h-2, w)
				nc.whr.Resize(1, w)
				nc.whr.Move(h-2, 0)
				nc.wstatus.Resize(1, w)
				nc.wstatus.Move(h-1, 0)
				nc.wmain.Refresh()
				nc.wstatus.Refresh()
				nc.whr.Refresh()
			}
		}
	}()
	// TODO: instead of setting timeout to poll, select on the fd or something.
	nc.root.Timeout(100)
	go func() {
		// Input goroutine.
		for {
			ch := nc.root.GetChar()
			if ch != 0 {
				nc.Input <- ch
			}
			select {
			case d := <-nc.done:
				d <- true
				return
			default:
			}
		}
	}()
	nc.wstatus.Clear()
	nc.wmain.Clear()
	nc.wmain.Refresh()
	nc.wstatus.Refresh()
	nc.whr.Refresh()
	return nc, nil
}

// Stop shuts down ncurses and hands back the terminal.
func (nc *NCWrap) Stop() {
	dc := make(chan bool, 2)
	// Shut down input and output goroutine.
	nc.done <- dc
	nc.done <- dc
	<-dc
	<-dc
	gc.End()
}

// Status prints a message to the status line.
func (nc *NCWrap) Status(s string, args ...interface{}) {
	if nc == nil {
		// TODO: Instead of bailing out like this, make a fake
		// UI for testing.
		return
	}
	nc.status <- fmt.Sprintf(s, args...)
}

// Redraw orders a screen redrawing.
func (nc *NCWrap) Redraw() {
	nc.redraw <- true
}

// ApplyMain applies a function synchronously to the main window.
func (nc *NCWrap) ApplyMain(f func(*gc.Window)) {
	s := make(chan bool)
	nc.main <- func(w *gc.Window) {
		defer close(s)
		f(w)
	}
	<-s
}

// Apply runs the function synchronously in the UI goroutine.
func (nc *NCWrap) Apply(f func()) {
	nc.ApplyMain(func(*gc.Window) { f() })
}
