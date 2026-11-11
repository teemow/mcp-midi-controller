package song

// This file is the notation a pattern is written in, kept small enough to
// write by hand and strict enough that a miscounted bar is an error rather
// than a song that drifts.
//
// A melodic grid is a line of steps separated by spaces. Each step is
//
//	A2          a note (C4 is middle C, MIDI 60); sharps "#", flats "b"
//	[A3 C4 E4]  a chord
//	-           the previous note or chord keeps sounding through this step
//	.           silence
//
// and a note or chord may carry "!" (an accent) or ":NN" (a velocity). A drum
// grid is one string per lane, one character per step: "x" a hit, "X" an
// accented hit, "o" a soft one, "." silence. In both, "|" marks a bar line:
// it takes no time, and once a grid uses bar lines every bar must have exactly
// the steps a bar holds.

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// PPQ is the resolution of every position in a compiled song: ticks per
// quarter note.
const PPQ = 480

const (
	defaultVelocity = 96
	defaultGate     = 0.9
	accentBoost     = 24
	softVelocity    = 0.55 // a soft drum hit, as a fraction of the lane's velocity
	minNoteTicks    = 10
)

// meter is a time signature and the bar length it gives.
type meter struct {
	num, den int
	barTicks int64
}

// parseMeter parses "beats/unit" ("" is 4/4).
func parseMeter(sig string) (meter, error) {
	if strings.TrimSpace(sig) == "" {
		sig = "4/4"
	}
	num, den, ok := strings.Cut(strings.TrimSpace(sig), "/")
	n, errN := strconv.Atoi(num)
	d, errD := strconv.Atoi(den)
	if !ok || errN != nil || errD != nil || n < 1 || n > 32 || (d != 1 && d != 2 && d != 4 && d != 8 && d != 16) {
		return meter{}, fmt.Errorf("%q is not a time signature like 4/4 or 6/8", sig)
	}
	return meter{num: n, den: d, barTicks: int64(n) * PPQ * 4 / int64(d)}, nil
}

// parseStep returns the length in ticks of a grid step such as "1/16" or
// "1/8t" ("" is 1/16).
func parseStep(step string) (int64, error) {
	st := strings.ToLower(strings.TrimSpace(step))
	if st == "" {
		st = "1/16"
	}
	triplet := strings.HasSuffix(st, "t")
	st = strings.TrimSuffix(st, "t")
	num, den, hasDen := strings.Cut(st, "/")
	n, errN := strconv.Atoi(num)
	d := 1
	var errD error
	if hasDen {
		d, errD = strconv.Atoi(den)
	}
	if errN != nil || errD != nil || n < 1 || d < 1 {
		return 0, fmt.Errorf("%q is not a step like 1/16, 1/8 or 1/8t", step)
	}
	whole := int64(PPQ * 4)
	ticks := whole * int64(n)
	if triplet {
		ticks = ticks * 2 / 3
		if (whole*int64(n)*2)%(3*int64(d)) != 0 {
			return 0, fmt.Errorf("step %q does not land on the %d-tick grid", step, PPQ)
		}
	} else if ticks%int64(d) != 0 {
		return 0, fmt.Errorf("step %q does not land on the %d-tick grid", step, PPQ)
	}
	ticks /= int64(d)
	if ticks < 1 {
		return 0, fmt.Errorf("step %q is too short", step)
	}
	return ticks, nil
}

// semitone is the pitch class of each note letter.
var semitone = map[byte]int{'c': 0, 'd': 2, 'e': 4, 'f': 5, 'g': 7, 'a': 9, 'b': 11}

// ParseNote reads a note as a song writes it: a name ("A2", "C#4", "Eb3"; C4
// is middle C) or a MIDI note number.
func ParseNote(s string) (int, error) { return parseNote(s) }

// parseNote parses a note name ("A2", "C#4", "Eb3"; C4 is MIDI 60) or a plain
// MIDI note number.
func parseNote(s string) (int, error) {
	s = strings.TrimSpace(s)
	if n, err := strconv.Atoi(s); err == nil {
		if n < 0 || n > 127 {
			return 0, fmt.Errorf("note %d is not within 0..127", n)
		}
		return n, nil
	}
	if s == "" {
		return 0, fmt.Errorf("empty note")
	}
	pc, ok := semitone[s[0]|0x20]
	if !ok {
		return 0, fmt.Errorf("%q is not a note (write A2, C#4, Eb3, or a MIDI number)", s)
	}
	i := 1
	for ; i < len(s); i++ {
		switch s[i] {
		case '#':
			pc++
			continue
		case 'b':
			pc--
			continue
		}
		break
	}
	octave, err := strconv.Atoi(s[i:])
	if err != nil {
		return 0, fmt.Errorf("%q is not a note (it needs an octave, as in A2)", s)
	}
	n := (octave+1)*12 + pc
	if n < 0 || n > 127 {
		return 0, fmt.Errorf("note %q is outside the MIDI range", s)
	}
	return n, nil
}

// NoteName renders a MIDI note number as a name (60 is "C4").
func NoteName(n int) string {
	names := []string{"C", "C#", "D", "D#", "E", "F", "F#", "G", "G#", "A", "A#", "B"}
	return fmt.Sprintf("%s%d", names[((n%12)+12)%12], n/12-1)
}

// gmDrums names the General MIDI drum notes every drum track knows.
var gmDrums = map[string]int{
	"kick": 36, "bd": 36,
	"rim": 37, "rimshot": 37, "sidestick": 37,
	"snare": 38, "sd": 38,
	"clap": 39, "cp": 39,
	"hat": 42, "hh": 42, "ch": 42, "closed_hat": 42,
	"pedal_hat": 44, "ph": 44,
	"open_hat": 46, "oh": 46,
	"tom_low": 45, "lt": 45,
	"tom_mid": 47, "mt": 47,
	"tom_high": 50, "ht": 50,
	"crash": 49, "ride": 51,
	"tamb": 54, "tambourine": 54,
	"cowbell": 56, "cb": 56,
	"shaker": 70, "clave": 75,
}

// drumNote resolves a lane name to its note: the track's own drum map first,
// then the General MIDI names, then a note name or number. A track whose kit
// was measured resolves a lane to that kit's pad instead of a General MIDI
// note, and a lane the kit does not have is an error.
func drumNote(t *Track, lane string) (int, error) {
	key := strings.ToLower(strings.TrimSpace(lane))
	if t != nil {
		for name, note := range t.DrumMap {
			if strings.ToLower(name) == key {
				return parseNote(note)
			}
		}
		if t.kit != nil {
			if n, ok := t.kit[key]; ok {
				return n, nil
			}
			if n, err := parseNote(lane); err == nil {
				return n, nil
			}
			return 0, fmt.Errorf("the kit %q has no pad for lane %q (it has: %s); use one of those, or put the lane into the track's drum_map",
				t.kitName, lane, strings.Join(sortedKeys(t.kit), ", "))
		}
	}
	if n, ok := gmDrums[key]; ok {
		return n, nil
	}
	if n, err := parseNote(lane); err == nil {
		return n, nil
	}
	if t != nil && t.anyLane {
		return 0, nil
	}
	known := sortedKeys(gmDrums)
	if t != nil {
		known = append(sortedKeys(t.DrumMap), known...)
	}
	return 0, fmt.Errorf("no drum lane %q (known: %s; or add it to the track's drum_map)", lane, strings.Join(known, ", "))
}

// patNote is one note of a compiled pattern, placed from the pattern's start.
type patNote struct {
	at, dur int64
	note    int
	vel     int
	// channel is the control channel a control note is sent on, 0 for a note
	// on the track's own channel.
	channel int
}

// compiledPattern is a pattern reduced to notes and a length.
type compiledPattern struct {
	ticks int64
	notes []patNote
}

// gridStep is one parsed step of a melodic grid line.
type gridStep struct {
	notes []int // empty for a rest or a tie
	tie   bool
	vel   int // 0 takes the pattern's velocity
	boost int
}

// compilePattern turns a pattern into notes placed from its start. t is the
// track that plays it: it supplies the drum map and the transposition. With a
// nil track the pattern is only checked for shape — drum lane names are not
// resolved.
func compilePattern(name string, p Pattern, m meter, songSwing float64, t *Track) (*compiledPattern, error) {
	at := "/patterns/" + name
	step, err := parseStep(p.Step)
	if err != nil {
		return nil, fmt.Errorf("%s/step: %w", at, err)
	}
	gate := p.Gate
	if gate == 0 {
		gate = defaultGate
	}
	vel := p.Velocity
	if vel == 0 {
		vel = defaultVelocity
	}
	swing := songSwing
	if p.Swing != nil {
		swing = *p.Swing
	}
	transpose := p.Transpose
	if t != nil {
		transpose += t.Transpose
	}
	// The grid of a pattern with a channel is control notes: sent as written.
	gridShift := transpose
	if p.Channel != 0 {
		gridShift = 0
	}
	stepsPerBar := 0
	if m.barTicks%step == 0 {
		stepsPerBar = int(m.barTicks / step)
	}
	// stepAt places a grid step, delaying every second one by the swing.
	stepAt := func(i int) int64 {
		pos := int64(i) * step
		if i%2 == 1 && swing > 0 {
			pos += int64(math.Round(swing * float64(step)))
		}
		return pos
	}

	out := &compiledPattern{}
	gridSteps := -1 // steps of the grid's lines, which must all agree
	sameLength := func(where string, n int) error {
		if gridSteps >= 0 && n != gridSteps {
			return fmt.Errorf("%s: %d steps, but the pattern's other lines have %d", where, n, gridSteps)
		}
		gridSteps = n
		return nil
	}

	for li, line := range p.Notes {
		where := fmt.Sprintf("%s/notes/%d", at, li)
		steps, err := parseGridLine(line, stepsPerBar)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", where, err)
		}
		if err := sameLength(where, len(steps)); err != nil {
			return nil, err
		}
		for i := 0; i < len(steps); i++ {
			st := steps[i]
			if len(st.notes) == 0 {
				continue
			}
			span := 1
			for i+span < len(steps) && steps[i+span].tie {
				span++
			}
			// The length runs to where the next step really starts, so a swung
			// note neither overlaps its neighbour nor leaves a gap at gate 1.
			dur := int64(math.Round(float64(stepAt(i+span)-stepAt(i)) * gate))
			if dur < minNoteTicks {
				dur = minNoteTicks
			}
			v := vel
			if st.vel > 0 {
				v = st.vel
			}
			v = clampVelocity(v + st.boost)
			for _, n := range st.notes {
				n += gridShift
				if n < 0 || n > 127 {
					return nil, fmt.Errorf("%s: step %d: %s transposed by %d leaves the MIDI range", where, i+1, NoteName(n-gridShift), gridShift)
				}
				out.notes = append(out.notes, patNote{at: stepAt(i), dur: dur, note: n, vel: v, channel: p.Channel})
			}
		}
	}

	for _, lane := range sortedKeys(p.Drums) {
		where := fmt.Sprintf("%s/drums/%s", at, lane)
		hits, err := parseDrumLane(p.Drums[lane], stepsPerBar)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", where, err)
		}
		if err := sameLength(where, len(hits)); err != nil {
			return nil, err
		}
		note := 0
		if t != nil {
			if note, err = drumNote(t, lane); err != nil {
				return nil, fmt.Errorf("%s: %w", where, err)
			}
		}
		dur := step / 2
		if dur < minNoteTicks {
			dur = minNoteTicks
		}
		for i, h := range hits {
			if h == 0 {
				continue
			}
			v := vel
			switch h {
			case 'X':
				v += accentBoost
			case 'o':
				v = int(math.Round(float64(vel) * softVelocity))
			}
			out.notes = append(out.notes, patNote{at: stepAt(i), dur: dur, note: note, vel: clampVelocity(v)})
		}
	}

	// eventsEnd is just past the last event's start: an event extends the
	// pattern by where it begins, not by how long it rings.
	var eventsEnd int64
	for i, ev := range p.Events {
		where := fmt.Sprintf("%s/events/%d", at, i)
		if ev.At < 0 || ev.Dur <= 0 {
			return nil, fmt.Errorf("%s: a note needs at >= 0 and dur > 0 (in beats)", where)
		}
		if ev.Velocity < 0 || ev.Velocity > 127 {
			return nil, fmt.Errorf("%s/vel: %d is not within 1..127", where, ev.Velocity)
		}
		names := strings.Fields(ev.Note)
		if len(names) == 0 {
			return nil, fmt.Errorf("%s/note: name the note(s)", where)
		}
		start := int64(math.Round(ev.At * PPQ))
		dur := int64(math.Round(ev.Dur * PPQ))
		if dur < minNoteTicks {
			dur = minNoteTicks
		}
		v := vel
		if ev.Velocity > 0 {
			v = ev.Velocity
		}
		channel := ev.Channel
		if channel == 0 {
			channel = p.Channel
		}
		for _, nm := range names {
			var n int
			var err error
			switch {
			case channel != 0:
				// A control note: a note as written, never a drum lane.
				n, err = parseNote(nm)
			case t != nil && t.Kind == "drums":
				n, err = drumNote(t, nm)
			case t == nil:
				// Shape check only: a drum lane name is fine here.
				if n, err = parseNote(nm); err != nil {
					n, err = 0, nil
				}
			default:
				n, err = parseNote(nm)
				n += transpose
			}
			if err != nil {
				return nil, fmt.Errorf("%s/note: %w", where, err)
			}
			if n < 0 || n > 127 {
				return nil, fmt.Errorf("%s/note: %q transposed by %d leaves the MIDI range", where, nm, transpose)
			}
			out.notes = append(out.notes, patNote{at: start, dur: dur, note: n, vel: v, channel: channel})
		}
		if start+1 > eventsEnd {
			eventsEnd = start + 1
		}
	}

	// Length: the declared bars, or the grid rounded up to whole bars.
	var gridTicks int64
	if gridSteps > 0 {
		gridTicks = int64(gridSteps) * step
	}
	if p.Bars > 0 {
		out.ticks = int64(p.Bars) * m.barTicks
		if gridTicks > out.ticks {
			return nil, fmt.Errorf("%s: the grid is %d steps but %d bar(s) hold %d", at, gridSteps, p.Bars, out.ticks/step)
		}
	} else {
		if gridTicks%m.barTicks != 0 {
			return nil, fmt.Errorf("%s: the grid is %d steps, which is not whole bars (a bar is %d steps of %s); fix the count or set bars", at, gridSteps, m.barTicks/step, stepName(p.Step))
		}
		extent := gridTicks
		if eventsEnd > extent {
			extent = eventsEnd
		}
		bars := (extent + m.barTicks - 1) / m.barTicks
		if bars < 1 {
			bars = 1
		}
		out.ticks = bars * m.barTicks
	}

	sort.SliceStable(out.notes, func(i, j int) bool { return out.notes[i].at < out.notes[j].at })
	return out, nil
}

// stepName renders a pattern's step for a message.
func stepName(step string) string {
	if strings.TrimSpace(step) == "" {
		return "1/16"
	}
	return step
}

func clampVelocity(v int) int {
	if v < 1 {
		return 1
	}
	if v > 127 {
		return 127
	}
	return v
}

// barChecker counts steps between bar lines and reports a bar of the wrong
// length. A grid without bar lines is not checked.
type barChecker struct {
	stepsPerBar int
	bar         int // 1-based bar being filled
	inBar       int
	seenLine    bool
}

// line handles a "|": it closes the current bar.
func (b *barChecker) line() error {
	if !b.seenLine && b.inBar == 0 {
		b.seenLine = true // a leading bar line
		return nil
	}
	b.seenLine = true
	if err := b.close(); err != nil {
		return err
	}
	b.bar++
	b.inBar = 0
	return nil
}

func (b *barChecker) close() error {
	if b.stepsPerBar > 0 && b.inBar != b.stepsPerBar {
		return fmt.Errorf("bar %d has %d steps, a bar holds %d", b.bar+1, b.inBar, b.stepsPerBar)
	}
	return nil
}

// end checks the last bar once the grid is read.
func (b *barChecker) end() error {
	if !b.seenLine || b.inBar == 0 {
		return nil
	}
	return b.close()
}

// parseGridLine parses one melodic grid line into steps.
func parseGridLine(line string, stepsPerBar int) ([]gridStep, error) {
	var steps []gridStep
	bars := barChecker{stepsPerBar: stepsPerBar}
	i := 0
	for i < len(line) {
		c := line[i]
		switch c {
		case ' ', '\t', '\n', '\r':
			i++
			continue
		case '|':
			if err := bars.line(); err != nil {
				return nil, err
			}
			i++
			continue
		}
		// One token: a chord in brackets or a run of non-space characters,
		// plus any suffix.
		start := i
		if c == '[' {
			end := strings.IndexByte(line[i:], ']')
			if end < 0 {
				return nil, fmt.Errorf("step %d: chord %q is missing its ]", len(steps)+1, line[i:])
			}
			i += end + 1
		}
		for i < len(line) && !strings.ContainsRune(" \t\n\r|", rune(line[i])) {
			i++
		}
		st, err := parseGridToken(line[start:i])
		if err != nil {
			return nil, fmt.Errorf("step %d: %w", len(steps)+1, err)
		}
		if st.tie && (len(steps) == 0 || (!steps[len(steps)-1].tie && len(steps[len(steps)-1].notes) == 0)) {
			// Nothing to hold: a tie after silence is silence.
			st.tie = false
		}
		steps = append(steps, st)
		bars.inBar++
	}
	if err := bars.end(); err != nil {
		return nil, err
	}
	return steps, nil
}

// parseGridToken parses one step of a melodic grid.
func parseGridToken(tok string) (gridStep, error) {
	switch tok {
	case ".":
		return gridStep{}, nil
	case "-":
		return gridStep{tie: true}, nil
	}
	var st gridStep
	body := tok
	// Suffixes: "!" accents, ":NN" sets the velocity.
	if j := strings.LastIndexByte(body, ':'); j >= 0 && !strings.Contains(body[j:], "]") {
		v, err := strconv.Atoi(strings.TrimSuffix(body[j+1:], "!"))
		if err != nil || v < 1 || v > 127 {
			return st, fmt.Errorf("%q: the velocity after \":\" must be 1..127", tok)
		}
		st.vel = v
		if strings.HasSuffix(body, "!") {
			st.boost = accentBoost
		}
		body = body[:j]
	}
	if strings.HasSuffix(body, "!") {
		st.boost = accentBoost
		body = strings.TrimSuffix(body, "!")
	}
	names := []string{body}
	if strings.HasPrefix(body, "[") {
		if !strings.HasSuffix(body, "]") {
			return st, fmt.Errorf("%q: after a chord's ] only \"!\" or \":velocity\" may follow", tok)
		}
		names = strings.Fields(body[1 : len(body)-1])
		if len(names) == 0 {
			return st, fmt.Errorf("%q: an empty chord", tok)
		}
	}
	for _, nm := range names {
		n, err := parseNote(nm)
		if err != nil {
			return st, err
		}
		st.notes = append(st.notes, n)
	}
	return st, nil
}

// parseDrumLane parses one drum lane into hits: 0 for silence, else the hit
// character ('x', 'X' or 'o').
func parseDrumLane(lane string, stepsPerBar int) ([]byte, error) {
	var hits []byte
	bars := barChecker{stepsPerBar: stepsPerBar}
	for i := 0; i < len(lane); i++ {
		switch c := lane[i]; c {
		case ' ', '\t', '\n', '\r':
		case '|':
			if err := bars.line(); err != nil {
				return nil, err
			}
		case '.', '-':
			hits = append(hits, 0)
			bars.inBar++
		case 'x', 'X', 'o':
			hits = append(hits, c)
			bars.inBar++
		default:
			return nil, fmt.Errorf("step %d: %q is not a drum step (x hit, X accent, o soft, . silence)", len(hits)+1, string(c))
		}
	}
	if err := bars.end(); err != nil {
		return nil, err
	}
	return hits, nil
}
