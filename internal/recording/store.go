package recording

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/teemow/mcp-midi-controller/internal/audiotap"
	"github.com/teemow/mcp-midi-controller/internal/lanhttp"
)

// Meta is the record kept next to a recording: what it is, how it was cut,
// how loud it is and where a copy of it went.
type Meta struct {
	// Name identifies the recording; File is its audio file in the store.
	Name   string `json:"name"`
	File   string `json:"file"`
	Format Format `json:"format"`
	// Song is the id of the song that was playing, Title its title (or the
	// name the recording was given).
	Song  string `json:"song,omitempty"`
	Title string `json:"title,omitempty"`
	// Tap is the audio tap it was recorded from.
	Tap        string    `json:"tap,omitempty"`
	RecordedAt time.Time `json:"recorded_at"`

	Seconds    float64 `json:"seconds"`
	SampleRate int     `json:"sample_rate"`
	Channels   int     `json:"channels"`
	BitDepth   int     `json:"bit_depth"`
	Bytes      int64   `json:"bytes"`

	PeakDBFS float64 `json:"peak_dbfs"`
	RMSDBFS  float64 `json:"rms_dbfs"`
	Clipped  int64   `json:"clipped,omitempty"`
	// Silent: no sample reached the silence threshold, so nothing was cut.
	Silent bool `json:"silent,omitempty"`

	// TrimmedLeadMS and TrimmedTailMS are what was cut before the first sound
	// and after the last; FadeOutMS how much of the end is faded out: the
	// second kept after the last sound, or the shorter fade of a take that
	// was still sounding when it ended.
	TrimmedLeadMS int64 `json:"trimmed_lead_ms"`
	TrimmedTailMS int64 `json:"trimmed_tail_ms"`
	FadeOutMS     int64 `json:"fade_out_ms,omitempty"`

	// CapturedSeconds is the audio the tap delivered while the recording ran
	// and WallSeconds the time that took. They differ when the stream fell
	// behind; Stalls says where (in frames of the capture, before the cut).
	CapturedSeconds float64          `json:"captured_seconds"`
	WallSeconds     float64          `json:"wall_seconds"`
	Stalls          []audiotap.Stall `json:"stalls,omitempty"`
	// Ended says why the capture ended, when it was not asked to.
	Ended string `json:"ended,omitempty"`

	// Drive is the copy in Google Drive, once uploaded.
	Drive *DriveCopy `json:"drive,omitempty"`
}

// DriveCopy is a recording's copy in Google Drive.
type DriveCopy struct {
	FileID     string    `json:"file_id"`
	Name       string    `json:"name"`
	Link       string    `json:"link"`
	Folder     string    `json:"folder,omitempty"`
	UploadedAt time.Time `json:"uploaded_at"`
}

// Store is the directory of recordings: for each one its audio file and its
// record (<name>.json).
type Store struct {
	dir string
}

// NewStore returns the store rooted at dir. The directory is created on first
// use.
func NewStore(dir string) *Store { return &Store{dir: dir} }

// Dir is the store's directory.
func (s *Store) Dir() string { return s.dir }

// partSuffix marks a capture that is still running or was never finished.
const partSuffix = ".f32.part"

// CleanName turns a title into a file name: the title as written, minus what
// a path cannot hold.
func CleanName(title string) string {
	var b strings.Builder
	for _, r := range title {
		switch {
		case r == '/' || r == '\\' || r == ':' || r < ' ':
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	return strings.Trim(strings.Join(strings.Fields(b.String()), " "), ". ")
}

// NewName returns a name no recording in the store has yet: the title and the
// time, so takes of one song sort in the order they were made.
func (s *Store) NewName(title string, at time.Time) (string, error) {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return "", err
	}
	base := strings.TrimSpace(CleanName(title) + " " + at.Format("2006-01-02 1504"))
	name := base
	for n := 2; s.taken(name); n++ {
		name = fmt.Sprintf("%s (%d)", base, n)
	}
	return name, nil
}

// taken reports whether any file of a recording called name exists.
func (s *Store) taken(name string) bool {
	for _, ext := range []string{".json", "." + string(FLAC), "." + string(WAV), partSuffix} {
		if _, err := os.Stat(filepath.Join(s.dir, name+ext)); err == nil {
			return true
		}
	}
	return false
}

// PartPath is where the capture of the recording called name is written while
// it runs.
func (s *Store) PartPath(name string) string { return filepath.Join(s.dir, name+partSuffix) }

// AudioPath is the audio file of a recording.
func (s *Store) AudioPath(m Meta) string { return filepath.Join(s.dir, m.File) }

// Save writes a recording's record.
func (s *Store) Save(m Meta) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return lanhttp.WriteFileAtomic(filepath.Join(s.dir, m.Name+".json"), data, 0o644)
}

// ErrNotFound is returned for a recording the store does not hold.
var ErrNotFound = errors.New("no such recording")

// Get loads the record of the recording called name. The name may carry the
// audio file's extension.
func (s *Store) Get(name string) (Meta, error) {
	name = strings.TrimSpace(name)
	for _, ext := range []string{"." + string(FLAC), "." + string(WAV), ".json"} {
		name = strings.TrimSuffix(name, ext)
	}
	if name == "" || name != filepath.Base(name) {
		return Meta{}, ErrNotFound
	}
	return s.load(filepath.Join(s.dir, name+".json"))
}

func (s *Store) load(path string) (Meta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Meta{}, ErrNotFound
		}
		return Meta{}, err
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return Meta{}, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	if _, err := os.Stat(filepath.Join(s.dir, m.File)); err != nil || m.File != filepath.Base(m.File) {
		return Meta{}, ErrNotFound
	}
	return m, nil
}

// List returns the recordings, newest first. A record that does not read, or
// whose audio is gone, is left out.
func (s *Store) List() ([]Meta, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Meta
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if m, err := s.load(filepath.Join(s.dir, e.Name())); err == nil {
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].RecordedAt.After(out[j].RecordedAt) })
	return out, nil
}

// Prune keeps the newest keep recordings and deletes the rest, audio and
// record. keep <= 0 keeps everything. It returns the names it deleted.
func (s *Store) Prune(keep int) ([]string, error) {
	if keep <= 0 {
		return nil, nil
	}
	all, err := s.List()
	if err != nil || len(all) <= keep {
		return nil, err
	}
	var gone []string
	for _, m := range all[keep:] {
		_ = os.Remove(filepath.Join(s.dir, m.File))
		if err := os.Remove(filepath.Join(s.dir, m.Name+".json")); err == nil {
			gone = append(gone, m.Name)
		}
	}
	return gone, nil
}

// RemoveStaleParts deletes captures that were never finished (the daemon
// stopped while they ran), sparing the ones named in running.
func (s *Store) RemoveStaleParts(running ...string) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	keep := map[string]bool{}
	for _, name := range running {
		keep[name+partSuffix] = true
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), partSuffix) && !keep[e.Name()] {
			_ = os.Remove(filepath.Join(s.dir, e.Name()))
		}
	}
}

// Finish turns a stopped capture into a recording in the store: it cuts and
// encodes the take, deletes the capture file and writes the record. m carries
// what the caller knows (name, song, title, tap, when); Finish fills in the
// rest. The capture file is deleted even when encoding fails, so a failed
// recording does not stay on the disk.
func (s *Store) Finish(m Meta, st audiotap.CaptureState, format Format, trim bool) (Meta, error) {
	raw := s.PartPath(m.Name)
	defer func() { _ = os.Remove(raw) }()

	rate := int(st.SampleRate)
	take := Take{
		RawPath: raw, Channels: st.Channels, SampleRate: rate, Frames: st.Frames,
		FirstSound: st.FirstSound, LastSound: st.LastSound,
	}
	if take.Frames == 0 {
		return Meta{}, errors.New("the tap delivered no audio while the recording ran")
	}
	cut := PlanCut(take, trim)

	m.Format = format
	m.File = m.Name + "." + string(format)
	m.SampleRate, m.Channels, m.BitDepth = rate, st.Channels, format.BitDepth()
	levels, err := Encode(filepath.Join(s.dir, m.File), take, cut, format, Tags{
		Title:   m.Title,
		Date:    m.RecordedAt.Format("2006-01-02"),
		Comment: "recorded by mcp-midi-controller",
	})
	if err != nil {
		return Meta{}, err
	}
	info, err := os.Stat(filepath.Join(s.dir, m.File))
	if err != nil {
		return Meta{}, err
	}
	ms := func(frames int64) int64 { return frames * 1000 / int64(rate) }
	m.Bytes = info.Size()
	m.Seconds = float64(cut.End-cut.Start) / float64(rate)
	m.PeakDBFS, m.RMSDBFS, m.Clipped = levels.PeakDBFS, levels.RMSDBFS, levels.Clipped
	m.Silent = st.FirstSound < 0
	m.TrimmedLeadMS, m.TrimmedTailMS, m.FadeOutMS = ms(cut.Start), ms(st.Frames-cut.End), ms(cut.FadeOut)
	m.CapturedSeconds = float64(st.Frames) / float64(rate)
	if !st.LastAudio.IsZero() {
		m.WallSeconds = st.LastAudio.Sub(st.Started).Seconds()
	}
	m.Stalls, m.Ended = st.Stalls, st.Ended
	if err := s.Save(m); err != nil {
		_ = os.Remove(filepath.Join(s.dir, m.File))
		return Meta{}, err
	}
	return m, nil
}
