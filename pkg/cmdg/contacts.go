package cmdg

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	log "github.com/sirupsen/logrus"
	people "google.golang.org/api/people/v1"
)

const (
	maxContacts      = 10000
	contactBatchSize = 2000
)

var (
	// Valid RFC5322 comment field. Actually this is a bit
	// restrictive since some other chars are allowed per section
	// 3.2.3. But this is playing it safe for now.
	rfc5322commentRE = regexp.MustCompile(`^[A-Za-z0-9]+$`)
)

func (c *CmdG) Contacts() []string {
	c.m.RLock()
	defer c.m.RUnlock()
	return append([]string{"me"}, c.contacts...)
}

func (c *CmdG) LoadContacts(ctx context.Context) error {
	co, err := c.GetContacts(ctx)
	if err != nil {
		return err
	}
	c.m.Lock()
	defer c.m.Unlock()
	c.contacts = co
	return nil
}

func quoteNameIfNeeded(s string) string {
	if rfc5322commentRE.MatchString(s) {
		return s
	}
	return fmt.Sprintf("%q", s)
}

// GetContacts gets all contact's email addresses in "Name Name <email@example.com>" format.
func (c *CmdG) GetContacts(ctx context.Context) ([]string, error) {
	// TODO: get a sync token and only do incremental download.
	var ret []string
	if err := c.people.People.Connections.List("people/me").Context(ctx).PageSize(contactBatchSize).PersonFields("names,emailAddresses").Pages(ctx, func(r *people.ListConnectionsResponse) error {
		log.Infof("Got batch of %d contacts, total %d", len(r.Connections), r.TotalItems)
		for _, p := range r.Connections {
			// Use name first listed.
			var name string
			if len(p.Names) > 0 {
				name = p.Names[0].DisplayName
			}
			for _, e := range p.EmailAddresses {
				if strings.Contains(e.Value, " ") {
					// Name already there.
					log.Warningf("Contact email address contains a space: %q", e.Value)
					ret = append(ret, e.Value)
				} else {
					if len(name) > 0 {
						ret = append(ret, fmt.Sprintf(`%s <%s>`, quoteNameIfNeeded(name), e.Value))
					} else {
						ret = append(ret, e.Value)
					}
				}
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Slice(ret, func(i, j int) bool {
		return strings.TrimLeft(ret[i], `"`) < strings.TrimLeft(ret[j], `"`)
	})
	return ret, nil
}
