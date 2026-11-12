package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
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
	// Envelope shapes a channel's notes: they rise to their amplitude in a
	// straight line over attack and, once released, fall 60 dB over release,
	// as a reverb's tail does, behind the strip's mute too. nil, or zero
	// durations, start and stop a note at once.
	Envelope func(ch int) (attack, release time.Duration)
	// Floor is what a channel's strip carries while it is open and no note
	// plays, as an amplitude at rigFloorHz: a tail that has not died yet,
	// held steady so a test does not hang on when the strip opens.
	Floor func(ch int) float64
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
	// SelfPlay is what a channel's instrument plays by itself while AUM's
	// transport runs, as a drum machine's own sequencer does: an amplitude
	// at rigSelfHz, behind the strip's mute like a note. nil, or 0, plays
	// nothing.
	SelfPlay func(ch int) float64
	// Running is whether the transport runs: Start Play on the global
	// channel starts it, Stop/Rewind stops it. Bypassed is the session's
	// sequencer's bypass (CC 106 there).
	Running, Bypassed bool
	// Armed is the master strip's record-arm (the song's mapping of its Rec
	// enable, CC 107 on the global channel) and Recording AUM's recorder
	// (Toggle Record, CC 105 there). Both latch by value, as on a device. The
	// recorder starts the transport when it stands. Switched off while the
	// transport runs it ends its take on the next bar line (Bar long), and a
	// second off before that line withdraws the first; switched off while the
	// transport stands it ends the take at once. A finished take of an armed
	// master is listed under the open session's name in Takes.
	Armed, Recording bool
	Bar              time.Duration
	// TakeTempo is the tempo AUM prints in a take's name.
	TakeTempo string
	// ClipOff is when the open session's sequencer switches the recorder
	// off, counted from the transport's start, as a stem's own sequence
	// does; 0 leaves the recorder to whoever started it.
	ClipOff  time.Duration
	Takes    map[string][]rigTake
	recStart time.Time
	// recEnd is the bar line a take ends on, zero while none is asked for.
	recEnd   time.Time
	clipSent bool
	// Surfaces are the control surfaces the daemon pushed, in order, each with
	// the brain connection it was pushed to (the first is 1).
	Surfaces []rigSurface
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
	notes   map[[2]int]*rigNote
	started time.Time // when the stream began
	frame   int64     // the frame the stream has been rendered up to
	brain   *websocket.Conn
	tap     *audiotap.Store
	// trackTaps are the taps of the session's tracks, by MIDI channel.
	trackTaps map[int]*audiotap.Store
	taps      int
	pulling   map[string]bool
	// lan is the daemon's LAN receiver as the probe app reaches it, started
	// by the first sync report (song_pull_report_test.go).
	lan     *httptest.Server
	lanOnce sync.Once
}

const rigSampleRate = 48000

// rigFloorHz is the pitch of a strip's Floor, rigSelfHz the one of what an
// instrument plays by itself (SelfPlay).
const (
	rigFloorHz = 110.0
	rigSelfHz  = 330.0
)

// The global channel's controls the rig follows: the transport's (the
// convention's CCs) and the sequencer's bypass.
const (
	rigStartCC  = 102
	rigStopCC   = 103
	rigRewindCC = 104
	rigRecordCC = 105
	rigBypassCC = 106
	rigArmCC    = 107
)

// rigTake is a recording AUM's recorder made: its name as AUM's list prints
// it (no extension) and its length.
type rigTake struct {
	Name   string
	Frames int64
}

// recorderOff is the recorder's mapped control at 0. r.mu must be held.
func (r *songRig) recorderOff() {
	switch {
	case !r.Recording:
	case !r.Running || r.Bar <= 0:
		r.endTake(time.Since(r.recStart), false)
	case !r.recEnd.IsZero():
		// A second off before the bar line withdraws the first.
		r.recEnd = time.Time{}
	default:
		bars := math.Floor(float64(time.Since(r.recStart))/float64(r.Bar)) + 1
		r.recEnd = r.recStart.Add(time.Duration(bars) * r.Bar)
	}
}

// recorder moves the recorder on with the clock: the session's sequencer
// switches it off when its time has come, and a take that was asked to end
// ends on its bar line. r.mu must be held.
func (r *songRig) recorder() {
	if !r.Recording || !r.Running {
		return
	}
	if r.ClipOff > 0 && !r.clipSent && time.Since(r.recStart) >= r.ClipOff {
		r.clipSent = true
		r.recorderOff()
	}
	if !r.recEnd.IsZero() && !time.Now().Before(r.recEnd) {
		r.endTake(r.recEnd.Sub(r.recStart), true)
	}
}

// endTake finishes the recorder's take, length long: one that ended on a bar
// line is a few frames short of it, as on a device. It is listed when the
// master was armed. r.mu must be held.
func (r *songRig) endTake(length time.Duration, barLine bool) {
	r.Recording, r.recEnd, r.clipSent = false, time.Time{}, false
	if !r.Armed {
		return
	}
	if !barLine && r.Bar > 0 {
		// A take ended while the transport stands ends wherever that is. The
		// rig ends it between two beats (of four to the bar), so a test does
		// not hang on where.
		beat := r.Bar / 4
		length = length/beat*beat + beat/2
	}
	frames := int64(length.Seconds() * rigSampleRate)
	if barLine {
		// A few frames short of the bar line, and not every take by the
		// same few: on a device 4, 6 and 7.
		frames -= 7 - int64(len(r.Takes)%2)*3
	}
	if r.Takes == nil {
		r.Takes = map[string][]rigTake{}
	}
	folder := r.Session[strings.LastIndexByte(r.Session, '/')+1:]
	name := fmt.Sprintf("Master - %s bpm - %03d", r.TakeTempo, len(r.Takes[folder])+1)
	r.Takes[folder] = append(r.Takes[folder], rigTake{Name: name, Frames: frames})
}

// rigSurface is a control surface the rig's brain was sent.
type rigSurface struct {
	// Brain counts the rig's brain connections: the session AUM loaded last
	// has the highest.
	Brain int
	midicontrol.ControlSurface
}

// rigNote is a sounding note: the frames it started and was released at (-1
// while it is held).
type rigNote struct {
	on, off int64
}

// gain is a note's envelope on channel ch at frame f, 0 to 1. r.mu must be
// held.
func (r *songRig) gain(n *rigNote, ch int, f int64) float64 {
	var attack, release time.Duration
	if r.Envelope != nil {
		attack, release = r.Envelope(ch)
	}
	rise := func(at int64) float64 {
		frames := attack.Seconds() * rigSampleRate
		if frames <= 0 {
			return 1
		}
		return math.Min(1, float64(at-n.on)/frames)
	}
	if n.off < 0 || f < n.off {
		return rise(f)
	}
	frames := release.Seconds() * rigSampleRate
	if frames <= 0 {
		return 0
	}
	t := float64(f-n.off) / frames
	if t >= 1 {
		return 0
	}
	return rise(n.off) * math.Pow(10, -3*t)
}

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
// it. The brain and the tap are not connected yet. opts are further options
// of the server, such as the automatic import of a session's rig.
func newSongRig(t *testing.T, opts ...Option) *songRig {
	t.Helper()
	r := &songRig{t: t, hub: midicontrol.NewHub(), reg: audiotap.NewRegistry(), notes: map[[2]int]*rigNote{}, pulling: map[string]bool{}}
	r.wda = ipadtest.New(ipadtest.MixerAUM().Source())
	t.Cleanup(r.wda.Close)
	r.s = newSongServer(t, append([]Option{WithMidiControl(r.hub), WithAudioTap(r.reg), WithIPad(ipad.New(r.wda.URL))}, opts...)...)
	// What a brain's or a tap's connect starts writes into the state dir.
	waitBackgroundAtCleanup(t, r.s)

	// The receivers' callbacks are the daemon's (cmd/mcp-midi-controller).
	mux := http.NewServeMux()
	midicontrol.Register(mux, r.hub, midicontrol.Callbacks{
		OnConnect: func(remote string) {
			r.s.NotifyMidiControl(true, remote)
			r.s.OnMidiControlConnected()
		},
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
	pause, settle := ipad.AUMLoadPause, ipad.ProbeSettle
	ipad.AUMLoadPause, ipad.ProbeSettle = 10*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { ipad.AUMLoadPause, ipad.ProbeSettle = pause, settle })
	old := []time.Duration{songPullGrace, songPullTimeout, songDialTimeout, songPresetWait, songSettleWait, songNoteHold, songNoteTail, songFloorWait, songTapNameGrace, songNoteHoldMax, songQuietWait, songPullPoll, songPullResolved}
	// The stand-ins answer within milliseconds; the timeouts only bound a
	// test on a machine too busy to run them.
	songPullGrace, songPullTimeout, songDialTimeout = 300*time.Millisecond, 20*time.Second, 20*time.Second
	songPullPoll, songPullResolved = 20*time.Millisecond, 20*time.Second
	songPresetWait, songSettleWait = 20*time.Millisecond, 20*time.Millisecond
	songNoteHold, songNoteTail, songFloorWait = 150*time.Millisecond, 150*time.Millisecond, 60*time.Millisecond
	songTapNameGrace = 100 * time.Millisecond
	songNoteHoldMax, songQuietWait = 600*time.Millisecond, 200*time.Millisecond
	pullSettle := songPullSettle
	songPullSettle = 60 * time.Millisecond
	t.Cleanup(func() {
		songPullSettle = pullSettle
		songPullGrace, songPullTimeout, songDialTimeout = old[0], old[1], old[2]
		songPresetWait, songSettleWait = old[3], old[4]
		songNoteHold, songNoteTail, songFloorWait = old[5], old[6], old[7]
		songTapNameGrace = old[8]
		songNoteHoldMax, songQuietWait = old[9], old[10]
		songPullPoll, songPullResolved = old[11], old[12]
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
	brain := r.taps
	if r.tap != nil {
		r.tap.Disconnect()
	}
	for _, st := range r.trackTaps {
		st.Disconnect()
	}
	r.trackTaps = map[int]*audiotap.Store{}
	var dialled []string
	dial := func(name string) *audiotap.Store {
		source := name
		if name == "" {
			// A tap without a name is known by where it dials from.
			name, source = "tap-"+string(rune('0'+r.taps)), "ProbeAudioTap"
		}
		st := r.reg.Connect(name, "ipad:1")
		st.SetFormat(audiotap.Format{Encoding: "f32le", Channels: 2, SampleRate: rigSampleRate, Source: source})
		dialled = append(dialled, name)
		return st
	}
	r.tap = dial(names[master])
	for strip, name := range names {
		if strip != master {
			r.trackTaps[strip] = dial(name)
		}
	}
	r.notes = map[[2]int]*rigNote{}
	r.mu.Unlock()
	// The tap receiver's connect callbacks, as the daemon wires them.
	for _, name := range dialled {
		r.s.NotifyAudioTap(true, name, "ipad:1")
		r.s.OnAudioTapConnected(name)
	}
	if old != nil {
		_ = old.CloseNow()
	}
	r.t.Cleanup(func() { _ = c.CloseNow() })
	go r.read(c, brain)
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline) && !r.hub.Connected(); {
		time.Sleep(5 * time.Millisecond)
	}
}

// read takes the daemon's commands off one brain connection, the rig's
// brain-th.
func (r *songRig) read(c *websocket.Conn, brain int) {
	for {
		_, data, err := c.Read(context.Background())
		if err != nil {
			return
		}
		var cmd midicontrol.Command
		if json.Unmarshal(data, &cmd) != nil {
			continue
		}
		if cmd.Type == midicontrol.ControlSurfaceType {
			var cs midicontrol.ControlSurface
			if json.Unmarshal(data, &cs) == nil {
				r.mu.Lock()
				r.Surfaces = append(r.Surfaces, rigSurface{brain, cs})
				r.mu.Unlock()
			}
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
			r.notes[[2]int{cmd.Channel, cmd.Note}] = &rigNote{on: r.frame, off: -1}
		case "noteOff":
			k := [2]int{cmd.Channel, cmd.Note}
			if n, held := r.notes[k]; held && n.off < 0 {
				n.off = r.frame
				if r.gain(n, cmd.Channel, r.frame) == 0 {
					delete(r.notes, k)
				}
			}
		case "pc":
			r.Programs = append(r.Programs, cmd)
		case "cc":
			if cmd.Channel == song.GlobalChannel {
				switch cmd.Controller {
				case rigStartCC:
					r.Running = true
				case rigStopCC:
					r.Running = false
				case rigBypassCC:
					r.Bypassed = cmd.Value >= 64
				case rigArmCC:
					r.Armed = cmd.Value >= 64
				case rigRecordCC:
					switch on := cmd.Value >= 64; {
					case on && !r.Recording:
						r.Recording, r.recStart, r.Running = true, time.Now(), true
						r.recEnd, r.clipSent = time.Time{}, false
					case !on:
						r.recorderOff()
					}
				}
			}
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
		r.recorder()
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
		note    *rigNote // nil for a strip's floor
	}
	var voices []voice
	for k, n := range r.notes {
		if r.muted[k[0]] {
			continue
		}
		hz, amp := 440*math.Pow(2, float64(k[1]-69)/12), 0.2
		if r.Voice != nil {
			hz, amp = r.Voice(k[0], k[1])
		}
		if amp > 0 {
			voices = append(voices, voice{k[0], hz, amp, n})
		}
	}
	if r.Floor != nil {
		for ch := 1; ch <= 16; ch++ {
			if amp := r.Floor(ch); amp > 0 && !r.muted[ch] {
				voices = append(voices, voice{ch, rigFloorHz, amp, nil})
			}
		}
	}
	if r.SelfPlay != nil && r.Running {
		for ch := 1; ch <= 16; ch++ {
			if amp := r.SelfPlay(ch); amp > 0 && !r.muted[ch] {
				voices = append(voices, voice{ch, rigSelfHz, amp, nil})
			}
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
					g := 1.0
					if vc.note != nil {
						g = r.gain(vc.note, vc.ch, f)
					}
					v += g * vc.amp * math.Sin(2*math.Pi*vc.hz*float64(f)/rigSampleRate)
				}
			}
			buf = append(buf, float32(v), float32(v))
		}
		tap.AppendAudio(buf)
	}
	// A released note that has died away is gone.
	defer func() {
		for k, n := range r.notes {
			if n.off >= 0 && r.gain(n, k[0], r.frame) == 0 {
				delete(r.notes, k)
			}
		}
	}()
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

// surface waits until the brain AUM started last was sent a control surface
// and no other follows it for a moment, and returns the last one: the surface
// the brain shows. It fails the test when none comes.
func (r *songRig) surface() midicontrol.ControlSurface {
	r.t.Helper()
	r.s.waitBackground()
	deadline := time.Now().Add(5 * time.Second)
	for last, since := -1, time.Now(); ; time.Sleep(10 * time.Millisecond) {
		r.mu.Lock()
		n, brain := len(r.Surfaces), r.taps
		var cs *rigSurface
		if n > 0 && r.Surfaces[n-1].Brain == brain {
			cs = &r.Surfaces[n-1]
		}
		r.mu.Unlock()
		if n != last {
			last, since = n, time.Now()
		}
		if cs != nil && time.Since(since) > 100*time.Millisecond {
			return cs.ControlSurface
		}
		if time.Now().After(deadline) {
			r.t.Fatalf("the brain of the session loaded last was sent no control surface (%d sent to earlier ones)", n)
		}
	}
}

// pullStaged plays the probe app in front: it fetches a staged session as
// soon as its file is there or changes.
func (r *songRig) pullStaged(file string) {
	r.mu.Lock()
	if r.pulling[file] {
		r.mu.Unlock()
		return
	}
	r.pulling[file] = true
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

// rigProbeBundle is the bundle id the rig's probe app is known under.
const rigProbeBundle = "TEAM.com.teemow.auv3probe"

// probeApp puts the AUv3 Probe app on the rig's iPad, with files in AUM's
// folder, and lets the daemon bring it forward. Like the app it fetches a
// staged session when its sync is started, or when "keep controller" is
// tapped on the session's row, but not a file it holds as a conflict.
func (r *songRig) probeApp(files map[string][]ipadtest.ProbeFile) *ipadtest.ProbeApp {
	r.t.Helper()
	rememberIPadApp("probe", rigProbeBundle)
	app := &ipadtest.ProbeApp{Files: files}
	app.OnKeepController = r.s.OnAUMSessionDownloaded
	app.OnSync = func() {
		var fetch []string
		app.Set(func(p *ipadtest.ProbeApp) {
			for folder, fs := range p.Files {
				for _, f := range fs {
					if f.State == "" {
						fetch = append(fetch, strings.TrimPrefix(folder+"/"+f.Name, "/"))
					}
				}
			}
		})
		for _, file := range fetch {
			if _, err := os.Stat(stagedSessionPath(strings.TrimSuffix(file, ".aumproj"))); err == nil {
				r.s.OnAUMSessionDownloaded(file)
			}
		}
	}
	r.wda.Apps = map[string]*ipadtest.ProbeApp{rigProbeBundle: app}
	return app
}

// aumLoads plays AUM loading the session the daemon taps in FILES: the
// session's brain and tap start once its row was tapped.
func (r *songRig) aumLoads(folder, name string, strips []ipadtest.Strip, notices ...string) {
	r.wda.SetScreens() // restart the gesture count
	r.wda.SourceFunc = ipadtest.OpenSessionScreens(folder, name, strips, 2, notices...)
	// An AUM that went away and was started again shows its start screen. The
	// row's tap is its fifth gesture then, and the brain and tap start with
	// the sixth as well.
	r.wda.AfterLaunch = ipadtest.FreshSessionScreens(folder, name, strips, 1, 2, notices...)
	// The session's connects write into the state dir, so the cleanup waits
	// for the goroutine to be gone, as pullStaged's does.
	stop, done := make(chan struct{}), make(chan struct{})
	r.t.Cleanup(func() { close(stop); <-done })
	go func() {
		defer close(done)
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
