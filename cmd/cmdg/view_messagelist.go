package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/ThomasHabets/cmdg/pkg/cmdg"
	"github.com/ThomasHabets/cmdg/pkg/dialog"
	"github.com/ThomasHabets/cmdg/pkg/display"
	"github.com/ThomasHabets/cmdg/pkg/input"
	gmail "google.golang.org/api/gmail/v1"
)

const (
	scrollLimit = 5

	messageListViewHelp = `?             — Help
enter         — Open message
space, x      — Mark message
e             — Archive marked messages
l             — Label marked messages
L             — Unlabel marked messages
*             — Toggle starred on hilighted message
c             — Compose new message
C             — Continue message from draft
N, n, ^N, j   — Next message
P, p, ^P, k   — Previous message
r, ^R         — Reload current view
g             — Go to label
1             — Go to inbox
s             — Search
q             — Quit
^L            — Refresh screen

Press [enter] to exit
`
)

var (
	messageListReloadTime       = time.Minute
	messageListHistoryCheckTime = 10 * time.Second
)

type historyUpdate struct {
	historyID cmdg.HistoryID
	history   []*gmail.History
}

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

	// Only for use by main thread.
	messages  []*cmdg.Message
	pos       int
	historyID cmdg.HistoryID
}

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

func (mv *MessageView) fetchPage(ctx context.Context, token string) {
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
		return
	}
	log.Infof("Listing messages took %v", time.Since(st))
	go func() {
		if err := page.PreloadSubjects(ctx); err != nil {
			mv.errors <- err
			return
		}
	}()
	mv.pageCh <- page
}

type MessageViewOp struct {
	fun  func(*MessageView)
	next *MessageViewOp
}

func (op *MessageViewOp) Do(view *MessageView) {
	if op == nil {
		return
	}
	op.fun(view)
	if op.next != nil {
		op.next.Do(view)
	}
}

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

// Gives messages, a marked map, and current pos, return marked ids,
// messages-if-marked-removed, and how much pos should go back by.
func filterMarked(msgs []*cmdg.Message, marked map[string]bool, pos int) ([]string, []*cmdg.Message, int) {
	var ids []string
	var ms []*cmdg.Message
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
			subj, err := curmsg.GetHeader(ctx, "subject")
			if errors.Cause(err) == cmdg.ErrMissing || subj == "" {
				subj = "(No subject)"
			} else if err != nil {
				return err
			}
			tm, err := curmsg.GetTimeFmt(ctx)
			if err != nil {
				return err
			}
			from, err := curmsg.GetFrom(ctx)
			if err != nil {
				return err
			}
			colors, fullColors, err := curmsg.GetLabelColors(ctx)
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
				curmsg.Preload(ctx, cmdg.LevelMetadata)
				mv.messageCh <- curmsg
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
							nm := cmdg.NewMessage(conn, ma.Message.Id)
							// TODO: add it in the right place, not the top.
							mv.messages = append([]*cmdg.Message{nm}, mv.messages...)
							mkMessagePos()
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

		case <-timer.C:
			if mv.label != "" {
				go func() {
					hists, hid, err := conn.History(ctx, mv.historyID, mv.label)
					if err != nil {
						log.Errorf("Getting history since %d: %v", mv.historyID, err)
					} else if len(hists) == 0 {
						log.Infof("No history since last check")
					} else {
						mv.historyUpdateCh <- historyUpdate{
							historyID: hid,
							history:   hists,
						}
					}
				}()
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

		case key := <-mv.keys.Chan():
			log.Debugf("MessageListView got key %d", key)
			switch key {
			case '?':
				help(messageListViewHelp, mv.keys)
			case input.Enter:
				vo, err := NewOpenMessageView(ctx, mv.messages[mv.pos], mv.keys)
				if err != nil {
					mv.errors <- errors.Wrapf(err, "Opening message")
				} else {
					op, err := vo.Run(ctx)
					if err != nil {
						mv.errors <- errors.Wrapf(err, "Running OpenMessageView")
					}
					op.Do(mv)
					mkMessagePos() // op.Do() could have changed the message positions around.
				}
			case input.CtrlL:
				if err := initScreen(); err != nil {
					// Screen failed to init. Yeah it's time to bail.
					return err
				}
			case 'x', ' ':
				marked[mv.messages[mv.pos].ID] = !marked[mv.messages[mv.pos].ID]
			case 'e':
				ids, nm, ofs := filterMarked(mv.messages, marked, mv.pos)
				if len(ids) == 0 {
					break
				}
				go func() {
					st := time.Now()
					if err := conn.BatchArchive(ctx, ids); err != nil {
						mv.errors <- errors.Wrapf(err, "Batch archiving")
					}
					log.Infof("Batch archived %d: %v", len(ids), time.Since(st))
				}()
				log.Infof("Batch archiving %d (in background)", len(ids))
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
			case '*':
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
			case 'l':
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
			case 'L':
				ids, _, _ := filterMarked(mv.messages, marked, mv.pos)
				if len(ids) != 0 {
					var opts []*dialog.Option
				outer:
					for _, l := range conn.Labels() {
						if l.ID == cmdg.Inbox {
							continue
						}
						for _, m := range ids {
							if !mv.messages[messagePos[m]].HasLabel(l.ID) {
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
			case 'c':
				if err := composeNew(ctx, conn, mv.keys); err != nil {
					mv.errors <- errors.Wrapf(err, "Composing new message")
				}
			case 'C':
				if err := continueDraft(ctx, conn, mv.keys); err != nil {
					mv.errors <- errors.Wrapf(err, "Continuing draft")
				}
			case 'N', 'n', 'j', input.CtrlN:
				if (mv.messages != nil) && (mv.pos < len(mv.messages)-1) {
					if mv.pos-scroll > contentHeight-scrollLimit {
						scroll++
					}
					mv.pos++
				} else {
					continue
				}
			case 'P', 'p', 'k', input.CtrlP:
				if mv.pos > 0 {
					mv.pos--
					if scroll > 0 && mv.pos < scroll+scrollLimit {
						scroll--
					}
				} else {
					continue
				}
			case 'r', input.CtrlR:
				empty()
				screen.Clear()
				go mv.fetchPage(ctx, "")
			case 'g':
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
			case '1':
				// TODO: not optimal, since it adds a
				// stack frame on every navigation.
				return NewMessageView(ctx, cmdg.Inbox, "", mv.keys).Run(ctx)
			case 's':
				q, err := dialog.Entry("Query> ", mv.keys)
				if err == dialog.ErrAborted {
					// That's fine.
				} else if err != nil {
					mv.errors <- errors.Wrapf(err, "Getting query")
				} else if q != "" {
					nv := NewMessageView(ctx, "", q, mv.keys)
					// TODO: not optimal, since it adds a
					// stack frame on every navigation.
					return nv.Run(ctx)
				}
			case 'q':
				return nil
			default:
				log.Infof("MessageListView got unknown key %v", key)
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
