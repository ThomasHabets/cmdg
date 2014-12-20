// cmdg is a command line client to Gmail.
//
// TODO: copyright
//
// TODO: license
//
// The major reason for its existence is that the Gmail web UI is not
// friendly to proper quoting.
//
// Main benefits over Gmail web:
//   * Really fast. No browser, CSS or javascript getting in the way.
//   * Low bandwidth.
//   * Uses your EDITOR for composing (emacs keys, yay!)
//
// TODO features:
//   * Thread view (default: show only latest email in thread)
//   * Send all email asynchronously, with a local journal file for
//     when there are network issues.
//   * GPG integration.
//   * Attach file.
//   * Mark unread.
//   * ReplyAll
//   * Archive from message view.
//   * Make Goto work from message view.
//   * Inline help showing keyboard shortcuts.
//   * History API for refreshing (?).
//   * Remove labels
//   * Mailbox pagination
//   * Delayed sending.
//   * Continuing drafts.
//   * Surface allow modifying "important" and "starred".
//   * The Gmail API supports batch. Does the Go library?
//   * Loading animations to show it's not stuck.
package main

import (
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/mail"
	"os"
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	gmail "code.google.com/p/google-api-go-client/gmail/v1"
	"github.com/ThomasHabets/drive-du/lib"
	"github.com/jroimartin/gocui"
	termbox "github.com/nsf/termbox-go"
)

const (
	version = "0.0.1"
)

var (
	help          = flag.Bool("help", false, "Show usage text and exit.")
	help2         = flag.Bool("h", false, "Show usage text and exit.")
	config        = flag.String("config", "", "Config file. If empty will default to ~/cmdg.conf.")
	configure     = flag.Bool("configure", false, "Configure OAuth and write config file.")
	readonly      = flag.Bool("readonly", false, "When configuring, only acquire readonly permission.")
	editor        = flag.String("editor", "/usr/bin/emacs", "Default editor to use if EDITOR is not set.")
	replyRegex    = flag.String("reply_regexp", `^(Re|Sv|Aw|AW): `, "If subject matches, there's no need to add a Re: prefix.")
	replyPrefix   = flag.String("reply_prefix", "Re: ", "String to prepend to subject in replies.")
	forwardRegex  = flag.String("forward_regexp", `^(Fwd): `, "If subject matches, there's no need to add a Fwd: prefix.")
	forwardPrefix = flag.String("forward_prefix", "Fwd: ", "String to prepend to subject in forwards.")
	signature     = flag.String("signature", "Best regards", "End of all emails.")
	logFile       = flag.String("log", "/dev/null", "Log non-sensitive data to this file.")
	waitingLabel  = flag.String("waiting_label", "", "Label used for 'awaiting reply'. If empty disables feature.")

	gmailService *gmail.Service

	messagesView    *gocui.View
	openMessageView *gocui.View
	bottomView      *gocui.View
	sendView        *gocui.View
	ui              *gocui.Gui

	sendMessage string

	// State keepers.
	openMessageScrollY int
	currentLabel       = inbox
	currentSearch      = ""
	messages           *messageList
	labels             = make(map[string]string) // From name to ID.
	labelIDs           = make(map[string]string) // From ID to name.
	openMessage        *gmail.Message

	replyRE   *regexp.Regexp
	forwardRE *regexp.Regexp
)

const (
	scopeReadonly = "https://www.googleapis.com/auth/gmail.readonly"
	scopeModify   = "https://www.googleapis.com/auth/gmail.modify"
	accessType    = "offline"
	email         = "me"

	publicClientID     = "335469909366-qhq3697h8u20bgup89tc00fibh60upa0.apps.googleusercontent.com"
	publicClientSecret = "vCElmPsOx5Ed3mkvmDmO9FSg"

	vnMessages    = "messages"
	vnOpenMessage = "openMessage"
	vnBottom      = "bottom"
	vnSend        = "send"

	// Fixed labels.
	inbox     = "INBOX"
	unread    = "UNREAD"
	draft     = "DRAFT"
	important = "IMPORTANT"
	spam      = "SPAM"
	starred   = "STARRED"
	trash     = "TRASH"
	sent      = "SENT"

	letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ /"
	maxLine = 80
	spaces  = " \t\r"
)

type sortLabels []string

func (a sortLabels) Len() int      { return len(a) }
func (a sortLabels) Swap(i, j int) { a[i], a[j] = a[j], a[i] }
func (a sortLabels) Less(i, j int) bool {
	if a[i] == inbox && a[j] != inbox {
		return true
	}
	if a[j] == inbox && a[i] != inbox {
		return false
	}
	return strings.ToLower(a[i]) < strings.ToLower(a[j])
}

func getHeader(m *gmail.Message, header string) string {
	for _, h := range m.Payload.Headers {
		if h.Name == header {
			return h.Value
		}
	}
	return ""
}

func list(g *gmail.Service, label, search string) *messageList {
	_, nres := ui.Size()
	nres -= 2 + 3 // Bottom view and room for snippet.
	st := time.Now()
	q := g.Users.Messages.List(email).
		//		PageToken().
		MaxResults(int64(nres)).
		//Fields("messages(id,payload,snippet,raw,sizeEstimate),resultSizeEstimate").
		Fields("messages,resultSizeEstimate")
	if label != "" {
		q = q.LabelIds(label)
	}
	if search != "" {
		q = q.Q(search)
	}
	res, err := q.Do()
	if err != nil {
		log.Fatalf("Listing: %v", err)
	}
	fmt.Fprintf(messagesView, "Total size: %d\n", res.ResultSizeEstimate)
	p := parallel{}
	ret := &messageList{
		marked: make(map[string]bool),
	}
	var profile *gmail.Profile
	p.add(func(ch chan<- func()) {
		var err error
		st := time.Now()
		profile, err = g.Users.GetProfile(email).Do()
		if err != nil {
			log.Fatalf("Get profile: %v", err)
		}
		log.Printf("Users.GetProfile: %v", time.Since(st))
		close(ch)
	})
	for _, m := range res.Messages {
		m2 := m
		p.add(func(ch chan<- func()) {
			mres, err := g.Users.Messages.Get(email, m2.Id).Format("full").Do()
			if err != nil {
				log.Fatalf("Get message: %v", err)
			}
			ch <- func() {
				ret.messages = append(ret.messages, mres)
			}
		})
	}
	p.run()
	log.Printf("Listing: %v", time.Since(st))
	status("%s: Showing %d/%d. Total: %d emails, %d threads",
		profile.EmailAddress, len(res.Messages), res.ResultSizeEstimate, profile.MessagesTotal, profile.ThreadsTotal)
	return ret
}

func hasLabel(labels []string, needle string) bool {
	for _, l := range labels {
		if l == needle {
			return true
		}
	}
	return false
}

func parseTime(s string) (time.Time, error) {
	var t time.Time
	var err error
	for _, layout := range []string{
		"Mon, 2 Jan 2006 15:04:05 -0700",
		"Mon, 2 Jan 2006 15:04:05 -0700 (MST)",
		"Mon, 2 Jan 2006 15:04:05 MST",
		"2 Jan 2006 15:04:05 -0700",
		"Mon, 2 Jan 2006 15:04:05 -0700 (GMT-07:00)",
		"Mon, _2 Jan 2006 15:04:05 -0700 (GMT-07:00)",
		time.RFC1123Z,
	} {
		t, err = time.Parse(layout, s)
		if err == nil {
			break
		}
	}
	return t, err
}

func timestring(m *gmail.Message) string {
	s := getHeader(m, "Date")
	ts, err := parseTime(s)
	if err != nil {
		return "Unknown"
	}
	if time.Since(ts) > 365*24*time.Hour {
		return ts.Format("2006")
	}
	if !(time.Now().Month() == ts.Month() && time.Now().Day() == ts.Day()) {
		return ts.Format("Jan 02")
	}
	return ts.Format("15:04")
}

func fromString(m *gmail.Message) string {
	s := getHeader(m, "From")
	if hasLabel(m.LabelIds, sent) {
		s = getHeader(m, "To")
	}
	a, err := mail.ParseAddress(s)
	if err != nil {
		return s
	}
	if len(a.Name) > 0 {
		return a.Name
	}
	return a.Address
}

func getLabels(g *gmail.Service) {
	st := time.Now()
	res, err := g.Users.Labels.List(email).Do()
	if err != nil {
		log.Fatalf("listing labels: %v", err)
	} else {
		log.Printf("Users.Labels.List: %v", time.Since(st))
	}
	labels = make(map[string]string)
	labelIDs = make(map[string]string)
	for _, l := range res.Labels {
		labels[l.Name] = l.Id
		labelIDs[l.Id] = l.Name
	}
}

func refreshMessages(g *gmail.Service) {
	marked := make(map[string]bool)
	current := 0
	if messages != nil {
		current = messages.current
		marked = messages.marked
	}
	messages = list(g, currentLabel, currentSearch)
	if marked != nil {
		messages.current = current
		messages.marked = marked
		messages.fixCurrent()
	}
	messages.draw()
}

func quit(g *gocui.Gui, v *gocui.View) error {
	return gocui.ErrorQuit
}

func mimeDecode(s string) (string, error) {
	s = strings.Replace(s, "-", "+", -1)
	s = strings.Replace(s, "_", "/", -1)
	data, err := base64.StdEncoding.DecodeString(s)
	return string(data), err
}

func mimeEncode(s string) string {
	s = base64.StdEncoding.EncodeToString([]byte(s))
	s = strings.Replace(s, "+", "-", -1)
	s = strings.Replace(s, "/", "_", -1)
	return s
}

func status(s string, args ...interface{}) {
	bottomView.Clear()
	fmt.Fprintf(bottomView, s, args...)
}

func getBody(m *gmail.Message) string {
	if len(m.Payload.Parts) == 0 {
		data, err := mimeDecode(string(m.Payload.Body.Data))
		if err != nil {
			return fmt.Sprintf("TODO Content error: %v", err)
		}
		return data
	}
	for _, p := range m.Payload.Parts {
		if p.MimeType == "text/plain" {
			data, err := mimeDecode(p.Body.Data)
			if err != nil {
				return fmt.Sprintf("TODO Content error: %v", err)
			}
			return string(data)
		}
	}
	return "TODO Unknown data"
}

func messagesCmdRefresh(g *gocui.Gui, v *gocui.View) error {
	status("Refreshing...")
	messagesView.Clear()
	fmt.Fprintf(messagesView, "Loading...")
	g.Flush()
	refreshMessages(gmailService)
	return nil
}

func messagesCmdOpen(g *gocui.Gui, v *gocui.View) error {
	openMessageScrollY = 0
	openMessageDraw(g, v)
	return nil
}

func markCurrentMessage() {
	if messages.marked[messages.messages[messages.current].Id] {
		delete(messages.marked, messages.messages[messages.current].Id)
	} else {
		messages.marked[messages.messages[messages.current].Id] = true
	}
	status("%d messages marked", len(messages.marked))
}

func messagesCmdMark(g *gocui.Gui, v *gocui.View) error {
	markCurrentMessage()
	return messagesCmdNext(g, v)
}

func messagesCmdArchive(g *gocui.Gui, v *gocui.View) error {
	return messagesCmdApply(g, v, "archiving", func(id string) error {
		st := time.Now()
		_, err := gmailService.Users.Messages.Modify(email, id, &gmail.ModifyMessageRequest{
			RemoveLabelIds: []string{inbox},
		}).Do()
		if err == nil {
			log.Printf("Users.Messages.Modify(archive): %v", time.Since(st))
		}
		return err
	})
}

func messagesCmdApply(g *gocui.Gui, v *gocui.View, verb string, f func(string) error) error {
	status("%s emails, please wait...", verb)
	ui.Flush()
	p := parallel{}
	var errstr string
	var ok, fail int
	for _, m := range messages.messages {
		id := m.Id
		if !messages.marked[id] {
			continue
		}
		p.add(func(ch chan<- func()) {
			err := f(id)
			if err != nil {
				ch <- func() {
					errstr = fmt.Sprintf("Error %s %q: %v", verb, id, err)
					fail++
				}
			} else {
				ch <- func() {
					delete(messages.marked, id)
					ok++
				}
			}
		})
	}
	p.run()
	if fail > 0 {
		status("%d %s OK, %d failed: %s", ok, verb, fail, errstr)
	} else {
		messages.marked = make(map[string]bool)
		status("OK, %s %d messages", verb, ok)
	}
	refreshMessages(gmailService)
	messages.draw()
	return nil
}

func messagesCmdDelete(g *gocui.Gui, v *gocui.View) error {
	return messagesCmdApply(g, v, "trashing", func(id string) error {
		st := time.Now()
		_, err := gmailService.Users.Messages.Trash(email, id).Do()
		if err == nil {
			log.Printf("Users.Messages.Trash: %v", time.Since(st))
		}
		return err
	})
}

func openMessageCmdMark(g *gocui.Gui, v *gocui.View) error {
	markCurrentMessage()
	return openMessageCmdNext(g, v)
}

func prefixQuote(in []string) []string {
	var out []string
	for _, line := range in {
		if len(line) == 0 {
			out = append(out, ">")
		} else {
			out = append(out, "> "+line)
		}
	}
	return out
}

func breakLines(in []string) []string {
	var out []string
	for _, line := range in {
		line = strings.TrimRight(line, spaces)
		if len(line) > maxLine {
			for n := 0; len(line) > maxLine; n++ {
				out = append(out, strings.TrimRight(line[:maxLine], spaces))
				line = strings.TrimLeft(line[maxLine:], spaces)
			}
		}
		// TODO: There's probably an off-by-one here whe line is multiple of maxLine.
		out = append(out, line)
	}
	return out
}

func getReply() (string, error) {
	subject := getHeader(openMessage, "Subject")
	if !replyRE.MatchString(subject) {
		subject = *replyPrefix + subject
	}
	head := fmt.Sprintf("To: %s\nSubject: %s\n\nOn %s, %s said:\n",
		getHeader(openMessage, "From"),
		getHeader(openMessage, "Subject"),
		getHeader(openMessage, "Date"),
		getHeader(openMessage, "From"))
	return runEditorHeadersOK(head + strings.Join(prefixQuote(breakLines(strings.Split(getBody(openMessage), "\n"))), "\n"))
}

func getForward() (string, error) {
	subject := getHeader(openMessage, "Subject")
	if !forwardRE.MatchString(subject) {
		subject = *forwardPrefix + subject
	}
	head := fmt.Sprintf("To: %s\nSubject: \n\n--------- Forwarded message -----------\nDate: %s\nFrom: %s\nTo: %s\nSubject: %s\n\n",
		subject,
		getHeader(openMessage, "Date"),
		getHeader(openMessage, "From"),
		getHeader(openMessage, "To"),
		getHeader(openMessage, "Subject"),
	)
	return runEditorHeadersOK(head + strings.Join(breakLines(strings.Split(getBody(openMessage), "\n")), "\n"))
}

func runEditor(input string) (string, error) {
	f, err := ioutil.TempFile("", "cmdg-")
	if err != nil {
		return "", fmt.Errorf("creating tempfile: %v", err)
	}
	f.Close()
	defer os.Remove(f.Name())

	if err := ioutil.WriteFile(f.Name(), []byte(input), 0600); err != nil {
		return "", err
	}

	// Restore terminal for editors use.
	termbox.Close()
	defer func() {
		if err := termbox.Init(); err != nil {
			log.Fatalf("termbox failed to re-init: %v", err)
		}
	}()

	// Run editor.
	bin := *editor
	if e := os.Getenv("EDITOR"); len(e) > 0 {
		bin = e
	}
	cmd := exec.Command(bin, f.Name())
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to open editor %q: %v", bin, err)
	}

	// Read back reply.
	data, err := ioutil.ReadFile(f.Name())
	if err != nil {
		return "", fmt.Errorf("reading back editor output: %v", err)
	}
	return string(data), nil
}

func runEditorHeadersOK(input string) (string, error) {
	var s string
	for {
		var err error
		s, err = runEditor(input)
		if err != nil {
			status("Running editor failed: %v", err)
			return "", err
		}
		s2 := strings.SplitN(s, "\n\n", 2)
		if len(s2) != 2 {
			// TODO: Ask about reopening editor.
			status("Malformed email, reopening editor")
			input = s
			continue
		}
		break
	}
	return s, nil
}

func messagesCmdSearch(g *gocui.Gui, v *gocui.View) error {
	maxX, maxY := g.Size()

	height := len(labels) + 1
	if height > maxY-10 {
		height = maxY - 10
	}

	var err error
	x, y := 5, maxY/2-height/2

	// TODO: this appears to be a bug in gocui. Only works with unique view name.
	vnSearch := fmt.Sprintf("search-%v", time.Now())
	searchView, err := g.SetView(vnSearch, x, y, maxX-5, y+height)
	if err != gocui.ErrorUnkView {
		status("Failed to create search dialog: %v", err)
		return nil
	}

	s := &searchBox{
		g:        g,
		v:        searchView,
		viewName: vnSearch,
	}
	for _, li := range letters {
		l := translateKey(li)
		if err := ui.SetKeybinding(vnSearch, l, 0, func(g *gocui.Gui, v *gocui.View) error {
			s.keyPress(l)
			return nil
		}); err != nil {
			log.Fatalf("Bind %v for %q: %v", l, vnSearch, err)
		}
	}
	for key, cb := range map[interface{}]func(g *gocui.Gui, v *gocui.View) error{
		gocui.KeyBackspace:  func(g *gocui.Gui, v *gocui.View) error { s.keyPress(gocui.KeyBackspace); return nil },
		gocui.KeyBackspace2: func(g *gocui.Gui, v *gocui.View) error { s.keyPress(gocui.KeyBackspace); return nil },
		gocui.KeyCtrlU:      func(g *gocui.Gui, v *gocui.View) error { s.keyPress(gocui.KeyCtrlU); return nil },
		gocui.KeyCtrlM:      func(g *gocui.Gui, v *gocui.View) error { s.enter(); return nil },
		gocui.KeyCtrlJ:      func(g *gocui.Gui, v *gocui.View) error { s.enter(); return nil },
		'\n':                func(g *gocui.Gui, v *gocui.View) error { s.enter(); return nil },
		'\r':                func(g *gocui.Gui, v *gocui.View) error { s.enter(); return nil },
	} {
		if err := ui.SetKeybinding(vnSearch, key, 0, cb); err != nil {
			log.Fatalf("Bind %v for %q: %v", key, vnSearch, err)
		}
	}
	g.Flush()
	g.SetCurrentView(vnSearch)
	fmt.Fprintf(searchView, "Search for: ")
	return nil
}

func translateKey(l rune) interface{} {
	if l == ' ' {
		return gocui.KeySpace
	}
	return l
}

type searchBox struct {
	g        *gocui.Gui
	v        *gocui.View
	viewName string
	cur      string
}

func (s *searchBox) keyPress(l interface{}) {
	if l == gocui.KeyBackspace {
		if len(s.cur) > 0 {
			s.cur = s.cur[:len(s.cur)-1]
		}
	} else if l == gocui.KeyCtrlU {
		s.cur = ""
	} else {
		s.cur += fmt.Sprintf("%c", l)
	}
	s.v.Clear()
	fmt.Fprintf(s.v, "Search for: %s", s.cur)
}

func (s *searchBox) enter() {
	if s.cur != "" {
		currentSearch = s.cur
		currentLabel = ""
		backToMessagesView(s.g, s.v.Name(), true)
	} else {
		backToMessagesView(s.g, s.v.Name(), false)
	}
}

func restoreView(g *gocui.Gui, vn string, reload bool) {
	switch vn {
	case vnMessages:
		messages.current = 0
		if reload {
			messagesView.Clear()
			fmt.Fprintf(messagesView, "Loading...")
			g.Flush()
			refreshMessages(gmailService)
			messages.marked = make(map[string]bool)
		}
		messages.draw()
	}
	g.Flush()
	g.SetCurrentView(vn)
	g.Flush()
}

func backToMessagesView(g *gocui.Gui, vn string, reload bool) {
	messages.current = 0
	if err := g.DeleteView(vn); err != nil {
		log.Fatalf("Failed to delete %q: %v", vn, err)
	}
	g.Flush()
	g.SetCurrentView(vnMessages)
	if reload {
		messagesView.Clear()
		fmt.Fprintf(messagesView, "Loading...")
		g.Flush()
		refreshMessages(gmailService)
		messages.marked = make(map[string]bool)
	}
	messages.draw()
}

func messagesCmdGoto(g *gocui.Gui, v *gocui.View) error {
	ls := []string{}
	for l := range labels {
		ls = append(ls, l)
	}
	sort.Sort(sortLabels(ls))

	_, err := newLabelBox(g, v, "Go to label> ", ls, gotoBoxEnter)
	if err != nil {
		status("%v", err)
	}
	return nil
}

func openMessageCmdLabel(g *gocui.Gui, v *gocui.View) error {
	ls := []string{}
	for l := range labels {
		ls = append(ls, l)
	}
	sort.Sort(sortLabels(ls))

	_, err := newLabelBox(g, v, "Add label> ", ls, addLabelEnter)
	if err != nil {
		status("%v", err)
	}
	return nil
}

func messagesCmdLabel(g *gocui.Gui, v *gocui.View) error {
	ls := []string{}
	for l := range labels {
		ls = append(ls, l)
	}
	sort.Sort(sortLabels(ls))

	_, err := newLabelBox(g, v, "Add label> ", ls, messagesAddLabelEnter)
	if err != nil {
		status("%v", err)
	}
	return nil
}

type labelBox struct {
	g          *gocui.Gui
	labelView  *gocui.View
	parentView *gocui.View

	labels []string
	prompt string

	cur         string
	active      int
	forcePicker bool // Enable enter-picker even when nothing has been typed.

	enterCB func(g *gocui.Gui, v *gocui.View, choice string)
}

func newLabelBox(g *gocui.Gui, parentView *gocui.View, prompt string, ls []string, enterCB func(g *gocui.Gui, v *gocui.View, choice string)) (*labelBox, error) {
	maxX, maxY := g.Size()

	height := len(labels) + 1
	if height > maxY-10 {
		height = maxY - 10
	}

	x, y := 5, maxY/2-height/2
	// TODO: this appears to be a bug in gocui. Only works with unique view name.
	vnLabel := fmt.Sprintf("label-%v", time.Now())
	labelView, err := g.SetView(vnLabel, x, y, maxX-5, y+height)
	if err != gocui.ErrorUnkView {
		return nil, fmt.Errorf("failed to create dialog: %v", err)
	}

	s := &labelBox{
		g:          g,
		labelView:  labelView,
		parentView: parentView,
		labels:     ls,
		prompt:     prompt,
		enterCB:    enterCB,
	}
	// Normal keystrokes identity mapped.
	for _, li := range letters {
		l := translateKey(li)
		if err := ui.SetKeybinding(vnLabel, l, 0, func(g *gocui.Gui, v *gocui.View) error {
			s.keyPress(l)
			return nil
		}); err != nil {
			log.Fatalf("Bind %v for %q: %v", l, vnLabel, err)
		}
	}

	// Control keys identity mapped.
	for _, key := range []interface{}{
		gocui.KeyBackspace,
		gocui.KeyBackspace2,
		gocui.KeyCtrlU,
		gocui.KeyCtrlN,
		gocui.KeyCtrlP,
		gocui.KeyArrowDown,
		gocui.KeyArrowUp,
	} {
		k := key
		cb := func(g *gocui.Gui, v *gocui.View) error { s.keyPress(k); return nil }
		if err := ui.SetKeybinding(vnLabel, key, 0, cb); err != nil {
			log.Fatalf("Bind %v for %q: %v", key, vnLabel, err)
		}
	}

	// Go!
	for key, cb := range map[interface{}]func(g *gocui.Gui, v *gocui.View) error{
		gocui.KeyCtrlM: func(g *gocui.Gui, v *gocui.View) error { s.enter(); return nil },
		gocui.KeyCtrlJ: func(g *gocui.Gui, v *gocui.View) error { s.enter(); return nil },
		'\n':           func(g *gocui.Gui, v *gocui.View) error { s.enter(); return nil },
		'\r':           func(g *gocui.Gui, v *gocui.View) error { s.enter(); return nil },
	} {
		if err := ui.SetKeybinding(vnLabel, key, 0, cb); err != nil {
			log.Fatalf("Bind %v for %q: %v", key, vnLabel, err)
		}
	}
	g.Flush()
	g.SetCurrentView(vnLabel)
	s.keyPress(gocui.KeyCtrlU)
	return s, nil
}

func (s *labelBox) enter() {
	matching := matchingLabels(s.labels, s.cur)

	cur := s.cur
	// If forcing picker (empty string, but ^N pressed)
	if s.forcePicker {
		cur = matching[s.active]
	}
	// If invalid label, use whatever's selected.
	if _, found := labels[cur]; !found {
		if s.active < len(matching) {
			n := matching[s.active]
			if _, found := labels[n]; found {
				cur = n
			}
		}
	}

	if s.cur == "" && !s.forcePicker {
		cur = ""
	}
	log.Printf("Pressed enter on %p", s)
	s.enterCB(s.g, s.parentView, cur)
	if err := s.g.DeleteView(s.labelView.Name()); err != nil {
		log.Fatalf("Failed to delete label view %q: %v", s.labelView.Name(), err)
	}
}

func (s *labelBox) keyPress(l interface{}) {
	switch l {
	case gocui.KeyBackspace, gocui.KeyBackspace2:
		if len(s.cur) > 0 {
			s.cur = s.cur[:len(s.cur)-1]
		}
		s.forcePicker = false
	case gocui.KeyArrowDown, gocui.KeyCtrlN:
		if s.forcePicker || s.cur != "" {
			s.active++
		}
		s.forcePicker = true
	case gocui.KeyArrowUp, gocui.KeyCtrlP:
		s.active--
		s.forcePicker = true
	case gocui.KeyCtrlU:
		s.cur = ""
		s.forcePicker = false
	default:
		s.cur += fmt.Sprintf("%c", l)
		s.forcePicker = false
	}

	// Figure out what the maximum 'active' counter is.
	matching := matchingLabels(s.labels, s.cur)
	if s.active >= len(matching) {
		s.active = len(matching) - 1
	}

	_, height := s.labelView.Size()
	if s.active >= height {
		s.active = height - 1
	}
	if s.active < 0 {
		s.active = 0
	}

	s.labelView.Clear()
	fmt.Fprintf(s.labelView, "%s%s", s.prompt, s.cur)

	// Print matching labels.
	fmt.Fprintf(s.labelView, "\n")
	lineNum := 0
	a := s.active
	for _, l := range matching {
		if lineNum > height-3 {
			break
		}
		if a == 0 && (s.forcePicker || s.cur != "") {
			fmt.Fprintf(s.labelView, "> %s\n", l)
		} else {
			fmt.Fprintf(s.labelView, "  %s\n", l)
		}
		a--
		lineNum++
	}
}

func matchingLabels(labels []string, label string) []string {
	ret := []string{}
	for _, l := range labels {
		if strings.Contains(strings.ToLower(l), strings.ToLower(label)) {
			ret = append(ret, l)
		}
	}
	return ret
}

// add label to the open message.
func addLabelEnter(g *gocui.Gui, parentView *gocui.View, choice string) {
	change := true
	defer restoreView(g, parentView.Name(), change)

	if choice == "" {
		return
	}

	id, ok := labels[choice]
	if !ok {
		status("Label %q doesn't exist", choice)
		return
	}

	if _, err := gmailService.Users.Messages.Modify(email, openMessage.Id, &gmail.ModifyMessageRequest{
		AddLabelIds: []string{id},
	}).Do(); err != nil {
		status("Failed to apply label %q: %v", choice, err)
	}
}

// add label to the marked messages.
func messagesAddLabelEnter(g *gocui.Gui, parentView *gocui.View, choice string) {
	change := true
	defer restoreView(g, parentView.Name(), change)

	if choice == "" {
		return
	}

	labelID, ok := labels[choice]
	if !ok {
		status("Label %q doesn't exist", choice)
		return
	}

	messagesCmdApply(g, parentView, "labelling", func(id string) error {
		st := time.Now()
		_, err := gmailService.Users.Messages.Modify(email, id, &gmail.ModifyMessageRequest{
			AddLabelIds: []string{labelID},
		}).Do()
		if err == nil {
			log.Printf("Users.Messages.Modify(labelling): %v", time.Since(st))
		}
		return err
	})
}

func gotoBoxEnter(g *gocui.Gui, v *gocui.View, choice string) {
	if choice == "" {
		backToMessagesView(g, v.Name(), false)
		return
	}

	change := false

	id, ok := labels[choice]
	if ok {
		change = true
		currentLabel = id
		currentSearch = ""
	} else {
		status("Label %q doesn't exist", choice)
	}
	backToMessagesView(g, v.Name(), change)
}

func messagesCmdCompose(g *gocui.Gui, v *gocui.View) error {
	status("Running editor")
	input := "To: \nSubject: \n\n" + *signature
	var err error
	sendMessage, err = runEditorHeadersOK(input)
	if err != nil {
		status("Running editor: %v", err)
		return nil
	}
	createSend(g)
	return nil
}

func sendCmdSend(g *gocui.Gui, v *gocui.View) error {
	st := time.Now()
	if _, err := gmailService.Users.Messages.Send(email, &gmail.Message{Raw: mimeEncode(sendMessage)}).Do(); err != nil {
		status("Error sending: %v", err)
		return nil
	}
	log.Printf("Users.Messages.Send: %v", time.Since(st))
	g.DeleteView(vnSend)
	g.Flush()
	g.SetCurrentView(vnMessages)
	status("Successfully sent")
	return nil
}

func sendCmdSendWait(g *gocui.Gui, v *gocui.View) error {
	st := time.Now()
	l, ok := labels[*waitingLabel]
	if !ok {
		log.Fatalf("Waiting label %q does not exist!", *waitingLabel)
	}

	status("Sending with label...")
	if msg, err := gmailService.Users.Messages.Send(email, &gmail.Message{
		Raw: mimeEncode(sendMessage),
	}).Do(); err != nil {
		status("Error sending: %v", err)
	} else {
		if _, err := gmailService.Users.Messages.Modify(email, msg.Id, &gmail.ModifyMessageRequest{
			AddLabelIds: []string{l},
		}).Do(); err != nil {
			status("Error labelling: %v", err)
			log.Printf("Error labelling: %v", err)
		} else {
			status("Successfully sent (with waiting label %q)", l)
			log.Printf("Users.Messages.Send+Add waiting: %v", time.Since(st))
		}
	}
	g.DeleteView(vnSend)
	g.Flush()
	g.SetCurrentView(vnMessages)
	return nil
}

func sendCmdAbort(g *gocui.Gui, v *gocui.View) error {
	if err := g.SetCurrentView(vnMessages); err != nil {
		log.Fatalf("Failed to set current view to %q: %v", vnMessages, err)
	}
	g.Flush()
	g.DeleteView(vnSend)
	status("Aborted send")
	return nil
}

func sendCmdDraft(g *gocui.Gui, v *gocui.View) error {
	st := time.Now()
	if _, err := gmailService.Users.Drafts.Create(email, &gmail.Draft{
		Message: &gmail.Message{Raw: mimeEncode(sendMessage)},
	}).Do(); err != nil {
		status("Error saving as draft: %v", err)
		// TODO: data loss!
		return nil
	}
	log.Printf("Users.Drafts.Create: %v", time.Since(st))
	g.SetCurrentView(vnMessages)
	g.Flush()
	g.DeleteView(vnSend)
	status("Saved as draft")
	return nil
}

func openMessageCmdReply(g *gocui.Gui, v *gocui.View) error {
	status("Composing reply")
	var err error
	sendMessage, err = getReply()
	g.Flush()
	if err != nil {
		status("Error creating reply: %v", err)
		return nil
	}
	openMessage = nil
	g.SetCurrentView(vnMessages)
	messages.draw()
	createSend(g)
	return nil
}

func openMessageCmdForward(g *gocui.Gui, v *gocui.View) error {
	status("Composing forwarded email")
	var err error
	sendMessage, err = getForward()
	g.Flush()
	if err != nil {
		status("Error creating forwarded email: %v", err)
		return nil
	}
	openMessage = nil
	g.SetCurrentView(vnMessages)
	messages.draw()
	createSend(g)
	return nil
}

func createSend(g *gocui.Gui) {
	maxX, maxY := g.Size()
	height := 7
	width := 40
	x, y := maxX/2-width/2, maxY/2-height/2
	sendView, err := g.SetView(vnSend, x, y, x+width, y+height)
	if err != gocui.ErrorUnkView {
		status("Failed to create dialog: %v", err)
		return
	}
	// TODO: move to template.
	s := "\n S - Send\n"
	if *waitingLabel != "" {
		s += " W - Send and apply waiting label\n"
	}
	fmt.Fprintf(sendView, s+" D - Draft\n A - Abort")
	for key, cb := range map[interface{}]func(g *gocui.Gui, v *gocui.View) error{
		's': sendCmdSend,
		'S': sendCmdSend,
		'w': sendCmdSendWait,
		'W': sendCmdSendWait,
		'a': sendCmdAbort,
		'A': sendCmdAbort,
		'd': sendCmdDraft,
		'D': sendCmdDraft,
	} {
		if err := ui.SetKeybinding(vnSend, key, 0, cb); err != nil {
			log.Fatalf("Bind %v for %q: %v", key, vnSend, err)
		}
	}
	g.Flush()
	g.SetCurrentView(vnSend)
}

func openMessageDraw(g *gocui.Gui, v *gocui.View) {
	openMessage = messages.messages[messages.current]
	go func() {
		if !hasLabel(openMessage.LabelIds, unread) {
			return
		}
		id := openMessage.Id
		st := time.Now()
		_, err := gmailService.Users.Messages.Modify(email, id, &gmail.ModifyMessageRequest{
			RemoveLabelIds: []string{unread},
		}).Do()
		if err != nil {
			// TODO: log to file or something.
		} else {
			log.Printf("Users.Messages.Modify(remove unread): %v", time.Since(st))
		}
	}()

	g.Flush()
	openMessageView.Clear()
	w, h := openMessageView.Size()

	bodyLines := breakLines(strings.Split(getBody(openMessage), "\n"))
	maxScroll := len(bodyLines) - h + 10
	if openMessageScrollY > maxScroll {
		openMessageScrollY = maxScroll
	}
	if openMessageScrollY < 0 {
		openMessageScrollY = 0
	}
	bodyLines = bodyLines[openMessageScrollY:]
	body := strings.Join(bodyLines, "\n")

	marked := ""
	if messages.marked[openMessage.Id] {
		marked = ", MARKED"
	}

	ls := []string{}
	for _, l := range openMessage.LabelIds {
		ls = append(ls, labelIDs[l])
	}
	sort.Sort(sortLabels(ls))

	fmt.Fprintf(openMessageView, "Email %d of %d%s", messages.current+1, len(messages.messages), marked)
	fmt.Fprintf(openMessageView, "From: %s", getHeader(openMessage, "From"))
	fmt.Fprintf(openMessageView, "To: %s", getHeader(openMessage, "To"))
	fmt.Fprintf(openMessageView, "Date: %s", getHeader(openMessage, "Date"))
	fmt.Fprintf(openMessageView, "Subject: %s", getHeader(openMessage, "Subject"))
	fmt.Fprintf(openMessageView, "Labels: %s", strings.Join(ls, ", "))
	fmt.Fprintf(openMessageView, strings.Repeat("-", w))
	fmt.Fprintf(openMessageView, "%s", body)
	//fmt.Fprintf(openMessageView, "%+v", *openMessage.Payload)

	if len(openMessage.Payload.Parts) > 0 {
		fmt.Fprintf(openMessageView, strings.Repeat("-", w))
	}
	for _, p := range openMessage.Payload.Parts {
		fmt.Fprintf(openMessageView, "Part of type %s", p.MimeType)
		fmt.Fprintf(openMessageView, "  Filename: %q", len(p.Filename))
		fmt.Fprintf(openMessageView, "  Subparts: %d", len(p.Parts))
	}
	g.SetCurrentView(vnOpenMessage)
}

func openMessageCmdPrev(g *gocui.Gui, v *gocui.View) error {
	openMessageScrollY = 0
	messages.cmdPrev()
	openMessageDraw(g, v)
	return nil
}
func openMessageCmdNext(g *gocui.Gui, v *gocui.View) error {
	openMessageScrollY = 0
	messages.cmdNext()
	openMessageDraw(g, v)
	return nil
}
func openMessageCmdPageDown(g *gocui.Gui, v *gocui.View) error {
	_, h := openMessageView.Size()
	openMessageScrollY += h
	openMessageDraw(g, v)
	return nil
}
func openMessageCmdScrollDown(g *gocui.Gui, v *gocui.View) error {
	openMessageScrollY += 2
	openMessageDraw(g, v)
	return nil
}
func openMessageCmdPageUp(g *gocui.Gui, v *gocui.View) error {
	_, h := openMessageView.Size()
	openMessageScrollY -= h
	openMessageDraw(g, v)
	return nil
}
func openMessageCmdScrollUp(g *gocui.Gui, v *gocui.View) error {
	openMessageScrollY -= 2
	openMessageDraw(g, v)
	return nil
}

func openMessageCmdClose(g *gocui.Gui, v *gocui.View) error {
	openMessage = nil
	g.SetCurrentView(vnMessages)
	messages.draw()
	return nil
}

func messagesCmdNext(g *gocui.Gui, v *gocui.View) error {
	messages.cmdNext()
	messages.draw()
	return nil
}

func messagesCmdPrev(g *gocui.Gui, v *gocui.View) error {
	messages.cmdPrev()
	messages.draw()
	return nil
}

func messagesCmdDetails(g *gocui.Gui, v *gocui.View) error {
	messages.cmdDetails()
	messages.draw()
	return nil
}

func layout(g *gocui.Gui) error {
	maxX, maxY := g.Size()
	_ = maxY
	var err error
	create := false
	if messagesView == nil {
		create = true
	}
	messagesView, err = g.SetView(vnMessages, -1, -1, maxX, maxY-2)
	if err != nil {
		if create != (err == gocui.ErrorUnkView) {
			return err
		}
	}
	bottomView, err = g.SetView(vnBottom, -1, maxY-2, maxX, maxY)
	if err != nil {
		if create != (err == gocui.ErrorUnkView) {
			return err
		}
	}
	if openMessage == nil {
		ui.DeleteView(vnOpenMessage)
	} else {
		openMessageView, err = ui.SetView(vnOpenMessage, -1, -1, maxX, maxY-2)
		if err != nil {
			return err
		}
	}
	if create {
		fmt.Fprintf(messagesView, "Loading...")
		status("cmdg")
	}
	return nil
}

func usage(f io.Writer) {
	fmt.Fprintf(f, `cmdg version %s - Command line interface to Gmail
https://github.com/ThomasHabets/cmdg/

Usage: %s [...options...]

`, version, os.Args[0])

	flag.VisitAll(func(fl *flag.Flag) {
		fmt.Fprintf(f, "  %15s  %s\n%sDefault: %q\n", "-"+fl.Name, fl.Usage, strings.Repeat(" ", 19), fl.DefValue)
	})
}

func main() {
	flag.Usage = func() { usage(os.Stderr) }
	flag.Parse()
	if flag.NArg() > 0 {
		log.Fatalf("Non-argument options provided: %q", flag.Args())
	}
	if *help || *help2 {
		usage(os.Stdout)
		return
	}

	var err error
	if replyRE, err = regexp.Compile(*replyRegex); err != nil {
		log.Fatalf("-reply_regexp %q is not a valid regex: %v", *replyRegex, err)
	}
	if forwardRE, err = regexp.Compile(*forwardRegex); err != nil {
		log.Fatalf("-forward_regexp %q is not a valid regex: %v", *forwardRegex, err)
	}
	if *config == "" {
		*config = path.Join(os.Getenv("HOME"), ".cmdg.conf")
	}

	scope := scopeModify
	if *readonly {
		scope = scopeReadonly
	}
	if *configure {
		if err := lib.ConfigureWriteSharedSecrets(scope, accessType, *config, publicClientID, publicClientSecret); err != nil {
			log.Fatalf("Failed to config: %v", err)
		}
		return
	}
	if fi, err := os.Stat(*config); err != nil {
		log.Fatalf("Missing config file %q: %v", *config, err)
	} else if (fi.Mode() & 0477) != 0400 {
		log.Fatalf("Config file (%q) permissions must be 0600 or better, was 0%o", *config, fi.Mode()&os.ModePerm)
	}

	conf, err := lib.ReadConfig(*config)
	if err != nil {
		log.Fatalf("Failed to read config: %v", err)
	}

	t, err := lib.Connect(conf.OAuth, scope, accessType)
	if err != nil {
		log.Fatalf("Failed to connect to gmail: %v", err)
	}
	g, err := gmail.New(t.Client())
	if err != nil {
		log.Fatalf("Failed to create gmail client: %v", err)
	}

	{
		f, err := os.OpenFile(*logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			log.Fatalf("Can't create logfile %q: %v", *logFile, err)
		}
		defer f.Close()
		log.SetOutput(f)
		log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	}

	ui = gocui.NewGui()
	if err := ui.Init(); err != nil {
		log.Panicln(err)
	}
	defer ui.Close()
	ui.SetLayout(layout)

	// Global keys.
	for key, cb := range map[interface{}]func(g *gocui.Gui, v *gocui.View) error{
		gocui.KeyCtrlC: quit,
	} {
		if err := ui.SetKeybinding("", key, 0, cb); err != nil {
			log.Fatalf("Bind %v: %v", key, err)
		}
	}

	// Message list keys.
	for key, cb := range map[interface{}]func(g *gocui.Gui, v *gocui.View) error{
		'q':                 quit,
		gocui.KeyTab:        messagesCmdDetails,
		gocui.KeyArrowUp:    messagesCmdPrev,
		gocui.KeyCtrlP:      messagesCmdPrev,
		'p':                 messagesCmdPrev,
		gocui.KeyArrowDown:  messagesCmdNext,
		gocui.KeyCtrlN:      messagesCmdNext,
		'n':                 messagesCmdNext,
		gocui.KeyCtrlR:      messagesCmdRefresh,
		'r':                 messagesCmdRefresh,
		'x':                 messagesCmdMark,
		gocui.KeyArrowRight: messagesCmdOpen,
		'\n':                messagesCmdOpen,
		'\r':                messagesCmdOpen,
		gocui.KeyCtrlM:      messagesCmdOpen,
		gocui.KeyCtrlJ:      messagesCmdOpen,
		'>':                 messagesCmdOpen,
		'd':                 messagesCmdDelete,
		'a':                 messagesCmdArchive,
		'e':                 messagesCmdArchive,
		'c':                 messagesCmdCompose,
		'g':                 messagesCmdGoto,
		'l':                 messagesCmdLabel,
		's':                 messagesCmdSearch,
	} {
		if err := ui.SetKeybinding(vnMessages, key, 0, cb); err != nil {
			log.Fatalf("Bind %v: %v", key, err)
		}
	}

	// Open message read.
	for key, cb := range map[interface{}]func(g *gocui.Gui, v *gocui.View) error{
		'q':                 quit,
		'<':                 openMessageCmdClose,
		gocui.KeyArrowLeft:  openMessageCmdClose,
		gocui.KeyEsc:        openMessageCmdClose,
		'p':                 openMessageCmdScrollUp,
		gocui.KeyArrowUp:    openMessageCmdScrollUp,
		'n':                 openMessageCmdScrollDown,
		'l':                 openMessageCmdLabel,
		gocui.KeyArrowDown:  openMessageCmdScrollDown,
		'x':                 openMessageCmdMark,
		'r':                 openMessageCmdReply,
		'f':                 openMessageCmdForward,
		gocui.KeyCtrlP:      openMessageCmdPrev,
		gocui.KeyCtrlN:      openMessageCmdNext,
		gocui.KeySpace:      openMessageCmdPageDown,
		gocui.KeyPgdn:       openMessageCmdPageDown,
		gocui.KeyBackspace:  openMessageCmdPageUp,
		gocui.KeyBackspace2: openMessageCmdPageUp,
		gocui.KeyPgup:       openMessageCmdPageUp,
	} {
		if err := ui.SetKeybinding(vnOpenMessage, key, 0, cb); err != nil {
			log.Fatalf("Bind %v: %v", key, err)
		}
	}
	ui.Flush()
	ui.SetCurrentView(vnMessages)
	refreshMessages(g)
	getLabels(g)
	gmailService = g
	err = ui.MainLoop()
	if err != nil && err != gocui.ErrorQuit {
		log.Panicln(err)
	}
}
