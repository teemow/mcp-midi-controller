package ipadtest

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// The probe app's texts the fake draws (package ipad names the same ones; it
// cannot be imported here, its own tests import this package).
const (
	probeUnitsTab       = "audio units"
	probeSessionsTab    = "aum sessions"
	probeConflict       = "conflict — both sides changed"
	probeKeepController = "keep controller"
	probeKeepDevice     = "keep device"
)

// ProbeFile is one file of the fake probe app's session browser.
type ProbeFile struct {
	// Name is the file's name with its extension.
	Name string
	// Info is the row's line about the copy in AUM's folder; empty draws
	// "session · 105 KB · 12. Nov 2026 at 02:06".
	Info string
	// State is the row's sync badge: "", "pulling…", "new from controller",
	// the text of an error, or Conflict.
	State string
}

// ProbeConflictState is the badge of a file the app holds as a conflict.
const ProbeConflictState = probeConflict

// ProbeApp is a stand-in for the AUv3 Probe app's window: its two tabs, the
// sessions tab's folder browser, and what a tap or a click does to them. The
// shapes follow WDA's JSON source of the app (the tab bar, the header with
// its sync button and status line, the navigation bar below the browser's
// root, folder rows, file rows with their badge, and a conflict's two buttons
// after its row); the names are made up. Rows are 80 points high and the
// list is drawn whole, so a long folder runs out of the window as the app's
// does.
type ProbeApp struct {
	mu sync.Mutex
	// Tab is the tab shown: "audio units", as after a start, when empty.
	Tab string
	// Units is the audio units tab's footer ("syncing 2/184…").
	Units string
	// Status is the sessions tab's status line; empty draws the app's own:
	// the number of conflicts, or "synced · N files · 02:00".
	Status string
	// Cycle disables the sync button, as a running sync cycle does.
	Cycle bool
	// Unlinked draws the sessions tab without its browser: no AUM folder.
	Unlinked bool
	// IDs draws the accessibility identifiers of an app with the sync report:
	// "keep-device:<path>" and "keep-controller:<path>" on a conflict's
	// buttons, "aum-folder:<path>" on the browser's navigation bar. StaleBar,
	// when set, is the folder path the bar names instead of the one shown (a
	// bar that kept the identifier of another screen).
	IDs      bool
	StaleBar string
	// Path is the folder the browser shows, nil at its root.
	Path []string
	// Files are the files by folder ("" is AUM's folder itself, "Songs" a
	// folder in it, "Sets/Live" one further down).
	Files map[string][]ProbeFile
	// Pinned lists a folder a second time at the root, above the others,
	// under the last part of its path.
	Pinned string
	// Arrivals are file names in the root's strip of arrivals.
	Arrivals []string
	// OnKeepController is called with the file's path after its "keep
	// controller" was clicked and its conflict cleared; OnSync after a tap
	// on the sync button. Both run outside the app's lock.
	OnKeepController func(path string)
	OnSync           func()
	// after are the hooks a tap or click has set off.
	after []func()
	// Log lists what the app was asked to do, in order: "tab aum sessions",
	// "back", "folder Songs", "sync", "keep controller Songs/A.aumproj",
	// "keep device …", and "tap x,y" for a tap on nothing.
	Log []string
}

// Actions returns the app's log so far.
func (p *ProbeApp) Actions() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.Log...)
}

// Set changes the app's state under its lock.
func (p *ProbeApp) Set(change func(p *ProbeApp)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	change(p)
}

// SetState changes the badge of the file at path ("Songs/A.aumproj").
func (p *ProbeApp) SetState(path, state string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if f := p.file(path); f != nil {
		f.State = state
	}
}

func (p *ProbeApp) file(path string) *ProbeFile {
	folder, name := "", path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		folder, name = path[:i], path[i+1:]
	}
	for i := range p.Files[folder] {
		if p.Files[folder][i].Name == name {
			return &p.Files[folder][i]
		}
	}
	return nil
}

// probeTarget is something on the app's screen that answers a tap or a click.
type probeTarget struct {
	label string
	// id is the button's accessibility identifier, "" without one.
	id   string
	rect [4]float64
	do   func()
}

func (p *ProbeApp) tab() string {
	if p.Tab == "" {
		return probeUnitsTab
	}
	return p.Tab
}

// draw lays the window out: its tree and what answers a tap. p.mu is held.
func (p *ProbeApp) draw() (node, []probeTarget) {
	const W, H = 1210.0, 834.0
	var targets []probeTarget
	button := func(label string, x, y, w, h float64, do func(), kids ...node) node {
		targets = append(targets, probeTarget{label: label, rect: [4]float64{x, y, w, h}, do: do})
		return el("Button", label, x, y, w, h, kids...)
	}
	// identify gives the button drawn last its accessibility identifier.
	identify := func(b node, id string) node {
		if !p.IDs {
			return b
		}
		targets[len(targets)-1].id = id
		return b.with("rawIdentifier", id).with("name", id)
	}
	logf := func(format string, a ...any) { p.Log = append(p.Log, fmt.Sprintf(format, a...)) }

	var content []node
	if p.tab() == probeUnitsTab {
		content = append(content,
			el("StaticText", probeUnitsTab, 16, 203, 136, 24),
			button("resync", 1089, 205, 106, 35, func() { logf("resync") }),
			el("ScrollView", "", 0, 254, W, 462,
				el("Other", "", 0, 254, W, 400,
					el("StaticText", "2/184 synced", 1097, 270, 97, 16),
					button("inspect Acme Synth", 28, 364, 1154, 56, func() { logf("inspect") },
						el("StaticText", "Acme Synth", 74, 364, 85, 21),
						el("Image", "Forward", 1175, 367, 7, 11),
						el("StaticText", "aumu · Acme · v1.0.0", 74, 387, 186, 15)))))
		if p.Units != "" {
			content = append(content, el("StaticText", p.Units, 40, 732, 113, 16))
		}
	} else {
		status := p.Status
		if status == "" {
			conflicts, files := 0, 0
			for _, fs := range p.Files {
				for _, f := range fs {
					files++
					if f.State == probeConflict {
						conflicts++
					}
				}
			}
			switch {
			case conflicts == 1:
				status = "1 conflict — pick a side below"
			case conflicts > 1:
				status = fmt.Sprintf("%d conflicts — pick a side below", conflicts)
			default:
				status = fmt.Sprintf("synced · %d files · 02:00", files)
			}
		}
		sync := button("sync", 963, 205, 88, 34, func() {
			logf("sync")
			if p.OnSync != nil {
				p.after = append(p.after, p.OnSync)
			}
		}, el("StaticText", "sync", 975, 213, 64, 19))
		if p.Cycle {
			sync.with("isEnabled", "0")
		}
		content = append(content,
			el("StaticText", probeSessionsTab, 16, 203, 149, 24),
			sync,
			button("open file", 1062, 204, 133, 37, func() { logf("open file") }),
			el("StaticText", status, 16, 228, 177, 14))
		if !p.Unlinked {
			content = append(content, p.browser(button, identify, logf))
		} else {
			content = append(content, el("StaticText", "sessions on device", 16, 270, 200, 16))
		}
		content = append(content,
			el("StaticText", "192.0.2.10:7800 · 3 staged", 784, 729, 231, 16),
			button("clear staging", 1024, 720, 171, 35, func() { logf("clear staging") }))
	}

	tabButton := func(label string, x, w float64) node {
		b := button(label, x, 765, w, 49, func() {
			logf("tab %s", label)
			// A tap on the tab that is shown returns its browser to the root.
			if p.tab() == label && label == probeSessionsTab {
				p.Path = nil
			}
			p.Tab = label
		})
		if p.tab() == label {
			b.with("value", "1")
		}
		return b
	}
	root := el("Application", "AUv3 Probe", 0, 0, W, H,
		el("Window", "", 0, 0, W, H,
			el("Other", "", 0, 0, W, H,
				el("StaticText", "auv3probe", 58, 44, 95, 21),
				el("StaticText", "connected · 192.0.2.10:7800", 56, 118, 223, 15),
				el("Other", "", 0, 191, W, 643, content...)),
			el("TabBar", "Tab Bar", 0, 764, W, 70,
				tabButton(probeUnitsTab, 168, 423),
				tabButton(probeSessionsTab, 595, 447))))
	return root, targets
}

// browser draws the folder browser at p.Path.
func (p *ProbeApp) browser(button func(string, float64, float64, float64, float64, func(), ...node) node, identify func(node, string) node, logf func(string, ...any)) node {
	const W = 1210.0
	here := strings.Join(p.Path, "/")
	var parts []node
	y := 270.0
	if len(p.Path) > 0 {
		bar := el("NavigationBar", "", 0, 254, W, 50,
			button("Back", 0, 257, 73, 44, func() {
				logf("back")
				p.Path = p.Path[:len(p.Path)-1]
			}),
			el("StaticText", p.Path[len(p.Path)-1], 580, 269, 50, 21))
		if p.IDs {
			shown := here
			if p.StaleBar != "" {
				shown = p.StaleBar
			}
			bar.with("rawIdentifier", "aum-folder:"+shown)
		}
		parts = append(parts, bar)
		y = 332
	}

	var rows []node
	folderRow := func(path string) {
		name := path[strings.LastIndex(path, "/")+1:]
		count, attention := 0, false
		for folder, fs := range p.Files {
			if folder == path || strings.HasPrefix(folder, path+"/") {
				count += len(fs)
				for _, f := range fs {
					attention = attention || f.State == probeConflict
				}
			}
		}
		label := fmt.Sprintf("%s, %d", name, count)
		kids := []node{el("Image", "Move", 30, y+2, 19, 16), el("StaticText", name, 61, y, 127, 21)}
		if attention {
			label = fmt.Sprintf("%s, has changes that need attention, %d", name, count)
			kids = append(kids, el("Other", "has changes that need attention", 197, y+7, 7, 7))
		}
		kids = append(kids, el("StaticText", fmt.Sprint(count), 1155, y+2, 9, 17), el("Image", "Forward", 1175, y+5, 7, 11))
		rows = append(rows, button(label, 28, y, 1154, 21, func() {
			logf("folder %s", path)
			p.Path = strings.Split(path, "/")
		}, kids...))
		y += 45
	}
	if here == "" {
		for _, name := range p.Arrivals {
			label := name + ", new from controller — tap to open in aum"
			rows = append(rows,
				button(label, 28, y, 1132, 36, func() { logf("open arrival %s", name) },
					el("StaticText", name, 55, y, 180, 21),
					el("StaticText", "new from controller — tap to open in aum", 55, y+22, 273, 14)),
				button("dismiss "+name, 1171, y+13, 10, 10, func() { logf("dismiss %s", name) }))
			y += 61
		}
		if p.Pinned != "" {
			folderRow(p.Pinned)
		}
	}
	// The folders directly below this one.
	seen := map[string]bool{}
	var below []string
	for folder := range p.Files {
		rest := folder
		if here != "" {
			var ok bool
			if rest, ok = strings.CutPrefix(folder, here+"/"); !ok {
				continue
			}
		}
		if rest == "" {
			continue
		}
		name, _, _ := strings.Cut(rest, "/")
		if !seen[name] {
			seen[name] = true
			below = append(below, name)
		}
	}
	sort.Strings(below)
	for _, name := range below {
		folderRow(strings.TrimPrefix(here+"/"+name, "/"))
	}
	for _, f := range p.Files[here] {
		path := strings.TrimPrefix(here+"/"+f.Name, "/")
		info := f.Info
		if info == "" {
			info = "session · 105 KB · 12. Nov 2026 at 02:06"
		}
		kids := []node{
			el("StaticText", f.Name, 28, y, 221, 21),
			el("Image", "Play", 1127, y+3, 18, 18),
			el("StaticText", info, 28, y+23, 297, 15),
		}
		if f.State != "" {
			kids = append(kids, el("StaticText", f.State, 29, y+41, 237, 15))
		}
		rows = append(rows, button("open "+f.Name+" in aum", 28, y, 1118, 56, func() { logf("open %s", path) }, kids...))
		if strings.HasSuffix(f.State, "…") {
			rows = append(rows, el("ActivityIndicator", "", 1168, y, 14, 14))
		} else {
			rows = append(rows, button("actions for "+f.Name, 1158, y, 24, 20, func() { logf("actions %s", path) }))
		}
		if f.State == probeConflict {
			rows = append(rows,
				identify(button(probeKeepDevice, 27, y+67, 152, 35, func() { logf("keep device %s", path) },
					el("StaticText", probeKeepDevice, 42, y+75, 125, 19)), "keep-device:"+path),
				identify(button(probeKeepController, 186, y+67, 194, 34, func() {
					logf("keep controller %s", path)
					if file := p.file(path); file != nil {
						file.State = ""
					}
					if hook := p.OnKeepController; hook != nil {
						p.after = append(p.after, func() { hook(path) })
					}
				}, el("StaticText", probeKeepController, 200, y+75, 168, 19)), "keep-controller:"+path))
			y += 46
		}
		y += 80
	}
	parts = append(parts, el("ScrollView", "", 0, 254, W, 456, el("Other", "", 0, 254, W, y-254, rows...)))
	return el("Other", "", 0, 254, W, 456, parts...)
}

// source is the app's tree as WDA's JSON source value.
func (p *ProbeApp) source() json.RawMessage {
	p.mu.Lock()
	defer p.mu.Unlock()
	root, _ := p.draw()
	b, _ := json.Marshal(root)
	return b
}

// Source renders the app's window as WDA's JSON source value.
func (p *ProbeApp) Source() json.RawMessage { return p.source() }

// tap acts on a tap at a point: whatever lies there on screen. A row below
// the window's edge is not on screen and cannot be tapped.
func (p *ProbeApp) tap(x, y float64) {
	p.mu.Lock()
	defer p.hooks()
	_, targets := p.draw()
	if y <= 834 {
		// The front-most is drawn last.
		for i := len(targets) - 1; i >= 0; i-- {
			r := targets[i].rect
			if x >= r[0] && x <= r[0]+r[2] && y >= r[1] && y <= r[1]+r[3] {
				targets[i].do()
				return
			}
		}
	}
	p.Log = append(p.Log, fmt.Sprintf("tap %v,%v", x, y))
}

// buttons lists the buttons labelled label, in the tree's order.
func (p *ProbeApp) buttons(label string, fold bool) [][4]float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, targets := p.draw()
	var out [][4]float64
	for _, t := range targets {
		if t.label == label || fold && strings.EqualFold(t.label, label) {
			out = append(out, t.rect)
		}
	}
	return out
}

// named lists the buttons WDA's "name" finds under name, with their labels:
// a button's name is its identifier, and its label only when it has none.
func (p *ProbeApp) named(name string) (labels []string, rects [][4]float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, targets := p.draw()
	for _, t := range targets {
		if t.id == name || t.id == "" && t.label == name {
			labels, rects = append(labels, t.label), append(rects, t.rect)
		}
	}
	return labels, rects
}

// click acts on a click on the button labelled label at rect: WDA scrolls an
// element into sight before it clicks it, so the row may lie anywhere.
func (p *ProbeApp) click(label string, rect [4]float64) bool {
	p.mu.Lock()
	defer p.hooks()
	_, targets := p.draw()
	for _, t := range targets {
		if strings.EqualFold(t.label, label) && t.rect == rect {
			t.do()
			return true
		}
	}
	return false
}

// hooks releases the app's lock and runs what the tap or click set off.
func (p *ProbeApp) hooks() {
	after := p.after
	p.after = nil
	p.mu.Unlock()
	for _, f := range after {
		f()
	}
}
