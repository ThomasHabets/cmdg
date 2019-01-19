package cmdg

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	gmail "google.golang.org/api/gmail/v1"
)

type Message struct {
	m       sync.RWMutex
	conn    *CmdG
	level   DataLevel
	headers map[string]string

	ID       string
	body     string // Printable body.
	Response *gmail.Message
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
		data, err := mimeDecode(string(part.Body.Data))
		data = stripUnprintable(data)
		if err != nil {
			return "", err
		}
		return data, nil
	}

	for _, p := range part.Parts {
		if partIsAttachment(p) {
			continue
		}
		switch p.MimeType {
		case "text/plain":
			return m.makeBody(ctx, p)
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
	}
	return err
}

func (m *Message) Lines(ctx context.Context) (int, error) {
	if err := m.Preload(ctx, LevelFull); err != nil {
		return 0, err
	}
	return len(strings.Split(m.body, "\n")), nil
}
