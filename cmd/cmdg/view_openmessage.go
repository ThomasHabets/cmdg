package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/ThomasHabets/cmdg/pkg/cmdg"
	"github.com/ThomasHabets/cmdg/pkg/dialog"
	"github.com/ThomasHabets/cmdg/pkg/display"
	"github.com/ThomasHabets/cmdg/pkg/input"
)

const (
	tsLayout = "2006-01-02 15:04:05"

	openMessageViewHelp = `?, F1     — Help
^R             — Reload
l              — Add label
L              — Remove label
*              — Toggle "starred"
u              — Exit message
U              — Mark unread
n, Down        — Scroll down
space          — Page down
backspace      — Page up
p, Up          — Scroll up
^P             — Previous message
^N             — Next message
f              — Forward message
r              — Reply
s, ^s          — Search within message
a              — Reply all
e              — Archive
t              — Browse attachments (if any)
H              — Force HTML view
\              — Show raw message source
|              — Pipe to command

Press [enter] to exit
`
)

var (
	enableDottime = flag.Bool("dottime", false, "Enable dottime.")
)

func isGraphicString(s string) bool {
	for _, r := range s {
		if !unicode.IsGraphic(r) {
			return false
		}
	}
	return true
}

func hilightIncremental(s string, m [][]int, format string) string {
	pos := 0
	var ret []string
	for _, h := range m {
		a, b := h[0], h[1]
		if a > pos {
			ret = append(ret, s[pos:a])
		}
		ret = append(ret, fmt.Sprintf("%s%s%s", format, s[a:b], display.Reset))
		pos = b
	}
	if pos < len(s) {
		ret = append(ret, s[pos:])
	}
	return strings.Join(ret, "")
}

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

// OpenMessageView is the view for an open message.
type OpenMessageView struct {
	msg    *cmdg.Message
	keys   *input.Input
	screen *display.Screen

	update chan struct{}
	errors chan error

	inIncrementalSearch bool
	incrementalCount    int
	incrementalCurrent  int
	incrementalQuery    string

	// Local view state. Main goroutine only.
	preferHTML bool
}

func dottime(t time.Time) string {
	_, s := t.Zone()
	return t.UTC().Format("2006-01-02T15·04·05") + fmt.Sprintf("%+03d", s/3600)
}

// NewOpenMessageView creates a new open message view.
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
		st := time.Now()
		if err := msg.Preload(ctx, cmdg.LevelFull); err != nil {
			ov.errors <- err
		}
		log.Infof("Got full message in %v", time.Since(st))
		ov.update <- struct{}{}
	}()
	return ov, err
}

func cancelledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// Draw draws the open message.
func (ov *OpenMessageView) Draw(lines []string, scroll int) error {
	// Some functions below need a context, but they should never make RPCs so let's give them
	ctx := cancelledContext()

	line := 0
	contentSpace := ov.screen.Height - 10

	var searching string
	if ov.inIncrementalSearch {
		searching = fmt.Sprintf(" Incremental search: %s (at %d of %d)", ov.incrementalQuery, ov.incrementalCurrent, ov.incrementalCount)
	}

	// TODO: msg index.
	ov.screen.Printlnf(line, "Email %d of %d (%d%%)%s", -1, -1, int(100*float64(scroll)/float64(len(lines)-contentSpace)), searching)
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

	if date, err := ov.msg.GetOriginalTime(ctx); err != nil {
		log.Warningf("Could not parse date for message: %v", err)
		s, _ := ov.msg.GetDateHeader(ctx)
		ov.screen.Printlnf(line, "Date: %s (parse error: %v)", s, err)
		//ov.errors <- err
	} else {
		dateLocal := date.Local()
		dt := ""
		if *enableDottime {
			dt = fmt.Sprintf(" (dottime: %s)", dottime(date))
		}
		ov.screen.Printlnf(line, "Date: %s%s", dateLocal.Format(tsLayout), dt)
	}
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
	if len(lines) > scroll {
		for _, l := range lines[scroll:] {
			l = strings.TrimRight(l, "\r ")
			ov.screen.Printlnf(line, "%s", l)
			line++
			if line >= ov.screen.Height-2 {
				break
			}
		}
	} else {
		log.Errorf("Scroll too high! %d >= %d", scroll, len(lines))
	}
	ov.screen.Printlnf(ov.screen.Height-2, strings.Repeat("—", ov.screen.Width))
	return nil
}

func showError(oscreen *display.Screen, keys *input.Input, msg string) {
	log.Warningf("Displaying error to user: %q", msg)

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

func (ov *OpenMessageView) incrementalSearch(ctx context.Context, inlines []string) (int, error) {
	lines := make([]string, len(inlines))
	copy(lines, inlines)

	ov.inIncrementalSearch = true
	defer func() { ov.inIncrementalSearch = false }()
	ov.incrementalQuery = ""

	ov.Draw(lines, 0)
	ov.screen.Draw()

	found := 0
	start := 0
	for {
		var ok bool
		var key string
		select {
		case <-ctx.Done():
			return -1, ctx.Err()
		case key, ok = <-ov.keys.Chan():
			break
		}
		if !ok {
			return -1, fmt.Errorf("incremental search key read channel closed")
		}
		switch key {
		case input.CtrlC:
			return found, nil
		case input.CtrlU:
			ov.incrementalQuery = ""
		case input.CtrlS, input.CtrlN, input.Enter, input.Return:
			start = found + 1
		case input.CtrlH, input.Backspace:
			ov.incrementalQuery = dialog.TrimOneChar(ov.incrementalQuery)
		default:
			if isGraphicString(key) {
				ov.incrementalQuery += key
			}
		}
		const queryPrefix = "(?i)"
		re, err := regexp.Compile(queryPrefix + ov.incrementalQuery)
		if err != nil {
			var err2 error
			re, err2 = regexp.Compile(queryPrefix + regexp.QuoteMeta(ov.incrementalQuery))
			if err2 != nil {
				return -1, fmt.Errorf("can't happen: couldn't regexp compile %q or quotemeta'd %q: %v; %v", ov.incrementalQuery, regexp.QuoteMeta(ov.incrementalQuery), err, err2)
			}
		}

		found = -1
		for found == -1 {
			ov.incrementalCount = 0
			ov.incrementalCurrent = 0
			// Find from here
			for n, l := range lines {
				if m := re.FindAllStringSubmatchIndex(l, -1); len(m) > 0 {
					ov.incrementalCount++
					if n >= start && found == -1 {
						// Current hit.
						ov.incrementalCurrent = ov.incrementalCount
						found = n
						lines[n] = hilightIncremental(lines[n], m, display.Reverse+display.Yellow)
					} else {
						// Other hits that may be visible.
						lines[n] = hilightIncremental(lines[n], m, display.Reverse)
					}
				}
			}
			// Found.
			if found > 0 {
				break
			}

			// Not found; wrap.
			if start != 0 {
				start = 0
				continue
			}

			// Not found even after wrapping.
			found = 0
		}
		ov.Draw(lines, found)
		copy(lines, inlines)
		ov.screen.Draw()
	}
}

// Run runs the open message view event loop.
func (ov *OpenMessageView) Run(ctx context.Context) (*MessageViewOp, error) {
	log.Infof("Running OpenMessageView")
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
			s := scroll
			if err := initScreen(); err != nil {
				// Screen failed to init. Yeah it's time to bail.
				return nil, err
			}
			scroll = s
			go func() {
				ov.update <- struct{}{}
			}()
		case err := <-ov.errors:
			if err != nil {
				showError(ov.screen, ov.keys, err.Error())
				ov.screen.Draw()
			}
			continue
		case <-ov.update:
			log.Infof("Message arrived")
			gb := ov.msg.GetBody
			if ov.preferHTML {
				gb = ov.msg.GetBodyHTML
			}
			b, err := gb(ctx)
			if err != nil {
				ov.errors <- errors.Wrapf(err, "Getting message body")
			} else {
				lines = []string{}
				for _, l := range strings.Split(b, "\n") {
					if len(l) == 0 {
						lines = append(lines, "")
						continue
					}
					for len(l) > 0 {
						// TODO: break on runewidth
						// TODO: break on word boundary
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
					st := time.Now()
					if err := ov.msg.RemoveLabelID(ctx, cmdg.Unread); err != nil {
						ov.errors <- errors.Wrapf(err, "Failed to remove unread label")
					} else {
						log.Infof("Marked unread in %v", time.Since(st))
					}
				}
				// Does not need to be signaled to
				// messageview; label list gets
				// updated by RemoveLabelID.
			}()
			// Redraw could include fewer lines, because 'H' toggled HTML.
			ov.screen.Clear()

			// TODO: double check that scroll is not too high after `lines` was recreated.
			ov.Draw(lines, scroll)
		case key, ok := <-ov.keys.Chan():
			if !ok {
				log.Errorf("OpenMessage: Input channel closed!")
				continue
			}

			switch key {
			case input.CtrlR:
				go func() {
					if err := ov.msg.Reload(ctx, cmdg.LevelFull); err != nil {
						ov.errors <- errors.Wrap(err, "reloading message")
					}
					ov.update <- struct{}{}
				}()
			case "?", input.F1:
				help(openMessageViewHelp, ov.keys)
			case "*":
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
			case "l":
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
			case "L":
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
			case "u":
				return nil, nil
			case "q":
				return OpQuit(), nil
			case input.CtrlP:
				return OpPrev(), nil
			case input.CtrlN:
				return OpNext(), nil
			case "U":
				if err := ov.msg.AddLabelID(ctx, cmdg.Unread); err != nil {
					ov.errors <- fmt.Errorf("Failed to mark unread : %v", err)
				} else {
					return nil, nil
				}
			case input.Home:
				scroll = 0
				ov.Draw(lines, scroll)
			case "n", input.Down:
				ov.screen.UseCache()
				scroll = ov.scroll(ctx, len(lines), scroll, 1)
				ov.Draw(lines, scroll)
			case " ", input.CtrlV, input.PgDown:
				scroll = ov.scroll(ctx, len(lines), scroll, ov.screen.Height-10)
				ov.Draw(lines, scroll)
			case "p", input.Up:
				ov.screen.UseCache()
				scroll = ov.scroll(ctx, len(lines), scroll, -1)
				ov.Draw(lines, scroll)
			case "f":
				if err := forward(ctx, conn, ov.keys, ov.msg); err != nil {
					ov.errors <- fmt.Errorf("Failed to forward: %v", err)
				}
			case "r":
				if err := reply(ctx, conn, ov.keys, ov.msg); err != nil {
					ov.errors <- fmt.Errorf("Failed to reply: %v", err)
				}
			case "a":
				if err := replyAll(ctx, conn, ov.keys, ov.msg); err != nil {
					ov.errors <- fmt.Errorf("Failed to replyAll: %v", err)
				}
			case "H":
				ov.preferHTML = !ov.preferHTML
				scroll = 0
				go func() {
					ov.update <- struct{}{}
				}()
			case "e": // Archive
				if err := ov.msg.RemoveLabelID(ctx, cmdg.Inbox); err != nil {
					ov.errors <- fmt.Errorf("Failed to archive : %v", err)
				} else {
					return OpRemoveCurrent(nil), nil
				}
			case "s", input.CtrlS: // Search
				ns, err := ov.incrementalSearch(ctx, lines)
				if err != nil {
					return nil, err
				}
				if ns > 0 {
					scroll = ns
				}
				ov.Draw(lines, scroll)
			case "t": // Attachmments
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
			case "\\":
				if err := ov.showRaw(ctx); err != nil {
					ov.errors <- err
				}
			case "|":
				cmds, err := dialog.Entry("Command> ", ov.keys)
				if err == dialog.ErrAborted || cmds == "" {
					// User aborted; do nothing.
					break
				} else if err != nil {
					ov.errors <- errors.Wrap(err, "failed to get pipe command")
					break
				}
				cmd := exec.CommandContext(ctx, *shell, "-c", cmds)
				m, err := ov.msg.Raw(ctx)
				if err != nil {
					ov.errors <- errors.Wrap(err, "failed to get raw message")
					break
				}
				cmd.Stdin = strings.NewReader(m)
				var buf bytes.Buffer
				cmd.Stdout = &buf
				cmd.Stderr = &buf
				if err := cmd.Run(); err != nil {
					ov.errors <- errors.Wrapf(err, "failed run pipe command: %q", buf.String())
					break
				}
				ov.errors <- ov.showPager(ctx, buf.String())
			case input.Backspace, input.CtrlH, input.PgUp, "Meta-v":
				scroll = ov.scroll(ctx, len(lines), scroll, -(ov.screen.Height - 10))
				ov.Draw(lines, scroll)
			default:
				log.Infof("Unknown key: %q", key)
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
	return ov.showPager(ctx, m)
}

func (ov *OpenMessageView) showPager(ctx context.Context, content string) error {
	ov.keys.Stop()
	defer ov.keys.Start()

	cmd := exec.CommandContext(ctx, pagerBinary)
	cmd.Stdin = strings.NewReader(content)
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
