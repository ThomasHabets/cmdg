package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/googleapi"

	"github.com/ThomasHabets/cmdg/pkg/cmdg"
	"github.com/ThomasHabets/cmdg/pkg/dialog"
	"github.com/ThomasHabets/cmdg/pkg/display"
	"github.com/ThomasHabets/cmdg/pkg/input"
)

const (
	scrollLimit = 5

	messageListViewHelp = `?, F1              — Help
enter              — Open message
space, x           — Mark message and advance
X                  — Mark message and step up
e                  — Archive marked messages
d                  — Move marked messages to trash
I                  … Mark marked mails as read
l                  — Label marked messages
L                  — Unlabel marked messages
*                  — Toggle starred on highlighted message
c                  — Compose new message
C                  — Continue message from draft
N, n, ^N, j, Down  — Next message
P, p, ^P, k, Up    — Previous message
r, ^R              — Reload current view
g                  — Go to label
1                  — Go to inbox
U                  … Mark marked mails as unread
s, ^s              — Search
q                  — Quit
^L                 — Refresh screen

Press [enter] to exit
`
)

var (
	messageListReloadTime          = time.Minute
	messageListReloadTimeout       = 40 * time.Second
	messageListHistoryCheckTime    = 10 * time.Second
	messageListHistoryCheckTimeout = 10 * time.Second
)

type historyUpdate struct {
	historyID cmdg.HistoryID
	history   []*gmail.History
}

type concurrency struct {
	lock chan struct{}
}

func newConcurrency(i int) *concurrency {
	return &concurrency{
		lock: make(chan struct{}, 1),
	}
}
func (c *concurrency) Take() bool {
	select {
	case c.lock <- struct{}{}:
		return true
	default:
		return false
	}
}
func (c *concurrency) Done() {
	<-c.lock
}

// MessageView is the state for a message view.
type MessageView struct {
	// Static state.
	label string
	query string

	// Communicate with main thread.
	keys            *input.Input
	errors          chan error
	pageCh          chan *cmdg.Page
	messageCh       chan *cmdg.Message
	historyUpdateCh chan historyUpdate
	removeMessage   chan string

	// Only for use by main thread.
	messages  []*cmdg.Message
	pos       int
	historyID cmdg.HistoryID
}

// NewMessageView creates a new message view.
func NewMessageView(ctx context.Context, label, q string, in *input.Input) *MessageView {
	v := &MessageView{
		label:           label,
		errors:          make(chan error, 20),
		pageCh:          make(chan *cmdg.Page),
		historyUpdateCh: make(chan historyUpdate, 20),
		messageCh:       make(chan *cmdg.Message),
		keys:            in,
		query:           q,
	}
	go v.fetchPage(ctx, "")
	return v
}

// returns:
// * true if doing anything. If this is 'false' then don't use other two returns.
// * new list of messages
// * an offset of how much pos should go back by after removal
func (mv *MessageView) applyMarked(ctx context.Context, name string, op func(context.Context, []string) error, marked map[string]bool) (bool, []*cmdg.Message, int) {
	ids, nm, ofs := filterMarked(mv.messages, marked, mv.pos)
	if len(ids) == 0 {
		log.Infof("No marked messages to do do operation %q on", name)
		return false, nil, 0
	}
	go func() {
		st := time.Now()
		if err := op(ctx, ids); err != nil {
			mv.errors <- errors.Wrapf(err, "batch operation %q failed", name)
		}
		log.Infof("Batch operation %q on %d messages: %v", name, len(ids), time.Since(st))
	}()
	log.Infof("Batch operation %q on %d messages (in background)", name, len(ids))
	return true, nm, ofs
}

func (mv *MessageView) fetchPage(ctx context.Context, token string) {
	ctx, cancel := context.WithTimeout(ctx, messageListReloadTimeout)
	if token == "" {
		// Only update history on first page.
		hid, err := conn.HistoryID(ctx)
		if err != nil {
			log.Errorf("Failed to get history ID: %v", err)
		} else {
			log.Infof("Initing history ID to %d", hid)
			mv.historyUpdateCh <- historyUpdate{
				historyID: hid,
			}
		}
	}

	log.Infof("Listing messages on label %q query %q with token %q…", mv.label, mv.query, token)
	st := time.Now()
	page, err := conn.ListMessages(ctx, mv.label, mv.query, token)
	if err != nil {
		mv.errors <- err
		cancel()
		return
	}
	log.Infof("Listing messages took %v", time.Since(st))
	go func() {
		defer cancel()
		if err := page.PreloadSubjects(ctx); err != nil {
			mv.errors <- err
			return
		}
	}()
	mv.pageCh <- page
}

// MessageViewOp is an operation to perform as the message closes.
type MessageViewOp struct {
	fun         func(*MessageView)
	quit        bool
	nextMessage bool
	prevMessage bool

	next *MessageViewOp
}

// Do does the op.
func (op *MessageViewOp) Do(view *MessageView) {
	if op == nil {
		return
	}
	if op.fun != nil {
		op.fun(view)
	}
	if op.next != nil {
		op.next.Do(view)
	}
}

// IsQuit returns if op is `quit`.
func (op *MessageViewOp) IsQuit(view *MessageView) bool {
	if op == nil {
		return false
	}
	if op.quit {
		return true
	}
	return op.next.IsQuit(view)
}

// IsNext returns if op is `next`.
func (op *MessageViewOp) IsNext(view *MessageView) bool {
	if op == nil {
		return false
	}
	if op.nextMessage {
		return true
	}
	return op.next.IsNext(view)
}

// IsPrev returns if op is `prev`
func (op *MessageViewOp) IsPrev(view *MessageView) bool {
	if op == nil {
		return false
	}
	if op.prevMessage {
		return true
	}
	return op.next.IsPrev(view)
}

// OpRemoveCurrent creates an op to remove current message from a list.
func OpRemoveCurrent(next *MessageViewOp) *MessageViewOp {
	return &MessageViewOp{
		fun: func(view *MessageView) {
			// TODO
			view.messages = append(view.messages[:view.pos], view.messages[view.pos+1:]...)
			if view.pos >= len(view.messages) && view.pos != 0 {
				view.pos--
			}
		},
		next: next,
	}
}

// OpQuit creates an op to quit.
func OpQuit() *MessageViewOp {
	return &MessageViewOp{
		quit: true,
	}
}

// OpPrev creates an op to go to prev message.
func OpPrev() *MessageViewOp {
	return &MessageViewOp{
		prevMessage: true,
	}
}

// OpNext creates an op to go to the next message.
func OpNext() *MessageViewOp {
	return &MessageViewOp{
		nextMessage: true,
	}
}

// filterMarked takes:
// * slice of messages
// * a set of marked message IDs
// * current position
// And returns the state if marked messages are removed
// * ids of the messages removed
// * slice of messages remaining after removal
// * an offset of how much pos should go back by after removal
//
// The offset is returned as an offset because it's used both for
// setting the new position and for adjusting current scroll position.
func filterMarked(msgs []*cmdg.Message, marked map[string]bool, pos int) ([]string, []*cmdg.Message, int) {
	var ids []string
	ms := []*cmdg.Message{}
	ofs := 0
	for n, msg := range msgs {
		if marked[msg.ID] {
			ids = append(ids, msg.ID)
			if n < pos {
				ofs++
			}
		} else {
			ms = append(ms, msg)
		}
	}
	return ids, ms, ofs
}

func filterMessage(msgs []*cmdg.Message, id string, pos int) ([]*cmdg.Message, int) {
	var ret []*cmdg.Message

	for n, msg := range msgs {
		if msg.ID == id {
			if n < pos {
				pos--
			}
			continue
		}
		ret = append(ret, msg)
	}
	return ret, pos
}

func (mv *MessageView) historyCheck(ctx context.Context) error {
	hists, hid, err := conn.History(ctx, mv.historyID, mv.label)
	if err != nil {
		return errors.Wrapf(err, "getting history since %d", mv.historyID)
	}
	if len(hists) == 0 {
		log.Infof("No history since last check")
		return nil
	}

	// The GMail API returns false positives if a new message
	// affects *any thread* that is in the current label, even if
	// the message itself doesn't have the label.
	// This was closed by Google as working as intended. :-(
	//
	// https://issuetracker.google.com/issues/137671760
	//
	// So we'll need to get the messages' list of labels before
	// sending them on to the list view.
	var wg sync.WaitGroup
	for hi := range hists {
		hi := hi
		for mi := range hists[hi].MessagesAdded {
			mi := mi
			if len(hists[hi].MessagesAdded[mi].Message.LabelIds) > 0 {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				m := cmdg.NewMessage(conn, hists[hi].MessagesAdded[mi].Message.Id)
				// Load labels.
				ls, err := m.GetLabels(ctx, true)
				if err != nil {
					log.Errorf("Failed to load labels for history entry: %v", err)
					return
				}
				for _, l := range ls {
					hists[hi].MessagesAdded[mi].Message.LabelIds = append(hists[hi].MessagesAdded[mi].Message.LabelIds, l.ID)
				}
			}()
		}
	}
	wg.Wait()

	mv.historyUpdateCh <- historyUpdate{
		historyID: hid,
		history:   hists,
	}
	return nil
}

// Run runs the messagelist view.
func (mv *MessageView) Run(ctx context.Context) error {
	log.Infof("Running MessageView")
	// TODO: defer a sync.WaitGroup.Wait() waiting on all goroutines spawned.
	theresMore := true
	var contentHeight int
	var pages []*cmdg.Page
	messagePos := map[string]int{}
	marked := map[string]bool{}
	var scroll int
	var screen *display.Screen

	initScreen := func() error {
		var err error
		screen, err = display.NewScreen()
		if err != nil {
			return err
		}
		contentHeight = screen.Height - 2
		scroll = 0 // TODO: only scroll back if we need to.
		return nil
	}
	if err := initScreen(); err != nil {
		return err
	}
	defer func() {
		screen.Clear()
		screen.Draw()
	}()

	mkMessagePos := func() {
		messagePos = map[string]int{}
		for n, m := range mv.messages {
			messagePos[m.ID] = n
		}
	}
	empty := func() {
		screen.Printf(0, 0, "Loading…")
		screen.Draw()
		pages = nil
		mv.messages = nil
		mkMessagePos()
		mv.pos = 0
		scroll = 0
	}
	empty()

	drawMessage := func(cur int) error {
		s := "Loading…"
		if cur >= len(mv.messages) {
			return fmt.Errorf("trying to draw message %d with len %d", cur, len(mv.messages))
		}
		curmsg := mv.messages[cur]

		prefix := " "
		reset := display.Reset
		if cur == mv.pos {
			reset = display.Reverse
			prefix = "*"
		}

		if curmsg.HasData(cmdg.LevelMetadata) {
			subj, err := curmsg.GetSubject(ctx)
			if errors.Cause(err) == cmdg.ErrMissing || subj == "" {
				subj = "(No subject)"
			} else if err != nil {
				return err
			}
			tm, err := curmsg.GetTimeFmt(ctx)
			if err != nil {
				log.Infof("Failed to parse mail date: %v", err)
				tm = "???"
			}
			from, err := curmsg.GetFrom(ctx)
			if err != nil {
				return err
			}
			colors, fullColors, err := curmsg.GetLabelColors(ctx, mv.label)
			if err != nil {
				return err
			}
			if len(colors) > 0 {
				colors = " | " + colors
				fullColors = " | " + fullColors
			}
			from = display.FixedWidth(from, 20)
			s = fmt.Sprintf("%[1]*.[1]*[2]s | %[3]s | %[4]s",
				6, tm,
				from, subj)
			if display.StringWidth(s)+display.StringWidth(fullColors) < screen.Width {
				s += fullColors
			} else {
				s += colors
			}
			s += reset
		} else {
			go func(cur int) {
				if err := curmsg.Preload(ctx, cmdg.LevelMetadata); err != nil {
					log.Warningf("Failed to load metadata for email ID %s: %v", curmsg.ID, err)
					if e, ok := errors.Cause(err).(*googleapi.Error); ok {
						log.Warningf("Failing to load was googleapi error %+v", e)
						if e.Code == 404 {
							mv.removeMessage <- curmsg.ID
						}
					}
				} else {
					mv.messageCh <- curmsg
				}
			}(cur)
		}

		if marked[curmsg.ID] {
			prefix += "X"
		} else {
			prefix += " "
		}

		if curmsg.IsUnread() {
			prefix = display.Bold + prefix + ">"
		} else {
			prefix += " "
		}

		star := " "
		if curmsg.HasLabel(cmdg.Starred) {
			star = "*"
			prefix = display.Yellow + prefix
		}

		screen.Printlnf(cur-scroll, "%s%s%s%s", reset, prefix, star, s)
		return nil
	}

	timer := time.NewTicker(messageListHistoryCheckTime)
	defer timer.Stop()
	historyConcurrency := newConcurrency(1)

	prev := func() bool {
		if mv.pos <= 0 {
			return false
		}
		mv.pos--
		if scroll > 0 && mv.pos < scroll+scrollLimit {
			scroll--
		}
		return true
	}
	next := func() bool {
		if mv.messages == nil {
			return false
		}
		if mv.pos >= len(mv.messages)-1 {
			return false
		}
		if mv.pos-scroll > contentHeight-scrollLimit {
			scroll++
		}
		mv.pos++
		return true
	}
	for {
		status := ""
		select {
		case histUpdate := <-mv.historyUpdateCh:
			log.Infof("Got history update: %+v", histUpdate)
			if histUpdate.historyID < mv.historyID {
				log.Warningf("Got out of order history entry %d < %d", histUpdate.historyID, mv.historyID)
			} else if histUpdate.historyID == mv.historyID {
				log.Infof("Got duplicate history update %d", mv.historyID)
			} else {
				mv.historyID = histUpdate.historyID
				for _, hist := range histUpdate.history {
					log.Infof("History entry: %d add, %d delete, %d labeladd, %d labeldelete", len(hist.MessagesAdded), len(hist.MessagesDeleted), len(hist.LabelsAdded), len(hist.LabelsRemoved))
					for _, m := range hist.MessagesDeleted {
						ind, found := messagePos[m.Message.Id]
						if found {
							log.Infof("Deleting message from in accordance with history")
							mv.messages = append(mv.messages[:ind], mv.messages[ind+1:]...)
							if ind < mv.pos {
								mv.pos--
							}
						}
					}

					for _, ladd := range hist.LabelsAdded {
						// Messages moved into this label (and other labels).
						if msgn, found := messagePos[ladd.Message.Id]; found {
							msg := mv.messages[msgn]
							for _, l := range ladd.LabelIds {
								log.Infof("Adding label %q", l)
								msg.AddLabelIDLocal(l)
							}
						} else {
							// New message for this view.
							this := false
							for _, l := range ladd.LabelIds {
								if l == mv.label {
									this = true
									break
								}
							}
							if this {
								// Confirmed. This is a new message.
								log.Infof("History says %q was moved to current label %q", ladd.Message.Id, mv.label)
								nm := cmdg.NewMessage(conn, ladd.Message.Id)
								// TODO: add it in the right place, not the top.
								mv.messages = append([]*cmdg.Message{nm}, mv.messages...)
								mkMessagePos()
							}
						}
					}

					for _, ma := range hist.MessagesAdded {
						// New messages… also in this view.
						if _, found := messagePos[ma.Message.Id]; !found {
							// Double-check that the message has the current label.
							// If there are no labels then err on the side of showing the message.
							//
							// That probably the right behaviour since it should only happen for
							// no-label searches getting new results.
							addme := true
							hasData := false
							if len(ma.Message.LabelIds) > 0 {
								addme = false
								hasData = true
								for _, l := range ma.Message.LabelIds {
									if l == mv.label {
										addme = true
										break
									}
								}
							}
							if addme {
								log.Infof("Adding message from history")
								var nm *cmdg.Message
								if hasData {
									nm = cmdg.NewMessageWithResponse(conn, ma.Message.Id, ma.Message, cmdg.LevelMinimal)
								} else {
									nm = cmdg.NewMessage(conn, ma.Message.Id)
								}
								// TODO: add it in the right place, not the top.
								mv.messages = append([]*cmdg.Message{nm}, mv.messages...)
								mkMessagePos()
							} else {
								log.Infof("Skipped adding message because history returned false positive")
							}
						}
					}
					for _, lrm := range hist.LabelsRemoved {
						ind, found := messagePos[lrm.Message.Id]
						if found {
							msg := mv.messages[ind]
							this := false
							for _, l := range lrm.LabelIds {
								for _, el := range msg.LocalLabels() {
									if l == el {
										msg.RemoveLabelIDLocal(l)
									}
								}
								if l == mv.label {
									this = true
								}
							}
							if this {
								log.Infof("… message %s gone from this view", lrm.Message.Id)
								mv.messages = append(mv.messages[:ind], mv.messages[ind+1:]...)
								if ind < mv.pos {
									mv.pos--
								}
								mkMessagePos()
							}
						}
					}
				}
			}

		case <-timer.C: // Check history every now and then.
			if mv.label != "" {
				if historyConcurrency.Take() {
					st := time.Now()
					go func() {
						defer historyConcurrency.Done()
						defer func() {
							log.Infof("History check took %v", time.Since(st))
						}()
						ctx, cancel := context.WithTimeout(ctx, messageListHistoryCheckTimeout)
						defer cancel()
						if err := mv.historyCheck(ctx); err != nil {
							log.Errorf("Error getting history: %s", err)
						}
					}()
				} else {
					log.Infof("Not history checking because one is already running")
				}
			} else {
				log.Infof("Not checking history because not in a label")
			}
			if false {
				// TODO: don't reset pos and scroll
				log.Infof("Timed reload")
				empty()
				screen.Clear()
				go mv.fetchPage(ctx, "")
			}

		case <-mv.keys.Winch():
			log.Infof("MessageListView got WINCH!")
			if err := initScreen(); err != nil {
				// Screen failed to init. Yeah it's time to bail.
				return err
			}
		case err := <-mv.errors:
			showError(screen, mv.keys, err.Error())
			screen.Draw()
			continue
		case m := <-mv.messageCh:
			cur := messagePos[m.ID]
			if err := drawMessage(cur); err != nil {
				mv.errors <- errors.Wrapf(err, "Drawing message")
			}
			screen.Draw() // TODO: avoid redrawing whole screen.
			continue
		case p := <-mv.pageCh:
			log.Printf("MessageListView: Got page!")
			pages = append(pages, p)
			mv.messages = append(mv.messages, p.Messages...)
			want := contentHeight
			if p.Response.NextPageToken == "" {
				log.Infof("All pages loaded")
				if len(mv.messages) == 0 {
					screen.Printlnf(0, "<empty>")
				}
				theresMore = false
			} else {
				if want > len(mv.messages) {
					go mv.fetchPage(ctx, p.Response.NextPageToken)
				} else {
					log.Infof("Enough pages. Have %d messages, want %d", len(mv.messages), want)
					theresMore = false
				}
			}
			mkMessagePos()

		case id := <-mv.removeMessage:
			mv.messages, mv.pos = filterMessage(mv.messages, id, mv.pos)
			mkMessagePos()

		case key, ok := <-mv.keys.Chan():
			if !ok {
				log.Errorf("MessageList: Input channel closed!")
				continue
			}
			log.Debugf("MessageListView got key %q", key)
			switch key {
			case "?", input.F1:
				help(messageListViewHelp, mv.keys)
			case input.Enter:
				if len(mv.messages) == 0 {
					// Let's assume we've never gotten to the state where mv.pos >= len(mv.messages)
					break
				}
				if mv.pos >= len(mv.messages) {
					break
				}
				for {
					vo, err := NewOpenMessageView(ctx, mv.messages[mv.pos], mv.keys)
					if err != nil {
						mv.errors <- errors.Wrapf(err, "Opening message")
					} else {
						op, err := vo.Run(ctx)
						if err != nil {
							mv.errors <- errors.Wrapf(err, "Running OpenMessageView")
						}
						op.Do(mv)
						if op.IsQuit(mv) {
							return nil
						}
						if op.IsPrev(mv) {
							if mv.pos > 0 {
								mv.pos--
								if scroll > 0 {
									scroll--
								}
							}
							continue
						}
						if op.IsNext(mv) {
							if mv.pos < len(mv.messages)-1 {
								mv.pos++
								if mv.pos-scroll > contentHeight-scrollLimit {
									scroll++
								}
							}
							continue
						}
						mkMessagePos() // op.Do() could have changed the message positions around.
					}
					break
				}
			case input.CtrlL:
				if err := initScreen(); err != nil {
					// Screen failed to init. Yeah it's time to bail.
					return err
				}
			case "e":
				idch := make(chan []string)
				ok, nm, ofs := mv.applyMarked(ctx, "archive", func(ctx context.Context, ids []string) error {
					idch <- ids
					return conn.BatchArchive(ctx, ids)
				}, marked)
				if !ok {
					break
				}
				for _, id := range <-idch {
					mv.messages[messagePos[id]].RemoveLabelIDLocal(cmdg.Inbox)
				}
				if mv.label == cmdg.Inbox {
					mv.pos -= ofs
					scroll -= ofs
					if scroll < 0 {
						scroll = 0
					}
					mv.messages = nm
					marked = map[string]bool{}
					mkMessagePos()
				}
			case "I": // Mark read.
				idch := make(chan []string)
				ok, _, _ := mv.applyMarked(ctx, "mark-read", func(ctx context.Context, ids []string) error {
					idch <- ids
					return conn.BatchUnlabel(ctx, ids, cmdg.Unread)
				}, marked)
				if !ok {
					break
				}
				for _, id := range <-idch {
					mv.messages[messagePos[id]].RemoveLabelIDLocal(cmdg.Unread)
				}
			case "U": // Mark unread.
				idch := make(chan []string)
				ok, _, _ := mv.applyMarked(ctx, "mark-unread", func(ctx context.Context, ids []string) error {
					idch <- ids
					return conn.BatchLabel(ctx, ids, cmdg.Unread)
				}, marked)
				if !ok {
					break
				}
				for _, id := range <-idch {
					mv.messages[messagePos[id]].AddLabelIDLocal(cmdg.Unread)
				}
			case "d":
				ok, nm, ofs := mv.applyMarked(ctx, "delete", conn.BatchTrash, marked)
				if !ok {
					break
				}
				mv.pos -= ofs
				scroll -= ofs
				if scroll < 0 {
					scroll = 0
				}
				mv.messages = nm
				marked = map[string]bool{}
				mkMessagePos()

			case "*":
				if mv.pos >= len(mv.messages) {
					break
				}
				// TODO: Because it's a toggle this is not suitable for batch operation.
				curmsg := mv.messages[mv.pos]
				f := curmsg.AddLabelID
				f2 := curmsg.AddLabelIDLocal

				verb := "Adding"
				if curmsg.HasLabel(cmdg.Starred) {
					f = curmsg.RemoveLabelID
					f2 = curmsg.RemoveLabelIDLocal
					verb = "Removing"
				}
				f2(cmdg.Starred)
				go func() {
					if err := f(ctx, cmdg.Starred); err != nil {
						mv.errors <- errors.Wrapf(err, "%s STARRED label", verb)
					}
				}()
			case "l":
				// TODO: can this be partially merged with 'L' code?
				ids, _, _ := filterMarked(mv.messages, marked, mv.pos)
				if len(ids) != 0 {
					var opts []*dialog.Option
					for _, l := range conn.Labels() {
						opts = append(opts, &dialog.Option{
							Key:   l.ID,
							Label: l.Label,
						})
					}
					label, err := dialog.Selection(opts, "Label> ", false, mv.keys)
					if errors.Cause(err) == dialog.ErrAborted {
						// No-op.
					} else if err != nil {
						mv.errors <- errors.Wrapf(err, "Selecting label")
					} else {
						for _, id := range ids {
							mv.messages[messagePos[id]].AddLabelIDLocal(label.Key)
						}
						log.Infof("Batch labelling %q/%q %d messages in the background…", label.Key, label.Label, len(ids))
						go func() {
							st := time.Now()
							if err := conn.BatchLabel(ctx, ids, label.Key); err != nil {
								mv.errors <- errors.Wrapf(err, "Batch labelling")
							} else {
								log.Infof("Batch labelled %d: %v", len(ids), time.Since(st))
							}
						}()
					}
				}
			case "L":
				ids, _, _ := filterMarked(mv.messages, marked, mv.pos)
				if len(ids) != 0 {
					var opts []*dialog.Option
				outer:
					for _, l := range conn.Labels() {
						for _, m := range ids {
							if !mv.messages[messagePos[m]].HasLabel(l.ID) {
								log.Warningf("Unknown label ID %q", l.ID)
								continue outer
							}
						}
						opts = append(opts, &dialog.Option{
							Key:   l.ID,
							Label: l.Label,
						})
					}
					if len(opts) > 0 {
						label, err := dialog.Selection(opts, "Label> ", false, mv.keys)
						if errors.Cause(err) == dialog.ErrAborted {
							// No-op.
						} else if err != nil {
							mv.errors <- errors.Wrapf(err, "Selecting label")
						} else {
							for _, id := range ids {
								mv.messages[messagePos[id]].RemoveLabelIDLocal(label.Key)
							}
							log.Infof("Batch unlabelling %q/%q from %d messages in the background…", label.Key, label.Label, len(ids))
							go func() {
								st := time.Now()
								if err := conn.BatchUnlabel(ctx, ids, label.Key); err != nil {
									mv.errors <- errors.Wrapf(err, "Batch labelling")
								} else {
									log.Infof("Batch unlabelled %d: %v", len(ids), time.Since(st))
								}
							}()
						}
					}
				}
			case "c":
				if err := composeNew(ctx, conn, mv.keys); err != nil {
					mv.errors <- errors.Wrapf(err, "Composing new message")
				}
			case "C":
				if err := continueDraft(ctx, conn, mv.keys); err != nil {
					mv.errors <- errors.Wrapf(err, "Continuing draft")
				}
			case input.Home, input.XHome:
				mv.pos = 0
				scroll = 0
			case "x", " ":
				if mv.pos < len(mv.messages) {
					marked[mv.messages[mv.pos].ID] = !marked[mv.messages[mv.pos].ID]
					next()
				}
			case "X":
				if mv.pos < len(mv.messages) {
					marked[mv.messages[mv.pos].ID] = !marked[mv.messages[mv.pos].ID]
					prev()
				}
			case "N", "n", "j", input.CtrlN, input.Down:
				screen.UseCache()
				if !next() {
					// If already on last one, don't redraw.
					continue
				}
			case "P", "p", "k", input.CtrlP, input.Up:
				screen.UseCache()
				if !prev() {
					// If already on first one, don't redraw.
					continue
				}
			case "r", input.CtrlR:
				empty()
				screen.Clear()
				go mv.fetchPage(ctx, "")
			case "g":
				var opts []*dialog.Option
				for _, l := range conn.Labels() {
					if strings.HasPrefix(l.ID, "CATEGORY_") {
						continue
					}
					if l.ID == "IMPORTANT" {
						continue
					}
					opts = append(opts, &dialog.Option{
						Key:   l.ID,
						Label: l.LabelString(),
					})
				}
				label, err := dialog.Selection(opts, "Label> ", false, mv.keys)
				if errors.Cause(err) == dialog.ErrAborted {
					// No-op.
				} else if err != nil {
					mv.errors <- errors.Wrapf(err, "Selecting label")
				} else {
					nv := NewMessageView(ctx, label.Key, "", mv.keys)
					// TODO: not optimal, since it adds a
					// stack frame on every navigation.
					return nv.Run(ctx)
				}
			case "1":
				// TODO: not optimal, since it adds a
				// stack frame on every navigation.
				return NewMessageView(ctx, cmdg.Inbox, "", mv.keys).Run(ctx)
			case "s", input.CtrlS:
				q, err := dialog.Entry("Query> ", mv.keys)
				if err == dialog.ErrAborted {
					// That's fine.
				} else if err != nil {
					mv.errors <- errors.Wrapf(err, "Getting query")
				} else {
					nv := NewMessageView(ctx, "", q, mv.keys)
					// TODO: not optimal, since it adds a
					// stack frame on every navigation.
					return nv.Run(ctx)
				}
			case "q":
				return nil
			default:
				log.Infof("MessageListView got unknown key %q %v", key, []byte(key))
			}
		}
		if mv.messages != nil {
			// Draw to buffer.
			st := time.Now()
			for n := 0; n < contentHeight; n++ {
				cur := n + scroll
				if cur >= len(mv.messages) {
					screen.Printlnf(n, "")
					continue
				}

				if err := drawMessage(cur); err != nil {
					mv.errors <- err
				}

				if time.Since(st) > 10*time.Millisecond {
					screen.Draw()
				}
			}
			if len(mv.messages) == 0 {
				screen.Printlnf(0, "<empty>")
			}
			log.Debugf("Print took %v", time.Since(st))
		}
		// Print status.
		if theresMore {
			status += display.Color(50) + "Loading…"
		}
		screen.Printlnf(screen.Height-2, "%s", strings.Repeat("—", screen.Width))
		screen.Printlnf(screen.Height-1, "%s", status)

		// Draw.
		st := time.Now()
		screen.Draw()
		log.Debugf("Draw took %v", time.Since(st))
	}
}
