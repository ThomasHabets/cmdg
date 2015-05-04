// cmdg is a command line client to Gmail.
//
// The major reason for its existence is that the Gmail web UI is not
// friendly to proper quoting.
//
// Main benefits over Gmail web:
//   * Really fast. No browser, CSS or javascript getting in the way.
//   * Low bandwidth.
//   * Uses your EDITOR for composing (emacs keys, yay!)
//
// TODO features (in rough prio order):
//   * Unicode character support.
//   * Smoother scrolling of messages.
//   * Contacts
//   * Attach file.
//   * Mailbox pagination
//   * Thread view (default: show only latest email in thread)
//   * Periodic message view refresh.
//   * Send all email asynchronously, with a local journal file for
//     when there are network issues.
//   * GPG sign.
//   * If sending fails, optionally re-open.
//   * GPG encrypt.
//   * GPG decrypt.
//   * Make Goto work from message view.
//   * History API for refreshing (?).
//   * Delayed sending.
//   * Continuing drafts.
//   * Special shortcuts for labelling 'important', 'starred' and 'unread'.
//   * The Gmail API supports batch. Does the Go library?
//   * Loading animations to show it's not stuck.
//   * In-memory cache for messages (all but labels for messages is immutable)
//   * On disk (encrypted) cache for messages.
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
package main

import (
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/ThomasHabets/cmdg/cmdglib"
	"github.com/ThomasHabets/cmdg/ncwrap"
	"github.com/ThomasHabets/drive-du/lib"
	gc "github.com/rthornton128/goncurses"
	gmail "google.golang.org/api/gmail/v1"
)

const (
	version = "0.0.2"
)

var (
	license       = flag.Bool("license", false, "Show program license.")
	help          = flag.Bool("help", false, "Show usage text and exit.")
	help2         = flag.Bool("h", false, "Show usage text and exit.")
	config        = flag.String("config", "", "Config file. If empty will default to ~/cmdg.conf.")
	configure     = flag.Bool("configure", false, "Configure OAuth and write config file.")
	readonly      = flag.Bool("readonly", false, "When configuring, only acquire readonly permission.")
	editor        = flag.String("editor", "/usr/bin/emacs", "Default editor to use if EDITOR is not set.")
	gpg           = flag.String("gpg", "/usr/bin/gpg", "Path to GnuPG.")
	replyRegex    = flag.String("reply_regexp", `(?i)^(Re|Sv|Aw|AW): `, "If subject matches, there's no need to add a Re: prefix.")
	replyPrefix   = flag.String("reply_prefix", "Re: ", "String to prepend to subject in replies.")
	forwardRegex  = flag.String("forward_regexp", `(?i)^(Fwd): `, "If subject matches, there's no need to add a Fwd: prefix.")
	forwardPrefix = flag.String("forward_prefix", "Fwd: ", "String to prepend to subject in forwards.")
	signature     = flag.String("signature", "Best regards", "End of all emails.")
	logFile       = flag.String("log", "/dev/null", "Log non-sensitive data to this file.")
	waitingLabel  = flag.String("waiting_label", "", "Label used for 'awaiting reply'. If empty disables feature.")
	threadView    = flag.Bool("thread", false, "Use thread view.")

	gmailService *gmail.Service

	nc *ncwrap.NCWrap

	// State keepers.
	labels       = make(map[string]string) // From name to ID.
	labelIDs     = make(map[string]string) // From ID to name.
	emailAddress string

	replyRE   *regexp.Regexp
	forwardRE *regexp.Regexp
)

const (
	scopeReadonly = "https://www.googleapis.com/auth/gmail.readonly"
	scopeModify   = "https://www.googleapis.com/auth/gmail.modify"
	accessType    = "offline"
	email         = "me"

	// TODO: Public client IDs are for some reason not allowed by the ToS for Open Source.
	publicClientID     = ""
	publicClientSecret = ""

	maxLine = 80
	spaces  = " \t\r"
)

type sortLabels []string

func (a sortLabels) Len() int      { return len(a) }
func (a sortLabels) Swap(i, j int) { a[i], a[j] = a[j], a[i] }
func (a sortLabels) Less(i, j int) bool {
	if a[i] == cmdglib.Inbox && a[j] != cmdglib.Inbox {
		return true
	}
	if a[j] == cmdglib.Inbox && a[i] != cmdglib.Inbox {
		return false
	}
	return strings.ToLower(a[i]) < strings.ToLower(a[j])
}

func list(label, search string) ([]listEntry, <-chan listEntry) {
	log.Printf("listing %q %q", label, search)
	nres := 100
	nres -= 2 + 3 // Bottom view and room for snippet.

	syncP := parallel{} // Run the parts that can't wait in parallel.

	// List messages.
	var res *gmail.ListMessagesResponse
	syncP.add(func(ch chan<- func()) {
		defer close(ch)
		var err error
		st := time.Now()
		q := gmailService.Users.Messages.List(email).
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
		res, err = q.Do()
		if err != nil {
			log.Fatalf("Listing: %v", err)
		}
		log.Printf("Listing: %v", time.Since(st))
	})

	var profile *gmail.Profile
	syncP.add(func(ch chan<- func()) {
		defer close(ch)
		st := time.Now()
		var err error
		profile, err = gmailService.Users.GetProfile(email).Do()
		if err != nil {
			log.Fatalf("Get profile: %v", err)
		}
		log.Printf("Users.GetProfile: %v", time.Since(st))
	})
	syncP.run()

	nc.Status("Total number of messages in folder: %d\n", res.ResultSizeEstimate)

	msgChan := make(chan listEntry)
	// Load messages async.
	{
		for _, m := range res.Messages {
			m2 := m
			go func() {
				st := time.Now()
				for {
					mres, err := gmailService.Users.Messages.Get(email, m2.Id).Format("full").Do()
					if err != nil {
						log.Printf("Get message failed, retrying: %v", err)
						continue
					}
					log.Printf("Users.Messages.Get: %v", time.Since(st))
					msgChan <- listEntry{
						msg: mres,
					}
					return
				}
			}()
		}
	}
	nc.Status("%s: Showing %d/%d. Total: %d emails, %d threads",
		profile.EmailAddress, len(res.Messages), res.ResultSizeEstimate, profile.MessagesTotal, profile.ThreadsTotal)
	emailAddress = profile.EmailAddress
	ret := []listEntry{}
	for _, m := range res.Messages {
		ret = append(ret, listEntry{
			msg: m,
		})
	}
	return ret, msgChan
}

func listThreads(label, search string) ([]listEntry, <-chan listEntry) {
	nres := 100
	nres -= 2 + 3 // Bottom view and room for snippet.

	syncP := parallel{} // Run the parts that can't wait in parallel.

	// List messages.
	var res *gmail.ListThreadsResponse
	syncP.add(func(ch chan<- func()) {
		defer close(ch)
		var err error
		st := time.Now()
		q := gmailService.Users.Threads.List(email).
			//		PageToken().
			MaxResults(int64(nres)).
			//Fields("messages(id,payload,snippet,raw,sizeEstimate),resultSizeEstimate").
			Fields("threads,resultSizeEstimate")
		if label != "" {
			q = q.LabelIds(label)
		}
		if search != "" {
			q = q.Q(search)
		}
		res, err = q.Do()
		if err != nil {
			log.Fatalf("Listing threads: %v", err)
		}
		log.Printf("Listing threads: %v", time.Since(st))
	})

	var profile *gmail.Profile
	syncP.add(func(ch chan<- func()) {
		defer close(ch)
		st := time.Now()
		var err error
		profile, err = gmailService.Users.GetProfile(email).Do()
		if err != nil {
			log.Fatalf("Get profile: %v", err)
		}
		log.Printf("Users.GetProfile: %v", time.Since(st))
	})
	syncP.run()

	nc.Status("Total number of threads in folder: %d\n", res.ResultSizeEstimate)

	msgChan := make(chan listEntry)
	// Load messages async.
	{
		for _, m := range res.Threads {
			m2 := m
			go func() {
				st := time.Now()
				for {
					mres, err := gmailService.Users.Threads.Get(email, m2.Id).Format("full").Do()
					if err != nil {
						log.Printf("Get thread failed, retrying: %v", err)
						continue
					}
					log.Printf("Users.Thread.Get: %v", time.Since(st))
					msgChan <- listEntry{
						thread: mres,
					}
					return
				}
			}()
		}
	}
	nc.Status("%s: Showing %d/%d. Total: %d emails, %d threads",
		profile.EmailAddress, len(res.Threads), res.ResultSizeEstimate, profile.MessagesTotal, profile.ThreadsTotal)
	ret := []listEntry{}
	for _, m := range res.Threads {
		log.Printf("Thread: %+v", m)
		ret = append(ret, listEntry{
			thread: m,
		})
	}
	return ret, msgChan
}

func getLabels() {
	st := time.Now()
	res, err := gmailService.Users.Labels.List(email).Do()
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

// Find plaintext body among all attachments.
func getBodyRecurse(m *gmail.MessagePart) string {
	if len(m.Parts) == 0 {
		data, err := mimeDecode(string(m.Body.Data))
		if err != nil {
			return fmt.Sprintf("TODO content error: %v", err)
		}
		return data
	}
	body := ""
	for _, p := range m.Parts {
		if p.MimeType == "text/plain" {
			data, err := mimeDecode(p.Body.Data)
			if err != nil {
				return fmt.Sprintf("TODO Content error: %v", err)
			}
			body += string(data)
		} else if p.MimeType == "text/html" {
			// Skip.
		} else if p.MimeType == "multipart/alternative" {
			body += getBodyRecurse(p)
		} else {
			// Skip.
			log.Printf("Unknown mimetype skipped: %q", p.MimeType)
		}
	}
	return body
}

func getBody(m *gmail.Message) string {
	if m.Payload == nil {
		return "loading..."
	}
	return strings.Trim(getBodyRecurse(m.Payload), " \n\r\t")
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

// getWord returns the first word and the remaining string.
func getWord(s string) (string, string) {
	seenChar := false
	for i, r := range s {
		if !unicode.IsSpace(r) {
			seenChar = true
		}
		if seenChar && unicode.IsSpace(r) {
			return s[0:i], s[i:]
		}
	}
	return s, ""
}

func getWords(s string) []string {
	ret := []string{}
	for len(s) > 0 {
		var w string
		w, s = getWord(s)
		ret = append(ret, w)
	}
	return ret
}

// breakLines takes a bunch of lines and breaks them on word boundary.
// TODO: How should it handle quoted (indented) lines?
func breakLines(in []string) []string {
	out := []string{}
	for _, line := range in {
		line = strings.TrimRight(line, spaces)
		if line == "" {
			out = append(out, line)
			continue
		}

		var newLine string
		for _, word := range getWords(line) {
			t := newLine + word
			if newLine == "" {
				newLine = t
			} else if len(t) < maxLine {
				newLine = t
			} else {
				out = append(out, newLine)
				newLine = strings.TrimLeft(word, spaces)
			}
		}
		if newLine != "" {
			out = append(out, newLine)
		}
	}
	return out
}

func standardHeaders() string {
	return "Content-Type: text/plain; charset=UTF-8\n"
}

func getReply(openMessage *gmail.Message) (string, error) {
	subject := cmdglib.GetHeader(openMessage, "Subject")
	if !replyRE.MatchString(subject) {
		subject = *replyPrefix + subject
	}

	addr := cmdglib.GetHeader(openMessage, "Reply-To")
	if addr == "" {
		addr = cmdglib.GetHeader(openMessage, "From")
	}

	head := fmt.Sprintf("To: %s\nSubject: %s\n\nOn %s, %s said:\n",
		addr,
		subject,
		cmdglib.GetHeader(openMessage, "Date"),
		cmdglib.GetHeader(openMessage, "From"),
	)
	s, err := runEditorHeadersOK(head + strings.Join(prefixQuote(breakLines(strings.Split(getBody(openMessage), "\n"))), "\n"))
	return standardHeaders() + s, err
}

func getReplyAll(openMessage *gmail.Message) (string, error) {
	subject := cmdglib.GetHeader(openMessage, "Subject")
	if !replyRE.MatchString(subject) {
		subject = *replyPrefix + subject
	}

	cc := strings.Split(cmdglib.GetHeader(openMessage, "Cc"), ",")
	addr := cmdglib.GetHeader(openMessage, "Reply-To")
	if addr == "" {
		addr = cmdglib.GetHeader(openMessage, "From")
	} else {
		cc = append(cc, cmdglib.GetHeader(openMessage, "From"))
	}
	cc = append(cc, strings.Split(cmdglib.GetHeader(openMessage, "To"), ",")...)
	var ncc []string
	for _, a := range cc {
		a = strings.Trim(a, " ")
		if len(a) == 0 {
			continue
		}
		if strings.Contains(a, "<"+emailAddress+">") {
			continue
		}
		ncc = append(ncc, a)
	}

	head := fmt.Sprintf("To: %s\nCc: %s\nSubject: %s\n\nOn %s, %s said:\n",
		addr,
		strings.Join(ncc, ", "),
		subject,
		cmdglib.GetHeader(openMessage, "Date"),
		cmdglib.GetHeader(openMessage, "From"))
	s, err := runEditorHeadersOK(head + strings.Join(prefixQuote(breakLines(strings.Split(getBody(openMessage), "\n"))), "\n"))
	return standardHeaders() + s, err
}

func getForward(openMessage *gmail.Message) (string, error) {
	subject := cmdglib.GetHeader(openMessage, "Subject")
	if !forwardRE.MatchString(subject) {
		subject = *forwardPrefix + subject
	}
	head := fmt.Sprintf("To: \nSubject: %s\n\n--------- Forwarded message -----------\nDate: %s\nFrom: %s\nTo: %s\nSubject: %s\n\n",
		subject,
		cmdglib.GetHeader(openMessage, "Date"),
		cmdglib.GetHeader(openMessage, "From"),
		cmdglib.GetHeader(openMessage, "To"),
		cmdglib.GetHeader(openMessage, "Subject"),
	)
	s, err := runEditorHeadersOK(head + strings.Join(breakLines(strings.Split(getBody(openMessage), "\n")), "\n"))
	return standardHeaders() + s, err
}

// runEditorHeadersOK is a poorly named function that calls runEditor() until the reply looks somewhat like an email.
func runEditorHeadersOK(input string) (string, error) {
	var s string
	for {
		var err error
		s, err = runEditor(input)
		if err != nil {
			nc.Status("Running editor failed: %v", err)
			return "", err
		}
		s2 := strings.SplitN(s, "\n\n", 2)
		if len(s2) != 2 {
			// TODO: Ask about reopening editor.
			nc.Status("Malformed email, reopening editor")
			input = s
			continue
		}
		break
	}
	return s, nil
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
	nc.Stop()
	defer func() {
		var err error
		nc, err = ncwrap.Start()
		if err != nil {
			log.Fatalf("ncurses failed to re-init: %v", err)
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

func createSend(thread, msg string) {
	maxY, maxX := winSize()
	height := 10
	width := 70
	x, y := maxX/2-width/2, maxY/2-height/2
	w, err := gc.NewWindow(height, width, y, x)
	if err != nil {
		log.Fatalf("Failed to create send dialog: %v", err)
	}
	defer w.Delete()
	log.Printf("send window: %d %d %d %d", height, width, y, x)

	w.Clear()
	w.Print("\n\n   s - Send\n   S - Send and Archive\n")
	if *waitingLabel != "" {
		w.Print("   w - Send and apply waiting label\n")
		w.Print("   W - Send, apply wait label, and archive\n")
	}
	w.Print("   d - Draft\n   a - Abort")
	winBorder(w)
	for {
		w.Refresh()
		gc.Cursor(0)
		key := <-nc.Input
		switch key {
		case 's':
			st := time.Now()
			if _, err := gmailService.Users.Messages.Send(email, &gmail.Message{
				ThreadId: thread,
				Raw:      mimeEncode(msg),
			}).Do(); err != nil {
				nc.Status("Error sending: %v", err)
				return
			}
			log.Printf("Users.Messages.Send: %v", time.Since(st))
			nc.Status("[green]Successfully sent")
			return
		case 'S':
			st := time.Now()
			if _, err := gmailService.Users.Messages.Send(email, &gmail.Message{
				ThreadId: thread,
				Raw:      mimeEncode(msg),
			}).Do(); err != nil {
				nc.Status("Error sending: %v", err)
				return
			}
			log.Printf("Users.Messages.Send: %v", time.Since(st))
			nc.Status("[green]Successfully sent")
			go func() {
				// TODO: Do this in a better way.
				nc.Input <- 'e'
			}()
			return
		case 'w':
			st := time.Now()
			l, ok := labels[*waitingLabel]
			if !ok {
				log.Fatalf("Waiting label %q does not exist!", *waitingLabel)
			}

			nc.Status("Sending with label...")
			if gmsg, err := gmailService.Users.Messages.Send(email, &gmail.Message{
				ThreadId: thread,
				Raw:      mimeEncode(msg),
			}).Do(); err != nil {
				nc.Status("Error sending: %v", err)
			} else {
				if _, err := gmailService.Users.Messages.Modify(email, gmsg.Id, &gmail.ModifyMessageRequest{
					AddLabelIds: []string{l},
				}).Do(); err != nil {
					nc.Status("Error labelling: %v", err)
					log.Printf("Error labelling: %v", err)
				} else {
					nc.Status("Successfully sent (with waiting label %q)", l)
					log.Printf("Users.Messages.Send+Add waiting: %v", time.Since(st))
				}
			}
			nc.Status("[green]Sent with label")
			return
		case 'W':
			st := time.Now()
			l, ok := labels[*waitingLabel]
			if !ok {
				log.Fatalf("Waiting label %q does not exist!", *waitingLabel)
			}

			nc.Status("Sending with label...")
			if gmsg, err := gmailService.Users.Messages.Send(email, &gmail.Message{
				ThreadId: thread,
				Raw:      mimeEncode(msg),
			}).Do(); err != nil {
				nc.Status("Error sending: %v", err)
			} else {
				if _, err := gmailService.Users.Messages.Modify(email, gmsg.Id, &gmail.ModifyMessageRequest{
					AddLabelIds: []string{l},
				}).Do(); err != nil {
					nc.Status("Error labelling: %v", err)
					log.Printf("Error labelling: %v", err)
				} else {
					nc.Status("Successfully sent (with waiting label %q)", l)
					log.Printf("Users.Messages.Send+Add waiting: %v", time.Since(st))
					go func() {
						// TODO: Do this in a better way.
						nc.Input <- 'e'
					}()
				}
			}
			nc.Status("[green]Sent with label")
			return
		case 'a':
			nc.Status("Aborted send")
			return
		case 'd':
			st := time.Now()
			if _, err := gmailService.Users.Drafts.Create(email, &gmail.Draft{
				Message: &gmail.Message{
					ThreadId: thread,
					Raw:      mimeEncode(msg),
				},
			}).Do(); err != nil {
				nc.Status("[red]Error saving as draft: %v", err)
				// TODO: data loss!
				return
			} else {
				nc.Status("Saved draft")
			}
			log.Printf("Users.Drafts.Create: %v", time.Since(st))
			return
		}
	}
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

	if *license {
		fmt.Printf("%s\n", licenseText)
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
		if len(publicClientID) > 0 {
			if err := lib.ConfigureWriteSharedSecrets(scope, accessType, *config, publicClientID, publicClientSecret); err != nil {
				log.Fatalf("Failed to config: %v", err)
			}
		} else {
			if err := lib.ConfigureWrite(scope, accessType, *config); err != nil {
				log.Fatalf("Failed to config: %v", err)
			}
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
	gmailService, err = gmail.New(t.Client())
	if err != nil {
		log.Fatalf("Failed to create gmail client: %v", err)
	}

	// Make sure oauth keys are correct before setting up ncurses.
	if _, err = gmailService.Users.GetProfile(email).Do(); err != nil {
		log.Fatalf("Get profile: %v", err)
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

	nc, err = ncwrap.Start()
	if err != nil {
		log.Fatalf("ncurses fail: %v", err)
	}
	defer func() {
		nc.Stop()
	}()
	nc.Status("Start[green]ing [red]up...")

	getLabels()

	messageListMain(*threadView)
}
