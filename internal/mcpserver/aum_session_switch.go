package mcpserver

// The session-switch registry: the daemon-owned mapping from Program Change
// programs (on the reserved session-switch channel, device.SessionSwitchChannel)
// to staged .aumproj sessions. AUM's "Session Load" is a GLOBAL action — never
// stored in the .aumproj — so the daemon cannot author it; instead the user
// hand-maps each "Session Load <session>" action in AUM's MIDI Control once
// (via Learn, the cheat-sheet from list_aum_session_switches makes it
// mechanical), and from then on a single PC switches the whole session.
//
// Programs are NEVER renumbered: the user's hand-wired AUM global mappings
// depend on them. Registering assigns the next free program (or an explicit
// one); removing leaves a hole.
//
// The switch path keeps the daemon in sync from both directions:
//   - switch_aum_session (MCP tool): send the PC through the brain hub, then
//     set the current session, re-import its rig and re-push the manifest.
//   - OnBrainSessionSwitch (upstream sessionSwitch frame): the brain-side
//     switcher row was tapped (it emitted the PC locally) — resolve the
//     program, set the current session, re-import and re-push.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teemow/mcp-midi-controller/internal/aum"
	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/mcp-midi-controller/internal/device"
	"github.com/teemow/mcp-midi-controller/internal/lanhttp"
	"github.com/teemow/mcp-midi-controller/internal/midicontrol"
)

// sessionSwitchEntry pins one staged session to one PC program. Name is the
// session's human title at registration time (falls back to the id), shown on
// the brain's switcher button and in the AUM setup cheat-sheet.
type sessionSwitchEntry struct {
	Program int    `json:"program"`
	ID      string `json:"id"` // staged session id (staging-relative, no .aumproj)
	Name    string `json:"name"`
}

// sessionSwitchRegistry is the persisted registry file shape
// (config.AUMSessionSwitchPath()). Entries are kept sorted by program.
type sessionSwitchRegistry struct {
	Entries []sessionSwitchEntry `json:"entries"`
}

// byID returns the entry for a staged session id (nil when unregistered).
func (r *sessionSwitchRegistry) byID(id string) *sessionSwitchEntry {
	for i := range r.Entries {
		if r.Entries[i].ID == id {
			return &r.Entries[i]
		}
	}
	return nil
}

// byProgram returns the entry pinned to a PC program (nil for a free program).
func (r *sessionSwitchRegistry) byProgram(program int) *sessionSwitchEntry {
	for i := range r.Entries {
		if r.Entries[i].Program == program {
			return &r.Entries[i]
		}
	}
	return nil
}

// nextFreeProgram returns the lowest unpinned program, or -1 when all 128 are
// taken. Holes left by removals are reused — they are free by definition (the
// user unmapped or never mapped them).
func (r *sessionSwitchRegistry) nextFreeProgram() int {
	for p := 0; p <= 127; p++ {
		if r.byProgram(p) == nil {
			return p
		}
	}
	return -1
}

// sort orders the entries by program so listings and the manifest are
// deterministic.
func (r *sessionSwitchRegistry) sort() {
	sort.Slice(r.Entries, func(i, j int) bool { return r.Entries[i].Program < r.Entries[j].Program })
}

// loadSessionSwitchRegistry reads the persisted registry; a missing file is an
// empty registry (nothing registered yet).
func loadSessionSwitchRegistry() (sessionSwitchRegistry, error) {
	var reg sessionSwitchRegistry
	data, err := os.ReadFile(config.AUMSessionSwitchPath())
	if err != nil {
		if os.IsNotExist(err) {
			return reg, nil
		}
		return reg, err
	}
	if err := json.Unmarshal(data, &reg); err != nil {
		return sessionSwitchRegistry{}, fmt.Errorf("parse %s: %w", config.AUMSessionSwitchPath(), err)
	}
	reg.sort()
	return reg, nil
}

// saveSessionSwitchRegistry persists the registry atomically (the pinned
// programs are what the user's hand-wired AUM mappings depend on — a torn
// write must never lose them).
func saveSessionSwitchRegistry(reg sessionSwitchRegistry) error {
	reg.sort()
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(config.StateDir(), 0o755); err != nil {
		return err
	}
	return lanhttp.WriteFileAtomic(config.AUMSessionSwitchPath(), data, 0o644)
}

// surfaceSessions reduces the registry to the manifest's sessions section,
// marking the daemon's current session. Empty when nothing is registered (the
// brain then renders no switcher row).
func (s *Server) surfaceSessions() []midicontrol.SurfaceSession {
	reg, err := loadSessionSwitchRegistry()
	if err != nil {
		log.Printf("session-switch registry: %v", err)
		return nil
	}
	current := s.currentAUMSessionID()
	out := make([]midicontrol.SurfaceSession, 0, len(reg.Entries))
	for _, e := range reg.Entries {
		out = append(out, midicontrol.SurfaceSession{
			Name:    e.Name,
			Program: e.Program,
			Channel: device.SessionSwitchChannel,
			Current: e.ID == current,
		})
	}
	return out
}

// repushControlSurface re-imports the current session's rig and pushes the
// fresh manifest (which carries the sessions section), so a registry change
// reaches the connected brain immediately. A no-op when no session was ever
// downloaded/imported — the next import/connect push carries the registry.
func (s *Server) repushControlSurface(trigger string) {
	if id := s.currentAUMSessionID(); id != "" {
		s.autoImportSession(id, trigger)
	}
}

// aumSetupHint is the one-time AUM wiring instruction for one registry entry,
// shared by the register/list tools.
func aumSetupHint(e sessionSwitchEntry) string {
	return fmt.Sprintf("AUM > MIDI Control > add \"Session Load %s\", arm Learn, then fire PC %d ch%d (switch_aum_session)",
		e.Name, e.Program, device.SessionSwitchChannel)
}

// --- MCP tool handlers ------------------------------------------------------

// sessionSwitchRow is the machine-readable per-entry row for the
// register/list session-switch tools.
type sessionSwitchRow struct {
	Program int    `json:"program"`
	Session string `json:"session"`
	Name    string `json:"name"`
	Channel int    `json:"channel"`
	Current bool   `json:"current,omitempty"`
	Setup   string `json:"setup"`
}

func sessionSwitchRowFor(e sessionSwitchEntry, current string) sessionSwitchRow {
	return sessionSwitchRow{
		Program: e.Program,
		Session: e.ID,
		Name:    e.Name,
		Channel: device.SessionSwitchChannel,
		Current: e.ID == current,
		Setup:   aumSetupHint(e),
	}
}

func (s *Server) handleRegisterAUMSessionSwitch(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Session string `json:"session"`
		Program *int   `json:"program"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	if strings.TrimSpace(args.Session) == "" {
		return textResult("/session: provide a staged session id (see list_aum_sessions)", true), nil
	}

	// The session must be staged: resolve and open it (its title names the
	// switcher button).
	path, err := resolveAUMSessionPath("", args.Session)
	if err != nil {
		return textResult("/session: "+err.Error(), true), nil
	}
	sess, err := aum.OpenFile(path)
	if err != nil {
		return textResult(fmt.Sprintf("/session: open staged session %q: %v", args.Session, err), true), nil
	}
	id := stagedRelID(path)
	name := sess.Title()
	if name == "" {
		name = id
	}

	// The whole load-mutate-save cycle runs under sessionSwitchMu: a
	// concurrent register/remove must never drop an entry or double-assign a
	// program (atomic writes only protect against torn files, not lost
	// updates).
	s.sessionSwitchMu.Lock()
	defer s.sessionSwitchMu.Unlock()

	reg, err := loadSessionSwitchRegistry()
	if err != nil {
		return textResult("load session-switch registry: "+err.Error(), true), nil
	}

	if existing := reg.byID(id); existing != nil {
		// Re-registering is idempotent — but never silently move a pinned
		// program (the user's hand-wired AUM mapping depends on it).
		if args.Program != nil && *args.Program != existing.Program {
			return textResult(fmt.Sprintf("session %q is already pinned to program %d; programs are never renumbered (remove_aum_session_switch first if you really want to re-pin)", id, existing.Program), true), nil
		}
		renamed := existing.Name != name
		existing.Name = name // refresh the title
		if err := saveSessionSwitchRegistry(reg); err != nil {
			return textResult("save session-switch registry: "+err.Error(), true), nil
		}
		if renamed {
			// The switcher button label changed — the brain must see it now,
			// not on the next unrelated push.
			s.repushControlSurface("session switch renamed")
		}
		row := sessionSwitchRowFor(*existing, s.currentAUMSessionID())
		return structResult(fmt.Sprintf("session %q already registered on program %d (name refreshed)\nsetup: %s", id, existing.Program, row.Setup), row), nil
	}

	var program int
	if args.Program != nil {
		program = *args.Program
		if program < 0 || program > 127 {
			return textResult("/program: must be 0..127", true), nil
		}
		if taken := reg.byProgram(program); taken != nil {
			return textResult(fmt.Sprintf("/program: %d is already pinned to session %q", program, taken.ID), true), nil
		}
	} else {
		program = reg.nextFreeProgram()
		if program < 0 {
			return textResult("all 128 programs are pinned — remove an entry first", true), nil
		}
	}

	entry := sessionSwitchEntry{Program: program, ID: id, Name: name}
	reg.Entries = append(reg.Entries, entry)
	if err := saveSessionSwitchRegistry(reg); err != nil {
		return textResult("save session-switch registry: "+err.Error(), true), nil
	}

	// The connected brain's switcher row updates with the next manifest push.
	s.repushControlSurface("session switch registered")

	row := sessionSwitchRowFor(entry, s.currentAUMSessionID())
	return structResult(fmt.Sprintf("registered session %q on program %d (channel %d)\none-time AUM wiring: %s",
		id, program, device.SessionSwitchChannel, row.Setup), row), nil
}

func (s *Server) handleListAUMSessionSwitches(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	reg, err := loadSessionSwitchRegistry()
	if err != nil {
		return textResult("load session-switch registry: "+err.Error(), true), nil
	}
	if len(reg.Entries) == 0 {
		return structResult("no session switches registered — pin a staged session with register_aum_session_switch",
			map[string]any{"switches": []sessionSwitchRow{}}), nil
	}
	current := s.currentAUMSessionID()
	rows := make([]sessionSwitchRow, 0, len(reg.Entries))
	var b strings.Builder
	fmt.Fprintf(&b, "%d session switch(es) on channel %d:\n", len(reg.Entries), device.SessionSwitchChannel)
	for _, e := range reg.Entries {
		row := sessionSwitchRowFor(e, current)
		rows = append(rows, row)
		marker := " "
		if row.Current {
			marker = "*"
		}
		fmt.Fprintf(&b, "%s PC %3d  %-30s %s\n", marker, e.Program, e.ID, row.Setup)
	}
	b.WriteString("(* = current session; setup is the one-time AUM Learn wiring per entry)")
	return structResult(b.String(), map[string]any{"switches": rows}), nil
}

func (s *Server) handleRemoveAUMSessionSwitch(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Session string `json:"session"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	if strings.TrimSpace(args.Session) == "" {
		return textResult("/session: provide the registered session id (see list_aum_session_switches)", true), nil
	}

	// Same load-mutate-save cycle as register: guarded so concurrent
	// mutations never lose a pin.
	s.sessionSwitchMu.Lock()
	defer s.sessionSwitchMu.Unlock()

	reg, err := loadSessionSwitchRegistry()
	if err != nil {
		return textResult("load session-switch registry: "+err.Error(), true), nil
	}
	entry := reg.byID(args.Session)
	if entry == nil {
		return textResult(fmt.Sprintf("session %q is not registered", args.Session), true), nil
	}
	removed := *entry
	kept := reg.Entries[:0]
	for _, e := range reg.Entries {
		if e.ID != args.Session {
			kept = append(kept, e)
		}
	}
	reg.Entries = kept
	if err := saveSessionSwitchRegistry(reg); err != nil {
		return textResult("save session-switch registry: "+err.Error(), true), nil
	}
	s.repushControlSurface("session switch removed")
	return textResult(fmt.Sprintf("removed session %q from program %d (the program stays a hole — others are never renumbered; also delete the \"Session Load %s\" action in AUM's MIDI Control)",
		removed.ID, removed.Program, removed.Name), false), nil
}

func (s *Server) handleSwitchAUMSession(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Session string `json:"session"`
		Program *int   `json:"program"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	reg, err := loadSessionSwitchRegistry()
	if err != nil {
		return textResult("load session-switch registry: "+err.Error(), true), nil
	}
	var entry *sessionSwitchEntry
	switch {
	case strings.TrimSpace(args.Session) != "":
		if entry = reg.byID(args.Session); entry == nil {
			return textResult(fmt.Sprintf("/session: %q is not registered (see list_aum_session_switches)", args.Session), true), nil
		}
	case args.Program != nil:
		if entry = reg.byProgram(*args.Program); entry == nil {
			return textResult(fmt.Sprintf("/program: %d is not pinned to a session (see list_aum_session_switches)", *args.Program), true), nil
		}
	default:
		return textResult("provide /session or /program", true), nil
	}

	if s.midi == nil {
		return textResult("no brain channel configured — the session-switch PC has no path into AUM", true), nil
	}
	cmd := midicontrol.Command{Type: "pc", Channel: device.SessionSwitchChannel, Program: entry.Program}
	sendCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.midi.Send(sendCtx, cmd); err != nil {
		return textResult(fmt.Sprintf("send session-switch PC %d ch%d: %v", entry.Program, device.SessionSwitchChannel, err), true), nil
	}

	// The PC is out: AUM fires the hand-mapped global Session Load. Track the
	// target as the current session and re-derive its rig so the control_*
	// tools, web UI and (re-pushed) brain surface all speak the NEW session.
	s.setCurrentAUMSession(entry.ID)
	s.autoImportSession(entry.ID, "session switch")
	s.broadcast("aum-session-switch", map[string]any{
		"session": entry.ID,
		"name":    entry.Name,
		"program": entry.Program,
		"channel": device.SessionSwitchChannel,
		"hint":    "AUM is loading the session; the rig was re-imported and the control surface re-pushed",
	})

	return structResult(fmt.Sprintf("sent PC %d on channel %d — AUM loads %q (if the Session Load action is wired); current session set to %q, rig re-imported, surface re-pushed",
		entry.Program, device.SessionSwitchChannel, entry.Name, entry.ID),
		sessionSwitchRowFor(*entry, entry.ID)), nil
}

// --- upstream sessionSwitch frames -------------------------------------------

// OnBrainSessionSwitch is the midicontrol receiver's sessionSwitch-frame
// callback: the brain's switcher row was tapped, the brain emitted the PC into
// AUM locally, and AUM is loading the target session. Resolve the program via
// the registry and bring the daemon along — set the current session, re-import
// its rig and re-push the manifest — so a brain-side switch keeps everything
// in lockstep. The receiver requires its callbacks not to block, so the work
// runs in a goroutine.
func (s *Server) OnBrainSessionSwitch(program int) {
	go func() {
		reg, err := loadSessionSwitchRegistry()
		if err != nil {
			log.Printf("brain session switch (pc %d): %v", program, err)
			return
		}
		entry := reg.byProgram(program)
		if entry == nil {
			log.Printf("brain session switch: pc %d is not pinned to a session", program)
			return
		}
		log.Printf("brain session switch: pc %d -> session %q", program, entry.ID)
		s.setCurrentAUMSession(entry.ID)
		s.autoImportSession(entry.ID, "brain session switch")
		s.broadcast("aum-session-switch", map[string]any{
			"session": entry.ID,
			"name":    entry.Name,
			"program": entry.Program,
			"channel": device.SessionSwitchChannel,
			"trigger": "brain",
			"hint":    "the brain switched sessions; the rig was re-imported and the control surface re-pushed",
		})
	}()
}
