package ipadtest

import (
	"encoding/json"
	"fmt"
	"math"
)

// Strip is one mixer strip of a synthetic AUM tree.
type Strip struct {
	// Title is the title button's label ("2: Synth", "MIDI 1", "CHAN 3",
	// or a custom name).
	Title string
	// Level is the dB label above the fader ("-1.0 dB", "-∞ dB"); empty for
	// MIDI strips.
	Level string
	// Muted, Soloed and Armed light M, S and R (the Selected trait). A
	// muted strip's MUTE button widens over SOLO, which leaves the tree.
	Muted, Soloed, Armed bool
	// Source is the source node's label; by default it repeats the title,
	// as AUM's square node button does. EmptySource leaves the slot empty.
	Source      string
	EmptySource bool
	// Inserts are the insert slots top to bottom.
	Inserts []Node
	// AddSlot shows the orange "+1" below the inserts.
	AddSlot bool
	// PlayEnable adds a File Player's PLAY ENABLE toggle (on when true).
	PlayEnable *bool
}

// Node is an insert slot.
type Node struct {
	// Label is the node's short name ("LP"); "" with Empty for a "+" slot.
	Label    string
	Empty    bool
	Bypassed bool
}

// Overlay is a popover, side panel or alert over the mixer.
type Overlay struct {
	// Kind is "popover", "panel" or "alert".
	Kind  string
	Title string
	// Rows are text → value pairs (value "" = a text alone).
	Rows [][2]string
	// Buttons are labelled buttons stacked below the rows.
	Buttons []string
	// Fields are text fields (their values).
	Fields []string
	// Cells are list cells (FILES): a name and the detail line below it.
	Cells [][2]string
	// NavBar titles a side panel with a navigation bar (the Navigator's:
	// an eye and a bookmark filter, no close button) instead of a 60-point
	// header text.
	NavBar bool
	// Steppers puts unlabelled ‹ and › buttons beside each row value, as
	// Clock Options has them.
	Steppers bool
	// Frame overrides the default frame (x, y, w, h).
	Frame *[4]float64
}

// AUM describes a synthetic AUM screen. The shapes follow WDA's JSON source
// of AUM 1.4.8 (frames, types, traits, the spelled-out MUTE texts, the
// square node buttons with their inner button, the add tile and its
// AUDIO/MIDI/IMPORT menu, the start screen, popovers and side panels); the
// names are made up.
type AUM struct {
	Tempo    string // "120" -> "♩120"
	Position string // "001:1  00:00.0"
	// MeterLabel is the main meter's stale "SESSION: …" accessibility label
	// (AUM does not refresh it; parsers must ignore it).
	MeterLabel string
	// Session set: the main menu is open and shows it.
	Session string
	// StartScreen is the screen CLEAR leaves: no strip (leave Strips empty),
	// the add tile in the middle and LAST STATE and DEFAULT below it.
	StartScreen bool
	TempoBar    bool
	// AddMenu opens the add tile's AUDIO / MIDI / IMPORT menu.
	AddMenu bool
	Strips  []Strip
	// Scroll is how far the mixer is scrolled sideways, in points: the
	// strips and the add tile sit that much further left, and the mixer's
	// horizontal scroll bar reads it in percent of its range.
	Scroll float64
	// OnScreenOnly keeps in the tree only the strips, and the add tile's
	// column, that reach into the window, as AUM does: of a mixer wider than
	// the window the others are not in it. Without it every strip is.
	OnScreenOnly bool
	// Overlays are drawn over the mixer, front-most last.
	Overlays []Overlay
	// PluginWindow opens a plugin window with this title ("Synth @A1").
	PluginWindow string
	// Routing opens the MIDI routing matrix with these endpoints (name and
	// kind pairs).
	Routing *Routing
	// MeterSource is printed above the main meter (default "SPKR OUT").
	MeterSource string
	// Keyboard opens the built-in keyboard with its HIDE↓ toolbar button.
	Keyboard bool
	// Notices are texts shown over the mixer that belong to no panel: the
	// "LOADING STATE" overlay of a session being restored, a line naming a
	// plugin that failed.
	Notices []string
	// Save opens the SAVE side panel.
	Save *SavePanel
	// Files opens the FILES side panel as AUM draws it. (An Overlay of kind
	// "panel" with Cells is a simpler list for tests that only read one.)
	Files *FilesPanel
	// Share opens iOS's share sheet over everything else, and Rename the
	// RENAME dialog its Rename action opens.
	Share  *ShareSheet
	Rename *RenameDialog
	// Sorry is the text of AUM's alert "Sorry", which stays until its OK:
	// "" draws none.
	Sorry string
}

// ShareSheet is iOS's share sheet as a long-press on a session's row of FILES
// opens it on an iPad whose language is German, read off AUM 1.4.8: a popover
// that another process draws. So its elements report their frames from the
// popover's corner, not from the screen's, and a tap aimed at a frame as the
// tree prints it lands beside the sheet and closes it.
//
// It is captioned with the file's name, offers apps to send the file to, and
// has a row of actions that ends in an entry which unfolds the rest of them
// as a list below, AUM's Rename first.
type ShareSheet struct {
	// Name captions the sheet: the session it is for. NoCaption leaves the
	// caption out.
	Name      string
	NoCaption bool
	// More says the row of actions was unfolded.
	More bool
}

// The entry of a ShareSheet's row of actions that unfolds the list below it,
// and the one that folds it again.
const (
	ShareMore = "Mehr anzeigen"
	ShareLess = "Weniger anzeigen"
)

type shareCell struct {
	name, id string
	r        [4]float64 // from the popover's corner
}

// Frame is the sheet's popover on screen: unfolded it grows upwards.
func (s ShareSheet) Frame() [4]float64 {
	if s.More {
		return [4]float64{816, 30, 375, 657}
	}
	return [4]float64{816, 282, 375, 405}
}

func (s ShareSheet) cells() []shareCell {
	fold := ShareMore
	if s.More {
		fold = ShareLess
	}
	cells := []shareCell{
		{"AirDrop", "shareCell", [4]float64{18, 106, 82, 118}},
		{"Drive", "shareCell", [4]float64{103, 106, 83, 118}},
		{"Messages", "shareCell", [4]float64{189, 106, 82, 118}},
		{"Notizen", "shareCell", [4]float64{274, 106, 83, 118}},
		{"Copy", "actionGroupCell", [4]float64{18, 257, 82, 134}},
		{"Copy URL", "actionGroupCell", [4]float64{103, 257, 83, 134}},
		{"Import Channels", "actionGroupCell", [4]float64{189, 257, 82, 134}},
		{fold, "actionGroupCell", [4]float64{274, 257, 83, 134}},
	}
	if s.More {
		cells = append(cells,
			shareCell{"Rename", "actionGroupCell", [4]float64{16, 401, 343, 51}},
			shareCell{"In Dateien sichern", "actionGroupCell", [4]float64{16, 471, 343, 51}},
			shareCell{"In PDF Expert speichern", "actionGroupCell", [4]float64{16, 522, 343, 51}})
	}
	return cells
}

// Hit is what the screen's point (x, y) meets: the name of the sheet's cell
// there ("" for a spot of the sheet that holds none), and whether the point
// lies in the sheet at all. A touch beside the sheet closes it.
func (s ShareSheet) Hit(x, y float64) (cell string, inside bool) {
	f := s.Frame()
	if x < f[0] || x >= f[0]+f[2] || y < f[1] || y >= f[1]+f[3] {
		return "", false
	}
	for _, c := range s.cells() {
		if x >= f[0]+c.r[0] && x < f[0]+c.r[0]+c.r[2] && y >= f[1]+c.r[1] && y < f[1]+c.r[1]+c.r[3] {
			return c.name, true
		}
	}
	return "", true
}

func shareSheet(s ShareSheet) []node {
	f := s.Frame()
	var cells []node
	for _, c := range s.cells() {
		// A cell is named by its own label; the text in it is hidden in the
		// list and shows in the rows above it.
		text := el("StaticText", c.name, c.r[0], c.r[1]+c.r[3]-47, c.r[2], 15).with("rawIdentifier", "cellTitleLabel")
		if c.r[2] > 300 {
			text = el("StaticText", c.name, c.r[0]+60, c.r[1]+15, 263, 21).with("rawIdentifier", "titleLabel").with("isVisible", "0")
		}
		cells = append(cells, el("Cell", c.name, c.r[0], c.r[1], c.r[2], c.r[3], text).with("rawIdentifier", c.id).with("traits", "Button"))
	}
	bar := el("NavigationBar", "", 0, 10, f[2], 97).with("rawIdentifier", "UIActivityContentView")
	if !s.NoCaption {
		bar = el("NavigationBar", "", 0, 10, f[2], 97,
			el("Other", s.Name, 86, 39, 263, 19).with("rawIdentifier", "LP.CaptionBar.TopCaption"),
			el("Other", "AUM Project · 13 KB", 86, 57, 263, 21).with("rawIdentifier", "LP.CaptionBar.BottomCaption")).with("rawIdentifier", "UIActivityContentView")
	}
	return []node{
		el("Other", "dismiss popup", 0, 0, 1210, 834).with("rawIdentifier", "PopoverDismissRegion"),
		el("Popover", "", f[0], f[1], f[2], f[3],
			bar,
			el("CollectionView", "", 0, 0, f[2], f[3], cells...).with("rawIdentifier", "activityCollectionView").with("isVisible", "0"),
			el("Toolbar", "Symbolleiste", 0, 0, f[2], f[3]).with("rawIdentifier", "Toolbar")),
	}
}

// RenameDialog is AUM's RENAME dialog, an alert over the mixer: a field that
// holds the session's name when it opens, Cancel and OK side by side.
type RenameDialog struct {
	// Name is the field's value.
	Name string
}

// Where RenameDialog draws its buttons, and the alert "Sorry" its OK.
var (
	RenameCancel = [4]float64{470, 233, 135, 44}
	RenameOK     = [4]float64{605, 233, 135, 44}
	SorryOK      = [4]float64{470, 423, 270, 44}
)

// NameExists is what AUM's alert "Sorry" says to a rename onto the name of
// another file of the folder.
const NameExists = "A file with that name already exists!"

func renameDialog(r RenameDialog) node {
	return el("Alert", "RENAME", 470, 129, 270, 148,
		el("StaticText", "RENAME", 486, 149, 238, 21),
		el("CollectionView", "", 470, 189, 270, 44,
			el("Cell", "", 485, 189, 240, 32,
				el("TextField", "", 492, 196, 226, 18,
					el("Button", "Clear text", 697, 197, 16, 16)).with("value", r.Name))),
		el("Button", "Cancel", RenameCancel[0], RenameCancel[1], RenameCancel[2], RenameCancel[3]),
		el("Button", "OK", RenameOK[0], RenameOK[1], RenameOK[2], RenameOK[3]))
}

// FilesPanel is AUM's FILES side panel: its root (title "FILES": Sessions,
// Recordings, Restore Previous State) or a folder of Sessions or Recordings.
// AUM titles every folder of a view with the view's name ("SESSIONS",
// "RECORDINGS") and names the folder in a header row above the list, which
// scrolls away with the list's first rows.
type FilesPanel struct {
	// Title is "FILES", "SESSIONS" or "RECORDINGS".
	Title string
	// Folder is the header row: "/" for the root of a view, a folder's name
	// inside it; "" draws none (the FILES root has none).
	Folder string
	// Cells are the list's rows from the first one on screen.
	Cells []FileCell
	// Scrolled says the list is scrolled down: the sort bar and the header
	// row are out of sight and the rows begin at the list's top.
	Scrolled bool
	// Cut says the scrolled list rests between two rows, as AUM's does after
	// a drag: Cells[0] reaches above the list's top edge, which hides its
	// name and leaves its second line, FilesRowsCut rows are whole below it,
	// and the next one reaches below the list's bottom edge.
	Cut bool
	// Sort is the value of the sort control above the list, the order the
	// list is in ("" draws SortLastOpened, as the iPad showed it).
	Sort string
}

// SortLastOpened is the value the sort control above a FILES list read on the
// iPad: the list in the order its entries were last opened, the newest first.
const SortLastOpened = "LAST OPENED"

// FileCell is one row of a FILES list.
type FileCell struct {
	// Name and Detail are the row's two lines (a session's detail is the
	// time it was saved).
	Name, Detail string
	// Folder draws the chevron of a row that opens a folder or a view.
	Folder bool
}

// FilesRows is how many rows a FILES list shows whole: below its sort bar
// and header row, once those are scrolled away, and when the scrolled list
// rests between two rows. A table holds a few more below its edge, in the
// tree and not visible.
const (
	FilesRowsTop      = 12
	FilesRowsScrolled = 14
	FilesRowsCut      = 13
)

// FilesCutY is the top of the row a list's top edge cuts (FilesPanel.Cut):
// 30 of its 51 points are above the edge, and the rows below follow it.
const FilesCutY = 30.0

// FilesRowY is the top of row i (from the first on screen) of a FILES list.
func FilesRowY(i int, scrolled bool) float64 {
	if scrolled {
		return 60 + float64(i)*50
	}
	return 175 + float64(i)*50
}

// SavePanel is AUM's SAVE side panel: the session's name in a text field,
// the folder it is saved into and the SAVE SESSION button.
type SavePanel struct {
	// Name is the name field's value.
	Name string
	// Folder is the folder as SAVE prints it ("/Songs", "/" for the root).
	Folder string
	// Button is the button's label: "SAVE SESSION", or "OVERWRITE?" for the
	// second after it was pressed with the name of a session that exists.
	Button string
}

// SaveButton is the SAVE panel's button, as x, y, w, h in points.
var SaveButton = [4]float64{966, 302, 228, 37}

// Routing is the MIDI routing matrix of a synthetic AUM screen.
type Routing struct {
	Sources      [][2]string
	Destinations [][2]string
}

// Cell is the matrix cell of source i and destination j, as x, y, w, h in
// points (light it with WDA.Bright to mark a connection).
func (r Routing) Cell(i, j int) [4]float64 {
	return [4]float64{430 + float64(i)*37 - 2, 434 + float64(j)*37 - 2, 36, 36}
}

type node map[string]any

// hide marks n and everything in it invisible (WDA isVisible "0").
func hide(n node) {
	n["isVisible"] = "0"
	if kids, ok := n["children"].([]node); ok {
		for _, k := range kids {
			hide(k)
		}
	}
}

func el(typ, label string, x, y, w, h float64, children ...node) node {
	n := node{
		"type": typ, "rect": node{"x": x, "y": y, "width": w, "height": h},
		"isEnabled": "1", "isVisible": "1", "traits": "", "value": nil, "rawIdentifier": nil,
	}
	if label != "" {
		n["label"] = label
		n["name"] = label
		if typ == "StaticText" {
			n["value"] = label
		}
	} else {
		n["label"] = nil
	}
	if len(children) > 0 {
		n["children"] = children
	}
	return n
}

func (n node) with(k string, v any) node { n[k] = v; return n }

// Source renders a as WDA's JSON source value.
func (a AUM) Source() json.RawMessage {
	const W, H = 1210.0, 834.0
	top := []node{}
	if a.Position != "" {
		x := 89.0
		if a.TempoBar {
			x = 340
		}
		top = append(top, el("StaticText", a.Position, x, 1, 135, 16))
	}
	if a.Tempo != "" {
		if a.TempoBar {
			// ✕, TAP, ‹ ♩ ›, metronome, Clock Options •••; the tempo is a
			// button here.
			top = append(top,
				el("Button", "", 0, 0, 60, 60),
				el("Button", "TAP", 60, 8, 50, 50),
				el("Button", "", 110, 8, 30, 50),
				el("Button", "♩"+a.Tempo, 138, 8, 60, 50).with("traits", "StaticText, Button"),
				el("Button", "", 200, 8, 30, 50),
				el("Button", "", 230, 8, 50, 50).with("traits", "Button"),
				el("Button", "", 280, 8, 50, 50))
		} else {
			top = append(top, el("StaticText", "♩"+a.Tempo, 0, 22, 60, 22))
		}
	}
	meter := a.MeterSource
	if meter == "" {
		meter = "SPKR OUT"
	}
	if a.MeterLabel != "" {
		top = append(top, el("Image", a.MeterLabel, 436, 0, 295, 60, el("StaticText", meter, 436, 4, 295, 12)))
	} else {
		top = append(top, el("StaticText", meter, 436, 4, 295, 12))
	}
	top = append(top, el("Button", "", 1000, 0, 60, 60, el("StaticText", "DSP:  3 %", 1000, 15, 60, 15)))

	// AUM centres the mixer while its strips and the add tile's column are
	// narrower than the window: the column is at 484 with no strip, the strip
	// at 363 and the column at 605 with one. A wider mixer starts 7 points in
	// and scrolls.
	var mixer []node
	n := float64(len(a.Strips))
	left := 7.0
	if (n+1)*242 < W {
		left = (W - (n+1)*242) / 2
	}
	inView := func(x, w float64) bool { return !a.OnScreenOnly || (x < W && x+w > 0) }
	for i, s := range a.Strips {
		if x := left + float64(i)*242 - a.Scroll; inView(x, 228) {
			mixer = append(mixer, strip(x, s))
		}
	}
	// The add tile is an unlabelled button in a column of its own. While its
	// menu is open it is a container of the menu's three buttons instead.
	tile := left + n*242 - a.Scroll
	col := []node{el("Button", "", tile+46, 372, 150, 150)}
	if a.AddMenu {
		col = []node{el("Other", "", tile+46, 372, 150, 150,
			el("Button", "AUDIO", tile+46, 372, 150, 45, el("StaticText", "AUDIO", tile+95, 383, 52, 23)),
			el("Button", "MIDI", tile+46, 425, 150, 45, el("StaticText", "MIDI", tile+103, 436, 36, 23)),
			el("Button", "IMPORT", tile+46, 477, 150, 45, el("StaticText", "IMPORT", tile+91, 488, 60, 23)))}
	}
	// The start screen's two buttons are in the tile's column, below the tile.
	if a.StartScreen {
		col = append(col,
			el("Button", "LAST STATE", tile+51, 611, 140, 36, el("StaticText", "LAST STATE", tile+100, 621, 62, 16)),
			el("Button", "DEFAULT", tile+51, 687, 140, 36, el("StaticText", "DEFAULT", tile+107, 697, 48, 16)))
	}
	if inView(tile, 242) {
		mixer = append(mixer, el("Other", "", tile, 60, 242, 774, col...))
	}
	mixer = append(mixer, scrollBar(W, MixerWidth(len(a.Strips)), a.Scroll))
	mixerView := el("ScrollView", "", 0, 60, W, 774, mixer...)
	// Under a side panel or the routing matrix AUM marks the mixer invisible.
	covered := a.Session != "" || a.Routing != nil || a.Save != nil || a.Files != nil
	for _, o := range a.Overlays {
		covered = covered || o.Kind == "panel"
	}
	if covered {
		hide(mixerView)
	}
	main := []node{el("Other", "", 0, 0, W, 60, top...), mixerView}
	if a.Session != "" {
		main = append(main, menuPanel(a.Session))
	}
	if a.PluginWindow != "" {
		main = append(main, el("Other", "", 93, 60, 1024, 383,
			el("Button", "-∞", 161, 60, 36, 32), el("Button", "", 197, 60, 32, 32),
			el("Button", a.PluginWindow, 539, 64, 100, 24), el("Button", "", 640, 60, 28, 32),
			el("Button", "", 1049, 60, 32, 32), el("Button", "", 1081, 60, 32, 32)))
	}
	if a.Routing != nil {
		main = append(main, routing(*a.Routing))
	}
	if a.Save != nil {
		main = append(main, savePanel(*a.Save))
	}
	if a.Files != nil {
		main = append(main, filesPanel(*a.Files))
	}
	if a.Keyboard {
		main = append(main, el("Other", "", 0, 666, W, 168,
			el("Button", "HIDE↓", 10, 666, 48, 24, el("StaticText", "HIDE↓", 13, 669, 42, 18)),
			el("Button", "DSP:  3 %", 1152, 666, 48, 24)))
	}
	for _, o := range a.Overlays {
		main = append(main, overlay(o)...)
	}
	if a.Share != nil {
		main = append(main, shareSheet(*a.Share)...)
	}
	if a.Rename != nil {
		main = append(main, renameDialog(*a.Rename))
	}
	if a.Sorry != "" {
		main = append(main, el("Alert", "Sorry", 470, 342, 270, 125,
			el("StaticText", "Sorry", 486, 362, 238, 21),
			el("StaticText", a.Sorry, 486, 386, 238, 16),
			el("Button", "OK", SorryOK[0], SorryOK[1], SorryOK[2], SorryOK[3])))
	}
	for i, n := range a.Notices {
		main = append(main, el("StaticText", n, 405, 380+float64(i)*30, 400, 24))
	}
	root := el("Application", "AUM", 0, 0, W, H,
		el("Window", "", 0, 0, W, H, el("Other", "", 0, 0, W, H, main...)))
	b, err := json.Marshal(root)
	if err != nil {
		panic(fmt.Sprintf("ipadtest: marshal AUM source: %v", err))
	}
	return b
}

// ScreenWidth is the width of the window the synthetic screens are drawn
// for (landscape).
const ScreenWidth = 1210.0

// StripX is the x of the i-th strip (from 0) in the mixer's content; the add
// tile's column is a strip's width further right of the last.
func StripX(i int) float64 { return 7 + float64(i)*242 }

// MixerWidth is the width of the content of a mixer of n strips: the strips
// and the add tile's column.
func MixerWidth(n int) float64 { return StripX(n) + 242 }

// MaxScroll is how far a mixer of n strips scrolls sideways.
func MaxScroll(n int) float64 { return math.Max(0, MixerWidth(n)-ScreenWidth) }

// scrollBar is the mixer's horizontal scroll bar as iOS lists it: its label
// counts the pages, its value is how far the mixer is scrolled in percent,
// with a no-break space before the sign, its thumb the part in view.
func scrollBar(w, content, scroll float64) node {
	pages := int(math.Ceil(content / w))
	label := fmt.Sprintf("Horizontal scroll bar, %d pages", pages)
	if pages <= 1 {
		label = "Horizontal scroll bar, 1 page"
	}
	pct, thumb := 0.0, w-56
	if rng := content - w; rng > 0 {
		pct = math.Round(100 * math.Max(0, math.Min(1, scroll/rng)))
		thumb = (w - 56) * w / content
	}
	return el("Other", label, 25, 801, w-50, 30,
		el("Other", "", 28+(w-56-thumb)*pct/100, 828, thumb, 3)).with("value", fmt.Sprintf("%.0f %%", pct))
}

// nodeButton is a square node button and, when loaded, the inner button
// over it (value "1", Selected).
func nodeButton(label string, x, y, size float64, empty bool) []node {
	out := []node{el("Button", label, x, y, size, size, el("StaticText", label, x+15, y+size-28, size-30, 14))}
	if !empty {
		in := size - 30
		if size < 80 {
			in = size - 22
		}
		off := (size - in) / 2
		out = append(out, el("Button", "", x+off, y+off, in, in).with("value", "1").with("traits", "Selected, Button"))
	}
	return out
}

func strip(x float64, s Strip) node {
	midi := s.Level == ""
	var kids []node
	if midi {
		// AUM glues texts around a MIDI node into its label.
		glued := "-∞, DSP:  2 %, -∞, " + s.Title + ", " + s.Title
		kids = append(kids, el("Other", "", x, 127, 228, 88, nodeButton(glued, x+111, 127, 88, false)...))
	} else {
		src := s.Source
		if src == "" {
			src = s.Title
		}
		kids = append(kids, el("Other", "", x, 88, 199, 98, nodeButton(src, x+101, 88, 98, s.EmptySource)...))
		kids = append(kids, el("Button", "", x+193, 87, 41, 99))
		if s.PlayEnable != nil {
			// The toggle is a button at the strip's left edge, left of the
			// node, labelled with the meter's reading as iOS glues it; the
			// caption is its child and reaches above it.
			t := el("Button", "-∞", x, 100, 65, 74, el("StaticText", "PLAY ENABLE", x, 88, 65, 37))
			if *s.PlayEnable {
				t = t.with("traits", "Selected, Button")
			}
			kids = append(kids, el("Other", "", x, 88, 101, 98, t))
		}
		var ins []node
		for k, n := range s.Inserts {
			nx := x + 72
			if n.Bypassed {
				nx -= 55
			}
			y := 221 + float64(k)*84
			ins = append(ins, el("Other", "", x, y, 146, 74, nodeButton(n.Label, nx, y, 74, n.Empty)...))
		}
		if s.AddSlot {
			ins = append(ins, el("Button", "+1", x+57, 221+float64(len(s.Inserts))*84, 42, 42))
		} else {
			ins = append(ins, el("Button", "+1", x+57, 678, 42, 42).with("isVisible", "0"))
		}
		kids = append(kids, el("ScrollView", "", x, 216, 166, 420, ins...))
		kids = append(kids, el("Other", "%.1f", x+166, 228, 62, 397, el("Button", s.Level, x+166, 203, 62, 20).with("traits", "StaticText, Button")))
		if s.Muted {
			kids = append(kids, el("Other", "MUTE", x+166, 630, 62, 30,
				el("StaticText", "M", x+180, 630, 11, 30), el("StaticText", "U", x+191, 630, 9, 30),
				el("StaticText", "T", x+200, 630, 7, 30), el("StaticText", "E", x+207, 630, 8, 30)).with("traits", "Selected"))
		} else {
			kids = append(kids,
				el("Other", "MUTE", x+166, 630, 30, 30,
					el("StaticText", "M", x+176, 630, 11, 30), el("StaticText", "U", x+191, 645, 0, 0)),
				el("Other", "SOLO", x+198, 630, 30, 30, el("StaticText", "S", x+210, 630, 7, 30)).with("traits", sel(s.Soloed)))
		}
		rec := el("Other", "REC", x+61, 700, 30, 30, el("StaticText", "R", x+72, 700, 8, 30), el("StaticText", "E", x+83, 714, 2, 2))
		kids = append(kids, el("Other", "", x, 666, 228, 98, append([]node{rec.with("traits", sel(s.Armed))}, nodeButton("SPKR", x+101, 666, 98, false)...)...))
	}
	kids = append(kids, el("Button", s.Title, x+72, 764, 156, 50))
	return el("Other", "", x, 65, 228, 764, kids...)
}

func sel(on bool) string {
	if on {
		return "Selected"
	}
	return ""
}

func menuPanel(session string) node {
	kids := []node{el("StaticText", "SESSION: "+session, 965, 75, 230, 20)}
	rows := [][]string{{"FILES", "SAVE", "CLEAR"}, {"NOTES", "ABOUT", "HELP"}, {"SETTINGS", "MIDI ROUTE", "MIDI CTRL"}}
	for r, row := range rows {
		y := 105 + float64(r)*114
		for c, caption := range row {
			x := 965 + float64(c)*83
			kids = append(kids, el("Button", "", x, y, 64, 64), el("StaticText", caption, x, y+64, 64, 20))
		}
	}
	return el("Other", "", 950, 0, 260, 834,
		el("Other", "", 950, 0, 260, 60, el("StaticText", "MENU", 990, 0, 180, 60)),
		el("ScrollView", "", 950, 60, 260, 774, kids...))
}

// savePanel follows WDA's source of AUM 1.4.8's SAVE panel (testdata
// aum-plugin-window-save.json of internal/ipad): a table with the name field
// and the folder in a cell each under their headers, and the button, whose
// own text is not visible.
func savePanel(p SavePanel) node {
	b := SaveButton
	return el("Other", "", 950, 0, 260, 834,
		el("Other", "", 950, 0, 260, 60,
			el("Button", "MIDI CTRL, SETTINGS, ABOUT, CLEAR, FILES, SESSION: Old, DSP:  0 %, -∞", 950, 0, 60, 60),
			el("StaticText", "SAVE", 990, 0, 180, 60)),
		el("Table", "", 950, 60, 260, 774,
			el("Other", "", 950, 60, 260, 56, el("StaticText", "SESSION NAME", 950, 60, 260, 56).with("traits", "Header, StaticText")),
			el("Cell", "", 950, 115, 260, 51, el("TextField", "", 966, 115, 228, 45).with("value", p.Name)),
			el("Other", "", 950, 183, 260, 38, el("StaticText", "FOLDER", 950, 183, 260, 38).with("traits", "Header, StaticText")),
			el("Cell", "", 950, 221, 260, 50,
				el("StaticText", p.Folder, 966, 221, 210, 50),
				el("Button", "chevron", 1183, 239, 11, 14).with("traits", "NotEnabled, Button")).with("traits", "Button"),
			el("Button", p.Button, b[0], b[1], b[2], b[3], el("StaticText", p.Button, 1038, 312, 84, 17).with("isVisible", "0"))))
}

// filesPanel follows WDA's source of AUM 1.4.8's FILES panel: a table with
// the EDIT and home buttons, the sort control and, inside it, the header row
// naming the folder, then a cell per row with its two texts. The FILES root
// has three cells and none of the bar.
func filesPanel(p FilesPanel) node {
	const x, w = 810.0, 400.0
	var rows []node
	root := p.Title == "FILES"
	if !root && !p.Scrolled {
		rows = append(rows,
			el("Button", "EDIT", 1144, 76, 50, 28, el("StaticText", "EDIT", 1157, 83, 24, 14).with("isVisible", "0")),
			el("Button", "", 1096, 76, 32, 28))
		order := p.Sort
		if order == "" {
			order = SortLastOpened
		}
		sort := el("Button", "", 826, 60, 270, 60).with("value", order).with("traits", "Adjustable")
		if p.Folder != "" {
			sort["children"] = []node{el("StaticText", p.Folder, x, 120, w, 56).with("traits", "Header, StaticText")}
		}
		rows = append(rows, sort)
	}
	whole := FilesRowsScrolled
	if !p.Scrolled {
		whole = FilesRowsTop
	}
	cut := !root && p.Scrolled && p.Cut
	if cut {
		whole = FilesRowsCut + 2 // and the row each edge cuts
	}
	for i, c := range p.Cells {
		y := FilesRowY(i, p.Scrolled)
		if root {
			y = 95 + float64(i)*85
		}
		if cut {
			y = FilesCutY + float64(i)*50
		}
		kids := []node{el("StaticText", c.Name, x+65, y+7, 180, 20)}
		if cut && i == 0 {
			kids[0] = kids[0].with("isVisible", "0")
		}
		if c.Detail != "" {
			kids = append(kids, el("StaticText", c.Detail, x+65, y+29, 114, 14))
		}
		cell := el("Cell", "", x, y, w, 51, kids...)
		if c.Folder {
			cell["children"] = append(kids, el("Button", "chevron", 1183, y+18, 11, 15).with("traits", "NotEnabled, Button"))
			cell = cell.with("traits", "Button")
		}
		if !root && i >= whole {
			if i >= whole+2 {
				break
			}
			hide(cell)
		}
		rows = append(rows, cell)
	}
	return el("Other", "", x, 0, w, 834,
		el("Other", "", x, 0, w, 60,
			el("Button", "MIDI CTRL, SETTINGS, ABOUT, CLEAR, FILES, SESSION: Old, DSP:  0 %, -∞", x, 0, 60, 60),
			el("StaticText", p.Title, x+40, 0, 320, 60)),
		el("Table", "", x, 60, w, 714, rows...),
		el("Button", "Manage recordings in AudioShare.", x, 774, w, 60,
			el("StaticText", "Manage recordings in AudioShare.", 987, 787, 104, 34)))
}

// Rect is where the overlay is drawn (x, y, w, h): its Frame, else the
// default of its kind.
func (o Overlay) Rect() [4]float64 {
	switch {
	case o.Frame != nil:
		return *o.Frame
	case o.Kind == "panel":
		return [4]float64{950, 0, 260, 834}
	case o.Kind == "alert":
		return [4]float64{455, 300, 300, 200}
	}
	return [4]float64{633, 186, 500, 400}
}

// Button is where the overlay draws the button labelled label (x, y, w, h):
// below its rows and text fields, as body lays them out.
func (o Overlay) Button(label string) ([4]float64, bool) {
	f := o.Rect()
	y := f[1] + 63
	switch o.Kind {
	case "panel":
		y = 70
	case "alert":
		y = f[1] + 50
	}
	y += 50 * float64(len(o.Rows)+len(o.Fields))
	for _, b := range o.Buttons {
		if b == label {
			return [4]float64{f[0] + 16, y + 5, f[2] - 32, 40}, true
		}
		y += 50
	}
	return [4]float64{}, false
}

func overlay(o Overlay) []node {
	f := o.Rect()
	switch o.Kind {
	case "panel":
		// The back button's label is glued from texts around it, as AUM's is.
		kids := []node{el("Other", "", f[0], 0, f[2], 60,
			el("Button", "SESSION: Old, Copyright (c) Someone, DSP:  0 %, -∞", f[0], 0, 60, 60),
			el("StaticText", o.Title, f[0]+40, 0, f[2]-80, 60))}
		if o.NavBar {
			kids = []node{el("NavigationBar", "", f[0], 0, f[2], 50,
				el("StaticText", o.Title, f[0]+95, 17, 82, 20).with("traits", "Header"),
				el("Button", "", f[0]+16, 11, 26, 27).with("traits", "NotEnabled, Button"),
				el("Button", "", f[0]+f[2]-56, 5, 40, 40)).with("rawIdentifier", o.Title)}
		}
		kids = append(kids, body(o, f[0], 70, f[2])...)
		return []node{el("Other", "", f[0], f[1], f[2], f[3], kids...)}
	case "alert":
		kids := []node{el("StaticText", o.Title, f[0]+20, f[1]+15, f[2]-40, 22)}
		kids = append(kids, body(o, f[0], f[1]+50, f[2])...)
		return []node{el("Alert", o.Title, f[0], f[1], f[2], f[3], kids...)}
	}
	kids := []node{el("NavigationBar", "", f[0], f[1]+13, f[2], 50, el("StaticText", o.Title, f[0]+180, f[1]+30, 140, 20)).with("rawIdentifier", o.Title)}
	kids = append(kids, body(o, f[0], f[1]+63, f[2])...)
	return []node{
		el("Other", "dismiss popup", 0, 0, 1210, 834).with("rawIdentifier", "PopoverDismissRegion"),
		el("Popover", "", f[0], f[1], f[2], f[3], kids...),
	}
}

// body lays out rows (text left, value button right), then text fields,
// then buttons, 50 points apart.
func body(o Overlay, x, y, w float64) []node {
	var out []node
	for _, r := range o.Rows {
		out = append(out, el("StaticText", r[0], x+16, y+15, 120, 20))
		if r[1] != "" {
			out = append(out, el("Button", r[1], x+w-115, y+7, 100, 36, el("StaticText", r[1], x+w-90, y+16, 50, 17)))
			if o.Steppers {
				out = append(out, el("Button", "", x+w-145, y+7, 30, 36), el("Button", "", x+w-15, y+7, 30, 36))
			}
		}
		y += 50
	}
	for _, f := range o.Fields {
		// iOS lists the field's clear button as a button of the dialog.
		out = append(out, el("TextField", "", x+16, y+5, w-32, 36,
			el("Button", "Clear text", x+w-40, y+15, 16, 16)).with("value", f))
		y += 50
	}
	for _, b := range o.Buttons {
		out = append(out, el("Button", b, x+16, y+5, w-32, 40, el("StaticText", b, x+w/2-40, y+15, 80, 20)))
		y += 50
	}
	for _, c := range o.Cells {
		out = append(out, el("Cell", "", x, y, w, 51,
			el("StaticText", c[0], x+65, y+7, 120, 20),
			el("StaticText", c[1], x+65, y+29, 110, 14)))
		y += 50
	}
	return out
}

func routing(r Routing) node {
	kids := []node{el("NavigationBar", "", 0, 60, 1210, 50, el("StaticText", "MIDI ROUTING", 555, 77, 100, 20)).with("rawIdentifier", "MIDI ROUTING")}
	for i, s := range r.Sources {
		x := 430 + float64(i)*37
		kids = append(kids, el("StaticText", s[0], x, 255, 18, 132), el("StaticText", s[1], x+14, 255, 18, 132))
	}
	for j, d := range r.Destinations {
		y := 434 + float64(j)*37
		kids = append(kids, el("StaticText", d[0], 653, y, 132, 18), el("StaticText", d[1], 653, y+14, 132, 18))
	}
	return el("Other", "", 0, 60, 1210, 774, kids...)
}

// DefaultAUM is a small session: a MIDI strip and five audio strips (one at
// -∞, the last scrolled off screen), the main menu open, and a stale meter
// label naming another session.
func DefaultAUM() AUM {
	return AUM{
		Tempo: "120", Position: "001:1  00:00.0",
		MeterLabel: "SESSION: Old Session, MIDI 1, 0.0, 0, -24, -48, SPKR OUT, -∞",
		Session:    "test-session (mcp test)",
		Strips: []Strip{
			{Title: "MIDI 1"},
			{Title: "2: Synth One", Level: "-1.0 dB", Inserts: []Node{{Label: "LP"}, {Empty: true}}},
			{Title: "3: Drum Machine", Level: "-∞ dB", Muted: true},
			{Title: "4: Synth Two", Level: "+0.0 dB"},
			{Title: "5: Bass Synth", Level: "-7.3 dB"},
			{Title: "6: Pad Synth", Level: "-7.9 dB"},
		},
	}
}

// MixerAUM is DefaultAUM with the main menu closed.
func MixerAUM() AUM {
	a := DefaultAUM()
	a.Session = ""
	return a
}

// LoadingNotice is the text AUM shows over the mixer while it restores a
// session's plugins.
const LoadingNotice = "LOADING STATE"

// OpenSessionScreens is AUM while a session is opened from FILES → Sessions →
// folder, for WDA.SourceFunc: one screen per gesture. A plugin window is open
// at first; FILES opens on its root; the session's row loads strips, which
// show LoadingNotice for the first loadingReads reads once the menu is asked
// for the session's name, and notices once the menu is closed again. It
// answers the gestures of a load that finds every row on its first page; a
// list that is searched further (SessionsAUM) is dragged, which is a gesture.
func OpenSessionScreens(folder, name string, strips []Strip, loadingReads int, notices ...string) func(gestures, reads int) json.RawMessage {
	old := MixerAUM()
	window := old
	window.PluginWindow = "Synth @A1"
	menu := DefaultAUM()
	// FILES as AUM draws it: its root, the root of Sessions (titled SESSIONS,
	// "/" in its header row) and the folder (the same title, its name in the
	// header row).
	files := MixerAUM()
	files.Files = &FilesPanel{Title: "FILES", Cells: []FileCell{
		{Name: "Sessions", Folder: true}, {Name: "Recordings", Folder: true},
		{Name: "Restore Previous State", Detail: "12. Nov 2026 at 20:30:00"}}}
	sessions := MixerAUM()
	sessions.Files = &FilesPanel{Title: "SESSIONS", Folder: "/", Cells: []FileCell{
		{Name: folder, Detail: "12. Nov 2026 at 20:00:00", Folder: true}, {Name: "other", Detail: "11. Nov 2026 at 09:00:00"}}}
	inFolder := MixerAUM()
	inFolder.Files = &FilesPanel{Title: "SESSIONS", Folder: folder, Cells: []FileCell{{Name: name, Detail: "12. Nov 2026 at 21:00:00"}}}
	fresh := AUM{Tempo: "104", Position: "001:1  00:00.0", Strips: strips}
	loading := fresh
	loading.Notices = []string{LoadingNotice}
	freshMenu := fresh
	freshMenu.Session = name
	loadingMenu := freshMenu
	loadingMenu.Notices = []string{LoadingNotice}
	done := fresh
	done.Notices = notices

	return func(gestures, reads int) json.RawMessage {
		switch gestures {
		case 0:
			return window.Source()
		case 1: // the window's ✕
			return old.Source()
		case 2: // ☰
			return menu.Source()
		case 3: // FILES
			return files.Source()
		case 4: // Sessions
			return sessions.Source()
		case 5: // the folder
			return inFolder.Source()
		case 6: // the session's row: AUM loads it
			return loading.Source()
		case 7: // ☰ again, for the name
			if reads < loadingReads {
				return loadingMenu.Source()
			}
			return freshMenu.Source()
		default: // the menu's close icon
			return done.Source()
		}
	}
}

// FreshSessionScreens is OpenSessionScreens in an AUM that was just started,
// for WDA.AfterLaunch: for the first blankReads reads its screen is not drawn
// yet, then it shows its start screen and no plugin window, so the menu is
// the first gesture and the session's row the fifth.
func FreshSessionScreens(folder, name string, strips []Strip, blankReads, loadingReads int, notices ...string) func(gestures, reads int) json.RawMessage {
	open := OpenSessionScreens(folder, name, strips, loadingReads, notices...)
	blank := AUM{}
	start := AUM{Tempo: "120", Position: "001:1  00:00.0", StartScreen: true}
	menu := start
	menu.Session = "Untitled"
	return func(gestures, reads int) json.RawMessage {
		switch {
		case gestures == 0 && reads < blankReads:
			return blank.Source()
		case gestures == 0:
			return start.Source()
		case gestures == 1: // ☰
			return menu.Source()
		default: // FILES and on, as in an AUM that was running
			return open(gestures+1, reads)
		}
	}
}
