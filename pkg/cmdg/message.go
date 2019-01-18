package cmdg

import (
	"context"
	"fmt"
	"net/mail"
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

func (m *Message) GetTimeFmt(ctx context.Context) (string, error) {
	s, err := m.GetHeader(ctx, "Date")
	if err != nil {
		return "", err
	}
	ts, err := parseTime(s)
	if err != nil {
		return "", err
	}
	ts.Local()
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
	log.Debugf("Downloading message %q level %q took %v", m.ID, level, time.Since(st))
	m.m.Lock()
	defer m.m.Unlock()
	m.Response = msg
	m.level = level
	m.headers = make(map[string]string)
	for _, h := range m.Response.Payload.Headers {
		m.headers[strings.ToLower(h.Name)] = h.Value
	}
	return err
}
