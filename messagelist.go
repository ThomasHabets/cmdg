package main

import (
	"fmt"

	gmail "code.google.com/p/google-api-go-client/gmail/v1"
)

type messageList struct {
	current     int
	marked      map[string]bool
	showDetails bool
	messages    []*gmail.Message
}

func (l *messageList) cmdNext() {
	l.current++
	l.fixCurrent()
}

func (l *messageList) cmdPrev() {
	l.current--
	l.fixCurrent()
}

func (l *messageList) fixCurrent() {
	if l.current >= len(l.messages) {
		l.current = len(l.messages) - 1
	}
	if l.current < 0 {
		l.current = 0
	}
}

func (l *messageList) cmdDetails() {
	l.showDetails = !l.showDetails
}

func (l *messageList) draw() {
	messagesView.Clear()
	fromMax := 20
	tsWidth := 7
	for n, m := range l.messages {
		s := fmt.Sprintf(" %+*s | %+*s | %s",
			tsWidth, timestring(m),
			fromMax, fromString(m),
			getHeader(m, "Subject"))
		if l.marked[m.Id] {
			s = "X" + s
		} else if hasLabel(m.LabelIds, unread) {
			s = ">" + s
		} else {
			s = " " + s
		}
		if n == l.current {
			s = "*" + s
		} else {
			s = " " + s
		}
		fmt.Fprint(messagesView, s)
		if n == l.current && l.showDetails {
			fmt.Fprintf(messagesView, "    %s", m.Snippet)
		}
	}
	ui.Flush()
}
