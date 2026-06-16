package mcpserver

// This file is the automatic side of the session rig: load a session from the
// iPad into AUM and — without further tool calls — the daemon re-derives the
// control rig and pushes the control-surface manifest to the ProbeMidiBrain.
//
// Two hook points drive it (no event bus):
//   - OnAUMSessionDownloaded: the aum receiver saw the iPad download a staged
//     .aumproj — the surest signal that session is about to be loaded. The
//     daemon tracks it as its "current session" (persisted in the state dir)
//     and runs the import.
//   - OnMidiControlConnected: the brain (re)connected — re-run the import for
//     the current session and push the manifest, so a brain that was offline
//     during the download still receives the surface.
//
// Both are gated by the aum_auto_import config flag (WithAUMAutoImport);
// current-session tracking itself is always on (it is cheap and the manual
// import_aum_session path uses it too).

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"sort"
	"time"

	"github.com/teemow/aum-session-go/aum"
	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/midi-device/device"
	"github.com/teemow/mcp-midi-controller/internal/lanhttp"
	"github.com/teemow/mcp-midi-controller/internal/midicontrol"
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
	if !s.aumAutoImport {
		return
	}
	s.autoImportSession(id, "session downloaded")
}

// OnMidiControlConnected is the brain-connect callback: re-run the import for
// the daemon's current session and push the manifest so the freshly connected
// brain renders the surface. The midicontrol receiver requires its callbacks
// not to block, so the work runs in a goroutine.
func (s *Server) OnMidiControlConnected() {
	if !s.aumAutoImport {
		return
	}
	go s.reimportCurrentAUMSession()
}

// reimportCurrentAUMSession is OnMidiControlConnected's body: a no-op when no
// session was ever downloaded/imported.
func (s *Server) reimportCurrentAUMSession() {
	id := s.currentAUMSessionID()
	if id == "" {
		return
	}
	s.autoImportSession(id, "brain connected")
}

// autoImportSession runs one auto-import for the staged session id: import the
// rig, broadcast the aum-rig notification, push the control-surface manifest.
// Failures are logged and broadcast (an agent watching the Activity feed sees
// the rig did NOT change), never fatal.
func (s *Server) autoImportSession(id, trigger string) {
	path, err := resolveAUMSessionPath("", id)
	if err != nil {
		log.Printf("aum auto-import %q (%s): %v", id, trigger, err)
		return
	}
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
// staged session id the iPad last downloaded (or the last import created a rig
// for), so a brain connect after a daemon restart still re-imports it.
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
