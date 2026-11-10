package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/mewkiz/flac"
	"github.com/mewkiz/flac/meta"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teemow/mcp-midi-controller/internal/audiotap"
	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/mcp-midi-controller/internal/drive"
	"github.com/teemow/mcp-midi-controller/internal/drive/drivetest"
	"github.com/teemow/mcp-midi-controller/internal/recording"
	"github.com/teemow/midi-transport/midicontrol"
)

const feedRate = 48000

// tapFeed streams a stereo tone into a tap the way a ProbeAudioTap does: a
// batch every few milliseconds, as much as the clock says is due. level is
// the tone's amplitude; zero is silence.
type tapFeed struct {
	store *audiotap.Store
	level atomic.Uint64 // math.Float64bits
	ticks atomic.Int64
	stop  chan struct{}
	done  chan struct{}
}

// set changes the tone's level and returns once the stream carries it: a
// batch computed before the change may still be on its way into the tap.
func (f *tapFeed) set(level float64) {
	f.level.Store(math.Float64bits(level))
	for target := f.ticks.Load() + 2; f.ticks.Load() < target; time.Sleep(time.Millisecond) {
		select {
		case <-f.done:
			return
		default:
		}
	}
}

// startTapFeed connects a tap called name and streams silence into it until
// the test ends.
func startTapFeed(t *testing.T, reg *audiotap.Registry, name string) *tapFeed {
	t.Helper()
	store := reg.Connect(name, "test")
	store.SetFormat(audiotap.Format{Encoding: "f32le", Channels: 2, SampleRate: feedRate})
	f := &tapFeed{store: store, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(f.done)
		tick := time.NewTicker(4 * time.Millisecond)
		defer tick.Stop()
		start, sent := time.Now(), 0
		for {
			select {
			case <-f.stop:
				return
			case <-tick.C:
			}
			if due := int(time.Since(start).Seconds()*feedRate) - sent; due > 0 {
				level := math.Float64frombits(f.level.Load())
				batch := make([]float32, 2*due)
				for i := range due {
					v := float32(level * math.Sin(2*math.Pi*440*float64(sent+i)/feedRate))
					batch[2*i], batch[2*i+1] = v, v
				}
				store.AppendAudio(batch)
				sent += due
			}
			f.ticks.Add(1)
		}
	}()
	t.Cleanup(func() { close(f.stop); <-f.done })
	return f
}

// newRecordingServer is a song server with a streaming tap called "master"
// and a recorder that does not dwell on a tail.
func newRecordingServer(t *testing.T, opts ...Option) (*Server, *tapFeed) {
	t.Helper()
	reg := audiotap.NewRegistry()
	s := newSongServer(t, append([]Option{WithAudioTap(reg)}, opts...)...)
	s.rec.quietFor, s.rec.maxTail, s.rec.poll = 150*time.Millisecond, 2*time.Second, 5*time.Millisecond
	return s, startTapFeed(t, reg, "master")
}

// lastRecording waits for the running recording to finish and returns how it
// ended.
func lastRecording(t *testing.T, s *Server) *recordingResult {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		s.rec.mu.Lock()
		cur, last := s.rec.current, s.rec.last
		s.rec.mu.Unlock()
		if cur == nil && last != nil && last.Upload != "uploading" {
			return last
		}
	}
	t.Fatal("the recording did not finish")
	return nil
}

// readFLAC decodes a recording: its tags, its frame count and its largest
// sample as a fraction of full scale.
func readFLAC(t *testing.T, path string) (tags map[string]string, frames int, peak float64) {
	t.Helper()
	stream, err := flac.ParseFile(path)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	defer func() { _ = stream.Close() }()
	tags = map[string]string{}
	for _, b := range stream.Blocks {
		if vc, ok := b.Body.(*meta.VorbisComment); ok {
			for _, tag := range vc.Tags {
				tags[tag[0]] = tag[1]
			}
		}
	}
	for {
		f, err := stream.ParseNext()
		if err != nil {
			break
		}
		frames += f.Subframes[0].NSamples
		for _, sub := range f.Subframes {
			for _, v := range sub.Samples[:sub.NSamples] {
				peak = math.Max(peak, math.Abs(float64(v))/(1<<23))
			}
		}
	}
	return tags, frames, peak
}

// TestStartAndStopRecording records a tap on its own: silence, a tone, silence
// again, stopped by hand. The file holds the tone and little else.
func TestStartAndStopRecording(t *testing.T) {
	s, feed := newRecordingServer(t)

	res := call(t, s.handleStartRecording, map[string]any{"name": "Jam: one"})
	if res.IsError || !strings.Contains(resultText(res), `from the tap "master" until stop_recording`) {
		t.Fatalf("start: %s", resultText(res))
	}
	if res := call(t, s.handleStartRecording, map[string]any{}); !res.IsError || !strings.Contains(resultText(res), "still running") {
		t.Fatalf("a second recording was started: %s", resultText(res))
	}
	time.Sleep(150 * time.Millisecond)
	feed.set(0.5)
	time.Sleep(400 * time.Millisecond)
	if res := call(t, s.handleListRecordings, map[string]any{}); !strings.Contains(resultText(res), `running: "Jam one `) || !strings.Contains(resultText(res), "so far (recording)") {
		t.Errorf("list while recording: %s", resultText(res))
	}
	feed.set(0)
	time.Sleep(300 * time.Millisecond)

	res = call(t, s.handleStopRecording, map[string]any{})
	if res.IsError || !strings.HasPrefix(resultText(res), `recorded "Jam one `) {
		t.Fatalf("stop: %s", resultText(res))
	}
	out, ok := res.StructuredContent.(*recordingResult)
	if !ok || out.Error != "" {
		t.Fatalf("stop returned %T %+v", res.StructuredContent, res.StructuredContent)
	}
	m := out.Meta
	if m.Format != recording.FLAC || m.Channels != 2 || m.SampleRate != feedRate || m.Tap != "master" || m.Song != "" || m.Title != "Jam: one" {
		t.Errorf("record = %+v", m)
	}
	// 0.4 s of tone, 20 ms before it and what silence followed (0.3 s, under
	// the second that would be kept): about 0.72 s, after about 130 ms of
	// lead. The bounds are wide, for a machine that sleeps longer than asked.
	if m.Seconds < 0.6 || m.Seconds > 5 || m.TrimmedLeadMS < 100 || m.TrimmedLeadMS > 3000 || m.Silent {
		t.Errorf("cut: %.3f s kept, %d ms of lead cut; want about 0.72 s and 130 ms", m.Seconds, m.TrimmedLeadMS)
	}
	if math.Abs(m.PeakDBFS+6.02) > 0.05 {
		t.Errorf("peak = %.2f dBFS, want -6.02 (a tone of 0.5)", m.PeakDBFS)
	}
	if filepath.Dir(out.Path) != config.RecordingsDir() || !strings.HasSuffix(out.Path, ".flac") {
		t.Errorf("path = %s, want a .flac under %s", out.Path, config.RecordingsDir())
	}
	tags, frames, peak := readFLAC(t, out.Path)
	if tags["TITLE"] != "Jam: one" || float64(frames)/feedRate != m.Seconds || math.Abs(peak-0.5) > 0.001 {
		t.Errorf("file: tags %v, %d frames, peak %.4f; the record says %.4f s", tags, frames, peak, m.Seconds)
	}
	if entries, _ := os.ReadDir(config.RecordingsDir()); len(entries) != 2 {
		t.Errorf("%d files in the recordings dir, want the audio and its record", len(entries))
	}

	// Nothing runs now: stopping reports the last one, listing shows it.
	if res := call(t, s.handleStopRecording, map[string]any{}); res.IsError || !strings.Contains(resultText(res), "no recording is running; the last one") {
		t.Errorf("stop with nothing running: %s", resultText(res))
	}
	res = call(t, s.handleListRecordings, map[string]any{})
	if text := resultText(res); !strings.Contains(text, "1 recording(s) in "+config.RecordingsDir()) || !strings.Contains(text, "peak -6.0") || strings.Contains(text, "running:") {
		t.Errorf("list: %s", text)
	}
}

// TestTimedRecording: a recording of a given length is exactly that long, in
// either format, whatever the tap played.
func TestTimedRecording(t *testing.T) {
	s, feed := newRecordingServer(t)
	feed.set(0.25)
	res := call(t, s.handleStartRecording, map[string]any{"seconds": 0.2, "format": "wav", "trim": false})
	if res.IsError || !strings.Contains(resultText(res), "for 0.2 s") {
		t.Fatalf("start: %s", resultText(res))
	}
	last := lastRecording(t, s)
	if last.Error != "" || last.Meta.Seconds != 0.2 || last.Meta.Format != recording.WAV || last.Meta.BitDepth != 32 || last.Meta.Ended != "" {
		t.Fatalf("timed recording: %+v", last)
	}
	if info, err := os.Stat(last.Path); err != nil || info.Size() != 44+int64(0.2*feedRate)*2*4 {
		t.Errorf("the WAV is %v bytes (%v), want a header and 9600 stereo float frames", info.Size(), err)
	}
	// Trimmed, a recording of nothing but tone keeps its length and is faded
	// out, since it was cut off sounding.
	if res := call(t, s.handleStartRecording, map[string]any{"seconds": 0.5}); res.IsError {
		t.Fatalf("start: %s", resultText(res))
	}
	last = lastRecording(t, s)
	if last.Error != "" || last.Meta.Seconds != 0.5 || last.Meta.FadeOutMS != 400 || last.Meta.TrimmedLeadMS != 0 {
		t.Errorf("a recording of tone throughout: %+v", last.Meta)
	}
	// Silence throughout is kept whole and said to be silent.
	feed.set(0)
	time.Sleep(50 * time.Millisecond)
	if res := call(t, s.handleStartRecording, map[string]any{"seconds": 0.1}); res.IsError {
		t.Fatalf("start: %s", resultText(res))
	}
	if last = lastRecording(t, s); !last.Meta.Silent || last.Meta.Seconds != 0.1 || !strings.Contains(describeResult(last), "silent throughout") {
		t.Errorf("a silent recording: %s", describeResult(last))
	}
}

// TestRecordingEndsWhenItsTapIsGone: a recording without a length whose tap
// stops streaming never reaches its length by the audio, so it ends by the
// clock, keeps what arrived, and lets the host sleep again.
func TestRecordingEndsWhenItsTapIsGone(t *testing.T) {
	var released atomic.Int32
	reg := audiotap.NewRegistry()
	s := newSongServer(t, WithAudioTap(reg), WithHostAwake(func() func() {
		return func() { released.Add(1) }
	}))
	s.rec.poll, s.rec.maxRun = 5*time.Millisecond, 200*time.Millisecond
	tap := reg.Connect("master", "test")
	tap.SetFormat(audiotap.Format{Encoding: "f32le", Channels: 2, SampleRate: feedRate})

	if res := call(t, s.handleStartRecording, map[string]any{}); res.IsError {
		t.Fatalf("start: %s", resultText(res))
	}
	tone := make([]float32, 2*feedRate/10)
	for i := range tone {
		tone[i] = 0.5
	}
	tap.AppendAudio(tone)
	reg.Disconnect("master")

	last := lastRecording(t, s)
	if last.Error != "" || last.Meta.Seconds != 0.1 {
		t.Fatalf("a recording whose tap went away: %+v", last)
	}
	// The hold is given back as finishRecording returns, just after the result.
	for deadline := time.Now().Add(5 * time.Second); released.Load() == 0 && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	if released.Load() != 1 {
		t.Errorf("the host hold was released %d time(s), want once", released.Load())
	}
}

func TestRecordingRefusals(t *testing.T) {
	s, _ := newRecordingServer(t)
	for name, tc := range map[string]struct {
		args map[string]any
		want string
	}{
		"an unknown tap":    {map[string]any{"tap": "drums"}, `no audio tap named "drums"; known taps: master`},
		"an unknown format": {map[string]any{"format": "mp3"}, `unknown format "mp3"`},
		"too long":          {map[string]any{"seconds": 4000}, "up to 1800 seconds"},
		"a level over full": {map[string]any{"silence_db": 3}, "between -120 and 0"},
	} {
		if res := call(t, s.handleStartRecording, tc.args); !res.IsError || !strings.Contains(resultText(res), tc.want) {
			t.Errorf("%s: %s", name, resultText(res))
		}
	}
	if res := call(t, s.handleStopRecording, map[string]any{}); res.IsError || !strings.Contains(resultText(res), "none was made") {
		t.Errorf("stop before any recording: %s", resultText(res))
	}
	if res := call(t, s.handleListRecordings, map[string]any{}); res.IsError || !strings.Contains(resultText(res), "no recordings in") {
		t.Errorf("list before any recording: %s", resultText(res))
	}
	if entries, _ := os.ReadDir(config.RecordingsDir()); len(entries) != 0 {
		t.Errorf("refused recordings left %d file(s) behind", len(entries))
	}

	// No tap at all, and a tap that has dropped.
	bare := newSongServer(t, WithAudioTap(audiotap.NewRegistry()))
	if res := call(t, bare.handleStartRecording, map[string]any{}); !res.IsError || !strings.Contains(resultText(res), "no audio tap has connected yet") {
		t.Errorf("recording without a tap: %s", resultText(res))
	}
	reg := audiotap.NewRegistry()
	reg.Connect("master", "test").SetFormat(audiotap.Format{Channels: 2, SampleRate: feedRate})
	reg.Disconnect("master")
	gone := newSongServer(t, WithAudioTap(reg))
	if res := call(t, gone.handleStartRecording, map[string]any{}); !res.IsError || !strings.Contains(resultText(res), "not streaming") {
		t.Errorf("recording a tap that dropped: %s", resultText(res))
	}
}

// brain stands in for the ProbeMidiBrain: it dials the daemon's hub and hands
// every command it is sent to on.
func brain(t *testing.T, hub *midicontrol.Hub, on func(midicontrol.Command)) {
	t.Helper()
	mux := http.NewServeMux()
	midicontrol.Register(mux, hub, midicontrol.Callbacks{})
	srv := httptest.NewServer(mux)
	ctx, cancel := context.WithCancel(context.Background())
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/midi-control", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline) && !hub.Connected(); {
		time.Sleep(5 * time.Millisecond)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			var cmd midicontrol.Command
			if json.Unmarshal(data, &cmd) == nil {
				on(cmd)
			}
		}
	}()
	t.Cleanup(func() { cancel(); <-done; _ = c.CloseNow(); srv.Close() })
}

// TestPlaySongRecords plays a song with record: the recording starts before
// the opening sound, follows the song to its end and its tail, and is cut at
// the first note — whatever sounded while the presets loaded is not in it.
func TestPlaySongRecords(t *testing.T) {
	hub := midicontrol.NewHub()
	s, feed := newRecordingServer(t, WithMidiControl(hub))
	// The stand-in session: while the presets load it makes a noise louder
	// than any note (from the first preset to the first control change that
	// follows them), then it is silent, and from the first note on it sounds.
	playing := false
	brain(t, hub, func(cmd midicontrol.Command) {
		switch {
		case cmd.Type == "noteOn" && !playing:
			playing = true
			feed.set(0.5)
		case cmd.Type == "pc" && !playing:
			feed.set(0.9)
		case cmd.Type == "cc" && !playing:
			feed.set(0)
		}
	})
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": toolSong}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	if res := call(t, s.handleAuthorSongSession, map[string]any{"song": "night_drive"}); res.IsError {
		t.Fatalf("author: %s", resultText(res))
	}

	res := call(t, s.handlePlaySong, map[string]any{"song": "night_drive", "preroll_ms": 300, "record": true})
	if res.IsError || !strings.Contains(resultText(res), `recording it as "Night Drive `) || !strings.Contains(resultText(res), `from the tap "master"`) {
		t.Fatalf("play: %s", resultText(res))
	}
	// A second recording cannot start while this one runs, and a song asked to
	// be recorded then does not start either.
	if res := call(t, s.handlePlaySong, map[string]any{"song": "night_drive", "record": true}); !res.IsError || !strings.Contains(resultText(res), "cannot record") {
		t.Fatalf("a second recorded playback started: %s", resultText(res))
	}
	if st := s.player.Status(); !st.Playing {
		t.Fatal("the refused playback stopped the song that was being recorded")
	}
	// Once the song has ended, the recording waits for the tail.
	var listed string
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if listed = resultText(call(t, s.handleListRecordings, map[string]any{})); strings.Contains(listed, "waiting for its tail to ring out") {
			break
		}
	}
	if s.player.Status().Playing || !strings.Contains(listed, "waiting for its tail to ring out") {
		t.Errorf("list after the song ended: %s", listed)
	}
	// The tail rings for a moment, then the session is silent.
	time.Sleep(200 * time.Millisecond)
	feed.set(0)

	last := lastRecording(t, s)
	if last.Error != "" {
		t.Fatalf("the recording failed: %s", last.Error)
	}
	m := last.Meta
	if m.Song != "night_drive" || m.Title != "Night Drive" || !strings.HasPrefix(m.Name, "Night Drive 20") || m.Tap != "master" {
		t.Errorf("record = %+v", m)
	}
	// Two bars at 300 BPM are 1.6 s; then 0.2 s of tail and the silence kept
	// after it (the recorder waits 150 ms for quiet in this test): about 2 s.
	if m.Seconds < 1.75 || m.Seconds > 6 {
		t.Errorf("the recording is %.3f s long, want the 1.6 s song and its tail", m.Seconds)
	}
	// The lead that was cut holds the preroll (300 ms) and the noise in it.
	if m.TrimmedLeadMS < 250 {
		t.Errorf("only %d ms of lead were cut, want the whole preroll", m.TrimmedLeadMS)
	}
	tags, _, peak := readFLAC(t, last.Path)
	if math.Abs(peak-0.5) > 0.001 {
		t.Errorf("the file peaks at %.3f: the noise of the loading presets (0.9) is in it", peak)
	}
	if tags["TITLE"] != "Night Drive" {
		t.Errorf("tags = %v", tags)
	}

	// A song that cannot start leaves no recording behind.
	if res := call(t, s.handlePlaySong, map[string]any{"song": "night_drive", "tracks": []string{"nope"}, "record": true}); !res.IsError {
		t.Fatalf("a playback of an unknown track started: %s", resultText(res))
	}
	s.rec.mu.Lock()
	cur := s.rec.current
	s.rec.mu.Unlock()
	if entries, _ := os.ReadDir(config.RecordingsDir()); cur != nil || len(entries) != 2 {
		t.Errorf("after a playback that did not start: recording %v, %d files; want none running and the one recording", cur, len(entries))
	}
}

// TestStoppedSongEndsItsRecording: stop_song ends the song, and the
// recording with it; stop_recording ends a recording and leaves the song.
func TestStoppedSongEndsItsRecording(t *testing.T) {
	hub := midicontrol.NewHub()
	s, feed := newRecordingServer(t, WithMidiControl(hub))
	brain(t, hub, func(cmd midicontrol.Command) {
		switch cmd.Type {
		case "noteOn":
			feed.set(0.5)
		}
	})
	slow := strings.Replace(toolSong, "tempo: 300", "tempo: 30", 1) // 16 s
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": slow}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	play := func() {
		t.Helper()
		feed.set(0)
		if res := call(t, s.handlePlaySong, map[string]any{"song": "night_drive", "preroll_ms": 20, "record": true}); res.IsError {
			t.Fatalf("play: %s", resultText(res))
		}
		for feed.level.Load() == 0 {
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(200 * time.Millisecond)
	}

	play()
	if res := call(t, s.handleStopSong, map[string]any{}); res.IsError {
		t.Fatalf("stop_song: %s", resultText(res))
	}
	feed.set(0)
	if last := lastRecording(t, s); last.Error != "" || last.Meta.Seconds < 0.15 || last.Meta.Seconds > 10 {
		t.Errorf("the recording of a stopped song: %+v", last)
	}

	play()
	res := call(t, s.handleStopRecording, map[string]any{})
	if res.IsError || !strings.HasPrefix(resultText(res), "recorded ") {
		t.Fatalf("stop_recording: %s", resultText(res))
	}
	if !s.player.Status().Playing {
		t.Error("stop_recording stopped the song")
	}
	call(t, s.handleStopSong, map[string]any{})
}

// TestRecordingsAreCapped: recordings_keep bounds how many stay.
func TestRecordingsAreCapped(t *testing.T) {
	s, feed := newRecordingServer(t)
	writeConfig(t, "recordings_keep: 2\n")
	feed.set(0.5)
	var names []string
	for range 3 {
		if res := call(t, s.handleStartRecording, map[string]any{"seconds": 0.05}); res.IsError {
			t.Fatalf("start: %s", resultText(res))
		}
		names = append(names, lastRecording(t, s).Meta.Name)
		// Recordings sort by the second they were started in.
		time.Sleep(1100 * time.Millisecond)
	}
	all, err := s.recordings().List()
	if err != nil || len(all) != 2 || all[0].Name != names[2] || all[1].Name != names[1] {
		t.Errorf("kept %+v (%v), want the two newest of %v", all, err, names)
	}
}

// writeConfig writes the daemon's config.yaml in the test's config dir.
func writeConfig(t *testing.T, yaml string) {
	t.Helper()
	if err := os.MkdirAll(config.ConfigDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config.ConfigDir(), "config.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
}

// --- Google Drive ---------------------------------------------------------------

// driveRig is a recording server whose Drive client talks to a stand-in.
type driveRig struct {
	t    *testing.T
	s    *Server
	feed *tapFeed
	fake *drivetest.Server
	// said collects every tool answer, text and structured, and log the
	// daemon's log: neither may ever hold a secret.
	said strings.Builder
	log  syncBuffer
}

// syncBuffer is a buffer the daemon's goroutines log into while the test
// reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// logged waits for a line holding want to appear in the daemon's log.
func (r *driveRig) logged(want string) bool {
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if strings.Contains(r.log.String(), want) {
			return true
		}
	}
	return false
}

func newDriveRig(t *testing.T) *driveRig {
	t.Helper()
	r := &driveRig{t: t, fake: drivetest.New(t)}
	r.s, r.feed = newRecordingServer(t, WithGoogleDriveOptions(
		drive.WithEndpoints(drive.EndpointsAt(r.fake.URL)),
		drive.WithChunkSize(256<<10),
		drive.WithBackoff(func(int) time.Duration { return 0 }),
		drive.WithPollInterval(2*time.Millisecond),
	))
	log.SetOutput(&r.log)
	t.Cleanup(func() {
		log.SetOutput(os.Stderr)
		if leaks := r.fake.Leaks(); len(leaks) > 0 {
			t.Errorf("secrets went where they do not belong: %v", leaks)
		}
		for _, secret := range r.fake.Secrets() {
			if strings.Contains(r.said.String(), secret) {
				t.Errorf("a tool's answer carries the secret %q", secret)
			}
			if strings.Contains(r.log.String(), secret) {
				t.Errorf("the log carries the secret %q", secret)
			}
		}
	})
	return r
}

// call runs a tool and keeps what it said.
func (r *driveRig) call(h mcp.ToolHandler, args map[string]any) *mcp.CallToolResult {
	r.t.Helper()
	res := call(r.t, h, args)
	structured, _ := json.Marshal(res.StructuredContent)
	r.said.WriteString(resultText(res) + "\n" + string(structured) + "\n")
	return res
}

// signIn connects the daemon, with the user approving.
func (r *driveRig) signIn() {
	r.t.Helper()
	drivetest.WriteClientFile(r.t, config.GoogleDriveClientPath())
	if res := r.call(r.s.handleConnectGoogleDrive, map[string]any{}); res.IsError {
		r.t.Fatalf("connect: %s", resultText(res))
	}
	for _, code := range r.fake.UserCodes() {
		r.fake.Approve(code)
	}
	r.waitFor("signed in as Test User")
}

// waitFor polls get_google_drive until its answer contains want.
func (r *driveRig) waitFor(want string) string {
	r.t.Helper()
	var text string
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if text = resultText(r.call(r.s.handleGetGoogleDrive, map[string]any{})); strings.Contains(text, want) {
			return text
		}
	}
	r.t.Fatalf("get_google_drive never said %q; last:\n%s", want, text)
	return ""
}

// record makes a short recording of tone and returns it.
func (r *driveRig) record(args map[string]any) *recordingResult {
	r.t.Helper()
	r.feed.set(0.5)
	if args["seconds"] == nil {
		args["seconds"] = 0.1
	}
	if res := r.call(r.s.handleStartRecording, args); res.IsError {
		r.t.Fatalf("start: %s", resultText(res))
	}
	last := lastRecording(r.t, r.s)
	if last.Error != "" {
		r.t.Fatalf("recording: %s", last.Error)
	}
	return last
}

// uploads returns the files (not folders) in the stand-in Drive.
func (r *driveRig) uploads() []drivetest.File {
	var out []drivetest.File
	for _, f := range r.fake.Files() {
		if !f.IsFolder() {
			out = append(out, f)
		}
	}
	return out
}

// TestGoogleDriveSignIn walks the sign-in as the tools show it: not set up,
// not signed in, waiting for the user, signed in, signed out.
func TestGoogleDriveSignIn(t *testing.T) {
	r := newDriveRig(t)
	clientFile := config.GoogleDriveClientPath()

	// No OAuth client yet: every tool says how to get one.
	for name, res := range map[string]*mcp.CallToolResult{
		"get":     r.call(r.s.handleGetGoogleDrive, map[string]any{}),
		"connect": r.call(r.s.handleConnectGoogleDrive, map[string]any{}),
	} {
		text := resultText(res)
		if !strings.Contains(text, clientFile) || !strings.Contains(text, "TVs and Limited Input devices") || !strings.Contains(text, "scripts/setup-google-drive.sh") {
			t.Errorf("%s without a client file: %s", name, text)
		}
		if (name == "connect") != res.IsError {
			t.Errorf("%s without a client file: IsError = %v", name, res.IsError)
		}
	}

	drivetest.WriteClientFile(t, clientFile)
	res := r.call(r.s.handleGetGoogleDrive, map[string]any{})
	if text := resultText(res); !strings.Contains(text, "not signed in (connect_google_drive)") || !strings.Contains(text, "the Drive's top level") || !strings.Contains(text, "when upload_recording is called") {
		t.Fatalf("get before the sign-in: %s", text)
	}
	if st := res.StructuredContent.(driveStatus); !st.Client || st.Connected || st.Pending != nil || st.ClientFile != clientFile {
		t.Errorf("status before the sign-in: %+v", st)
	}

	res = r.call(r.s.handleConnectGoogleDrive, map[string]any{})
	codes := r.fake.UserCodes()
	if res.IsError || len(codes) != 1 || !strings.Contains(resultText(res), "enter the code "+codes[0]) || !strings.Contains(resultText(res), r.fake.URL+"/device") {
		t.Fatalf("connect: %s (codes %v)", resultText(res), codes)
	}
	if !strings.Contains(resultText(res), "only the files it creates itself") {
		t.Errorf("connect does not say what the daemon asks for: %s", resultText(res))
	}
	// While the user has not approved: the same code again, from both tools,
	// and no second sign-in started.
	if text := resultText(r.call(r.s.handleConnectGoogleDrive, map[string]any{})); !strings.Contains(text, "already waiting") || !strings.Contains(text, codes[0]) {
		t.Errorf("connect while waiting: %s", text)
	}
	if text := resultText(r.call(r.s.handleGetGoogleDrive, map[string]any{})); !strings.Contains(text, "waiting for approval") || !strings.Contains(text, codes[0]) {
		t.Errorf("get while waiting: %s", text)
	}
	if n := r.fake.CountRequests("POST /device/code"); n != 1 {
		t.Errorf("%d sign-ins were started, want one", n)
	}

	r.fake.Approve(codes[0])
	text := r.waitFor("signed in as Test User <user@example.com>")
	if strings.Contains(text, "waiting for approval") {
		t.Errorf("get after the sign-in: %s", text)
	}
	if info, err := os.Stat(config.GoogleDriveTokenPath()); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the sign-in on disk: %v, mode %v; want owner-only in the state dir", err, info.Mode().Perm())
	}
	if text := resultText(r.call(r.s.handleConnectGoogleDrive, map[string]any{})); !strings.Contains(text, "already signed in to Google Drive as Test User") {
		t.Errorf("connect when signed in: %s", text)
	}
	if !r.logged("google drive: signed in") {
		t.Errorf("the log does not note the sign-in: %s", r.log.String())
	}

	// The user removes the app's access at Google: the daemon notices.
	r.fake.RevokeAll()
	if text := resultText(r.call(r.s.handleGetGoogleDrive, map[string]any{})); !strings.Contains(text, "expired or was revoked") || !strings.Contains(text, "connect_google_drive") {
		t.Errorf("get after the access was removed: %s", text)
	}
	// Signing in again works, and so does signing out.
	r.signIn()
	if res := r.call(r.s.handleConnectGoogleDrive, map[string]any{"disconnect": true}); res.IsError || !strings.Contains(resultText(res), "signed out of Google Drive") {
		t.Fatalf("disconnect: %s", resultText(res))
	}
	if _, err := os.Stat(config.GoogleDriveTokenPath()); err == nil {
		t.Error("the sign-in is still on disk after disconnecting")
	}
	if text := resultText(r.call(r.s.handleConnectGoogleDrive, map[string]any{"disconnect": true})); !strings.Contains(text, "was not signed in") {
		t.Errorf("disconnect when signed out: %s", text)
	}
}

// TestGoogleDriveSignInDeclined: a declined sign-in is reported, and the next
// one starts afresh.
func TestGoogleDriveSignInDeclined(t *testing.T) {
	r := newDriveRig(t)
	drivetest.WriteClientFile(t, config.GoogleDriveClientPath())
	r.call(r.s.handleConnectGoogleDrive, map[string]any{})
	first := r.fake.UserCodes()
	r.fake.Deny(first[0])
	r.waitFor("the last sign-in did not complete: the sign-in was declined")
	res := r.call(r.s.handleConnectGoogleDrive, map[string]any{})
	if codes := r.fake.UserCodes(); res.IsError || len(codes) != 1 || !strings.Contains(resultText(res), codes[0]) {
		t.Errorf("connect after a declined sign-in: %s", resultText(res))
	}
	// Disconnecting gives up the sign-in under way.
	r.call(r.s.handleConnectGoogleDrive, map[string]any{"disconnect": true})
	if text := resultText(r.call(r.s.handleGetGoogleDrive, map[string]any{})); !strings.Contains(text, "not signed in") || strings.Contains(text, "waiting") {
		t.Errorf("get after giving up a sign-in: %s", text)
	}
}

// TestUploadRecording uploads recordings and checks what lands in the Drive
// and what the daemon remembers of it.
func TestUploadRecording(t *testing.T) {
	r := newDriveRig(t)
	writeConfig(t, "google_drive:\n  folder: Music/Songs\n")

	if res := r.call(r.s.handleUploadRecording, map[string]any{}); !res.IsError || !strings.Contains(resultText(res), "no recording yet") {
		t.Fatalf("upload with no recording: %s", resultText(res))
	}
	rec := r.record(map[string]any{"name": "Night Drive"})
	local, err := os.ReadFile(rec.Path)
	if err != nil {
		t.Fatal(err)
	}

	// Before the OAuth client exists, and before the sign-in.
	if res := r.call(r.s.handleUploadRecording, map[string]any{}); !res.IsError || !strings.Contains(resultText(res), "scripts/setup-google-drive.sh") {
		t.Fatalf("upload without a client file: %s", resultText(res))
	}
	drivetest.WriteClientFile(t, config.GoogleDriveClientPath())
	if res := r.call(r.s.handleUploadRecording, map[string]any{}); !res.IsError || !strings.Contains(resultText(res), "run connect_google_drive") {
		t.Fatalf("upload before the sign-in: %s", resultText(res))
	}
	r.signIn()
	if text := resultText(r.call(r.s.handleGetGoogleDrive, map[string]any{})); !strings.Contains(text, `uploads go to the folder "Music/Songs"`) {
		t.Errorf("get: %s", text)
	}

	// The newest recording, into the configured folder, under its own name.
	res := r.call(r.s.handleUploadRecording, map[string]any{})
	if res.IsError {
		t.Fatalf("upload: %s", resultText(res))
	}
	files := r.uploads()
	if len(files) != 1 {
		t.Fatalf("the Drive holds %d files, want one", len(files))
	}
	f := files[0]
	if r.fake.Path(f) != "Music/Songs/"+rec.Meta.File || f.MIMEType != "audio/flac" || !bytes.Equal(f.Data, local) {
		t.Errorf("in the Drive: %q (%s), %d bytes; want Music/Songs/%s with the local file's %d bytes", r.fake.Path(f), f.MIMEType, len(f.Data), rec.Meta.File, len(local))
	}
	if !strings.Contains(f.Description, "Recorded by mcp-midi-controller on 20") || !strings.Contains(f.Description, "peak -6.0 dBFS") {
		t.Errorf("description = %q", f.Description)
	}
	link := "https://drive.example/file/d/" + f.ID + "/view"
	if text := resultText(res); !strings.Contains(text, link) || !strings.Contains(text, `into the folder "Music/Songs"`) {
		t.Errorf("upload said: %s", text)
	}
	if out := res.StructuredContent.(map[string]any); out["link"] != link || out["uploaded"] != true {
		t.Errorf("upload returned %+v", out)
	}
	// The link is remembered with the recording.
	if text := resultText(r.call(r.s.handleListRecordings, map[string]any{})); !strings.Contains(text, "Google Drive: "+link) {
		t.Errorf("list after the upload: %s", text)
	}
	// Asked again, it is not sent again.
	res = r.call(r.s.handleUploadRecording, map[string]any{"recording": rec.Meta.Name})
	if text := resultText(res); res.IsError || !strings.Contains(text, "already in Google Drive") || !strings.Contains(text, link) || len(r.uploads()) != 1 {
		t.Errorf("a second upload: %s (%d files in the Drive)", text, len(r.uploads()))
	}

	// Sent again on demand, under another name and into another folder.
	res = r.call(r.s.handleUploadRecording, map[string]any{"recording": rec.Meta.File, "again": true, "name": "For Anna", "folder": "Shared"})
	files = r.uploads()
	if res.IsError || len(files) != 2 || r.fake.Path(files[1]) != "Shared/For Anna.flac" {
		t.Fatalf("an upload under another name: %s", resultText(res))
	}
	if res := r.call(r.s.handleUploadRecording, map[string]any{"recording": "nothing"}); !res.IsError || !strings.Contains(resultText(res), `no recording "nothing"`) {
		t.Errorf("upload of an unknown recording: %s", resultText(res))
	}
	if !r.logged("google drive: uploaded the recording") {
		t.Errorf("the log does not note the upload: %s", r.log.String())
	}

	// A connection that fails on the way does not show: the file arrives whole.
	second := r.record(map[string]any{"name": "Take two", "seconds": 4, "format": "wav"})
	r.fake.FailChunks(503)
	r.fake.CutNextChunk(1000)
	if res := r.call(r.s.handleUploadRecording, map[string]any{}); res.IsError {
		t.Fatalf("upload over a bad connection: %s", resultText(res))
	}
	want, _ := os.ReadFile(second.Path)
	if files = r.uploads(); len(files) != 3 || !bytes.Equal(files[2].Data, want) || files[2].MIMEType != "audio/wav" || len(want) < 3*256<<10 {
		t.Errorf("after a bad connection the Drive holds %d files; the last has %d bytes, want %d", len(files), len(files[len(files)-1].Data), len(want))
	}
}

// TestRecordingUploadsItself: a recording started with upload goes to the
// Drive when it is finished, and so does every song's with auto_upload; an
// upload that fails is reported where the recording is.
func TestRecordingUploadsItself(t *testing.T) {
	hub := midicontrol.NewHub()
	r := newDriveRig(t)
	r.s.midi = hub
	brain(t, hub, func(midicontrol.Command) {})
	r.signIn()

	rec := r.record(map[string]any{"upload": true})
	if rec.Upload != "uploaded" || rec.Meta.Drive == nil || len(r.uploads()) != 1 {
		t.Fatalf("a recording started with upload: %+v (%d files in the Drive)", rec, len(r.uploads()))
	}
	if got, err := r.s.recordings().Get(rec.Meta.Name); err != nil || got.Drive == nil || got.Drive.Link != rec.Meta.Drive.Link {
		t.Errorf("the record does not hold the link: %+v, %v", got.Drive, err)
	}

	// auto_upload: a song's recording uploads itself, a recording made on its
	// own does not.
	writeConfig(t, "google_drive:\n  auto_upload: true\n  folder: Songs\n")
	if text := resultText(r.call(r.s.handleGetGoogleDrive, map[string]any{})); !strings.Contains(text, "uploaded by themselves") {
		t.Errorf("get with auto_upload: %s", text)
	}
	if rec := r.record(map[string]any{}); rec.Upload != "" || len(r.uploads()) != 1 {
		t.Errorf("a recording made on its own was uploaded: %+v", rec)
	}
	if res := r.call(r.s.handleSaveSong, map[string]any{"yaml": toolSong}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	res := r.call(r.s.handlePlaySong, map[string]any{"song": "night_drive", "preroll_ms": 20, "record": true})
	if res.IsError || !strings.Contains(resultText(res), "and then uploaded to Google Drive") {
		t.Fatalf("play: %s", resultText(res))
	}
	for r.s.player.Status().Playing {
		time.Sleep(5 * time.Millisecond)
	}
	r.feed.set(0)
	rec = lastRecording(t, r.s)
	files := r.uploads()
	if rec.Upload != "uploaded" || len(files) != 2 || r.fake.Path(files[1]) != "Songs/"+rec.Meta.File {
		t.Fatalf("a song's recording with auto_upload: %+v (%d files in the Drive)", rec, len(files))
	}

	// The sign-in ends; the next recording is made, its upload fails, and
	// both are said.
	r.fake.RevokeAll()
	r.feed.set(0.5)
	rec = r.record(map[string]any{"upload": true})
	if rec.Error != "" || !strings.Contains(rec.Upload, "not connected to Google Drive") || !strings.Contains(rec.Upload, "run connect_google_drive") || rec.Meta.Drive != nil {
		t.Fatalf("a recording whose upload fails: %+v", rec)
	}
	if text := resultText(r.call(r.s.handleListRecordings, map[string]any{})); !strings.Contains(text, "the upload of") || !strings.Contains(text, "not connected to Google Drive") {
		t.Errorf("list after a failed upload: %s", text)
	}
	if text := resultText(r.call(r.s.handleStopRecording, map[string]any{})); !strings.Contains(text, "the upload to Google Drive failed") {
		t.Errorf("stop after a failed upload: %s", text)
	}
}

// TestRecordingHoldsTheHostAwake: the host is held awake from the start of a
// recording until its file is written, and a recording that never starts
// holds nothing.
func TestRecordingHoldsTheHostAwake(t *testing.T) {
	var held, released atomic.Int32
	hub := midicontrol.NewHub()
	s, feed := newRecordingServer(t, WithMidiControl(hub), WithHostAwake(func() func() {
		held.Add(1)
		var once sync.Once
		return func() { once.Do(func() { released.Add(1) }) }
	}))
	brain(t, hub, func(midicontrol.Command) {})
	feed.set(0.5)

	if res := call(t, s.handleStartRecording, map[string]any{"seconds": 0.3}); res.IsError {
		t.Fatalf("start: %s", resultText(res))
	}
	if held.Load() != 1 || released.Load() != 0 {
		t.Errorf("while recording: held %d, released %d; want one hold", held.Load(), released.Load())
	}
	lastRecording(t, s)
	for deadline := time.Now().Add(5 * time.Second); released.Load() != 1 && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	if held.Load() != 1 || released.Load() != 1 {
		t.Errorf("after the recording: held %d, released %d; want the hold released", held.Load(), released.Load())
	}

	// A refused recording holds nothing; a recorded song that cannot start
	// gives back both holds, the recording's and the playback's.
	if res := call(t, s.handleStartRecording, map[string]any{"tap": "nope"}); !res.IsError || held.Load() != 1 {
		t.Errorf("a refused recording: %s (held %d)", resultText(res), held.Load())
	}
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": toolSong}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	if res := call(t, s.handlePlaySong, map[string]any{"song": "night_drive", "tracks": []string{"nope"}, "record": true}); !res.IsError {
		t.Fatalf("a playback of an unknown track started: %s", resultText(res))
	}
	if held.Load() != 3 || released.Load() != 3 {
		t.Errorf("after a recorded song that did not start: held %d, released %d; want 3 and 3", held.Load(), released.Load())
	}
}

// TestDescribeRecordingStalls: late audio is told apart from missing audio by
// the clock.
func TestDescribeRecordingStalls(t *testing.T) {
	m := recording.Meta{
		Name: "Take", Format: recording.FLAC, Seconds: 60, SampleRate: 48000, Channels: 2, BitDepth: 24,
		TrimmedLeadMS: 2000, CapturedSeconds: 70, WallSeconds: 70.02,
		Stalls: []audiotap.Stall{{Frame: 48000 * 12, GapMS: 350}, {Frame: 48000 * 30, GapMS: 900, Reconnect: true}},
	}
	text := describeRecording(m, "")
	if !strings.Contains(text, "arrived late 2 time(s) while recording, at 0:10.0 (350 ms) 0:28.0 (900 ms): all of the audio arrived, so the take is whole") {
		t.Errorf("late but whole: %s", text)
	}
	m.WallSeconds = 71.3
	if text = describeRecording(m, ""); !strings.Contains(text, "about 1.30 s of audio never arrived, so the take has a break there") {
		t.Errorf("audio missing: %s", text)
	}
	m.Stalls = nil
	if text = describeRecording(m, ""); strings.Contains(text, "late") || strings.Contains(text, "arrived") {
		t.Errorf("no stalls: %s", text)
	}
}

// TestUploadFolderIsRemembered: the folder the daemon made for uploads keeps
// receiving them when the user renames it and moves it somewhere else; only a
// folder that is gone is made anew.
func TestUploadFolderIsRemembered(t *testing.T) {
	r := newDriveRig(t)
	writeConfig(t, "google_drive:\n  folder: Songs\n")
	r.signIn()
	folders := func() (out []drivetest.File) {
		for _, f := range r.fake.Files() {
			if f.IsFolder() {
				out = append(out, f)
			}
		}
		return out
	}
	// upload records a take, uploads it and returns where it landed.
	upload := func() string {
		t.Helper()
		rec := r.record(map[string]any{})
		if res := r.call(r.s.handleUploadRecording, map[string]any{"recording": rec.Meta.Name}); res.IsError {
			t.Fatalf("upload: %s", resultText(res))
		}
		files := r.uploads()
		return strings.TrimSuffix(r.fake.Path(files[len(files)-1]), "/"+rec.Meta.File)
	}

	if at := upload(); at != "Songs" || len(folders()) != 1 {
		t.Fatalf("the first upload landed in %q with %d folder(s), want a new Songs", at, len(folders()))
	}
	songs := folders()[0].ID

	// The user files the folder away under a name of their own.
	music := r.fake.AddFolder("Music", "root")
	r.fake.Rename(songs, "Demos")
	r.fake.Move(songs, music)
	if at := upload(); at != "Music/Demos" || len(folders()) != 2 {
		t.Errorf("after the folder was renamed and moved the upload landed in %q with %d folders, want the same folder (Music/Demos)", at, len(folders()))
	}
	// The folder on the call is another path: it gets a folder of its own, and
	// is remembered separately.
	rec := r.record(map[string]any{})
	if res := r.call(r.s.handleUploadRecording, map[string]any{"recording": rec.Meta.Name, "folder": "Shared"}); res.IsError {
		t.Fatalf("upload into another folder: %s", resultText(res))
	}
	if at := upload(); at != "Music/Demos" || len(folders()) != 3 {
		t.Errorf("after an upload into another folder the configured one landed in %q with %d folders", at, len(folders()))
	}

	// In the bin, the folder is gone: the next upload makes a new Songs.
	r.fake.Trash(songs)
	if at := upload(); at != "Songs" || len(folders()) != 4 {
		t.Errorf("after the folder went into the bin the upload landed in %q with %d folders, want a new Songs", at, len(folders()))
	}
	// And that new one is the one remembered from now on.
	if at := upload(); at != "Songs" || len(folders()) != 4 {
		t.Errorf("the upload after that landed in %q with %d folders", at, len(folders()))
	}
}

// TestPlaySongRecordsSequenced: a song the session's sequencer plays is
// recorded too. No note passes through the daemon then, so the recording takes
// the start of the music from the playback's clock: what sounds in the
// lead-in, while the presets load, is not in the file.
func TestPlaySongRecordsSequenced(t *testing.T) {
	hub := midicontrol.NewHub()
	s, feed := newRecordingServer(t, WithMidiControl(hub))
	t.Cleanup(func() { s.player.Stop() })
	stageSequencer(t)
	// The stand-in session: when the transport starts it makes a noise louder
	// than the song for the first 600 ms of the lead-in (1.6 s for this song),
	// falls silent, and plays the song from the end of the lead-in on.
	brain(t, hub, func(cmd midicontrol.Command) {
		if cmd.Type != "cc" || cmd.Controller != 102 {
			return
		}
		go func() {
			start := time.Now()
			feed.set(0.9)
			time.Sleep(time.Until(start.Add(600 * time.Millisecond)))
			feed.set(0)
			time.Sleep(time.Until(start.Add(1600 * time.Millisecond)))
			feed.set(0.5)
			time.Sleep(time.Until(start.Add(3200 * time.Millisecond)))
			feed.set(0)
		}()
	})
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": toolSong}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	if res := call(t, s.handleAuthorSongSession, map[string]any{"song": "night_drive"}); res.IsError {
		t.Fatalf("author: %s", resultText(res))
	}

	res := call(t, s.handlePlaySong, map[string]any{"song": "night_drive", "record": true})
	if text := resultText(res); res.IsError || !strings.Contains(text, "the session's sequencer plays") || !strings.Contains(text, `recording it as "Night Drive `) {
		t.Fatalf("play: %s", text)
	}
	last := lastRecording(t, s)
	if last.Error != "" {
		t.Fatalf("the recording failed: %s", last.Error)
	}
	m := last.Meta
	// The lead-in that was cut holds the noise; the file starts at the song.
	if m.TrimmedLeadMS < 1400 {
		t.Errorf("only %d ms of lead were cut, want the 1.6 s lead-in", m.TrimmedLeadMS)
	}
	if m.Seconds < 1.5 || m.Seconds > 6 {
		t.Errorf("the recording is %.3f s long, want the 1.6 s song and what followed", m.Seconds)
	}
	if _, _, peak := readFLAC(t, last.Path); math.Abs(peak-0.5) > 0.001 {
		t.Errorf("the file peaks at %.3f: the noise of the lead-in (0.9) is in it", peak)
	}
}
