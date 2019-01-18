package cmdg

import (
	"context"
	"net/http"
	"sync"

	"github.com/ThomasHabets/drive-du/lib"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	gmail "google.golang.org/api/gmail/v1"
)

const (
	version   = "0.4"
	userAgent = "cmdg med " + version
	scope     = "https://www.googleapis.com/auth/gmail.modify https://www.google.com/m8/feeds"
	pageSize  = 10

	accessType = "offline"
	email      = "me"

	// Messages.Get()
	levelEmpty    dataLevel = ""         // Nothing
	levelMinimal  dataLevel = "minimal"  // ID, labels
	levelMetadata dataLevel = "metadata" // ID, labels, headers
	levelFull     dataLevel = "full"     // ID, labels, headers, payload
	// DO NOT USE: levelRaw      dataLevel = "raw"
)

type (
	dataLevel string
)

type CmdG struct {
	m            sync.Mutex
	authedClient *http.Client
	gmail        *gmail.Service
	messageCache map[string]*Message
}

func (c *CmdG) MessageCache(msgID string) (*Message, bool) {
	c.m.Lock()
	defer c.m.Unlock()
	t, f := c.messageCache[msgID]
	return t, f
}

func New(fn string) (*CmdG, error) {
	conn := &CmdG{
		messageCache: make(map[string]*Message),
	}

	// Read config.
	conf, err := lib.ReadConfig(fn)
	if err != nil {
		return nil, errors.Wrap(err, "reading config")
	}

	// Connect.
	conn.authedClient, err = lib.Connect(conf.OAuth, scope, accessType)
	if err != nil {
		return nil, errors.Wrap(err, "failed to connect to gmail")
	}

	// Set up client.
	conn.gmail, err = gmail.New(conn.authedClient)
	if err != nil {
		return nil, err
	}
	conn.gmail.UserAgent = userAgent
	return conn, nil
}

func (c *CmdG) ListMessages(ctx context.Context, label, token string) (*Page, error) {
	nres := int64(pageSize)
	q := c.gmail.Users.Messages.List(email).
		PageToken(token).
		MaxResults(int64(nres)).
		Context(ctx).
		Fields("messages,resultSizeEstimate,nextPageToken").
		LabelIds("INBOX")
	res, err := q.Do()
	if err != nil {
		return nil, errors.Wrap(err, "listing messages")
	}
	log.Infof("Next page token: %q", res.NextPageToken)
	p := &Page{
		conn:     c,
		Label:    label,
		Response: res,
	}
	for _, m := range res.Messages {
		p.Messages = append(p.Messages, NewMessage(c, m.Id))
	}
	return p, nil
}
