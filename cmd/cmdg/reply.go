package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/pkg/errors"

	"github.com/ThomasHabets/cmdg/pkg/cmdg"
	"github.com/ThomasHabets/cmdg/pkg/dialog"
	"github.com/ThomasHabets/cmdg/pkg/input"
)

const (
	replyPrefix   = "Re: "
	forwardPrefix = "Fwd: "
)

var (
	replyPrefixes   = regexp.MustCompile(`^(Re|Sv|Aw): `)
	forwardPrefixes = regexp.MustCompile(`^(Fwd): `)
)

func replyQuoted(s string) string {
	lines := strings.Split(s, "\n")
	var ret []string
	for _, l := range lines {
		ret = append(ret, "> "+l)
	}
	return strings.Join(ret, "\n")
}

func replyOrForward(ctx context.Context, conn *cmdg.CmdG, keys *input.Input, to, cc, subjPrefix string, rmPrefix *regexp.Regexp, msg *cmdg.Message) error {
	b, err := msg.GetUnpatchedBody(ctx)
	if err != nil {
		return err
	}
	subj, err := msg.GetHeader(ctx, "Subject")
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
	prefill := strings.Join(headers, "\n") + "\n\n" + strings.Join(body, "\n")
	return compose(ctx, conn, keys, prefill)
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
	to, err := dialog.Selection(conn.Contacts(), true, keys)
	if err != nil {
		return err
	}
	if strings.EqualFold(to, "me") {
		p, err := conn.GetProfile(ctx)
		if err != nil {
			return errors.Wrap(err, "failed to get own email address")
		}
		to = p.EmailAddress
	}

	return replyOrForward(ctx, conn, keys, to, "", forwardPrefix, forwardPrefixes, msg)
}
