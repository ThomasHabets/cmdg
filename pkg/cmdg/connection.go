package cmdg

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"mime"
	"mime/multipart"
	"net/http"
	"net/mail"
	"net/textproto"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/proxy"
	"golang.org/x/oauth2"
	drive "google.golang.org/api/drive/v3"
	gmail "google.golang.org/api/gmail/v1"
	"google.golang.org/api/googleapi/transport"
	people "google.golang.org/api/people/v1"
)

const (
	// Scope for email, contacts, and appdata.
	scope = "https://www.googleapis.com/auth/gmail.modify https://www.googleapis.com/auth/contacts https://www.googleapis.com/auth/drive.appdata"

	pageSize = 100

	accessType = "offline"
	email      = "me"
)

// Different levels of detail to download.
const (
	LevelEmpty    DataLevel = ""         // Nothing
	LevelMinimal  DataLevel = "minimal"  // ID, labels
	LevelMetadata DataLevel = "metadata" // ID, labels, headers
	LevelFull     DataLevel = "full"     // ID, labels, headers, payload
)

const (
	// Not so much a level as a separate request. Type `string` so that it won't be usable as a `DataLevel`.
	levelRaw string = "RAW"

	appDataFolder = "appDataFolder"
)

var (
	// Version is the app version as reported in RPCs.
	Version = "unspecified"

	// NewThread is the thread ID to use for new threads.
	NewThread ThreadID

	shouldLogRPC = flag.Bool("log_rpc", false, "Log all RPCs.")
	socks5       = flag.String("socks5", "", "Use SOCKS5 proxy. host:port")
)

type (
	// DataLevel is the level of detail to download.
	DataLevel string

	// HistoryID is a sparse timeline of numbers.
	HistoryID uint64

	// ThreadID is IDs of threads.
	ThreadID string
)

// CmdG is the main app for cmdg. It holds both rpc clients and various caches. Everything except the UI.
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

func userAgent() string {
	return "cmdg " + Version
}

// MessageCache returns the message from the cache, or nil if not found.
func (c *CmdG) MessageCache(msg *Message) *Message {
	c.m.Lock()
	defer c.m.Unlock()
	if t, found := c.messageCache[msg.ID]; found {
		return t
	}
	c.messageCache[msg.ID] = msg
	return msg
}

// LabelCache returns the label fro the cache, or nil if not found.
func (c *CmdG) LabelCache(label *Label) *Label {
	c.m.Lock()
	defer c.m.Unlock()
	if t, f := c.labelCache[label.ID]; f {
		return t
	}
	c.labelCache[label.ID] = label
	return label
}

// NewFake creates a fake client, used for testing.
func NewFake(client *http.Client) (*CmdG, error) {
	conn := &CmdG{
		authedClient: client,
	}
	return conn, conn.setupClients()
}

func readConf(fn string) (Config, error) {
	f, err := ioutil.ReadFile(fn)
	if err != nil {
		return Config{}, err
	}
	var conf Config
	if err := json.Unmarshal(f, &conf); err != nil {
		return Config{}, errors.Wrapf(err, "unmarshalling config")
	}
	return conf, nil
}

// New creates a new CmdG.
func New(fn string) (*CmdG, error) {
	conn := &CmdG{
		messageCache: make(map[string]*Message),
		labelCache:   make(map[string]*Label),
	}

	// Read config.
	conf, err := readConf(fn)
	if err != nil {
		return nil, err
	}

	var tp http.RoundTripper

	// Set up SOCKS5 proxy.
	if *socks5 != "" {
		dialer, err := proxy.SOCKS5("tcp", *socks5, nil, proxy.Direct)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to connect to socks5 proxy %q", *socks5)
		}
		if cd, ok := dialer.(proxy.ContextDialer); ok {
			tp = &http.Transport{
				DialContext: cd.DialContext,
			}
		} else {
			tp = &http.Transport{
				Dial: dialer.Dial,
			}
		}
	}

	// Attach APIkey, if any.
	if conf.OAuth.APIKey != "" {
		newtp := &transport.APIKey{
			Key:       conf.OAuth.APIKey,
			Transport: tp,
		}
		tp = newtp
	}

	// Set up google http.Client.
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, &http.Client{
		Transport: tp,
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
			Scopes: []string{scope},

			// TODO: This method doesn't work anymore, so
			// why is this code still here?
			//
			// RedirectURL: oauthRedirectOffline,
		}
		conn.authedClient = cfg.Client(ctx, token)
	}
	return conn, conn.setupClients()
}

func (c *CmdG) setupClients() error {
	// Set up gmail client.
	{
		var err error
		c.gmail, err = gmail.New(c.authedClient)
		if err != nil {
			return errors.Wrap(err, "creating GMail client")
		}
		c.gmail.UserAgent = userAgent()
	}

	// Set up drive client.
	{
		var err error
		c.drive, err = drive.New(c.authedClient)
		if err != nil {
			return errors.Wrap(err, "creating Drive client")
		}
		c.drive.UserAgent = userAgent()
	}
	// Set up people client.
	{
		var err error
		c.people, err = people.New(c.authedClient)
		if err != nil {
			return errors.Wrap(err, "creating People client")
		}
		c.drive.UserAgent = userAgent()
	}
	return nil
}

func wrapLogRPC(fn string, cb func() error, af string, args ...interface{}) error {
	st := time.Now()
	err := cb()
	logRPC(st, err, fmt.Sprintf("%s(%s)", fn, af), args...)
	return err
}

func logRPC(st time.Time, err error, s string, args ...interface{}) {
	if *shouldLogRPC {
		log.Infof("RPC> %s => %v %v", fmt.Sprintf(s, args...), err, time.Since(st))
	}
}

// LoadLabels batch loads all labels into the cache.
func (c *CmdG) LoadLabels(ctx context.Context) error {
	// Load initial labels.
	st := time.Now()
	var res *gmail.ListLabelsResponse
	err := wrapLogRPC("gmail.Users.Labels.List", func() (err error) {
		res, err = c.gmail.Users.Labels.List(email).Context(ctx).Do()
		return
	}, "email=%q", email)
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

// Labels returns a list of all labels.
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

// GetProfile returns the profile for the current user.
func (c *CmdG) GetProfile(ctx context.Context) (*gmail.Profile, error) {
	var ret *gmail.Profile
	err := wrapLogRPC("gmail.Users.GetProfile", func() (err error) {
		ret, err = c.gmail.Users.GetProfile(email).Context(ctx).Do()
		return
	}, "email=%q", email)
	return ret, err
}

// Part is a part of a message. Contents and header.
type Part struct {
	Contents string
	Header   textproto.MIMEHeader
}

// FullString returns the "serialized" part.
func (p *Part) FullString() string {
	var hs []string
	for k, vs := range p.Header {
		for _, v := range vs {
			hs = append(hs, fmt.Sprintf("%s: %s", k, v))
		}
	}
	// TODO: this can't be right. Go libraries use maps for headers but we depend on order. ;-(
	sort.Slice(hs, func(i, j int) bool {
		if strings.HasPrefix(strings.ToLower(hs[i]), "content-type: ") {
			return true
		}
		return hs[i] < hs[j]
	})
	return strings.Join(hs, "\r\n") + "\r\n\r\n" + p.Contents
}

// ParseUserMessage parses what's in the user's editor and turns into into a Part and message headers.
func ParseUserMessage(in string) (mail.Header, *Part, error) {
	m, err := mail.ReadMessage(strings.NewReader(in))
	if err != nil {
		return nil, nil, errors.Wrapf(err, "message to send is malformed")
	}
	b, err := ioutil.ReadAll(m.Body)
	if err != nil {
		return nil, nil, errors.Wrapf(err, "failed to read user message")
	}
	m.Header["MIME-Version"] = []string{"1.0"}
	return m.Header, &Part{
		Header: map[string][]string{
			"Content-Type":        []string{`text/plain; charset="UTF-8"`},
			"Content-Disposition": []string{"inline"},
		},
		Contents: string(b),
	}, nil
}

// SendParts sends a multipart message.
// Args:
//   mp:    multipart type. "mixed" is a typical type.
//   head:  Email header.
//   parts: Email parts.
func (c *CmdG) SendParts(ctx context.Context, threadID ThreadID, mp string, head mail.Header, parts []*Part) error {
	var mbuf bytes.Buffer
	w := multipart.NewWriter(&mbuf)

	// Create mail contents.
	for _, p := range parts {
		p2, err := w.CreatePart(p.Header)
		if err != nil {
			return errors.Wrapf(err, "failed to create part")
		}
		if _, err := p2.Write([]byte(p.Contents)); err != nil {
			return errors.Wrapf(err, "assembling part")
		}
	}
	if err := w.Close(); err != nil {
		return errors.Wrapf(err, "closing multipart")
	}

	addrHeader := map[string]bool{
		"to":       true,
		"cc":       true,
		"bcc":      true,
		"reply-to": true,
	}

	// Add message headers for gmail.
	var hlines []string
	for k, vs := range head {
		if addrHeader[strings.ToLower(k)] {
			for _, v := range vs {
				if v == "" {
					continue
				}
				as, err := mail.ParseAddressList(v)
				if err != nil {
					return errors.Wrapf(err, "parsing address list %q, which is %q", k, v)
				}
				var ass []string
				for _, a := range as {
					if a.Name == "" {
						ass = append(ass, a.Address)
					} else {
						ass = append(ass, fmt.Sprintf(`"%s" <%s>`, mime.QEncoding.Encode("utf-8", a.Name), a.Address))
					}
				}
				hlines = append(hlines, fmt.Sprintf("%s: %s", k, strings.Join(ass, ", ")))
			}
		} else {
			for _, v := range vs {
				hlines = append(hlines, fmt.Sprintf("%s: %s", k, mime.QEncoding.Encode("utf-8", v)))

			}
		}
	}
	sort.Strings(hlines)
	hlines = append(hlines, fmt.Sprintf(`Content-Type: multipart/%s; boundary="%s"`, mp, w.Boundary()))
	hlines = append(hlines, `Content-Disposition: inline`)
	msgs := strings.Join(hlines, "\r\n") + "\r\n\r\n" + mbuf.String()

	log.Infof("Final message: %q", msgs)
	return c.send(ctx, threadID, msgs)
}

func (c *CmdG) send(ctx context.Context, threadID ThreadID, msg string) (err error) {
	return wrapLogRPC("gmail.Users.Messages.Send", func() error {
		_, err = c.gmail.Users.Messages.Send(email, &gmail.Message{
			Raw:      MIMEEncode(msg),
			ThreadId: string(threadID),
		}).Context(ctx).Do()
		return err
	}, "email=%q threadID=%q msg=%q", email, threadID, msg)
}

// PutFile uploads a file into the config dir on Google drive.
func (c *CmdG) PutFile(ctx context.Context, fn string, contents []byte) error {
	const name = "signature.txt"
	const folder = appDataFolder
	err := wrapLogRPC("drive.Files.Create", func() error {
		_, err := c.drive.Files.Create(&drive.File{
			Name:    name,
			Parents: []string{folder},
		}).Context(ctx).Media(bytes.NewBuffer(contents)).Do()
		return err
	}, "name=%q parents=%s", name, folder)
	if err != nil {
		return errors.Wrapf(err, "creating file %q with %d bytes of data", fn, len(contents))
	}
	return nil
}

func (c *CmdG) getFileID(ctx context.Context, fn string) (string, error) {
	var token string
	for {
		var l *drive.FileList
		err := wrapLogRPC("drive.Files.List", func() (err error) {
			l, err = c.drive.Files.List().Context(ctx).Spaces(appDataFolder).PageToken(token).Do()
			return
		}, "folder=%q token=%q", appDataFolder, token)
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

// UpdateFile updates an existing file on Google drive in the config folder..
func (c *CmdG) UpdateFile(ctx context.Context, fn string, contents []byte) error {
	id, err := c.getFileID(ctx, fn)
	if err != nil {
		if err == os.ErrNotExist {
			if err := wrapLogRPC("drive.Files.Create", func() error {
				_, err := c.drive.Files.Create(&drive.File{
					Name:    fn,
					Parents: []string{appDataFolder},
				}).Context(ctx).Media(bytes.NewBuffer(contents)).Do()
				return err
			}, "name=%q parent=%q", fn, appDataFolder); err != nil {
				return errors.Wrapf(err, "creating file %q with %d bytes of data", fn, len(contents))
			}
			return nil
		}
		return errors.Wrapf(err, "getting file ID for %q", fn)
	}

	if err := wrapLogRPC("drive.Files.Update", func() error {
		_, err := c.drive.Files.Update(id, &drive.File{
			Name: fn,
		}).Context(ctx).Media(bytes.NewBuffer(contents)).Do()
		return err
	}, "name=%q", fn); err != nil {
		return errors.Wrapf(err, "updating file %q, id %q", fn, id)
	}
	return nil
}

// GetFile downloads a file from the config folder.
func (c *CmdG) GetFile(ctx context.Context, fn string) ([]byte, error) {
	var token string
	for {
		var l *drive.FileList
		err := wrapLogRPC("drive.Files.List", func() (err error) {
			l, err = c.drive.Files.List().Context(ctx).Spaces(appDataFolder).PageToken(token).Do()
			return
		}, "spaces=%q token=%q", appDataFolder, token)
		if err != nil {
			return nil, err
		}
		for _, f := range l.Files {
			if f.Name == fn {
				var r *http.Response
				err := wrapLogRPC("drive.Files.Get", func() (err error) {
					r, err = c.drive.Files.Get(f.Id).Context(ctx).Download()
					return
				}, "fileID=%v", f.Id)
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

// MakeDraft creates a new draft.
func (c *CmdG) MakeDraft(ctx context.Context, msg string) error {
	return wrapLogRPC("gmail.Users.Drafts.Create", func() error {
		_, err := c.gmail.Users.Drafts.Create(email, &gmail.Draft{
			Message: &gmail.Message{
				Raw: MIMEEncode(msg),
			},
		}).Context(ctx).Do()
		return err
	}, "email=%q msg=%q", email, msg)
}

// BatchArchive archives all the given message IDs.
func (c *CmdG) BatchArchive(ctx context.Context, ids []string) error {
	return wrapLogRPC("gmail.Users.Messages.BatchModify", func() error {
		return c.gmail.Users.Messages.BatchModify(email, &gmail.BatchModifyMessagesRequest{
			Ids:            ids,
			RemoveLabelIds: []string{Inbox},
		}).Context(ctx).Do()
	}, "email=%q remove=INBOX ids=%v)", email, ids)
}

// BatchDelete deletes. Does not put in trash. Does not pass go:
// "Immediately and permanently deletes the specified message. This operation cannot be undone."
//
// cmdg doesn't actually request oauth permission to do this, so this function is never used.
// Instead BatchTrash is used.
func (c *CmdG) BatchDelete(ctx context.Context, ids []string) error {
	return wrapLogRPC("gmail.Users.Messages.BatchDelete", func() error {
		return c.gmail.Users.Messages.BatchDelete(email, &gmail.BatchDeleteMessagesRequest{
			Ids: ids,
		}).Context(ctx).Do()
	}, "email=%q ids=%v", email, ids)
}

// BatchTrash trashes the messages.
//
// There isn't actually a BatchTrash, so we'll pretend with just labels.
func (c *CmdG) BatchTrash(ctx context.Context, ids []string) error {
	return c.BatchLabel(ctx, ids, Trash)
}

// BatchLabel adds one new label to many messages.
func (c *CmdG) BatchLabel(ctx context.Context, ids []string, labelID string) error {
	return wrapLogRPC("gmail.Users.Messages.BatchModify", func() error {
		return c.gmail.Users.Messages.BatchModify(email, &gmail.BatchModifyMessagesRequest{
			Ids:         ids,
			AddLabelIds: []string{labelID},
		}).Context(ctx).Do()
	}, "email=%q add_labelID=%v ids=%v", email, labelID, ids)
}

// BatchUnlabel removes one label from many messages.
func (c *CmdG) BatchUnlabel(ctx context.Context, ids []string, labelID string) (err error) {
	return wrapLogRPC("gmail.Users.Messages.BatchModify", func() error {
		return c.gmail.Users.Messages.BatchModify(email, &gmail.BatchModifyMessagesRequest{
			Ids:            ids,
			RemoveLabelIds: []string{labelID},
		}).Context(ctx).Do()
	}, "email=%q remove_labelID=%v ids=%v", email, labelID, ids)
}

// HistoryID returns the current history ID.
func (c *CmdG) HistoryID(ctx context.Context) (HistoryID, error) {
	var p *gmail.Profile
	err := wrapLogRPC("gmail.Users.GetProfile", func() (err error) {
		p, err = c.gmail.Users.GetProfile(email).Context(ctx).Do()
		return
	}, "email=%q", email)
	if err != nil {
		return 0, err
	}
	return HistoryID(p.HistoryId), nil
}

// MoreHistory returns if stuff happened since start ID.
func (c *CmdG) MoreHistory(ctx context.Context, start HistoryID, labelID string) (bool, error) {
	log.Infof("History for %d %s", start, labelID)
	var r *gmail.ListHistoryResponse
	err := wrapLogRPC("gmail.Users.History.List", func() (err error) {

		r, err = c.gmail.Users.History.List(email).Context(ctx).StartHistoryId(uint64(start)).LabelId(labelID).Do()
		return
	}, "email=%q historyID=%v labelID=%q", email, start, labelID)
	if err != nil {
		return false, err
	}
	return len(r.History) > 0, nil
}

// History returns history since startID (all pages).
func (c *CmdG) History(ctx context.Context, startID HistoryID, labelID string) ([]*gmail.History, HistoryID, error) {
	log.Infof("History for %d %s", startID, labelID)
	var ret []*gmail.History
	var h HistoryID
	err := wrapLogRPC("gmail.Users.History.List", func() error {
		return c.gmail.Users.History.List(email).Context(ctx).StartHistoryId(uint64(startID)).LabelId(labelID).Pages(ctx, func(r *gmail.ListHistoryResponse) error {
			ret = append(ret, r.History...)
			h = HistoryID(r.HistoryId)
			return nil
		})
	}, "email=%q historyID=%v labelID=%q", email, startID, labelID)
	if err != nil {
		return nil, 0, err
	}
	return ret, h, nil
}

// ListMessages lists messages in a given label or query, with optional page token.
func (c *CmdG) ListMessages(ctx context.Context, label, query, token string) (*Page, error) {
	const fields = "messages,resultSizeEstimate,nextPageToken"
	nres := int64(pageSize)

	q := c.gmail.Users.Messages.List(email).
		PageToken(token).
		MaxResults(int64(nres)).
		Context(ctx).
		Fields(fields)
	if query != "" {
		q = q.Q(query)
	}
	if label != "" {
		q = q.LabelIds(label)
	}
	var res *gmail.ListMessagesResponse
	err := wrapLogRPC("gmail.Users.Messages.List", func() (err error) {
		res, err = q.Do()
		return
	}, "email=%q token=%v labelID=%q query=%q size=%d fields=%q)", email, token, label, query, nres, fields)
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

// ListDrafts lists all drafts.
func (c *CmdG) ListDrafts(ctx context.Context) ([]*Draft, error) {
	var ret []*Draft
	if err := wrapLogRPC("gmail.Users.Drafts.List", func() error {
		return c.gmail.Users.Drafts.List(email).Pages(ctx, func(r *gmail.ListDraftsResponse) error {
			for _, d := range r.Drafts {
				nd := NewDraft(c, d.Id)
				ret = append(ret, nd)
				go func() {
					if err := nd.load(ctx, LevelMetadata); err != nil {
						log.Errorf("Loading a draft: %v", err)
					}
				}()
			}
			return nil
		})
	}, "email=%q", email); err != nil {
		return nil, err
	}
	return ret, nil
}
