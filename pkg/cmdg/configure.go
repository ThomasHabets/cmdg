package cmdg

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"strings"

	"github.com/pkg/errors"
	"golang.org/x/oauth2"
)

const (
	spaces               = "\n\t\r "
	oauthRedirectOffline = "urn:ietf:wg:oauth:2.0:oob"

	// Populate these for a binary-only release.
	defaultClientID     = ""
	defaultClientSecret = ""
)

// ConfigOAuth contains the config for the oauth.
type ConfigOAuth struct {
	ClientID, ClientSecret, RefreshToken, AccessToken, APIKey string
}

// Config is… hmm… this should probably be cleand up.
type Config struct {
	OAuth ConfigOAuth
}

func readLine(s string) (string, error) {
	reader := bufio.NewReader(os.Stdin)
	fmt.Printf(s)
	id, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	id = strings.Trim(id, spaces)
	return id, nil
}

func auth(cfg ConfigOAuth) (string, error) {
	at := oauth2.AccessTypeOffline
	if accessType == "online" {
		at = oauth2.AccessTypeOnline
	}
	ocfg := oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://accounts.google.com/o/oauth2/auth",
			TokenURL: "https://accounts.google.com/o/oauth2/token",
		},
		Scopes:      []string{scope},
		RedirectURL: oauthRedirectOffline,
	}
	fmt.Printf("Cut and paste this URL into your browser:\n  %s\n", ocfg.AuthCodeURL("", at))
	fmt.Printf("Returned code: ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return "", err
	}
	token, err := ocfg.Exchange(oauth2.NoContext, line)
	if err != nil {
		return "", err
	}
	return token.RefreshToken, nil
}

func makeConfig() ([]byte, error) {
	var err error

	id := defaultClientID
	secret := defaultClientSecret

	if id == "" {
		id, err = readLine("ClientID: ")
		if err != nil {
			return nil, err
		}
	}
	if secret == "" {
		secret, err = readLine("ClientSecret: ")
		if err != nil {
			return nil, err
		}
	}

	token, err := auth(ConfigOAuth{
		ClientID:     id,
		ClientSecret: secret,
	})
	if err != nil {
		return nil, err
	}
	conf := &Config{
		OAuth: ConfigOAuth{
			ClientID:     id,
			ClientSecret: secret,
			RefreshToken: token,
		},
	}
	b, err := json.Marshal(conf)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// Configure sets up configuration with oauth and stuff.
func Configure(fn string) error {
	b, err := makeConfig()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(path.Dir(fn), 0700); err != nil {
		return errors.Wrapf(err, "creating config directory %q", path.Dir(fn))
	}
	return ioutil.WriteFile(fn, b, 0600)
}
