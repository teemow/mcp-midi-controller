package mdns

import (
	"slices"
	"testing"
)

func TestDNSSDArgs(t *testing.T) {
	got := dnssdArgs("mcp-midi-controller", 7800, []string{
		"version=v1.2.3",
		"capabilities=audio,midi",
		`path=C:\rig\x41`,
	})
	want := []string{
		"-R", "mcp-midi-controller", "_mcpmidi._tcp", "local", "7800",
		"version=v1.2.3",
		"capabilities=audio,midi",
		`path=C:\\rig\\x41`, // dns-sd unescapes \\ back to one literal backslash
	}
	if !slices.Equal(got, want) {
		t.Fatalf("dnssdArgs:\n got %q\nwant %q", got, want)
	}

	if got := dnssdArgs("x", 1, nil); !slices.Equal(got, []string{"-R", "x", "_mcpmidi._tcp", "local", "1"}) {
		t.Fatalf("dnssdArgs without TXT: %q", got)
	}
}

func TestParseReply(t *testing.T) {
	for _, tc := range []struct {
		line, name, status string
		ok                 bool
	}{
		{
			line:   "23:01:10.580  Got a reply for service mcp-midi-controller._mcpmidi._tcp.local.: Name now registered and active",
			name:   "mcp-midi-controller",
			status: dnssdRegistered,
			ok:     true,
		},
		{ // automatic rename after a name conflict
			line:   "23:01:12.001  Got a reply for service mcp-midi-controller (2)._mcpmidi._tcp.local.: Name now registered and active",
			name:   "mcp-midi-controller (2)",
			status: dnssdRegistered,
			ok:     true,
		},
		{ // instance names may contain ": " and dots
			line:   "Got a reply for service rig: v1.2._mcpmidi._tcp.local.: Name registration removed",
			name:   "rig: v1.2",
			status: "Name registration removed",
			ok:     true,
		},
		{
			line:   "23:01:10.580  Got a reply for service x._mcpmidi._tcp.local.: Error -65548",
			name:   "x",
			status: "Error -65548",
			ok:     true,
		},
		{line: "Registering Service mcp-midi-controller._mcpmidi._tcp.local port 7800 TXT version=v1"},
		{line: "DATE: ---Mon 09 Nov 2026---"},
		{line: "23:01:09.982  ...STARTING..."},
		{line: "DNSService call failed -65563"},
		{line: ""},
	} {
		name, status, ok := parseReply(tc.line)
		if name != tc.name || status != tc.status || ok != tc.ok {
			t.Errorf("parseReply(%q) = %q, %q, %v; want %q, %q, %v", tc.line, name, status, ok, tc.name, tc.status, tc.ok)
		}
	}
}
