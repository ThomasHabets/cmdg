package cmdg

import (
	"bytes"
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/html/charset"
	gmail "google.golang.org/api/gmail/v1"
)

var (
	gpg = flag.String("gpg", "gpg", "Path to GnuPG.")
)

type Message struct {
	m       sync.RWMutex
	conn    *CmdG
	level   DataLevel
	headers map[string]string

	ID        string
	body      string // Printable body.
	gpgStatus *GPGStatus
	Response  *gmail.Message
}

func (m *Message) GPGStatus() *GPGStatus {
	return m.gpgStatus
}

func NewMessage(c *CmdG, msgID string) *Message {
	if m, found := c.MessageCache(msgID); found {
		return m
	}
	return &Message{
		conn: c,
		ID:   msgID,
	}
}

// assumes R lock held!
func (m *Message) HasData(level DataLevel) bool {
	m.m.RLock()
	defer m.m.RUnlock()

	switch m.level {
	case LevelFull:
		return true
	case LevelMetadata:
		return level != LevelFull
	case LevelMinimal:
		return LevelMinimal != ""
	case LevelEmpty:
		return false
	}
	panic(fmt.Sprintf("can't happen: current level is %q", m.level))
}

func (m *Message) IsUnread() bool {
	return m.HasLabel("UNREAD")
}

func (m *Message) HasLabel(label string) bool {
	// TODO: this only works for label IDs.
	if m.Response == nil {
		return false
	}
	for _, l := range m.Response.LabelIds {
		if label == l {
			return true
		}
	}
	return false
}

// ParseTime tries a few time formats and returns the one that works.
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
		"Mon, _2 Jan 06 15:04:05 -0700",
		time.RFC1123Z,
	} {
		t, err = time.Parse(layout, s)
		if err == nil {
			break
		}
	}
	return t, err
}

func (m *Message) GetFrom(ctx context.Context) (string, error) {
	s, err := m.GetHeader(ctx, "From")
	if err != nil {
		return "", err
	}
	a, err := mail.ParseAddress(s)
	if err != nil {
		log.Warningf("%q is not a valid address: %v", s, err)
		return s, nil
	}
	if len(a.Name) > 0 {
		return a.Name, nil
	}
	return a.Address, nil
}

func (m *Message) GetTime(ctx context.Context) (time.Time, error) {
	s, err := m.GetHeader(ctx, "Date")
	if err != nil {
		return time.Time{}, err
	}
	ts, err := parseTime(s)
	if err != nil {
		return time.Time{}, err
	}
	ts.Local()
	return ts, nil
}
func (m *Message) GetTimeFmt(ctx context.Context) (string, error) {
	ts, err := m.GetTime(ctx)
	if err != nil {
		return "", err
	}
	if time.Since(ts) > 365*24*time.Hour {
		return ts.Format("2006"), nil
	}
	if !(time.Now().Month() == ts.Month() && time.Now().Day() == ts.Day()) {
		return ts.Format("Jan 02"), nil
	}
	return ts.Format("15:04"), nil
}

func (m *Message) GetHeader(ctx context.Context, k string) (string, error) {
	if err := m.Preload(ctx, LevelMetadata); err != nil {
		return "", err
	}
	h, ok := m.headers[strings.ToLower(k)]
	if ok {
		return h, nil
	}
	return "", fmt.Errorf("header not found in msg %q: %q", m.ID, k)
}

// mime decode for gmail. Seems to be special version of base64.
func mimeEncode(s string) string {
	s = base64.StdEncoding.EncodeToString([]byte(s))
	s = strings.Replace(s, "+", "-", -1)
	s = strings.Replace(s, "/", "_", -1)
	return s
}
func mimeDecode(s string) (string, error) {
	s = strings.Replace(s, "-", "+", -1)
	s = strings.Replace(s, "_", "/", -1)
	data, err := base64.StdEncoding.DecodeString(s)
	return string(data), err
}

var unprintableRE = regexp.MustCompile(`[\033\r]`)

func stripUnprintable(s string) string {
	return unprintableRE.ReplaceAllString(s, "")
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

func (m *Message) makeBody(ctx context.Context, part *gmail.MessagePart) (string, error) {
	if len(part.Parts) == 0 {
		log.Infof("Single part body of type %q with input len %d", part.MimeType, len(part.Body.Data))
		data, err := mimeDecode(string(part.Body.Data))
		data = stripUnprintable(data)
		log.Infof("… contents is %q", data)
		if err != nil {
			return "", err
		}
		return data, nil
	}

	log.Infof("Multi part body (%q) with input len %d", part.MimeType, len(part.Body.Data))
	for _, p := range part.Parts {
		if partIsAttachment(p) {
			continue
		}
		switch p.MimeType {
		case "text/plain":
			return m.makeBody(ctx, p)
		default:
			log.Infof("Ignoring part of type %q", p.MimeType)
		}
	}

	return "", fmt.Errorf("not implemented")
}

func (m *Message) GetBody(ctx context.Context) (string, error) {
	if err := m.Preload(ctx, LevelFull); err != nil {
		return "", err
	}
	return m.body, nil
}

func (m *Message) ReloadLabels(ctx context.Context) error {
	msg, err := m.conn.gmail.Users.Messages.Get(email, m.ID).
		Format(string(LevelMinimal)).
		Context(ctx).
		Do()
	m.m.Lock()
	defer m.m.Unlock()
	if m.Response == nil {
		m.Response = msg
		m.level = LevelMinimal
	} else {
		m.Response.LabelIds = msg.LabelIds
	}
	return err
}

func (m *Message) tryGPGSigned(ctx context.Context) error {
	if m.Response.Payload.MimeType != "multipart/signed" {
		return nil
	}
	var partSig *gmail.MessagePart
	var dec string
	for _, p := range m.Response.Payload.Parts {
		switch p.MimeType {
		case "text/plain":
			var err error
			dec, err = mimeDecode(p.Body.Data)
			if err != nil {
				return err
			}
			var hs []string
			for _, h := range p.Headers {
				hs = append(hs, fmt.Sprintf("%s: %s", h.Name, h.Value))
			}
			hp := strings.Join(hs, "\r\n") + "\r\n\r\n"
			dec = hp + dec
			// TODO: what if it's signed HTML?
		case "application/pgp-signature":
			partSig = p
		default:
			log.Warningf("Found unexpected part in signed packet: %q", p.MimeType)
		}
	}

	// Fetch attachment.
	body, err := m.conn.gmail.Users.Messages.Attachments.Get(email, m.ID, partSig.Body.AttachmentId).Context(ctx).Do()
	if err != nil {
		return errors.Wrap(err, "failed to download signature attachment")
	}
	sigDec, err := mimeDecode(body.Data)
	if err != nil {
		return errors.Wrap(err, "failed to MIME decode signature attachment")
	}
	st, err := GPGVerify(ctx, dec, sigDec)
	if err != nil {
		return err
	}
	m.gpgStatus = st
	return nil
}

func (m *Message) tryGPGEncrypted(ctx context.Context) error {
	if m.Response.Payload.MimeType != "multipart/encrypted" {
		return nil
	}

	// Expect two subparts.
	var partMeta *gmail.MessagePart
	var partData *gmail.MessagePart
	for _, p := range m.Response.Payload.Parts {
		switch p.MimeType {
		case "application/pgp-encrypted":
			partMeta = p
		case "application/octet-stream":
			partData = p
		default:
			log.Warningf("Found unexpected part in encrypted packet: %q", p.MimeType)
		}
	}
	if partMeta == nil || partData == nil {
		log.Warningf("Encrypted packet missing either meta or data")
	}

	// Fetch data attachment.
	body, err := m.conn.gmail.Users.Messages.Attachments.Get(email, m.ID, partData.Body.AttachmentId).Context(ctx).Do()
	if err != nil {
		return errors.Wrap(err, "failed to download encrypted data attachment")
	}
	dec, err := mimeDecode(body.Data)
	if err != nil {
		return errors.Wrap(err, "failed to MIME decode encrypted data attachment")
	}

	// Decrypt data attachment.
	dec2, status, err := GPGDecode(ctx, dec)
	if err != nil {
		return err
	}

	msg, err := mail.ReadMessage(strings.NewReader(dec2))
	if err != nil {
		return err
	}

	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil {
		return err
	}
	if strings.HasPrefix(mediaType, "multipart/") {
		log.Infof("Multipart encrypted with media type %q", mediaType)
		mr := multipart.NewReader(msg.Body, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return errors.Wrap(err, "failed to get mime part")
			}
			dec, err := toUTF8Reader(map[string][]string(p.Header), p)
			t, err := ioutil.ReadAll(dec)
			if err != nil {
				return errors.Wrap(err, "utf8reading mime part")
			}
			ct := p.Header.Get("Content-Type")
			mt, _, err := mime.ParseMediaType(ct)
			if err != nil {
				return errors.Wrapf(err, "parsing content-type %q", ct)
			}
			if p.FileName() == "" {
				np := &gmail.MessagePart{
					MimeType: mt,
					Body: &gmail.MessagePartBody{
						Data: mimeEncode(string(t)),
					},
				}
				m.body, err = m.makeBody(ctx, np)
				if err != nil {
					return errors.Wrap(err, "failed to decrypt")
				}
			} else {
				// TODO: handle attachment.
			}
		}

	} else {
		r, err := toUTF8Reader(map[string][]string(msg.Header), msg.Body)
		t, err := ioutil.ReadAll(r)
		if err != nil {
			return err
		}
		m.body = string(t)
	}

	m.gpgStatus = status
	return nil
}

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
	log.Printf("No decoder for charset %q", params["charset"])
	return r, nil
}

type GPGStatus struct {
	Signed        string
	Encrypted     []string
	GoodSignature bool
	Warnings      []string
}

var (
	goodSignatureRE = regexp.MustCompile(`(?m)^gpg: Good signature from "(.*)"`)
	badSignatureRE  = regexp.MustCompile(`(?m)^gpg: BAD signature from "(.*)"`)
	encryptedRE     = regexp.MustCompile(`(?m)^gpg: encrypted with[^\n]+\n([^\n]+)\n`)
)

func GPGDecode(ctx context.Context, dec string) (string, *GPGStatus, error) {
	var stderr bytes.Buffer
	var stdout bytes.Buffer
	cmd := exec.CommandContext(ctx, *gpg, "--batch", "--no-tty")
	cmd.Stdin = bytes.NewBufferString(dec)
	cmd.Stderr = &stderr
	cmd.Stdout = &stdout
	if err := cmd.Start(); err != nil {
		return "", nil, errors.Wrapf(err, "failed to start gpg (%q)", *gpg)
	}
	if err := cmd.Wait(); err != nil {
		return "", nil, errors.Wrapf(err, "gpg decode failed")
	}
	status := &GPGStatus{}
	if m := goodSignatureRE.FindStringSubmatch(stderr.String()); m != nil {
		status.Signed = stripUnprintable(m[1])
		status.GoodSignature = true
	}
	if ms := encryptedRE.FindAllStringSubmatch(stderr.String(), -1); ms != nil {
		for _, m := range ms {
			status.Encrypted = append(status.Encrypted, strings.Trim(stripUnprintable(m[1]), "\t "))
		}
	}

	return stdout.String(), status, nil
}

func GPGVerify(ctx context.Context, data, sig string) (*GPGStatus, error) {
	dir, err := ioutil.TempDir("", "gpg-signature")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	log.Infof("Checking signature with %q…", dir)
	dataFN := path.Join(dir, "data")
	sigFN := path.Join(dir, "data.gpg")
	if err := ioutil.WriteFile(dataFN, []byte(data), 0600); err != nil {
		return nil, err
	}
	if err := ioutil.WriteFile(sigFN, []byte(sig), 0600); err != nil {
		return nil, err
	}

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, *gpg, "--verify", "--no-tty", sigFN, dataFN)
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, errors.Wrapf(err, "failed to start gpg (%q)", *gpg)
	}
	status := &GPGStatus{}
	goodOrBad := false
	if err := cmd.Wait(); err != nil {
		e, ok := err.(*exec.ExitError)
		if !ok {
			return nil, errors.Wrapf(err, "gpg verify failed for odd reason. stderr: %q", stderr.String())
		}
		u, ok := e.Sys().(syscall.WaitStatus)
		if !ok {
			return nil, errors.Wrapf(e, "gpg verify failed, and not unix status. stderr: %q", stderr.String())
		}
		if u.ExitStatus() != 1 {
			return nil, errors.Wrapf(e, "gpg verify failed, and not status 1 (was %d). stderr: %q", u.ExitStatus(), stderr.String())
		}
		// Continue since status 1, assume either good or bad signature now.
	}
	if m := badSignatureRE.FindStringSubmatch(stderr.String()); m != nil {
		status.Signed = stripUnprintable(m[1])
		goodOrBad = true
	}
	if m := goodSignatureRE.FindStringSubmatch(stderr.String()); m != nil {
		status.Signed = stripUnprintable(m[1])
		status.GoodSignature = true
		goodOrBad = true
	}
	if !goodOrBad {
		return nil, fmt.Errorf("signature not good nor bad. What? %q", stderr.String())
	}
	return status, nil
}

func (m *Message) Preload(ctx context.Context, level DataLevel) error {
	{
		if m.HasData(level) {
			return nil
		}
	}

	st := time.Now()
	msg, err := m.conn.gmail.Users.Messages.Get(email, m.ID).
		Format(string(level)).
		Context(ctx).
		Do()
	if err != nil {
		return err
	}
	log.Debugf("Downloading message %q level %q took %v", m.ID, level, time.Since(st))

	m.m.Lock()
	defer m.m.Unlock()
	m.Response = msg
	m.level = level
	m.headers = make(map[string]string)
	for _, h := range m.Response.Payload.Headers {
		m.headers[strings.ToLower(h.Name)] = h.Value
	}
	if level == LevelFull {
		m.body, err = m.makeBody(ctx, m.Response.Payload)
		if err := m.tryGPGEncrypted(ctx); err != nil {
			log.Errorf("Decrypting GPG: %v", err)
		}
		if err := m.tryGPGSigned(ctx); err != nil {
			log.Errorf("Checking GPG signature: %v", err)
		}
	}
	return err
}

func (m *Message) Lines(ctx context.Context) (int, error) {
	if err := m.Preload(ctx, LevelFull); err != nil {
		return 0, err
	}
	return len(strings.Split(m.body, "\n")), nil
}
