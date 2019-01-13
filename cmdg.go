// cmdg is a command line client to Gmail.
//
/*
 *  Copyright (C) 2015 Thomas Habets <thomas@habets.se>
 *
 *  This software is dual-licensed GPL and "Thomas is allowed to release a
 *  binary version that adds shared API keys and nothing else".
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
 *
 * Some more interesting stuff can be found in doc for:
 *  golang.org/x/text/encoding
 * golang.org/x/text/encoding/charmap
 */
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math"
	"math/rand"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/mail"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/ThomasHabets/cmdg/cmdglib"
	"github.com/ThomasHabets/cmdg/ncwrap"
	"github.com/ThomasHabets/drive-du/lib"
	gc "github.com/rthornton128/goncurses"
	"golang.org/x/net/html/charset"
	gmail "google.golang.org/api/gmail/v1"
)

const (
	version   = "0.3"
	userAgent = "cmdg " + version

	backoffTime = 50 * time.Millisecond // Initial backoff time for API calls.
	backoffBase = 1.7
	maxRetries  = 20
	maxTimeout  = 5 * time.Second

	configDirMode os.FileMode = 0700

	// Relative to configDir.
	configFileName = "cmdg.conf"

	// Relative to $HOME.
	defaultConfigDir     = ".cmdg"
	defaultSignatureFile = ".signature"
)

var (
	license       = flag.Bool("license", false, "Show program license.")
	help          = flag.Bool("help", false, "Show usage text and exit.")
	help2         = flag.Bool("h", false, "Show usage text and exit.")
	configDir     = flag.String("config_dir", "", "Config directory. If empty will default to ~/"+defaultConfigDir)
	configure     = flag.Bool("configure", false, "Configure OAuth and write config file.")
	readonly      = flag.Bool("readonly", false, "When configuring, only acquire readonly permission.")
	gpg           = flag.String("gpg", "/usr/bin/gpg", "Path to GnuPG.")
	replyRegex    = flag.String("reply_regexp", `(?i)^(Re|Sv|Aw|AW): `, "If subject matches, there's no need to add a Re: prefix.")
	replyPrefix   = flag.String("reply_prefix", "Re: ", "String to prepend to subject in replies.")
	forwardRegex  = flag.String("forward_regexp", `(?i)^(Fwd): `, "If subject matches, there's no need to add a Fwd: prefix.")
	forwardPrefix = flag.String("forward_prefix", "Fwd: ", "String to prepend to subject in forwards.")
	signature     = flag.String("signature", "", "File containing end of all emails. Defaults to ~/"+defaultSignatureFile)
	logFile       = flag.String("log", "/dev/null", "Log non-sensitive data to this file.")
	waitingLabel  = flag.String("waiting_label", "", "Label used for 'awaiting reply'. If empty disables feature.")
	threadView    = flag.Bool("thread", false, "Use thread view.")
	lynx          = flag.String("lynx", "lynx", "Path to 'lynx' browser. Used to render HTML email.")
	preConfig     = flag.String("preconfig", "", "Command to run before reading config. Used if config is generated.")
	enableHistory = flag.Bool("history", true, "Enable history API to optimize network use. Seems to be a bit unreliable on the server side.")
	openBinary    = flag.String("open", "xdg-open", "Command to open attachments with.")
	openWait      = flag.Bool("open_wait", false, "Wait after opening attachment. If using X, then makes sense to say no.")

	authedClient *http.Client
	gmailService *gmail.Service
	scope        string // OAuth scope

	nc *ncwrap.NCWrap

	// State keepers.
	labels       = make(map[string]string) // From name to ID.
	labelIDs     = make(map[string]string) // From ID to name.
	contacts     contactsT
	emailAddress string

	logRedirected bool // Don't write API measurements to log until it's been redirected.

	pagerBinary  string
	editorBinary string

	replyRE   *regexp.Regexp
	forwardRE *regexp.Regexp

	errNoHistory = errors.New("nothing new since last check")

	sleep = time.Sleep
)

const (
	// Scopes. Gmail and contacts.
	scopeReadonly = "https://www.googleapis.com/auth/gmail.readonly https://www.googleapis.com/auth/contacts.readonly"
	scopeModify   = "https://www.googleapis.com/auth/gmail.modify https://www.google.com/m8/feeds"
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

func profileAPI(op string, d time.Duration) {
	if logRedirected {
		log.Printf("API call %v: %v", op, d)
	}
}

func backoff(n int) (time.Duration, bool) {
	f := func(n int) float64 {
		return float64(backoffTime.Nanoseconds()) * math.Pow(backoffBase, float64(n))
	}
	ns := int64(f(n))
	if true {
		// Randomize backoff a bit.
		ns = int64(f(n) + (f(n+1)-f(n))*rand.Float64())
	}
	done := false
	if ns > maxTimeout.Nanoseconds() {
		ns = maxTimeout.Nanoseconds()
	}
	if n > maxRetries {
		done = true
	}
	return time.Duration(ns), done
}

// list returns some initial message stubs, with the full message coming later on the returned channel.
// label is the label ID ("" means all mail).
// search is the search query ("" means match all).
func list(label, search, pageToken string, nres int, historyID uint64) (uint64, []listEntry, <-chan listEntry, []error) {
	log.Printf("Listing label %q, search %q. HistoryID %v", label, search, historyID)
	syncP := parallel{} // Run the parts that can't wait in parallel.

	var newHistoryID uint64
	if *enableHistory && historyID > 0 {
		st := time.Now()
		res, err := gmailService.Users.History.List(email).MaxResults(1).StartHistoryId(historyID).Do()
		if err == nil {
			profileAPI("History.List", time.Since(st))
		}
		if err != nil {
			log.Printf("Failed to check history: %v", err)
		} else if len(res.History) == 0 {
			return res.HistoryId, nil, nil, []error{errNoHistory}
		} else {
			newHistoryID = res.HistoryId
			log.Printf("New history ID: %v", newHistoryID)
		}
	}

	var funcErr []error
	// List messages.
	var res *gmail.ListMessagesResponse
	syncP.add(func(ch chan<- func()) {
		defer close(ch)
		var err error
		st := time.Now()
		q := gmailService.Users.Messages.List(email).
			PageToken(pageToken).
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
			ch <- func() {
				funcErr = append(funcErr, fmt.Errorf("Users.Messages.List: %v", err))
			}
			return
		}
		profileAPI("Users.Messages.List", time.Since(st))
	})

	// Get Profile to update status line.
	var profile *gmail.Profile
	syncP.add(func(ch chan<- func()) {
		defer close(ch)
		st := time.Now()
		p, err := gmailService.Users.GetProfile(email).Do()
		if err != nil {
			ch <- func() {
				funcErr = append(funcErr, fmt.Errorf("Users.GetProfile: %v", err))
			}
			return
		}
		profile = p
		profileAPI("Users.GetProfile", time.Since(st))
	})
	syncP.run()
	if len(funcErr) != 0 {
		return 0, nil, nil, funcErr
	}

	nc.Status("Total number of messages in folder: %d", res.ResultSizeEstimate)

	msgChan := make(chan listEntry, len(res.Messages))
	var wg sync.WaitGroup
	{
		// Load message bodies async and in parallel.
		for _, m := range res.Messages {
			wg.Add(1)
			m2 := m
			go func() {
				defer wg.Done()
				st := time.Now()
				for bo := 0; ; bo++ {
					mres, err := gmailService.Users.Messages.Get(email, m2.Id).Format("full").Do()
					if err != nil {
						s, done := backoff(bo)
						if done {
							log.Printf("Get message failed, backoff expired, giving up: %v", err)
							return
						}
						sleep(s)
						log.Printf("Get message failed, retrying: %v", err)
						continue
					}
					profileAPI("Users.Messages.Get", time.Since(st))
					msgChan <- listEntry{
						msg: mres,
					}
					return
				}
			}()
		}
		go func() {
			wg.Wait()
			close(msgChan)
		}()
	}
	nc.Status("%s: Showing %d/%d. Total: %d emails, %d threads",
		profile.EmailAddress, len(res.Messages), res.ResultSizeEstimate, profile.MessagesTotal, profile.ThreadsTotal)
	ret := []listEntry{}
	for _, m := range res.Messages {
		ret = append(ret, listEntry{
			msg: m,
		})
	}
	return newHistoryID, ret, msgChan, nil
}

// TODO: clean this up to look more like list().
func listThreads(label, search, pageToken string, nres int, historyID uint64) ([]listEntry, <-chan listEntry) {
	log.Printf("Listing thread label %q, search %q. historyID %v", label, search, historyID)
	syncP := parallel{} // Run the parts that can't wait in parallel.

	// Check if there are any new messages.
	// TODO

	var funcErr []error
	// List messages.
	var res *gmail.ListThreadsResponse
	syncP.add(func(ch chan<- func()) {
		defer close(ch)
		var err error
		st := time.Now()
		q := gmailService.Users.Threads.List(email).
			PageToken(pageToken).
			MaxResults(int64(nres)).
			Fields("threads,resultSizeEstimate")
		if label != "" {
			q = q.LabelIds(label)
		}
		if search != "" {
			q = q.Q(search)
		}
		res, err = q.Do()
		if err != nil {
			ch <- func() {
				funcErr = append(funcErr, fmt.Errorf("Listing threads: %v", err))
			}
		}
		profileAPI("Users.Threads.List", time.Since(st))
	})

	// Get Profile to update status line.
	// TODO: merge with list()
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
	var wg sync.WaitGroup
	// Load thread message bodies async.
	{
		for _, m := range res.Threads {
			wg.Add(1)
			m2 := m
			go func() {
				defer wg.Done()
				st := time.Now()
				for bo := 0; ; bo++ {
					mres, err := gmailService.Users.Threads.Get(email, m2.Id).Format("full").Do()
					if err != nil {
						s, done := backoff(bo)
						if done {
							log.Printf("Get thread failed, retrying: %v", err)
							return
						}
						log.Printf("Get thread %q failed, retrying after %v: %v", m2.Id, s, err)
						sleep(s)
						continue
					}
					profileAPI("Users.Thread.Get", time.Since(st))
					msgChan <- listEntry{
						thread: mres,
					}
					return
				}
			}()
		}
		go func() {
			wg.Wait()
			close(msgChan)
		}()
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

func updateLabels(ls []*gmail.Label) {
	labels = make(map[string]string)
	labelIDs = make(map[string]string)
	for _, l := range ls {
		labels[l.Name] = l.Id
		labelIDs[l.Id] = l.Name
	}
}

func getLabels() ([]*gmail.Label, error) {
	st := time.Now()
	res, err := gmailService.Users.Labels.List(email).Do()
	if err != nil {
		nc.Status("[red]Listing labels: %v", err)
		return nil, err
	}
	profileAPI("Users.Labels.List", time.Since(st))
	return res.Labels, nil
}

func mimeDecode(s string) (string, error) {
	s = strings.Replace(s, "-", "+", -1)
	s = strings.Replace(s, "_", "/", -1)
	data, err := base64.StdEncoding.DecodeString(s)
	return string(data), err
}

// Fetch and gpg decode a gmail attachment.
func gpgDecodeAttachment(msgID, id string) (string, error) {
	body, err := gmailService.Users.Messages.Attachments.Get(email, msgID, id).Do()
	if err != nil {
		return "", err
	}
	dec, err := mimeDecode(body.Data)
	if err != nil {
		return "", err
	}
	return gpgDecode(dec)
}

// GPG decode a string.
func gpgDecode(p string) (string, error) {
	in := bytes.NewBufferString(p)
	var stderr, stdout bytes.Buffer
	cmd := exec.CommandContext(context.TODO(), *gpg, "-v", "--batch", "--no-tty")
	cmd.Stdin = in
	cmd.Stderr = &stderr
	cmd.Stdout = &stdout
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("failed to run gpg: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		return "", fmt.Errorf("gpg decode failed: %v. Stderr: %q", err, stderr.String())
	}

	return stdout.String(), nil
}

// mime decode for gmail. Seems to be special version of base64.
func mimeEncode(s string) string {
	s = base64.StdEncoding.EncodeToString([]byte(s))
	s = strings.Replace(s, "+", "-", -1)
	s = strings.Replace(s, "/", "_", -1)
	return s
}

// Given a header and a reader, make a new reader that is aware of
// headers special meaning for decoding.
func toUTF8Reader(header mail.Header, r io.Reader) (io.Reader, error) {
	_, params, err := mime.ParseMediaType(header.Get("Content-Type"))
	if err != nil {
		return nil, err
	}
	switch header.Get("Content-Transfer-Encoding") {
	case "quoted-printable":
		r = quotedprintable.NewReader(r)
	case "base64":
		r = base64.NewDecoder(base64.StdEncoding, r)
	}
	e, _ := charset.Lookup(params["charset"])
	if e != nil {
		return e.NewDecoder().Reader(r), nil
	}
	log.Printf("No decoder for charmap %q", params["charset"])
	return r, nil
}

func partIsAttachment(p *gmail.MessagePart) bool {
	for _, head := range p.Headers {
		if head.Name == "Content-Disposition" {
			// TODO: Is this the correct way? Maybe check "attachment" instead?
			return head.Value != "inline"
		}
	}
	return false
}

// Fixup a message part so that application/pgp-encrypted gets
// subparts with decrypted data.
func fixupGPG(msgID string, m *gmail.MessagePart) {
	var enc *gmail.MessagePart

	// Check if mail is encrypted, and if so add encrypted
	// sections underneath application/pgp-encrypted.
	// TODO: this should probably be moved to a message fixup-thing.
	for _, p := range m.Parts {
		// TODO: check version
		if p.MimeType == "application/pgp-encrypted" {
			enc = p
			continue
		}
		if p.MimeType == "application/octet-stream" {
			if enc == nil {
				continue
			}
			if len(enc.Parts) > 0 {
				// Already added.
				continue
			}

			data, err := gpgDecodeAttachment(msgID, p.Body.AttachmentId)
			if err != nil {
				log.Printf("Failed to decode GPG: %v", err)
				continue
			}
			msg, err := mail.ReadMessage(strings.NewReader(data))
			if err != nil {
				log.Printf("Failed to read message: %v", err)
				continue
			}

			mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
			if err != nil {
				log.Printf("Failed to parse media type: %v", err)
				continue
			}
			if strings.HasPrefix(mediaType, "multipart/") {
				mr := multipart.NewReader(msg.Body, params["boundary"])
				for {
					p, err := mr.NextPart()
					if err == io.EOF {
						break
					}
					if err != nil {
						log.Printf("Failed to get mime part: %v", err)
						break
					}
					dec, err := toUTF8Reader(map[string][]string(p.Header), p)
					t, err := ioutil.ReadAll(dec)
					if err != nil {
						log.Printf("Failed to read mime part body: %v", err)
					}
					np := &gmail.MessagePart{
						MimeType: mediaType,
						Body: &gmail.MessagePartBody{
							Data: mimeEncode(string(t)),
						},
					}
					for k, vs := range p.Header {
						for _, v := range vs {
							if strings.ToLower(k) == strings.ToLower("Content-Disposition") {
								t := strings.Split(v, ";")
								for _, t2 := range t {
									t2 = strings.Trim(t2, " ")
									eq := strings.SplitN(t2, "=", 2)
									if eq[0] == "filename" {
										np.Filename = strings.Trim(eq[1], `"`)
									}
								}
							}
							np.Headers = append(np.Headers, &gmail.MessagePartHeader{
								Name:  k,
								Value: v,
							})
						}
					}
					enc.Parts = append(enc.Parts, np)
				}
			} else {
				// TODO: merge this code with the above multipart code.
				dec, err := toUTF8Reader(msg.Header, msg.Body)
				t, err := ioutil.ReadAll(dec)
				if err != nil {
					log.Printf("Failed to read body: %v", err)
				}
				if enc == nil {
					continue
				}
				var hs []*gmail.MessagePartHeader
				for k, vs := range msg.Header {
					for _, v := range vs {
						hs = append(hs, &gmail.MessagePartHeader{
							Name:  k,
							Value: v,
						})
					}
				}
				enc.Parts = append(enc.Parts, &gmail.MessagePart{
					Headers:  hs,
					MimeType: mediaType,
					Body: &gmail.MessagePartBody{
						Data: mimeEncode(string(t)),
					},
				})
			}
		}
	}
}

// Find plaintext body among all attachments.
func getBodyRecurse(msgID string, m *gmail.MessagePart) string {
	if len(m.Parts) == 0 {
		data, err := mimeDecode(string(m.Body.Data))
		if err != nil {
			return fmt.Sprintf("mime decoding error: %v", err)
		}
		if strings.HasPrefix(cmdglib.GetHeaderPart(m, "Content-Type"), "text/html") {
			if data, err := html2txt(data); err != nil {
				log.Printf("Rendering HTML: %v", err)
			} else {
				return data
			}
		}
		return data
	}
	body := ""
	htmlBody := "" // Used only if there's no plaintext version.

	fixupGPG(msgID, m)

	for _, p := range m.Parts {
		if partIsAttachment(p) && p.MimeType != "application/pgp-encrypted" {
			continue
		}
		switch p.MimeType {
		case "text/plain":
			data, err := mimeDecode(p.Body.Data)
			if err != nil {
				return fmt.Sprintf("mime decoding error for text/plain: %v", err)
			}
			body += string(data)
		case "text/html":
			data, err := mimeDecode(p.Body.Data)
			if err != nil {
				return fmt.Sprintf("mime decoding error for text/html: %v", err)
			}
			htmlBody += string(data)
		case "multipart/alternative", "multipart/related", "multipart/mixed":
			body += getBodyRecurse(msgID, p)
		case "application/pgp-encrypted":
			body += getBodyRecurse(msgID, p)
		default:
			// Skip.
			log.Printf("Unknown mimetype skipped: %q", p.MimeType)
		}
	}
	if body != "" {
		return body
	}
	if htmlBody != "" {
		b, err := html2txt(htmlBody)
		if err != nil {
			log.Printf("Rendering HTML: %v", err)
			return htmlBody
		}
		return b
	}
	return "Error extracting content: none found."
}

func getBody(m *gmail.Message) string {
	if m.Payload == nil {
		return "loading..."
	}
	return strings.Trim(getBodyRecurse(m.Id, m.Payload), " \n\r\t")
}

var (
	html2txtCacheLock sync.Mutex
	html2txtCache     = make(map[string]string)
)

// html2txt uses lynx to render HTML to plain text.
func html2txt(s string) (string, error) {
	html2txtCacheLock.Lock()
	defer html2txtCacheLock.Unlock()
	if r, found := html2txtCache[s]; found {
		return r, nil
	}
	var stdout bytes.Buffer
	st := time.Now()
	cmd := exec.Command(*lynx, "-dump", "-stdin")
	cmd.Stdin = bytes.NewBufferString(s)
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return "", err
	}
	profileAPI("lynx", time.Since(st))
	for len(html2txtCache) > 10 {
		// Delete a random cached entry.
		for k := range html2txtCache {
			delete(html2txtCache, k)
			break
		}
	}
	html2txtCache[s] = stdout.String()
	return stdout.String(), nil
}

func prefixQuote(in []string) []string {
	var out []string
	for _, line := range in {
		if len(line) == 0 {
			out = append(out, ">")
		} else if line[0] == '>' {
			out = append(out, ">"+line)
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

		// Don't CC self, that would be silly.
		if strings.Contains(a, "<"+emailAddress+">") || a == emailAddress {
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

func runPager(input string) error {
	// Re-acquire terminal when done.
	defer runSomething()()
	cmd := exec.Command(pagerBinary)
	cmd.Stdin = bytes.NewBufferString(input)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to run pager %q: %v", pagerBinary, err)
	}
	return nil
}

// runSomething gives up terminal control, and returns a lambda that reacquires it.
func runSomething() func() {
	// Restore terminal for editors use.
	nc.Stop()

	return func() {
		var err error
		nc, err = ncwrap.Start()
		if err != nil {
			log.Fatalf("ncurses failed to re-init: %v", err)
		}
	}
}

// createSend asks how to send the message just composed, and sends it.
// thread is the thread id, and may be empty.
// msg is the string representation of the message.
func createSend(thread, msg string) (err error) {
	defer func() {
		if err != nil {
			if err2 := saveFailedSend(msg); err2 != nil {
				nc.Status("[red]Double fail: %v; %v", err, err2)
				log.Printf("Failed while laving failsafe: %v %v", err, err2)
			}
		}
	}()
	// Run menu.
	var choice gc.Key
	{
		cs := []keyChoice{
			{'s', "Send"},
			{'S', "Send and archive"},
		}
		if *waitingLabel != "" {
			cs = append(cs,
				keyChoice{'w', "Send and apply waiting label"},
				keyChoice{'W', "Send, apply waiting label, and archive"},
			)
		}
		cs = append(cs,
			keyChoice{'d', "Save as draft"},
			keyChoice{'a', "Abort, discarding draft"},
		)
		choice = keyMenu(cs)
	}
	switch choice {
	case 's':
		st := time.Now()
		if _, err := gmailService.Users.Messages.Send(email, &gmail.Message{
			ThreadId: thread,
			Raw:      mimeEncode(msg),
		}).Do(); err != nil {
			nc.Status("Error sending: %v", err)
			return err
		}
		log.Printf("Users.Messages.Send: %v", time.Since(st))
		nc.Status("[green]Successfully sent")
	case 'S':
		st := time.Now()
		if _, err := gmailService.Users.Messages.Send(email, &gmail.Message{
			ThreadId: thread,
			Raw:      mimeEncode(msg),
		}).Do(); err != nil {
			nc.Status("Error sending: %v", err)
			return err
		}
		log.Printf("Users.Messages.Send: %v", time.Since(st))
		nc.Status("[green]Successfully sent")
		go func() {
			// TODO: Do this in a better way.
			nc.Input <- 'e'
		}()
	case 'w': // Send with label.
		st := time.Now()
		l, hasLabel := labels[*waitingLabel]
		nc.Status("Sending with label...")

		// Send.
		gmsg, err := gmailService.Users.Messages.Send(email, &gmail.Message{
			ThreadId: thread,
			Raw:      mimeEncode(msg),
		}).Do()
		if err != nil {
			nc.Status("Error sending: %v", err)
			return err
		}

		if !hasLabel {
			nc.Status("Sent OK, [red]but label %q doesn't exist, so can't add it.", *waitingLabel)
		} else {
			// Add label.
			if _, err := gmailService.Users.Messages.Modify(email, gmsg.Id, &gmail.ModifyMessageRequest{
				AddLabelIds: []string{l},
			}).Do(); err != nil {
				nc.Status("Error labelling: %v", err)
				log.Printf("Error labelling: %v", err)
			} else {
				nc.Status("Successfully sent (with waiting label %q)", l)
				log.Printf("Users.Messages.Send+Add waiting: %v", time.Since(st))
			}
			nc.Status("[green]Sent with label")
		}
	case 'W': // Send with label and archive.
		st := time.Now()
		l, hasLabel := labels[*waitingLabel]

		nc.Status("Sending with label...")
		gmsg, err := gmailService.Users.Messages.Send(email, &gmail.Message{
			ThreadId: thread,
			Raw:      mimeEncode(msg),
		}).Do()
		if err != nil {
			nc.Status("Error sending: %v", err)
			return err
		}

		if !hasLabel {
			nc.Status("Sent OK, [red]but label %q doesn't exist, so can't add it.", *waitingLabel)
		} else {
			// Add label.
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
			nc.Status("[green]Sent with label")
		}
		go func() {
			// TODO: Archive in a better way.
			nc.Input <- 'e'
		}()
	case 'a':
		nc.Status("Aborted send")
	case 'd':
		st := time.Now()
		if _, err := gmailService.Users.Drafts.Create(email, &gmail.Draft{
			Message: &gmail.Message{
				ThreadId: thread,
				Raw:      mimeEncode(msg),
			},
		}).Do(); err != nil {
			nc.Status("[red]Error saving as draft: %v", err)
			return err
		}
		nc.Status("Saved draft")
		log.Printf("Users.Drafts.Create: %v", time.Since(st))
	default:
		nc.Status("[red]Error: invalid key %q pressed!", choice)
		return fmt.Errorf("invalid key %q", choice)
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

func reconnect() error {
	if *preConfig != "" {
		cmd := exec.Command(*preConfig)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to run preconfig %q: %v", *preConfig, err)
		}
	}
	conf, err := lib.ReadConfig(configFilePath())
	if err != nil {
		return fmt.Errorf("failed to read config %q: %v", configFilePath(), err)
	}
	authedClient, err = lib.Connect(conf.OAuth, scope, accessType)
	if err != nil {
		return fmt.Errorf("failed to connect to gmail: %v", err)
	}
	gmailService, err = gmail.New(authedClient)
	if err != nil {
		return err
	}
	gmailService.UserAgent = userAgent
	return nil
}

func configFilePath() string {
	return path.Join(*configDir, configFileName)
}

func main() {
	syscall.Umask(0077)
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
	if *configDir == "" {
		*configDir = path.Join(os.Getenv("HOME"), defaultConfigDir)
	}
	if *signature == "" {
		*signature = path.Join(os.Getenv("HOME"), defaultSignatureFile)
	}
	scope = scopeModify
	if *readonly {
		scope = scopeReadonly
	}
	if *configure {
		if err := os.MkdirAll(*configDir, configDirMode); err != nil {
			log.Printf("Failed to create %q, continuing anyway: %v", *configDir, err)
		}
		if len(publicClientID) > 0 {
			if err := lib.ConfigureWriteSharedSecrets(scope, accessType, configFilePath(), publicClientID, publicClientSecret); err != nil {
				log.Fatalf("Failed to config: %v", err)
			}
		} else {
			if err := lib.ConfigureWrite(scope, accessType, configFilePath()); err != nil {
				log.Fatalf("Failed to config: %v", err)
			}
		}
		return
	}
	if fi, err := os.Stat(configFilePath()); err != nil {
		log.Fatalf("Missing config file %q: %v", configFilePath(), err)
	} else if (fi.Mode() & 0477) != 0400 {
		log.Fatalf("Config file (%q) permissions must be 0600 or better, was 0%o", configFilePath(), fi.Mode()&os.ModePerm)
	}

	pagerBinary = os.Getenv("PAGER")
	if len(pagerBinary) == 0 {
		log.Fatalf("You need to set the PAGER environment variable. When in doubt, set to 'less'.")
	}

	editorBinary = os.Getenv("VISUAL")
	if len(editorBinary) == 0 {
		editorBinary = os.Getenv("EDITOR")
		if len(editorBinary) == 0 {
			log.Fatalf("You need to set the VISUAL or EDITOR environment variable. Set to your favourite editor.")
		}
	}

	if err := reconnect(); err != nil {
		log.Fatalf("Failed to create gmail client: %v", err)
	}

	// Make sure oauth keys are correct before setting up ncurses.
	{
		profile, err := gmailService.Users.GetProfile(email).Do()
		if err != nil {
			log.Fatalf("Get profile: %v", err)
		}
		emailAddress = profile.EmailAddress
	}

	// Get some initial data that should always succeed.
	if c, err := getLabels(); err != nil {
		log.Fatalf("Getting labels: %v", err)
	} else {
		updateLabels(c)
	}
	if err := updateContacts(); err != nil {
		log.Fatalf("Getting contacts: %v", err)
	}

	// Redirect logging.
	{
		f, err := os.OpenFile(*logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			log.Fatalf("Can't create logfile %q: %v", *logFile, err)
		}
		defer f.Close()
		log.SetOutput(f)
		log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
		logRedirected = true
	}

	nc, err = ncwrap.Start()
	if err != nil {
		log.Fatalf("ncurses failed to start: %v", err)
	}
	defer func() {
		nc.Stop()
	}()
	nc.Status("Start[green]ing [red]up...")

	messageListMain(*threadView)
}
