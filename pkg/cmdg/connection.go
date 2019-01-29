package cmdg

import (
	"bytes"
	"context"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"golang.org/x/oauth2"
	drive "google.golang.org/api/drive/v3"
	gmail "google.golang.org/api/gmail/v1"
	"google.golang.org/api/googleapi/transport"
	people "google.golang.org/api/people/v1"
)

const (
	version   = "1.0-beta"
	userAgent = "cmdg " + version
	// Scope for email, contacts, and appdata.
	scope = "https://www.googleapis.com/auth/gmail.modify https://www.googleapis.com/auth/contacts https://www.googleapis.com/auth/drive.appdata"

	pageSize = 100

	accessType = "offline"
	email      = "me"

	LevelEmpty    DataLevel = ""         // Nothing
	LevelMinimal  DataLevel = "minimal"  // ID, labels
	LevelMetadata DataLevel = "metadata" // ID, labels, headers
	LevelFull     DataLevel = "full"     // ID, labels, headers, payload

	// Not so much a level as a separate request. Type `string` so that it won't be usable as a `DataLevel`.
	levelRaw string = "RAW"

	appDataFolder = "appDataFolder"
)

type (
	DataLevel string
)

type CmdG struct {
	m            sync.RWMutex
	authedClient *http.Client
	gmail        *gmail.Service
	drive        *drive.Service
	people       *people.Service
	messageCache map[string]*Message
	labelCache   map[string]*Label
	contacts     []string
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
		Transport: &transport.APIKey{Key: conf.OAuth.APIKey},
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

	// Set up gmail client.
	{
		var err error
		conn.gmail, err = gmail.New(conn.authedClient)
		if err != nil {
			return nil, errors.Wrap(err, "creating GMail client")
		}
		conn.gmail.UserAgent = userAgent
	}

	// Set up drive client.
	{
		var err error
		conn.drive, err = drive.New(conn.authedClient)
		if err != nil {
			return nil, errors.Wrap(err, "creating Drive client")
		}
		conn.drive.UserAgent = userAgent
	}
	// Set up people client.
	{
		var err error
		conn.people, err = people.New(conn.authedClient)
		if err != nil {
			return nil, errors.Wrap(err, "creating People client")
		}
		conn.drive.UserAgent = userAgent
	}
	return conn, nil
}

func (c *CmdG) LoadLabels(ctx context.Context) error {
	// Load initial labels.
	st := time.Now()
	res, err := c.gmail.Users.Labels.List(email).Context(ctx).Do()
	if err != nil {
		return err
	}
	log.Infof("Loaded labels in %v", time.Since(st))
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

func (c *CmdG) PutFile(ctx context.Context, fn string, contents []byte) error {
	if _, err := c.drive.Files.Create(&drive.File{
		Name:    "signature.txt",
		Parents: []string{appDataFolder},
	}).Context(ctx).Media(bytes.NewBuffer(contents)).Do(); err != nil {
		return errors.Wrapf(err, "creating file %q with %d bytes of data", fn, len(contents))
	}
	return nil
}

func (c *CmdG) getFileID(ctx context.Context, fn string) (string, error) {
	var token string
	for {
		l, err := c.drive.Files.List().Context(ctx).Spaces(appDataFolder).PageToken(token).Do()
		if err != nil {
			return "", err
		}
		for _, f := range l.Files {
			if f.Name == fn {
				return f.Id, nil
			}
		}
		token = l.NextPageToken
		if token == "" {
			break
		}
	}
	return "", os.ErrNotExist
}

func (c *CmdG) UpdateFile(ctx context.Context, fn string, contents []byte) error {
	id, err := c.getFileID(ctx, fn)
	if err != nil {
		if err == os.ErrNotExist {
			if _, err := c.drive.Files.Create(&drive.File{
				Name:    fn,
				Parents: []string{appDataFolder},
			}).Context(ctx).Media(bytes.NewBuffer(contents)).Do(); err != nil {
				return errors.Wrapf(err, "creating file %q with %d bytes of data", fn, len(contents))
			}
			return nil
		}
		return errors.Wrapf(err, "getting file ID for %q", fn)
	}

	if _, err := c.drive.Files.Update(id, &drive.File{
		Name: fn,
	}).Context(ctx).Media(bytes.NewBuffer(contents)).Do(); err != nil {
		return errors.Wrapf(err, "updating file %q, id %q", fn, id)
	}
	return nil
}

func (c *CmdG) GetFile(ctx context.Context, fn string) ([]byte, error) {
	var token string
	for {
		l, err := c.drive.Files.List().Context(ctx).Spaces("appDataFolder").PageToken(token).Do()
		if err != nil {
			return nil, err
		}
		for _, f := range l.Files {
			if f.Name == fn {
				r, err := c.drive.Files.Get(f.Id).Context(ctx).Download()
				if err != nil {
					return nil, err
				}
				defer r.Body.Close()
				return ioutil.ReadAll(r.Body)
			}
		}
		token = l.NextPageToken
		if token == "" {
			break
		}
	}

	return nil, os.ErrNotExist
}

func (c *CmdG) MakeDraft(ctx context.Context, msg string) error {
	_, err := c.gmail.Users.Drafts.Create(email, &gmail.Draft{
		Message: &gmail.Message{
			Raw: mimeEncode(msg),
		},
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

func (c *CmdG) HistoryID(ctx context.Context) (uint64, error) {
	p, err := c.gmail.Users.GetProfile(email).Context(ctx).Do()
	if err != nil {
		return 0, err
	}
	return p.HistoryId, nil
}

func (c *CmdG) MoreHistory(ctx context.Context, start uint64, labelID string) (bool, error) {
	log.Infof("History for %d %s", start, labelID)
	r, err := c.gmail.Users.History.List(email).Context(ctx).StartHistoryId(start).LabelId(labelID).Do()
	if err != nil {
		return false, err
	}
	return len(r.History) > 0, nil
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
