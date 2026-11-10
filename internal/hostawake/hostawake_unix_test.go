//go:build unix

package hostawake

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// standIn writes a stand-in for the host's stay-awake tool into a temp dir. It
// records its arguments in <dir>/args and its pid in <dir>/pid, then runs body
// (`exec` keeps that pid for the long-running part, so it is the process the
// hold kills).
func standIn(t *testing.T, body string) (bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	bin = filepath.Join(dir, "stay-awake")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > '" + dir + "/args'\n" +
		"echo $$ > '" + dir + "/pid'\n" +
		body
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, dir
}

// startedPid waits for the stand-in to report its pid.
func startedPid(t *testing.T, dir string) int {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if b, err := os.ReadFile(filepath.Join(dir, "pid")); err == nil && strings.HasSuffix(string(b), "\n") {
			pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err != nil {
				t.Fatal(err)
			}
			return pid
		}
	}
	t.Fatal("the tool was not started")
	return 0
}

// gone reports whether pid has exited and been reaped.
func gone(pid int) bool {
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

// returns fails the test unless release returns promptly.
func returns(t *testing.T, release func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		release()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("release did not return")
	}
}

// TestHoldRunsTheToolUntilReleased checks the tool runs with its arguments for
// as long as the hold lasts and is gone, reaped, once release returns.
func TestHoldRunsTheToolUntilReleased(t *testing.T) {
	bin, dir := standIn(t, "exec sleep 600\n")
	release := hold([]string{bin, "-i", "-s", "-w", "4711"})
	pid := startedPid(t, dir)
	args, err := os.ReadFile(filepath.Join(dir, "args"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Fields(string(args)), []string{"-i", "-s", "-w", "4711"}; !slices.Equal(got, want) {
		t.Errorf("the tool ran with %q, want %q", got, want)
	}
	time.Sleep(50 * time.Millisecond)
	if gone(pid) {
		t.Fatal("the tool ended before the hold was released")
	}

	returns(t, release)
	if !gone(pid) {
		t.Fatal("the tool outlived the release")
	}
	// A second release finds nothing to do.
	returns(t, release)
}

// TestHoldReleasedFromManyGoroutines checks concurrent releases all return
// with the tool gone.
func TestHoldReleasedFromManyGoroutines(t *testing.T) {
	bin, dir := standIn(t, "exec sleep 600\n")
	release := hold([]string{bin})
	pid := startedPid(t, dir)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release()
			if !gone(pid) {
				t.Error("a release returned while the tool was still running")
			}
		}()
	}
	wg.Wait()
}

// TestHoldSurvivesAToolThatEnds checks a tool that ends by itself (it was
// killed, or it refused its arguments) leaves a release that still returns.
func TestHoldSurvivesAToolThatEnds(t *testing.T) {
	bin, dir := standIn(t, "exit 1\n")
	release := hold([]string{bin})
	pid := startedPid(t, dir)
	for deadline := time.Now().Add(5 * time.Second); !gone(pid); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the ended tool was not reaped")
		}
	}
	returns(t, release)
}

// TestHoldWithoutATool checks a host with no tool, or one whose tool cannot be
// started, is left as it is: Hold never fails, and release does nothing.
func TestHoldWithoutATool(t *testing.T) {
	returns(t, hold(nil))
	returns(t, hold([]string{filepath.Join(t.TempDir(), "no-such-tool")}))
}
