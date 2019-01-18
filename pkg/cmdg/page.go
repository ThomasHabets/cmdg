package cmdg

import (
	"context"
	"sync"

	gmail "google.golang.org/api/gmail/v1"
)

type Page struct {
	Label string

	m sync.RWMutex

	conn     *CmdG
	Messages []*Message
	Response *gmail.ListMessagesResponse
}

func (p *Page) Next(ctx context.Context) (*Page, error) {
	return p.conn.ListMessages(ctx, p.Label, p.Response.NextPageToken)
}

func (p *Page) PreloadSubjects(ctx context.Context) error {
	conc := 100
	sem := make(chan struct{}, conc)
	num := len(p.Response.Messages)
	errs := make([]error, num, num)
	for n := 0; n < len(p.Response.Messages); n++ {
		n := n
		sem <- struct{}{}
		go func() {
			defer func() { <-sem }()

			if err := p.Messages[n].Preload(ctx, levelMetadata); err != nil {
				errs[n] = err
			}
		}()
	}
	for t := 0; t < conc; t++ {
		sem <- struct{}{}
	}
	return nil
}
