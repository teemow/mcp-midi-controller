package mcpserver

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/teemow/midi-transport/midicontrol"

	"github.com/teemow/mcp-midi-controller/internal/song"
)

// getBusy reads GET /busy from the daemon's loopback handler.
func getBusy(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	resp, err := http.Get(srv.URL + "/busy")
	if err != nil {
		t.Fatalf("GET /busy: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /busy: status %d: %s", resp.StatusCode, body)
	}
	return string(body)
}

// waitBusy waits until GET /busy answers what match accepts.
func waitBusy(t *testing.T, srv *httptest.Server, desc string, match func(string) bool) {
	t.Helper()
	var got string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if got = getBusy(t, srv); match(got) {
			return
		}
	}
	t.Fatalf("GET /busy never answered %s; last answer:\n%s", desc, got)
}

func idle(got string) bool { return got == "idle\n" }

// TestBusyWhileAToolCallRuns checks a tool call counts from when it arrives
// until it has returned.
func TestBusyWhileAToolCallRuns(t *testing.T) {
	s := newSongServer(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	release := make(chan struct{})
	mcp.AddTool(s.mcp, &mcp.Tool{Name: "test_block"}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return textResult("done", false), nil, nil
	})
	cs := connectClient(t, s)

	if got := getBusy(t, srv); !idle(got) {
		t.Fatalf("before any call GET /busy answered %q, want idle", got)
	}
	returned := make(chan error, 1)
	go func() {
		_, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "test_block", Arguments: map[string]any{}})
		returned <- err
	}()
	waitBusy(t, srv, "busy with the call", func(got string) bool {
		return strings.HasPrefix(got, "busy\n") && strings.Contains(got, "tool call test_block, for ")
	})
	close(release)
	if err := <-returned; err != nil {
		t.Fatalf("call: %v", err)
	}
	waitBusy(t, srv, "idle once the call returned", idle)
}

// TestBusyWhileASongPlays checks play_song counts for as long as the song
// plays, past its tool call, and not after stop_song.
func TestBusyWhileASongPlays(t *testing.T) {
	hub := midicontrol.NewHub()
	mux := http.NewServeMux()
	midicontrol.Register(mux, hub, midicontrol.Callbacks{})
	brain := httptest.NewServer(mux)
	defer brain.Close()
	s := newSongServer(t, WithMidiControl(hub))
	t.Cleanup(func() { s.player.Stop() })
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": toolSong}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	frames, _ := dialBrain(t, brain, hub)
	go func() {
		for range frames {
		}
	}()

	if res := call(t, s.handlePlaySong, map[string]any{"song": "night_drive", "preroll_ms": 2000}); res.IsError {
		t.Fatalf("play: %s", resultText(res))
	}
	if got := getBusy(t, srv); !strings.HasPrefix(got, "busy\n") || !strings.Contains(got, `play_song "night_drive", for `) {
		t.Fatalf("while the song plays GET /busy answered:\n%s", got)
	}
	if res := call(t, s.handleStopSong, map[string]any{}); res.IsError {
		t.Fatalf("stop: %s", resultText(res))
	}
	waitBusy(t, srv, "idle after stop_song", idle)
}

// TestHoldMarksBusyAndKeepsTheHostAwake checks hold takes both and gives both
// back.
func TestHoldMarksBusyAndKeepsTheHostAwake(t *testing.T) {
	host := &fakeHost{status: func() song.Status { return song.Status{} }}
	s := newSongServer(t, WithHostAwake(host.hold))
	release := s.hold(`record_song_stems "battalion"`)
	if held, released := host.counts(); held != 1 || released != 0 {
		t.Fatalf("hold: %d hold(s) taken and %d released, want 1 and 0", held, released)
	}
	if got := s.busy.Report(); !strings.Contains(got, `record_song_stems "battalion", for `) {
		t.Fatalf("hold: busy reports %q", got)
	}
	release()
	if held, released := host.counts(); held != 1 || released != 1 {
		t.Fatalf("release: %d hold(s) taken and %d released, want 1 and 1", held, released)
	}
	if got := s.busy.Report(); !idle(got) {
		t.Fatalf("release: busy reports %q, want idle", got)
	}
}
