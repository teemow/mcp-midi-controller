package mcpserver

// This file is the automatic side of the session rig: load a session from the
// iPad into AUM and — without further tool calls — the daemon re-derives the
// control rig and pushes the control-surface manifest to the ProbeMidiBrain.
//
// Three hook points drive it (no event bus):
//   - OnAUMSessionDownloaded: the aum receiver saw the iPad download a staged
//     .aumproj, which most often means that session is about to be loaded.
//     The daemon tracks it as its "current session" (persisted in the state
//     dir) and runs the import.
//   - OnMidiControlConnected: the brain (re)connected — re-run the import for
//     the current session and push the manifest, so a brain that was offline
//     during the download still receives the surface.
//   - OnAudioTapConnected: the master tap of a session the daemon authored
//     dialled in. It does so under the session's id, so this is the session
//     AUM has open, whatever was downloaded last: it becomes the current
//     session, and its rig is imported and pushed unless it is there already
//     (sessionOpen). load_song says the same of the session it loaded, for a
//     tap that gives no name.
//
// A download is a guess at what AUM will load, and the guess is wrong when a
// session is loaded that the iPad holds already: load_song then skips the
// pull, nothing is downloaded, and the marker still names the session
// downloaded last. On the rig the brain of the session just loaded was sent
// the control surface of another one that way. The tap is not a guess, which
// is why it has the last word.
//
// The imports are gated by the aum_auto_import config flag
// (WithAUMAutoImport); current-session tracking itself is always on (it is
// cheap and the manual import_aum_session path uses it too).

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/teemow/aum-session-go/aum"
	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/mcp-midi-controller/internal/lanhttp"
	"github.com/teemow/mcp-midi-controller/internal/song"
	"github.com/teemow/midi-device/device"
	"github.com/teemow/midi-transport/midicontrol"
)

// WithAUMAutoImport enables the automatic session-rig import (config
// aum_auto_import). Default (no option) is off: imports stay tool-driven.
func WithAUMAutoImport(enabled bool) Option {
	return func(s *Server) { s.aumAutoImport = enabled }
}

// --- hook points ------------------------------------------------------------

// OnAUMSessionDownloaded is the aum receiver's download callback: the iPad
// fetched a staged file (identified by its staging-dir-relative path, which
// may carry subfolder segments), so (for a .aumproj) that session becomes the
// daemon's current session and — with auto-import enabled — its rig is
// imported and the control-surface manifest pushed. Midimap downloads are
// ignored (nothing to import). It runs synchronously; the receiver invokes it
// after the response is served, so the download itself is never delayed.
func (s *Server) OnAUMSessionDownloaded(file string) {
	if aum.FileKind(file) != aum.KindSession {
		return
	}
	id := aum.StripExt(file)
	s.setCurrentAUMSession(id)
	s.noteSessionPulled(id)
	if !s.aumAutoImport {
		return
	}
	s.importCurrentAUMSession("session downloaded")
}

// OnMidiControlConnected is the brain-connect callback: re-run the import for
// the daemon's current session and push the manifest so the freshly connected
// brain renders the surface. The midicontrol receiver requires its callbacks
// not to block, so the work runs in a goroutine (tracked by s.bg).
func (s *Server) OnMidiControlConnected() {
	if !s.aumAutoImport {
		return
	}
	s.bg.Go(s.reimportCurrentAUMSession)
}

// reimportCurrentAUMSession is OnMidiControlConnected's body: a no-op when no
// session was ever downloaded/imported.
func (s *Server) reimportCurrentAUMSession() {
	s.importCurrentAUMSession("brain connected")
}

// OnAudioTapConnected is the tap receiver's connect callback. The master tap
// of a session the daemon authored dials in under the session's id
// (song.TapName), so its connection says which session AUM has open, however
// it came to be open: load_song, its file tapped in AUM, a session switch.
// That session is the daemon's current one from then on (sessionOpen). Every
// other tap says nothing: a track's, one without a name, and one whose name
// is not a staged session's (a tap named by hand). The work runs in a
// goroutine (tracked by s.bg), so the tap's stream is read meanwhile.
func (s *Server) OnAudioTapConnected(name string) {
	id, strip, ok := song.ParseTapName(name)
	if !ok || strip != song.MasterTap {
		return
	}
	if path, err := resolveAUMSessionPath("", id); err != nil || !isFile(path) {
		return
	}
	s.bg.Go(func() { s.tapOpen(id, "the session's tap connected") })
}

// tapOpen is OnAudioTapConnected's body: sessionOpen, for a session whose
// master tap is still the one that connected last when its turn comes. It
// waits for currentMu behind the imports before it, and meanwhile the tap may
// have dropped or another session's master may have dialled in: the older
// word would then replace the marker, the rig and the surface of the session
// AUM has open with its own.
func (s *Server) tapOpen(id, trigger string) {
	s.currentMu.Lock()
	defer s.currentMu.Unlock()
	if s.audio != nil {
		if open := s.tapSession(); open != id {
			log.Printf("current AUM session: %q's tap is not the newest master (that is %q): left as is", id, open)
			return
		}
	}
	s.sessionOpenLocked(id, trigger)
}

func isFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}

// sessionOpen takes note that AUM has the staged session id open: it is the
// daemon's current session, and with auto-import its rig is the one imported
// and its control surface the one the brain has. The import is left out when
// the session was the current one already and its rig was imported since the
// brain connected: at a load both the tap's connect and load_song say the
// same thing, a moment after the brain's connect imported the rig.
func (s *Server) sessionOpen(id, trigger string) {
	s.currentMu.Lock()
	defer s.currentMu.Unlock()
	s.sessionOpenLocked(id, trigger)
}

// sessionOpenLocked is sessionOpen for a caller that holds currentMu.
func (s *Server) sessionOpenLocked(id, trigger string) {
	was := s.currentAUMSessionID()
	if was != id {
		log.Printf("current AUM session: %q, not %q (%s)", id, was, trigger)
		s.setCurrentAUMSession(id)
	}
	if !s.aumAutoImport || (was == id && s.rig.is(id, s.loaded.generation())) {
		return
	}
	s.autoImportSession(id, trigger)
}

// importCurrentAUMSession imports the rig of the current session, as the
// marker names it when the import's turn comes, and pushes its control
// surface. The marker is read under the lock that every automatic import
// holds, so the import that runs last is the one of the session named last:
// a brain's connect that read the marker before a tap's connect changed it
// would otherwise push the older session's surface after the newer one's.
func (s *Server) importCurrentAUMSession(trigger string) {
	s.currentMu.Lock()
	defer s.currentMu.Unlock()
	if id := s.currentAUMSessionID(); id != "" {
		s.autoImportSession(id, trigger)
	}
}

// importedRig is the session whose rig the last automatic import brought in,
// and the brain connection it was pushed to (loadedSession's count).
type importedRig struct {
	mu      sync.Mutex
	session string
	brain   uint64
}

func (r *importedRig) set(session string, brain uint64) {
	r.mu.Lock()
	r.session, r.brain = session, brain
	r.mu.Unlock()
}

func (r *importedRig) is(session string, brain uint64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.session == session && r.brain == brain
}

// autoImportSession runs one auto-import for the staged session id: import the
// rig, broadcast the aum-rig notification, push the control-surface manifest.
// Failures are logged and broadcast (an agent watching the Activity feed sees
// the rig did NOT change), never fatal. Its callers hold currentMu.
func (s *Server) autoImportSession(id, trigger string) {
	path, err := resolveAUMSessionPath("", id)
	if err != nil {
		log.Printf("aum auto-import %q (%s): %v", id, trigger, err)
		return
	}
	// A brain that connects from here on has not seen this push: it is not
	// the one the rig is noted for.
	brain := s.loaded.generation()
	o, err := s.importSessionRig(path, false)
	if err != nil {
		log.Printf("aum auto-import %q (%s): %v", id, trigger, err)
		s.broadcast("aum-rig", map[string]any{
			"session": id,
			"trigger": trigger,
			"error":   err.Error(),
		})
		return
	}
	log.Printf("aum auto-import %q (%s): %d device(s), %d control(s), %d replaced",
		id, trigger, len(o.surface), o.rig.Controls(), len(o.replaced))
	s.notifyAUMRig(trigger, o)
	s.pushControlSurface(o)
	s.rig.set(id, brain)
}

// notifyAUMRig broadcasts that the session rig changed (devices were created /
// replaced by an import), so the Activity feed and agents see the rig change
// without polling. Like the other notifiers, clients receive it only after
// setting a logging level.
func (s *Server) notifyAUMRig(trigger string, o *aumImportOutcome) {
	names := make([]string, 0, len(o.created))
	for _, c := range o.created {
		if c.Kind != "" { // skip warning rows
			names = append(names, c.Name)
		}
	}
	s.broadcast("aum-rig", map[string]any{
		"session":  o.sessionID,
		"title":    o.title,
		"trigger":  trigger,
		"devices":  names,
		"controls": o.rig.Controls(),
		"replaced": o.replaced,
		"skipped":  len(o.rig.Skipped),
		"hint":     "session rig imported; the control_* tools now mirror the session's mappings",
	})
}

// --- current-session tracking ------------------------------------------------

// currentAUMSession is the persisted current-session marker (state dir): the
// staged session AUM has open as far as the daemon knows, so a brain connect
// after a daemon restart still re-imports it. That is the session whose
// master tap dialled in last or that load_song loaded last, and since then
// the one the iPad downloaded, a session switch named or an import created a
// rig for.
type currentAUMSession struct {
	ID string `json:"id"`
	At string `json:"at"` // RFC3339
}

// setCurrentAUMSession records id as the daemon's current session. A write
// failure only costs re-import-on-restart, so it is logged, not surfaced.
func (s *Server) setCurrentAUMSession(id string) {
	cur := currentAUMSession{ID: id, At: time.Now().UTC().Format(time.RFC3339)}
	data, err := json.Marshal(cur)
	if err != nil {
		log.Printf("record current AUM session: %v", err)
		return
	}
	path := config.CurrentAUMSessionPath()
	if err := os.MkdirAll(config.StateDir(), 0o755); err != nil {
		log.Printf("record current AUM session: %v", err)
		return
	}
	if err := lanhttp.WriteFileAtomic(path, data, 0o644); err != nil {
		log.Printf("record current AUM session: %v", err)
	}
}

// currentAUMSessionID reads the persisted current session ("" when none was
// ever recorded or the marker is unreadable).
func (s *Server) currentAUMSessionID() string {
	data, err := os.ReadFile(config.CurrentAUMSessionPath())
	if err != nil {
		return ""
	}
	var cur currentAUMSession
	if err := json.Unmarshal(data, &cur); err != nil {
		return ""
	}
	return cur.ID
}

// --- control-surface manifest push --------------------------------------------

// pushControlSurface sends the controlSurface manifest derived from an import
// outcome to the connected brain. No connected brain is the normal offline
// case (ErrNoBrain, silent — the brain-connect hook pushes later); other send
// failures are logged.
func (s *Server) pushControlSurface(o *aumImportOutcome) {
	if s.midi == nil || len(o.surface) == 0 {
		return
	}
	frame := s.buildControlSurface(o)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.midi.SendJSON(ctx, frame); err != nil {
		if !errors.Is(err, midicontrol.ErrNoBrain) {
			log.Printf("push control surface for session %q: %v", o.sessionID, err)
		}
		return
	}
	log.Printf("pushed control surface for session %q (%d device(s))", o.sessionID, len(frame.Devices))
}

// buildControlSurface reduces the import outcome's created devices to the
// compact controlSurface frame the brain renders: per control its widget kind
// plus the exact wire message — the same DeriveRig output the control_* tools
// speak, so surface taps and tool calls emit identical MIDI. The session-switch
// registry rides along as the sessions section, so EVERY import/connect push
// carries the switcher (and the brain fullState-caches it per session).
func (s *Server) buildControlSurface(o *aumImportOutcome) midicontrol.ControlSurface {
	cs := midicontrol.ControlSurface{
		Type:     midicontrol.ControlSurfaceType,
		Session:  o.sessionID,
		Title:    o.title,
		Sessions: s.surfaceSessions(),
	}
	for _, src := range o.surface {
		dev := midicontrol.SurfaceDevice{Name: src.logical}
		for i := range src.dt.Controls {
			if sc, ok := surfaceControl(&src.dt.Controls[i], src.send); ok {
				dev.Controls = append(dev.Controls, sc)
			}
		}
		if len(dev.Controls) > 0 {
			cs.Devices = append(cs.Devices, dev)
		}
	}
	return cs
}

// surfaceControl reduces one device control to its surface form. ok is false
// for control types the brain protocol cannot emit (sysex/osc/nrpn) — session-
// derived rigs never contain those, but a hand-edited type must not break the
// frame.
func surfaceControl(c *device.Control, bindingSend int) (midicontrol.SurfaceControl, bool) {
	msg := midicontrol.SurfaceMsg{Channel: c.WireChannel(clampSend(bindingSend)-1) + 1}
	switch c.Type {
	case device.ControlCC:
		msg.Type = "cc"
		if c.CC != nil {
			msg.Number = *c.CC
		}
	case device.ControlNoteOn:
		msg.Type = "noteOn"
		if c.CC != nil { // note number reuses the CC field (see device.Control)
			msg.Number = *c.CC
		}
	case device.ControlNoteOff:
		msg.Type = "noteOff"
		if c.CC != nil {
			msg.Number = *c.CC
		}
	case device.ControlProgramChange:
		msg.Type = "pc"
		if c.Program != nil {
			msg.Number = *c.Program
		}
	default:
		return midicontrol.SurfaceControl{}, false
	}

	sc := midicontrol.SurfaceControl{Name: c.Name, Msg: msg}
	if c.Type == device.ControlProgramChange {
		sc.Widget = "preset"
		return sc, true
	}
	switch c.Value.Type {
	case device.ValueEnum:
		sc.Values = surfaceValues(c.Value.Values)
		switch len(sc.Values) {
		case 1:
			sc.Widget = "trigger"
		case 2:
			sc.Widget = "toggle"
		default:
			sc.Widget = "enum"
		}
	default:
		sc.Widget = "fader"
		if c.Value.Min != nil {
			v := int(*c.Value.Min)
			sc.Min = &v
		}
		if c.Value.Max != nil {
			v := int(*c.Value.Max)
			sc.Max = &v
		}
	}
	return sc, true
}

// surfaceValues orders an enum's label->value map by wire value (then label)
// so the rendered widget is deterministic across pushes.
func surfaceValues(values map[string]int) []midicontrol.SurfaceValue {
	out := make([]midicontrol.SurfaceValue, 0, len(values))
	for label, v := range values {
		out = append(out, midicontrol.SurfaceValue{Label: label, Value: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Value != out[j].Value {
			return out[i].Value < out[j].Value
		}
		return out[i].Label < out[j].Label
	})
	return out
}
