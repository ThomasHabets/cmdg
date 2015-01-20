// Copyright Thomas Habets <thomas@habets.se> 2015
package main

import (
	"bytes"
	"log"
	"os/exec"
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

func openMessagePrint(w *gc.Window, msgs []*gmail.Message, current int, marked bool, currentLabel string) {
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

	w.Clear()
	bodyLines := breakLines(strings.Split(getBody(m), "\n"))
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

	_, width := w.MaxYX()
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
	for {
		nc.ApplyMain(func(w *gc.Window) {
			openMessagePrint(w, msgs, current, marked[msgs[current].Id], currentLabel)
		})
		key := <-nc.Input
		nc.Status("OK")
		switch key {
		case 'q':
			return true
		case gc.KEY_LEFT, '<', 'u':
			return false
		case 16: // CtrlP
			if current > 0 {
				current--
			}
		case 14: // CtrlN
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
			openMessageCmdGPGVerify(msgs[current])
		case 'n': // Scroll up.
		case 'p': // Scroll down.
		case ' ': // Page down .
		case '\b': // Page up..
		default:
			nc.Status("unknown key: %v", gc.KeyString(key))
		}
	}
}

func openMessageCmdGPGVerify(msg *gmail.Message) {
	nc.Status("Verifying...")
	in := bytes.NewBuffer([]byte(getBody(msg)))
	cmd := exec.Command(*gpg, "-v")
	cmd.Stdin = in
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
	if cmd.ProcessState.Success() {
		nc.Status("[green]Verify succeeded")
	} else if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok {
		switch uint32(ws) {
		case 1:
			nc.Status("[red]Signature found, but BAD")
		default:
			nc.Status("[red]Unable to verify anything")
		}
	} else {
		nc.Status("[red]Verify failed: nc.Status %v", cmd.ProcessState.String())
	}
}
