//go:build darwin

package hostawake

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ownCaffeinate is the pid of the caffeinate this process started, "" when
// there is none.
func ownCaffeinate() string {
	out, _ := exec.Command("/usr/bin/pgrep", "-P", strconv.Itoa(os.Getpid()), "-x", "caffeinate").Output()
	return strings.TrimSpace(string(out))
}

// asserting reports whether the host lists pid as a caffeinate that keeps it
// from idle sleep.
func asserting(t *testing.T, pid string) bool {
	t.Helper()
	out, err := exec.Command("/usr/bin/pmset", "-g", "assertions").Output()
	if err != nil {
		t.Fatalf("pmset: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "pid "+pid+"(caffeinate)") && strings.Contains(line, "PreventUserIdleSystemSleep") {
			return true
		}
	}
	return false
}

// TestHoldTakesThePowerAssertion runs the real caffeinate: for as long as the
// hold lasts the host lists it among its power assertions, on behalf of this
// process, and once release has returned it does not.
func TestHoldTakesThePowerAssertion(t *testing.T) {
	release := Hold()
	defer release()

	var pid string
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if pid = ownCaffeinate(); pid != "" && asserting(t, pid) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the host lists no assertion of this process's caffeinate (pid %q)", pid)
		}
	}
	args, err := exec.Command("/bin/ps", "-o", "args=", "-p", pid).Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	if got, want := strings.TrimSpace(string(args)), strings.Join(command(os.Getpid()), " "); got != want {
		t.Errorf("caffeinate runs as %q, want %q", got, want)
	}

	release()
	if left := ownCaffeinate(); left != "" {
		t.Errorf("caffeinate %s outlived the release", left)
	}
	if asserting(t, pid) {
		t.Errorf("the host still lists the assertion of caffeinate %s after the release", pid)
	}
}
