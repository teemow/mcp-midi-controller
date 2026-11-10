// Package hostawake keeps the daemon's host awake while the daemon keeps time.
//
// A host that dozes when nobody is at it fires timers late: a laptop cycling
// between sleep and dark wake stalls a Go timer by a second and more, which is
// a hole in anything the daemon plays against the wall clock. Hold asks the
// host to stay awake for as long as that matters.
//
// Only macOS is wired (caffeinate_darwin.go): a Mac left alone dozes under the
// running daemon. Everywhere else Hold does nothing. The driver that runs the
// host's own tool is platform neutral, so it is tested everywhere against a
// stand-in.
package hostawake

import (
	"log"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
)

// Hold keeps the host awake until the returned release is called. It never
// fails: a host that cannot be held awake is logged and left as it is, and
// release then does nothing. release may be called more than once and from
// any goroutine; it returns once the hold is gone.
func Hold() (release func()) {
	return hold(command(os.Getpid()))
}

// hold runs argv, a tool that keeps the host awake for as long as it lives,
// and returns the func that kills it. An empty argv holds nothing.
func hold(argv []string) (release func()) {
	if len(argv) == 0 {
		return func() {}
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	if err := cmd.Start(); err != nil {
		log.Printf("hostawake: %v: the host is not held awake", err)
		return func() {}
	}
	var released atomic.Bool
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		err := cmd.Wait()
		if !released.Load() {
			log.Printf("hostawake: %s ended while it was needed (%v): the host is no longer held awake", argv[0], err)
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			released.Store(true)
			_ = cmd.Process.Kill()
			<-exited
		})
	}
}
