package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/ThomasHabets/cmdg/pkg/cmdg"
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
	log.Infof("Listing messages with token %q…", token)
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

func (mv *MessageView) Run(ctx context.Context) error {
	theresMore := true
	screen, err := display.NewScreen()
	if err != nil {
		return err
	}
	screen.Printf(0, 0, "Loading…")
	screen.Draw()
	var pages []*cmdg.Page
	var messages []*cmdg.Message
	messagePos := map[string]int{}
	contentHeight := screen.Height - 2
	pos := 0 // Current message.
	scroll := 0

	drawMessage := func(cur int) error {
		s := "Loading…"
		if messages[cur].HasData(cmdg.LevelMetadata) {
			subj, err := messages[cur].GetHeader(ctx, "subject")
			if err != nil {
				return err
			}
			tm, err := messages[cur].GetTimeFmt(ctx)
			if err != nil {
				return err
			}
			from, err := messages[cur].GetFrom(ctx)
			if err != nil {
				return err
			}
			from = display.FixedWidth(from, 20)
			s = fmt.Sprintf("%[1]*.[1]*[2]s | %[3]s | %[4]s",
				6, tm,
				from, subj)
		} else {
			go func(cur int) {
				messages[cur].Preload(ctx, cmdg.LevelMetadata)
				mv.messageCh <- messages[cur]
			}(cur)
		}
		// Show current.
		prefix := " "
		if cur == pos {
			prefix = display.Reverse + "*"
		}

		if false {
			// TODO: if marked
			prefix += "X"
		} else {
			prefix += " "
		}

		if messages[cur].IsUnread() {
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
			log.Errorf("Got error!", err)
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
				messagePos[m.ID] = len(messages) + n
			}
			messages = append(messages, p.Messages...)
			want := contentHeight
			if p.Response.NextPageToken == "" {
				log.Infof("All pages loaded")
				theresMore = false
			} else {
				if want > len(messages) {
					go mv.fetchPage(ctx, p.Response.NextPageToken)
				} else {
					log.Infof("Enough pages. Have %d messages, want %d", len(messages), want)
					theresMore = false
				}
			}

		case key := <-mv.keys.Chan():
			log.Debugf("Got key %d", key)
			switch key {
			case 'N', 'n', input.CtrlN:
				if (messages != nil) && (pos < len(messages)-1) {
					if pos-scroll > contentHeight-scrollLimit {
						scroll++
					}
					pos++
				} else {
					continue
				}
			case 'P', 'p', input.CtrlP:
				if pos > 0 {
					pos--
					if scroll > 0 && pos < scroll+scrollLimit {
						scroll--
					}
				} else {
					continue
				}
			case 'q':
				return nil
			default:
				log.Infof("Unknown key %v", key)
			}
		}
		if messages != nil {
			// Draw to buffer.
			st := time.Now()
			for n := 0; n < contentHeight; n++ {
				cur := n + scroll
				if cur >= len(messages) {
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
		screen.Printlnf(screen.Height-2, "%s", strings.Repeat("-", screen.Width))
		screen.Printlnf(screen.Height-1, "%s", status)

		// Draw.
		st := time.Now()
		screen.Draw()
		log.Infof("Draw took %v", time.Since(st))
	}
}
