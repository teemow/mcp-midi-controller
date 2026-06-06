package device

import "testing"

// TestConventionTapCC checks the tap-bypass CC sequence: a contiguous block
// starting at 77, clear of the mixer/transport bands, that overflows once the
// reserved tap range is exhausted.
func TestConventionTapCC(t *testing.T) {
	if TapControlChannel != 16 {
		t.Fatalf("TapControlChannel = %d, want 16 (the reserved tap channel)", TapControlChannel)
	}

	// The first three taps take CC 77, 78, 79 in order.
	for _, tc := range []struct {
		n    int
		want int
	}{{1, 77}, {2, 78}, {3, 79}} {
		cc, ok := ConventionTapCC(tc.n)
		if !ok || cc != tc.want {
			t.Fatalf("ConventionTapCC(%d) = %d,%v, want %d,true", tc.n, cc, ok, tc.want)
		}
	}

	// n < 1 is invalid.
	if cc, ok := ConventionTapCC(0); ok {
		t.Fatalf("ConventionTapCC(0) = %d,%v, want _,false", cc, ok)
	}

	// The tap block ends at CC 95 (19 slots); the 20th tap overflows.
	if cc, ok := ConventionTapCC(19); !ok || cc != 95 {
		t.Fatalf("ConventionTapCC(19) = %d,%v, want 95,true", cc, ok)
	}
	if cc, ok := ConventionTapCC(20); ok {
		t.Fatalf("ConventionTapCC(20) = %d,%v, want _,false (block exhausted)", cc, ok)
	}

	// Every assigned tap CC stays clear of the mixer (≤76), transport/system
	// (≥102) and the MIDI-reserved 96..101 band, so the block is distinct even
	// when read on a shared channel.
	for n := 1; ; n++ {
		cc, ok := ConventionTapCC(n)
		if !ok {
			break
		}
		if cc <= 76 || cc >= 96 {
			t.Fatalf("ConventionTapCC(%d) = %d, outside the clear 77..95 band", n, cc)
		}
	}
}
