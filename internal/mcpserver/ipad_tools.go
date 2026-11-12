package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teemow/mcp-midi-controller/internal/ipad"
)

// WithIPad attaches the WebDriverAgent client behind the ipad_* and AUM UI
// tools: the USB channel into the iPad's user interface. Without it the tools
// are not registered (config ipad_wda_url "").
func WithIPad(c *ipad.Client) Option {
	return func(s *Server) { s.ipad = c }
}

// Defaults and caps for the iPad tools.
const (
	ipadTreeLimit       = 400
	ipadWaitDefault     = 5 * time.Second
	ipadWaitMax         = 60 * time.Second
	ipadHoldDefault     = 1200 * time.Millisecond
	ipadSwipeDefault    = 300 * time.Millisecond
	ipadTapLabelTimeout = 2 * time.Second
)

const ipadAppProp = `"app": {"type": "string", "description": "Which app the action targets: aum (default), probe (AUv3 Probe, resolved on the device), or a bundle ID. The app is brought to the front (launched when not running) and stays there. Only AUM keeps the iPad awake: after work in another app bring AUM back (ipad_launch without app), or the iPad locks itself and needs its passcode."}`

// registerIPadTools wires the iPad UI tools. They drive the iPad's screen over
// USB through WebDriverAgent — a channel independent of MIDI — so an agent can
// both operate AUM by touch and verify that MIDI it sent through the brain or
// a control_* tool really changed AUM.
func (s *Server) registerIPadTools() {
	if s.ipad == nil {
		return
	}
	s.mcp.AddTool(&mcp.Tool{
		Name: "ipad_status",
		Description: "Check the USB control channel into the iPad's UI: whether WebDriverAgent is reachable and ready, the device's iPadOS version, the screen size in points and its pixels-per-point scale, the app in the foreground, and whether the iPad is locked (it then has to be unlocked on the device: the daemon never unlocks it). " +
			"When WebDriverAgent is not reachable it checks the layers below it (go-ios tunnel, developer disk image) and names the command that brings the missing one up. Read-only; opens no WebDriverAgent session.",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}, s.handleIPadStatus)

	s.mcp.AddTool(&mcp.Tool{
		Name: "ipad_screenshot",
		Description: "Take a screenshot of the iPad and return it as an image. By default one image pixel is one point, so a pixel position in the image is the point to pass to ipad_tap / ipad_hold / ipad_swipe (add region.x/region.y for a crop). " +
			"Pass region (in points) to crop to one area, e.g. a mixer strip, and pixels_per_point 2 for full Retina detail on a small crop. " +
			"Use it for what AUM's accessibility tree does not carry: mute/solo/record-arm state (lit buttons), meters, plugin windows. Screenshots show private rig data (session, plugin and device names); they are returned to you only and never stored.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"region": {"type": "object", "description": "Crop rectangle in points.", "properties": {
					"x": {"type": "number"}, "y": {"type": "number"},
					"width": {"type": "number", "exclusiveMinimum": 0}, "height": {"type": "number", "exclusiveMinimum": 0}
				}, "required": ["x", "y", "width", "height"]},
				"pixels_per_point": {"type": "number", "exclusiveMinimum": 0, "maximum": 3, "description": "Output resolution (default 1; capped at the device scale, 2 on Retina iPads)."},
				"format": {"type": "string", "enum": ["jpeg", "png"], "description": "Image format (default jpeg)."}
			}
		}`),
	}, s.handleIPadScreenshot)

	s.mcp.AddTool(&mcp.Tool{
		Name: "ipad_ui_tree",
		Description: "Read an app's accessibility tree: one line per element with its type, label, value and frame in points, e.g. `Button \"MIDI 1\" (79,764 156x50)`. Tap a listed label with ipad_tap {label}, or its frame's centre with ipad_tap {x,y}. " +
			"By default only labelled elements are listed; all=true adds the unlabelled ones (icon-only buttons, containers) indented by depth. contains filters by label/value text. " +
			"For AUM's parsed state (tempo, session, fader levels) use get_aum_ui instead.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				` + ipadAppProp + `,
				"all": {"type": "boolean", "description": "Also list unlabelled elements, indented by depth."},
				"contains": {"type": "string", "description": "Only elements whose label or value contains this text (case-insensitive)."},
				"limit": {"type": "integer", "minimum": 1, "maximum": 2000, "description": "Maximum elements to list (default 400)."}
			}
		}`),
	}, s.handleIPadUITree)

	targets := make([]string, len(ipad.AUMTargets))
	var targetDocs []string
	for i, t := range ipad.AUMTargets {
		targets[i] = t.Name
		targetDocs = append(targetDocs, t.Name+" = "+t.Description)
	}
	targetEnum, _ := json.Marshal(targets)
	s.mcp.AddTool(&mcp.Tool{
		Name: "ipad_tap",
		Description: "Tap the iPad's screen in an app, one of three ways: label (the element with that accessibility label, case-insensitive, buttons first — list labels with ipad_ui_tree), x+y (a point, in points, in the app's current orientation), or target (a named AUM control: " + strings.Join(targetDocs, "; ") + "). " +
			"AUM's mixer titles, dB labels, MUTE/SOLO/REC and list rows tap by label; its icon-only buttons need x+y or a target. A label that sits on a caption under an icon (AUM's main menu: FILES, SAVE, …) taps the caption and does nothing: tap the icon by position. " +
			"gesture double double-taps (AUM: reset a fader, edit the tempo value, add a slot below the last insert); gesture twice taps twice 0.5 s apart in one session, which AUM's two-step buttons need (CLEAR → Sure?, OVERWRITE?, REMOVE CHANNEL → TAP TO CONFIRM take the second tap only for about a second). " +
			"Taps can change or lose state (CLEAR, a session row in FILES loads it, SAVE SESSION, REMOVE CHANNEL, eject, record): take an ipad_screenshot first near such controls, and iterate on the ' (mcp test)' session copies, never the original sessions. The aum_* tools do the common AUM operations by name and verify them.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				` + ipadAppProp + `,
				"label": {"type": "string", "description": "Accessibility label to tap."},
				"x": {"type": "number", "description": "Point to tap (with y)."},
				"y": {"type": "number", "description": "Point to tap (with x)."},
				"target": {"type": "string", "enum": ` + string(targetEnum) + `, "description": "A named AUM control (app must be aum)."},
				"gesture": {"type": "string", "enum": ["tap", "double", "twice"], "description": "tap (default), double (a double-tap), or twice (two taps 0.5 s apart: confirms a two-step button)."}
			}
		}`),
	}, s.handleIPadTap)

	s.mcp.AddTool(&mcp.Tool{
		Name:        "ipad_hold",
		Description: "Long-press a point (in points) in an app, e.g. an AUM node or slot for its SELECT/REPLACE/REMOVE SLOT menu, or AUM's record button (un-arms all). Default 1.2 s.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				` + ipadAppProp + `,
				"x": {"type": "number"}, "y": {"type": "number"},
				"duration_ms": {"type": "integer", "minimum": 100, "maximum": 10000, "description": "Press duration (default 1200)."}
			},
			"required": ["x", "y"]
		}`),
	}, s.handleIPadHold)

	s.mcp.AddTool(&mcp.Tool{
		Name:        "ipad_swipe",
		Description: "Drag from one point to another (in points) in an app, as a touch: down, move, up. Use it to scroll AUM's mixer sideways to reach strips get_aum_ui reports as off screen (e.g. from (1100,560) to (300,560)), or to swipe a node left (bypass + eject). A drag that starts on an AUM node turns into a long-press.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				` + ipadAppProp + `,
				"x1": {"type": "number"}, "y1": {"type": "number"},
				"x2": {"type": "number"}, "y2": {"type": "number"},
				"duration_ms": {"type": "integer", "minimum": 50, "maximum": 5000, "description": "Move duration (default 300)."}
			},
			"required": ["x1", "y1", "x2", "y2"]
		}`),
	}, s.handleIPadSwipe)

	s.mcp.AddTool(&mcp.Tool{
		Name: "ipad_launch",
		Description: "Bring an app to the front on the iPad, launching it when it is not running (a running app keeps its state; AUM keeps its loaded session). Reports the window size and orientation. " +
			"Without app it brings AUM back, which is how work in another app ends: AUM keeps the iPad awake, the AUv3 Probe app does not, and an iPad left with it in front locks itself.",
		InputSchema: json.RawMessage(`{"type": "object", "properties": {` + ipadAppProp + `}}`),
	}, s.handleIPadLaunch)

	s.mcp.AddTool(&mcp.Tool{
		Name: "get_aum_ui",
		Description: "Read AUM's state from its own user interface over USB — independent of the MIDI path, so it shows whether MIDI the daemon sent really landed: the tempo, the transport position, the session name (only while AUM's main menu is open — aum_panel open menu), the DSP load, the main meter's source, whether the start screen, tempo bar or a plugin window is showing, " +
			"every open popover, side panel or alert (title, rows of text → value, buttons), and every mixer strip with its number, title (custom titles too), fader level in dB, mute / solo / record-arm state, its nodes top to bottom (source, insert slots, output; empty or bypassed) and whether it is on screen. " +
			"Not in AUM's tree: plugin parameters (use the audio tap) and MIDI routing connections (aum_midi_routing read). Brings AUM to the front. Emits the state as structuredContent.",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}, s.handleGetAUMUI)

	s.mcp.AddTool(&mcp.Tool{
		Name: "wait_for_aum_ui",
		Description: "Verify a MIDI action against AUM's own UI: re-read AUM's state (one read takes 0.5–2 s) until every expectation holds or timeout_ms passes, then report matched or what still differs. " +
			"Typical loop: get_aum_ui (baseline) -> send the MIDI (control_*, send_midi, set_transport, switch_aum_session, recall_scene) -> wait_for_aum_ui. " +
			"Expect exact values (tempo_bpm, a channel's level_db, muted, soloed, armed, a node's bypassed, session, strip_count, an open overlay or plugin_window) or a change away from the baseline (not_tempo_bpm, a channel's not_level_db) when the CC-to-value curve is unknown; transport playing/stopped compares the position across reads. " +
			"Channels are selected by number (\"2\" = the strip titled \"2: …\"), \"MIDI 1\", or part of the title. A level of -100 dB or below stands for -∞. Not matching is a normal result (isError false, matched false).",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"tempo_bpm": {"type": "number", "description": "Expected tempo."},
				"not_tempo_bpm": {"type": "number", "description": "Expect the tempo to differ from this (the baseline)."},
				"tempo_tolerance": {"type": "number", "minimum": 0, "description": "Tempo tolerance in BPM (default 0.05)."},
				"session": {"type": "string", "description": "Expected session name as AUM's main menu shows it (case-insensitive; .aumproj optional). Open the menu first (ipad_tap target menu): AUM shows the name nowhere else."},
				"transport": {"type": "string", "enum": ["playing", "stopped"]},
				"channels": {"type": "array", "items": {"type": "object", "properties": {
					"channel": {"type": "string", "description": "Strip selector: number, \"MIDI n\", \"#position\", or part of the title."},
					"level_db": {"type": "number", "description": "Expected fader level in dB."},
					"not_level_db": {"type": "number", "description": "Expect the level to differ from this (the baseline)."},
					"tolerance_db": {"type": "number", "minimum": 0, "description": "Level tolerance in dB (default 0.5)."},
					"muted": {"type": "boolean"},
					"soloed": {"type": "boolean"},
					"armed": {"type": "boolean", "description": "Record-arm state."},
					"nodes": {"type": "array", "items": {"type": "object", "properties": {
						"node": {"type": "string", "description": "source, output, an insert slot number, or a node label."},
						"bypassed": {"type": "boolean"}
					}, "required": ["node", "bypassed"]}}
				}, "required": ["channel"]}},
				"strip_count": {"type": "integer", "minimum": 0, "description": "Expected number of mixer strips."},
				"overlay": {"type": "string", "description": "Expect an open popover, side panel or alert whose title contains this."},
				"no_overlay": {"type": "boolean", "description": "Expect nothing open over the mixer."},
				"plugin_window": {"type": "string", "description": "Expect a plugin window whose title contains this (e.g. after a Show & Front Plugin mapping)."},
				"timeout_ms": {"type": "integer", "minimum": 0, "maximum": 60000, "description": "How long to keep re-reading (default 5000)."}
			}
		}`),
	}, s.handleWaitForAUMUI)
}

// ipadError turns a WebDriverAgent failure into a tool error. When WDA is not
// reachable it diagnoses the stack below it and names the fix.
func (s *Server) ipadError(ctx context.Context, err error) *mcp.CallToolResult {
	if ipad.Unreachable(err) {
		return textResult(err.Error()+"\n"+s.ipadStackHint(ctx), true)
	}
	if ipad.SessionLost(err) {
		return textResult(s.withFrontNote(err.Error()+"\nAnother WebDriverAgent client (e.g. scripts/ipad) opened a session meanwhile; WDA serves one at a time, so this call's session was ended. Retry once that client is idle."), true)
	}
	// A locked iPad fails every call, each in its own words.
	if s.ipadLocked(ctx) {
		return textResult(fmt.Sprintf("%s: %s. WebDriverAgent said: %v", ipadLockedText, ipadUnlock, err), true)
	}
	// So does an app that went away: it left the Home Screen in front.
	if ipad.AppWentAway(err) {
		return textResult(appGone(err), true)
	}
	return textResult(s.withFrontNote(err.Error()), true)
}

// ipadStackHint names the missing layer of the device stack. The commands are
// those of auv3-probe's scripts/ipad, run on the host the iPad is plugged into.
func (s *Server) ipadStackHint(ctx context.Context) string {
	url := s.ipad.BaseURL()
	if !s.ipad.HasGoIOS() {
		return fmt.Sprintf("Bring up the device stack on the host the iPad is plugged into (auv3-probe: scripts/ipad tunnel, scripts/ipad mount-ddi after an iPad restart, scripts/ipad wda), or point ipad_wda_url (now %s) at its WebDriverAgent. See docs/ipad-control.md.", url)
	}
	l := s.ipad.Diagnose(ctx)
	switch {
	case !l.Tunnel:
		return "No go-ios tunnel to the iPad: connect it over USB, then run `scripts/ipad tunnel` (auv3-probe) and keep it running; then `scripts/ipad wda`."
	case !l.DiskImage:
		return "The go-ios tunnel is up but no developer disk image is mounted (it is lost on every iPad restart): run `scripts/ipad mount-ddi`, then `scripts/ipad wda`."
	default:
		return fmt.Sprintf("The tunnel and disk image are up but WebDriverAgent is not answering on %s: run `scripts/ipad wda` (Settings > Developer > Enable UI Automation on, iPad unlocked) and keep it running.", url)
	}
}

// ipadStatus is ipad_status's structured result.
type ipadStatus struct {
	WDAURL    string        `json:"wda_url"`
	Reachable bool          `json:"reachable"`
	Status    *ipad.Status  `json:"status,omitempty"`
	Screen    *ipad.Screen  `json:"screen,omitempty"`
	ActiveApp *ipad.AppInfo `json:"active_app,omitempty"`
	// Locked is whether the iPad is on its lock screen (absent when
	// WebDriverAgent does not say).
	Locked *bool        `json:"locked,omitempty"`
	Layers *ipad.Layers `json:"layers,omitempty"`
	Hint   string       `json:"hint,omitempty"`
	// Note says that another app than AUM is in front.
	Note string `json:"note,omitempty"`
}

func (s *Server) handleIPadStatus(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	out := ipadStatus{WDAURL: s.ipad.BaseURL()}
	st, err := s.ipad.Status(ctx)
	if err != nil {
		if s.ipad.HasGoIOS() {
			l := s.ipad.Diagnose(ctx)
			out.Layers = &l
		}
		out.Hint = s.ipadStackHint(ctx)
		return structResult(fmt.Sprintf("iPad UI control unavailable: %v\n%s", err, out.Hint), out), nil
	}
	out.Reachable = true
	out.Status = &st
	var b strings.Builder
	state := "ready"
	if !st.Ready {
		state = "NOT ready"
	}
	fmt.Fprintf(&b, "WebDriverAgent %s on %s: %s %s (%s)", state, out.WDAURL, st.OS, st.OSVersion, st.Device)
	if scr, err := s.ipad.ScreenInfo(ctx); err == nil {
		out.Screen = &scr
		fmt.Fprintf(&b, "\nscreen: %gx%g points, %g pixels per point (%s)", scr.Width, scr.Height, scr.Scale, ipad.Size{Width: scr.Width, Height: scr.Height}.Orientation())
	}
	if app, err := s.ipad.ActiveApp(ctx); err == nil {
		out.ActiveApp = &app
		fmt.Fprintf(&b, "\nforeground app: %s (pid %d)", app.BundleID, app.PID)
	}
	if locked, err := s.ipad.Locked(ctx); err == nil {
		out.Locked = &locked
	}
	switch {
	case out.Locked != nil && *out.Locked:
		out.Hint = ipadUnlock
		fmt.Fprintf(&b, "\nthe iPad is LOCKED: %s. Until then no app can be brought to the front, and the iPad tools and load_song fail.", ipadUnlock)
	default:
		// What the daemon brought forward, else what WebDriverAgent names
		// when that is an app (in windowed multitasking it names the Home
		// Screen whatever is shown).
		out.Note = s.frontNote()
		if out.Note == "" && out.ActiveApp != nil && out.ActiveApp.BundleID != springBoard {
			out.Note = frontNoteFor(out.ActiveApp.BundleID, time.Time{})
		}
		if out.Note != "" {
			b.WriteString("\n" + out.Note)
		}
	}
	return structResult(b.String(), out), nil
}

func (s *Server) handleIPadScreenshot(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Region         *ipad.Rect `json:"region"`
		PixelsPerPoint float64    `json:"pixels_per_point"`
		Format         string     `json:"format"`
	}
	if res := decodeArgs(req, &args); res != nil {
		return res, nil
	}
	scr, err := s.ipad.ScreenInfo(ctx)
	if err != nil {
		return s.ipadError(ctx, err), nil
	}
	png, err := s.ipad.Screenshot(ctx)
	if err != nil {
		return s.ipadError(ctx, err), nil
	}
	shot, err := ipad.RenderShot(png, scr.Scale, ipad.ShotOptions{Region: args.Region, PixelsPerPoint: args.PixelsPerPoint, Format: args.Format})
	if err != nil {
		return textResult(err.Error(), true), nil
	}
	r := shot.Region
	text := fmt.Sprintf("iPad screenshot %dx%d px (%s, %d KB) of the region (%g,%g %gx%g) in points at %g px/point; point = (%g + px/%g, %g + py/%g).",
		shot.Width, shot.Height, shot.MIMEType, len(shot.Data)/1024, r.X, r.Y, r.Width, r.Height, shot.PPP, r.X, shot.PPP, r.Y, shot.PPP)
	return &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.ImageContent{Data: shot.Data, MIMEType: shot.MIMEType},
		&mcp.TextContent{Text: s.withFrontNote(text)},
	}}, nil
}

// ipadTree is ipad_ui_tree's structured result.
type ipadTree struct {
	App       string      `json:"app"`
	Window    ipad.Size   `json:"window"`
	Nodes     []ipad.Node `json:"nodes"`
	Truncated bool        `json:"truncated,omitempty"`
	// Note says that the call left another app than AUM in front.
	Note string `json:"note,omitempty"`
}

func (s *Server) handleIPadUITree(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		App      string `json:"app"`
		All      bool   `json:"all"`
		Contains string `json:"contains"`
		Limit    int    `json:"limit"`
	}
	if res := decodeArgs(req, &args); res != nil {
		return res, nil
	}
	if args.Limit <= 0 {
		args.Limit = ipadTreeLimit
	}
	app, err := s.ipadApp(ctx, args.App)
	if err != nil {
		return textResult(err.Error(), true), nil
	}
	out := ipadTree{App: app}
	err = s.ipad.WithApp(ctx, app, func(sess *ipad.Session) error {
		var err error
		if out.Window, err = sess.WindowSize(ctx); err != nil {
			return err
		}
		root, err := sess.Source(ctx)
		if err != nil {
			return err
		}
		out.Nodes, out.Truncated = root.Flatten(ipad.FlattenOptions{All: args.All, Contains: args.Contains, Limit: args.Limit})
		return nil
	})
	if err != nil {
		return s.ipadError(ctx, err), nil
	}
	if out.Nodes == nil {
		out.Nodes = []ipad.Node{}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d elements, window %gx%g points (%s)\n", app, len(out.Nodes), out.Window.Width, out.Window.Height, out.Window.Orientation())
	b.WriteString(ipad.FormatNodes(out.Nodes, args.All))
	if out.Truncated {
		fmt.Fprintf(&b, "… cut at %d elements; narrow with contains or raise limit\n", args.Limit)
	}
	if out.Note = s.frontNote(); out.Note != "" {
		b.WriteString(out.Note + "\n")
	}
	return structResultNoEmbed(b.String(), out), nil
}

func (s *Server) handleIPadTap(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		App     string   `json:"app"`
		Label   string   `json:"label"`
		X       *float64 `json:"x"`
		Y       *float64 `json:"y"`
		Target  string   `json:"target"`
		Gesture string   `json:"gesture"`
	}
	if res := decodeArgs(req, &args); res != nil {
		return res, nil
	}
	ways := 0
	for _, set := range []bool{args.Label != "", args.X != nil || args.Y != nil, args.Target != ""} {
		if set {
			ways++
		}
	}
	if ways != 1 {
		return textResult("pass exactly one of label, x+y, or target", true), nil
	}
	if (args.X == nil) != (args.Y == nil) {
		return textResult("x and y go together", true), nil
	}
	switch args.Gesture {
	case "", "tap", "double", "twice":
	default:
		return textResult("gesture must be tap, double or twice", true), nil
	}
	app, err := s.ipadApp(ctx, args.App)
	if err != nil {
		return textResult(err.Error(), true), nil
	}
	if args.Target != "" && app != ipad.AUMBundleID {
		return textResult("target names AUM controls; use app aum (the default)", true), nil
	}
	verb := map[string]string{"": "tapped", "tap": "tapped", "double": "double-tapped", "twice": "tapped twice"}[args.Gesture]
	var msg string
	err = s.ipad.WithApp(ctx, app, func(sess *ipad.Session) error {
		gesture := func(x, y float64) error {
			switch args.Gesture {
			case "double":
				return sess.DoubleTap(ctx, x, y)
			case "twice":
				return sess.TapTwice(ctx, x, y, 0)
			}
			return sess.TapAt(ctx, x, y)
		}
		switch {
		case args.Label != "" && (args.Gesture == "" || args.Gesture == "tap"):
			n, err := sess.TapLabel(ctx, args.Label, ipadTapLabelTimeout)
			if err != nil {
				return err
			}
			msg = fmt.Sprintf("tapped %q in %s", args.Label, app)
			if n > 1 {
				msg += fmt.Sprintf(" (%d matches, tapped the first)", n)
			}
		case args.Label != "":
			r, n, err := sess.LabelRect(ctx, args.Label, ipadTapLabelTimeout)
			if err != nil {
				return err
			}
			x, y := r.Center()
			if err := gesture(x, y); err != nil {
				return err
			}
			msg = fmt.Sprintf("%s %q at (%g,%g) in %s", verb, args.Label, x, y, app)
			if n > 1 {
				msg += fmt.Sprintf(" (%d matches, used the first)", n)
			}
		case args.Target != "":
			win, err := sess.WindowSize(ctx)
			if err != nil {
				return err
			}
			x, y, err := ipad.AUMTarget(args.Target, win)
			if err != nil {
				return err
			}
			if err := gesture(x, y); err != nil {
				return err
			}
			msg = fmt.Sprintf("%s AUM %s at (%g,%g)", verb, args.Target, x, y)
		default:
			if err := gesture(*args.X, *args.Y); err != nil {
				return err
			}
			msg = fmt.Sprintf("%s (%g,%g) in %s", verb, *args.X, *args.Y, app)
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ipad.ErrNoSuchLabel) {
			return textResult(s.withFrontNote(err.Error()+" in "+app+"; list the labels with ipad_ui_tree, or tap by x+y"), true), nil
		}
		return s.ipadError(ctx, err), nil
	}
	return textResult(s.withFrontNote(msg), false), nil
}

func (s *Server) handleIPadHold(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		App        string   `json:"app"`
		X          *float64 `json:"x"`
		Y          *float64 `json:"y"`
		DurationMS int      `json:"duration_ms"`
	}
	if res := decodeArgs(req, &args); res != nil {
		return res, nil
	}
	if args.X == nil || args.Y == nil {
		return textResult("x and y are required", true), nil
	}
	d := ipadHoldDefault
	if args.DurationMS > 0 {
		d = time.Duration(args.DurationMS) * time.Millisecond
	}
	app, err := s.ipadApp(ctx, args.App)
	if err != nil {
		return textResult(err.Error(), true), nil
	}
	err = s.ipad.WithApp(ctx, app, func(sess *ipad.Session) error { return sess.Hold(ctx, *args.X, *args.Y, d) })
	if err != nil {
		return s.ipadError(ctx, err), nil
	}
	return textResult(s.withFrontNote(fmt.Sprintf("held (%g,%g) for %v in %s", *args.X, *args.Y, d, app)), false), nil
}

func (s *Server) handleIPadSwipe(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		App        string   `json:"app"`
		X1         *float64 `json:"x1"`
		Y1         *float64 `json:"y1"`
		X2         *float64 `json:"x2"`
		Y2         *float64 `json:"y2"`
		DurationMS int      `json:"duration_ms"`
	}
	if res := decodeArgs(req, &args); res != nil {
		return res, nil
	}
	if args.X1 == nil || args.Y1 == nil || args.X2 == nil || args.Y2 == nil {
		return textResult("x1, y1, x2 and y2 are required", true), nil
	}
	d := ipadSwipeDefault
	if args.DurationMS > 0 {
		d = time.Duration(args.DurationMS) * time.Millisecond
	}
	app, err := s.ipadApp(ctx, args.App)
	if err != nil {
		return textResult(err.Error(), true), nil
	}
	err = s.ipad.WithApp(ctx, app, func(sess *ipad.Session) error {
		return sess.Swipe(ctx, *args.X1, *args.Y1, *args.X2, *args.Y2, d)
	})
	if err != nil {
		return s.ipadError(ctx, err), nil
	}
	return textResult(s.withFrontNote(fmt.Sprintf("swiped (%g,%g) -> (%g,%g) over %v in %s", *args.X1, *args.Y1, *args.X2, *args.Y2, d, app)), false), nil
}

func (s *Server) handleIPadLaunch(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		App string `json:"app"`
	}
	if res := decodeArgs(req, &args); res != nil {
		return res, nil
	}
	app, err := s.ipadApp(ctx, args.App)
	if err != nil {
		return textResult(err.Error(), true), nil
	}
	var win ipad.Size
	err = s.ipad.WithApp(ctx, app, func(sess *ipad.Session) error {
		var err error
		win, err = sess.WindowSize(ctx)
		return err
	})
	if err != nil {
		return s.ipadError(ctx, err), nil
	}
	if ipad.IsApp("probe", app) {
		// A host without go-ios cannot look this id up; the alias (app: probe)
		// and load_song need it to bring the probe app forward.
		rememberIPadApp("probe", app)
	}
	out := map[string]any{"app": app, "window": win, "orientation": win.Orientation()}
	text := fmt.Sprintf("%s is in front, window %gx%g points (%s)", app, win.Width, win.Height, win.Orientation())
	if note := s.frontNote(); note != "" {
		out["note"] = note
		text += "\n" + note
	}
	return structResult(text, out), nil
}

func (s *Server) handleGetAUMUI(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	st, err := s.ipad.ReadAUM(ctx)
	if err != nil {
		return s.ipadError(ctx, err), nil
	}
	return structResult(describeAUMState(st), st), nil
}

func (s *Server) handleWaitForAUMUI(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		ipad.Expectation
		TimeoutMS *int `json:"timeout_ms"`
	}
	if res := decodeArgs(req, &args); res != nil {
		return res, nil
	}
	if err := args.Validate(); err != nil {
		return textResult(err.Error(), true), nil
	}
	timeout := ipadWaitDefault
	if args.TimeoutMS != nil {
		timeout = min(time.Duration(*args.TimeoutMS)*time.Millisecond, ipadWaitMax)
	}
	res, err := s.ipad.WaitAUM(ctx, args.Expectation, timeout, 0)
	if err != nil {
		return s.ipadError(ctx, err), nil
	}
	var b strings.Builder
	if res.Matched {
		fmt.Fprintf(&b, "matched after %d ms (%d reads)", res.ElapsedMS, res.Reads)
	} else {
		fmt.Fprintf(&b, "NOT matched after %d ms (%d reads):\n  - %s", res.ElapsedMS, res.Reads, strings.Join(res.Mismatches, "\n  - "))
	}
	b.WriteString("\n\n" + describeAUMState(res.State))
	return structResult(b.String(), res), nil
}

// describeAUMState renders AUMState for text-only clients.
func describeAUMState(st ipad.AUMState) string {
	var b strings.Builder
	fmt.Fprintf(&b, "AUM, %s %gx%g points", st.Orientation, st.Window.Width, st.Window.Height)
	switch {
	case st.StartScreen:
		b.WriteString(", start screen (no session loaded)")
	case st.MenuOpen:
		b.WriteString(", main menu open")
	}
	if st.TempoBar {
		b.WriteString(", tempo bar open")
	}
	if st.Keyboard {
		b.WriteString(", keyboard open")
	}
	b.WriteByte('\n')
	if st.Session != "" {
		fmt.Fprintf(&b, "session: %q\n", st.Session)
	} else if !st.StartScreen {
		b.WriteString("session: not on screen (AUM shows its name only in the main menu: aum_panel open menu)\n")
	}
	if st.TempoBPM != nil {
		fmt.Fprintf(&b, "tempo: %g BPM\n", *st.TempoBPM)
	}
	if st.Position != "" {
		fmt.Fprintf(&b, "position: %s\n", st.Position)
	}
	if st.DSP != "" {
		fmt.Fprintf(&b, "DSP: %s\n", st.DSP)
	}
	if st.MeterSource != "" {
		fmt.Fprintf(&b, "main meter: %s\n", st.MeterSource)
	}
	for _, o := range st.Overlays {
		fmt.Fprintf(&b, "open %s %q", o.Kind, o.Title)
		if len(o.Buttons) > 0 {
			b.WriteString(" buttons: " + strings.Join(o.Buttons, ", "))
		}
		b.WriteByte('\n')
	}
	for _, w := range st.PluginWindows {
		fmt.Fprintf(&b, "plugin window %q\n", w.Title)
	}
	if len(st.Channels) == 0 {
		b.WriteString("channels: none visible\n")
	} else {
		b.WriteString("channels:\n")
		for _, ch := range st.Channels {
			fmt.Fprintf(&b, "  strip %d %q", ch.Strip, ch.Title)
			if ch.LevelLabel != "" {
				b.WriteString(" " + ch.LevelLabel)
			}
			for _, f := range []struct {
				name string
				on   *bool
			}{{"MUTED", ch.Muted}, {"SOLO", ch.Soloed}, {"ARMED", ch.Armed}} {
				if f.on != nil && *f.on {
					b.WriteString(" " + f.name)
				}
			}
			if !ch.OnScreen {
				b.WriteString(" (off screen)")
			}
			if nodes := nodeSummary(ch); nodes != "" {
				b.WriteString(" nodes: " + nodes)
			}
			b.WriteByte('\n')
		}
	}
	b.WriteString("not in AUM's tree: plugin parameters (get_audio_tap), MIDI routing connections (aum_midi_routing read)")
	return b.String()
}

// nodeSummary lists a strip's nodes compactly: "source PAR, 1 LP (bypassed), 2 +, output SPKR".
func nodeSummary(ch ipad.AUMChannel) string {
	var parts []string
	for _, n := range ch.Nodes {
		p := n.Role
		if n.Role == "insert" {
			p = fmt.Sprint(n.Slot)
		}
		switch {
		case n.Empty:
			p += " +"
		case n.Label != "":
			p += " " + n.Label
		}
		if n.Bypassed {
			p += " (bypassed)"
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, ", ")
}

// decodeArgs unmarshals a tool call's arguments, returning an error result on
// bad input (nil on success or when there are no arguments).
func decodeArgs(req *mcp.CallToolRequest, v any) *mcp.CallToolResult {
	if len(req.Params.Arguments) == 0 {
		return nil
	}
	if err := json.Unmarshal(req.Params.Arguments, v); err != nil {
		return textResult("invalid arguments: "+err.Error(), true)
	}
	return nil
}
