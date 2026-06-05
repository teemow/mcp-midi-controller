package aum

// This file is the Phase-4 diff oracle: it compares a session's actual
// channel-control mappings against the server's AUM mixer CC convention (the
// same convention build.go applies when authoring), so diff_aum_session can
// report whether a session is "wired to the convention" or not. The ordinal /
// master-strip rules mirror applyConvention so author→diff round-trips agree.

import "fmt"

// ConventionCheck is the per-target result of comparing one channel-control
// mapping against the mixer CC convention. ActualCC/Channel are -1 when the
// target is an unassigned placeholder (Status "missing").
type ConventionCheck struct {
	Collection string `json:"collection"`
	Target     string `json:"target"`
	ExpectedCC int    `json:"expectedCC"`
	ActualCC   int    `json:"actualCC"`
	Channel    int    `json:"channel"`
	Status     string `json:"status"` // "ok" | "missing" | "mismatch"
}

// ConventionReport summarizes a session's adherence to the mixer CC convention:
// how many channel-control targets the convention defines for this session
// (Expected) and how many are actually wired to their convention CC (Wired).
type ConventionReport struct {
	Expected int               `json:"expected"`
	Wired    int               `json:"wired"`
	Checks   []ConventionCheck `json:"checks"`
}

// CheckMixerConvention compares every non-master audio strip's channel controls
// (Volume/Mute/Solo/Rec) against the AUM mixer CC convention and returns the
// per-target verdicts. The non-master audio strips are numbered 1..8 in array
// order (the master is the last audio strip), matching applyConvention.
func (s *Session) CheckMixerConvention() ConventionReport {
	actual := map[string]Mapping{}
	for _, m := range s.Mappings(true) {
		actual[m.Collection+"\x00"+m.Target] = m
	}

	chans := s.Channels()
	masterPos := -1
	for i, ch := range chans {
		if ch.Kind == KindAudio {
			masterPos = i
		}
	}

	var rep ConventionReport
	ordinal := 0
	for i, ch := range chans {
		if ch.Kind != KindAudio || i == masterPos {
			continue
		}
		ordinal++
		coll := fmt.Sprintf("Channels/chan%d/Channel controls", i)
		for _, ctl := range audioChannelControls {
			cc, ok := conventionMixerCC(ordinal, ctl.name)
			if !ok {
				continue
			}
			rep.Expected++
			chk := ConventionCheck{
				Collection: coll, Target: ctl.name,
				ExpectedCC: cc, ActualCC: -1, Channel: -1,
			}
			if m, found := actual[coll+"\x00"+ctl.name]; found && m.Spec.Enabled {
				chk.ActualCC = m.Spec.Data1
				chk.Channel = m.Spec.Channel
				if m.Spec.Type == TypeCC && m.Spec.Data1 == cc {
					chk.Status = "ok"
					rep.Wired++
				} else {
					chk.Status = "mismatch"
				}
			} else {
				chk.Status = "missing"
			}
			rep.Checks = append(rep.Checks, chk)
		}
	}
	return rep
}

// CheckConvention is the full-surface check: it extends CheckMixerConvention
// with the global Transport block (Toggle Play / Start Play / Stop-Rewind /
// Rewind / Toggle Record / Tap Tempo), so diff_aum_session reports coverage of
// the whole brain-control convention an authored session bakes, not just the
// mixer CCs. Pan/send (node-knob targets) and the not-yet-corpus-confirmed
// transport extras (prev/next bar, tempo value, metronome) are part of the
// documented convention but are not authored, so they are excluded here to keep
// "expected" equal to what BuildSession actually wires.
func (s *Session) CheckConvention() ConventionReport {
	rep := s.CheckMixerConvention()

	actual := map[string]Mapping{}
	for _, m := range s.Mappings(true) {
		actual[m.Collection+"\x00"+m.Target] = m
	}
	for _, target := range transportTargets {
		cc, ok := conventionTransportCC(target)
		if !ok {
			continue
		}
		rep.Expected++
		chk := ConventionCheck{
			Collection: "Transport", Target: target,
			ExpectedCC: cc, ActualCC: -1, Channel: -1,
		}
		if m, found := actual["Transport\x00"+target]; found && m.Spec.Enabled {
			chk.ActualCC = m.Spec.Data1
			chk.Channel = m.Spec.Channel
			if m.Spec.Type == TypeCC && m.Spec.Data1 == cc {
				chk.Status = "ok"
				rep.Wired++
			} else {
				chk.Status = "mismatch"
			}
		} else {
			chk.Status = "missing"
		}
		rep.Checks = append(rep.Checks, chk)
	}
	return rep
}
