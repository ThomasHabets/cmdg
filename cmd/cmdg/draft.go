package main

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/ThomasHabets/cmdg/pkg/cmdg"
	"github.com/ThomasHabets/cmdg/pkg/dialog"
	"github.com/ThomasHabets/cmdg/pkg/input"
)

func continueDraft(ctx context.Context, conn *cmdg.CmdG, keys *input.Input) error {
	drafts, err := conn.ListDrafts(ctx)
	if err != nil {
		return errors.Wrap(err, "listing drafts")
	}

	var opts []*dialog.Option
	for _, d := range drafts {
		to, err := d.GetHeader(ctx, "To")
		if err != nil {
			to = "<no recipient>"
		}

		subj, err := d.GetHeader(ctx, "Subject")
		if err != nil {
			subj = "<no subject>"
		}

		opts = append(opts, &dialog.Option{
			Key:   d.ID,
			Label: fmt.Sprintf("To:%s Subj:%s", to, subj),
		})
	}

	dOpt, err := dialog.Selection(opts, "Draft (NOT IMPLEMENTED YET)> ", false, keys)
	if err == dialog.ErrAborted {
		return nil
	} else if err != nil {
		return errors.Wrap(err, "dialog failed")
	}
	log.Infof("Draft selected: %s %q", dOpt.Key, dOpt)

	return nil
}
