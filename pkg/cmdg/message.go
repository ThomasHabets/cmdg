package cmdg

import (
	"context"
	"fmt"
	"strings"
	"sync"

	gmail "google.golang.org/api/gmail/v1"
)

type Message struct {
	m       sync.RWMutex
	conn    *CmdG
	level   dataLevel
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
func (m *Message) hasData(level dataLevel) bool {
	switch m.level {
	case levelFull:
		return true
	case levelMetadata:
		return level != levelFull
	case levelMinimal:
		return levelMinimal != ""
	case levelEmpty:
		return false
	}
	panic(fmt.Sprintf("can't happen: current level is %q", m.level))
}

func (m *Message) IsUnread() bool {
	return m.HasLabel("UNREAD")
}

func (m *Message) HasLabel(label string) bool {
	// TODO: this only works for label IDs.
	for _, l := range m.Response.LabelIds {
		if label == l {
			return true
		}
	}
	return false
}

func (m *Message) GetHeader(ctx context.Context, k string) (string, error) {
	if err := m.Preload(ctx, levelMetadata); err != nil {
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
		Format(string(levelMinimal)).
		Context(ctx).
		Do()
	m.m.Lock()
	defer m.m.Unlock()
	if m.Response == nil {
		m.Response = msg
		m.level = levelMinimal
	} else {
		m.Response.LabelIds = msg.LabelIds
	}
	return err
}

func (m *Message) Preload(ctx context.Context, level dataLevel) error {
	{
		m.m.RLock()
		has := m.hasData(level)
		m.m.RUnlock()
		if has {
			return nil
		}
	}

	msg, err := m.conn.gmail.Users.Messages.Get(email, m.ID).
		Format(string(level)).
		Context(ctx).
		Do()
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
