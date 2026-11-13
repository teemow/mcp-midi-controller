package song

import (
	"strings"
	"testing"

	"github.com/teemow/midi-device/device"

	"github.com/teemow/mcp-midi-controller/internal/juce"
)

// kitSong is a song whose drums are a Battalion with samples and locks and
// no sequence.
const kitSong = `title: Kit
tempo: 120
tracks:
  - id: drums
    kind: drums
    instrument:
      plugin: battalion
      params: {drum1samplerenabled: "On"}
      samples:
        1: Kicks/Abstrakta 1 Kick.wav
        3: "file:///private/var/mobile/Containers/Data/Application/D/Documents/my%20hat.wav"
        4: ""
      locks: [drum1pitch, drum1pan]
patterns:
  beat: {bars: 1, drums: {kick: "x..."}}
sections:
  a: {bars: 2, play: {drums: beat}}
arrangement: [a]
`

// kitCatalog is battalionCatalog with the parameters kitSong sets and locks.
func kitCatalog() Catalog {
	cat := battalionCatalog()
	d := cat["battalion"]
	for _, id := range []string{"drum1samplerenabled", "drum1pitch", "drum1pan", "drum2pan"} {
		d.Parameters = append(d.Parameters, device.ProbeParam{Identifier: id, DisplayName: id, KeyPath: id, Min: 0, Max: 1, Writable: true})
	}
	cat["battalion"] = d
	return cat
}

func planKit(t *testing.T, doc string) *Plan {
	t.Helper()
	p, err := NewPlan(mustParse(t, doc), kitCatalog())
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	return p
}

func TestKitState(t *testing.T) {
	n := planKit(t, kitSong).Node("drums", "")
	if !n.WritesState() {
		t.Fatal("the drums write nothing into their state")
	}
	if got, want := n.StateWrites(), "3 samples and 2 locks"; got != want {
		t.Errorf("StateWrites = %q, want %q", got, want)
	}
	doc, err := n.AuthoredState(map[string][]byte{battalionStateKey: battalionBase()})
	if err != nil {
		t.Fatal(err)
	}
	root := readState(t, doc)
	// The URL as Battalion itself writes it for a sample of its library,
	// through a bundle id that stands for any.
	for attr, want := range map[string]string{
		"url_0": "file:///private/var/containers/Bundle/Application/00000000-0000-0000-0000-000000000000/UA%20Battalion.app/PlugIns/UA%20Battalion.appex/Unfiltered%20Audio/Sample%20Library/Kicks/Abstrakta%201%20Kick.wav",
		"url_2": "file:///private/var/mobile/Containers/Data/Application/D/Documents/my%20hat.wav",
		"url_3": "",
	} {
		if got := root.SelectAttrValue(attr, "(none)"); got != want {
			t.Errorf("%s = %q, want %q", attr, got, want)
		}
	}
	// A voice the song gives no sample keeps the base's.
	if got := root.SelectAttrValue("url_1", "(none)"); got != "(none)" {
		t.Errorf("url_1 = %q, want it left alone", got)
	}
	locks := root.SelectElement("locks")
	if locks == nil {
		t.Fatal("no <locks>")
	}
	var got []string
	for _, a := range locks.Attr {
		got = append(got, a.Key+"="+a.Value)
	}
	if strings.Join(got, " ") != "drum1pan=1 drum1pitch=1" {
		t.Errorf("<locks> holds %v", got)
	}
	// No sequence: the sequence and its parameters stay as the base has them.
	if got := paramValue(root, "seqChan1lockLength"); got != "1.0" {
		t.Errorf("SNAP of voice 1 became %s", got)
	}
	if got := strings.Join(attrValues(t, root, "fills0")[:4], " "); got != "0 0 0 0" {
		t.Errorf("voice 1's steps = %s", got)
	}
}

// TestKitStateLocksElement checks a state without <locks> gets one, before
// <sequences>, and a state's own locks stay.
func TestKitStateLocksElement(t *testing.T) {
	base := func(locks string) map[string][]byte {
		text, err := juce.DecodeBinaryXML(battalionBase())
		if err != nil {
			t.Fatal(err)
		}
		text = strings.Replace(text, "<locks/>", locks, 1)
		return map[string][]byte{battalionStateKey: juce.EncodeBinaryXML(text)}
	}
	n := planKit(t, kitSong).Node("drums", "")

	doc, err := n.AuthoredState(base(""))
	if err != nil {
		t.Fatal(err)
	}
	root := readState(t, doc)
	locks := root.SelectElement("locks")
	if locks == nil || locks.SelectAttrValue("drum1pitch", "") != "1" {
		t.Fatalf("no <locks> with drum1pitch in a state that had none")
	}
	var tags []string
	for _, c := range root.ChildElements() {
		if c.Tag != "PARAM" {
			tags = append(tags, c.Tag)
		}
	}
	if got := strings.Join(tags, " "); got != "locks sequences" {
		t.Errorf("the state's elements are %s, want locks before sequences", got)
	}

	doc, err = n.AuthoredState(base(`<locks drum2pan="1"/>`))
	if err != nil {
		t.Fatal(err)
	}
	if l := readState(t, doc).SelectElement("locks"); l.SelectAttrValue("drum2pan", "") != "1" || l.SelectAttrValue("drum1pan", "") != "1" {
		t.Errorf("the state's own lock or the song's is missing")
	}
}

func TestKitStateChecks(t *testing.T) {
	for name, tc := range map[string]struct{ from, to, want string }{
		"voice":      {"        4: \"\"\n", "        9: x.wav\n", `/tracks/0/instrument/samples/9: "9" is not a voice (1..8)`},
		"absolute":   {"1: Kicks/Abstrakta 1 Kick.wav", "1: /Kicks/Abstrakta 1 Kick.wav", `/instrument/samples/1: "/Kicks/Abstrakta 1 Kick.wav": a sample of the factory library is its path there`},
		"up":         {"1: Kicks/Abstrakta 1 Kick.wav", "1: Kicks/../x.wav", `"Kicks/../x.wav": ".." is not a folder or file of the factory library`},
		"scheme":     {"1: Kicks/Abstrakta 1 Kick.wav", "1: https://example.com/x.wav", "or a file:// URL"},
		"empty lock": {"[drum1pitch, drum1pan]", `[drum1pitch, ""]`, "/instrument/locks/1: a lock is the id of a parameter"},
		"twice":      {"[drum1pitch, drum1pan]", "[drum1pitch, drum1pitch]", "/instrument/locks/1: drum1pitch is locked twice"},
		"in a scene": {"sections:", "scenes:\n  s: {drums: {locks: [drum1pan]}}\nsections:", "field locks not found"},
	} {
		doc := strings.Replace(kitSong, tc.from, tc.to, 1)
		if doc == kitSong {
			t.Fatalf("%s: the song no longer contains %q", name, tc.from)
		}
		_, err := Parse([]byte(doc))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
	// Neither on an effect nor on a file.
	for name, tc := range map[string]struct{ from, to, want string }{
		"effect samples": {"    kind: drums\n", "    kind: drums\n    effects: [{plugin: delay, samples: {1: x.wav}}]\n", "/tracks/0/effects/0/samples: only an instrument has samples"},
		"effect locks":   {"    kind: drums\n", "    kind: drums\n    effects: [{plugin: delay, locks: [x]}]\n", "/tracks/0/effects/0/locks: only an instrument has locks"},
	} {
		if _, err := Parse([]byte(strings.Replace(kitSong, tc.from, tc.to, 1))); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
}

func TestKitStatePlanChecks(t *testing.T) {
	for name, tc := range map[string]struct{ from, to, want string }{
		"plugin":       {"plugin: battalion\n      params: {drum1samplerenabled: \"On\"}", "plugin: drums", "/tracks/0/instrument: Test Drums has no step sequence, samples or locks a song can write; only UA Battalion does"},
		"preset":       {"plugin: battalion", "plugin: battalion\n      preset: Rebound", `a preset replaces the whole kit with its own sequence, samples and locks; leave out the preset ("Rebound") of an instrument with 3 samples and 2 locks`},
		"scene preset": {"sections:", "scenes:\n  s: {drums: {preset: Rebound}}\nsections:", "/scenes/s/drums/preset: a preset replaces the whole kit"},
		"lock":         {"[drum1pitch, drum1pan]", "[drum1pitch, drum9pan]", "/tracks/0/instrument/locks: drum9pan is not a parameter of UA Battalion"},
	} {
		doc := strings.Replace(kitSong, tc.from, tc.to, 1)
		_, err := NewPlan(mustParse(t, doc), kitCatalog())
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
}

// TestKitStateFingerprint checks another sample or lock is another session,
// and a sequence alone keeps the fingerprint it had.
func TestKitStateFingerprint(t *testing.T) {
	a := planKit(t, kitSong).Fingerprint()
	for name, doc := range map[string]string{
		"sample": strings.Replace(kitSong, "Abstrakta 1 Kick", "Abstrakta 2 Kick", 1),
		"lock":   strings.Replace(kitSong, "[drum1pitch, drum1pan]", "[drum1pitch]", 1),
		"none":   strings.Replace(kitSong, "        4: \"\"\n", "", 1),
	} {
		if planKit(t, doc).Fingerprint() == a {
			t.Errorf("another %s kept the session's fingerprint", name)
		}
	}
	if planKit(t, kitSong).Fingerprint() != a {
		t.Error("the same song has two fingerprints")
	}
	// The order a song lists its locks in is no other session.
	if planKit(t, strings.Replace(kitSong, "[drum1pitch, drum1pan]", "[drum1pan, drum1pitch]", 1)).Fingerprint() != a {
		t.Error("the locks' order changed the fingerprint")
	}

}

// TestKitStateRoundTrip checks samples and locks are written back as read.
func TestKitStateRoundTrip(t *testing.T) {
	s := mustParse(t, kitSong)
	data, err := s.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	again, err := Parse(data)
	if err != nil {
		t.Fatalf("the written song does not parse: %v\n%s", err, data)
	}
	p1, _ := NewPlan(s, kitCatalog())
	p2, err := NewPlan(again, kitCatalog())
	if err != nil {
		t.Fatal(err)
	}
	if p1.Fingerprint() != p2.Fingerprint() {
		t.Errorf("the samples or locks changed in the round trip:\n%s", data)
	}
	for _, want := range []string{"samples:\n", `"1": Kicks/Abstrakta 1 Kick.wav`, "locks:\n", "- drum1pitch"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("the written song lacks %q:\n%s", want, data)
		}
	}
}
