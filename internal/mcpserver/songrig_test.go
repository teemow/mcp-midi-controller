package mcpserver

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/teemow/aum-session-go/aum"
	"github.com/teemow/mcp-midi-controller/internal/audiotap"
	"github.com/teemow/mcp-midi-controller/internal/ipad"
	"github.com/teemow/mcp-midi-controller/internal/ipad/ipadtest"
	"github.com/teemow/mcp-midi-controller/internal/song"
	"github.com/teemow/midi-transport/midicontrol"
)

// songRig stands in for the iPad in the song tools' tests: a brain that takes
// the daemon's commands, a tap that streams what a small synth makes of them,
// the probe app that pulls staged sessions, and AUM's screen.
type songRig struct {
	t   *testing.T
	s   *Server
	hub *midicontrol.Hub
	reg *audiotap.Registry
	wda *ipadtest.WDA
	url string // the brain's endpoint

	mu sync.Mutex
	// Voice is what a note sounds like: its frequency and amplitude (0 is
	// silence). nil plays every note at its own pitch.
	Voice func(ch, note int) (hz, amp float64)
	// Strips maps a strip's mute control (channel, controller) to the note
	// channel it silences; strips() fills it from a song's plan. A muted
	// strip's notes do not reach the tap, and as on a device a mute that
	// reverses the one before it for the same strip within rigMuteWindow is
	// lost.
	Strips map[[2]int]int
	// LostMutes counts the mutes lost that way.
	LostMutes int
	muted     map[int]bool
	muteAt    map[int]time.Time
	// Programs are the Program Changes received, in order.
	Programs []midicontrol.Command
	// Commands is everything received.
	Commands []midicontrol.Command
	// Session, when set, is the staged session the rig's AUM has open, and
	// makes its taps behave like the plugin that sends its name: every tap of
	// that session dials in under the name the staged file gives it, the
	// master's with the whole mix and a track's with that track alone.
	// Without it there is one tap that gives no name, as from a build of
	// the plugin from before taps had names.
	Session string
	notes   map[[2]int]bool
	started time.Time // when the stream began
	frame   int64     // the frame the stream has been rendered up to
	brain   *websocket.Conn
	tap     *audiotap.Store
	// trackTaps are the taps of the session's tracks, by MIDI channel.
	trackTaps map[int]*audiotap.Store
	taps      int
	pulling   bool
}

const rigSampleRate = 48000

// rigMuteWindow stands for one audio buffer of AUM: two opposite mutes for a
// strip inside it leave the strip at the first.
const rigMuteWindow = 25 * time.Millisecond

// strips makes the rig's mixer the one of a song: every track's strip with
// its mute.
func (r *songRig) strips(plan *song.Plan) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Strips, r.muted, r.muteAt = map[[2]int]int{}, map[int]bool{}, map[int]time.Time{}
	for _, tp := range plan.Tracks {
		m := tp.MuteEvent(true)
		r.Strips[[2]int{m.Channel, m.Data1}] = tp.Channel
	}
}

// wantMuted waits until the muted strips are the ones of the given note
// channels (the brain's connection delivers a mute a moment after the daemon
// sent it), and fails the test when no mute was lost on the way but the
// mixer stands otherwise, or when one was lost.
func (r *songRig) wantMuted(when string, want ...int) {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.mu.Lock()
		var got []int
		for ch, on := range r.muted {
			if on {
				got = append(got, ch)
			}
		}
		lost := r.LostMutes
		r.mu.Unlock()
		sort.Ints(got)
		if lost == 0 && slices.Equal(got, want) {
			return
		}
		if lost > 0 || time.Now().After(deadline) {
			r.t.Fatalf("muted strips %s = %v, want %v (%d mute(s) reversed the one before within an audio buffer and were lost)", when, got, want, lost)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// newSongRig starts a server with the test plugins staged and a rig around
// it. The brain and the tap are not connected yet.
func newSongRig(t *testing.T) *songRig {
	t.Helper()
	r := &songRig{t: t, hub: midicontrol.NewHub(), reg: audiotap.NewRegistry(), notes: map[[2]int]bool{}}
	r.wda = ipadtest.New(ipadtest.MixerAUM().Source())
	t.Cleanup(r.wda.Close)
	r.s = newSongServer(t, WithMidiControl(r.hub), WithAudioTap(r.reg), WithIPad(ipad.New(r.wda.URL)))

	mux := http.NewServeMux()
	midicontrol.Register(mux, r.hub, midicontrol.Callbacks{
		OnConnect:    func(remote string) { r.s.NotifyMidiControl(true, remote) },
		OnDisconnect: func(remote string) { r.s.NotifyMidiControl(false, remote) },
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	r.url = "ws" + strings.TrimPrefix(srv.URL, "http") + "/midi-control"

	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go r.stream(stop)
	fastSongWaits(t)
	return r
}

// fastSongWaits shortens the waits of load_song and check_song.
func fastSongWaits(t *testing.T) {
	t.Helper()
	pause := ipad.AUMLoadPause
	ipad.AUMLoadPause = 10 * time.Millisecond
	t.Cleanup(func() { ipad.AUMLoadPause = pause })
	old := []time.Duration{songPullGrace, songPullTimeout, songDialTimeout, songPresetWait, songSettleWait, songNoteHold, songNoteTail, songFloorWait, songTapNameGrace}
	// The stand-ins answer within milliseconds; the timeouts only bound a
	// test on a machine too busy to run them.
	songPullGrace, songPullTimeout, songDialTimeout = 300*time.Millisecond, 20*time.Second, 20*time.Second
	songPresetWait, songSettleWait = 20*time.Millisecond, 20*time.Millisecond
	songNoteHold, songNoteTail, songFloorWait = 150*time.Millisecond, 150*time.Millisecond, 60*time.Millisecond
	songTapNameGrace = 100 * time.Millisecond
	t.Cleanup(func() {
		songPullGrace, songPullTimeout, songDialTimeout = old[0], old[1], old[2]
		songPresetWait, songSettleWait = old[3], old[4]
		songNoteHold, songNoteTail, songFloorWait = old[5], old[6], old[7]
		songTapNameGrace = old[8]
	})
}

// stagedTapNames reads the names a staged session gives its taps, by strip:
// what the plugin restores when AUM loads the file, and dials in under.
func stagedTapNames(t *testing.T, id string) map[int]string {
	t.Helper()
	data, err := os.ReadFile(stagedSessionPath(id))
	if err != nil {
		t.Fatalf("the rig's session %q is not staged: %v", id, err)
	}
	sess, err := aum.Open(data)
	if err != nil {
		t.Fatalf("open the staged %q: %v", id, err)
	}
	names := map[int]string{}
	for strip, ch := range sess.Channels() {
		for slot := range ch.Nodes {
			doc, err := sess.NodeAuStateDoc(strip, slot)
			if err != nil {
				continue
			}
			raw, isTap := doc["probeAudioTapConfig"]
			if !isTap {
				continue
			}
			var cfg struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(raw, &cfg); err != nil {
				t.Fatalf("the tap config of strip %d (%s): %v", strip, raw, err)
			}
			names[strip] = cfg.Name
		}
	}
	return names
}

// connect starts a new brain and new taps, as AUM does when it loads a
// session; the ones before are dropped.
func (r *songRig) connect() {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, r.url, nil)
	if err != nil {
		r.t.Fatalf("dial the brain endpoint: %v", err)
	}
	// The taps of the session, by strip: the last strip is the master's, a
	// track's strip has the number of its MIDI channel.
	var names map[int]string
	master := 0
	if r.Session != "" {
		names = stagedTapNames(r.t, r.Session)
		for strip := range names {
			master = max(master, strip)
		}
	}
	r.mu.Lock()
	old := r.brain
	r.brain = c
	r.taps++
	if r.tap != nil {
		r.tap.Disconnect()
	}
	for _, st := range r.trackTaps {
		st.Disconnect()
	}
	r.trackTaps = map[int]*audiotap.Store{}
	dial := func(name string) *audiotap.Store {
		source := name
		if name == "" {
			// A tap without a name is known by where it dials from.
			name, source = "tap-"+string(rune('0'+r.taps)), "ProbeAudioTap"
		}
		st := r.reg.Connect(name, "ipad:1")
		st.SetFormat(audiotap.Format{Encoding: "f32le", Channels: 2, SampleRate: rigSampleRate, Source: source})
		return st
	}
	r.tap = dial(names[master])
	for strip, name := range names {
		if strip != master {
			r.trackTaps[strip] = dial(name)
		}
	}
	r.notes = map[[2]int]bool{}
	r.mu.Unlock()
	if old != nil {
		_ = old.CloseNow()
	}
	r.t.Cleanup(func() { _ = c.CloseNow() })
	go r.read(c)
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline) && !r.hub.Connected(); {
		time.Sleep(5 * time.Millisecond)
	}
}

// read takes the daemon's commands off one brain connection.
func (r *songRig) read(c *websocket.Conn) {
	for {
		_, data, err := c.Read(context.Background())
		if err != nil {
			return
		}
		var cmd midicontrol.Command
		if json.Unmarshal(data, &cmd) != nil {
			continue
		}
		r.mu.Lock()
		// What sounded until now is rendered before the command changes it,
		// so the audio follows the commands however late the stream's own
		// tick runs on a busy machine.
		r.render()
		r.Commands = append(r.Commands, cmd)
		switch cmd.Type {
		case "noteOn":
			r.notes[[2]int{cmd.Channel, cmd.Note}] = true
		case "noteOff":
			delete(r.notes, [2]int{cmd.Channel, cmd.Note})
		case "pc":
			r.Programs = append(r.Programs, cmd)
		case "cc":
			strip, ok := r.Strips[[2]int{cmd.Channel, cmd.Controller}]
			if !ok {
				break
			}
			on := cmd.Value >= 64
			if at, seen := r.muteAt[strip]; seen && on != r.muted[strip] && time.Since(at) < rigMuteWindow {
				r.LostMutes++
				break
			}
			r.muted[strip], r.muteAt[strip] = on, time.Now()
		}
		r.mu.Unlock()
	}
}

// stream feeds the tap in real time with the sounding notes as sines.
func (r *songRig) stream(stop <-chan struct{}) {
	tick := time.NewTicker(4 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
		}
		r.mu.Lock()
		r.render()
		r.mu.Unlock()
	}
}

// render appends the audio from where the stream stands up to now, with the
// notes sounding at the moment. r.mu must be held.
func (r *songRig) render() {
	if r.started.IsZero() {
		r.started = time.Now()
	}
	due := int64(time.Since(r.started).Seconds() * rigSampleRate)
	if r.tap == nil {
		r.frame = due
		return
	}
	type voice struct {
		ch      int
		hz, amp float64
	}
	var voices []voice
	for k := range r.notes {
		if r.muted[k[0]] {
			continue
		}
		hz, amp := 440*math.Pow(2, float64(k[1]-69)/12), 0.2
		if r.Voice != nil {
			hz, amp = r.Voice(k[0], k[1])
		}
		if amp > 0 {
			voices = append(voices, voice{k[0], hz, amp})
		}
	}
	if due <= r.frame {
		return
	}
	// feed gives a tap the voices of one MIDI channel, or of all (0).
	feed := func(tap *audiotap.Store, ch int) {
		buf := make([]float32, 0, 2*(due-r.frame))
		for f := r.frame; f < due; f++ {
			var v float64
			for _, vc := range voices {
				if ch == 0 || vc.ch == ch {
					v += vc.amp * math.Sin(2*math.Pi*vc.hz*float64(f)/rigSampleRate)
				}
			}
			buf = append(buf, float32(v), float32(v))
		}
		tap.AppendAudio(buf)
	}
	feed(r.tap, 0)
	for ch, tap := range r.trackTaps {
		feed(tap, ch)
	}
	r.frame = due
}

// waitCommands waits until the commands the rig received satisfy done, and
// returns them. The brain's connection delivers them a moment after the
// daemon sent them.
func (r *songRig) waitCommands(done func([]midicontrol.Command) bool) []midicontrol.Command {
	r.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		r.mu.Lock()
		cmds := append([]midicontrol.Command(nil), r.Commands...)
		r.mu.Unlock()
		if done(cmds) || time.Now().After(deadline) {
			return cmds
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// pullStaged plays the probe app in front: it fetches a staged session as
// soon as its file is there or changes.
func (r *songRig) pullStaged(file string) {
	r.mu.Lock()
	if r.pulling {
		r.mu.Unlock()
		return
	}
	r.pulling = true
	r.mu.Unlock()
	// The download hook writes into the state dir, so the cleanup waits for
	// the goroutine to be gone: stopping it without waiting would let the
	// test's temp dir be removed under a pull that had just begun.
	stop, done := make(chan struct{}), make(chan struct{})
	r.t.Cleanup(func() { close(stop); <-done })
	go func() {
		defer close(done)
		seen := ""
		for {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
			}
			if sha := fileSHA256(stagedSessionPath(strings.TrimSuffix(file, ".aumproj"))); sha != "" && sha != seen {
				seen = sha
				r.s.OnAUMSessionDownloaded(file)
			}
		}
	}()
}

// aumLoads plays AUM loading the session the daemon taps in FILES: the
// session's brain and tap start once its row was tapped.
func (r *songRig) aumLoads(folder, name string, strips []ipadtest.Strip, notices ...string) {
	r.wda.SetScreens() // restart the gesture count
	r.wda.SourceFunc = ipadtest.OpenSessionScreens(folder, name, strips, 2, notices...)
	stop := make(chan struct{})
	r.t.Cleanup(func() { close(stop) })
	go func() {
		for r.wda.Gestures() < 6 {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
		r.connect()
	}()
}

// nightDriveStrips is the mixer of toolSong's session.
var nightDriveStrips = []ipadtest.Strip{
	{Title: "Brain"}, {Title: "bass", Level: "+0.0 dB"}, {Title: "pad", Level: "+0.0 dB"},
	{Title: "drums", Level: "+0.0 dB"}, {Title: "Master", Level: "+0.0 dB"},
}
