// cmdg is the next generation cmdg.
//
// TODO before it can replace old code:
// * HTML emails.
// * Reconnecting (not needed?)
// * Error messages (as opposed to just exiting)
// * Add/remove label
// * Periodic refresh of inbox, labels, and contacts
//
// Missing features that can wait
// * colors on labels
// * attach stuff on send
// * sign on send
// * encrypt on send
package main

import (
	"context"
	"flag"
	"os"
	"path"
	"syscall"

	log "github.com/sirupsen/logrus"

	"github.com/ThomasHabets/cmdg/pkg/cmdg"
	"github.com/ThomasHabets/cmdg/pkg/gpg"
	"github.com/ThomasHabets/cmdg/pkg/input"
)

var (
	cfgFile = flag.String("config", "", "Config file. Default is ~/"+path.Join(defaultConfigDir, configFileName))
	gpgFlag = flag.String("gpg", "gpg", "Path to GnuPG.")

	conn *cmdg.CmdG

	// Relative to configDir.
	configFileName = "cmdg.conf"

	// Relative to $HOME.
	defaultConfigDir = ".cmdg"
)

func configFilePath() string {
	if *cfgFile != "" {
		return *cfgFile
	}
	return path.Join(os.Getenv("HOME"), defaultConfigDir, configFileName)
}

func run(ctx context.Context) error {
	keys := input.New()
	if err := keys.Start(); err != nil {
		return err
	}

	v := NewMessageView(ctx, "INBOX", "", keys)

	if err := v.Run(ctx); err != nil {
		log.Errorf("Bailing due to error: %v", err)
	}
	log.Infof("MessageView returned, stopping keys")
	keys.Stop()
	log.Infof("Shutting down")
	return nil
}

func main() {
	syscall.Umask(0077)
	flag.Parse()
	ctx := context.Background()
	cmdg.GPG = gpg.New(*gpgFlag)

	var err error
	conn, err = cmdg.New(configFilePath())
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	log.Infof("Connected")
	if err := conn.LoadLabels(ctx); err != nil {
		log.Fatalf("Loading labels: %v", err)
	}
	log.Infof("Labels loaded")
	if err := conn.LoadContacts(ctx); err != nil {
		log.Fatalf("Loading contacts: %v", err)
	}
	log.Infof("Contacts loadedo")

	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}
