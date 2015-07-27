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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/ThomasHabets/cmdg/cmdglib"
	"github.com/ThomasHabets/cmdg/messagegetter"
	"github.com/ThomasHabets/cmdg/ncwrap"
	gc "github.com/rthornton128/goncurses"
	gmail "google.golang.org/api/gmail/v1"
)

const (
	refreshDuration = 30 * time.Second
	ctrlR           = 18
	ctrlP           = 16
	ctrlN           = 14

	draftListBatchSize = 100
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
// if 'free' is true, allow 'write-ins'. Else 'write-ins' become empty string.
func stringChoice(prompt string, ls []string, free bool) string {
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
		w.Print(fmt.Sprintf("\n %s> %s\n", prompt, s))
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
					if free {
						return s
					}
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

type messageListState struct {
	thread        bool                         // Thread or message view.
	quit          bool                         // All done.
	historyID     uint64                       // Last seen historyID.
	current       int                          // Index of current email/thread.
	showDetails   bool                         // Show snippets.
	currentLabel  string                       // Current label/folder.
	currentSearch string                       // Current search expression.
	msgs          []listEntry                  // Current messages.
	marked        map[string]bool              // Marked message/thread IDs.
	msgDo         chan func(*messageListState) // Do things in sync handler.
	msgsCh        chan []listEntry             // Full list of messages/threads, possibly only initial data.
	msgUpdateCh   chan listEntry               // Send back updated/full messages/threads.
}

func (m *messageListState) archive(id string) {
	if m.currentLabel == cmdglib.Inbox {
		delete(m.marked, id)
	}
	for n, msg := range m.msgs {
		if msg.msg.Id == id {
			m.msgs = append(m.msgs[:n], m.msgs[n+1:]...)
			return
		}
	}
}

// bgLoadMsgs loads messages asynchronously and sends that info back to the main thread via channels.
func bgLoadMsgs(msgDo chan<- func(*messageListState), msgsCh chan<- []listEntry, msgUpdateCh chan<- listEntry, thread bool, historyID uint64, label, search string) {
	log.Printf("Loading label %q, search %q", label, search)
	var l []listEntry
	var lch <-chan listEntry
	var errs []error

	// Get messages/threads.
	if thread {
		l, lch = listThreads(label, search, "", 100, historyID)
	} else {
		var newHistoryID uint64
		var lch2 <-chan listEntry
		c := make(chan listEntry)
		lch = c
		newHistoryID, l, lch2, errs = list(label, search, "", 100, historyID)
		go func() {
			goodHistoryID := true
			var nh uint64
			// Get the *lowest* history ID. That's the safest bet.
			for m := range lch2 {
				if nh == 0 || m.msg.HistoryId < nh {
					nh = m.msg.HistoryId
				}
				c <- m
			}
			if newHistoryID == 0 {
				newHistoryID = nh
				goodHistoryID = false
			}
			msgDo <- func(state *messageListState) {
				if goodHistoryID || state.historyID == 0 {
					state.historyID = newHistoryID
				}
			}
		}()
	}
	if len(errs) == 1 && errs[0] == errNoHistory {
		// Nothing changed, and that's fine.
		log.Printf("No changes since last reload.")
	} else if len(errs) != 0 {
		msgDo <- func(*messageListState) {
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

	// Get contacts.
	if c, err := getContacts(); err != nil {
		log.Printf("Getting contacts: %v", err)
	} else {
		msgDo <- func(state *messageListState) {
			contacts = c
		}
	}

	// Get labels.
	if c, err := getLabels(); err != nil {
		log.Printf("Getting labels: %v", err)
	} else {
		msgDo <- func(state *messageListState) {
			updateLabels(c)
		}
	}
}

// goLoadMsgs schedules a message reload.
func (m *messageListState) goLoadMsgs() {
	go bgLoadMsgs(m.msgDo, m.msgsCh, m.msgUpdateCh, m.thread, m.historyID, m.currentLabel, m.currentSearch)
}

func (m *messageListState) changeLabel(label, search string) {
	m.historyID = 0
	m.marked = make(map[string]bool)
	m.currentLabel = label
	m.currentSearch = search
	m.goLoadMsgs()
}

// getDrafts returns all the drafts, with full message content.
func getDrafts() ([]*gmail.Draft, error) {
	var page string
	mg := messagegetter.New(gmailService, email, profileAPI, backoff)

	var drafts []*gmail.Draft
	dmap := make(map[string]int)
	for {
		ts := time.Now()
		l, err := gmailService.Users.Drafts.List(email).MaxResults(draftListBatchSize).PageToken(page).Do()
		if err != nil {
			return nil, err
		}
		profileAPI("Users.Drafts.List", time.Since(ts))
		page = l.NextPageToken
		for _, d := range l.Drafts {
			mg.Add(d.Message.Id)
			dmap[d.Message.Id] = len(drafts)
			drafts = append(drafts, d)
		}
		if page == "" {
			break
		}
	}
	mg.Done()
	for m := range mg.Get() {
		drafts[dmap[m.Id]].Message = m
	}
	// TODO: Sort drafts.
	return drafts, nil
}

type keyChoice struct {
	key  gc.Key
	help string
}

func keyMenu(choices []keyChoice) gc.Key {
	maxY, maxX := winSize()
	height := len(choices) + 4
	width := 70
	x, y := maxX/2-width/2, maxY/2-height/2
	w, err := gc.NewWindow(height, width, y, x)
	if err != nil {
		log.Fatalf("Failed to create send dialog: %v", err)
	}
	defer w.Delete()
	log.Printf("send window: %d %d %d %d", height, width, y, x)

	w.Clear()
	w.Print("\n\n")
	for _, c := range choices {
		w.Printf("   %s - %s\n", gc.KeyString(c.key), c.help)
	}
	winBorder(w)
	for {
		w.Refresh()
		gc.Cursor(0)
		key := <-nc.Input
		for _, c := range choices {
			if key == c.key {
				return c.key
			}
			log.Printf("Invalid choice %v", key)
		}
	}
}

func continueDraft() {
	drafts, err := getDrafts()
	if err != nil {
		nc.Status("Getting drafts: %v", err)
	}
	var ss []string
	for n, d := range drafts {
		ss = append(ss, fmt.Sprintf("To:%s %s %d", cmdglib.GetHeader(d.Message, "To"), d.Message.Snippet, n))
	}
	dn := stringChoice("Draft", ss, false)
	re := regexp.MustCompile(` (\d+)$`)
	m := re.FindStringSubmatch(dn)
	if len(m) != 2 {
		nc.Status("Selecting draft failed!")
		return
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		nc.Status("Internal error selecting draft: %v", err)
		return
	}
	oldDraft := drafts[n]
	input := fmt.Sprintf("To: %s\nCc: %s\nBcc: %s\nSubject: %s\n\n%s",
		cmdglib.GetHeader(oldDraft.Message, "To"),
		cmdglib.GetHeader(oldDraft.Message, "Cc"),
		cmdglib.GetHeader(oldDraft.Message, "Bcc"),
		cmdglib.GetHeader(oldDraft.Message, "Subject"),
		getBody(oldDraft.Message),
	)
	newDraft, err := runEditor(input)
	if err != nil {
		nc.Status("Running editor: %v", err)
		return
	}
	choice := keyMenu([]keyChoice{
		// Don't use 's', since it could mean save or send.
		{'d', "Discard changes"},
		{'D', "Discard draft"},
		{'u', "Update draft"},
		{'S', "Send"},
	})
	switch choice {
	case 'd': // Discard changes.
		nc.Status("Discarded change to draft")
	case 'D': // Discard draft.
		nc.Status("TODO: Discard draft")
	case 'u': // Update draft.
		// TODO: Retry.
		st := time.Now()
		if _, err := gmailService.Users.Drafts.Update(email, oldDraft.Id, &gmail.Draft{
			Message: &gmail.Message{
				ThreadId: oldDraft.Message.ThreadId,
				Raw:      mimeEncode(newDraft),
			},
		}).Do(); err != nil {
			nc.Status("[red]Error updating draft %s: %v", oldDraft.Id, err)
			return
		}
		profileAPI("Users.Drafts.Update", time.Since(st))
		nc.Status("[green]Updated draft")
	case 'S': // Send.
		nc.Status("TODO: Send draft.")
	}
}

func compose() {
	to := stringChoice("To", contactAddresses(), true)
	nc.Status("Running editor")
	input := fmt.Sprintf("To: %s\nSubject: \n\n%s\n", to, getSignature())
	sendMessage, err := runEditor(input)
	if err != nil {
		helpWin(fmt.Sprintf("Running editor:\n%v", err))
		return
	}
	createSend("", sendMessage)
	nc.Status("Composed email")
}

// messageListInput handles input. It's run synchronously in the main thread.
func messageListInput(key gc.Key, state *messageListState) {
	// Messages that are both marked and in the current view.
	mm := markedMessages(state.msgs, state.marked)
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
C                 Continue draft
d                 Delete marked emails
e                 Archive marked emails
l                 Label marked emails
L                 Unlabel marked emails
s                 Search
1                 Go to inbox
0                 Re-read config.
`)
		nc.ApplyMain(func(w *gc.Window) { w.Clear() })
	case 'q':
		state.quit = true
	case '0':
		if err := reconnect(); err != nil {
			nc.Status("Failed to reconnect: %v", err)
		} else {
			nc.Status("Reconnected successfully")
		}
	case gc.KEY_UP, 'p', ctrlP, 'k':
		if state.current > 0 {
			state.current--
		}
	case gc.KEY_DOWN, 'n', ctrlN, 'j':
		if state.current < len(state.msgs)-1 {
			state.current++
		}
	case gc.KEY_TAB:
		state.showDetails = !state.showDetails
	case 'r', ctrlR:
		state.goLoadMsgs()
	case 'x':
		if len(state.msgs) > 0 {
			id := state.msgs[state.current].ID()
			if state.marked[id] {
				delete(state.marked, id)
			} else {
				state.marked[id] = true
			}
		}
	case gc.KEY_RIGHT, '\n', '\r', '>':
		if state.thread {
			var ms []*gmail.Thread
			for _, m := range state.msgs {
				ms = append(ms, m.thread)
			}
			openThreadMain(ms, state)
			if state.quit {
				return
			}
		} else {
			var ms []*gmail.Message
			for _, m := range state.msgs {
				ms = append(ms, m.msg)
			}
			openMessageMain(ms, state)
			if state.quit {
				return
			}
		}
		state.goLoadMsgs()
	case '1':
		state.changeLabel(cmdglib.Inbox, "")
	case 'g':
		newLabel := stringChoice("Go to label", sortedLabels(), false)
		if newLabel != "" {
			newLabel = labels[newLabel]
			log.Printf("Going to label %q (%q)", newLabel, labelIDs[newLabel])
			state.changeLabel(newLabel, "")
		}
	case 'c': // Compose.
		compose()
		// We could be in sent folders or a search that sees this message.
		state.goLoadMsgs()
	case 'C':
		continueDraft()
	case 'd':
		if len(mm) == 0 {
			nc.Status("No messages marked")
			break
		}
		allFine := true
		for _, m := range mm {
			st := time.Now()
			if _, err := gmailService.Users.Messages.Trash(email, m.ID()).Do(); err == nil {
				state.goLoadMsgs()
				log.Printf("Users.Messages.Trash: %v", time.Since(st))
				delete(state.marked, m.ID())
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
		var errCount int32
		var wg sync.WaitGroup
		for _, m := range mm {
			m := m
			wg.Add(1)
			go func() {
				defer wg.Done()
				st := time.Now()
				if _, err := gmailService.Users.Messages.Modify(email, m.ID(), &gmail.ModifyMessageRequest{
					RemoveLabelIds: []string{cmdglib.Inbox},
				}).Do(); err == nil {
					state.archive(m.ID())
					log.Printf("Users.Messages.Archive: %v", time.Since(st))
				} else {
					nc.Status("[red]Failed to archive message %s: %v", m, err)
					atomic.AddInt32(&errCount, 1)
				}
			}()
		}
		wg.Wait()
		state.goLoadMsgs()
		if errCount > 0 {
			nc.Status("[green]Archived messages")
		}

	case 'l': // Add label.
		if len(mm) == 0 {
			nc.Status("No messages marked")
			break
		}
		newLabel := stringChoice("Add label", sortedLabels(), false)
		if newLabel != "" {
			id := labels[newLabel]
			var errCount int32
			var wg sync.WaitGroup
			for _, m := range mm {
				m := m
				wg.Add(1)
				go func() {
					defer wg.Done()
					st := time.Now()
					if _, err := gmailService.Users.Messages.Modify(email, m.ID(), &gmail.ModifyMessageRequest{
						AddLabelIds: []string{id},
					}).Do(); err == nil {
						state.goLoadMsgs()
						log.Printf("Users.Messages.Label: %v", time.Since(st))
					} else {
						log.Printf("Users.Messages.Label error: %v", err)
						nc.Status("[red]Failed to label message %s: %v", m, err)
						atomic.AddInt32(&errCount, 1)
					}
				}()
			}
			wg.Wait()
			if errCount > 0 {
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
		newLabel := stringChoice("Remove label", ls, false)
		if newLabel != "" {
			state.goLoadMsgs()
			id := labels[newLabel]
			allFine := true
			for _, m := range mm {
				st := time.Now()
				if _, err := gmailService.Users.Messages.Modify(email, m.ID(), &gmail.ModifyMessageRequest{
					RemoveLabelIds: []string{id},
				}).Do(); err == nil {
					state.goLoadMsgs()
					log.Printf("Users.Messages.Unlabel: %v", time.Since(st))
					if state.currentLabel == newLabel {
						delete(state.marked, m.ID())
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
			state.changeLabel("", cs)
		}
	default:
		nc.Status("Unknown key %v (%v)", key, gc.KeyString(key))
	}
}

func messageListMain(thread bool) {
	nc.ApplyMain(func(w *gc.Window) {
		w.Clear()
		w.Print("Loading...")
	})
	state := messageListState{
		thread:      thread,
		msgDo:       make(chan func(*messageListState)),
		msgsCh:      make(chan []listEntry),
		msgUpdateCh: make(chan listEntry),
	}
	state.changeLabel(cmdglib.Inbox, "")

	refreshTicker := time.NewTicker(refreshDuration)
	defer refreshTicker.Stop()
	for !state.quit {
		select {
		case <-refreshTicker.C:
			state.goLoadMsgs()
		case key := <-nc.Input:
			messageListInput(key, &state)
		case newMsgs := <-state.msgsCh:
			old := make(map[string]listEntry)
			for _, m := range state.msgs {
				old[m.ID()] = m
			}
			state.msgs = newMsgs
			for n := range state.msgs {
				if m, found := old[state.msgs[n].ID()]; found {
					state.msgs[n] = m
				}
			}
			if state.current >= len(state.msgs) {
				state.current = len(state.msgs) - 1
			}
			if state.current < 0 {
				state.current = 0
			}
		case m := <-state.msgUpdateCh:
			for n := range state.msgs {
				if state.msgs[n].ID() == m.ID() {
					state.msgs[n] = m
				}
			}
		case f := <-state.msgDo:
			f(&state)
		}
		nc.ApplyMain(func(w *gc.Window) {
			messageListPrint(w, state.msgs, state.marked, state.current, state.showDetails, state.currentLabel, state.currentSearch)
		})
	}
}

// This runs in the UI goroutine.
func messageListPrint(w *gc.Window, msgs []listEntry, marked map[string]bool, current int, showDetails bool, currentLabel, currentSearch string) {
	w.Move(0, 0)
	maxY, maxX := w.MaxYX()

	fromMax := 20
	tsWidth := 6
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

		// Selector, mark, unread, starred.
		prefix := []string{" ", " ", " ", " "}

		if n == current {
			prefix[0] = "*"
			style = "[reverse]" + style
		}

		if marked[m.ID()] {
			prefix[1] = "X"
		}

		if cmdglib.HasLabel(m.LabelIds(), cmdglib.Unread) {
			prefix[2] = ">"
		}

		if cmdglib.HasLabel(m.LabelIds(), cmdglib.Starred) {
			prefix[3] = "*"
		}

		s = strings.Join(prefix, "") + s

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
