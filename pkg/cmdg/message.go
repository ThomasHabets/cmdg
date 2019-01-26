package cmdg

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"io/ioutil"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/html/charset"
	gmail "google.golang.org/api/gmail/v1"

	"github.com/ThomasHabets/cmdg/pkg/display"
	"github.com/ThomasHabets/cmdg/pkg/gpg"
)

const (
	Inbox  = "INBOX"
	Unread = "UNREAD"
)

var (
	GPG *gpg.GPG
)

type Attachment struct {
	ID       string
	MsgID    string
	conn     *CmdG
	contents []byte
	Part     *gmail.MessagePart
}

func (a *Attachment) Download(ctx context.Context) ([]byte, error) {
	if a.contents != nil {
		return a.contents, nil
	}
	body, err := a.conn.gmail.Users.Messages.Attachments.Get(email, a.MsgID, a.ID).Context(ctx).Do()
	if err != nil {
		return nil, err
	}
	d, err := mimeDecode(body.Data)
	if err != nil {
		return nil, err
	}
	return []byte(d), nil
}

type Message struct {
	m       sync.RWMutex
	conn    *CmdG
	level   DataLevel
	headers map[string]string

	ID           string
	body         string // Printable body.
	originalBody string
	gpgStatus    *gpg.Status
	Response     *gmail.Message

	attachments []*Attachment
}

func (msg *Message) Attachments(ctx context.Context) ([]*Attachment, error) {
	if err := msg.Preload(ctx, LevelFull); err != nil {
		return nil, err
	}
	msg.m.RLock()
	defer msg.m.RUnlock()
	return msg.attachments, nil
}

func (msg *Message) Raw(ctx context.Context) (string, error) {
	m, err := msg.conn.gmail.Users.Messages.Get(email, msg.ID).Format(levelRaw).Context(ctx).Do()
	if err != nil {
		return "", err
	}
	dec, err := mimeDecode(m.Raw)
	if err != nil {
		return "", err
	}
	return dec, nil
}

// called with lock held
func (msg *Message) annotateAttachments() error {
	var bodystr []string
	for _, p := range msg.Response.Payload.Parts {
		if !partIsAttachment(p) {
			continue
		}
		msg.attachments = append(msg.attachments, &Attachment{
			MsgID: msg.ID,
			ID:    p.Body.AttachmentId,
			Part:  p,
			conn:  msg.conn,
		})
		bodystr = append(bodystr, fmt.Sprintf("%s\n<<<Attachment %q; press 't' to view>>>", display.Bold, p.Filename))
	}
	msg.body += strings.Join(bodystr, "\n")
	return nil
}

func (m *Message) GPGStatus() *gpg.Status {
	return m.gpgStatus
}

func NewMessage(c *CmdG, msgID string) *Message {
	return c.MessageCache(&Message{
		conn: c,
		ID:   msgID,
	})
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
	return m.HasLabel(Unread)
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

func (m *Message) RemoveLabelID(ctx context.Context, labelID string) error {
	st := time.Now()
	_, err := m.conn.gmail.Users.Messages.Modify(email, m.ID, &gmail.ModifyMessageRequest{
		RemoveLabelIds: []string{labelID},
	}).Context(ctx).Do()
	if err != nil {
		return errors.Wrapf(err, "removing label ID %q from %q", labelID, m.ID)
	}
	log.Infof("Removed label ID %q from %q: %v", labelID, m.ID, time.Since(st))
	if err := m.ReloadLabels(ctx); err != nil {
		return errors.Wrapf(err, "reloading labels from %q", m.ID)
	}
	return err
}

func (m *Message) AddLabelID(ctx context.Context, labelID string) error {
	st := time.Now()
	_, err := m.conn.gmail.Users.Messages.Modify(email, m.ID, &gmail.ModifyMessageRequest{
		AddLabelIds: []string{labelID},
	}).Context(ctx).Do()
	if err != nil {
		return errors.Wrapf(err, "removing label ID %q from %q", labelID, m.ID)
	}
	log.Infof("Added label ID %q to %q: %v", labelID, m.ID, time.Since(st))
	if err := m.ReloadLabels(ctx); err != nil {
		return errors.Wrapf(err, "reloading labels from %q", m.ID)
	}
	return err
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

func (m *Message) GetReplyTo(ctx context.Context) (string, error) {
	s, err := m.GetHeader(ctx, "Reply-To")
	if err == nil && s != "" {
		return s, nil
	}
	return m.GetHeader(ctx, "From")
}

func (m *Message) GetReplyToAll(ctx context.Context) (string, string, error) {
	to, err := m.GetReplyTo(ctx)
	if err != nil {
		return "", "", err
	}
	cc := []string{}
	if f, err := m.GetHeader(ctx, "From"); err != nil {
		return "", "", err
	} else if f != to {
		cc = append(cc, f)
	}
	if c, err := m.GetHeader(ctx, "CC"); err != nil && len(c) != 0 {
		cc = append(cc, c)
	}
	return to, strings.Join(cc, ", "), err
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

type Label struct {
	ID       string
	Label    string
	Response *gmail.Label
}

func (m *Message) GetLabels(ctx context.Context, withUnread bool) ([]*Label, error) {
	if err := m.Preload(ctx, LevelMinimal); err != nil {
		return nil, err
	}
	var ret []*Label
	for _, l := range m.Response.LabelIds {
		if l == Unread {
			continue
		}
		l2 := &Label{
			ID:    l,
			Label: "<unknown>",
		}
		ret = append(ret, m.conn.LabelCache(l2))
	}
	return ret, nil
}

// Return labels as a printable string. With colors, but without "UNREAD".
func (m *Message) GetLabelsString(ctx context.Context) (string, error) {
	var s []string
	ls, err := m.GetLabels(ctx, false)
	if err != nil {
		return "", err
	}
	for _, l := range ls {
		s = append(s, l.Label)
	}
	return strings.Join(s, ", "), nil
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
	ts = ts.Local()
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
		return stripUnprintable(h), nil
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

var errNoUsablePart = fmt.Errorf("could not find message part usable as message body")

func (m *Message) makeBody(ctx context.Context, part *gmail.MessagePart) (string, error) {
	if len(part.Parts) == 0 {
		log.Infof("Single part body of type %q with input len %d", part.MimeType, len(part.Body.Data))
		data, err := mimeDecode(string(part.Body.Data))
		data = stripUnprintable(data)
		// log.Infof("… contents is %q", data)
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
		case "multipart/alternative":
			return m.makeBody(ctx, p)
		default:
			log.Infof("Ignoring part of type %q", p.MimeType)
		}
	}

	// Could not find any part of message to use as body.
	return "", errNoUsablePart
}

func (m *Message) GetBody(ctx context.Context) (string, error) {
	if err := m.Preload(ctx, LevelFull); err != nil {
		return "", err
	}
	return m.body, nil
}

func (m *Message) GetUnpatchedBody(ctx context.Context) (string, error) {
	if err := m.Preload(ctx, LevelFull); err != nil {
		return "", err
	}
	return m.originalBody, nil
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
	// https://tools.ietf.org/html/rfc3156
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
	st, err := GPG.Verify(ctx, dec, sigDec)
	if err != nil {
		return err
	}
	m.gpgStatus = st
	return nil
}

var inlineGPG = regexp.MustCompile(`(?sm)(-----BEGIN PGP SIGNED MESSAGE-----.*-----BEGIN PGP SIGNATURE-----.*-----END PGP SIGNATURE-----)`)

func (m *Message) tryGPGInlineSigned(ctx context.Context) error {
	var e2 error
	b2 := inlineGPG.ReplaceAllStringFunc(m.body, func(in string) string {
		st, err := GPG.VerifyInline(ctx, in)
		if err != nil {
			e2 = err
			return in
		}
		// Don't set m.gpgStatus because that'd make it look like the whole message is green.
		if !st.GoodSignature {
			e2 = fmt.Errorf("signature is there, but not 'good'")
			return in
		}
		return fmt.Sprintf("%[1]sBEGIN message signed by %[2]s%[4]s\n%[3]s\n%[1]sEND message signed by %[2]s%[4]s", display.Green, st.Signed, in, display.Reset)
	})
	if e2 != nil {
		return e2
	}
	m.body = b2
	return nil
}

func (m *Message) tryGPGEncrypted(ctx context.Context) error {
	// https://tools.ietf.org/html/rfc3156
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
	dec2, status, err := GPG.Decrypt(ctx, dec)
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

func (m *Message) Reload(ctx context.Context, level DataLevel) error {
	return m.load(ctx, level)
}

func (m *Message) Preload(ctx context.Context, level DataLevel) error {
	if m.HasData(level) {
		return nil
	}
	return m.load(ctx, level)
}

func (m *Message) load(ctx context.Context, level DataLevel) error {
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
		if err != nil && err != errNoUsablePart {
			return err
		}
		if err := m.tryGPGEncrypted(ctx); err != nil {
			m.body = fmt.Sprintf("%sDecrypting GPG: %v%s", display.Red, err, display.Grey)
		}
		if err := m.tryGPGSigned(ctx); err != nil {
			log.Errorf("Checking GPG signature: %v", err)
		}
		m.originalBody = m.body
		if err := m.tryGPGInlineSigned(ctx); err != nil {
			log.Errorf("Checking GPG inline signature: %v", err)
		}
		if err := m.annotateAttachments(); err != nil {
			log.Errorf("Failed to annotate attachments: %v", err)
		}
	}
	return nil
}
