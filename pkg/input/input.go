// Package input provides raw input handling.
package input

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"golang.org/x/crypto/ssh/terminal"
	"golang.org/x/sys/unix"
)

const (
	fd = 0

	// Named keys.
	EscChar = 27

	CtrlC     = "\x03"
	CtrlH     = "\x08"
	Return    = "\x0a"
	CtrlL     = "\x0c"
	Enter     = "\x0d"
	CtrlN     = "\x0e"
	CtrlP     = "\x10"
	CtrlR     = "\x12"
	CtrlS     = "\x13"
	CtrlU     = "\x15"
	CtrlV     = "\x16"
	Esc       = "\x1b"
	Backspace = "\x7F"

	// Multibyte chars.
	multibyteOneMore  = "O["
	multibyteStopChar = '~'
	Up                = "\x1B[A"
	Down              = "\x1B[B"
	Right             = "\x1B[C"
	Left              = "\x1B[D"
	F1                = "\x1BOP"
	F2                = "\x1BOQ"
	F3                = "\x1BOR"
	F4                = "\x1BOS"
	Home              = "\x1B[1~"
	End               = "\x1B[4~"
	PgUp              = "\x1B[5~"
	PgDown            = "\x1B[6~"
)

var (
	repeatProtection = 5 * time.Millisecond

	errTimeout = fmt.Errorf("timeout")

	readKeyTimeout       = 50 * time.Millisecond
	readMultibyteTimeout = 10 * time.Millisecond
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

func duration2Timeval(timeout time.Duration) *unix.Timeval {
	// Workaround to set unix.Timeval since unix.Timeval.Usec is
	// different types on different systems. :-(
	// By last check it can only be int32 and int64:
	// grep -A 2 ^'type Timeval struct ' ~/go/src/golang.org/x/sys/unix/*.go | grep Usec | sed 's/.*go-//' | awk '{print $2}' | sort | uniq
	tv := &unix.Timeval{
		Sec: timeout.Nanoseconds() / 1e9,
	}
	var usec interface{}
	usec = &tv.Usec
	switch u := usec.(type) {
	case *int64:
		*u = (timeout.Nanoseconds() / 1000) % 1e6
	case *int32:
		*u = int32((timeout.Nanoseconds() / 1000) % 1e6)
	default:
		log.Errorf("Unknown type of unix.Timeval.Usec")
		tv.Sec = 0
		tv.Usec = 50000
	}
	return tv
}

// Return a key, or errTimeout if no key was pressed.
func readByte(fd int, timeout time.Duration) (byte, error) {
	deadline := time.Now().Add(timeout)

	// TODO: cleaner way to do this?
	// Drawbacks:
	// * Takes 50ms to shut down
	// * Spins the CPU a bit
	// * Wake up CPU at least every 50ms
	fds := unix.FdSet{}
	fds.Bits[uint(fd)/unix.NFDBITS] |= (1 << (uint(fd) % unix.NFDBITS))
	var n int
	var err error
	for {
		to := deadline.Sub(time.Now())
		if to < 0 {
			return 0, errTimeout
		}
		n, err = unix.Select(fd+1, &fds, &unix.FdSet{}, &unix.FdSet{}, duration2Timeval(to))
		if err == syscall.EINTR {
			// log.Debugf("unix.Select() returned EINTR")
		} else {
			break
		}
	}
	if err != nil {
		return 0, errors.Wrapf(err, "unix.Select()")
	}
	if n != 1 {
		return 0, errTimeout
	}
	//idle := keyTime.Sub(last)
	b := make([]byte, 1, 1)
	//log.Infof("Non-iowait input time: %v", idle)
	// log.Infof("About to read")

	if n, err := os.Stdin.Read(b); err != nil {
		return 0, errors.Wrapf(err, "reading byte")
	} else if n != 1 {
		return 0, fmt.Errorf("read %d bytes, expected 1", n)
	}
	//log.Infof("Read byte: %v", b[0])
	return b[0], nil
}

// maxTimeout returns the timeout, unless it's after the deadline, in which case it returns as much of the timeout as it can.
func maxTimeout(deadline time.Time, timeout time.Duration) time.Duration {
	return maxTimeoutNow(time.Now(), deadline, timeout)
}

func maxTimeoutNow(now time.Time, deadline time.Time, timeout time.Duration) time.Duration {
	t := now.Add(timeout)
	if deadline.After(t) {
		return timeout
	}
	if now.After(deadline) {
		return time.Duration(0)
	}
	return deadline.Sub(now)
}

// readKey reads a whole key including multibyte keys.
func readKey(fd int) (string, error) {
	deadline := time.Now().Add(readKeyTimeout)

	// Read a byte.
	b, err := readByte(fd, maxTimeout(deadline, readKeyTimeout))
	if err == errTimeout {
		return "", err
	}
	if err != nil {
		return "", errors.Wrapf(err, "reading key byte")
	}

	// Construct full key.
	key := fmt.Sprintf("%c", b)
	if b != EscChar {
		// TODO: For these multibytes, should they be checked
		// with utf8.DecodeRune, utf8.FullRune, or utf8.Valid?
		if (b & 0xe0) == 0xc0 {
			// Two-byte UTF-8.
			// Example: ö
			b2, err := readByte(fd, maxTimeout(deadline, readMultibyteTimeout))
			if err != nil {
				return "", err
			}
			return string([]byte{b, b2}), nil
		}
		if (b & 0xf0) == 0xe0 {
			// Three-byte UTF-8.
			// Example: ☃
			b2, err := readByte(fd, maxTimeout(deadline, readMultibyteTimeout))
			if err != nil {
				return "", err
			}
			b3, err := readByte(fd, maxTimeout(deadline, readMultibyteTimeout))
			if err != nil {
				return "", err
			}
			return string([]byte{b, b2, b3}), nil
		}
		if (b & 0xf8) == 0xf0 {
			// Four-byte UTF-8.
			// Example: 𐍈
			b2, err := readByte(fd, maxTimeout(deadline, readMultibyteTimeout))
			if err != nil {
				return "", err
			}
			b3, err := readByte(fd, maxTimeout(deadline, readMultibyteTimeout))
			if err != nil {
				return "", err
			}
			b4, err := readByte(fd, maxTimeout(deadline, readMultibyteTimeout))
			if err != nil {
				return "", err
			}
			return string([]byte{b, b2, b3, b4}), nil
		}
		return key, nil
	}

	// More bytes to read. Carry on.
	b, err = readByte(fd, maxTimeout(deadline, readMultibyteTimeout))
	if err == errTimeout {
		// Plain esc.
		return key, nil
	}
	if err != nil {
		return "", errors.Wrapf(err, "reading second byte in multibyte")
	}

	if strings.Contains(multibyteOneMore, fmt.Sprintf("%c", b)) {
		// This is how arrow keys show up.
		b2, err := readByte(fd, maxTimeout(deadline, readMultibyteTimeout))
		if err == errTimeout {
			log.Errorf("Got unknown multibyte sequence (Esc,<something>,<nothing>)")
			return "", err
		}
		if err != nil {
			return "", errors.Wrapf(err, "reading third byte in multibyte")
		}
		if strings.Contains("0123456789", fmt.Sprintf("%c", b2)) {
			s := fmt.Sprintf("%c%c%c", EscChar, b, b2)
			for {
				b, err := readByte(fd, maxTimeout(deadline, readMultibyteTimeout))
				if err == errTimeout {
					log.Errorf("Got unknown multibyte sequence (%q)", s)
					return "", err
				}
				s += fmt.Sprintf("%c", b)

				if b == multibyteStopChar {
					break
				}
			}
			return s, nil
		}
		return fmt.Sprintf("%c%c%c", EscChar, b, b2), nil
	}
	if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') {
		return fmt.Sprintf("Meta-%c", b), nil
	}
	log.Errorf("Discarding key %v because it came right after escape", b)
	return key, nil
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
			select {
			case <-i.stop:
				log.Infof("Input loop told to stop")
				return
			default:
				// go on
			}

			key, err := readKey(fd)
			if errors.Cause(err) == errTimeout {
				continue
			}
			if err != nil {
				log.Errorf("Reading key: %v", err)
				// This could very well be benign, so don't kill the input loop.
				continue
			}
			if key == "" {
				log.Errorf("Read key successfully, but it was empty string")
				continue
			}

			// log.Infof("read done")
			keyTime := time.Now()
			if i.pasteProtection() && keyTime.Sub(last) < repeatProtection {
				log.Warningf("Paste protection blocked keypress %q registering. %v < %v", key, keyTime.Sub(last), repeatProtection)
				last = keyTime
				continue
			}
			if keyTime.Sub(lastEnter) < repeatProtection {
				log.Warningf("Post-enter paste protection blocked keypress %q registering. %v < %v", key, keyTime.Sub(lastEnter), repeatProtection)
				last = keyTime
				continue
			}

			i.keys <- key
			if key == Enter || key == Return {
				lastEnter = keyTime
			}

			last = keyTime
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
