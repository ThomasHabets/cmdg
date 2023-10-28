package main

import (
	"context"
	"fmt"
	"net/mail"
	"net/textproto"
	"regexp"
	"strings"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/ThomasHabets/cmdg/pkg/cmdg"
	"github.com/ThomasHabets/cmdg/pkg/dialog"
	"github.com/ThomasHabets/cmdg/pkg/input"
)

const (
	replyPrefix   = "Re: "
	forwardPrefix = "Fwd: "
	spaces        = " \t"
)

var (
	replyPrefixes   = regexp.MustCompile(`(?i)^(Re|Sv|Aw): `)
	forwardPrefixes = regexp.MustCompile(`(?i)^(Fwd): `)
	removeCharsRE   = regexp.MustCompile(`\r`)

	headerInReplyTo  = textproto.CanonicalMIMEHeaderKey("In-Reply-To")
	headerReferences = textproto.CanonicalMIMEHeaderKey("References")
	headerMessageID  = textproto.CanonicalMIMEHeaderKey("Message-ID")
)

func replyQuoted(s string) string {
	lines := strings.Split(removeCharsRE.ReplaceAllString(s, ""), "\n")
	var ret []string
	for _, l := range lines {
		space := " "
		if strings.HasPrefix(l, ">") {
			space = ""
		}
		ret = append(ret, strings.TrimRight(">"+space+l, spaces))
	}
	return strings.Join(ret, "\n")
}

// Args:
//   msg: Message to reply or forward.
func replyOrForward(ctx context.Context, conn *cmdg.CmdG, keys *input.Input, to, cc, subjPrefix string, rmPrefix *regexp.Regexp, msg *cmdg.Message) error {
	b, err := msg.GetUnpatchedBody(ctx)
	if err != nil {
		return err
	}
	subj, err := msg.GetSubject(ctx)
	if err != nil {
		return err
	}
	date, err := msg.GetTime(ctx)
	if err != nil {
		return err
	}
	orig, err := msg.GetHeader(ctx, "From")
	if err != nil {
		return err
	}
	headers := []string{
		fmt.Sprintf("To: %s", to),
	}
	if len(cc) != 0 {
		headers = append(headers, fmt.Sprintf("CC: %s", cc))
	}

	headers = append(headers, fmt.Sprintf("Subject: %s%s", subjPrefix, rmPrefix.ReplaceAllString(subj, "")))
	body := []string{
		fmt.Sprintf("On %s, %s said:", date.Format("Mon, 2 Jan 2006 15:04:05 -0700"), orig),
		replyQuoted(b),
	}
	if signature != "" {
		body = append(body, "\n--\n"+signature+"\n")
	}

	threadID, err := msg.ThreadID(ctx)
	if err != nil {
		return err
	}

	prefill := strings.Join(headers, "\n") + "\n\n" + strings.Join(body, "\n")
	refs, err := msg.GetReferences(ctx)
	if err != nil {
		// don't care
	}

	headOps := []headOp{
		func(h *mail.Header) {
			if h.Get("from") == "" {
				t := conn.GetDefaultSender()
				if t != "" {
					(*h)["From"] = []string{t}
				}
			}
		},
	}
	if v, err := msg.GetHeader(ctx, headerMessageID); err != nil {
		log.Errorf("Failed to get message ID when replying: %v", err)
		if refs != nil {
			headOps = append(headOps, func(head *mail.Header) {
				(*head)[headerReferences] = []string{strings.Join(refs, " ")}
			})
		}
	} else {
		headOps = append(headOps, func(head *mail.Header) {
			(*head)[headerInReplyTo] = []string{v}
			(*head)[headerReferences] = []string{strings.Join(append(refs, v), " ")}
		})
	}

	return compose(ctx, conn, headOps, keys, threadID, prefill)
}

func reply(ctx context.Context, conn *cmdg.CmdG, keys *input.Input, msg *cmdg.Message) error {
	to, err := msg.GetReplyTo(ctx)
	if err != nil {
		return err
	}
	return replyOrForward(ctx, conn, keys, to, "", replyPrefix, replyPrefixes, msg)
}

func replyAll(ctx context.Context, conn *cmdg.CmdG, keys *input.Input, msg *cmdg.Message) error {
	to, cc, err := msg.GetReplyToAll(ctx)
	if err != nil {
		return err
	}
	return replyOrForward(ctx, conn, keys, to, cc, replyPrefix, replyPrefixes, msg)
}

func forward(ctx context.Context, conn *cmdg.CmdG, keys *input.Input, msg *cmdg.Message) error {
	// Get recipient
	toOpt, err := dialog.Selection(dialog.Strings2Options(conn.Contacts()), "To> ", true, keys)
	if err == dialog.ErrAborted {
		return nil
	} else if err != nil {
		return err
	}
	to := toOpt.Key
	if strings.EqualFold(to, "me") {
		p, err := conn.GetProfile(ctx)
		if err != nil {
			return errors.Wrap(err, "failed to get own email address")
		}
		to = p.EmailAddress
	}

	return replyOrForward(ctx, conn, keys, to, "", forwardPrefix, forwardPrefixes, msg)
}
