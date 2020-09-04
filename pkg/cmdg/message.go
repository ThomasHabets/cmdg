package cmdg

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"io/ioutil"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"os/exec"
	"regexp"
	"runtime/debug"
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

// Special labels.
const (
	Inbox   = "INBOX"
	Trash   = "TRASH"
	Unread  = "UNREAD"
	Starred = "STARRED"
)

const (
	tmpfilePattern = "cmdg-*"

	defaultInboxBG = "#ffffff"
	defaultInboxFG = "#000000"
)

var (
	// GPG is the handle to a GPG config.
	GPG *gpg.GPG

	// Lynx is the executable to use as web browser to use to render HTML to text.
	Lynx = "lynx"

	// Openssl is the executable is used to verify some signatures.
	Openssl = "openssl"

	// ErrMissing is used e.g. if a header is not present. As opposed to malformed.
	ErrMissing = fmt.Errorf("resource missing")
)

// Attachment is an attachment.
type Attachment struct {
	ID       string
	MsgID    string
	conn     *CmdG
	contents []byte
	Part     *gmail.MessagePart
}

// Download downloads an attachment.
func (a *Attachment) Download(ctx context.Context) ([]byte, error) {
	if a.contents != nil {
		return a.contents, nil
	}
	var body *gmail.MessagePartBody
	err := wrapLogRPC("gmail.Users.Messages.Attachments.Get", func() (err error) {
		body, err = a.conn.gmail.Users.Messages.Attachments.Get(email, a.MsgID, a.ID).Context(ctx).Do()
		return
	}, "email=%q msg=%v attachment=%v", email, a.MsgID, a.ID)
	if err != nil {
		return nil, err
	}
	d, err := MIMEDecode(body.Data)
	if err != nil {
		return nil, err
	}
	return []byte(d), nil
}

// Message is an email message.
type Message struct {
	m       sync.RWMutex
	conn    *CmdG
	level   DataLevel
	headers map[string]string

	ID           string
	body         string // Printable body.
	bodyHTML     string
	originalBody string
	gpgStatus    *gpg.Status
	Response     *gmail.Message

	raw         string
	attachments []*Attachment
}

// ThreadID returns the thread ID of the message.
func (msg *Message) ThreadID(ctx context.Context) (ThreadID, error) {
	if err := msg.Preload(ctx, LevelMinimal); err != nil {
		return NewThread, err
	}
	msg.m.RLock()
	defer msg.m.RUnlock()
	return ThreadID(msg.Response.ThreadId), nil
}

// Attachments returns a list of attachments.
func (msg *Message) Attachments(ctx context.Context) ([]*Attachment, error) {
	if err := msg.Preload(ctx, LevelFull); err != nil {
		return nil, err
	}
	msg.m.RLock()
	defer msg.m.RUnlock()
	return msg.attachments, nil
}

// Raw returns the raw message.
func (msg *Message) Raw(ctx context.Context) (string, error) {
	msg.m.Lock()
	defer msg.m.Unlock()
	return msg.rawNoLock(ctx)
}

func (msg *Message) rawNoLock(ctx context.Context) (string, error) {
	// Check cache.
	if msg.raw != "" {
		return msg.raw, nil
	}

	var m *gmail.Message
	err := wrapLogRPC("gmail.Users.Messages.Get", func() (err error) {
		m, err = msg.conn.gmail.Users.Messages.Get(email, msg.ID).Format(levelRaw).Context(ctx).Do()
		return
	}, "email=%q msg=%v level=%s", email, msg.ID, levelRaw)
	if err != nil {
		return "", err
	}
	dec, err := MIMEDecode(m.Raw)
	if err != nil {
		return "", err
	}
	msg.raw = dec
	return msg.raw, nil
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

// GPGStatus returns an annotated GPG status.
func (msg *Message) GPGStatus() *gpg.Status {
	return msg.gpgStatus
}

// NewMessage creates a new message.
func NewMessage(c *CmdG, msgID string) *Message {
	return c.MessageCache(&Message{
		conn: c,
		ID:   msgID,
	})
}

// NewMessageWithResponse creates a new message from data already received from the gmail API.
func NewMessageWithResponse(c *CmdG, msgID string, resp *gmail.Message, level DataLevel) *Message {
	m := NewMessage(c, msgID)
	m.Response = resp
	m.level = level
	return m
}

func hasData(has, want DataLevel) bool {
	switch has {
	case LevelFull:
		return true
	case LevelMetadata:
		return want != LevelFull
	case LevelMinimal:
		return (want != LevelFull) && (want != LevelMetadata)
	case LevelEmpty:
		return want == LevelEmpty
	}
	panic(fmt.Sprintf("can't happen: current level is %q, want %q", has, want))
}

// HasData returns if the message has at least the given level.
func (msg *Message) HasData(level DataLevel) bool {
	msg.m.RLock()
	defer msg.m.RUnlock()
	return hasData(msg.level, level)
}

// IsUnread returns if the UNREAD label is set.
func (msg *Message) IsUnread() bool {
	return msg.HasLabel(Unread)
}

// HasLabel checks for a given labelID.
func (msg *Message) HasLabel(labelID string) bool {
	msg.m.Lock()
	defer msg.m.Unlock()
	if msg.Response == nil {
		return false
	}
	for _, l := range msg.Response.LabelIds {
		if labelID == l {
			return true
		}
	}
	return false
}

// RemoveLabelID removes a label.
func (msg *Message) RemoveLabelID(ctx context.Context, labelID string) error {
	var nm *gmail.Message
	st := time.Now()
	err := wrapLogRPC("gmail.Users.Messages.Modify", func() (err error) {
		nm, err = msg.conn.gmail.Users.Messages.Modify(email, msg.ID, &gmail.ModifyMessageRequest{
			RemoveLabelIds: []string{labelID},
		}).Context(ctx).Do()
		return
	}, "%q msg=%v remove_labelID=%v", email, msg.ID, labelID)
	if err != nil {
		return errors.Wrapf(err, "removing label ID %q from %q", labelID, msg.ID)
	}

	log.Infof("Removed label ID %q from %q. Now %q: %v", labelID, msg.ID, nm.LabelIds, time.Since(st))

	msg.m.Lock()
	defer msg.m.Unlock()
	if msg.Response == nil {
		msg.Response = nm
	} else {
		msg.Response.LabelIds = nm.LabelIds
	}
	return nil
}

// AddLabelIDLocal adds a local label to the local cache *only*. It'll be overwritten at next sync.
// It's used for faster UI response time on label adding.
func (msg *Message) AddLabelIDLocal(labelID string) {
	msg.m.Lock()
	defer msg.m.Unlock()
	if msg.Response == nil {
		return
	}
	for _, l := range msg.Response.LabelIds {
		if l == labelID {
			return
		}
	}
	msg.Response.LabelIds = append(msg.Response.LabelIds, labelID)
}

// RemoveLabelIDLocal removes a local label from the local cache *only*. It'll be overwritten at next sync.
// It's used for faster UI response time on label removing.
func (msg *Message) RemoveLabelIDLocal(labelID string) {
	if msg.Response == nil {
		return
	}
	nl := make([]string, len(msg.Response.LabelIds))
	msg.m.Lock()
	defer msg.m.Unlock()
	for _, l := range msg.Response.LabelIds {
		if l != labelID {
			nl = append(nl, l)
		}
	}
	msg.Response.LabelIds = nl
}

// LocalLabels returns the label IDs, whatever they are. If we have not downloaded anything then empty list is returned.
func (msg *Message) LocalLabels() []string {
	if msg.Response == nil {
		return nil
	}
	return msg.Response.LabelIds
}

// AddLabelID adds a label to a message.
func (msg *Message) AddLabelID(ctx context.Context, labelID string) error {
	st := time.Now()
	var nm *gmail.Message
	err := wrapLogRPC("gmail.Users.Messages.Modify", func() (err error) {
		nm, err = msg.conn.gmail.Users.Messages.Modify(email, msg.ID, &gmail.ModifyMessageRequest{
			AddLabelIds: []string{labelID},
		}).Context(ctx).Do()
		return
	}, "email=%q msg=%v add_labelID=%v", email, msg.ID, labelID)
	if err != nil {
		return errors.Wrapf(err, "removing label ID %q from %q", labelID, msg.ID)
	}
	log.Infof("Added label ID %q to %q. Is now %q: %v", labelID, msg.ID, nm.LabelIds, time.Since(st))
	msg.m.Lock()
	defer msg.m.Unlock()
	if msg.Response == nil {
		msg.Response = nm
	} else {
		msg.Response.LabelIds = nm.LabelIds
	}
	return nil
}

// parseTime tries a few time formats and returns the one that works.
func parseTime(s string) (time.Time, error) {
	if t, err := mail.ParseDate(s); err == nil {
		return t, err
	}
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
		"Mon, 2 Jan 06 15:04:05 MST",
		"Mon, _2 Jan 06 15:04:05 MST",
		"2 Jan 06 15:04:05",
		"_2 Jan 06 15:04:05",
		"02 Jan 2006 15:04:05 -0700 (MST)",
		time.RFC1123Z,
	} {
		t, err = time.Parse(layout, s)
		if err == nil {
			break
		}
	}
	return t, err
}

// GetReferences returns a slice of references.
func (msg *Message) GetReferences(ctx context.Context) ([]string, error) {
	s, err := msg.GetHeader(ctx, "References")
	if err != nil {
		return nil, err
	}
	return []string{s}, nil
}

// GetReplyTo returns the address to use for replies as the `To` line.
func (msg *Message) GetReplyTo(ctx context.Context) (string, error) {
	s, err := msg.GetHeader(ctx, "Reply-To")
	if err == nil && s != "" {
		return s, nil
	}
	return msg.GetHeader(ctx, "From")
}

func filteredEmails(from string, cc map[string]bool) []string {
	var ret []string
	fa, err := mail.ParseAddress(from)
	if err != nil {
		log.Errorf("Failed to parse 'from' address %q: %v", from, err)
		fa = &mail.Address{ // Dummy entry.
			Address: "",
		}
	}
	seen := map[string]bool{
		fa.Address: true,
	}
	for s := range cc {
		a, err := mail.ParseAddress(s)
		if err != nil {
			log.Errorf("Failed to parse 'cc' address %q: %v", s, err)
			ret = append(ret, s)
			continue
		}
		if !seen[a.Address] {
			ret = append(ret, s)
			seen[a.Address] = true
		}
	}
	return ret
}

// GetReplyToAll returns both To and CC lines for reply-all.
func (msg *Message) GetReplyToAll(ctx context.Context) (string, string, error) {
	from, err := msg.GetReplyTo(ctx)
	if err != nil {
		return "", "", err
	}
	cc := make(map[string]bool)
	if f, err := msg.GetHeader(ctx, "From"); err != nil {
		return "", "", err
	} else if f != from {
		cc[f] = true
	}
	if c, err := msg.GetHeader(ctx, "CC"); err == nil && len(c) != 0 {
		cc[c] = true
	}
	if c, err := msg.GetHeader(ctx, "To"); err == nil && len(c) != 0 {
		// TODO: if this is not "me"
		cc[c] = true
	}
	return from, strings.Join(filteredEmails(from, cc), ", "), err
}

// GetFrom returns email address (not name) of sender.
func (msg *Message) GetFrom(ctx context.Context) (string, error) {
	s, err := msg.GetHeader(ctx, "From")
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

// Label is a gmail label.
type Label struct {
	ID       string
	Label    string
	Response *gmail.Label
	m        sync.Mutex
}

// LabelString is the string of the label.
func (l *Label) LabelString() string {
	c := l.LabelColor()
	l.m.Lock()
	defer l.m.Unlock()
	if l.Response == nil {
		// This should not be possible.
		log.Errorf("Label response is nil for label ID %q", l.ID)
		debug.PrintStack()
		return fmt.Sprintf("<Internal error: label response nil for label ID %q>", l.ID)
	}
	if c == "" {
		c = display.Normal
	}
	return fmt.Sprintf("%s%s%s", c, l.Label, display.Normal)
}

// LabelColor returns an ANSI escape to render this label's color.
func (l *Label) LabelColor() string {
	l.m.Lock()
	defer l.m.Unlock()
	if l.Response == nil {
		return ""
	}
	var c string
	if l.Response.Color == nil {
		if l.ID == Inbox {
			c = colorMap(defaultInboxFG, defaultInboxBG)
		} else {
			return ""
		}
	} else {
		c = colorMap(l.Response.Color.TextColor, l.Response.Color.BackgroundColor)
	}
	return c
}

// LabelColorChar returns a full string to render just one char wide label.
func (l *Label) LabelColorChar() string {
	c := l.LabelColor()
	if c == "" {
		return ""
	}
	l.m.Lock()
	defer l.m.Unlock()
	// TODO: use first *character*, not just first byte.
	return fmt.Sprintf("%s%c", c, l.Label[0])
}

// GetLabelColors returns two strings: Labels with just colors, and one with the label strings in those colors.
func (msg *Message) GetLabelColors(ctx context.Context, exclude string) (string, string, error) {
	ls, err := msg.GetLabels(ctx, false)
	if err != nil {
		return "", "", err
	}
	var ret1, ret2 []string
	for _, l := range ls {
		if l.ID == exclude {
			continue
		}
		lc := l.LabelColorChar()
		if lc != "" {
			ret1 = append(ret1, lc)
			ret2 = append(ret2, l.LabelString())
		}
	}
	return strings.Join(ret1, ""), strings.Join(ret2, " "), nil
}

// GetLabels returns all labels for the message.
func (msg *Message) GetLabels(ctx context.Context, withUnread bool) ([]*Label, error) {
	if err := msg.Preload(ctx, LevelMinimal); err != nil {
		return nil, err
	}
	var ret []*Label
	for _, l := range msg.Response.LabelIds {
		if l == Unread {
			continue
		}
		l2 := &Label{
			ID:    l,
			Label: "<unknown>",
		}
		// Turn it into a fully lod label, if possible.
		l3 := msg.conn.LabelCache(l2)

		// Not loaded. Load it.
		if l3.Response == nil {
			log.Infof("Late loading of label ID %q", l)
			var l4 *gmail.Label
			err := wrapLogRPC("gmail.Users.Messages.Labels.Get", func() (err error) {
				l4, err = msg.conn.gmail.Users.Labels.Get(email, l).Context(ctx).Do()
				return
			}, "email=%q labelID=%v", email, l)
			if err != nil {
				log.Errorf("Failed to fetch label ID %q: %v", l, err)
			}
			l3.m.Lock()
			l3.Response = l4
			l3.m.Unlock()
		}
		ret = append(ret, l3)
	}
	return ret, nil
}

func colorMap(fgs, bgs string) string {
	// Textcolor.
	textColorMap := map[string]int{
		// Shades of grey.
		"#000000": 232,
		"#434343": 240,
		"#666666": 238,
		"#999999": 248,
		"#cccccc": 240,
		"#efefef": 240,
		"#f3f3f3": 240,
		"#ffffff": 255,

		"#4986e7": 21, // NON-STANDARD blue

		"#fb4c2f": 9,   // Orange-ish.
		"#ffad46": 208, // NON-STANDARD orange
		"#ffad47": 240, // Yellow-orange
		"#fad165": 240, // Yellow

		"#16a766": 240, // Green-ish.
		"#16a765": 28,  // NON-STANDARD green.
		"#43d692": 240, // Lighter puke-green.

		"#4a86e8": 240, // Light blue
		"#a479e2": 240, // Purple
		"#f691b3": 240, // Pink
		"#f6c5be": 240, // Pig-pink
		"#ffe6c7": 240, // White-yellow
		"#fef1d1": 240, // Even lighter.
		"#b9e4d0": 240, // Puke-green.
		"#c6f3de": 200,
		"#c9daf8": 200,
		"#e4d7f5": 200,
		"#fcdee8": 200,
		"#efa093": 200,
		"#ffd6a2": 200,
		"#fce8b3": 200,
		"#89d3b2": 200,
		"#a0eac9": 200,
		"#a4c2f4": 200,
		"#d0bcf1": 200,
		"#fbc8d9": 200,
		"#e66550": 200,
		"#ffbc6b": 200,
		"#fcda83": 200,
		"#44b984": 200,
		"#68dfa9": 200,
		"#6d9eeb": 200,
		"#b694e8": 200,
		"#f7a7c0": 200,
		"#cc3a21": 200,
		"#eaa041": 200,
		"#f2c960": 200,
		"#149e60": 200,
		"#3dc789": 200,
		"#3c78d8": 200,
		"#8e63ce": 200,
		"#e07798": 200,
		"#ac2b16": 200,
		"#cf8933": 200,
		"#d5ae49": 200,
		"#0b804b": 200,
		"#2a9c68": 200,
		"#285bac": 200,
		"#653e9b": 200,
		"#b65775": 200,
		"#822111": 200,
		"#a46a21": 200,
		"#aa8831": 200,
		"#076239": 200,
		"#1a764d": 200,
		"#1c4587": 200,
		"#41236d": 200,
		"#83334c": 200,

		"#711a36": 52,  // NON-STANDARD maroon.
		"#fbd3e0": 205, // NON-STANDARD pink.
		"#fbe983": 11,  // NON-STANDARD yellow.
		"#594c05": 58,  // NON-STANDARD dark yellow.
		"#b3efd3": 79,  // NON-standard light greenish
		"#0b4f30": 22,  // NON-standard green
	}

	fg, found := textColorMap[fgs]
	if !found {
		log.Infof("Could not find foreground %q", fgs)
		fg = 50
	}
	bg, found := textColorMap[bgs]
	if !found {
		log.Infof("Could not find background %q", bgs)
		bg = 200
	}
	return fmt.Sprintf("\033[38;5;%dm\033[48;5;%dm", fg, bg)
}

// GetLabelsString returns labels as a printable string. With colors, but without "UNREAD".
func (msg *Message) GetLabelsString(ctx context.Context) (string, error) {
	var s []string
	ls, err := msg.GetLabels(ctx, false)
	if err != nil {
		return "", err
	}
	for _, l := range ls {
		s = append(s, l.LabelString())
	}
	return strings.Join(s, ", "), nil
}

// GetOriginalTime returns the timestamp as claimed by the headers in its original timezone.
func (msg *Message) GetOriginalTime(ctx context.Context) (time.Time, error) {
	s, err := msg.GetHeader(ctx, "Date")
	if err != nil {
		return time.Time{}, err
	}
	ts, err := parseTime(s)
	if err != nil {
		return time.Time{}, err
	}
	return ts, err
}

// GetTime returns the message's time in the local timezone.
func (msg *Message) GetTime(ctx context.Context) (time.Time, error) {
	ts, err := msg.GetOriginalTime(ctx)
	ts = ts.Local()
	return ts, err
}

// GetTimeFmt returns a `time` format string appropriate for the age of the message.
func (msg *Message) GetTimeFmt(ctx context.Context) (string, error) {
	ts, err := msg.GetTime(ctx)
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

// GetHeader returns a header.
func (msg *Message) GetHeader(ctx context.Context, k string) (string, error) {
	if err := msg.Preload(ctx, LevelMetadata); err != nil {
		return "", err
	}
	h, ok := msg.headers[strings.ToLower(k)]
	if ok {
		return stripUnprintable(h), nil
	}
	return "", errors.Wrapf(ErrMissing, "header not found in msg %q: %q", msg.ID, k)
}

// MIMEEncode does mime decode for gmail. Seems to be special version of base64.
func MIMEEncode(s string) string {
	s = base64.StdEncoding.EncodeToString([]byte(s))
	s = strings.Replace(s, "+", "-", -1)
	s = strings.Replace(s, "/", "_", -1)
	return s
}

// MIMEDecode does mime encode for fmail. Seems to be a special version of base64.
func MIMEDecode(s string) (string, error) {
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

func htmlRender(ctx context.Context, s string) (string, error) {
	var stdout bytes.Buffer
	st := time.Now()
	cmd := exec.CommandContext(ctx, Lynx, "-dump", "-stdin")
	cmd.Stdin = strings.NewReader(s)
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return "", err
	}
	log.Infof("Rendered HTML in %v", time.Since(st))
	return fmt.Sprintf("%sRendered HTML%s\n%s", display.Blue, display.Reset, stdout.String()), nil
}

var errNoUsablePart = fmt.Errorf("could not find message part usable as message body")

// makeBodyAlt takes a multipart and tries to render the best thing it can from it.
func makeBodyAlt(ctx context.Context, part *gmail.MessagePart, preferHTML bool) (string, error) {
	wantT := "text/plain"
	acceptT := "text/html"
	if preferHTML {
		wantT, acceptT = acceptT, wantT
	}

	var ret []string
	var alt []string
	for _, p := range part.Parts {
		if partIsAttachment(p) {
			continue
		}
		dec, err := MIMEDecode(string(p.Body.Data))
		if err != nil {
			return "", err
		}

		if p.MimeType == "text/html" {
			dec, err = htmlRender(ctx, dec)
			if err != nil {
				return "", errors.Wrapf(err, "rendering HTML")
			}
		}

		log.Debugf("Alt mimetype: %q", p.MimeType)
		switch p.MimeType {
		case wantT:
			if len(strings.Trim(dec, "\n\r \t")) > 0 {
				ret = append(ret, dec)
			}
		case acceptT:
			if len(strings.Trim(dec, "\n\r \t")) > 0 {
				alt = append(alt, dec)
			}
		case "multipart/alternative", "multipart/related", "multipart/signed", "multipart/mixed":
			t, err := makeBodyAlt(ctx, p, preferHTML)
			if err != nil {
				return "", err
			}
			// However it was rendered it should be rendered.
			ret = append(ret, t)
			alt = append(alt, t)
		case "application/pkcs7-signature":
			// Ignored for now.
		default:
			log.Warningf("Unknown mimetype in alt: %q", p.MimeType)
		}
	}
	if len(ret) > 0 {
		return strings.Join(ret, "\n"), nil
	}
	return strings.Join(alt, "\n"), nil
}

func makeBody(ctx context.Context, part *gmail.MessagePart, preferHTML bool) (string, error) {
	if len(part.Parts) == 0 {
		log.Infof("Single part body of type %q with input len %d", part.MimeType, len(part.Body.Data))
		data, err := MIMEDecode(string(part.Body.Data))
		if err != nil {
			return "", err
		}

		data = stripUnprintable(data)
		if part.MimeType == "text/html" {
			var err error
			data, err = htmlRender(ctx, data)
			if err != nil {
				return "", errors.Wrapf(err, "rendering HTML")
			}
		}
		return data, nil
	}

	log.Infof("Message is type %q", part.MimeType)
	return makeBodyAlt(ctx, part, preferHTML)
}

// GetBody returns the message body.
func (msg *Message) GetBody(ctx context.Context) (string, error) {
	if err := msg.Preload(ctx, LevelFull); err != nil {
		return "", err
	}
	return msg.body, nil
}

// GetBodyHTML returns the message's HTML body.
func (msg *Message) GetBodyHTML(ctx context.Context) (string, error) {
	if err := msg.Preload(ctx, LevelFull); err != nil {
		return "", err
	}
	return msg.bodyHTML, nil
}

// GetUnpatchedBody returns the raw body, before fixups.
func (msg *Message) GetUnpatchedBody(ctx context.Context) (string, error) {
	if err := msg.Preload(ctx, LevelFull); err != nil {
		return "", err
	}
	return msg.originalBody, nil
}

// ReloadLabels reloads label data for the message.
func (msg *Message) ReloadLabels(ctx context.Context) error {
	log.Debugf("Reloading labels of %q %s", msg.ID, string(debug.Stack()))
	var msg2 *gmail.Message
	err := wrapLogRPC("gmail.Users.Messages.Get", func() (err error) {
		msg2, err = msg.conn.gmail.Users.Messages.Get(email, msg.ID).
			Format(string(LevelMinimal)).
			Context(ctx).
			Do()
		return
	}, "email=%q msgID=%v level=%s", email, msg.ID, LevelMinimal)
	if err != nil {
		return err
	}
	msg.m.Lock()
	defer msg.m.Unlock()
	if msg.Response == nil {
		msg.Response = msg2
		msg.level = LevelMinimal
	} else {
		msg.Response.LabelIds = msg2.LabelIds
	}
	return nil
}

// try verifying any signatures.
// CALLED WITH MUTEX HELD
func (msg *Message) trySigned(ctx context.Context) error {
	// https://tools.ietf.org/html/rfc3156
	if msg.Response.Payload.MimeType != "multipart/signed" {
		return nil
	}
	var partSig *gmail.MessagePart
	var dec string
	for _, p := range msg.Response.Payload.Parts {
		switch p.MimeType {
		case "text/plain":
			var err error
			dec, err = MIMEDecode(p.Body.Data)
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
			// GPG/PGP signed
			partSig = p
		case "application/x-pkcs7-signature":
			return msg.trySMIMESigned(ctx)
		default:
			log.Warningf("Found unexpected part in signed packet: %q", p.MimeType)
		}
	}

	if partSig == nil {
		return fmt.Errorf("no supported attached signature")
	}

	// Fetch attachment.
	var body *gmail.MessagePartBody
	err := wrapLogRPC("gmail.Users.Messages.Attachments.Get", func() (err error) {
		body, err = msg.conn.gmail.Users.Messages.Attachments.Get(email, msg.ID, partSig.Body.AttachmentId).Context(ctx).Do()
		return err
	}, "email=%q msgID=%v attachmentID=%v", email, msg.ID, partSig.Body.AttachmentId)
	if err != nil {
		return errors.Wrap(err, "failed to download signature attachment")
	}
	sigDec, err := MIMEDecode(body.Data)
	if err != nil {
		return errors.Wrap(err, "failed to MIME decode signature attachment")
	}
	st, err := GPG.Verify(ctx, dec, sigDec)
	if err != nil {
		return err
	}
	msg.gpgStatus = st
	return nil
}

var inlineGPG = regexp.MustCompile(`(?sm)(-----BEGIN PGP SIGNED MESSAGE-----.*-----BEGIN PGP SIGNATURE-----.*-----END PGP SIGNATURE-----)`)

func (msg *Message) tryGPGInlineSigned(ctx context.Context) error {
	var e2 error
	b2 := inlineGPG.ReplaceAllStringFunc(msg.body, func(in string) string {
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
	msg.body = b2
	return nil
}

func (msg *Message) tryGPGEncrypted(ctx context.Context) error {
	// https://tools.ietf.org/html/rfc3156
	if msg.Response.Payload.MimeType != "multipart/encrypted" {
		return nil
	}

	// Expect two subparts.
	var partMeta *gmail.MessagePart
	var partData *gmail.MessagePart
	for _, p := range msg.Response.Payload.Parts {
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
	var body *gmail.MessagePartBody
	err := wrapLogRPC("gmail.Users.Messages.Attachments.Get", func() (err error) {
		body, err = msg.conn.gmail.Users.Messages.Attachments.Get(email, msg.ID, partData.Body.AttachmentId).Context(ctx).Do()
		return
	}, "email=%q msgID=%v attachmentID=%v", email, msg.ID, partData.Body.AttachmentId)
	if err != nil {
		return errors.Wrap(err, "failed to download encrypted data attachment")
	}
	dec, err := MIMEDecode(body.Data)
	if err != nil {
		return errors.Wrap(err, "failed to MIME decode encrypted data attachment")
	}

	// Decrypt data attachment.
	dec2, status, err := GPG.Decrypt(ctx, dec)
	if err != nil {
		return err
	}

	msg2, err := mail.ReadMessage(strings.NewReader(dec2))
	if err != nil {
		return err
	}

	mediaType, params, err := mime.ParseMediaType(msg2.Header.Get("Content-Type"))
	if err != nil {
		return err
	}
	if strings.HasPrefix(mediaType, "multipart/") {
		log.Infof("Multipart encrypted with media type %q", mediaType)
		mr := multipart.NewReader(msg2.Body, params["boundary"])
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
						Data: MIMEEncode(string(t)),
					},
				}
				msg.body, err = makeBody(ctx, np, false)
				if err != nil {
					return errors.Wrap(err, "failed to decrypt")
				}
			} else {
				// TODO: handle attachment.
			}
		}

	} else {
		r, err := toUTF8Reader(map[string][]string(msg2.Header), msg2.Body)
		t, err := ioutil.ReadAll(r)
		if err != nil {
			return err
		}
		msg.body = string(t)
	}

	msg.gpgStatus = status
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

// Reload unconditionally reloads the message.
func (msg *Message) Reload(ctx context.Context, level DataLevel) error {
	return msg.load(ctx, level)
}

// Preload loads message data, unless it's already loaded.
func (msg *Message) Preload(ctx context.Context, level DataLevel) error {
	if msg.HasData(level) {
		return nil
	}
	return msg.load(ctx, level)
}

func (msg *Message) load(ctx context.Context, level DataLevel) error {
	st := time.Now()
	log.Debugf("Loading message %q at level %v, stack %s", msg.ID, level, string(debug.Stack()))
	var msg2 *gmail.Message
	err := wrapLogRPC("gmail.Users.Messages.Get", func() (err error) {
		msg2, err = msg.conn.gmail.Users.Messages.Get(email, msg.ID).
			Format(string(level)).
			Context(ctx).
			Do()
		return
	}, "email=%q msgID=%v level=%s", email, msg.ID, level)
	if err != nil {
		return err
	}
	log.Debugf("Downloading message %q level %q took %v", msg.ID, level, time.Since(st))

	msg.m.Lock()
	defer msg.m.Unlock()
	msg.Response = msg2
	msg.level = level
	msg.headers = make(map[string]string)
	for _, h := range msg.Response.Payload.Headers {
		msg.headers[strings.ToLower(h.Name)] = h.Value
	}
	if level == LevelFull {
		msg.bodyHTML, err = makeBody(ctx, msg.Response.Payload, true)
		if err != nil && err != errNoUsablePart {
			return err
		}
		// TODO: do GPG stuff to HTML?

		msg.body, err = makeBody(ctx, msg.Response.Payload, false)
		if err != nil && err != errNoUsablePart {
			return err
		}
		if err := msg.tryGPGEncrypted(ctx); err != nil {
			msg.body = fmt.Sprintf("%sDecrypting GPG: %v%s", display.Red, err, display.Grey)
		}
		if err := msg.trySigned(ctx); err != nil {
			log.Errorf("Checking GPG signature: %v", err)
		}
		msg.originalBody = msg.body
		if err := msg.tryGPGInlineSigned(ctx); err != nil {
			log.Errorf("Checking GPG inline signature: %v", err)
		}
		if err := msg.annotateAttachments(); err != nil {
			log.Errorf("Failed to annotate attachments: %v", err)
		}
	}
	return nil
}

// Draft is a draft.
type Draft struct {
	ID       string
	Response *gmail.Draft

	level   DataLevel
	headers map[string]string
	conn    *CmdG
	m       sync.RWMutex
	body    string
}

// NewDraft returns a new draft.
func NewDraft(c *CmdG, id string) *Draft {
	return &Draft{
		ID:   id,
		conn: c,
	}
}

// GetHeader retrieves a header.
func (d *Draft) GetHeader(ctx context.Context, h string) (string, error) {
	if err := d.load(ctx, LevelMetadata); err != nil {
		return "", err
	}
	d.m.RLock()
	defer d.m.RUnlock()
	return d.headers[strings.ToLower(h)], nil
}

// HasData returns if data at a given level is already loaded.
func (d *Draft) HasData(level DataLevel) bool {
	d.m.RLock()
	defer d.m.RUnlock()
	return hasData(d.level, level)
}

func (d *Draft) load(ctx context.Context, level DataLevel) error {
	if d.HasData(level) {
		return nil
	}
	log.Debugf("Loading draft %q at level %v %s", d.ID, level, string(debug.Stack()))
	var r *gmail.Draft
	if err := wrapLogRPC("gmail.User.Drafts.Get", func() (err error) {
		r, err = d.conn.gmail.Users.Drafts.Get(email, d.ID).Context(ctx).Format(string(level)).Do()
		return
	}, "email=%q msgID=%v level=%v", email, d.ID, level); err != nil {
		return err
	}
	d.m.Lock()
	defer d.m.Unlock()
	d.Response = r
	d.level = level
	d.headers = make(map[string]string)
	for _, h := range d.Response.Message.Payload.Headers {
		d.headers[strings.ToLower(h.Name)] = h.Value
	}
	if level == LevelFull {
		var err error
		d.body, err = makeBody(ctx, d.Response.Message.Payload, false)
		if err != nil {
			return errors.Wrap(err, "rendering draft body")
		}
	}
	return nil
}

// GetBody returns the body.
func (d *Draft) GetBody(ctx context.Context) (string, error) {
	if err := d.load(ctx, LevelFull); err != nil {
		return "", err
	}
	return d.body, nil
}

// UpdateParts updates a draft… right?
func (d *Draft) UpdateParts(ctx context.Context, head mail.Header, parts []*Part) error {
	//d.update(ctx,
	return fmt.Errorf("NOT IMPLEMENTED")
}

func (d *Draft) update(ctx context.Context, content string) error {
	if err := wrapLogRPC("gmail.Users.Drafts.Update", func() error {
		_, err := d.conn.gmail.Users.Drafts.Update(email, d.ID, &gmail.Draft{
			Message: &gmail.Message{
				Raw: MIMEEncode(content),
			},
		}).Context(ctx).Do()
		return err
	}, "email=%q msgID=%v contents=%q", email, d.ID, content); err != nil {
		return err
	}

	// Pretend we don't know anything about this draft anymore.
	d.m.Lock()
	d.level = LevelEmpty
	d.m.Unlock()
	return nil
}

// Send sends the draft. Sending a draft makes it no longer a draft.
func (d *Draft) Send(ctx context.Context) error {
	if err := d.load(ctx, LevelFull); err != nil {
		return errors.Wrap(err, "downloading draft for send")
	}
	return wrapLogRPC("gmail.USers.Drafts.Send", func() error {
		_, err := d.conn.gmail.Users.Drafts.Send(email, d.Response).Context(ctx).Do()
		return err
	}, "email=%q draftID=%v", email, d.ID)
}

// Delete deletes the draft.
func (d *Draft) Delete(ctx context.Context) error {
	return wrapLogRPC("gmail.Users.Drafts.Delete", func() error {
		return d.conn.gmail.Users.Drafts.Delete(email, d.ID).Context(ctx).Do()
	}, "email=%q draftID=%v", email, d.ID)
}
