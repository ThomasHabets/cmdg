// Package messagegetter provides an async API for fetching a whole bunch of gmail messages.
package messagegetter

import (
	"log"
	"sync"
	"time"

	gmail "google.golang.org/api/gmail/v1"
)

// MessageGetter provides an async interface to fetch gmail messages.
type MessageGetter struct {
	g          *gmail.Service
	idc        chan string
	mc         chan *gmail.Message
	email      string
	profileAPI func(op string, d time.Duration)
	backoff    func(n int) (time.Duration, bool)
}

// New creates a new MessageGetter.
func New(g *gmail.Service, email string, profileAPI func(op string, d time.Duration), backoff func(n int) (time.Duration, bool)) *MessageGetter {
	m := &MessageGetter{
		g:          g,
		idc:        make(chan string),
		mc:         make(chan *gmail.Message),
		profileAPI: profileAPI,
		backoff:    backoff,
		email:      email,
	}
	go m.run()
	return m
}

// Add adds a new message ID to be retrieved.
func (m *MessageGetter) Add(id string) {
	m.idc <- id
}

func (m *MessageGetter) run() {
	defer close(m.mc)
	var wg sync.WaitGroup
	for id := range m.idc {
		wg.Add(1)
		id := id
		go func() {
			defer wg.Done()
			st := time.Now()
			for bo := 0; ; bo++ {
				msg, err := m.g.Users.Messages.Get(m.email, id).Format("full").Do()
				if err != nil {
					s, done := m.backoff(bo)
					if done {
						// TODO: should probably not give up.
						log.Printf("Get message failed, backoff expired, giving up: %v", err)
						return
					}
					time.Sleep(s)
					log.Printf("Users.Messages.Get failed, retrying: %v", err)
					continue
				}
				m.profileAPI("Users.Messages.Get", time.Since(st))
				m.mc <- msg
				return
			}
		}()
	}
	wg.Wait()
}

// Get returns a channel where all messages will be sent.
func (m *MessageGetter) Get() <-chan *gmail.Message {
	return m.mc
}

// Done tells MessageGetter that no more messages will be asked for.
func (m *MessageGetter) Done() {
	close(m.idc)
}
