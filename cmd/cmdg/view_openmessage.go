package main

import (
	"context"
	"fmt"
	"strings"

	log "github.com/sirupsen/logrus"

	"github.com/ThomasHabets/cmdg/pkg/cmdg"
	"github.com/ThomasHabets/cmdg/pkg/display"
	"github.com/ThomasHabets/cmdg/pkg/input"
)

const (
	tsLayout = "2006-01-02 15:04:05"
)

type OpenMessageView struct {
	msg    *cmdg.Message
	keys   *input.Input
	screen *display.Screen

	update chan struct{}
	errors chan error
}

func NewOpenMessageView(ctx context.Context, msg *cmdg.Message, in *input.Input) (*OpenMessageView, error) {
	screen, err := display.NewScreen()
	if err != nil {
		return nil, err
	}
	ov := &OpenMessageView{
		msg:    msg,
		keys:   in,
		screen: screen,
		update: make(chan struct{}),
		errors: make(chan error, 20),
	}
	go func() {
		msg.Preload(ctx, cmdg.LevelFull)
		ov.update <- struct{}{}
	}()
	return ov, err
}

func cancelledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func (ov *OpenMessageView) Draw(scroll int) error {
	// Some functions below need a context, but they should never make RPCs so let's give them
	ctx := cancelledContext()

	line := 0

	ov.screen.Printlnf(line, "Email %d of %d", -1, -1)
	line++

	// From.
	from, err := ov.msg.GetHeader(ctx, "From")
	if err != nil {
		ov.errors <- err
		from = fmt.Sprintf("Unknown: %q", err)
	}
	var signed string
	var encrypted string
	if st := ov.msg.GPGStatus(); st != nil {
		if st.Signed != "" {
			if st.GoodSignature {
				signed = fmt.Sprintf(" — signed by %s", st.Signed)
				if len(st.Warnings) == 0 {
					signed = display.Bold + display.Green + signed
				} else {
					signed = " but with warnings"
				}
			} else {
				signed = fmt.Sprintf("%s — BAD signature from %s", display.Bold+display.Red, st.Signed)
			}
		}
		if len(st.Encrypted) != 0 {
			encrypted = fmt.Sprintf("%s — Encrypted to %s", display.Green+display.Bold, strings.Join(st.Encrypted, ";"))
		}
	}
	ov.screen.Printlnf(line, "From: %s%s", from, signed)
	line++

	// To.
	to, err := ov.msg.GetHeader(ctx, "To")
	if err != nil {
		ov.errors <- err
		to = fmt.Sprintf("Unknown: %q", err)
	}
	ov.screen.Printlnf(line, "To: %s%s", to, encrypted)
	line++

	// CC.
	cc, err := ov.msg.GetHeader(ctx, "CC")
	if err != nil {
		cc = ""
	}
	ov.screen.Printlnf(line, "CC: %s", cc)
	line++

	// Date.
	date, err := ov.msg.GetTime(ctx)
	if err != nil {
		ov.errors <- err
	}
	ov.screen.Printlnf(line, "Date: %s", date.Format(tsLayout))
	line++

	// Subject
	subject, err := ov.msg.GetHeader(ctx, "Subject")
	if err != nil {
		ov.errors <- err
		subject = fmt.Sprintf("Unknown: %q", err)
	}
	ov.screen.Printlnf(line, "Subject: %s", subject)
	line++

	// Labels
	labels, err := ov.msg.GetLabelsString(ctx)
	if err != nil {
		ov.errors <- err
		labels = fmt.Sprintf("Unknown: %q", err)
	}
	ov.screen.Printlnf(line, "Labels: %s", labels)
	line++

	ov.screen.Printlnf(line, strings.Repeat("—", ov.screen.Width))
	line++
	b, err := ov.msg.GetBody(ctx)
	if err != nil {
		ov.screen.Printlnf(line, display.Red+"Failed to load body of message: %v", err)
	} else {
		for _, l := range strings.Split(b, "\n")[scroll:] {
			l = strings.Trim(l, "\r ")
			ov.screen.Printlnf(line, "%s", l)
			line++
			if line >= ov.screen.Height-2 {
				break
			}
		}
	}
	ov.screen.Printlnf(ov.screen.Height-2, strings.Repeat("—", ov.screen.Width))
	return nil
}

func (ov *OpenMessageView) Run(ctx context.Context) error {
	ov.screen.Printf(0, 0, "Loading…")
	ov.screen.Draw()
	scroll := 0
	for {
		select {
		case err := <-ov.errors:
			err = err
			// TODO: Surface error.
		case <-ov.update:
			log.Infof("Got message, I think")
			ov.Draw(scroll)
		case key := <-ov.keys.Chan():
			switch key {
			case 'u', 'q':
				return nil
			case 'n':
				if ov.msg.HasData(cmdg.LevelFull) {
					log.Infof("Has data")
					lines, err := ov.msg.Lines(ctx)
					if err != nil {
						return err
					}
					if scroll < (lines - ov.screen.Height + 10) {
						log.Infof("yes scroll")
						scroll++
						ov.Draw(scroll)
					}
				}
			case 'p':
				scroll--
				if scroll < 0 {
					scroll = 0
				}
				ov.Draw(scroll)
			}
		}
		ov.screen.Draw()
	}
}
