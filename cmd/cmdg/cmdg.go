// cmdg is the next generation cmdg.
//
// TODO before it can replace old code:
// * HTML emails.
// * Reconnecting (not needed?)
// * Periodic refresh of inbox
//
// Missing features that can wait:
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
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/ThomasHabets/cmdg/pkg/cmdg"
	"github.com/ThomasHabets/cmdg/pkg/gpg"
	"github.com/ThomasHabets/cmdg/pkg/input"
)

var (
	cfgFile   = flag.String("config", "", "Config file. Default is ~/"+path.Join(defaultConfigDir, configFileName))
	gpgFlag   = flag.String("gpg", "gpg", "Path to GnuPG.")
	logFile   = flag.String("log", "/dev/null", "Log debug data to this file.")
	configure = flag.Bool("configure", false, "Configure OAuth.")

	conn *cmdg.CmdG

	// Relative to configDir.
	configFileName = "cmdg.conf"

	// Relative to $HOME.
	defaultConfigDir = ".cmdg"

	pagerBinary  string
	visualBinary string

	labelReloadTime = time.Minute
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

	if *configure {
		if err := cmdg.Configure(configFilePath()); err != nil {
			log.Fatalf("Configuring: %v", err)
		}
		return
	}

	ctx := context.Background()

	pagerBinary = os.Getenv("PAGER")
	if len(pagerBinary) == 0 {
		log.Fatalf("You need to set the PAGER environment variable. When in doubt, set to 'less'.")
	}

	visualBinary = os.Getenv("VISUAL")
	if len(visualBinary) == 0 {
		visualBinary = os.Getenv("EDITOR")
		if len(visualBinary) == 0 {
			log.Fatalf("You need to set the VISUAL or EDITOR environment variable. Set to your favourite editor.")
		}
	}

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
	log.Infof("Contacts loaded")

	go func() {
		ch := time.Tick(labelReloadTime)
		for {
			<-ch
			if err := conn.LoadLabels(ctx); err != nil {
				log.Errorf("Loading labels: %v", err)
			} else {
				log.Infof("Reloaded labels")
			}
			if err := conn.LoadContacts(ctx); err != nil {
				log.Errorf("Loading contacts: %v", err)
			} else {
				log.Infof("Reloaded contacts")
			}
		}
	}()

	// Redirect logging.
	{
		f, err := os.OpenFile(*logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			log.Fatalf("Can't create logfile %q: %v", *logFile, err)
		}
		defer f.Close()
		log.SetOutput(f)
		log.SetFormatter(&log.TextFormatter{
			DisableColors: true,
		})
	}

	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}
