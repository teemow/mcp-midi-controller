package song

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/teemow/aum-session-go/aum"
)

// controlSong has a drum machine that plays its pads on channel 1 and hears
// control notes on 10 and 11, beside a bass on channel 2.
const controlSong = `
title: Control Song
tempo: 120
tracks:
  - id: drums
    kind: drums
    control_channels: [11, 10]
    instrument: {plugin: drums}
  - id: bass
    transpose: 12
    instrument: {plugin: synth, params: {Cutoff: 0.5}}
    effects:
      - {id: echo, plugin: delay, midi: true}
patterns:
  beat:
    drums:
      kick: "x...x...x...x..."
      hat:  "x.x.x.x.x.x.x.x."
    events:
      - {at: 2, dur: 2, note: D2, channel: 10}   # no hat on beats 3 and 4
      - {at: 0, dur: 0.25, note: E2}             # a pad, on the drums' own channel
  drop: {channel: 10, step: 1/4, notes: "C3 - - ."}
  shuffle: {channel: 11, step: 1/4, bars: 1, notes: "C0 . . ."}
  bass: {step: 1/4, notes: "A1 . A1 ."}
sections:
  a: {bars: 1, play: {drums: beat, bass: bass}}
  b: {bars: 1, play: {drums: [drop, shuffle], bass: bass}}
arrangement: [a, b]
`

func TestControlChannelsValidate(t *testing.T) {
	mustParse(t, controlSong)

	for _, tc := range []struct {
		name, from, to, want string
	}{
		{"out of range", "control_channels: [11, 10]", "control_channels: [13]", "/tracks/0/control_channels/0: 13 is not within 1..12"},
		{"zero", "control_channels: [11, 10]", "control_channels: [0]", "/tracks/0/control_channels/0: 0 is not within 1..12"},
		{"own channel", "control_channels: [11, 10]", "control_channels: [1, 10, 11]", "/tracks/0/control_channels/0: MIDI channel 1 is the track's own channel"},
		{"another track's channel", "control_channels: [11, 10]", "control_channels: [2, 10, 11]", `/tracks/0/control_channels/0: MIDI channel 2 is track "bass"'s`},
		{"another track's explicit channel", "    transpose: 12\n", "    transpose: 12\n    channel: 10\n", `/tracks/0/control_channels/1: MIDI channel 10 is track "bass"'s`},
		{"twice", "control_channels: [11, 10]", "control_channels: [10, 11, 10]", "/tracks/0/control_channels/2: MIDI channel 10 is listed twice"},
		{"shared", "    transpose: 12\n", "    transpose: 12\n    control_channels: [10]\n", `/tracks/1/control_channels/0: MIDI channel 10 is already a control channel of track "drums"`},
		{"pattern channel out of range", "drop: {channel: 10,", "drop: {channel: 16,", "/patterns/drop/channel: 16 is not within 1..12"},
		{"event channel out of range", "note: D2, channel: 10}", "note: D2, channel: 17}", "/patterns/beat/events/0/channel: 17 is not within 1..12"},
		{"drum lanes on a channel", "beat:\n", "beat:\n    channel: 10\n", "/patterns/beat/channel: control notes are written as notes or events, not drum lanes"},
		{"transposed control pattern", "drop: {channel: 10,", "drop: {channel: 10, transpose: 2,", "/patterns/drop/transpose: control notes are sent as written"},
		{"a track without the channel", "play: {drums: [drop, shuffle], bass: bass}", "play: {drums: drop, bass: shuffle}", `/sections/b/play/bass/0: pattern "shuffle" sends on channel 11, which is not one of "bass"'s control_channels (none)`},
		{"an event on a channel the track lacks", "control_channels: [11, 10]", "control_channels: [11, 9]", `/sections/a/play/drums/0: pattern "beat" sends on channel 10, which is not one of "drums"'s control_channels (9, 11)`},
		{"on the track's own channel", "drop: {channel: 10,", "drop: {channel: 1,", `/sections/b/play/drums/0: pattern "drop" sends on channel 1, which is "drums"'s own channel; leave the channel out`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(controlSong, tc.from) {
				t.Fatalf("the test song has no %q to change", tc.from)
			}
			_, err := Parse([]byte(strings.Replace(controlSong, tc.from, tc.to, 1)))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Parse: %v, want an error with %q", err, tc.want)
			}
		})
	}

	// A track at the position of a control channel takes it by default.
	crowded := controlSong
	for i := 3; i <= 10; i++ {
		crowded = strings.Replace(crowded, "patterns:", "  - {id: t"+string(rune('0'+i%10))+", instrument: {plugin: synth}}\npatterns:", 1)
	}
	if _, err := Parse([]byte(crowded)); err == nil || !strings.Contains(err.Error(), `MIDI channel 10 is track "t0"'s`) {
		t.Errorf("Parse with a tenth track on channel 10: %v, want the drums' control channel refused", err)
	}
}

// TestCompileControlNotes: control notes go out on their channel as written,
// next to the track's own notes, and are counted apart.
func TestCompileControlNotes(t *testing.T) {
	s := mustParse(t, controlSong)
	tl, err := Compile(s, nil)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	bar := tl.BarTicks
	type key struct {
		tick          int64
		channel, note int
	}
	ons := map[key]Event{}
	offs := map[key]int64{} // note on -> its note off
	open := map[[2]int]key{}
	for _, e := range tl.Events {
		switch e.Kind {
		case NoteOn:
			k := key{e.Tick, e.Channel, e.Data1}
			ons[k] = e
			open[[2]int{e.Channel, e.Data1}] = k
		case NoteOff:
			if k, ok := open[[2]int{e.Channel, e.Data1}]; ok {
				offs[k] = e.Tick
				delete(open, [2]int{e.Channel, e.Data1})
			}
		}
	}

	// Section a: the kick (GM 36) and the free pad E2 (40) on channel 1, and
	// the mute of voice 3 (D2, 38) on channel 10 from beat 3 to the bar's end.
	for _, note := range []int{36, 40} {
		if e, ok := ons[key{0, 1, note}]; !ok || e.Track != "drums" {
			t.Errorf("no drums note %d on channel 1 at the start: %v", note, onChannel(tl, 1))
		}
	}
	mute := key{2 * PPQ, 10, 38}
	if e, ok := ons[mute]; !ok || e.Track != "drums" {
		t.Fatalf("no D2 (38) on channel 10 at beat 3; channel 10 has %v", onChannel(tl, 10))
	}
	if offs[mute] != 4*PPQ {
		t.Errorf("the mute ends at tick %d, want it held to the end of the bar (%d)", offs[mute], 4*PPQ)
	}
	// Section b: drop's C3 (48) held three beats on 10, shuffle's C0 (12) on 11.
	if off, want := offs[key{bar, 10, 48}], bar+int64(math.Round(3*PPQ*defaultGate)); off != want {
		t.Errorf("the solo C3 on channel 10 ends at %d, want three beats at the gate (%d)", off, want)
	}
	// drop fills the section's bar, so shuffle, next in the play list, never
	// comes: patterns in a list play one after another.
	if got := onChannel(tl, 11); len(got) != 0 {
		t.Errorf("channel 11 has %v, want nothing", got)
	}
	// The bass keeps its own transpose on its own channel.
	if _, ok := ons[key{0, 2, 45}]; !ok {
		t.Errorf("no bass A1 + 12 (45) on channel 2 at the start: %v", onChannel(tl, 2))
	}
	// Nothing of the drums leaks onto a channel it has not.
	for k, e := range ons {
		if e.Track == "drums" && k.channel != 1 && k.channel != 10 && k.channel != 11 {
			t.Errorf("drums note %+v on channel %d", k, k.channel)
		}
	}

	for _, ts := range tl.Tracks {
		switch ts.ID {
		case "drums":
			// a: 4 kicks + 8 hats + the free C2; b: nothing on channel 1.
			// Control notes: a's D2; b's C3 (drop fills the bar).
			if ts.Channel != 1 || ts.Notes != 13 || ts.ControlNotes != 2 {
				t.Errorf("drums summary = %+v, want 13 notes on channel 1 and 2 control notes", ts)
			}
		case "bass":
			if ts.ControlNotes != 0 {
				t.Errorf("bass summary = %+v, want no control notes", ts)
			}
		}
	}
}

// TestCompileControlNotesSameNote: a pad and a control on the same note
// number do not cut each other short.
func TestCompileControlNotesSameNote(t *testing.T) {
	doc := strings.Replace(controlSong, "      - {at: 0, dur: 0.25, note: E2}             # a pad, on the drums' own channel\n",
		"      - {at: 2, dur: 0.25, note: \"38\"}          # a pad on 38 on channel 1, while 38 is held on 10\n", 1)
	tl, err := Compile(mustParse(t, doc), nil)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	var padOff, muteOff int64 = -1, -1
	for _, e := range tl.Events {
		if e.Kind == NoteOff && e.Data1 == 38 && e.Tick <= tl.BarTicks {
			switch e.Channel {
			case 1:
				padOff = e.Tick
			case 10:
				muteOff = e.Tick
			}
		}
	}
	if muteOff != 4*PPQ {
		t.Errorf("the mute on channel 10 ends at %d, want %d: the pad on channel 1 cut it", muteOff, 4*PPQ)
	}
	if padOff != 2*PPQ+PPQ/4 {
		t.Errorf("the pad on channel 1 ends at %d, want %d", padOff, 2*PPQ+PPQ/4)
	}
}

// TestCompileControlNotesAsWritten: the track's transpose moves its own
// notes and leaves its control notes as written.
func TestCompileControlNotesAsWritten(t *testing.T) {
	tl, err := Compile(mustParse(t, `
title: Transposed
tempo: 120
tracks:
  - id: keys
    transpose: 12
    control_channels: [9]
    instrument: {plugin: synth}
patterns:
  line:
    step: 1/4
    transpose: 2
    notes: "A1 . . ."
    events: [{at: 1, dur: 1, note: D2, channel: 9}]
  hold: {channel: 9, step: 1/4, notes: ". . C3 ."}
sections:
  a: {bars: 2, play: {keys: [line, hold]}}
arrangement: [a]
`), nil)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	var got []string
	for _, e := range tl.Events {
		if e.Kind == NoteOn {
			got = append(got, fmt.Sprintf("%d:ch%d:%d", e.Tick/PPQ, e.Channel, e.Data1))
		}
	}
	// A1 (33) + 12 + 2 on channel 1; D2 (38) and, a bar later, C3 (48) on 9.
	want := "0:ch1:47 1:ch9:38 6:ch9:48"
	if strings.Join(got, " ") != want {
		t.Errorf("notes = %v, want %s", got, want)
	}
}

func onChannel(tl *Timeline, ch int) []Event {
	var out []Event
	for _, e := range tl.Events {
		if e.Channel == ch && e.Kind == NoteOn {
			out = append(out, e)
		}
	}
	return out
}

// TestPlanControlChannels: the control channels belong to the track, so the
// session's control mappings start above them and the fingerprint knows them.
func TestPlanControlChannels(t *testing.T) {
	s := mustParse(t, controlSong)
	p := mustPlan(t, s)
	drums := p.Track("drums")
	if got := drums.Hears(); len(got) != 3 || got[0] != 1 || got[1] != 10 || got[2] != 11 {
		t.Errorf("drums hear %v, want [1 10 11]", got)
	}
	if bass := p.Track("bass").Hears(); len(bass) != 1 || bass[0] != 2 {
		t.Errorf("bass hears %v, want [2]", bass)
	}
	if p.ControlChannels[0] != 12 || p.ControlChannels[len(p.ControlChannels)-1] != 16 {
		t.Errorf("control channels = %v, want 12..16, above the drums' control channels", p.ControlChannels)
	}
	for _, n := range p.allNodes() {
		if n.Bypass.Channel <= 11 {
			t.Errorf("%s bypass on %s, inside the tracks' channels", nodeLabel(n), n.Bypass)
		}
		for _, pa := range n.Params {
			if pa.At.Channel <= 11 {
				t.Errorf("%s %s on %s, inside the tracks' channels", nodeLabel(n), pa.Key, pa.At)
			}
		}
	}

	// Without control channels the plan is another session, and the one
	// with them is the same whatever order they are listed in.
	without := mustParse(t, strings.NewReplacer(
		"    control_channels: [11, 10]\n", "",
		"note: D2, channel: 10}", "note: D2}",
		"play: {drums: [drop, shuffle], bass: bass}", "play: {bass: bass}",
	).Replace(controlSong))
	if fp := mustPlan(t, without).Fingerprint(); fp == p.Fingerprint() {
		t.Error("a track's control channels do not change the plan's fingerprint")
	}
	swapped := mustParse(t, strings.Replace(controlSong, "control_channels: [11, 10]", "control_channels: [10, 11]", 1))
	if fp := mustPlan(t, swapped).Fingerprint(); fp != p.Fingerprint() {
		t.Error("the order control channels are listed in changes the fingerprint")
	}
}

// TestBuildSessionControlChannels reads the authored MIDI matrix back: the
// drums' instrument hears its channel and its control channels, the bass's
// note-following effect only the bass's notes, and MIDI Control none of them.
func TestBuildSessionControlChannels(t *testing.T) {
	p := mustPlan(t, mustParse(t, controlSong))
	sess, _, err := p.BuildSession(SessionOptions{FullControl: true})
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	data, err := sess.Archive().Encode()
	if err != nil {
		t.Fatal(err)
	}
	re, err := aum.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	filters := matrixFilters(t, re)
	mask := func(chs ...int) int64 {
		var m int64
		for _, ch := range chs {
			m |= 1 << (ch - 1)
		}
		return m
	}
	for dst, want := range map[string]int64{
		"Node:Chan1:Slot0":     mask(1, 10, 11), // the drums' instrument
		"Node:Chan2:Slot0":     mask(2),         // the bass's
		"Node:Chan2:Slot2":     mask(2),         // the bass's echo, which follows its notes
		"BuiltIn:MIDI Control": mask(12, 13, 14, 15, 16),
	} {
		got, ok := filters[dst]
		if !ok {
			t.Errorf("the matrix has no destination %s (has %v)", dst, filters)
			continue
		}
		if got != want {
			t.Errorf("%s passes channels %016b, want %016b", dst, got, want)
		}
	}

	// full_control maps every other parameter as Notes and CCs, never on a
	// channel an instrument hears.
	for _, m := range re.Mappings(false) {
		if m.Spec.Channel+1 <= 11 && m.Spec.Channel+1 != 0 {
			t.Errorf("%s/%s is mapped on channel %d, which an instrument hears", m.Collection, m.Target, m.Spec.Channel+1)
		}
	}
}

// matrixFilters reads every destination's channel mask from a session's MIDI
// matrix.
func matrixFilters(t *testing.T, sess *aum.Session) map[string]int64 {
	t.Helper()
	a := sess.Archive()
	root, _ := a.Root().(map[string]any)
	matrix, _ := a.Deref(root["midiMatrixState"]).(map[string]any)
	if matrix == nil {
		t.Fatal("the session has no MIDI matrix")
	}
	filters, _ := a.Deref(nsValue(a, matrix, "filters")).(map[string]any)
	keys, _ := filters["NS.keys"].([]any)
	out := map[string]int64{}
	for _, k := range keys {
		name, _ := a.Deref(k).(string)
		f, _ := a.Deref(nsValue(a, filters, name)).(map[string]any)
		switch v := a.Deref(nsValue(a, f, "channelFilter")).(type) {
		case int64:
			out[name] = v
		case uint64:
			out[name] = int64(v)
		case int:
			out[name] = int64(v)
		default:
			t.Fatalf("%s's channelFilter is %T", name, v)
		}
	}
	return out
}
