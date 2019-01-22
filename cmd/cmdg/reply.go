// TODO: merge common code between replytemplate builders.
package main

import (
	"context"
	"fmt"
	// "io/ioutil"
	// "os"
	// "os/exec"
	"strings"
	// "time"

	"github.com/pkg/errors"
	// log "github.com/sirupsen/logrus"

	"github.com/ThomasHabets/cmdg/pkg/cmdg"
	"github.com/ThomasHabets/cmdg/pkg/dialog"
	"github.com/ThomasHabets/cmdg/pkg/input"
)

var (
	replyPrefix   = "Re: "
	forwardPrefix = "Fwd: "
)

func replyQuoted(s string) string {
	lines := strings.Split(s, "\n")
	var ret []string
	for _, l := range lines {
		ret = append(ret, "> "+l)
	}
	return strings.Join(ret, "\n")
}

func forward(ctx context.Context, conn *cmdg.CmdG, keys *input.Input, msg *cmdg.Message) error {
	// Get recipient
	to, err := dialog.Selection(conn.Contacts(), true, keys)
	if err != nil {
		return err
	}
	orig, err := msg.GetHeader(ctx, "From")
	if err != nil {
		return err
	}

	date, err := msg.GetTime(ctx)
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

	subj, err := msg.GetHeader(ctx, "Subject")
	if err != nil {
		return err
	}

	b, err := msg.GetUnpatchedBody(ctx)
	if err != nil {
		return err
	}

	prefill := []string{
		fmt.Sprintf("To: %s", to),
		fmt.Sprintf("Subject: %s%s", forwardPrefix, subj),
		"",
		fmt.Sprintf("On %s, %s said:", date.Format("Mon, 2 Jan 2006 15:04:05 -0700"), orig),
	}
	return compose(ctx, conn, keys, strings.Join(prefill, "\n")+"\n"+replyQuoted(b))
}

func reply(ctx context.Context, conn *cmdg.CmdG, keys *input.Input, msg *cmdg.Message) error {
	orig, err := msg.GetHeader(ctx, "From")
	if err != nil {
		return err
	}
	date, err := msg.GetTime(ctx)
	if err != nil {
		return err
	}
	to, err := msg.GetReplyTo(ctx)
	if err != nil {
		return err
	}
	subj, err := msg.GetHeader(ctx, "Subject")
	if err != nil {
		return err
	}
	subj = strings.TrimPrefix(subj, replyPrefix)

	b, err := msg.GetUnpatchedBody(ctx)
	if err != nil {
		return err
	}

	prefill := []string{
		fmt.Sprintf("To: %s", to),
		fmt.Sprintf("Subject: %s%s", replyPrefix, subj),
		"",
		fmt.Sprintf("On %s, %s said:", date.Format("Mon, 2 Jan 2006 15:04:05 -0700"), orig),
		replyQuoted(b),
	}
	return compose(ctx, conn, keys, strings.Join(prefill, "\n"))
}

func replyAll(ctx context.Context, conn *cmdg.CmdG, keys *input.Input, msg *cmdg.Message) error {
	to, cc, err := msg.GetReplyToAll(ctx)
	if err != nil {
		return err
	}
	orig, err := msg.GetHeader(ctx, "From")
	if err != nil {
		return err
	}
	date, err := msg.GetTime(ctx)
	if err != nil {
		return err
	}
	subj, err := msg.GetHeader(ctx, "Subject")
	if err != nil {
		return err
	}
	subj = strings.TrimPrefix(subj, replyPrefix)
	b, err := msg.GetUnpatchedBody(ctx)
	if err != nil {
		return err
	}

	prefill := []string{
		fmt.Sprintf("To: %s", to),
	}
	if len(cc) != 0 {
		prefill = append(prefill, fmt.Sprintf("CC: %s", cc))
	}
	prefill = append(prefill, fmt.Sprintf("Subject: %s%s", replyPrefix, subj))
	prefill = append(prefill,
		"",
		fmt.Sprintf("On %s, %s said:", date.Format("Mon, 2 Jan 2006 15:04:05 -0700"), orig))
	return compose(ctx, conn, keys, strings.Join(prefill, "\n")+"\n"+replyQuoted(b))
}
