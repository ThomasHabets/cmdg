package main

import (
	"encoding/base64"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	gmail "code.google.com/p/google-api-go-client/gmail/v1"
	"github.com/ThomasHabets/drive-du/lib"
	//"github.com/jhillyerd/go.enmime"
	"github.com/jroimartin/gocui"
)

var (
	config    = flag.String("config", "", "Config file.")
	configure = flag.Bool("configure", false, "Configure oauth.")

	messagesView    *gocui.View
	openMessageView *gocui.View
	bottomView      *gocui.View
	ui              *gocui.Gui

	// State keepers.
	openMessageScrollY int
	messages           *messageList
	openMessage        *gmail.Message
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
			mres, err := g.Users.Messages.Get(email, m2.Id).Format("full").Do()
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
	fromMax := 10
	for n, m := range l.messages {
		s := fmt.Sprintf("%.*s | %s", fromMax, getHeader(m, "From")[:fromMax], getHeader(m, "Subject"))
		if n == l.current {
			fmt.Fprintf(messagesView, "* %s", s)
			if l.showDetails {
				fmt.Fprintf(messagesView, "    %s", m.Snippet)
			}
		} else {
			fmt.Fprintf(messagesView, "  %s", s)
		}
	}
	ui.Flush()
}

func (l *messageList) next() {
	if l.current < len(l.messages)-1 {
		l.current++
	}
}
func (l *messageList) prev() {
	if l.current > 0 {
		l.current--
	}
}
func (l *messageList) details() {
	l.showDetails = !l.showDetails
}

func run(g *gmail.Service) {
	messages = list(g)
	messages.draw()
}
func quit(g *gocui.Gui, v *gocui.View) error {
	return gocui.ErrorQuit
}
func next(g *gocui.Gui, v *gocui.View) error {
	messages.next()
	messages.draw()
	return nil
}

func mimeDecode(s string) (string, error) {
	s = strings.Replace(s, "-", "+", -1)
	s = strings.Replace(s, "_", "/", -1)
	data, err := base64.StdEncoding.DecodeString(s)
	return string(data), err
}
func getBody(m *gmail.Message) string {
	if len(m.Payload.Parts) == 0 {
		data, err := mimeDecode(string(m.Payload.Body.Data))
		if err != nil {
			return fmt.Sprintf("TODO Content error: %v", err)
		}
		return data
	}
	for _, p := range m.Payload.Parts {
		if p.MimeType == "text/plain" {
			data, err := mimeDecode(p.Body.Data)
			if err != nil {
				return fmt.Sprintf("TODO Content error: %v", err)
			}
			return string(data)
		}
	}
	return "TODO Unknown data"
}

func messagesCmdOpen(g *gocui.Gui, v *gocui.View) error {
	openMessageScrollY = 0
	openMessageDraw(g, v)
	return nil
}

func openMessageDraw(g *gocui.Gui, v *gocui.View) {
	openMessage = messages.messages[messages.current]
	g.Flush()
	openMessageView.Clear()
	w, h := openMessageView.Size()

	bodyLines := strings.Split(getBody(openMessage), "\n")
	maxScroll := len(bodyLines) - h
	if openMessageScrollY > maxScroll {
		openMessageScrollY = maxScroll
	}
	if openMessageScrollY < 0 {
		openMessageScrollY = 0
	}
	bodyLines = bodyLines[openMessageScrollY:]
	body := strings.Join(bodyLines, "\n")

	fmt.Fprintf(openMessageView, "Email %d of %d", messages.current+1, len(messages.messages))
	fmt.Fprintf(openMessageView, "From: %s", getHeader(openMessage, "From"))
	fmt.Fprintf(openMessageView, "Date: %s", getHeader(openMessage, "Date"))
	fmt.Fprintf(openMessageView, "Subject: %s", getHeader(openMessage, "Subject"))
	fmt.Fprintf(openMessageView, strings.Repeat("-", w))
	fmt.Fprintf(openMessageView, "%s", body)
	fmt.Fprintf(openMessageView, "%+v", *openMessage.Payload)
	for _, p := range openMessage.Payload.Parts {
		fmt.Fprintf(openMessageView, "%+v", *p)
	}
	g.SetCurrentView("openMessage")
}

func openMessageCmdPrev(g *gocui.Gui, v *gocui.View) error {
	openMessageScrollY = 0
	messages.prev()
	openMessageDraw(g, v)
	return nil
}
func openMessageCmdNext(g *gocui.Gui, v *gocui.View) error {
	openMessageScrollY = 0
	messages.next()
	openMessageDraw(g, v)
	return nil
}
func openMessageCmdPageDown(g *gocui.Gui, v *gocui.View) error {
	_, h := openMessageView.Size()
	openMessageScrollY += h
	openMessageDraw(g, v)
	return nil
}
func openMessageCmdScrollDown(g *gocui.Gui, v *gocui.View) error {
	openMessageScrollY += 2
	openMessageDraw(g, v)
	return nil
}
func openMessageCmdPageUp(g *gocui.Gui, v *gocui.View) error {
	_, h := openMessageView.Size()
	openMessageScrollY -= h
	openMessageDraw(g, v)
	return nil
}
func openMessageCmdScrollUp(g *gocui.Gui, v *gocui.View) error {
	openMessageScrollY -= 2
	openMessageDraw(g, v)
	return nil
}

func openMessageCmdClose(g *gocui.Gui, v *gocui.View) error {
	openMessage = nil
	g.SetCurrentView("messages")
	messages.draw()
	return nil
}

func prev(g *gocui.Gui, v *gocui.View) error {
	messages.prev()
	messages.draw()
	return nil
}
func details(g *gocui.Gui, v *gocui.View) error {
	messages.details()
	messages.draw()
	return nil
}

func layout(g *gocui.Gui) error {
	maxX, maxY := g.Size()
	_ = maxY
	var err error
	create := false
	if messagesView == nil {
		create = true
	}
	messagesView, err = g.SetView("messages", -1, -1, maxX, maxY-2)
	if err != nil {
		if create != (err == gocui.ErrorUnkView) {
			return err
		}
	}
	bottomView, err = g.SetView("bottom", -1, maxY-2, maxX, maxY)
	if err != nil {
		if create != (err == gocui.ErrorUnkView) {
			return err
		}
	}
	if openMessage == nil {
		ui.DeleteView("openMessage")
	} else {
		openMessageView, err = ui.SetView("openMessage", -1, -1, maxX, maxY-2)
		if err != nil {
			return err
		}
	}
	if create {
		fmt.Fprintf(messagesView, "Loading...")
		fmt.Fprintf(bottomView, "cmdg")
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
	if err := ui.SetKeybinding("", 'q', 0, quit); err != nil {
		log.Fatalf("Bind Q: %v", err)
	}
	if err := ui.SetKeybinding("messages", 'p', 0, prev); err != nil {
		log.Fatalf("Bind P: %v", err)
	}
	if err := ui.SetKeybinding("messages", 'n', 0, next); err != nil {
		log.Fatalf("Bind N: %v", err)
	}
	if err := ui.SetKeybinding("messages", 'a', 0, messagesCmdOpen); err != nil {
		log.Fatalf("Bind enter: %v", err)
	}

	for key, cb := range map[interface{}]func(g *gocui.Gui, v *gocui.View) error{
		'a':                openMessageCmdClose,
		'p':                openMessageCmdScrollUp,
		'n':                openMessageCmdScrollDown,
		gocui.KeyCtrlP:     openMessageCmdPrev,
		gocui.KeyCtrlN:     openMessageCmdNext,
		gocui.KeySpace:     openMessageCmdPageDown,
		gocui.KeyPgdn:      openMessageCmdPageDown,
		gocui.KeyBackspace: openMessageCmdPageUp,
		gocui.KeyPgup:      openMessageCmdPageUp,
	} {
		if err := ui.SetKeybinding("openMessage", key, 0, cb); err != nil {
			log.Fatalf("Bind %v: %v", key, err)
		}
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
