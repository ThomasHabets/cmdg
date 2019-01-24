package main

import (
	"context"
	"flag"

	log "github.com/sirupsen/logrus"

	"github.com/ThomasHabets/cmdg/pkg/cmdg"
	"github.com/ThomasHabets/cmdg/pkg/dialog"
	"github.com/ThomasHabets/cmdg/pkg/input"
)

var (
	openBinary = flag.String("open", "xdg-open", "Command to open attachments with.")
)

func listAttachments(ctx context.Context, keys *input.Input, msg *cmdg.Message) error {
	as, err := msg.Attachments(ctx)
	if err != nil {
		return err
	}
	ass := make([]string, len(as), len(as))
	for n, a := range as {
		ass[n] = a.Part.Filename
	}
	which, err := dialog.Selection(dialog.Strings2Options(ass), false, keys)
	log.Infof("Chose %q", which)
	return nil
}
