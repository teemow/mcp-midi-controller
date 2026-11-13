//go:build !windows

// Package scripts holds the tests of the shell scripts beside it.
package scripts

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// macRig is a stand-in for the Mac the update agent runs on: a clone of a
// throwaway origin holding deploy-mac.sh, the launchd templates and a tiny
// daemon that prints its version, a launchctl that only notes what it is told,
// and an HTTP stand-in for the daemon's GET /busy.
type macRig struct {
	t                         *testing.T
	root, work, clone, bin    string
	state, launchctlLog, stub string
	env                       []string

	mu   sync.Mutex
	busy string // GET /busy's answer; "" answers 404, like a daemon from before /busy
}

// The stand-in daemon embeds a web UI's dist, with a copy of the docs in it,
// as the real one does.
const (
	fakeMain = `package main

import (
	"fmt"

	"example.com/fakedaemon/internal/webui"
)

var version = "dev"

var _ = webui.FS

func main() { fmt.Println(version, %q) }
`
	fakeWebUI = `package webui

import "embed"

//go:embed all:dist
var FS embed.FS
`
)

func newMacRig(t *testing.T) *macRig {
	t.Helper()
	for _, tool := range []string{"bash", "git", "curl", "go"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found: %v", tool, err)
		}
	}
	root := t.TempDir()
	r := &macRig{
		t:     t,
		root:  root,
		work:  filepath.Join(root, "work"),
		clone: filepath.Join(root, "clone"),
		bin:   filepath.Join(root, "bin", "mcp-midi-controller"),
		state: filepath.Join(root, "state", "mcp-midi-controller"),
		stub:  filepath.Join(root, "stub"),
		busy:  "idle\n",
	}
	r.launchctlLog = filepath.Join(root, "launchctl.log")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		answer := r.busy
		r.mu.Unlock()
		if req.URL.Path != "/busy" || answer == "" {
			http.NotFound(w, req)
			return
		}
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(srv.Close)

	home := filepath.Join(root, "home")
	config := filepath.Join(root, "config", "mcp-midi-controller")
	for _, d := range []string{home, config, filepath.Dir(r.bin), r.stub} {
		mustMkdir(t, d)
	}
	mustWrite(t, filepath.Join(config, "config.yaml"),
		"# the daemon's own config\nlisten_addr: \""+strings.TrimPrefix(srv.URL, "http://")+"\" # loopback\n", 0o644)
	// The script runs on macOS only, and launchctl is macOS's: stand-ins.
	mustWrite(t, filepath.Join(r.stub, "uname"), "#!/bin/sh\necho Darwin\n", 0o755)
	mustWrite(t, filepath.Join(r.stub, "launchctl"), "#!/bin/sh\necho \"$*\" >>"+r.launchctlLog+"\n", 0o755)

	r.env = append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(root, "config"),
		"XDG_STATE_HOME="+filepath.Join(root, "state"),
		"MCP_MIDI_BIN="+r.bin,
		"PATH="+r.stub+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GOFLAGS=", "GOTOOLCHAIN=local", "GOWORK=off",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
	)

	origin := filepath.Join(root, "origin.git")
	r.git(root, "-c", "init.defaultBranch=main", "init", "--quiet", "--bare", origin)
	r.git(root, "-c", "init.defaultBranch=main", "init", "--quiet", r.work)
	script, err := os.ReadFile("deploy-mac.sh")
	if err != nil {
		t.Fatal(err)
	}
	r.write("scripts/deploy-mac.sh", string(script), 0o755)
	for _, plist := range []string{"com.teemow.mcp-midi-controller.plist", "com.teemow.mcp-midi-controller.update.plist"} {
		b, err := os.ReadFile(filepath.Join("..", "init", plist))
		if err != nil {
			t.Fatal(err)
		}
		r.write("init/"+plist, string(b), 0o644)
	}
	r.write("go.mod", "module example.com/fakedaemon\n\ngo 1.22\n", 0o644)
	r.write("cmd/mcp-midi-controller/main.go", fakeMainSource("first"), 0o644)
	r.write("internal/webui/webui.go", fakeWebUI, 0o644)
	r.write("internal/webui/dist/index.html", "<html>first</html>\n", 0o644)
	r.write("internal/webui/dist/docs/manifest.json", `{"docs":[]}`+"\n", 0o644)
	r.write("README.md", "# fake daemon\n", 0o644)
	r.git(r.work, "add", "-A")
	r.git(r.work, "commit", "--quiet", "-m", "first")
	r.git(r.work, "tag", "v0.1.0")
	r.git(r.work, "remote", "add", "origin", origin)
	r.git(r.work, "push", "--quiet", "--tags", "origin", "main")
	r.git(root, "clone", "--quiet", origin, r.clone)
	return r
}

func fakeMainSource(build string) string {
	return strings.Replace(fakeMain, "%q", strconv.Quote(build), 1)
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	mustMkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func (r *macRig) git(dir string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Env = dir, r.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// write puts a file into the work tree merges are made in.
func (r *macRig) write(rel, body string, mode os.FileMode) {
	r.t.Helper()
	mustWrite(r.t, filepath.Join(r.work, rel), body, mode)
}

// merge commits the work tree and pushes it to origin's main, as a merged PR
// does.
func (r *macRig) merge(msg string) {
	r.t.Helper()
	r.git(r.work, "add", "-A")
	r.git(r.work, "commit", "--quiet", "-m", msg)
	r.git(r.work, "push", "--quiet", "origin", "main")
}

// setBusy sets what the daemon's GET /busy answers.
func (r *macRig) setBusy(answer string) {
	r.mu.Lock()
	r.busy = answer
	r.mu.Unlock()
}

// update runs one run of the update agent and returns what it logged.
func (r *macRig) update() string {
	r.t.Helper()
	return r.run("--update")
}

// deployByHand runs scripts/deploy-mac.sh without arguments, as a person on
// the Mac does, and returns what it logged.
func (r *macRig) deployByHand() string {
	r.t.Helper()
	return r.run()
}

// run runs the clone's deploy-mac.sh with args and returns what it logged.
func (r *macRig) run(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("bash", append([]string{filepath.Join(r.clone, "scripts", "deploy-mac.sh")}, args...)...)
	cmd.Dir, cmd.Env = r.clone, r.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("deploy-mac.sh %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// restarts counts the daemon's restarts: its agent loaded or kickstarted.
func (r *macRig) restarts() int {
	r.t.Helper()
	b, err := os.ReadFile(r.launchctlLog)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		r.t.Fatal(err)
	}
	n := 0
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "kickstart ") && strings.HasSuffix(line, "/com.teemow.mcp-midi-controller") ||
			strings.HasPrefix(line, "bootstrap ") && strings.HasSuffix(line, "/com.teemow.mcp-midi-controller.plist") {
			n++
		}
	}
	return n
}

// installed is what the installed binary says it is.
func (r *macRig) installed() string {
	r.t.Helper()
	out, err := exec.Command(r.bin).Output()
	if err != nil {
		r.t.Fatalf("run the installed binary: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func (r *macRig) originVersion() string {
	r.t.Helper()
	return r.git(r.work, "describe", "--tags", "--always", "HEAD")
}

func (r *macRig) wantRestarts(when string, n int) {
	r.t.Helper()
	if got := r.restarts(); got != n {
		r.t.Fatalf("%s: the daemon was restarted %d time(s), want %d", when, got, n)
	}
}

func wantLogged(t *testing.T, log string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(log, p) {
			t.Fatalf("the update did not log %q; it logged:\n%s", p, log)
		}
	}
}

// TestMacUpdateRestartsOnlyForANewDaemon checks a merge that leaves the binary
// as it was (docs, tests, web sources) does not restart the daemon, and one
// that changes it does.
func TestMacUpdateRestartsOnlyForANewDaemon(t *testing.T) {
	r := newMacRig(t)

	log := r.update()
	r.wantRestarts("the first update", 1)
	wantLogged(t, log, "building v0.1.0", "deployed v0.1.0")
	if got := r.installed(); got != "v0.1.0 first" {
		t.Fatalf("installed %q, want v0.1.0 first", got)
	}
	if log := r.update(); log != "" {
		t.Fatalf("an update with main where it was logged:\n%s", log)
	}

	// A docs-only merge, its copy in the web UI's bundle included:
	// fast-forwarded, the same daemon, no restart.
	r.write("README.md", "# fake daemon\n\nNow with docs.\n", 0o644)
	r.write("docs/notes.md", "notes\n", 0o644)
	r.write("internal/webui/dist/docs/notes.md", "notes\n", 0o644)
	r.write("internal/webui/dist/docs/manifest.json", `{"docs":["notes.md"]}`+"\n", 0o644)
	r.merge("docs: notes")
	docs := r.originVersion()
	log = r.update()
	r.wantRestarts("a docs-only merge", 1)
	wantLogged(t, log, "fast-forwarded to", docs+" builds the same daemon as the running v0.1.0: not restarting")
	if strings.Contains(log, "building") {
		t.Fatalf("a docs-only merge built the daemon:\n%s", log)
	}
	if got := r.installed(); got != "v0.1.0 first" {
		t.Fatalf("after a docs-only merge the installed binary is %q, want it left as v0.1.0 first", got)
	}
	if got := r.git(r.clone, "rev-parse", "HEAD"); got != r.git(r.work, "rev-parse", "HEAD") {
		t.Fatalf("the clone was not fast-forwarded to main")
	}
	if log := r.update(); log != "" {
		t.Fatalf("a second update after the docs-only merge logged:\n%s", log)
	}

	// A merge that changes the daemon: built and restarted.
	r.write("cmd/mcp-midi-controller/main.go", fakeMainSource("second"), 0o644)
	r.merge("feat: second")
	second := r.originVersion()
	log = r.update()
	r.wantRestarts("a merge that changes the daemon", 2)
	wantLogged(t, log, "building "+second, "deployed "+second)
	if got := r.installed(); got != second+" second" {
		t.Fatalf("installed %q, want %s second", got, second)
	}

	// A rebuilt web UI is a new daemon.
	r.write("internal/webui/dist/index.html", "<html>second</html>\n", 0o644)
	r.merge("feat: a new web UI")
	log = r.update()
	r.wantRestarts("a merge that changes the web UI", 3)
	wantLogged(t, log, "deployed "+r.originVersion())

	// A change to the daemon's launchd agent restarts it too, through a reload.
	r.write("init/com.teemow.mcp-midi-controller.plist", "<!-- changed -->\n", 0o644)
	r.merge("fix: the daemon's agent")
	log = r.update()
	r.wantRestarts("a merge that changes the daemon's agent", 4)
	wantLogged(t, log, "loaded com.teemow.mcp-midi-controller")
}

// TestMacUpdateWaitsWhileTheDaemonIsBusy checks the update builds nothing and
// restarts nothing while the daemon says it is busy, logs what it waits for,
// goes ahead once it is idle, and waits no longer than its limit.
func TestMacUpdateWaitsWhileTheDaemonIsBusy(t *testing.T) {
	r := newMacRig(t)
	r.update()
	r.wantRestarts("the first update", 1)

	r.write("cmd/mcp-midi-controller/main.go", fakeMainSource("second"), 0o644)
	r.merge("feat: second")
	second := r.originVersion()
	stems := `record_song_stems "battalion", for 2m0s (since 04:17:03)`
	r.setBusy("busy\n" + stems + "\n" + `tool call get_song_stems, for 0s (since 04:19:03)` + "\n")
	for i := 0; i < 2; i++ {
		log := r.update()
		r.wantRestarts("an update while the daemon is busy", 1)
		wantLogged(t, log, "update to "+second+" waits: the daemon is busy", "busy: "+stems, "busy: tool call get_song_stems")
		if strings.Contains(log, "building") {
			t.Fatalf("the update built while the daemon is busy:\n%s", log)
		}
	}
	if got := r.installed(); got != "v0.1.0 first" {
		t.Fatalf("while busy the installed binary became %q", got)
	}

	r.setBusy("idle\n")
	log := r.update()
	r.wantRestarts("the update once the daemon is idle", 2)
	wantLogged(t, log, "deployed "+second)
	if _, err := os.Stat(filepath.Join(r.state, "waiting-since")); !os.IsNotExist(err) {
		t.Fatalf("the wait is still noted after the deploy: %v", err)
	}

	// A daemon busy for longer than the update waits: it goes ahead.
	r.write("cmd/mcp-midi-controller/main.go", fakeMainSource("third"), 0o644)
	r.merge("feat: third")
	third := r.originVersion()
	r.setBusy("busy\n" + stems + "\n")
	r.update()
	r.wantRestarts("the first wait for a busy daemon", 2)
	hourAgo := time.Now().Add(-61 * time.Minute).Unix()
	mustWrite(t, filepath.Join(r.state, "waiting-since"), strconv.FormatInt(hourAgo, 10)+"\n", 0o644)
	log = r.update()
	r.wantRestarts("the update after an hour's wait", 3)
	wantLogged(t, log, "update to "+third+" goes ahead although the daemon is busy: it has waited 61 min", "deployed "+third)
	if got := r.installed(); got != third+" third" {
		t.Fatalf("installed %q, want %s third", got, third)
	}

	// A daemon that does not know /busy (or does not answer) has nothing to
	// wait for.
	r.write("cmd/mcp-midi-controller/main.go", fakeMainSource("fourth"), 0o644)
	r.merge("feat: fourth")
	r.setBusy("")
	log = r.update()
	r.wantRestarts("an update with a daemon that does not answer /busy", 4)
	wantLogged(t, log, "deployed "+r.originVersion())
}

// TestMacDeployByHandEndsTheWait checks a deploy by hand while the update
// waits for a busy daemon ends that wait: the next update that finds the
// daemon busy waits its full time again, rather than counting from the old
// start and restarting at once.
func TestMacDeployByHandEndsTheWait(t *testing.T) {
	r := newMacRig(t)
	r.update()
	r.wantRestarts("the first update", 1)

	r.write("cmd/mcp-midi-controller/main.go", fakeMainSource("second"), 0o644)
	r.merge("feat: second")
	second := r.originVersion()
	playing := `play_song "battalion", for 1m0s (since 04:17:03)`
	r.setBusy("busy\n" + playing + "\n")
	wantLogged(t, r.update(), "update to "+second+" waits: the daemon is busy")
	r.wantRestarts("an update while the daemon is busy", 1)

	// The wait began over an hour before the next merge comes in; the person
	// deploys by hand in between.
	waiting := filepath.Join(r.state, "waiting-since")
	hourAgo := time.Now().Add(-61 * time.Minute).Unix()
	mustWrite(t, waiting, strconv.FormatInt(hourAgo, 10)+"\n", 0o644)
	log := r.deployByHand()
	r.wantRestarts("the deploy by hand", 2)
	wantLogged(t, log, "restarting although the daemon is busy:", "busy: "+playing, "deployed "+second)
	if _, err := os.Stat(waiting); !os.IsNotExist(err) {
		t.Fatalf("the wait is still noted after the deploy by hand: %v", err)
	}

	r.write("cmd/mcp-midi-controller/main.go", fakeMainSource("third"), 0o644)
	r.merge("feat: third")
	third := r.originVersion()
	log = r.update()
	r.wantRestarts("an update while the daemon is busy, after the deploy by hand", 2)
	wantLogged(t, log, "update to "+third+" waits: the daemon is busy (waited 0 of")
	if got := r.installed(); got != second+" second" {
		t.Fatalf("while busy the installed binary became %q, want %s second", got, second)
	}
}
