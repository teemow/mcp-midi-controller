package mcpserver

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/teemow/midi-transport/midicontrol"
)

func intp(v int) *int { return &v }

// TestCompileSequenceSequentialDefault verifies the at_ms-less timeline: note
// steps run back-to-back, a control step occupies no time (it fires where the
// previous step ended, together with the next note's start).
func TestCompileSequenceSequentialDefault(t *testing.T) {
	events, span, err := compileSequence([]seqStep{
		{Note: intp(60), DurationMS: intp(200)},
		{CC: intp(74), Value: intp(90)},
		{Note: intp(64), DurationMS: intp(300)},
	}, 100, 1)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if span != 500 {
		t.Fatalf("span = %d, want 500", span)
	}
	// Expected timeline: on60@0, off60@200, cc@200, on64@200, off64@500 —
	// with off-before-cc-before-on at the shared 200ms timestamp.
	type ev struct {
		at   int
		typ  string
		data int
	}
	var got []ev
	for _, e := range events {
		d := e.cmd.Note
		if e.cmd.Type == "cc" {
			d = e.cmd.Controller
		}
		got = append(got, ev{e.at, e.cmd.Type, d})
	}
	want := []ev{
		{0, "noteOn", 60},
		{200, "noteOff", 60},
		{200, "cc", 74},
		{200, "noteOn", 64},
		{500, "noteOff", 64},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestCompileSequenceOverlap verifies an explicit at_ms expresses legato: the
// second note starts while the first still sounds, so its note-on precedes the
// first note's note-off on the timeline.
func TestCompileSequenceOverlap(t *testing.T) {
	events, span, err := compileSequence([]seqStep{
		{Note: intp(36), DurationMS: intp(600)},
		{AtMS: intp(400), Note: intp(48), DurationMS: intp(400)},
	}, 100, 1)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if span != 800 {
		t.Fatalf("span = %d, want 800", span)
	}
	// on36@0, on48@400, off36@600, off48@800.
	if events[1].cmd.Type != "noteOn" || events[1].cmd.Note != 48 || events[1].at != 400 {
		t.Fatalf("event 1 = %+v, want noteOn 48 @400", events[1])
	}
	if events[2].cmd.Type != "noteOff" || events[2].cmd.Note != 36 || events[2].at != 600 {
		t.Fatalf("event 2 = %+v, want noteOff 36 @600", events[2])
	}
}

// TestCompileSequenceDefaults verifies velocity/duration/channel defaults flow
// from the call level, and a chord step expands to one on/off pair per note.
func TestCompileSequenceDefaults(t *testing.T) {
	events, span, err := compileSequence([]seqStep{
		{Notes: []int{60, 64, 67}},
	}, 88, 5)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if span != seqDefaultNoteMS {
		t.Fatalf("span = %d, want default %d", span, seqDefaultNoteMS)
	}
	if len(events) != 6 {
		t.Fatalf("got %d events, want 6", len(events))
	}
	for _, e := range events {
		if e.cmd.Channel != 5 {
			t.Fatalf("channel = %d, want 5", e.cmd.Channel)
		}
		if e.cmd.Type == "noteOn" && e.cmd.Velocity != 88 {
			t.Fatalf("velocity = %d, want 88", e.cmd.Velocity)
		}
	}
}

// TestCompileSequenceErrors verifies the /sequence/<i> error pointers for the
// rejected shapes.
func TestCompileSequenceErrors(t *testing.T) {
	cases := []struct {
		name  string
		steps []seqStep
		want  string
	}{
		{"empty step", []seqStep{{}}, "/sequence/0: provide note(s) or cc"},
		{"note and cc", []seqStep{{Note: intp(60), CC: intp(74), Value: intp(1)}}, "/sequence/0: provide note(s) or cc, not both"},
		{"cc without value", []seqStep{{CC: intp(74)}}, "/sequence/0/value"},
		{"bad note", []seqStep{{Notes: []int{200}}}, "/sequence/0/notes/0"},
		{"negative at", []seqStep{{AtMS: intp(-1), Note: intp(60)}}, "/sequence/0/at_ms"},
		{"too long", []seqStep{{Note: intp(60), DurationMS: intp(20_000)}}, "max 10000ms"},
		{"no steps", nil, "/sequence: no steps"},
	}
	for _, tc := range cases {
		_, _, err := compileSequence(tc.steps, 100, 1)
		if err == nil {
			t.Fatalf("%s: expected error", tc.name)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: error %q missing %q", tc.name, err.Error(), tc.want)
		}
	}
}

// TestRunSequenceFlushesOnError verifies a mid-sequence send failure releases
// every still-sounding note through the same sender (no hung chord).
func TestRunSequenceFlushesOnError(t *testing.T) {
	events, _, err := compileSequence([]seqStep{
		{Notes: []int{60, 64}, DurationMS: intp(10)},
	}, 100, 1)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	var sent []midicontrol.Command
	fail := errors.New("boom")
	calls := 0
	sender := func(_ context.Context, ev seqEvent) error {
		calls++
		if calls == 3 { // first noteOff fails after both noteOns succeeded
			return fail
		}
		sent = append(sent, ev.cmd)
		return nil
	}
	if err := runSequence(context.Background(), sender, events); !errors.Is(err, fail) {
		t.Fatalf("err = %v, want wrapped boom", err)
	}
	// Both notes were on when the failure hit; both must get a flush note-off.
	offs := map[int]bool{}
	for _, c := range sent {
		if c.Type == "noteOff" {
			offs[c.Note] = true
		}
	}
	if !offs[60] || !offs[64] {
		t.Fatalf("flush note-offs missing, sent: %+v", sent)
	}
}

// TestRunSequenceFlushesOnCancel verifies cancellation mid-hold still releases
// the sounding note, and that the flush note-off arrives on a live (detached)
// context rather than the cancelled one.
func TestRunSequenceFlushesOnCancel(t *testing.T) {
	events, _, err := compileSequence([]seqStep{
		{Note: intp(60), DurationMS: intp(5_000)},
	}, 100, 1)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var offs []int
	sender := func(ctx context.Context, ev seqEvent) error {
		if err := ctx.Err(); err != nil {
			return err // a real sender fails on a cancelled context
		}
		switch ev.cmd.Type {
		case "noteOn":
			cancel() // cancel while the note is held
		case "noteOff":
			offs = append(offs, ev.cmd.Note)
		}
		return nil
	}
	if err := runSequence(ctx, sender, events); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(offs) != 1 || offs[0] != 60 {
		t.Fatalf("flush note-off missing or wrong, got: %v", offs)
	}
}

// TestPlayNotesRejectsSequencePlusNotes verifies the mutual exclusion at the
// handler level.
func TestPlayNotesRejectsSequencePlusNotes(t *testing.T) {
	s, _ := probeTestServer(t, 440)
	res := call(t, s.handlePlayNotes, map[string]any{
		"note":     60,
		"sequence": []map[string]any{{"note": 62}},
	})
	if !res.IsError {
		t.Fatalf("expected error, got: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "not both") {
		t.Fatalf("error missing mutual-exclusion hint: %s", resultText(res))
	}
}

// TestProbeSoundRejectsBadSequence verifies probe_sound surfaces the compile
// pointer for an invalid sequence step.
func TestProbeSoundRejectsBadSequence(t *testing.T) {
	s, _ := probeTestServer(t, 440)
	res := call(t, s.handleProbeSound, map[string]any{
		"sequence": []map[string]any{{"value": 3}},
	})
	if !res.IsError {
		t.Fatalf("expected error, got: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "/sequence/0") {
		t.Fatalf("error missing /sequence/0 pointer: %s", resultText(res))
	}
}
