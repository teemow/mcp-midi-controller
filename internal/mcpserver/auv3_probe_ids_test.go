package mcpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/midi-transport/auv3"
)

// TestAUv3ProbesSharingASubtype stages two audio units that share a subtype
// (an instrument and a MIDI processor from one vendor) through the real
// receiver, then reads them back by the ids the tools report. Before the
// component id both were staged as one <subtype>.json and the second
// overwrote the first.
func TestAUv3ProbesSharingASubtype(t *testing.T) {
	s := newAUMServer(t)
	ts := httptest.NewServer(auv3.Handler(config.AUv3ProbesDir(), nil))
	defer ts.Close()

	dumps := map[string]string{
		"aumu-_ab12-_xy_zw": `{"component":{"type":"aumu","subtype":"Ab12","manufacturer":"XyZw"},"name":"Synth",
			"parameters":[{"address":1,"identifier":"cutoff","displayName":"Cutoff","min":0,"max":1,"value":0.5,"writable":true,"readable":true}]}`,
		"aumi-_ab12-_xy_zw": `{"component":{"type":"aumi","subtype":"Ab12","manufacturer":"XyZw"},"name":"Synth MIDI",
			"parameters":[{"address":1,"identifier":"rate","displayName":"Rate","min":0,"max":1,"value":0.5,"writable":true,"readable":true}]}`,
	}
	for _, body := range dumps {
		resp, err := http.Post(ts.URL+"/auv3-probe", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("post dump: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("post dump: status %d", resp.StatusCode)
		}
	}

	res := call(t, s.handleListAUv3Probes, map[string]any{})
	sc, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("list: no structured content: %s", resultText(res))
	}
	probes, _ := sc["probes"].([]probeSummary)
	if len(probes) != 2 {
		t.Fatalf("list: %d probes, want 2:\n%s", len(probes), resultText(res))
	}

	for id, wantParam := range map[string]string{"aumu-_ab12-_xy_zw": "Cutoff", "aumi-_ab12-_xy_zw": "Rate"} {
		get := call(t, s.handleGetAUv3Probe, map[string]any{"device_id": id})
		if get.IsError || !strings.Contains(resultText(get), wantParam) {
			t.Fatalf("get %s: want param %s:\n%s", id, wantParam, resultText(get))
		}
		imp := call(t, s.handleImportAUv3Probe, map[string]any{"device_id": id})
		if imp.IsError || !strings.Contains(resultText(imp), "draft \""+id+"\"") {
			t.Fatalf("import %s: want draft %q:\n%s", id, id, resultText(imp))
		}
	}
	if _, ok := s.drafts["aumu-_ab12-_xy_zw"]; !ok {
		t.Fatalf("instrument draft missing after both imports")
	}
	if _, ok := s.drafts["aumi-_ab12-_xy_zw"]; !ok {
		t.Fatalf("MIDI processor draft missing after both imports")
	}
}
