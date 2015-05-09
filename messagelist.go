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
	"fmt"
	"io/ioutil"
	"log"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/ThomasHabets/cmdg/cmdglib"
	"github.com/ThomasHabets/cmdg/ncwrap"
	gc "github.com/rthornton128/goncurses"
	gmail "google.golang.org/api/gmail/v1"
)

func getSignature() string {
	b, err := ioutil.ReadFile(*signature)
	if err != nil {
		return ""
	}
	return string(b)
}

func winSize() (int, int) {
	var maxX, maxY int
	a := make(chan int, 2)
	b := make(chan int, 2)
	nc.ApplyMain(func(w *gc.Window) {
		y, x := gc.StdScr().MaxYX()
		a <- x
		b <- y
	})
	maxX = <-a
	maxY = <-b
	return maxY, maxX
}

func sortedLabels() []string {
	ls := []string{}
	for l := range labels {
		ls = append(ls, l)
	}
	sort.Sort(sortLabels(ls))
	return ls
}

func winBorder(w *gc.Window) {
	if err := w.Border(gc.ACS_VLINE, gc.ACS_VLINE, gc.ACS_HLINE, gc.ACS_HLINE, gc.ACS_ULCORNER, gc.ACS_URCORNER, gc.ACS_LLCORNER, gc.ACS_LRCORNER); err != nil {
		log.Fatalf("Failed to add border: %v", err)
	}
}

// stringChoice interactively asks the user for a label or email or something, and returns it.
func stringChoice(prompt string, ls []string) string {
	maxY, maxX := winSize()

	w, err := gc.NewWindow(maxY-5, maxX-4, 2, 2)
	if err != nil {
		log.Fatalf("Creating stringChoice window: %v", err)
	}
	defer w.Delete()

	s := ""
	cur := -1

	for {
		w.Clear()
		w.Print(fmt.Sprintf("\n %s %s\n", prompt, s))
		seenLabels := 0
		curLabel := ""
		for _, l := range ls {
			if strings.Contains(strings.ToLower(l), strings.ToLower(s)) {
				prefix := " "
				if seenLabels == cur {
					prefix = ">[bold]"
					curLabel = l
				}
				ncwrap.ColorPrint(w, "  %s%s[unbold]\n", ncwrap.Preformat(prefix), l)
				seenLabels++
			}
			if y, _ := w.MaxYX(); seenLabels > y-2 {
				break
			}
		}
		winBorder(w)
		w.Refresh()
		select {
		case key := <-nc.Input:
			switch key {
			case '\b', gc.KEY_BACKSPACE, 127:
				if len(s) > 0 {
					// TODO: don't break mid-rune.
					s = s[:len(s)-1]
				}
				if len(s) == 0 {
					cur = -1
				}
			case gc.KEY_DOWN, 14: // CtrlN
				if cur < seenLabels-1 {
					cur++
				}
			case gc.KEY_UP, 16: // CtrlP
				if cur > 0 {
					cur--
				}
			case '\n', '\r':
				if seenLabels == 0 {
					return ""
				}
				return curLabel
			default:
				cur = 0
				if unicode.IsPrint(rune(key)) {
					s = fmt.Sprintf("%s%c", s, key)
				} else {
					s = fmt.Sprintf("%s<%d>", s, key)
				}
			}
		}
	}
}

func getText(prompt string) string {
	maxY, maxX := winSize()
	height := 7
	width := maxX - 4
	x, y := maxX/2-width/2, maxY/2-height/2
	w, err := gc.NewWindow(height, width, y, x)
	if err != nil {
		log.Fatalf("Creating text window: %v", err)
	}
	defer w.Delete()

	s := ""
	for {
		w.Clear()
		w.Print(pad(fmt.Sprintf("%s %s\n", prompt, s)))
		winBorder(w)
		w.Refresh()
		select {
		case key := <-nc.Input:
			switch key {
			case '\b', gc.KEY_BACKSPACE, 127:
				if len(s) > 0 {
					// TODO: don't break mid-rune.
					s = s[:len(s)-1]
				}
			case '\n', '\r':
				return s
			default:
				if unicode.IsPrint(rune(key)) {
					s = fmt.Sprintf("%s%c", s, key)
				}
			}
		}
	}
}

func pad(s string) string {
	var nl []string
	for _, l := range strings.Split(s, "\n") {
		nl = append(nl, "  "+l)
	}
	return "\n" + strings.Join(nl, "\n")
}

func helpWin(s string) {
	maxY, maxX := winSize()
	height := maxY - 4
	width := maxX - 4
	x, y := maxX/2-width/2, maxY/2-height/2

	w, err := gc.NewWindow(height, width, y, x)
	if err != nil {
		log.Fatalf("Creating text window: %v", err)
	}
	defer w.Delete()
	w.Clear()
	ncwrap.ColorPrint(w, "%s", ncwrap.Preformat(pad(s)))
	winBorder(w)
	w.Refresh()
	<-nc.Input
}

// markedMessages returns the messages/threads that are both in the current view, and marked.
func markedMessages(msgs []listEntry, marked map[string]bool) []listEntry {
	var ret []listEntry
	for _, m := range msgs {
		if marked[m.ID()] {
			ret = append(ret, m)
		}
	}
	return ret
}

type listEntry struct {
	msg    *gmail.Message
	thread *gmail.Thread
}

func (e *listEntry) ID() string {
	if e.msg != nil {
		return e.msg.Id
	}
	return e.thread.Id
}

func (e *listEntry) Time() string {
	if e.msg != nil {
		return cmdglib.TimeString(e.msg)
	}
	if len(e.thread.Messages) == 0 {
		return "Loading"
	}
	return cmdglib.TimeString(e.thread.Messages[len(e.thread.Messages)-1])
}

func (e *listEntry) From() string {
	if e.msg != nil {
		return cmdglib.FromString(e.msg)
	}
	if len(e.thread.Messages) == 0 {
		return "Loading"
	}
	// TODO: better fromstring.
	return cmdglib.FromString(e.thread.Messages[0])
}

func (e *listEntry) Subject() string {
	if e.msg != nil {
		return cmdglib.GetHeader(e.msg, "Subject")
	}
	if len(e.thread.Messages) == 0 {
		return "Loading"
	}
	return cmdglib.GetHeader(e.thread.Messages[0], "Subject")
}

func (e *listEntry) Snippet() string {
	if e.msg != nil {
		return e.msg.Snippet
	}
	if len(e.thread.Messages) == 0 {
		return "Loading"
	}
	return e.thread.Snippet
}

func (e *listEntry) LabelIds() []string {
	if e.msg != nil {
		return e.msg.LabelIds
	}
	var l []string
	for _, m := range e.thread.Messages {
		l = append(l, m.LabelIds...)
	}
	return l
}

func messageListMain(thread bool) {
	currentLabel := cmdglib.Inbox // Label ID.
	currentSearch := ""
	nc.ApplyMain(func(w *gc.Window) {
		w.Clear()
		w.Print("Loading...")
	})
	msgsCh := make(chan []listEntry)
	msgUpdateCh := make(chan listEntry)
	msgDo := make(chan func())

	// Synchronous function to list messages / threads.
	// To be called in a goroutine.
	loadMsgs := func(label, search string) {
		log.Printf("Loading %s", label)
		var l []listEntry
		var lch <-chan listEntry
		var errs []error
		if thread {
			l, lch = listThreads(label, search)
		} else {
			l, lch, errs = list(label, search, "", 100)
		}
		if len(errs) != 0 {
			msgDo <- func() {
				e := []string{}
				for _, ee := range errs {
					e = append(e, ee.Error())
				}
				helpWin(fmt.Sprintf("[red]ERROR listing:\n%v", strings.Join(e, "\n")))
				nc.ApplyMain(func(w *gc.Window) { w.Clear() })
			}
		} else {
			msgsCh <- l
			for m := range lch {
				msgUpdateCh <- m
			}
		}
		if c, err := getContacts(); err != nil {
			log.Printf("Getting contacts: %v", err)
		} else {
			msgDo <- func() {
				contacts = c
			}
		}
		if c, err := getLabels(); err != nil {
			log.Printf("Getting labels: %v", err)
		} else {
			msgDo <- func() {
				updateLabels(c)
			}
		}
	}
	go loadMsgs(currentLabel, currentSearch)
	marked := make(map[string]bool)
	showDetails := false
	current := 0

	var msgs []listEntry
	for {
		// Instead of reloading when there's a change, the state should be updated locally.
		reloadTODO := false

		// Messages that are both marked and in the current view.
		mm := markedMessages(msgs, marked)

		select {
		case key := <-nc.Input:
			switch key {
			case '?':
				helpWin(`q                 Quit
Up, p, ^P, k      Previous
Down, n, ^N, j    Next
r, ^R             Reload
x                 Mark/unmark
Tab               Show/hide snippets
Right, Enter, >   Open message
g                 Go to label
c                 Compose
d                 Delete marked emails
e                 Archive marked emails
l                 Label marked emails
L                 Unlabel marked emails
s                 Search
1                 Go to cmdglib.Inbox
`)
				nc.ApplyMain(func(w *gc.Window) { w.Clear() })
			case 'q':
				return
			case gc.KEY_UP, 'p', 16, 'k':
				if current > 0 {
					current--
				}
			case gc.KEY_DOWN, 'n', 14, 'j':
				if current < len(msgs)-1 {
					current++
				}
			case gc.KEY_TAB:
				showDetails = !showDetails
			case 'r', 18: // CtrlR
				go loadMsgs(currentLabel, currentSearch)
			case 'x':
				if len(msgs) > 0 {
					if marked[msgs[current].ID()] {
						delete(marked, msgs[current].ID())
					} else {
						marked[msgs[current].ID()] = true
					}
				}
			case gc.KEY_RIGHT, '\n', '\r', '>':
				if thread {
					var ms []*gmail.Thread
					for _, m := range msgs {
						ms = append(ms, m.thread)
					}
					if openThreadMain(ms, current, marked, currentLabel) {
						return
					}
				} else {
					var ms []*gmail.Message
					for _, m := range msgs {
						ms = append(ms, m.msg)
					}
					if openMessageMain(ms, current, marked, currentLabel) {
						return
					}
				}
				reloadTODO = true
			case '1':
				marked = make(map[string]bool)
				currentLabel = cmdglib.Inbox
				currentSearch = ""
				go loadMsgs(currentLabel, currentSearch)
			case 'g':
				newLabel := stringChoice("Go to label>", sortedLabels())
				if newLabel != "" {
					newLabel = labels[newLabel]
					log.Printf("Going to label %q (%q)", newLabel, labelIDs[newLabel])
					marked = make(map[string]bool)
					currentLabel = newLabel
					currentSearch = ""
					go loadMsgs(currentLabel, currentSearch)
				}
			case 'c': // Compose.
				to := stringChoice("To: ", contactAddresses())
				nc.Status("Running editor")
				input := fmt.Sprintf("To: %s\nSubject: \n\n%s\n", to, getSignature())
				sendMessage, err := runEditor(input)
				if err != nil {
					nc.Status("Running editor: %v", err)
				}
				createSend("", sendMessage)
				nc.Status("Sent email")
				// We could be in sent folders or a search that sees this message.
				reloadTODO = true
			case 'd':
				if len(mm) == 0 {
					nc.Status("No messages marked")
					break
				}
				allFine := true
				for _, m := range mm {
					st := time.Now()
					if _, err := gmailService.Users.Messages.Trash(email, m.ID()).Do(); err == nil {
						reloadTODO = true
						log.Printf("Users.Messages.Trash: %v", time.Since(st))
						delete(marked, m.ID())
					} else {
						nc.Status("[red]Failed to trash message %s: %v", m, err)
						allFine = false
					}
				}
				if allFine {
					nc.Status("[green]Trashed messages")
				}

			case 'e': // Archive.
				if len(mm) == 0 {
					nc.Status("No messages marked")
					break
				}
				allFine := true
				for _, m := range mm {
					st := time.Now()
					if _, err := gmailService.Users.Messages.Modify(email, m.ID(), &gmail.ModifyMessageRequest{
						RemoveLabelIds: []string{cmdglib.Inbox},
					}).Do(); err == nil {
						reloadTODO = true
						log.Printf("Users.Messages.Archive: %v", time.Since(st))
						if currentLabel == cmdglib.Inbox {
							delete(marked, m.ID())
						}
					} else {
						nc.Status("[red]Failed to archive message %s: %v", m, err)
						allFine = false
					}
				}
				if allFine {
					nc.Status("[green]Archived messages")
				}

			case 'l': // Add label.
				if len(mm) == 0 {
					nc.Status("No messages marked")
					break
				}
				newLabel := stringChoice("Add label>", sortedLabels())
				if newLabel != "" {
					id := labels[newLabel]
					allFine := true
					for _, m := range mm {
						st := time.Now()
						if _, err := gmailService.Users.Messages.Modify(email, m.ID(), &gmail.ModifyMessageRequest{
							AddLabelIds: []string{id},
						}).Do(); err == nil {
							reloadTODO = true
							log.Printf("Users.Messages.Label: %v", time.Since(st))
						} else {
							nc.Status("[red]Failed to label message %s: %v", m, err)
							allFine = false
						}
					}
					if allFine {
						nc.Status("[green]Labelled messages")
					}
				}

			case 'L': // Remove label.
				if len(mm) == 0 {
					nc.Status("No messages marked")
					break
				}

				// Labels to ask for.
				ls := []string{}
			nextLabel:
				for l, lid := range labels {
					for _, m := range mm {
						for _, hl := range m.LabelIds() {
							if lid == hl {
								ls = append(ls, l)
								continue nextLabel
							}
						}
					}
				}
				sort.Sort(sortLabels(ls))

				// Ask for labels.
				newLabel := stringChoice("Remove label>", ls)
				if newLabel != "" {
					reloadTODO = true
					id := labels[newLabel]
					allFine := true
					for _, m := range mm {
						st := time.Now()
						if _, err := gmailService.Users.Messages.Modify(email, m.ID(), &gmail.ModifyMessageRequest{
							RemoveLabelIds: []string{id},
						}).Do(); err == nil {
							reloadTODO = true
							log.Printf("Users.Messages.Unlabel: %v", time.Since(st))
							if currentLabel == newLabel {
								delete(marked, m.ID())
							}
						} else {
							nc.Status("[red]Failed to unlabel message %s: %v", m, err)
							allFine = false
						}
					}
					if allFine {
						nc.Status("[green]Unlabel messages")
					}
				}

			case 's':
				cs := getText("Search: ")
				if cs != "" {
					currentLabel = ""
					currentSearch = cs
					marked = make(map[string]bool)
					go loadMsgs(currentLabel, currentSearch)
				}
			default:
				nc.Status("Unknown key %v (%v)", key, gc.KeyString(key))
				continue
			}
		case newMsgs := <-msgsCh:
			old := make(map[string]listEntry)
			for _, m := range msgs {
				old[m.ID()] = m
			}
			msgs = newMsgs
			for n := range msgs {
				if m, found := old[msgs[n].ID()]; found {
					msgs[n] = m
				}
			}
			if current >= len(msgs) {
				current = len(msgs) - 1
			}
			if current < 0 {
				current = 0
			}
		case m := <-msgUpdateCh:
			for n := range msgs {
				if msgs[n].ID() == m.ID() {
					msgs[n] = m
				}
			}
		case f := <-msgDo:
			f()
		}
		if reloadTODO {
			go loadMsgs(currentLabel, currentSearch)
		}
		nc.ApplyMain(func(w *gc.Window) {
			messageListPrint(w, msgs, marked, current, showDetails, currentLabel, currentSearch)
		})
	}
}

// This runs in the UI goroutine.
func messageListPrint(w *gc.Window, msgs []listEntry, marked map[string]bool, current int, showDetails bool, currentLabel, currentSearch string) {
	w.Move(0, 0)
	maxY, maxX := w.MaxYX()

	fromMax := 20
	tsWidth := 7
	if len(msgs) == 0 {
		ncwrap.ColorPrint(w, "<empty for label %q, search query %q>", currentLabel, currentSearch)
	}
	for n, m := range msgs {
		if n >= maxY {
			break
		}
		style := ""
		if cmdglib.HasLabel(m.LabelIds(), cmdglib.Unread) {
			style = "[bold]"
		}
		s := fmt.Sprintf("%*.*s | %*.*s | %s",
			tsWidth, tsWidth, m.Time(),
			fromMax, fromMax, m.From(),
			m.Subject())
		if marked[m.ID()] {
			s = "X" + s
		} else if cmdglib.HasLabel(m.LabelIds(), cmdglib.Unread) {
			s = ">" + s
		} else {
			s = " " + s
		}
		if n == current {
			s = "*" + s
			style = "[reverse]" + style
		} else {
			s = " " + s
		}
		// TODO: #runes, not #bytes.
		if len(s) > maxX-4 {
			s = s[:maxX-4]
		}
		s = fmt.Sprintf("%-*.*s", maxX-10, maxX-10, s)
		ncwrap.ColorPrint(w, "%s%s\n", ncwrap.Preformat(style), s)
		if n == current && showDetails {
			//maxX, _ := messagesView.Size()
			maxX := 80
			maxX -= 10
			s := m.Snippet()
			for len(s) > 0 {
				n := maxX
				// TODO: don't break mid-rune.
				if n >= len(s) {
					n = len(s)
				}
				ncwrap.ColorPrint(w, "    %s\n", strings.Trim(s[:n], spaces))
				s = s[n:]
			}
		}
	}
	for i := len(msgs); i < maxY; i++ {
		w.Printf("\n")
	}
}
