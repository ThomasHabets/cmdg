// Raw input handling.
package input

import (
	"os"
	"syscall"
	"time"

	log "github.com/sirupsen/logrus"
	"golang.org/x/crypto/ssh/terminal"
)

const (
	fd        = 0
	Backspace = 127
	Enter     = 13
	CtrlC     = 3
	CtrlR     = 18
	CtrlN     = 14
	CtrlP     = 16
	CtrlV     = 22
)

type Input struct {
	running chan struct{} // Closed (non-blocking) if running.
	stop    chan struct{} // Close to stop.
	keys    chan byte     // Open if running.
}

func (i *Input) Chan() <-chan byte {
	return i.keys
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
	for _ = range i.keys {
	}
	<-i.running
	log.Infof("Keyboard input stopped")
}

// Turn on raw mode and receive keys.
func (i *Input) Start() error {
	log.Infof("Starting keyboard input")
	oldState, err := terminal.MakeRaw(fd)
	if err != nil {
		return err
	}
	i.running = make(chan struct{})
	i.stop = make(chan struct{})
	i.keys = make(chan byte)
	go func() {
		defer close(i.running)
		defer close(i.keys)
		defer terminal.Restore(fd, oldState)
		last := time.Now()
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
				b := make([]byte, 1, 1)
				log.Infof("Non-iowait input time: %v", time.Since(last))
				log.Infof("About to read")
				n, err := os.Stdin.Read(b)
				log.Infof("read done")
				last = time.Now()

				if err != nil {
					log.Errorf("Read returned error: %v", err)
					return
				} else if n != 1 {
					log.Errorf("Read returned other than 1: %d", n)
					return
				}
				i.keys <- b[0]
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
	return &Input{}
}
