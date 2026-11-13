package song

// This file is what a song writes into a drum machine's saved state: its step
// sequence (stepseq.go), a sample per voice and the parameters its variation
// leaves alone (its locks). None of them is a parameter, so none of them is
// sent: battalion.go writes them into UA Battalion's state when the song's
// session is authored, and a scene cannot change them.

import (
	"fmt"
	"hash/fnv"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// kitState is what a song writes into an instrument's saved state.
type kitState struct {
	at      string        // where the document says it: the instrument
	seq     *stepSequence // nil without one
	samples []voiceSample // by voice
	locks   []string      // sorted
}

// voiceSample is one voice's sample.
type voiceSample struct {
	voice int // 1-based
	// path is the sample by its path in the plugin's factory library
	// ("Kicks/707 Kick.wav"), a file URL as the plugin keeps it, or "" for
	// none.
	path string
}

// isURL reports whether the sample is written as a URL rather than a path in
// the factory library.
func (s voiceSample) isURL() bool { return strings.HasPrefix(s.path, "file://") }

// battalionLibrary is the plugin's factory library as its saved state names
// it: in the app bundle on the device. The bundle's id changes whenever the app
// is installed again or updated, and the plugin finds a sample by what follows
// "Unfiltered Audio/Sample Library/" in the URL, whatever bundle comes before
// it (seen on the rig: docs/research/battalion.md). So a song names the
// library through a bundle id that stands for any.
const battalionLibrary = "/private/var/containers/Bundle/Application/00000000-0000-0000-0000-000000000000/UA Battalion.app/PlugIns/UA Battalion.appex/Unfiltered Audio/Sample Library"

// libraryURL is the URL the plugin's state keeps for a sample of its factory
// library, by its path there ("Kicks/707 Kick.wav").
func libraryURL(path string) string {
	u := url.URL{Scheme: "file", Path: battalionLibrary + "/" + path}
	return u.String()
}

// newKitState checks what an instrument writes into its saved state. at is
// where the instrument sits in the document, m the song's meter. It returns
// nil after reporting a problem.
func newKitState(e *errs, at string, pl Plugin, m meter) *kitState {
	before := len(e.list)
	k := &kitState{at: at}
	if len(pl.Sequence) > 0 {
		k.seq = checkStepSequence(e, at+"/sequence", pl.Sequence, m)
	}
	k.samples = checkSamples(e, at+"/samples", pl.Samples)
	k.locks = checkLocks(e, at+"/locks", pl.Locks)
	if len(e.list) > before {
		return nil
	}
	return k
}

// checkSamples checks an instrument's samples and returns them by voice. Two
// keys of one voice ("1" and "01") are refused: either sample could win, and
// the session's fingerprint would change from one run to the next.
func checkSamples(e *errs, at string, samples map[string]string) []voiceSample {
	var out []voiceSample
	keyOf := map[int]string{}
	for _, key := range sortedKeys(samples) {
		vat := at + "/" + key
		voice, err := strconv.Atoi(strings.TrimSpace(key))
		if err != nil || voice < 1 || voice > sequenceVoices {
			e.addf("%s: %q is not a voice (1..%d)", vat, key, sequenceVoices)
			continue
		}
		if other, ok := keyOf[voice]; ok {
			e.addf("%s: voice %d has two samples (under %q and %q)", vat, voice, other, key)
			continue
		}
		keyOf[voice] = key
		path := strings.TrimSpace(samples[key])
		if err := checkSamplePath(path); err != nil {
			e.addf("%s: %v", vat, err)
			continue
		}
		out = append(out, voiceSample{voice: voice, path: path})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].voice < out[j].voice })
	return out
}

// checkSamplePath checks a sample: a path in the factory library, a file
// URL, or "" for none.
func checkSamplePath(path string) error {
	switch {
	case path == "", strings.HasPrefix(path, "file://"):
		return nil
	case strings.Contains(path, "://"):
		return fmt.Errorf("%q: a sample is a path in the factory library (\"Kicks/707 Kick.wav\") or a file:// URL", path)
	case strings.HasPrefix(path, "/"), strings.Contains(path, `\`):
		return fmt.Errorf("%q: a sample of the factory library is its path there, with \"/\" between its folders and no \"/\" in front (\"Kicks/707 Kick.wav\")", path)
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("%q: %q is not a folder or file of the factory library", path, part)
		}
	}
	return nil
}

// checkLocks checks an instrument's locks and returns them sorted. Whether
// each is a parameter of the plugin is checked against its probe (Plan).
func checkLocks(e *errs, at string, locks []string) []string {
	seen := map[string]bool{}
	var out []string
	for i, id := range locks {
		id = strings.TrimSpace(id)
		switch {
		case id == "":
			e.addf("%s/%d: a lock is the id of a parameter (drum1pitch)", at, i)
		case seen[id]:
			e.addf("%s/%d: %s is locked twice", at, i, id)
		default:
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// fingerprint identifies what the state writes: another one is another
// session.
func (k *kitState) fingerprint() string {
	h := fnv.New64a()
	if k.seq != nil {
		_, _ = fmt.Fprintf(h, "q%s|", k.seq.fingerprint())
	}
	for _, s := range k.samples {
		_, _ = fmt.Fprintf(h, "s%d:%s|", s.voice, s.path)
	}
	for _, id := range k.locks {
		_, _ = fmt.Fprintf(h, "l%s|", id)
	}
	return fmt.Sprintf("%016x", h.Sum64())
}

// says lists what the state writes, for a message: "its own sequence, 2
// samples and 3 locks".
func (k *kitState) says() string {
	var parts []string
	if k.seq != nil {
		parts = append(parts, "its own sequence")
	}
	if n := len(k.samples); n > 0 {
		parts = append(parts, plural(n, "sample"))
	}
	if n := len(k.locks); n > 0 {
		parts = append(parts, plural(n, "lock"))
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}
