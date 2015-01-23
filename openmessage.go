// Copyright Thomas Habets <thomas@habets.se> 2015
package main

import (
	"bytes"
	"log"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	gc "code.google.com/p/goncurses"
	gmail "code.google.com/p/google-api-go-client/gmail/v1"
	"github.com/ThomasHabets/cmdg/ncwrap"
)

func notLabeled(m *gmail.Message) []string {
	ls := []string{}
nextLabel:
	for l := range labels {
		for _, hl := range m.LabelIds {
			if labels[l] == hl {
				continue nextLabel
			}
		}
		ls = append(ls, l)
	}
	sort.Sort(sortLabels(ls))
	return ls
}

func labeled(m *gmail.Message) []string {
	ls := []string{}
	for _, hl := range m.LabelIds {
		ls = append(ls, labelIDs[hl])
	}
	sort.Sort(sortLabels(ls))
	return ls
}

func maxScroll(lines, height int) int {
	return lines - height/2
}

func openMessagePrint(w *gc.Window, msgs []*gmail.Message, current int, marked bool, currentLabel string, scroll int) {
	m := msgs[current]
	go func() {
		if !hasLabel(m.LabelIds, unread) {
			return
		}
		id := m.Id
		st := time.Now()
		_, err := gmailService.Users.Messages.Modify(email, id, &gmail.ModifyMessageRequest{
			RemoveLabelIds: []string{unread},
		}).Do()
		if err != nil {
			// TODO: log to file or something.
		} else {
			log.Printf("Users.Messages.Modify(remove unread): %v", time.Since(st))
		}
	}()

	w.Move(0, 0)
	height, width := w.MaxYX()

	bodyLines := breakLines(strings.Split(getBody(m), "\n"))
	ms := maxScroll(len(bodyLines), height/2)
	if scroll > ms {
		scroll = ms
	}
	if scroll < 0 {
		scroll = 0
	}
	if len(bodyLines) > scroll {
		bodyLines = bodyLines[scroll:]
	}
	body := strings.Join(bodyLines, "\n")

	mstr := ""
	if marked {
		mstr = ", [bold]MARKED[unbold]"
	}
	ls := []string{}
	for _, l := range m.LabelIds {
		if l != currentLabel {
			ls = append(ls, labelIDs[l])
		}
	}
	sort.Sort(sortLabels(ls))

	lsstr := strings.Join(ls, ", ")
	if len(lsstr) > 0 {
		lsstr = ", " + lsstr
	}
	ncwrap.ColorPrint(w, `Email %d of %d%s
From: %s
To: %s
Date: %s
Subject: [bold]%s[unbold]
Labels: [bold]%s[unbold]%s
%s
%s`,
		current+1, len(msgs), ncwrap.Preformat(mstr),
		getHeader(m, "From"),
		getHeader(m, "To"),
		getHeader(m, "Date"),
		getHeader(m, "Subject"),
		labelIDs[currentLabel],
		lsstr,
		strings.Repeat("-", width),
		body)
}

// Return true if cmdg should quit.
func openMessageMain(msgs []*gmail.Message, current int, marked map[string]bool, currentLabel string) bool {
	nc.Status("Opening message")
	scroll := 0
	for {
		maxY, _ := winSize()
		nc.ApplyMain(func(w *gc.Window) {
			openMessagePrint(w, msgs, current, marked[msgs[current].Id], currentLabel, scroll)
		})
		key := <-nc.Input
		nc.Status("OK")
		switch key {
		case '?':
			helpWin(`q                 Quit
^P, k             Previous
^N, j             Next
f                 Forward
r                 Reply
a                 Reply all
e                 Archive
l                 Add label
L                 Remove label
x                 Mark message (TODO)
v                 Verify GPG signature
p                 Scroll up
n                 Scroll down
Space             Page down
Backspace         Page up
`)
		case 'q':
			return true
		case gc.KEY_LEFT, '<', 'u':
			return false
		case 16, 'k': // CtrlP
			scroll = 0
			if current > 0 {
				current--
			}
		case 14, 'j': // CtrlN
			scroll = 0
			if current < len(msgs)-1 {
				current++
			}
		case 'f':
			nc.Status("Composing forward")
			msg, err := getForward(msgs[current])
			if err != nil {
				nc.Status("Failed to compose forward: %v", err)
			} else {
				createSend(msgs[current].ThreadId, msg)
			}
		case 'r':
			nc.Status("Composing reply")
			msg, err := getReply(msgs[current])
			if err != nil {
				nc.Status("Failed to compose reply: %v", err)
			} else {
				createSend(msgs[current].ThreadId, msg)
			}
		case 'a':
			nc.Status("Composing reply to all")
			msg, err := getReplyAll(msgs[current])
			if err != nil {
				nc.Status("Failed to compose reply all: %v", err)
			} else {
				createSend(msgs[current].ThreadId, msg)
			}
		case 'e':
			st := time.Now()
			if _, err := gmailService.Users.Messages.Modify(email, msgs[current].Id, &gmail.ModifyMessageRequest{
				RemoveLabelIds: []string{inbox},
			}).Do(); err == nil {
				log.Printf("Users.Messages.Modify(archive): %v", time.Since(st))
				nc.Status("[green]OK, archived")
			} else {
				nc.Status("Failed to archive: %v", err)
			}
			return false
		case 'l':
			ls := notLabeled(msgs[current])
			id := getLabel("Add label>", ls)
			if id != "" {
				if _, err := gmailService.Users.Messages.Modify(email, msgs[current].Id, &gmail.ModifyMessageRequest{
					AddLabelIds: []string{id},
				}).Do(); err != nil {
					nc.Status("[red]Failed to apply label %q: %v", id, labelIDs[id], err)
				} else {
					nc.Status("[green]Applied label %q (%q)", id, labelIDs[id])
				}
			}

		case 'L':
			ls := labeled(msgs[current])
			id := getLabel("Remove label>", ls)
			if id != "" {
				if _, err := gmailService.Users.Messages.Modify(email, msgs[current].Id, &gmail.ModifyMessageRequest{
					RemoveLabelIds: []string{id},
				}).Do(); err != nil {
					nc.Status("[red]Failed to remove label %q (%q): %v", id, labelIDs[id], err)
				} else {
					nc.Status("[green]Removed label %q (%q)", id, labelIDs[id])
				}
			}

		case 'x':
			// TODO; Mark message
		case 'v':
			openMessageCmdGPGVerify(msgs[current], true)
		case 'n': // Scroll down.
			scroll++
		case 'p': // Scroll up.
			scroll--
		case ' ':
			scroll += maxY - 4
		case '\b', gc.KEY_BACKSPACE: // Page up..
			scroll -= maxY - 4
		default:
			nc.Status("unknown key: %v", gc.KeyString(key))
		}
		if scroll < 0 {
			scroll = 0
		}
		ms := maxScroll(len(breakLines(strings.Split(getBody(msgs[current]), "\n"))), maxY)
		if scroll > ms {
			scroll = ms
		}
	}
}

var (
	gpgKeyIDRE = regexp.MustCompile(`(?m)^gpg: Signature made (.+) using \w+ key ID (\w+)$`)
	gpgErrorRE = regexp.MustCompile(`(?m)^gpg: ((?:Can't check signature|BAD ).*)$`)
)

func downloadKey(keyID string) {
	cmd := exec.Command(*gpg, "--batch", "--no-tty", "--recv-keys", keyID)
	if err := cmd.Run(); err != nil {
		log.Printf("Failed to download GPG key %q: %v", keyID, err)
	}
}

func openMessageCmdGPGVerify(msg *gmail.Message, doDownload bool) {
	nc.Status("Verifying...")
	in := bytes.NewBuffer([]byte(getBody(msg)))
	var stderr bytes.Buffer
	cmd := exec.Command(*gpg, "-v", "--batch", "--no-tty")
	cmd.Stdin = in
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		nc.Status("[red]Verify failed to execute: %v", err)
		return
	}
	if err := cmd.Wait(); err != nil {
		if _, normal := err.(*exec.ExitError); !normal {
			nc.Status("[red]Verify failed, failed to run: %v", err)
			return
		}
	}

	// Extract key ID.
	keyID := "Unknown"
	m := gpgKeyIDRE.FindStringSubmatch(stderr.String())
	if len(m) == 3 {
		keyID = m[2]
	}

	// Extract error message.
	gpgError := "Unknown"
	m = gpgErrorRE.FindStringSubmatch(stderr.String())
	if len(m) == 2 {
		gpgError = m[1]
	}

	switch gpgError {
	case "Can't check signature: public key not found":
		// TODO: do this async.
		if doDownload {
			downloadKey(keyID)
			openMessageCmdGPGVerify(msg, false)
			return
		}
	}

	if cmd.ProcessState.Success() {
		nc.Status("[green]Verify succeeded")
	} else if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok {
		switch uint32(ws) {
		case 1:
			nc.Status("[red]Signature found, but BAD")
		default:
			nc.Status("[red]Unable to verify anything. Key ID: %s. Error: %s", keyID, gpgError)
		}
	} else {
		nc.Status("[red]Verify failed: nc.Status %v", cmd.ProcessState.String())
	}
}
