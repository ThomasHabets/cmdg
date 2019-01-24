package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/ThomasHabets/cmdg/pkg/cmdg"
	"github.com/ThomasHabets/cmdg/pkg/dialog"
	"github.com/ThomasHabets/cmdg/pkg/display"
	"github.com/ThomasHabets/cmdg/pkg/input"
)

const (
	scrollLimit = 5
)

type MessageView struct {
	// Static state.
	label string

	// Communicate with main thread.
	keys      *input.Input
	errors    chan error
	pageCh    chan *cmdg.Page
	messageCh chan *cmdg.Message

	// Only for use by main thread.
	messages []*cmdg.Message
	pos      int
}

func NewMessageView(ctx context.Context, label string, in *input.Input) *MessageView {
	v := &MessageView{
		label:     label,
		errors:    make(chan error),
		pageCh:    make(chan *cmdg.Page),
		messageCh: make(chan *cmdg.Message),
		keys:      in,
	}
	go v.fetchPage(ctx, "")
	return v
}

func (m *MessageView) fetchPage(ctx context.Context, token string) {
	log.Infof("Listing messages on label %q with token %q…", m.label, token)
	page, err := conn.ListMessages(ctx, m.label, token)
	if err != nil {
		m.errors <- err
		return
	}
	go func() {
		if err := page.PreloadSubjects(ctx); err != nil {
			m.errors <- err
			return
		}
	}()
	m.pageCh <- page
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

func (mv *MessageView) Run(ctx context.Context) error {
	theresMore := true
	screen, err := display.NewScreen()
	if err != nil {
		return err
	}
	contentHeight := screen.Height - 2
	var pages []*cmdg.Page
	messagePos := map[string]int{}
	scroll := 0

	empty := func() {
		screen.Printf(0, 0, "Loading…")
		screen.Draw()
		pages = nil
		mv.messages = nil
		messagePos = map[string]int{}
		mv.pos = 0
		scroll = 0
	}
	empty()

	drawMessage := func(cur int) error {
		s := "Loading…"
		if mv.messages[cur].HasData(cmdg.LevelMetadata) {
			subj, err := mv.messages[cur].GetHeader(ctx, "subject")
			if err != nil {
				return err
			}
			tm, err := mv.messages[cur].GetTimeFmt(ctx)
			if err != nil {
				return err
			}
			from, err := mv.messages[cur].GetFrom(ctx)
			if err != nil {
				return err
			}
			from = display.FixedWidth(from, 20)
			s = fmt.Sprintf("%[1]*.[1]*[2]s | %[3]s | %[4]s",
				6, tm,
				from, subj)
		} else {
			go func(cur int) {
				mv.messages[cur].Preload(ctx, cmdg.LevelMetadata)
				mv.messageCh <- mv.messages[cur]
			}(cur)
		}
		// Show current.
		prefix := " "
		if cur == mv.pos {
			prefix = display.Reverse + "*"
		}

		if false {
			// TODO: if marked
			prefix += "X"
		} else {
			prefix += " "
		}

		if mv.messages[cur].IsUnread() {
			prefix = display.Bold + prefix + ">"
		} else {
			prefix += " "
		}

		screen.Printlnf(cur-scroll, "%s %s", prefix, s)
		return nil
	}

	for {
		// Get event.
		select {
		case err := <-mv.errors:
			log.Errorf("MessageView got error: %v", err)
			screen.Printf(10, 0, "Got error: %v", err)
		case m := <-mv.messageCh:
			cur := messagePos[m.ID]
			if err := drawMessage(cur); err != nil {
				return err
			}
			screen.Draw()
			continue
		case p := <-mv.pageCh:
			log.Printf("Got page!")
			pages = append(pages, p)
			for n, m := range p.Messages {
				messagePos[m.ID] = len(mv.messages) + n
			}
			mv.messages = append(mv.messages, p.Messages...)
			want := contentHeight
			if p.Response.NextPageToken == "" {
				log.Infof("All pages loaded")
				theresMore = false
			} else {
				if want > len(mv.messages) {
					go mv.fetchPage(ctx, p.Response.NextPageToken)
				} else {
					log.Infof("Enough pages. Have %d messages, want %d", len(mv.messages), want)
					theresMore = false
				}
			}

		case key := <-mv.keys.Chan():
			log.Debugf("Got key %d", key)
			switch key {
			case 13:
				vo, err := NewOpenMessageView(ctx, mv.messages[mv.pos], mv.keys)
				if err != nil {
					return err
				}
				op, err := vo.Run(ctx)
				if err != nil {
					return err
				}
				op.Do(mv)
			case 'c':
				if err := composeNew(ctx, conn, mv.keys); err != nil {
					return err
				}
			case 'N', 'n', input.CtrlN:
				if (mv.messages != nil) && (mv.pos < len(mv.messages)-1) {
					if mv.pos-scroll > contentHeight-scrollLimit {
						scroll++
					}
					mv.pos++
				} else {
					continue
				}
			case 'P', 'p', input.CtrlP:
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
					opts = append(opts, &dialog.Option{
						Key:   l.ID,
						Label: l.Label,
					})
				}
				label, err := dialog.Selection(opts, false, mv.keys)
				if err != nil {
					return err
				}
				nv := NewMessageView(ctx, label.Key, mv.keys)

				// TODO: not optimal, since it adds a
				// stack frame on every navigation.
				return nv.Run(ctx)
			case 'q':
				return nil
			default:
				log.Infof("Unknown key %v", key)
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
					return err
				}

				if time.Since(st) > 10*time.Millisecond {
					screen.Draw()
				}
			}
			log.Infof("Print took %v", time.Since(st))
		}
		// Print status.
		status := ""
		if theresMore {
			status += display.Color(50) + "Loading…"
		}
		screen.Printlnf(screen.Height-2, "%s", strings.Repeat("—", screen.Width))
		screen.Printlnf(screen.Height-1, "%s", status)

		// Draw.
		st := time.Now()
		screen.Draw()
		log.Infof("Draw took %v", time.Since(st))
	}
}
