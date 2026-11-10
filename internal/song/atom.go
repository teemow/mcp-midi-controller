package song

// This file encodes a sequence as the saved state of Atom | Piano Roll², the
// AUv3 MIDI sequencer a song's session can host. Its state is a dictionary of
// JSON values, one per key, which a session file carries as the node's saved
// state; AUM hands it to the plugin when the session loads.
//
// The song is one clip: one pattern as long as the sequence, every track's
// notes on the track's own MIDI channel, and every control change as a raw
// MIDI event. Positions are in sixteenth notes from the clip's start.
//
// Atom keeps the clip twice. Its engine plays "patterns"; its window draws
// "store", the editor's own state, and never looks at "patterns". Both are
// written from the same sequence, so the window shows what plays.

import (
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/teemow/aum-session-go/aum"
	"github.com/teemow/midi-device/device"
)

// AtomComponent identifies Atom | Piano Roll² (Audio Veek).
var AtomComponent = device.ProbeComponent{Type: "aumi", Subtype: "apr2", Manufacturer: "VEEK"}

const (
	// atomTicks is the length of Atom's time unit, a sixteenth note.
	atomTicks = PPQ / 4
	// atomStateVersion is the version number Atom stamps on its own saved
	// state. It refuses a state without it ("saved in the beta version").
	atomStateVersion = 116
	// atomPatternID is the id Atom gives a clip's first pattern.
	atomPatternID = "initial"
)

// atomNote and atomEvent are a note and a raw MIDI message of an Atom pattern.
type atomNote struct {
	Time     float64 `json:"time"`
	Duration float64 `json:"duration"`
	Channel  int     `json:"channel"` // 0-based
	Pitch    int     `json:"pitch"`
	Velocity int     `json:"velocity"`
}

type atomEvent struct {
	Time  float64 `json:"time"`
	Byte1 int     `json:"byte1"`
	Byte2 int     `json:"byte2"`
	Byte3 int     `json:"byte3"`
}

type atomInterval struct {
	Begin float64 `json:"begin"`
	End   float64 `json:"end"`
}

type atomPattern struct {
	ID       string       `json:"id"`
	Interval atomInterval `json:"interval"`
	Notes    []atomNote   `json:"notes"`
	Events   []atomEvent  `json:"events"`
}

// atomTime converts a tick position into Atom's sixteenths.
func atomTime(tick int64) float64 { return float64(tick) / atomTicks }

// atomPatternOf renders the sequence as one Atom pattern.
func atomPatternOf(q *Sequence) atomPattern {
	p := atomPattern{
		ID:       atomPatternID,
		Interval: atomInterval{Begin: 0, End: atomTime(q.Ticks)},
		Notes:    make([]atomNote, 0, len(q.Notes)),
		Events:   make([]atomEvent, 0, len(q.Controls)),
	}
	for _, n := range q.Notes {
		p.Notes = append(p.Notes, atomNote{
			Time: atomTime(n.Tick), Duration: atomTime(n.Ticks),
			Channel: n.Channel - 1, Pitch: n.Note, Velocity: n.Velocity,
		})
	}
	for _, e := range q.Controls {
		ch := (e.Channel - 1) & 0x0F
		ev := atomEvent{Time: atomTime(e.Tick)}
		if e.Kind == Program {
			ev.Byte1, ev.Byte2 = 0xC0|ch, e.Data1
		} else {
			ev.Byte1, ev.Byte2, ev.Byte3 = 0xB0|ch, e.Data1, e.Data2
		}
		p.Events = append(p.Events, ev)
	}
	return p
}

// atomUUID derives a stable UUID from a name, so authoring the same song
// twice writes the same state.
func atomUUID(name string) string {
	h := sha1.Sum([]byte("mcp-midi-controller/atom/" + name))
	h[6] = h[6]&0x0F | 0x50 // version 5
	h[8] = h[8]&0x3F | 0x80 // RFC 4122 variant
	return strings.ToUpper(fmt.Sprintf("%x-%x-%x-%x-%x", h[0:4], h[4:6], h[6:8], h[8:10], h[10:16]))
}

// AtomState encodes the sequence as Atom's saved state: the clip, launched,
// played once from the transport's start on every channel. name is what the
// clip is called; strip and slot are where the session hosts the plugin.
func (q *Sequence) AtomState(name string, strip, slot int) (map[string][]byte, error) {
	channels := make([]bool, 16)
	for i := range channels {
		channels[i] = true
	}
	pattern := atomPatternOf(q)
	id := atomUUID(name)
	values := map[string]any{
		"id":             id,
		"name":           name,
		"trackIndex":     strip,
		"slotIndex":      slot,
		"colorIndex":     0,
		"patterns":       []atomPattern{pattern},
		"store":          atomStore(q, pattern, name, id, strip, slot),
		"pending":        []any{},
		"activePattern":  0,
		"playingPattern": 0,
		"subdivision":    16,
		"contentOffset":  []int{0, 0},
		"zoomScale":      0.25,

		// The clip is armed, so it runs with the transport, and plays through
		// once: the song must not start over when it has ended.
		"isLaunched":         true,
		"isLooped":           false,
		"isOneShotMode":      false,
		"isStopWithHostMode": false,
		"isContiguousMode":   false,
		"isLoopLatch":        false,
		"isLoopLegato":       false,
		"quantum":            map[string]int{"start": 4, "stop": 4},
		"chokeMode":          0,

		// Nothing is recorded into the clip and nothing passes through it.
		"isRecording":     false,
		"isStepInputting": false,
		"isMonitoring":    false,
		"isMuted":         false,

		// The notes are played as written.
		"isQuantizing":             false,
		"quantizeNoteOns":          false,
		"quantizeNoteOffs":         false,
		"quantizeNoteDurations":    false,
		"outputChannels":           channels,
		"outputIsFunneled":         false,
		"outputLatency":            0,
		"outputProbability":        1,
		"outputSwing":              0,
		"outputTempo":              1,
		"outputTransposeOctaves":   0,
		"outputTransposeSemitones": 0,
		"scaleMask":                []int{},
		"scaleShiftMode":           1,
		// A control change must stay where the song left it.
		"isSnapbackAutomation": false,

		// No note, controller or program is set to launch the clip (-1); the
		// rest of the launch settings are the plugin's own defaults.
		"noteTrigger":        -1,
		"ccTrigger":          -1,
		"pcTrigger":          -1,
		"isNoteOnTrigger":    true,
		"isNoteOnRetrigger":  false,
		"isNoteOnRelease":    true,
		"isNoteOffTrigger":   false,
		"isNoteOffRetrigger": false,
		"isNoteOffRelease":   false,
		"switchSource":       2,
		"switchChannel":      15,
		"switchNote":         0,
		"switchCc":           0,
		"switchOffset":       0,
		"switchMultiplier":   1,
	}
	out := make(map[string][]byte, len(values))
	for k, v := range values {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("encode the sequencer's %s: %w", k, err)
		}
		out[k] = b
	}
	return out, nil
}

// atomStore renders the editor's state: what Atom's window shows. It is not
// JSON but the editor's own serialization, a JavaScript object literal with
// Map and Set constructors. Everything but the clip is the editor's defaults.
func atomStore(q *Sequence, p atomPattern, name, id string, strip, slot int) string {
	sixteenthsPerBar := atomTime(q.BarTicks)
	bars := 1
	if sixteenthsPerBar > 0 {
		bars = max(1, int(p.Interval.End/sixteenthsPerBar+0.999))
	}
	num, den := q.MeterNum, q.MeterDen
	if num <= 0 || den <= 0 {
		num, den = 4, 4
	}
	all := func(v string) string { return "[" + strings.TrimSuffix(strings.Repeat(v+",", 16), ",") + "]" }
	num64 := func(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
	str := func(s string) string { b, _ := json.Marshal(s); return string(b) }

	var notes, order, events strings.Builder
	for i, n := range p.Notes {
		if i > 0 {
			notes.WriteByte(',')
			order.WriteByte(',')
		}
		nid := fmt.Sprintf(`"n%05d"`, i)
		t, d := num64(n.Time), num64(n.Duration)
		fmt.Fprintf(&notes, `[%s,{"channel":%d,"quantized":{"time":%s,"duration":%s,"pitch":%d,"velocity":%d},"unquantized":{"time":%s,"duration":%s,"pitch":%d,"velocity":%d},"swinged":{"time":%s,"duration":%s},"jittered":{"velocity":%d},"bias":{"time":0,"duration":0,"pitch":0,"velocity":0,"swing":{"time":0,"duration":0},"jitter":{"velocity":0},"offset":{"velocity":0.5}}}]`,
			nid, n.Channel, t, d, n.Pitch, n.Velocity, t, d, n.Pitch, n.Velocity, t, d, n.Velocity)
		order.WriteString(nid)
	}
	for i, e := range p.Events {
		if i > 0 {
			events.WriteByte(',')
		}
		fmt.Fprintf(&events, `["e%05d",{"time":%s,"data":[%d,%d,%d]}]`, i, num64(e.Time), e.Byte1, e.Byte2, e.Byte3)
	}

	var b strings.Builder
	b.WriteString(`{"automation":{"tool":"select","views":{"show":"velocity"}},`)
	fmt.Fprintf(&b, `"channels":{"mode":0,"visible":%s,"locked":%s},`, all("true"), all("false"))
	b.WriteString(`"favorite":{"scales":new Set([])},"grid":{"snap":true},"history":{"canUndo":false,"canRedo":false},"host":{"transportStateFlags":0},`)
	b.WriteString(`"input":{"rec":false,"step":false,"monitoring":false},"instances":{"map":new Map([])},"latency":{"millis":0},`)
	b.WriteString(`"layers":{"visible":new Set([]),"clipRepeats":new Map([]),"pitchOffsets":new Map([])},`)
	b.WriteString(`"layout":{"toolbarThickness":44,"menuThickness":308,"notificationHeight":22,"headerHeight":22,"barsButtonsWidth":44,"barsButtonsHeight":110,"barsButtonsPaddingTop":44,"barsButtonsPaddingBottom":22,"keybedWidth":44,"wholeNoteWidth":264,"octaveHeight":264,"clipThumbnailHeight":88,"clipThumbnailHeaderHeight":11,"backgroundBarCount":8},`)
	fmt.Fprintf(&b, `"meta":{"generation":%s,"id":%s,"name":%s,"track":%d,"slot":%d,"color":0,"render":0},`, str(atomUUID(name+"/generation")), str(id), str(name), strip, slot)
	b.WriteString(`"clipQuantizeToolbar":{"noteOns":true,"noteOffs":false,"noteDurations":false,"snap":true,"strength":1},`)
	b.WriteString(`"clipSwingToolbar":{"negative":false,"link":false,"snap":true,"timeStrength":0,"durationStrength":0},`)
	b.WriteString(`"clipRepitchToolbar":{"shiftMode":"down","snap":true,"strength":1},`)
	b.WriteString(`"clipJitterVelocityToolbar":{"snapOffset":true,"offset":0.5,"snapStrength":true,"strength":0},`)
	b.WriteString(`"notification":{"message":null,"type":"info","level":0},"octaves":{"begin":-1,"end":10},`)
	fmt.Fprintf(&b, `"output":{"channels":%s,"funneled":false,"muted":false},`, all("true"))
	b.WriteString(`"panels":{"clipMeta":{"visible":false},"channels":{"visible":false},"entry":{"visible":false},"quantize":{"visible":false},"grid":{"visible":false},"time":{"visible":false},"sync":{"visible":false},"scales":{"visible":false},"layers":{"visible":false},"snippets":{"visible":false},"phrases":{"visible":false},"clipQuantize":{"visible":false},"clipSwing":{"visible":false},"clipRepitch":{"visible":false},"clipJitterVelocity":{"visible":false},"mainMenu":{"visible":false},"move":{"visible":false},"mutate":{"visible":false},"automation":{"visible":false,"height":{"percentage":61.8,"computed":0}}},`)
	fmt.Fprintf(&b, `"phrases":{"active":%[1]s,"playing":%[1]s,"map":new Map([[%[1]s,{"bars":%[2]d,"grid":{"subdivision":16},"timeSignature":{"numerator":%[3]d,"denominator":%[4]d},"interval":{"begin":%[5]s,"end":%[6]s},"notes":new Map([%[7]s]),"orderings":new Set([%[8]s]),"selections":new Set([]),"events":new Map([%[9]s])}]]),"orderings":new Set([%[1]s])},`,
		str(p.ID), bars, num, den, num64(p.Interval.Begin), num64(p.Interval.End), notes.String(), order.String(), events.String())
	b.WriteString(`"playback":{"launched":true,"looped":false},"playhead":{"time":0},"probability":{"factor":1},`)
	b.WriteString(`"quantize":{"enabled":false,"noteOns":false,"noteOffs":false,"noteDurations":false},`)
	b.WriteString(`"quantum":{"chokeMode":0,"noteTrigger":-1,"ccTrigger":-1,"pcTrigger":-1,"noteOnTrigger":true,"noteOnRetrigger":false,"noteOnRelease":true,"noteOffTrigger":false,"noteOffRetrigger":false,"noteOffRelease":false,"startBeat":4,"stopBeat":4,"contiguous":false,"oneShot":false,"stopWithHost":false,"loopLegato":false,"loopLatch":false,"snapbackAutomation":false},`)
	b.WriteString(`"scale":{"tonic":"C","chroma":"","spelling":"traditional","shiftMode":"down"},"sequence":{"tool":"select","notes":{"audit":false,"channel":0,"velocity":100}},`)
	b.WriteString(`"pending":new Map([]),"snippets":{"map":new Map([])},"swing":{"mix":0},`)
	b.WriteString(`"switch":{"source":2,"noteNumber":0,"ccNumber":0,"noteChannel":0,"ccChannel":0,"pcChannel":15,"offset":0,"multiplier":1},`)
	b.WriteString(`"temporary":{"sequence":{"isSelectingImmediate":false,"isDraggingSelection":false,"isResizingSelection":false,"drag":{"time":0,"pitch":0},"resize":{"begin":0,"end":0}},"automation":{"isDraggingSelection":false,"isResizingSelection":false}},`)
	b.WriteString(`"tempo":{"multiplier":1},"transpose":{"semitones":0},"zoom":{"x":1,"y":1}}`)
	return b.String()
}

// stampAtomVersion writes atomStateVersion into the saved state of the node at
// strip and slot. A session's saved plugin state is the plugin's own
// dictionary; the session library authors its entries as data and stamps a
// version of its own, where Atom expects this number.
func stampAtomVersion(sess *aum.Session, strip, slot int) error {
	a := sess.Archive()
	fail := func(what string) error {
		return fmt.Errorf("the session has no %s for the sequencer at strip %d slot %d", what, strip, slot)
	}
	root, _ := a.Root().(map[string]any)
	strips := nsObjects(a, root["nodeArchives"])
	if strip < 0 || strip >= len(strips) {
		return fail("strip")
	}
	nodes := nsObjects(a, strips[strip])
	if slot < 0 || slot >= len(nodes) {
		return fail("node")
	}
	node, _ := a.Deref(nodes[slot]).(map[string]any)
	state, _ := a.Deref(node["archiveNodeState"]).(map[string]any)
	doc, _ := a.Deref(nsValue(a, state, "AuStateDoc")).(map[string]any)
	keys, _ := doc["NS.keys"].([]any)
	objs, _ := doc["NS.objects"].([]any)
	if doc == nil || len(keys) != len(objs) {
		return fail("saved state")
	}
	version := a.NewBuilder().Intern(uint64(atomStateVersion))
	for i, k := range keys {
		if name, _ := a.Deref(k).(string); name == "version" {
			objs[i] = version
			return nil
		}
	}
	return fail("state version")
}

// nsObjects returns the elements of an archived NSArray.
func nsObjects(a *aum.Archive, v any) []any {
	arr, _ := a.Deref(v).(map[string]any)
	objs, _ := arr["NS.objects"].([]any)
	return objs
}

// nsValue returns the value an archived NSDictionary holds for key, nil when
// it has none.
func nsValue(a *aum.Archive, dict map[string]any, key string) any {
	keys, _ := dict["NS.keys"].([]any)
	objs, _ := dict["NS.objects"].([]any)
	for i, k := range keys {
		if name, _ := a.Deref(k).(string); name == key && i < len(objs) {
			return objs[i]
		}
	}
	return nil
}
