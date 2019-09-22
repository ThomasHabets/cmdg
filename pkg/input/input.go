// Package input provides raw input handling.
package input

import (
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	log "github.com/sirupsen/logrus"
	"golang.org/x/crypto/ssh/terminal"
)

const (
	fd = 0

	// Named keys.
	EnterChar  = 0x0d
	ReturnChar = 0x0a

	CtrlC     = "\x03"
	CtrlH     = "\x08"
	Return    = "\x0a"
	CtrlL     = "\x0c"
	Enter     = "\x0d"
	CtrlN     = "\x0e"
	CtrlP     = "\x10"
	CtrlR     = "\x12"
	CtrlU     = "\x15"
	CtrlV     = "\x16"
	Backspace = "\x7F"

	Up    = "\x1B\x6B\x41"
	Down  = "\x1B\x6B\x42"
	Right = "\x1B\x6B\x43"
	Left  = "\x1B\x6B\x44"
)

var (
	repeatProtection = 5 * time.Millisecond
)

type Input struct {
	running chan struct{} // Closed (non-blocking) if running.
	stop    chan struct{} // Close to stop.
	winch   chan os.Signal
	keys    chan string // Open if running.

	m           sync.RWMutex
	pasteStatus []bool
}

func (i *Input) PastePush(b bool) {
	i.m.Lock()
	defer i.m.Unlock()
	i.pasteStatus = append(i.pasteStatus, b)
}

func (i *Input) PastePop() {
	i.m.Lock()
	defer i.m.Unlock()
	i.pasteStatus = i.pasteStatus[:len(i.pasteStatus)-1]
}

func (i *Input) pasteProtection() bool {
	i.m.RLock()
	defer i.m.RUnlock()
	if len(i.pasteStatus) == 0 {
		return true
	}
	return i.pasteStatus[len(i.pasteStatus)-1]
}

func (i *Input) Chan() <-chan string {
	return i.keys
}

func (i *Input) Winch() <-chan os.Signal {
	return i.winch
}

// Stop input loop, turn off raw mode.
func (i *Input) Stop() {
	log.Infof("Stopping keyboard input")
	select {
	case <-i.running:
		log.Infof("Called stop though already stopped")
		return // already stopped
	default:
	}
	close(i.stop)
	for range i.keys {
	}
	<-i.running
	log.Infof("Keyboard input stopped")
}

// Start turns on raw mode and the key-receive loop.
func (i *Input) Start() error {
	log.Infof("Starting keyboard input")
	oldState, err := terminal.MakeRaw(fd)
	if err != nil {
		return err
	}
	i.running = make(chan struct{})
	i.stop = make(chan struct{})
	i.keys = make(chan string)
	go func() {
		defer close(i.running)
		defer close(i.keys)
		defer terminal.Restore(fd, oldState)
		last := time.Now()
		lastEnter := time.Now()
		for {
			// TODO: cleaner way to do this?
			// Drawbacks:
			// * Takes 50ms to shut down
			// * Spins the CPU a bit
			// * Wake up CPU at least every 50ms
			fds := syscall.FdSet{
				Bits: [16]int64{1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
			}
			n, err := syscall.Select(fd+1, &fds, &syscall.FdSet{}, &syscall.FdSet{}, &syscall.Timeval{
				Sec:  0,
				Usec: 50000,
			})
			if err != nil {
				log.Errorf("syscall.Select(): %v", err)
			}
			if n == 1 {
				//idle := keyTime.Sub(last)
				b := make([]byte, 1, 1)
				//log.Infof("Non-iowait input time: %v", idle)
				// log.Infof("About to read")
				n, err := os.Stdin.Read(b)
				// log.Infof("read done")
				keyTime := time.Now()
				if i.pasteProtection() && keyTime.Sub(last) < repeatProtection {
					log.Warningf("Paste protection blocked keypress %v registering. %v < %v", b[0], keyTime.Sub(last), repeatProtection)
				} else if keyTime.Sub(lastEnter) < repeatProtection {
					log.Warningf("Post-enter paste protection blocked keypress %v registering. %v < %v", b[0], keyTime.Sub(lastEnter), repeatProtection)
				} else {
					if err != nil {
						log.Errorf("Read returned error: %v", err)
						return
					} else if n != 1 {
						log.Errorf("Read returned other than 1: %d", n)
						return
					}
					i.keys <- fmt.Sprintf("%c", b[0])
					if b[0] == EnterChar || b[0] == ReturnChar {
						lastEnter = keyTime
					}
				}
				last = keyTime
			}
			select {
			case <-i.stop:
				log.Infof("Input loop told to stop")
				return
			default:
				// go on
			}
			//log.Infof("Got key %d!!!", int(b[0]))
			// TODO: handle multibyte keys.
		}
	}()
	return nil
}

func New() *Input {
	i := &Input{
		winch: make(chan os.Signal, 1),
	}
	signal.Notify(i.winch, syscall.SIGWINCH)
	return i
}
