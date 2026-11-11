package mcpserver

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teemow/aum-session-go/aum"
	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/mcp-midi-controller/internal/juce"
	"github.com/teemow/mcp-midi-controller/internal/song"
	"github.com/teemow/midi-device/device"
)

// stageBattalion stages a stand-in for UA Battalion's probe.
func stageBattalion(t *testing.T) {
	t.Helper()
	dump := `{"component": {"type": "aumu", "subtype": "uaBL", "manufacturer": "UnAu"}, "name": "UA Battalion",
		"parameters": [{"address": 0, "identifier": "seqChan1density", "displayName": "Seq. Chan. 1: Density", "min": 0, "max": 1, "value": 0.5, "writable": true}]}`
	if err := os.WriteFile(filepath.Join(config.AUv3ProbesDir(), "aumu-uaBL-UnAu.json"), []byte(dump), 0o644); err != nil {
		t.Fatal(err)
	}
}

// battalionState is a stand-in for the state of Battalion's default patch:
// voice 1's steps and PROB lane and the parameters of their lengths.
func battalionState(decay string) []byte {
	zeros := strings.TrimSuffix(strings.Repeat("0,", 64), ",")
	halves := strings.TrimSuffix(strings.Repeat("0.5,", 64), ",")
	return juce.EncodeBinaryXML(`<?xml version="1.0" encoding="UTF-8"?>` + "\n\n" +
		`<state presetName="Default"><PARAM id="drum1env1decay" value="` + decay + `"/>` +
		`<PARAM id="seqChan1seqLength" value="15.0"/><PARAM id="seqChan1lockLength" value="1.0"/><PARAM id="seqChan1probSeqLength" value="15.0"/><PARAM id="seqChan1probSeqMode" value="0.0"/>` +
		`<sequences fills0="` + zeros + `" probabilities0="` + halves + `"/></state>`)
}

// writeState stores a state for Battalion as a default or base state.
func writeState(t *testing.T, dir string, doc map[string][]byte) {
	t.Helper()
	def := device.AUv3DefaultState{Component: song.BattalionComponent, Name: "UA Battalion", State: map[string]device.StateEntry{}}
	for k, v := range doc {
		def.State[k] = device.StateEntry{Base64: base64.StdEncoding.EncodeToString(v)}
	}
	if err := writeAUv3DefaultState(filepath.Join(dir, "aumu-uaBL-UnAu.yaml"), def); err != nil {
		t.Fatal(err)
	}
}

const sequenceSong = `title: Lanes
tempo: 120
tracks:
  - id: drums
    instrument:
      plugin: UA Battalion
      sequence:
        1:
          steps: "x... x... x... x..."
          prob: {values: "0.5 0.5 0.5 0.5 0.5 0.5 0 0 0 0 0 0", mode: free}
patterns:
  rest: {bars: 1, notes: "."}
sections:
  a: {bars: 1, play: {drums: rest}}
arrangement: [a]
`

// authoredBattalion authors the sequence song and returns the Battalion
// node's saved state as the staged session has it.
func authoredBattalion(t *testing.T, s *Server) (map[string][]byte, string) {
	t.Helper()
	res := call(t, s.handleAuthorSongSession, map[string]any{"song": "lanes"})
	if res.IsError {
		return nil, resultText(res)
	}
	sess, err := aum.OpenFile(filepath.Join(config.AUMSessionsDir(), "Songs", "Lanes.aumproj"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := sess.NodeAuStateDoc(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	return doc, ""
}

func TestAuthorSongStepSequence(t *testing.T) {
	s := newSongServer(t)
	stageBattalion(t)
	res := call(t, s.handleSaveSong, map[string]any{"yaml": sequenceSong})
	if res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	// The song says at once that the rig has nothing to write it over.
	if text := resultText(res); !strings.Contains(text, "its own sequence, written into UA Battalion's saved state") ||
		!strings.Contains(text, "warning: the rig has no base state of UA Battalion") {
		t.Errorf("save does not tell of the sequence and the missing base state:\n%s", text)
	}

	// Without a state to write over, the author says how to get one.
	if _, msg := authoredBattalion(t, s); !strings.Contains(msg, "capture_auv3_default_state {session_id, channel, slot, base: true}") {
		t.Fatalf("author without a base state: %q", msg)
	}

	// The base state: the sequence is written over it, and the node gets
	// the XML alone.
	writeState(t, config.AUv3BaseStatesDir(), map[string][]byte{"jucePluginState": battalionState("0.21"), "data": {1, 2}})
	if text := resultText(call(t, s.handleGetSong, map[string]any{"song": "lanes"})); strings.Contains(text, "warning: the rig has no base state") {
		t.Errorf("get_song still misses the base state:\n%s", text)
	}
	doc, msg := authoredBattalion(t, s)
	if msg != "" {
		t.Fatalf("author: %s", msg)
	}
	text := stateText(t, doc)
	for _, want := range []string{
		`fills0="1,0,0,0,1,0,0,0,1,0,0,0,1,0,0,0,0,`,
		`probabilities0="0.5,0.5,0.5,0.5,0.5,0.5,0,0,0,0,0,0,0.5,`,
		`<PARAM id="seqChan1probSeqLength" value="11.0"/>`,
		`<PARAM id="seqChan1probSeqMode" value="1.0"/>`,
		`<PARAM id="seqChan1lockLength" value="0.0"/>`,
		`<PARAM id="drum1env1decay" value="0.21"/>`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the authored state lacks %s:\n%s", want, text)
		}
	}

	// A default state of the rig is what every Battalion starts as: the
	// sequence is written over it rather than over the base.
	writeState(t, config.AUv3DefaultStatesDir(), map[string][]byte{"jucePluginState": battalionState("0.42"), "data": {3}})
	doc, msg = authoredBattalion(t, s)
	if msg != "" {
		t.Fatalf("author: %s", msg)
	}
	if text := stateText(t, doc); !strings.Contains(text, `value="0.42"`) || !strings.Contains(text, `fills0="1,0,0,0,1`) {
		t.Errorf("the sequence was not written over the default state:\n%s", text)
	}
}

// stateText checks the node's state is Battalion's XML alone and returns it.
func stateText(t *testing.T, doc map[string][]byte) string {
	t.Helper()
	if len(doc) != 1 || doc["jucePluginState"] == nil {
		keys := []string{}
		for k := range doc {
			keys = append(keys, k)
		}
		t.Fatalf("the node's state has %v, want jucePluginState alone", keys)
	}
	text, err := juce.DecodeBinaryXML(doc["jucePluginState"])
	if err != nil {
		t.Fatal(err)
	}
	return text
}
