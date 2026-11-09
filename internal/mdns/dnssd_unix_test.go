//go:build unix

package mdns

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeDNSSD writes a stand-in for dns-sd into a temp dir. Every invocation
// appends its pid to <dir>/pids (`exec` keeps that pid for the long-running
// part, so it is the process the driver kills) and its arguments to
// <dir>/args, then runs body with $n set to the invocation's 1-based number.
func fakeDNSSD(t *testing.T, body string) (bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	bin = filepath.Join(dir, "dns-sd")
	script := "#!/bin/sh\n" +
		"echo $$ >> '" + dir + "/pids'\n" +
		"printf '%s\\n' \"$@\" > '" + dir + "/args'\n" +
		"n=$(wc -l < '" + dir + "/pids' | tr -d ' ')\n" +
		"echo \"Registering Service $2.$3.$4 port $5\"\n" +
		body
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, dir
}

// registered is the reply line real dns-sd prints once the name is active.
const registered = `echo "12:00:00.000  Got a reply for service $2.$3.$4.: Name now registered and active"` + "\n"

func testDNSSD(bin string, txt ...string) *dnssd {
	d := newDNSSD(bin, "rig", 7800, txt)
	d.confirmTimeout = 2 * time.Second
	d.minRetry = 10 * time.Millisecond
	d.maxRetry = 50 * time.Millisecond
	return d
}

func pids(t *testing.T, dir string) []int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "pids"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		t.Fatal(err)
	}
	var out []int
	for _, f := range strings.Fields(string(b)) {
		pid, err := strconv.Atoi(f)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, pid)
	}
	return out
}

// gone reports whether pid has exited and been reaped.
func gone(pid int) bool {
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDNSSDAnnounceRegistersAndWithdrawsOnCancel(t *testing.T) {
	bin, dir := fakeDNSSD(t, registered+"exec sleep 600\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d := testDNSSD(bin, "version=v1", `odd=a\b`)
	if err := d.announce(ctx); err != nil {
		t.Fatalf("announce: %v", err)
	}
	args, err := os.ReadFile(filepath.Join(dir, "args"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Split(strings.TrimSuffix(string(args), "\n"), "\n"); !slices.Equal(got, d.args) {
		t.Fatalf("dns-sd ran with %q, want %q", got, d.args)
	}

	p := pids(t, dir)
	if len(p) != 1 || gone(p[0]) {
		t.Fatalf("want one live dns-sd, got pids %v", p)
	}
	cancel()
	eventually(t, "dns-sd to be killed on cancel", func() bool { return gone(p[0]) })
	time.Sleep(50 * time.Millisecond)
	if n := len(pids(t, dir)); n != 1 {
		t.Fatalf("dns-sd restarted after cancel: %d starts", n)
	}
}

func TestDNSSDAnnounceFailsWhenDNSSDExits(t *testing.T) {
	bin, dir := fakeDNSSD(t, "echo 'DNSService call failed -65563' >&2\nexit 1\n")
	err := testDNSSD(bin).announce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "exited before registering") || !strings.Contains(err.Error(), "-65563") {
		t.Fatalf("announce error = %v, want the exit with dns-sd's stderr", err)
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(pids(t, dir)); n != 1 {
		t.Fatalf("failed initial registration was retried: %d starts", n)
	}
}

func TestDNSSDAnnounceFailsOnErrorReply(t *testing.T) {
	bin, dir := fakeDNSSD(t,
		`echo "12:00:00.000  Got a reply for service $2.$3.$4.: Name in use, please choose another"`+"\nexec sleep 600\n")
	err := testDNSSD(bin).announce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Name in use") {
		t.Fatalf("announce error = %v, want the error reply", err)
	}
	if p := pids(t, dir); len(p) != 1 || !gone(p[0]) {
		t.Fatalf("dns-sd not stopped after an error reply: pids %v", p)
	}
}

func TestDNSSDAnnounceTimesOut(t *testing.T) {
	bin, dir := fakeDNSSD(t, "exec sleep 600\n")
	d := testDNSSD(bin)
	d.confirmTimeout = 100 * time.Millisecond
	err := d.announce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no registration reply") {
		t.Fatalf("announce error = %v, want a timeout", err)
	}
	if p := pids(t, dir); len(p) != 1 || !gone(p[0]) {
		t.Fatalf("dns-sd not stopped after the timeout: pids %v", p)
	}
}

func TestDNSSDAnnounceMissingBinary(t *testing.T) {
	err := testDNSSD(filepath.Join(t.TempDir(), "no-dns-sd")).announce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "start dns-sd") {
		t.Fatalf("announce error = %v, want a start failure", err)
	}
}

func TestDNSSDReannouncesWhenDNSSDDies(t *testing.T) {
	// The first two runs register and then die (as on an mDNSResponder
	// restart); the second one's restart fails outright once; the fourth
	// run stays up.
	bin, dir := fakeDNSSD(t, `case $n in
1|2) `+strings.TrimSuffix(registered, "\n")+`; sleep 0.05; exit 1 ;;
3) exit 2 ;;
esac
`+registered+"exec sleep 600\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := testDNSSD(bin).announce(ctx); err != nil {
		t.Fatalf("announce: %v", err)
	}
	eventually(t, "four dns-sd starts", func() bool { return len(pids(t, dir)) >= 4 })
	time.Sleep(100 * time.Millisecond)
	p := pids(t, dir)
	if len(p) != 4 {
		t.Fatalf("want 4 starts, got %d", len(p))
	}
	if gone(p[3]) {
		t.Fatal("re-announced dns-sd is not running")
	}
	cancel()
	eventually(t, "dns-sd to be killed on cancel", func() bool { return gone(p[3]) })
}
