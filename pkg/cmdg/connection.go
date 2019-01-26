package cmdg

import (
	"context"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"sort"
	"sync"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"golang.org/x/oauth2"
	gmail "google.golang.org/api/gmail/v1"
	"google.golang.org/api/googleapi/transport"
)

const (
	version   = "1.0"
	userAgent = "cmdg med " + version
	// Scope for email, contacts, and appdata.
	scope = "https://www.googleapis.com/auth/gmail.modify https://www.google.com/m8/feeds https://www.googleapis.com/auth/drive.appdata"

	pageSize = 100

	accessType = "offline"
	email      = "me"

	// Messages.Get()
	LevelEmpty    DataLevel = ""         // Nothing
	LevelMinimal  DataLevel = "minimal"  // ID, labels
	LevelMetadata DataLevel = "metadata" // ID, labels, headers
	LevelFull     DataLevel = "full"     // ID, labels, headers, payload

	// Not so much a level as a separate request. Type `string` so that it won't be usable as a `DataLevel`.
	levelRaw string = "RAW"
)

type (
	DataLevel string
)

type CmdG struct {
	m            sync.RWMutex
	authedClient *http.Client
	gmail        *gmail.Service
	messageCache map[string]*Message
	labelCache   map[string]*Label
	contacts     contacts
}

func (c *CmdG) MessageCache(msg *Message) *Message {
	c.m.Lock()
	defer c.m.Unlock()
	if t, found := c.messageCache[msg.ID]; found {
		return t
	}
	c.messageCache[msg.ID] = msg
	return msg
}

func (c *CmdG) LabelCache(label *Label) *Label {
	c.m.Lock()
	defer c.m.Unlock()
	if t, f := c.labelCache[label.ID]; f {
		return t
	}
	c.labelCache[label.ID] = label
	return label
}

func New(fn string) (*CmdG, error) {
	conn := &CmdG{
		messageCache: make(map[string]*Message),
		labelCache:   make(map[string]*Label),
	}

	// Read config.
	var conf Config
	{
		f, err := ioutil.ReadFile(fn)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(f, &conf); err != nil {
			return nil, errors.Wrapf(err, "unmarshalling config")
		}
	}

	// Attach APIkey, if any.
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, &http.Client{
		Transport: &transport.APIKey{Key: conf.OAuth.ApiKey},
	})

	// Connect.
	{
		token := &oauth2.Token{
			AccessToken:  conf.OAuth.AccessToken,
			RefreshToken: conf.OAuth.RefreshToken,
		}
		cfg := oauth2.Config{
			ClientID:     conf.OAuth.ClientID,
			ClientSecret: conf.OAuth.ClientSecret,
			Endpoint: oauth2.Endpoint{
				AuthURL:  "https://accounts.google.com/o/oauth2/auth",
				TokenURL: "https://accounts.google.com/o/oauth2/token",
			},
			Scopes:      []string{scope},
			RedirectURL: oauthRedirectOffline,
		}
		conn.authedClient = cfg.Client(ctx, token)
	}

	// Set up client.
	{
		var err error
		conn.gmail, err = gmail.New(conn.authedClient)
		if err != nil {
			return nil, errors.Wrap(err, "creating GMail client")
		}
		conn.gmail.UserAgent = userAgent
	}
	return conn, nil
}

func (c *CmdG) LoadLabels(ctx context.Context) error {
	// Load initial labels.
	res, err := c.gmail.Users.Labels.List(email).Context(ctx).Do()
	if err != nil {
		return err
	}
	c.m.Lock()
	defer c.m.Unlock()
	for _, l := range res.Labels {
		c.labelCache[l.Id] = &Label{
			ID:       l.Id,
			Label:    l.Name,
			Response: l,
		}
	}
	return nil
}

func (c *CmdG) Labels() []*Label {
	c.m.RLock()
	defer c.m.RUnlock()
	var ret []*Label
	for _, l := range c.labelCache {
		ret = append(ret, l)
	}
	sort.Slice(ret, func(i, j int) bool {
		if ret[i].ID == Inbox {
			return true
		}
		if ret[j].ID == Inbox {
			return false
		}
		return ret[i].Label < ret[j].Label
	})
	return ret
}

func (c *CmdG) GetProfile(ctx context.Context) (*gmail.Profile, error) {
	return c.gmail.Users.GetProfile(email).Context(ctx).Do()
}

func (c *CmdG) Send(ctx context.Context, msg string) error {
	_, err := c.gmail.Users.Messages.Send(email, &gmail.Message{
		Raw: mimeEncode(msg),
	}).Context(ctx).Do()
	return err
}

func (c *CmdG) BatchArchive(ctx context.Context, ids []string) error {
	return c.gmail.Users.Messages.BatchModify(email, &gmail.BatchModifyMessagesRequest{
		Ids:            ids,
		RemoveLabelIds: []string{Inbox},
	}).Context(ctx).Do()
}

func (c *CmdG) BatchLabel(ctx context.Context, ids []string, labelID string) error {
	return c.gmail.Users.Messages.BatchModify(email, &gmail.BatchModifyMessagesRequest{
		Ids:         ids,
		AddLabelIds: []string{labelID},
	}).Context(ctx).Do()
}

func (c *CmdG) BatchUnlabel(ctx context.Context, ids []string, labelID string) error {
	return c.gmail.Users.Messages.BatchModify(email, &gmail.BatchModifyMessagesRequest{
		Ids:            ids,
		RemoveLabelIds: []string{labelID},
	}).Context(ctx).Do()
}

func (c *CmdG) ListMessages(ctx context.Context, label, query, token string) (*Page, error) {
	nres := int64(pageSize)
	q := c.gmail.Users.Messages.List(email).
		PageToken(token).
		MaxResults(int64(nres)).
		Context(ctx).
		Fields("messages,resultSizeEstimate,nextPageToken")
	if query != "" {
		q = q.Q(query)
	}
	if label != "" {
		q = q.LabelIds(label)
	}
	res, err := q.Do()
	if err != nil {
		return nil, errors.Wrap(err, "listing messages")
	}
	log.Infof("Next page token: %q", res.NextPageToken)
	p := &Page{
		conn:     c,
		Label:    label,
		Query:    query,
		Response: res,
	}
	for _, m := range res.Messages {
		p.Messages = append(p.Messages, NewMessage(c, m.Id))
	}
	return p, nil
}
