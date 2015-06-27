package main

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
	"strings"

	"github.com/ThomasHabets/cmdg/cmdglib"
	"github.com/ThomasHabets/cmdg/ncwrap"
	gc "github.com/rthornton128/goncurses"
	gmail "google.golang.org/api/gmail/v1"
)

// Return true if cmdg should quit.
func openThreadMain(ts []*gmail.Thread, state *messageListState) {
	nc.Status("Opening thread")
	scroll := 0
	currentMessage := 0
	for {
		nc.ApplyMain(func(w *gc.Window) {
			openThreadPrint(w, ts, state.current, currentMessage, state.marked[ts[state.current].Id], state.currentLabel, scroll)
		})
		key := <-nc.Input
		switch key {
		case '?':
			helpWin(`q                 Quit
^P                Previous thread
^N                Next thread
Left, <, u        Exit thread
p, k              Previous message
n, j              Next message
Right, >, Enter   Open message
f                 Forward last
r                 Reply to last
a                 Reply all to last
e                 Archive
l                 Add label
L                 Remove label
x                 Mark thread (TODO)
Space             Page down
Backspace         Page up
`)
			nc.ApplyMain(func(w *gc.Window) { w.Clear() })
		case 'q':
			state.quit = true
			return
		case gc.KEY_LEFT, '<', 'u':
			return
		case gc.KEY_RIGHT, '>', '\n', '\r':
			r := state.current
			state.current = currentMessage
			openMessageMain(ts[state.current].Messages, state)
			state.current = r
			if state.quit {
				return
			}
		case ctrlP:
			if state.current > 0 {
				state.current--
			}
		case ctrlN:
			if state.current < len(ts)-1 {
				state.current++
			}
		case 'p', 'k':
			if currentMessage > 0 {
				currentMessage--
			}
		case 'n', 'j':
			if currentMessage < len(ts[state.current].Messages)-1 {
				currentMessage++
			}
		default:
			nc.Status("unknown key: %v", gc.KeyString(key))
		}
	}
}

// openThreadPrint redraws the thread list.
// It's run in the UI goroutine.
func openThreadPrint(w *gc.Window, ts []*gmail.Thread, currentThread, currentMessage int, marked bool, currentLabel string, scroll int) {
	w.Clear()
	t := ts[currentThread]
	w.Move(0, 0)
	//height, width := w.MaxYX()
	tswidth := 7

	if len(t.Messages) == 0 {
		ncwrap.ColorPrint(w, "Thread: [bold]UNKNOWN[unbold] (??? messages)\n")
	} else {
		ncwrap.ColorPrint(w, "Thread: [bold]%s[unbold] (%d messages)\n", cmdglib.GetHeader(t.Messages[0], "Subject"), len(t.Messages))
	}
	for n, m := range t.Messages {
		prefix := "  "
		if n == currentMessage {
			prefix = "> "
		}
		ncwrap.ColorPrint(w, "%s[green]%*.*s - %s\n", prefix, tswidth, tswidth, cmdglib.TimeString(m), cmdglib.GetHeader(m, "From"))

		// Expand unread and the last email.
		if cmdglib.HasLabel(m.LabelIds, cmdglib.Unread) || n == len(t.Messages)-1 {
			bodyLines := breakLines(strings.Split(getBody(m), "\n"))
			body := strings.Join(bodyLines, "\n")
			ncwrap.ColorPrint(w, "%s\n", body)
		}
	}
}
