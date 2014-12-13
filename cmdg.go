package main

import (
	"flag"
	"fmt"
	"log"
	"time"

	gmail "code.google.com/p/google-api-go-client/gmail/v1"
	"github.com/ThomasHabets/drive-du/lib"
	"github.com/jroimartin/gocui"
)

var (
	config    = flag.String("config", "", "Config file.")
	configure = flag.Bool("configure", false, "Configure oauth.")

	messagesView *gocui.View
	ui           *gocui.Gui
)

const (
	scope      = "https://www.googleapis.com/auth/gmail.readonly"
	accessType = "offline"
	email      = "me"
)

func getHeader(m *gmail.Message, header string) string {
	for _, h := range m.Payload.Headers {
		if h.Name == header {
			return h.Value
		}
	}
	return ""
}

type parallel struct {
	chans []<-chan func()
}

func (p *parallel) add(f func(chan<- func())) {
	c := make(chan func())
	go f(c)
	p.chans = append(p.chans, c)
}

func (p *parallel) run() {
	for _, ch := range p.chans {
		f := <-ch
		f()
	}
}

type messageList struct {
	current     int
	showDetails bool
	messages    []*gmail.Message
}

func list(g *gmail.Service) *messageList {
	res, err := g.Users.Messages.List(email).
		//		LabelIds().
		//		PageToken().
		MaxResults(20).
		//Fields("messages(id,payload,snippet,raw,sizeEstimate),resultSizeEstimate").
		Fields("messages,resultSizeEstimate").
		Do()
	if err != nil {
		log.Fatalf("Listing: %v", err)
	}
	fmt.Fprintf(messagesView, "Total size: %d\n", res.ResultSizeEstimate)
	p := parallel{}
	ret := &messageList{}
	for _, m := range res.Messages {
		m2 := m
		p.add(func(ch chan<- func()) {
			mres, err := g.Users.Messages.Get(email, m2.Id).Format("metadata").Do()
			if err != nil {
				log.Fatalf("Get message: %v", err)
			}
			ch <- func() {
				ret.messages = append(ret.messages, mres)
			}
		})
	}
	p.run()
	return ret
}

func (l *messageList) draw() {
	messagesView.Clear()
	for n, m := range l.messages {
		if n == l.current {
			fmt.Fprintf(messagesView, "* %s", getHeader(m, "Subject"))
			if l.showDetails {
				fmt.Fprintf(messagesView, "    %s", m.Snippet)
			}
		} else {
			fmt.Fprintf(messagesView, "  %s", getHeader(m, "Subject"))
		}
	}
	ui.Flush()
}

var l *messageList

func (l *messageList) next() {
	l.current++
	l.draw()
}
func (l *messageList) prev() {
	l.current--
	l.draw()
}
func (l *messageList) details() {
	l.showDetails = !l.showDetails
	l.draw()
}

func run(g *gmail.Service) {
	l = list(g)
	l.draw()
}
func quit(g *gocui.Gui, v *gocui.View) error {
	return gocui.ErrorQuit
}
func next(g *gocui.Gui, v *gocui.View) error {
	l.next()
	return nil
}
func prev(g *gocui.Gui, v *gocui.View) error {
	l.prev()
	return nil
}
func details(g *gocui.Gui, v *gocui.View) error {
	l.details()
	return nil
}

func layout(g *gocui.Gui) error {
	maxX, maxY := g.Size()
	_ = maxY
	var err error
	if messagesView == nil {
		messagesView, err = g.SetView("messages", -1, -1, maxX, 20)
		if err != nil {
			if err != gocui.ErrorUnkView {
				return err
			}
		}
		messagesView.Clear()
		fmt.Fprintf(messagesView, "Loading...")
	}
	return nil
}
func main() {
	flag.Parse()
	if *config == "" {
		log.Fatalf("-config required")
	}

	if *configure {
		if err := lib.ConfigureWrite(scope, accessType, *config); err != nil {
			log.Fatalf("Failed to config: %v", err)
		}
		return
	}

	conf, err := lib.ReadConfig(*config)
	if err != nil {
		log.Fatalf("Failed to read config: %v", err)
	}

	t, err := lib.Connect(conf.OAuth, scope, accessType)
	if err != nil {
		log.Fatalf("Failed to connect to gmail: %v", err)
	}
	g, err := gmail.New(t.Client())
	if err != nil {
		log.Fatal("Failed to create gmail client: %v", err)
	}

	ui = gocui.NewGui()
	if err := ui.Init(); err != nil {
		log.Panicln(err)
	}
	defer ui.Close()
	ui.SetLayout(layout)
	if err := ui.SetKeybinding("", gocui.KeyCtrlC, 0, quit); err != nil {
		log.Panicln(err)
	}
	if err := ui.SetKeybinding("messages", gocui.KeyTab, 0, details); err != nil {
		log.Fatalf("Bind Q: %v", err)
	}
	if err := ui.SetKeybinding("messages", 'q', 0, quit); err != nil {
		log.Fatalf("Bind Q: %v", err)
	}
	if err := ui.SetKeybinding("messages", 'p', 0, prev); err != nil {
		log.Fatalf("Bind P: %v", err)
	}
	if err := ui.SetKeybinding("messages", 'n', 0, next); err != nil {
		log.Fatalf("Bind N: %v", err)
	}
	go func() {
		time.Sleep(time.Second)
		ui.SetCurrentView("messages")
		run(g)
	}()
	err = ui.MainLoop()
	if err != nil && err != gocui.ErrorQuit {
		log.Panicln(err)
	}
}
