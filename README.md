# cmdg - A command line client to GMail

Copyright Thomas Habets <thomas@habets.se> 2015-2021

https://github.com/ThomasHabets/cmdg

## License

This software is dual-licensed GPL and "Thomas is allowed to release a
binary version that adds shared API keys and nothing else".

## Introduction

cmdg is a commandline client to GMail that provides a UI more similar
to Pine/Alpine.

It uses the GMail API to interact with your mailbox. This has several
benefits.

### Benefits over IMAP
* No passwords stored on disk. (application-specific passwords are
  also passwords, and can be used for more than GMail). OAuth2 is used
  instead, and cmdgs access can be revoked
  [here](https://security.google.com/settings/security/permissions).
  cmdg can only access your GMail, and cannot lose your password even
  if the machine it runs on gets hacked.
* The "labels" model is native in the cmdg UI, unlike IMAP clients
  that try to map GMail labels onto IMAP.
* Contacts are taken from your Google contacts
* TODO: other benefits, I'm sure.

### Benefits over the GMail web UI
* Emacs-ish keys. If there's a need the key mapping can be made
  configurable.
* Uses a real $EDITOR.
* Really fast. No browser, CSS, or javascript getting in the way.
* Proper quoting. The GMail web UI encourages top-posting. Ugh.
* It uses 100% keyboard navigation. GMail web UI has very good
  keyboard navigation for a web app, but still requires mouse for
  a few things.
* cmdg works without a graphic terminal.
* cmdg uses less bandwidth (citation needed), and much less memory.
* Local GPG integration. There are currently no *good* ways to
  integrate GPG with the GMail web UI.

### A security difference
* GMail web UI uses username and password to log in, which means they
  can be stolen. You should be using [U2F
  Yubikeys](https://www.yubico.com/products/yubikey-hardware/fido-u2f-security-key/),
  so that losing the password isn't as big of a deal. The user has to
  re-type the password every now and then, which is an opportunity for
  the attacker to steal the password.
* OAuth token in cmdg.conf can be copied, and the thief would be
  able to access the users GMail until the key is revoked. The
  access does not expire on its own.

## Installing

### Building manually

```
$ go build ./cmd/cmdg
$ sudo cp cmdg /usr/local/bin
```

### Homebrew (maintained by separate author)

```
brew tap cutzenfriend/homebrew-cmdg
brew install cmdg
```

## Configuring
You need to configure `cmdg` in order to provide it with authentication
so it can talk to GMail on your behalf. To do this you'll need to generate
a ClientID and ClientSecret. You can do this with the following steps:

  1. Go to the [Google Developers Console](https://console.developers.google.com/apis).
  1. Select an existing project or create a new project
  1. Navigate to the "APIs & Services > OAuth consent screen" page.
  1. Fill out the OAuth consent screen.
  1. Make sure to add scopes for the various APIs you'll need
     1. Drive API - `.../auth/drive.appdata`
     1. Gmail API - `../auth/gmail.modify`
     1. People API - `.../auth/contacts.readonly`
  1. Click the button "Create Credentials" and select "OAuth client ID" from the dropdown.
  1. Set the "Application type" to "Desktop app" and make the name anything you'd like.
  1. This should give you a Client ID and Client Secret you can provide to `cmdg`.

```
$ cmdg -configure
[It will ask about ClientID and ClientSecret.
For now you have create one at https://console.developers.google.com]
Cut and paste this URL into your browser:
  https://long-url....
Returned code: <paste code here>
$
```
This creates `~/.cmdg/cmdg.conf`.

## Running
```
$ cmdg
```
For keyboard shortcuts press '?' or F1 in most screens.

To quit, press 'q'.
