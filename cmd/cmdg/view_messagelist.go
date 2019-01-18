package main

import (
	"context"
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
	keys   *input.Input
	errors chan error
	pageCh chan *cmdg.Page
}

func NewMessageView(ctx context.Context, label string, in *input.Input) *MessageView {
	v := &MessageView{
		label:  label,
		errors: make(chan error),
		pageCh: make(chan *cmdg.Page),
		keys:   in,
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
	if err := page.PreloadSubjects(ctx); err != nil {
		m.errors <- err
		return
	}
	m.pageCh <- page
}

func (m *MessageView) Run(ctx context.Context) error {
	theresMore := true
	screen, err := display.NewScreen()
	if err != nil {
		return err
	}
	screen.Printf(0, 0, "Loading…")
	screen.Draw()
	var pages []*cmdg.Page
	var messages []*cmdg.Message
	contentHeight := screen.Height - 2
	pos := 0 // Current message.
	scroll := 0
	for {
		// Get event.
		select {
		case err := <-m.errors:
			log.Errorf("Got error!", err)
			screen.Printf(10, 0, "Got error: %v", err)
		case p := <-m.pageCh:
			log.Printf("Got page!")
			pages = append(pages, p)
			messages = append(messages, p.Messages...)
			want := contentHeight
			if p.Response.NextPageToken == "" {
				log.Infof("All pages loaded")
				theresMore = false
			} else {
				if want > len(messages) {
					go m.fetchPage(ctx, p.Response.NextPageToken)
				} else {
					log.Infof("Enough pages. Have %d messages, want %d", len(messages), want)
					theresMore = false
				}
			}

		case key := <-m.keys.Chan():
			log.Debugf("Got key %d", key)
			switch key {
			case 'n':
				if (messages != nil) && (pos < len(messages)-1) {
					if pos-scroll > contentHeight-scrollLimit {
						scroll++
					}
					pos++
				}
			case 'p':
				if pos > 0 {
					pos--
				}
				if scroll > 0 && pos < scroll+scrollLimit {
					scroll--
				}
			case 'q':
				return nil
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
				s, err := messages[cur].GetHeader(ctx, "subject")
				if err != nil {
					return err
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

				screen.Printlnf(n, "%s %s", prefix, s)
			}
			log.Infof("Print took %v", time.Since(st))
		}
		// Print status.
		status := ""
		if theresMore {
			status += display.Color(50) + "Loading…"
		}
		screen.Printlnf(screen.Height-1, "%s", status)

		// Draw.
		st := time.Now()
		screen.Draw()
		log.Infof("Draw took %v", time.Since(st))
	}
}
