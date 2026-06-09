package device

import (
	"bytes"
	"encoding/base64"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestClassifyStateDocRoundTrip(t *testing.T) {
	doc := map[string][]byte{
		"probeMidiBrainConfig": []byte(`{"host":"box:7800","controlEnabled":true}`),                            // pure JSON text
		"jucePluginState":      append([]byte("VC2!\x9c\x01\x00\x00"), []byte(`<?xml version="1.0"?><P/>`)...), // binary header + XML
		"ModulePreset":         []byte(`<ModulePreset><Script/></ModulePreset>`),                               // bare XML text
		"data":                 {0x00, 0x01, 0x02, 0xff, 0x80, 0x7f, 0x00},                                     // opaque binary
		"empty":                {},
	}
	got := ClassifyStateDoc(doc)

	if e := got["probeMidiBrainConfig"]; e.Text == "" || e.Base64 != "" || e.Prefix != "" {
		t.Errorf("JSON should classify as text, got %+v", e)
	}
	if e := got["jucePluginState"]; e.Text == "" || e.Prefix == "" {
		t.Errorf("prefixed XML should classify as text+prefix, got %+v", e)
	} else if e.Text[:5] != "<?xml" {
		t.Errorf("prefixed XML text should start at the XML decl, got %q", e.Text[:5])
	}
	if e := got["ModulePreset"]; e.Text == "" || e.Prefix != "" {
		t.Errorf("bare XML should classify as text (no prefix), got %+v", e)
	}
	if e := got["data"]; e.Base64 == "" || e.Text != "" {
		t.Errorf("opaque binary should classify as base64, got %+v", e)
	}

	// Every classified entry must round-trip back to the exact input bytes.
	d := AUv3DefaultState{
		Component: ProbeComponent{Type: "aumu", Subtype: "iSEM", Manufacturer: "Artu"},
		State:     got,
	}
	rt, err := d.StateDoc()
	if err != nil {
		t.Fatalf("StateDoc: %v", err)
	}
	for k, want := range doc {
		if !bytes.Equal(rt[k], want) {
			t.Errorf("round-trip %q = %x, want %x", k, rt[k], want)
		}
	}
}

func TestStateEntryBytes(t *testing.T) {
	raw := []byte{0xde, 0xad, 0xbe, 0xef}
	cases := map[string]struct {
		e    StateEntry
		want []byte
		err  bool
	}{
		"text":        {StateEntry{Text: "hi"}, []byte("hi"), false},
		"base64":      {StateEntry{Base64: base64.StdEncoding.EncodeToString(raw)}, raw, false},
		"prefix+text": {StateEntry{Prefix: base64.StdEncoding.EncodeToString([]byte("AB")), Text: "<x/>"}, []byte("AB<x/>"), false},
		"empty":       {StateEntry{}, []byte{}, false},
		"both":        {StateEntry{Text: "x", Base64: "eA=="}, nil, true},
		"bad base64":  {StateEntry{Base64: "!!!notb64"}, nil, true},
		"bad prefix":  {StateEntry{Prefix: "!!!", Text: "x"}, nil, true},
	}
	for name, c := range cases {
		got, err := c.e.Bytes()
		if c.err {
			if err == nil {
				t.Errorf("%s: expected error", name)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !bytes.Equal(got, c.want) {
			t.Errorf("%s: got %x want %x", name, got, c.want)
		}
	}
}

func TestStateDocRejectsIdentityKeys(t *testing.T) {
	d := AUv3DefaultState{
		Component: ProbeComponent{Type: "aumu", Subtype: "iSEM", Manufacturer: "Artu"},
		State:     map[string]StateEntry{"type": {Text: "aumu"}},
	}
	if _, err := d.StateDoc(); err == nil {
		t.Fatal("expected identity key to be rejected")
	}
}

func TestAUv3DefaultStateValidate(t *testing.T) {
	ok := AUv3DefaultState{
		Component: ProbeComponent{Type: "aumu", Subtype: "iSEM", Manufacturer: "Artu"},
		State:     map[string]StateEntry{"data": {Base64: "AAAA"}},
	}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid state rejected: %v", err)
	}
	noComp := AUv3DefaultState{State: map[string]StateEntry{"data": {Text: "x"}}}
	if err := noComp.Validate(); err == nil {
		t.Fatal("missing component should fail")
	}
	noState := AUv3DefaultState{Component: ok.Component}
	if err := noState.Validate(); err == nil {
		t.Fatal("empty state should fail")
	}
}

func TestAUv3DefaultStateYAMLRoundTrip(t *testing.T) {
	in := AUv3DefaultState{
		Component: ProbeComponent{Type: "aumu", Subtype: "iSEM", Manufacturer: "Artu"},
		Name:      "Arturia iSEM",
		State: map[string]StateEntry{
			"probeMidiBrainConfig": {Text: `{"host":"box:7800"}`},
			"ISEMPatch":            {Base64: "AAAA"},
			"jucePluginState":      {Prefix: "VkMyIQ==", Text: "<?xml?>"},
		},
	}
	out, err := yaml.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back AUv3DefaultState
	if err := yaml.Unmarshal(out, &back); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	a, _ := in.StateDoc()
	b, err := back.StateDoc()
	if err != nil {
		t.Fatalf("StateDoc after round-trip: %v", err)
	}
	for k, av := range a {
		if !bytes.Equal(av, b[k]) {
			t.Errorf("yaml round-trip %q changed bytes", k)
		}
	}
	if back.Name != in.Name || back.Component.Subtype != "iSEM" {
		t.Errorf("yaml round-trip lost component/name: %+v", back)
	}
}
