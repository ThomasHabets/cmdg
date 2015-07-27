package cmdglib

/*
 *  Copyright (C) 2015 Thomas Habets <thomas@habets.se>
 *
 *  This program is free software; you can redistribute it and/or modify
 *  it under the terms of the GNU General Public License as published by
 *  the Free Software Foundation; either version 2 of the License, or
 *  (at your option) any later version.
 *
 *  This program is distributed in the hope that it will be useful,
 *  but WITHOUT ANY WARRANTY; without even the implied warranty of
 *  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 *  GNU General Public License for more details.
 *
 *  You should have received a copy of the GNU General Public License along
 *  with this program; if not, write to the Free Software Foundation, Inc.,
 *  51 Franklin Street, Fifth Floor, Boston, MA 02110-1301 USA.
 */

import (
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	gmail "google.golang.org/api/gmail/v1"
)

// Well known labels.
const (
	Inbox     = "INBOX"
	Unread    = "UNREAD"
	Draft     = "DRAFT"
	Important = "IMPORTANT"
	Spam      = "SPAM"
	Starred   = "STARRED"
	Trash     = "TRASH"
	Sent      = "SENT"
)

func utf8Decode(s string) string {
	ret := ""
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		ret = fmt.Sprintf("%s%c", ret, r)
		s = s[size:]
	}
	return ret
}

// GetHeader gets the value for a given header from a Message.
func GetHeader(m *gmail.Message, header string) string {
	if m.Payload == nil {
		return "loading"
	}
	return GetHeaderPart(m.Payload, header)
}

// GetHeaderPart gets the value for a given header from a MessagePart.
func GetHeaderPart(p *gmail.MessagePart, header string) string {
	for _, h := range p.Headers {
		if strings.EqualFold(h.Name, header) {
			// TODO: How to decode correctly?
			if false {
				return utf8Decode(h.Value)
			}
			return h.Value
		}
	}
	return ""
}

// ParseTime tries a few time formats and returns the one that works.
func ParseTime(s string) (time.Time, error) {
	var t time.Time
	var err error
	for _, layout := range []string{
		"Mon, 2 Jan 2006 15:04:05 -0700",
		"Mon, 2 Jan 2006 15:04:05 -0700 (MST)",
		"Mon, 2 Jan 2006 15:04:05 MST",
		"2 Jan 2006 15:04:05 -0700",
		"Mon, 2 Jan 2006 15:04:05 -0700 (GMT-07:00)",
		"Mon, _2 Jan 2006 15:04:05 -0700 (GMT-07:00)",
		time.RFC1123Z,
	} {
		t, err = time.Parse(layout, s)
		if err == nil {
			break
		}
	}
	return t, err
}

// TimeString returns "Date" header as a useful string. (e.g. mail from today shows hours)
func TimeString(m *gmail.Message) string {
	s := GetHeader(m, "Date")
	ts, err := ParseTime(s)
	if err != nil {
		return "Unknown"
	}
	ts = ts.Local()
	if time.Since(ts) > 365*24*time.Hour {
		return ts.Format("2006")
	}
	if !(time.Now().Month() == ts.Month() && time.Now().Day() == ts.Day()) {
		return ts.Format("Jan 02")
	}
	return ts.Format("15:04")
}

// FromString gets the source address, unless mail is sent, in which case get destination.
func FromString(m *gmail.Message) string {
	s := GetHeader(m, "From")
	if HasLabel(m.LabelIds, Sent) {
		s = GetHeader(m, "To")
	}
	a, err := mail.ParseAddress(s)
	if err != nil {
		return s
	}
	if len(a.Name) > 0 {
		return a.Name
	}
	return a.Address
}

// HasLabel checks if the given label is in the slice.
func HasLabel(labels []string, needle string) bool {
	for _, l := range labels {
		if l == needle {
			return true
		}
	}
	return false
}
