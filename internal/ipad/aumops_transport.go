package ipad

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// AUMTransportActions are aum_transport's actions.
var AUMTransportActions = []string{
	"state", "play", "stop", "toggle", "rewind", "locate", "set_tempo", "nudge_tempo",
	"metronome", "record", "unarm_all",
}

// AUMTransport runs a transport, tempo or recording operation.
func (c *Client) AUMTransport(ctx context.Context, r AUMRequest) (AUMResult, error) {
	var res AUMResult
	err := c.DriveAUM(ctx, func(d *AUMDriver) error {
		var err error
		switch r.Action {
		case "state", "":
			var playing bool
			if playing, err = d.playing(); err == nil {
				res = d.result("state", true, fmt.Sprintf("transport %s at %s, %s", map[bool]string{true: "playing", false: "stopped"}[playing], d.St.Position, tempoText(d.St)), map[string]any{"playing": playing})
			}
		case "play", "stop", "toggle":
			res, err = d.playStop(r.Action)
		case "rewind":
			res, err = d.rewind()
		case "locate":
			res, err = d.locate(r.Bars)
		case "set_tempo":
			if r.BPM == nil {
				return fmt.Errorf("set_tempo needs bpm")
			}
			res, err = d.setTempo(*r.BPM)
		case "nudge_tempo":
			res, err = d.nudgeTempo(r.Steps)
		case "metronome":
			res, err = d.metronome(r.On)
		case "record":
			if err := needConfirm(r, "record starts recording every armed channel and writes files (a second record call stops it)"); err != nil {
				return err
			}
			res, err = d.record()
		case "unarm_all":
			res, err = d.unarmAll()
		default:
			err = fmt.Errorf("unknown aum_transport action %q (%s)", r.Action, strings.Join(AUMTransportActions, ", "))
		}
		return err
	})
	return res, err
}

func tempoText(st AUMState) string {
	if st.TempoBPM == nil {
		return "tempo not visible"
	}
	return fmt.Sprintf("%g BPM", *st.TempoBPM)
}

// transportX are the transport buttons, placed from the position display
// above them (the tempo bar shifts the whole transport right): rewind,
// play, record.
func (d *AUMDriver) transportX() (rewind, play, record float64) {
	x := d.St.positionFrame.X
	if d.St.positionFrame.Empty() {
		x = 89
	}
	return x + 22, x + 67, x + 112
}

// playing reads the position twice, about a second apart: it advances
// while the transport runs.
func (d *AUMDriver) playing() (bool, error) {
	p1 := d.St.Position
	if p1 == "" {
		return false, fmt.Errorf("the transport position is not visible")
	}
	if err := d.pause(700 * time.Millisecond); err != nil {
		return false, err
	}
	if err := d.Read(); err != nil {
		return false, err
	}
	return d.St.Position != p1, nil
}

func (d *AUMDriver) playStop(action string) (AUMResult, error) {
	was, err := d.playing()
	if err != nil {
		return AUMResult{}, err
	}
	want := !was
	switch action {
	case "play":
		want = true
	case "stop":
		want = false
	}
	if was == want {
		return d.result(action, true, fmt.Sprintf("already %s at %s", map[bool]string{true: "playing", false: "stopped"}[was], d.St.Position), nil), nil
	}
	_, play, _ := d.transportX()
	if err := d.tap(play, 33, "play/stop ▷"); err != nil {
		return AUMResult{}, err
	}
	now := was
	deadline := time.Now().Add(aumWait)
	for time.Now().Before(deadline) {
		if err := d.Read(); err != nil {
			return AUMResult{}, err
		}
		if now, err = d.playing(); err != nil {
			return AUMResult{}, err
		}
		if now == want {
			break
		}
	}
	return d.result(action, now == want, fmt.Sprintf("transport %s at %s", map[bool]string{true: "playing", false: "stopped"}[now], d.St.Position), map[string]any{"playing": now}), nil
}

func (d *AUMDriver) rewind() (AUMResult, error) {
	was := d.St.Position
	rw, _, _ := d.transportX()
	if err := d.tap(rw, 33, "rewind |◁"); err != nil {
		return AUMResult{}, err
	}
	ok, err := d.until(aumWait, func(st AUMState) bool { return strings.HasPrefix(st.Position, "001:1") })
	return d.result("rewind", ok, fmt.Sprintf("position %q -> %q", was, d.St.Position), nil), err
}

// barOf reads the bar of a position text ("003:1  00:04.0" -> 3).
func barOf(pos string) (int, bool) {
	bar, _, ok := strings.Cut(strings.TrimSpace(pos), ":")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(bar)
	return n, err == nil
}

// locate drags sideways on the transport, which moves the position by whole
// bars (95 points went two bars), and corrects once.
func (d *AUMDriver) locate(bars int) (AUMResult, error) {
	if bars == 0 {
		return AUMResult{}, fmt.Errorf("locate needs bars: how many bars to move (negative = back)")
	}
	start, ok := barOf(d.St.Position)
	if !ok {
		return AUMResult{}, fmt.Errorf("the transport position %q is not readable", d.St.Position)
	}
	target := start + bars
	_, play, _ := d.transportX()
	for try := 0; try < 3; try++ {
		cur, _ := barOf(d.St.Position)
		if cur == target {
			break
		}
		dx := float64(target-cur) * 47.5
		dx = math.Max(-play+20, math.Min(d.win.Width/2, dx))
		if err := d.drag("the transport", DragTiming{Move: 500 * time.Millisecond, Hold: 300 * time.Millisecond},
			Point{play, 33}, Point{play + dx, 33}); err != nil {
			return AUMResult{}, err
		}
		if err := d.Read(); err != nil {
			return AUMResult{}, err
		}
	}
	cur, _ := barOf(d.St.Position)
	return d.result("locate", cur == target, fmt.Sprintf("bar %d -> %d (wanted %d): %s", start, cur, target, d.St.Position), nil), nil
}

// setTempo opens the tempo bar, double-taps the tempo for Edit Value,
// types the BPM and taps Set.
func (d *AUMDriver) setTempo(bpm float64) (AUMResult, error) {
	opened := !d.St.TempoBar
	if ok, err := d.ensureTempoBar(); err != nil || !ok {
		return d.result("set_tempo", false, "the tempo bar did not open", nil), err
	}
	was := tempoText(d.St)
	x, y := d.St.tempoFrame.Center()
	if err := d.doubleTap(x, y, "the tempo value"); err != nil {
		return AUMResult{}, err
	}
	dlg, err := d.waitOverlay("", aumWait)
	if err != nil {
		return AUMResult{}, err
	}
	if dlg == nil {
		return d.result("set_tempo", false, "the Edit Value dialog did not open", nil), nil
	}
	if _, err := d.typeText(0, strconv.FormatFloat(bpm, 'f', -1, 64)); err != nil {
		return AUMResult{}, err
	}
	if err := d.confirmDialog(dlg); err != nil {
		return AUMResult{}, err
	}
	ok, err := d.until(aumWait, func(st AUMState) bool {
		return st.Overlay("alert") == nil && st.TempoBPM != nil && math.Abs(*st.TempoBPM-bpm) < 0.05
	})
	if err != nil {
		return AUMResult{}, err
	}
	if opened && d.St.TempoBar {
		if _, err := d.closeTop(); err != nil {
			return AUMResult{}, err
		}
	}
	return d.result("set_tempo", ok, fmt.Sprintf("tempo %s -> %s", was, tempoText(d.St)), nil), nil
}

// nudgeTempo taps the tempo bar's ‹ or › arrow |steps| times (0.1 BPM each).
func (d *AUMDriver) nudgeTempo(steps int) (AUMResult, error) {
	if steps == 0 {
		return AUMResult{}, fmt.Errorf("nudge_tempo needs steps: 0.1-BPM steps, negative = slower")
	}
	if steps > 50 || steps < -50 {
		return AUMResult{}, fmt.Errorf("nudge_tempo takes at most 50 steps; use set_tempo")
	}
	opened := !d.St.TempoBar
	if ok, err := d.ensureTempoBar(); err != nil || !ok {
		return d.result("nudge_tempo", false, "the tempo bar did not open", nil), err
	}
	if d.St.TempoBPM == nil {
		return AUMResult{}, fmt.Errorf("the tempo is not visible")
	}
	was := *d.St.TempoBPM
	tf := d.St.tempoFrame
	x, label := tf.X+tf.Width+23, "›"
	if steps < 0 {
		x, label = tf.X-23, "‹"
	}
	for i := 0; i < abs(steps); i++ {
		if err := d.tap(x, 32, label); err != nil {
			return AUMResult{}, err
		}
	}
	want := was + float64(steps)/10
	ok, err := d.until(aumWait, func(st AUMState) bool { return st.TempoBPM != nil && math.Abs(*st.TempoBPM-want) < 0.05 })
	if err != nil {
		return AUMResult{}, err
	}
	if opened && d.St.TempoBar {
		if _, err := d.closeTop(); err != nil {
			return AUMResult{}, err
		}
	}
	return d.result("nudge_tempo", ok, fmt.Sprintf("tempo %g -> %s (wanted %g)", was, tempoText(d.St), want), nil), nil
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// metronome taps the tempo bar's metronome toggle (green when on).
func (d *AUMDriver) metronome(want *bool) (AUMResult, error) {
	opened := !d.St.TempoBar
	if ok, err := d.ensureTempoBar(); err != nil || !ok {
		return d.result("metronome", false, "the tempo bar did not open", nil), err
	}
	tf := d.St.tempoFrame
	x := tf.X + tf.Width + 66
	btn := d.find(func(e *Element) bool {
		return e.Type == "Button" && !e.Hidden && e.Rect.Y < 60 && e.Rect.Contains(x, 32)
	})
	var was *bool
	if btn != nil {
		s := btn.Selected()
		was = &s
		x, _ = btn.Rect.Center()
	}
	if want != nil && was != nil && *was == *want {
		return d.result("metronome", true, "metronome already "+onOff(*want), nil), nil
	}
	if err := d.tap(x, 32, "the metronome"); err != nil {
		return AUMResult{}, err
	}
	ok := false
	msg := "tapped the metronome; its state is not in AUM's tree (check with ipad_screenshot: green when on)"
	if was != nil {
		var err error
		ok, err = d.until(aumWait, func(st AUMState) bool {
			b := d.find(func(e *Element) bool {
				return e.Type == "Button" && !e.Hidden && e.Rect.Y < 60 && e.Rect.Contains(x, 32)
			})
			return b != nil && b.Selected() != *was
		})
		if err != nil {
			return AUMResult{}, err
		}
		msg = verdict(ok, "metronome "+onOff(!*was), "the metronome state did not change")
	}
	if opened && d.St.TempoBar {
		if _, err := d.closeTop(); err != nil {
			return AUMResult{}, err
		}
	}
	return d.result("metronome", ok, msg, nil), nil
}

// record taps record. Stopped, it starts the transport (after the pre-roll)
// and records every armed channel; while playing it waits for the next sync
// quantum. Tapping it while recording stops the take.
func (d *AUMDriver) record() (AUMResult, error) {
	armed := 0
	for _, ch := range d.St.Channels {
		if ch.Armed != nil && *ch.Armed {
			armed++
		}
	}
	wasPlaying, err := d.playing()
	if err != nil {
		return AUMResult{}, err
	}
	_, _, rec := d.transportX()
	if err := d.tap(rec, 33, "record ●"); err != nil {
		return AUMResult{}, err
	}
	if err := d.pause(1500 * time.Millisecond); err != nil {
		return AUMResult{}, err
	}
	if err := d.Read(); err != nil {
		return AUMResult{}, err
	}
	playing, err := d.playing()
	if err != nil {
		return AUMResult{}, err
	}
	finished := d.findPrefix("RECORDING FINISHED", nil) != nil || d.findPrefix("Recording saved", nil) != nil
	msg := fmt.Sprintf("record tapped with %d armed channel(s); transport %s at %s", armed,
		map[bool]string{true: "playing", false: "stopped"}[playing], d.St.Position)
	if finished {
		msg += "; RECORDING FINISHED (the take is in FILES → Recordings)"
	}
	if armed == 0 {
		msg += "; no channel is armed, so nothing records (aum_channel arm)"
	}
	return d.result("record", finished || playing || wasPlaying, msg, map[string]any{"armed": armed, "playing": playing, "finished": finished}), nil
}

// unarmAll long-presses record and answers Yes to "Un-arm recording".
func (d *AUMDriver) unarmAll() (AUMResult, error) {
	_, _, rec := d.transportX()
	if err := d.hold(Rect{X: rec - 15, Y: 18, Width: 30, Height: 30}, "record ●"); err != nil {
		return AUMResult{}, err
	}
	dlg, err := d.waitOverlay("", aumWait)
	if err != nil {
		return AUMResult{}, err
	}
	if dlg == nil {
		return d.result("unarm_all", false, "the Un-arm recording question did not appear", nil), nil
	}
	if err := d.tapText("Yes", &dlg.Frame); err != nil {
		if err2 := d.tapText("Ja", &dlg.Frame); err2 != nil {
			return AUMResult{}, err
		}
	}
	ok, err := d.until(aumWait, func(st AUMState) bool {
		if st.Overlay("alert") != nil {
			return false
		}
		for _, ch := range st.Channels {
			if ch.Armed != nil && *ch.Armed {
				return false
			}
		}
		return true
	})
	return d.result("unarm_all", ok, verdict(ok, "no channel is armed", "some channels are still armed"), nil), err
}
