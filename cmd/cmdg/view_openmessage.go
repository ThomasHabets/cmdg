package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/ThomasHabets/cmdg/pkg/cmdg"
	"github.com/ThomasHabets/cmdg/pkg/dialog"
	"github.com/ThomasHabets/cmdg/pkg/display"
	"github.com/ThomasHabets/cmdg/pkg/input"
)

const (
	tsLayout = "2006-01-02 15:04:05"

	openMessageViewHelp = `?         — Help
^R        — Reload
l         — Add label
L         — Remove label
*         — Toggle "starred"
u         — Exit message
U         — Mark unread
n         — Scroll down
space     — Page down
backspace — Page up
p         — Scroll up
f         — Forward message
r         — Reply
a         — Reply all
e         — Archive
t         — Browse attachments (if any)
\         — Show raw message source

Press [enter] to exit
`
)

func help(txt string, keys *input.Input) error {
	screen, err := display.NewScreen()
	if err != nil {
		return err
	}
	lines := strings.Split(txt, "\n")
	maxlen := 0
	for _, l := range lines {
		if n := len(l); n > maxlen {
			maxlen = n
		}
	}
	screen.Printlnf(0, strings.Repeat("—", screen.Width))
	for n, l := range lines {
		screen.Printlnf(n+1, "%s%s", strings.Repeat(" ", (screen.Width-maxlen)/2), l)
	}
	for {
		screen.Draw()
		k := <-keys.Chan()
		switch k {
		case input.Enter:
			return nil
		}
	}
}

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
		if err := msg.Preload(ctx, cmdg.LevelFull); err != nil {
			ov.errors <- err
		}
		ov.update <- struct{}{}
	}()
	return ov, err
}

func cancelledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func (ov *OpenMessageView) Draw(lines []string, scroll int) error {
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

	// Draw body.
	for _, l := range lines[scroll:] {
		l = strings.Trim(l, "\r ")
		ov.screen.Printlnf(line, "%s", l)
		line++
		if line >= ov.screen.Height-2 {
			break
		}
	}
	ov.screen.Printlnf(ov.screen.Height-2, strings.Repeat("—", ov.screen.Width))
	return nil
}

func showError(oscreen *display.Screen, keys *input.Input, msg string) {
	screen := oscreen.Copy()
	lines := []string{
		strings.Repeat("—", screen.Width),
	}
	for len(msg) > 0 {
		this := msg
		if len(this) > screen.Width {
			this, msg = msg[:screen.Width], msg[screen.Width:]
		} else {
			msg = ""
		}
		lines = append(lines, this)
	}
	lines = append(lines, "Press [enter] to continue", lines[0])
	start := (screen.Height - len(lines)) / 2
	for n, l := range lines {
		screen.Printlnf(start+n, "%s%s", display.Red, l)
	}
	screen.Draw()
	for {
		if input.Enter == <-keys.Chan() {
			return
		}
	}
}

func (ov *OpenMessageView) Run(ctx context.Context) (*MessageViewOp, error) {
	scroll := 0
	initScreen := func() error {
		var err error
		ov.screen, err = display.NewScreen()
		if err != nil {
			return err
		}
		scroll = 0
		return nil
	}
	if err := initScreen(); err != nil {
		return nil, err
	}
	ov.screen.Printf(0, 0, "Loading…")
	ov.screen.Draw()
	var lines []string
	for {
		select {
		case <-ov.keys.Winch():
			log.Infof("OpenMessageView got WINCH")
			if err := initScreen(); err != nil {
				// Screen failed to init. Yeah it's time to bail.
				return nil, err
			}
			go func() {
				ov.update <- struct{}{}
			}()
		case err := <-ov.errors:
			showError(ov.screen, ov.keys, err.Error())
			ov.screen.Draw()
			continue
		case <-ov.update:
			log.Infof("Message arrived")
			b, err := ov.msg.GetBody(ctx)
			if err != nil {
				ov.errors <- errors.Wrapf(err, "Getting message body")
			} else {
				lines = []string{}
				for _, l := range strings.Split(b, "\n") {
					for len(l) > 0 {
						if len(l) > ov.screen.Width {
							lines = append(lines, l[:ov.screen.Width])
							l = l[ov.screen.Width:]
						} else {
							lines = append(lines, l)
							l = ""
						}
					}
				}
			}
			go func() {
				if ov.msg.IsUnread() {
					if err := ov.msg.RemoveLabelID(ctx, cmdg.Unread); err != nil {
						ov.errors <- errors.Wrapf(err, "Failed to remove unread label")
					}
				}
				// Does not need to be signaled to
				// messageview; label list gets
				// reloaded by RemoveLabelID.
			}()
			ov.Draw(lines, scroll)
		case key := <-ov.keys.Chan():
			switch key {
			case input.CtrlR:
				go func() {
					if err := ov.msg.Reload(ctx, cmdg.LevelFull); err != nil {
						ov.errors <- errors.Wrap(err, "reloading message")
					}
					ov.update <- struct{}{}
				}()
			case '?':
				help(openMessageViewHelp, ov.keys)
			case '*':
				if ov.msg.HasLabel(cmdg.Starred) {
					if err := ov.msg.RemoveLabelID(ctx, cmdg.Starred); err != nil {
						ov.errors <- errors.Wrap(err, "Removing STARRED label")
					}
				} else {
					if err := ov.msg.AddLabelID(ctx, cmdg.Starred); err != nil {
						ov.errors <- errors.Wrap(err, "Adding STARRED label")
					}
				}
				if err := ov.msg.ReloadLabels(ctx); err != nil {
					ov.errors <- errors.Wrapf(err, "Failed to reload labels")
				}
				ov.Draw(lines, scroll)
			case 'l':
				var opts []*dialog.Option
				for _, l := range conn.Labels() {
					opts = append(opts, &dialog.Option{
						Key:   l.ID,
						Label: l.Label,
					})
				}
				label, err := dialog.Selection(opts, "Label> ", false, ov.keys)
				if errors.Cause(err) == dialog.ErrAborted {
					// No-op.
				} else if err != nil {
					ov.errors <- errors.Wrapf(err, "Selecting label")
				} else {
					st := time.Now()
					if err := ov.msg.AddLabelID(ctx, label.Key); err != nil {
						ov.errors <- errors.Wrapf(err, "Failed to label")
					} else {
						log.Infof("Labelled: %v", time.Since(st))
					}
					if err := ov.msg.ReloadLabels(ctx); err != nil {
						ov.errors <- errors.Wrapf(err, "Failed to reload labels")
					}
				}
				ov.Draw(lines, scroll)
			case 'L':
				var opts []*dialog.Option
				labels, err := ov.msg.GetLabels(ctx, true)
				if err != nil {
					ov.errors <- errors.Wrapf(err, "Getting message labels")
				} else {
					for _, l := range labels {
						opts = append(opts, &dialog.Option{
							Key:   l.ID,
							Label: l.Label,
						})
					}
					label, err := dialog.Selection(opts, "Label> ", false, ov.keys)
					if errors.Cause(err) == dialog.ErrAborted {
						// No-op.
					} else if err != nil {
						ov.errors <- errors.Wrapf(err, "Selecting label")
					} else {
						st := time.Now()
						if err := ov.msg.RemoveLabelID(ctx, label.Key); err != nil {
							ov.errors <- errors.Wrapf(err, "Failed to unlabel")
						} else {
							log.Infof("Unlabelled: %v", time.Since(st))
						}
						if err := ov.msg.ReloadLabels(ctx); err != nil {
							ov.errors <- errors.Wrapf(err, "Failed to reload labels")
						}
					}
					ov.Draw(lines, scroll)
				}
			case 'u', 'q':
				return nil, nil
			case 'U':
				if err := ov.msg.AddLabelID(ctx, cmdg.Unread); err != nil {
					ov.errors <- fmt.Errorf("Failed to mark unread : %v", err)
				} else {
					return nil, nil
				}
			case 'n':
				scroll = ov.scroll(ctx, len(lines), scroll, 1)
				ov.Draw(lines, scroll)
			case ' ', input.CtrlV:
				scroll = ov.scroll(ctx, len(lines), scroll, ov.screen.Height-10)
				ov.Draw(lines, scroll)
			case 'p':
				scroll = ov.scroll(ctx, len(lines), scroll, -1)
				ov.Draw(lines, scroll)
			case 'f':
				if err := forward(ctx, conn, ov.keys, ov.msg); err != nil {
					ov.errors <- fmt.Errorf("Failed to forward: %v", err)
				}
			case 'r':
				if err := reply(ctx, conn, ov.keys, ov.msg); err != nil {
					ov.errors <- fmt.Errorf("Failed to reply: %v", err)
				}
			case 'a':
				if err := replyAll(ctx, conn, ov.keys, ov.msg); err != nil {
					ov.errors <- fmt.Errorf("Failed to replyAll: %v", err)
				}
			case 'e':
				if err := ov.msg.RemoveLabelID(ctx, cmdg.Inbox); err != nil {
					ov.errors <- fmt.Errorf("Failed to archive : %v", err)
				} else {
					return OpRemoveCurrent(nil), nil
				}
			case 't':
				as, err := ov.msg.Attachments(ctx)
				if err != nil {
					ov.errors <- fmt.Errorf("Listing attachments failed: %v", err)
				} else if len(as) > 0 {
					if err := listAttachments(ctx, ov.keys, ov.msg); errors.Cause(err) == dialog.ErrAborted {
						log.Infof("View attachment aborted")
					} else if err != nil {
						ov.errors <- fmt.Errorf("Attachment browser action failed: %v", err)
					}
				}
			case '\\':
				if err := ov.showRaw(ctx); err != nil {
					ov.errors <- err
				}
			case input.Backspace, input.CtrlH:
				scroll = ov.scroll(ctx, len(lines), scroll, -(ov.screen.Height - 10))
				ov.Draw(lines, scroll)
			default:
				log.Infof("Unknown key: %d", key)
			}
		}
		ov.screen.Draw()
	}
}

func (ov *OpenMessageView) showRaw(ctx context.Context) error {
	m, err := ov.msg.Raw(ctx)
	if err != nil {
		return errors.Wrapf(err, "Fetching raw msg")
	}
	ov.keys.Stop()
	defer ov.keys.Start()

	cmd := exec.CommandContext(ctx, pagerBinary)
	cmd.Stdin = strings.NewReader(m)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return errors.Wrapf(err, "failed to start pager %q", pagerBinary)
	}
	if err := cmd.Wait(); err != nil {
		return errors.Wrapf(err, "pager %q failed", pagerBinary)
	}
	log.Infof("Pager finished")
	return nil
}

func (ov *OpenMessageView) scroll(ctx context.Context, lines, scroll, inc int) int {
	if ov.msg.HasData(cmdg.LevelFull) {
		scroll += inc
		if maxscroll := (lines - ov.screen.Height + 10); scroll >= maxscroll {
			scroll = maxscroll
		}
		if scroll < 0 {
			scroll = 0
		}
	}
	return scroll
}
