// med is 'ed' for (g)mail.

package main

import (
	"context"
	"flag"
	"syscall"

	log "github.com/sirupsen/logrus"
	//gmail "google.golang.org/api/gmail/v1"

	cmdg "github.com/ThomasHabets/cmdg/pkg/cmdg"
)

var (
	cfgFile = flag.String("conf", "cmdg.conf", "Config file.")
)

func list(ctx context.Context, cmdg *cmdg.CmdG) error {
	page, err := cmdg.ListMessages(ctx, "INBOX", "", "")
	if err != nil {
		return err
	}
	if true {
		if err := page.PreloadSubjects(ctx); err != nil {
			return err
		}
	}
	for n := range page.Response.Messages {
		/*
			msg, err := cmdg.GetMessage(ctx, m.Id)
			if err != nil {
				return err
			}
			log.Infof("Mail: %q", msg.GetHeader("subject"))
		*/
		s, err := page.Messages[n].GetHeader(ctx, "subject")
		if err != nil {
			return err
		}
		log.Infof("Mail: %q", s)
	}
	return nil
}

func main() {
	syscall.Umask(0077)
	flag.Parse()

	conn, err := cmdg.New(*cfgFile)
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	log.Infof("Connected")

	ctx := context.Background()
	if err := list(ctx, conn); err != nil {
		log.Fatalf("Failed to list INBOX: %v", err)
	}
}
