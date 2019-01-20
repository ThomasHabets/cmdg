package cmdg

import (
	"context"
	"encoding/xml"
	"fmt"
	"io/ioutil"
	"time"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

type contactEmail struct {
	Primary bool   `xml:"primary,attr"`
	Rel     string `xml:"rel,attr"`
	Email   string `xml:"address,attr"`
}
type contactEntry struct {
	ID    string         `xml:"id"`
	Title string         `xml:"title"`
	Email []contactEmail `xml:"email"`
}
type contacts struct {
	ID    string         `xml:"id"`
	Title string         `xml:"title"`
	Entry []contactEntry `xml:"entry"`
}

func (c *CmdG) Contacts() []string {
	ret := []string{"me"}
	c.m.RLock()
	defer c.m.RUnlock()
	for _, c := range c.contacts.Entry {
		for _, e := range c.Email {
			if c.Title != "" {
				ret = append(ret, fmt.Sprintf("%s <%s>", c.Title, e.Email))
			} else {
				ret = append(ret, e.Email)
			}
		}
	}
	return ret
}

func (c *CmdG) LoadContacts(ctx context.Context) error {
	// Make request.
	// TODO: Context
	st := time.Now()
	resp, err := c.authedClient.Get("https://www.google.com/m8/feeds/contacts/default/full")
	if err != nil {
		return errors.Wrap(err, "getting contacts")
	}
	defer resp.Body.Close()

	// Read response.
	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return errors.Wrap(err, "reading contacts")
	}
	et := time.Now()
	var con contacts
	if err := xml.Unmarshal(b, &con); err != nil {
		return errors.Wrap(err, "decoding contacts XML")
	}
	log.Infof("Got %d contacts in %v", len(con.Entry), et.Sub(st))
	c.m.Lock()
	defer c.m.Unlock()
	c.contacts = con
	return nil
}
