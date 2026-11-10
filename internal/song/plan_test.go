package song

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/teemow/aum-session-go/aum"
	"github.com/teemow/midi-device/device"
)

func TestPlanLayout(t *testing.T) {
	p := mustPlan(t, mustParse(t, testSong))

	// Tracks take the strips after the brain and the low MIDI channels.
	for i, want := range []struct {
		id             string
		channel, strip int
	}{{"bass", 1, 1}, {"lead", 2, 2}, {"drums", 3, 3}} {
		tp := p.Tracks[i]
		if tp.ID != want.id || tp.Channel != want.channel || tp.Strip != want.strip {
			t.Errorf("track %d = %s ch%d strip %d, want %+v", i, tp.ID, tp.Channel, tp.Strip, want)
		}
	}
	if p.MasterStrip != 4 {
		t.Errorf("master strip = %d, want 4", p.MasterStrip)
	}
	// Control is every channel above the tracks.
	if len(p.ControlChannels) != 13 || p.ControlChannels[0] != 4 || p.ControlChannels[12] != 16 {
		t.Errorf("control channels = %v, want 4..16", p.ControlChannels)
	}

	// Slots, as AUM stores a strip: the instrument at 0, the output at 1, the
	// effects from 2. The master's effects follow its bus source and output.
	if n := p.Node("lead", "delay"); n == nil || n.Strip != 2 || n.Slot != 2 {
		t.Errorf("lead's first effect is at strip %d slot %d, want strip 2 slot 2", n.Strip, n.Slot)
	}
	if n := p.Node("lead", "space"); n == nil || n.Strip != 2 || n.Slot != 3 || n.MIDI {
		t.Errorf("lead's reverb is at strip %d slot %d (MIDI %v), want strip 2 slot 3 without MIDI", n.Strip, n.Slot, n.MIDI)
	}
	if n := p.Node("lead", ""); n == nil || n.Slot != 0 || !n.MIDI {
		t.Errorf("lead's instrument = %+v, want slot 0 with MIDI", n)
	}
	if n := p.Node(MasterTrack, "glue"); n == nil || n.Strip != 4 || n.Slot != 2 {
		t.Errorf("master effect = %+v, want strip 4 slot 2", n)
	}

	// Strips sit on the convention's CCs on the global channel.
	bass := p.Track("bass")
	vol, _ := device.ConventionMixerCC(1, "Volume")
	mute, _ := device.ConventionMixerCC(1, "Mute")
	if bass.Level != (Assignment{Kind: MsgCC, Channel: GlobalChannel, Number: vol}) || bass.Mute.Number != mute {
		t.Errorf("bass strip = level %s, mute %s; want the convention's CC %d and %d on channel %d", bass.Level, bass.Mute, vol, mute, GlobalChannel)
	}

	// Every other control has an address of its own, none on a track's
	// channel and none on a controller MIDI reserves.
	seen := map[Assignment]string{}
	claim := func(a Assignment, what string) {
		t.Helper()
		if other, taken := seen[a]; taken {
			t.Errorf("%s and %s share %s", what, other, a)
		}
		seen[a] = what
		if a.Channel <= 3 || a.Channel > 15 {
			t.Errorf("%s is on channel %d, outside the control channels", what, a.Channel)
		}
		if a.Kind == MsgCC && a.Channel != GlobalChannel && midiReservedCC(a.Number) {
			t.Errorf("%s uses the reserved CC %d", what, a.Number)
		}
	}
	for _, tp := range p.Tracks {
		claim(tp.Level, tp.ID+" level")
		claim(tp.Mute, tp.ID+" mute")
	}
	programs := map[int]bool{}
	for _, n := range p.allNodes() {
		claim(n.Bypass, nodeLabel(n)+" bypass")
		for _, pa := range n.Params {
			claim(pa.At, nodeLabel(n)+" "+pa.Key)
		}
		for _, pr := range n.Presets {
			if programs[pr.Program] {
				t.Errorf("program %d loads two presets", pr.Program)
			}
			programs[pr.Program] = true
		}
	}
	if len(programs) != 5 {
		t.Errorf("planned %d preset loads, want 5 (bass 1, lead 2, reverb 2)", len(programs))
	}
}

// TestPlanResolvesNames checks plugins, presets and parameters are found the
// way a person writes them, and a miss says what would have matched.
func TestPlanResolvesNames(t *testing.T) {
	ok := func(from, to string) {
		t.Helper()
		if _, err := NewPlan(mustParse(t, strings.Replace(testSong, from, to, 1)), testCatalog()); err != nil {
			t.Errorf("%q -> %q: %v", from, to, err)
		}
	}
	ok("preset: Deep Bass", "preset: deep bass")          // case
	ok("params: {Cutoff: 0.25}", "params: {cutoff: 25%}") // identifier, percentage
	ok("params: {Cutoff: 0.25}", "params: {Cutoff: 0.25, Waveform: Sine}")
	// A plugin is named by its id, by its name or by a part that fits only it.
	ok("instrument: {plugin: drums}", "instrument: {plugin: Test Drums}")
	ok("instrument: {plugin: drums}", "instrument: {plugin: drum}")
	ok("- {plugin: delay}", "- {plugin: test delay}")
	// A plugin whose identifiers are numbers is addressed by display name.
	ok("- {id: glue, plugin: reverb, bypass: true}", "- {id: glue, plugin: reverb, bypass: true, params: {mix: 0.5}}")

	for name, tc := range map[string]struct{ from, to, want string }{
		"plugin":       {"instrument: {plugin: drums}", "instrument: {plugin: drum machine}", `no staged AUv3 probe "drum machine" (did you mean: drums (Test Drums)?)`},
		"two plugins":  {"instrument: {plugin: drums}", "instrument: {plugin: test}", `"test" fits 4 plugins: delay (Test Delay), drums (Test Drums), reverb (Test Reverb), synth (Test Synth); use the id`},
		"preset":       {"preset: Deep Bass", "preset: Deep Base", `Test Synth has no factory preset "Deep Base" (did you mean: Deep Bass?)`},
		"param":        {"params: {Cutoff: 0.25}", "params: {Cutof: 0.25, Resonanz: 0.1}", `has no writable parameter "Resonanz"`},
		"read-only":    {"params: {Cutoff: 0.25}", "params: {Meter: 0.25}", `has no writable parameter "Meter"`},
		"out of range": {"params: {Cutoff: 0.25}", "params: {Cutoff: 3}", "3 is outside Cutoff's range 0..1"},
		"bad label":    {"params: {Cutoff: 0.25}", "params: {Cutoff: 0.25, Waveform: Triangle}", `"Triangle" is not a value of Waveform (its values are: Saw, Square, Sine)`},
	} {
		_, err := NewPlan(mustParse(t, strings.Replace(testSong, tc.from, tc.to, 1)), testCatalog())
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want an error containing %q", name, err, tc.want)
		}
	}
}

// TestPlanAmbiguousParam checks a display name two parameters share is
// refused with the identifiers that tell them apart.
func TestPlanAmbiguousParam(t *testing.T) {
	cat := testCatalog()
	twin := cat["delay"]
	twin.Parameters = append(twin.Parameters,
		device.ProbeParam{Identifier: "tone_l", DisplayName: "Tone", Max: 1, Writable: true},
		device.ProbeParam{Identifier: "tone_r", DisplayName: "Tone", Max: 1, Writable: true})
	cat["delay"] = twin
	doc := strings.Replace(testSong, "params: {Mix: 0.2}", "params: {Tone: 0.2}", 1)
	_, err := NewPlan(mustParse(t, doc), cat)
	if err == nil || !strings.Contains(err.Error(), `has 2 parameters called "Tone"; use the identifier (tone_l, tone_r)`) {
		t.Fatalf("NewPlan: %v, want the ambiguity reported with both identifiers", err)
	}
}

// TestPlanNeedsHomeValues checks a control a scene changes must be declared on
// the track, so the sections that leave it alone have a value to return to.
func TestPlanNeedsHomeValues(t *testing.T) {
	doc := strings.Replace(testSong, "instrument: {plugin: synth, preset: Warm Pad}", "instrument: {plugin: synth}", 1)
	doc = strings.Replace(doc, "params: {Cutoff: 80%}", "params: {Cutoff: 80%, Resonance: 0.4}", 1)
	_, err := NewPlan(mustParse(t, doc), testCatalog())
	if err == nil {
		t.Fatal("NewPlan succeeded, want the missing home values reported")
	}
	for _, want := range []string{
		"/scenes/chorus/lead/preset: lead's instrument has no preset of its own",
		`/scenes/chorus/bass/params/Resonance: bass's instrument does not declare "Resonance"`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("problems do not include %q:\n%v", want, err)
		}
	}
}

func TestParamWire(t *testing.T) {
	cutoff := device.ProbeParam{DisplayName: "Cutoff", Min: 0, Max: 1}
	hz := device.ProbeParam{DisplayName: "Freq", Min: 20, Max: 20020}
	wave := device.ProbeParam{DisplayName: "Wave", Min: 0, Max: 3, ValueStrings: []string{"Saw", "Square", "Tri", "Sine"}}
	onoff := device.ProbeParam{DisplayName: "Chorus", Min: 0, Max: 1}
	for _, tc := range []struct {
		p    device.ProbeParam
		v    Value
		want int
	}{
		{cutoff, Number(0), 0}, {cutoff, Number(1), 127}, {cutoff, Number(0.5), 64},
		{hz, Number(10020), 64}, {hz, Value{Str: "25%", IsStr: true}, 32},
		{wave, Value{Str: "saw", IsStr: true}, 0}, {wave, Value{Str: "Tri", IsStr: true}, 85}, {wave, Value{Str: "Sine", IsStr: true}, 127},
		{onoff, Value{Str: "on", IsStr: true}, 127}, {onoff, Value{Str: "false", IsStr: true}, 0},
	} {
		got, err := paramWire(tc.p, tc.v)
		if err != nil || got != tc.want {
			t.Errorf("paramWire(%s, %s) = %d, %v; want %d", tc.p.DisplayName, tc.v, got, err, tc.want)
		}
	}
	for _, v := range []Value{Number(-1), {Str: "150%", IsStr: true}, {Str: "loud", IsStr: true}} {
		if _, err := paramWire(cutoff, v); err == nil {
			t.Errorf("paramWire(Cutoff, %s) succeeded, want an error", v)
		}
	}
}

// TestPlanFillsControlChannels checks the pool moves on to the next channel
// when one is full, and that running out is an error rather than a collision.
func TestPlanFillsControlChannels(t *testing.T) {
	big := device.ProbeDump{Component: device.ProbeComponent{Type: "aumu", Subtype: "Big1", Manufacturer: "Test"}, Name: "Big"}
	params := map[string]Value{}
	for i := 0; i < 150; i++ {
		id := "p" + string(rune('a'+i/26)) + string(rune('a'+i%26))
		big.Parameters = append(big.Parameters, device.ProbeParam{Identifier: id, Max: 1, Writable: true})
		params[id] = Number(0.5)
	}
	cat := testCatalog()
	cat["big"] = big
	s := mustParse(t, testSong)
	s.Tracks[0].Instrument = Plugin{Plugin: "big", NodeState: NodeState{Params: params}}
	s.Scenes = nil
	for name, sec := range s.Sections {
		sec.Scene = nil
		s.Sections[name] = sec
	}
	p, err := NewPlan(s, cat)
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	channels := map[int]int{}
	for _, pa := range p.Node("bass", "").Params {
		channels[pa.At.Channel]++
	}
	if channels[14] == 0 || channels[13] == 0 || len(channels) != 2 {
		t.Errorf("150 parameters landed on channels %v, want them spilling from 14 into 13", channels)
	}

	// Twelve tracks leave two control channels; 300 parameters do not fit.
	for i := 150; i < 300; i++ {
		id := "q" + string(rune('a'+i/26)) + string(rune('a'+i%26))
		big.Parameters = append(big.Parameters, device.ProbeParam{Identifier: id, Max: 1, Writable: true})
		params[id] = Number(0.5)
	}
	cat["big"] = big
	for len(s.Tracks) < maxTracks {
		s.Tracks = append(s.Tracks, Track{ID: "t" + string(rune('a'+len(s.Tracks))), Instrument: Plugin{Plugin: "synth"}})
	}
	if _, err := NewPlan(s, cat); err == nil || !strings.Contains(err.Error(), "more controls than") {
		t.Errorf("NewPlan with too many controls: %v, want it refused", err)
	}
}

// TestBuildSession authors the test song's session and reads it back: the
// strips and chains, the matrix that gives every track its channel, and the
// mapping behind every control.
func TestBuildSession(t *testing.T) {
	s := mustParse(t, testSong)
	p := mustPlan(t, s)
	prepared := 0
	sess, rep, err := p.BuildSession(SessionOptions{Prepare: func(*aum.NodeSpec) error { prepared++; return nil }})
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	if prepared != 7 {
		t.Errorf("Prepare saw %d plugins, want 7", prepared)
	}
	if rep.Strips != 5 || rep.Presets != 5 {
		t.Errorf("report = %+v, want 5 strips and 5 preset loads", rep)
	}
	data, err := sess.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	re, err := aum.Open(data)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}

	chans := re.Channels()
	if len(chans) != 5 || chans[0].Kind != aum.KindMIDI || chans[4].Title != "Master" {
		t.Fatalf("strips = %d (%v…), want brain, three tracks, master", len(chans), chans[0].Kind)
	}
	if re.Title() != "Test Song" || re.Tempo() != 120 {
		t.Errorf("session = %q at %g BPM, want the song's title and tempo", re.Title(), re.Tempo())
	}
	// bass, as AUM stores it: the synth, the output, then the delay. The fader
	// carries the track's -3 dB.
	bassChain := chans[1].Nodes
	if len(bassChain) != 3 || bassChain[0].Component == nil || bassChain[0].Component.Subtype != "Syn1" ||
		bassChain[1].ArchiveDescClass != "BusDestDescription" ||
		bassChain[2].Component == nil || bassChain[2].Component.Subtype != "Dly1" {
		t.Errorf("bass chain = %+v, want instrument, output, effect", bassChain)
	}
	// lead: synth, output, delay, reverb.
	if lead := chans[2].Nodes; len(lead) != 4 || lead[1].ArchiveDescClass != "BusDestDescription" ||
		lead[3].Component == nil || lead[3].Component.Subtype != "Rvb1" {
		t.Errorf("lead chain = %+v, want instrument, output, two effects", lead)
	}
	if fl := chans[1].FaderLevel; fl == nil || *fl < 0.70 || *fl > 0.71 {
		t.Errorf("bass fader = %v, want the gain of -3 dB", fl)
	}
	if !chans[2].Muted {
		t.Error("the lead's strip is not muted, though the track declares it")
	}
	// master: bus source, output, the glue effect, the tap.
	if n := len(chans[4].Nodes); n != 4 {
		t.Errorf("master chain has %d slots, want source, output, effect, tap", n)
	}

	// Mappings: every planned control is in the session where the plan says.
	got := map[string]aum.Mapping{}
	for _, m := range re.Mappings(false) {
		got[m.Collection+"/"+m.Target] = m
	}
	check := func(path string, at Assignment) {
		t.Helper()
		m, ok := got[path]
		if !ok {
			t.Errorf("no mapping for %s", path)
			return
		}
		if m.Spec.Type != aum.SpecStateTypeCC || m.Spec.Data1 != at.Number || m.Spec.Channel != at.Channel-1 {
			t.Errorf("%s = %+v, want %s", path, m.Spec, at)
		}
	}
	bass := p.Track("bass")
	check("Channels/chan1/Channel controls/Volume", bass.Level)
	check("Channels/chan1/Channel controls/Mute", bass.Mute)
	check("Channels/chan1/slot0/_AUMNode:Bypass", p.Node("bass", "").Bypass)
	check("Channels/chan1/slot0/cutoff", p.Node("bass", "").Params[0].At)
	check("Channels/chan1/slot2/mix", p.Node("bass", "echo").Params[0].At)
	check("Channels/chan1/slot2/_AUMNode:Bypass", p.Node("bass", "echo").Bypass)
	check("Channels/chan4/slot2/_AUMNode:Bypass", p.Node(MasterTrack, "glue").Bypass)
	check("Transport/Start Play", p.Transport["Start Play"])
	tap, _ := device.ConventionTapCC(1)
	if m := got["Channels/chan4/slot3/_AUMNode:Bypass"]; !m.AutoToggle || m.Spec.Data1 != tap || m.Spec.Channel != device.TapControlChannel-1 {
		t.Errorf("master tap toggle = %+v, want the convention's cycling CC %d on the tap channel", m, tap)
	}

	// Preset loads: Program Changes on the global channel.
	loads := re.PresetLoads(2, 0)
	if len(loads) != 2 || loads[0].Name != "Warm Pad" || loads[0].Number != 1 || loads[1].Name != "Bright Lead" || loads[0].Channel != GlobalChannel-1 {
		t.Errorf("lead preset loads = %+v, want Warm Pad then Bright Lead on the global channel", loads)
	}

	// The rig the daemon derives from the session sees the song's controls.
	rig, err := aum.DeriveRig(re, "song", nil)
	if err != nil {
		t.Fatalf("DeriveRig: %v", err)
	}
	if rig.Session == nil || len(rig.Nodes) != 7 {
		t.Errorf("derived rig has %d node devices, want one per hosted plugin (7)", len(rig.Nodes))
	}
}

// TestBuildSessionOptions checks the two options that add to a session: a tap
// on every track, and mappings for everything the song itself does not use.
func TestBuildSessionOptions(t *testing.T) {
	p := mustPlan(t, mustParse(t, testSong))
	plain, plainRep, err := p.BuildSession(SessionOptions{})
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	sess, rep, err := p.BuildSession(SessionOptions{TrackTaps: true, FullControl: true})
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	if got, want := len(sess.Channels()[1].Nodes), len(plain.Channels()[1].Nodes)+1; got != want {
		t.Errorf("bass chain with a track tap has %d slots, want %d", got, want)
	}
	if rep.Banked == 0 || rep.Overflow != 0 {
		t.Errorf("full control banked %d targets with %d left over, want every target mapped", rep.Banked, rep.Overflow)
	}

	// Full control leaves the song's own mappings where the plan put them and
	// keeps every mapping off the tracks' channels.
	bassLevel := p.Track("bass").Level
	for _, m := range sess.Mappings(false) {
		if m.Spec.Channel < 3 {
			t.Fatalf("%s/%s is mapped on MIDI channel %d, a track's channel", m.Collection, m.Target, m.Spec.Channel+1)
		}
		if m.Collection == "Channels/chan1/Channel controls" && m.Target == "Volume" &&
			(m.Spec.Data1 != bassLevel.Number || m.Spec.Channel != bassLevel.Channel-1) {
			t.Errorf("full control moved the bass level to %+v", m.Spec)
		}
	}
	if len(sess.Mappings(false)) <= plainRep.Mappings {
		t.Errorf("full control has %d mappings, no more than the song's own %d", len(sess.Mappings(false)), plainRep.Mappings)
	}
}

// TestBuildSessionTapNames checks every tap of a session says which session
// and which strip it sits on, in the state the plugin restores, and that a
// session without an id is authored with the taps it always had.
func TestBuildSessionTapNames(t *testing.T) {
	p := mustPlan(t, mustParse(t, testSong))
	tapState := func(sess *aum.Session, strip, slot int) string {
		t.Helper()
		data, err := sess.Archive().Encode()
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		re, err := aum.Open(data)
		if err != nil {
			t.Fatalf("re-open: %v", err)
		}
		doc, err := re.NodeAuStateDoc(strip, slot)
		if err != nil {
			t.Fatalf("strip %d slot %d: %v", strip, slot, err)
		}
		return string(doc["probeAudioTapConfig"])
	}
	nameOf := func(state string) string {
		t.Helper()
		var cfg struct {
			Streaming bool   `json:"streaming"`
			Name      string `json:"name"`
		}
		if err := json.Unmarshal([]byte(state), &cfg); err != nil || !cfg.Streaming {
			t.Fatalf("tap state %q: %v, want a streaming tap", state, err)
		}
		return cfg.Name
	}

	sess, _, err := p.BuildSession(SessionOptions{TrackTaps: true, Session: "Songs/Test Song"})
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	seen := map[string]bool{}
	for _, tp := range p.Tracks {
		// A track's tap follows its effects.
		got := nameOf(tapState(sess, tp.Strip, firstInsertSlot+len(tp.Nodes)-1))
		if want := "Songs/Test Song/" + tp.ID; got != want {
			t.Errorf("the tap of %s is named %q, want %q", tp.ID, got, want)
		}
		seen[got] = true
	}
	master := nameOf(tapState(sess, p.MasterStrip, firstInsertSlot+len(p.Master)))
	if master != "Songs/Test Song/master" || master != TapName("Songs/Test Song", MasterTap) {
		t.Errorf("the master's tap is named %q, want Songs/Test Song/master", master)
	}
	seen[master] = true
	if len(seen) != len(p.Tracks)+1 {
		t.Errorf("the session's taps share names: %v", seen)
	}

	// Without track taps only the master's is there, named the same.
	sess, _, err = p.BuildSession(SessionOptions{Session: "Songs/Test Song"})
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	if got := nameOf(tapState(sess, p.MasterStrip, firstInsertSlot+len(p.Master))); got != master {
		t.Errorf("the master's tap without track taps is named %q, want %q", got, master)
	}

	// No session id, no name: the state a build of the plugin from before
	// taps had names was authored.
	sess, _, err = p.BuildSession(SessionOptions{TrackTaps: true})
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	for _, at := range [][2]int{{p.Tracks[0].Strip, firstInsertSlot + len(p.Tracks[0].Nodes) - 1}, {p.MasterStrip, firstInsertSlot + len(p.Master)}} {
		if got := tapState(sess, at[0], at[1]); got != `{"streaming":true}` {
			t.Errorf("a tap of a session without an id has the state %s, want only the streaming flag", got)
		}
	}
}

// TestTapName checks a tap's name splits back into its session and strip,
// whatever the two hold, and that other names are not taken for one.
func TestTapName(t *testing.T) {
	for _, c := range []struct{ session, strip, name string }{
		{"Songs/Night Drive", "master", "Songs/Night Drive/master"},
		{"Night Drive", "bass", "Night Drive/bass"},
		{"Live sets/2026/Opener", "lead 2", "Live sets/2026/Opener/lead 2"},
		// A slash in a track's id does not move the split.
		{"Songs/A", "fx/return", "Songs/A/fx%2Freturn"},
		{"Songs/A", "100%/master", "Songs/A/100%25%2Fmaster"},
	} {
		name := TapName(c.session, c.strip)
		if name != c.name {
			t.Errorf("TapName(%q, %q) = %q, want %q", c.session, c.strip, name, c.name)
		}
		session, strip, ok := ParseTapName(name)
		if !ok || session != c.session || strip != c.strip {
			t.Errorf("ParseTapName(%q) = %q, %q, %v; want %q, %q", name, session, strip, ok, c.session, c.strip)
		}
	}
	// What a tap without a name is known by, and names that lack a part.
	for _, name := range []string{"", "192.0.2.10:51234", "[2001:db8::10]:51234", "tap-1", "ProbeAudioTap", "/master", "Songs/A/"} {
		if session, strip, ok := ParseTapName(name); ok {
			t.Errorf("ParseTapName(%q) = %q, %q: not a session's tap", name, session, strip)
		}
	}
}

// TestPlanAllPresets checks a session that maps every factory preset: each
// preset has a Program Change of its own, the instruments' first, at an
// address that does not depend on which presets the song names.
func TestPlanAllPresets(t *testing.T) {
	all := testSong + "session: {presets: all}\n"
	p := mustPlan(t, mustParse(t, all))

	// bass and lead host the synth (4 presets each), drums 2, lead's reverb
	// and the master's 2 each: 14 Program Changes on the global channel.
	seen := map[[2]int]string{}
	total := 0
	for _, n := range p.allNodes() {
		if got, want := len(n.Presets), len(n.dump.FactoryPresets); got != want {
			t.Errorf("%s maps %d of its %d presets", nodeLabel(n), got, want)
		}
		for _, pr := range n.Presets {
			key := [2]int{pr.Channel, pr.Program}
			if other, taken := seen[key]; taken {
				t.Errorf("%s %q and %s share PC %d ch%d", nodeLabel(n), pr.Name, other, pr.Program, pr.Channel)
			}
			seen[key] = nodeLabel(n) + " " + pr.Name
			total++
		}
	}
	if total != 14 || p.PresetsUnmapped != 0 {
		t.Errorf("%d presets mapped, %d unmapped; want 14 and 0", total, p.PresetsUnmapped)
	}
	// Instruments come first: bass 0-3, lead 4-7, drums 8-9, then effects.
	if pr, ok := p.Node("lead", "").Preset("bright lead"); !ok || pr.Program != 6 || pr.Channel != GlobalChannel {
		t.Errorf("lead's Bright Lead = %+v (%v), want PC 6 on the global channel", pr, ok)
	}
	if pr, ok := p.Node("lead", "space").Preset("Large Hall"); !ok || pr.Program != 11 {
		t.Errorf("the reverb's Large Hall = %+v (%v), want PC 11", pr, ok)
	}
	if _, ok := p.Node("lead", "").PresetEvent("No Such Preset"); ok {
		t.Error("a preset the plugin does not have got a Program Change")
	}

	// Naming another preset changes the notes' sound, not the session.
	other := strings.Replace(all, "preset: Deep Bass", "preset: Init", 1)
	if a, b := p.Fingerprint(), mustPlan(t, mustParse(t, other)).Fingerprint(); a != b {
		t.Error("with presets: all, the session's fingerprint changed with the preset a track names")
	}
	// Without the option it does, and the option itself makes another session.
	plain := mustPlan(t, mustParse(t, testSong))
	if plain.Fingerprint() == p.Fingerprint() {
		t.Error("presets: all did not change the fingerprint")
	}
	if a, b := plain.Fingerprint(), mustPlan(t, mustParse(t, strings.Replace(testSong, "preset: Deep Bass", "preset: Init", 1))).Fingerprint(); a == b {
		t.Error("without presets: all, naming another preset left the fingerprint alone")
	}
	for _, n := range plain.allNodes() {
		for _, pr := range n.Presets {
			if pr.Channel != GlobalChannel {
				t.Errorf("%s %q is on channel %d without presets: all", nodeLabel(n), pr.Name, pr.Channel)
			}
		}
	}

	// The song plays its own presets at their new addresses.
	s := mustParse(t, all)
	tl, err := Compile(s, p)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	want, _ := p.Node("bass", "").PresetEvent("Deep Bass")
	found := false
	for _, e := range tl.Sections[0].State {
		found = found || (e.Kind == Program && e.Channel == want.Channel && e.Data1 == want.Data1)
	}
	if !found || want.Data1 != 3 {
		t.Errorf("the opening state does not load Deep Bass at %+v", want)
	}

	// The authored session holds every Preset Load.
	sess, rep, err := p.BuildSession(SessionOptions{})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if rep.Presets != 14 || len(sess.PresetLoads(2, 0)) != 4 {
		t.Errorf("session has %d preset loads (%d on the lead), want 14 and 4", rep.Presets, len(sess.PresetLoads(2, 0)))
	}
}

// TestPlanAllPresetsOverflow checks presets spill over into the control
// channels below the global one, that what does not fit is counted, and that
// a preset the song names must fit.
func TestPlanAllPresetsOverflow(t *testing.T) {
	cat := testCatalog()
	big := cat["synth"]
	big.FactoryPresets = nil
	for i := 0; i < 900; i++ {
		big.FactoryPresets = append(big.FactoryPresets, device.ProbePreset{Number: i, Name: "P" + strconv.Itoa(i)})
	}
	cat["synth"] = big
	doc := `
title: Lab
tempo: 120
session: {presets: all}
tracks:
  - {id: a, instrument: {plugin: synth, preset: P5}}
  - {id: b, instrument: {plugin: synth, preset: PRESET}}
patterns: {rest: {bars: 1, notes: "."}}
sections: {idle: {bars: 1}}
arrangement: [idle]
`
	s := mustParse(t, strings.Replace(doc, "PRESET", "P100", 1))
	p, err := NewPlan(s, cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	// Two tracks leave the channels 3..15 for Program Changes: 13 x 128.
	room := 13 * 128
	if got := len(p.Node("a", "").Presets) + len(p.Node("b", "").Presets); got != room || p.PresetsUnmapped != 1800-room {
		t.Errorf("%d presets mapped, %d unmapped; want %d and %d", got, p.PresetsUnmapped, room, 1800-room)
	}
	// Track a's presets fill channel 15 and go on below it.
	if pr, _ := p.Node("a", "").Preset("P128"); pr.Channel != 14 || pr.Program != 0 {
		t.Errorf("a's P128 = PC %d ch%d, want PC 0 on channel 14", pr.Program, pr.Channel)
	}
	if pr, _ := p.Node("b", "").Preset("P100"); pr.Channel != 8 || pr.Program != (900+100)%128 {
		t.Errorf("b's P100 = PC %d ch%d, want PC %d on channel 8", pr.Program, pr.Channel, (900+100)%128)
	}
	// A preset the song names beyond the room is an error, not a silent gap.
	_, err = NewPlan(mustParse(t, strings.Replace(doc, "PRESET", "P899", 1)), cat)
	if err == nil || !strings.Contains(err.Error(), `b's instrument loads "P899"`) || !strings.Contains(err.Error(), "room for 1664") {
		t.Errorf("a named preset beyond the room: %v", err)
	}
	if _, err := Parse([]byte(strings.Replace(doc, "presets: all", "presets: most", 1))); err == nil {
		t.Error("an unknown presets option was accepted")
	}
}

// TestPlanAllPresetsSameName checks a plugin that lists two presets under one
// name gets one Preset Load for it: a session holds one per name.
func TestPlanAllPresetsSameName(t *testing.T) {
	cat := testCatalog()
	twice := cat["drums"]
	twice.FactoryPresets = append(twice.FactoryPresets, device.ProbePreset{Number: 2, Name: "Kit 808"}, device.ProbePreset{Number: 3, Name: "Kit Lo-Fi"})
	cat["drums"] = twice
	p, err := NewPlan(mustParse(t, testSong+"session: {presets: all}\n"), cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	n := p.Node("drums", "")
	if got := n.FactoryPresets(); len(got) != 3 || len(n.Presets) != 3 || p.PresetsUnmapped != 0 {
		t.Fatalf("drums: presets %v, %d mapped, %d unmapped; want three names, each mapped once", got, len(n.Presets), p.PresetsUnmapped)
	}
	if pr, _ := n.Preset("Kit 808"); pr.Number != 0 {
		t.Errorf("Kit 808 loads preset number %d, want the first of the two (0)", pr.Number)
	}
	if _, _, err := p.BuildSession(SessionOptions{}); err != nil {
		t.Fatalf("build: %v", err)
	}
}

// TestTrackHomeState checks one track's own sound is its strip and its
// plugins, and nothing of another track.
func TestTrackHomeState(t *testing.T) {
	p := mustPlan(t, mustParse(t, testSong))
	evs, err := p.TrackHomeState("bass")
	if err != nil {
		t.Fatalf("track home state: %v", err)
	}
	// Deep Bass, then the instrument's bypass and Cutoff, the echo's bypass
	// and Mix, and the strip's level and mute.
	if len(evs) != 7 || evs[0].Kind != Program {
		t.Fatalf("bass home state = %v", controls(evs))
	}
	lead, _ := p.TrackHomeState("lead")
	for _, e := range lead {
		for _, b := range evs {
			if e == b {
				t.Errorf("lead's and bass's home state share %s", describe(e))
			}
		}
	}
}
