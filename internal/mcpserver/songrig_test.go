package mcpserver

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/teemow/mcp-midi-controller/internal/audiotap"
	"github.com/teemow/mcp-midi-controller/internal/ipad"
	"github.com/teemow/mcp-midi-controller/internal/ipad/ipadtest"
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
	// Programs are the Program Changes received, in order.
	Programs []midicontrol.Command
	// Commands is everything received.
	Commands []midicontrol.Command
	notes    map[[2]int]bool
	started  time.Time // when the stream began
	frame    int64     // the frame the stream has been rendered up to
	brain    *websocket.Conn
	tap      *audiotap.Store
	taps     int
	pulling  bool
}

const rigSampleRate = 48000

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
	old := []time.Duration{songPullGrace, songPullTimeout, songDialTimeout, songPresetWait, songSettleWait, songNoteHold, songNoteTail, songFloorWait}
	songPullGrace, songPullTimeout, songDialTimeout = 300*time.Millisecond, 300*time.Millisecond, 2*time.Second
	songPresetWait, songSettleWait = 20*time.Millisecond, 20*time.Millisecond
	songNoteHold, songNoteTail, songFloorWait = 80*time.Millisecond, 60*time.Millisecond, 40*time.Millisecond
	t.Cleanup(func() {
		songPullGrace, songPullTimeout, songDialTimeout = old[0], old[1], old[2]
		songPresetWait, songSettleWait = old[3], old[4]
		songNoteHold, songNoteTail, songFloorWait = old[5], old[6], old[7]
	})
}

// connect starts a new brain and a new tap, as AUM does when it loads a
// session; the ones before are dropped.
func (r *songRig) connect() {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, r.url, nil)
	if err != nil {
		r.t.Fatalf("dial the brain endpoint: %v", err)
	}
	r.mu.Lock()
	old := r.brain
	r.brain = c
	r.taps++
	name := "tap-" + string(rune('0'+r.taps))
	if r.tap != nil {
		r.tap.Disconnect()
	}
	r.tap = r.reg.Connect(name, "ipad:1")
	r.tap.SetFormat(audiotap.Format{Encoding: "f32le", Channels: 2, SampleRate: rigSampleRate, Source: "ProbeAudioTap"})
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
	type voice struct{ hz, amp float64 }
	var voices []voice
	for k := range r.notes {
		hz, amp := 440*math.Pow(2, float64(k[1]-69)/12), 0.2
		if r.Voice != nil {
			hz, amp = r.Voice(k[0], k[1])
		}
		if amp > 0 {
			voices = append(voices, voice{hz, amp})
		}
	}
	if due <= r.frame {
		return
	}
	buf := make([]float32, 0, 2*(due-r.frame))
	for ; r.frame < due; r.frame++ {
		var v float64
		for _, vc := range voices {
			v += vc.amp * math.Sin(2*math.Pi*vc.hz*float64(r.frame)/rigSampleRate)
		}
		buf = append(buf, float32(v), float32(v))
	}
	r.tap.AppendAudio(buf)
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
	stop := make(chan struct{})
	r.t.Cleanup(func() { close(stop) })
	go func() {
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
