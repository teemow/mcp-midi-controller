package song

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Patch applies an id-keyed merge patch (ADR 0002) to a song YAML document and
// returns the patched YAML after checking its structure with Parse.
//
// Maps merge recursively as in RFC 7386, with null removing a key. Lists whose
// entries carry an id — tracks, a track's effects and modulators, the master
// chain, and a stem strip's effects — are addressed in the patch as maps keyed
// by that id: null removes the entry (an id the list does not have is refused),
// an existing id merges in place (and moves if place is given), and a new id
// inserts the entry at place ("first", "last", "after: <id>" or "before: <id>",
// defaulting to "last"). Several entries placed "first" (or "last") in one
// patch keep the patch's key order; the key order of a JSON object is not
// guaranteed to survive (a craft tool's patch is sorted by id), so chain
// entries with "after: <id>" when their order matters. Lists of values (play
// lists, arrangement, notes, automation, events, pedal, depth, locks,
// control_channels, a mapping range) are replaced whole.
//
// A patch changes only the places it names, whatever YAML anchors (&name),
// aliases (*name) and merge keys (<<) the song uses. A value the patch changes
// through an alias becomes a copy of the value the alias names, changed there
// only; the aliases of an anchored value the patch changes, moves before the
// anchor or removes become copies of the value as it was; and a mapping the
// patch changes has its merge keys written out as the keys they bring. Anchors
// and aliases the patch does not reach stay as they are. A patch itself has no
// anchors, aliases or merge keys.
//
// Comments on untouched parts of songYAML are kept. The patched document is
// written out again with a 2-space indent, so the first patch normalizes the
// author's indentation and spacing.
func Patch(songYAML, patchData []byte) ([]byte, error) {
	songDoc, err := decodeMappingDoc("parse song", songYAML)
	if err != nil {
		return nil, err
	}
	patchDoc, err := decodeMappingDoc("parse patch", patchData)
	if err != nil {
		return nil, err
	}
	patchRoot := patchDoc.Content[0]
	if err := checkPatchNode(patchRoot, nil); err != nil {
		return nil, err
	}
	if len(patchRoot.Content) == 0 {
		if _, err := Parse(songYAML); err != nil {
			return nil, err
		}
		return bytes.Clone(songYAML), nil
	}

	songRoot := own(songDoc, songDoc.Content[0])
	songDoc.Content[0] = songRoot
	prepare(songDoc, songRoot, patchRoot, nil)
	if err := mergeMapping(songRoot, patchRoot, nil); err != nil {
		return nil, err
	}
	if err := relinkAliases(songDoc); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(songDoc); err != nil {
		return nil, fmt.Errorf("encode patched song: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encode patched song: %w", err)
	}
	out := buf.Bytes()
	if _, err := Parse(out); err != nil {
		return nil, err
	}
	return out, nil
}

func decodeMappingDoc(label string, data []byte) (*yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s: empty document", label)
		}
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("%s: multiple YAML documents are not supported", label)
		}
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: expected a mapping object at the root", label)
	}
	return &doc, nil
}

// checkPatchNode refuses duplicate keys, keys that are not strings, and the
// anchors, aliases and merge keys a patch has no use for: a patch names each
// value it sets in its place.
func checkPatchNode(n *yaml.Node, segs []string) error {
	if n.Kind == yaml.AliasNode {
		return fmt.Errorf("%s: a patch cannot use an alias (*%s): write the value out", pointerOf(segs), n.Value)
	}
	if n.Anchor != "" {
		return fmt.Errorf("%s: a patch cannot set an anchor (&%s)", pointerOf(segs), n.Anchor)
	}
	switch n.Kind {
	case yaml.MappingNode:
		seen := make(map[string]bool, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if isMergeKey(k) {
				return fmt.Errorf("%s: a patch cannot use a merge key (<<): write the keys out", pointerOf(segs))
			}
			if k.Anchor != "" {
				return fmt.Errorf("%s: a patch cannot set an anchor (&%s)", pointerOf(appendSeg(segs, k.Value)), k.Anchor)
			}
			if k.Kind != yaml.ScalarNode {
				return fmt.Errorf("%s: mapping key must be a string", pointerOf(segs))
			}
			if seen[k.Value] {
				return fmt.Errorf("%s: duplicate key %q in patch", pointerOf(appendSeg(segs, k.Value)), k.Value)
			}
			seen[k.Value] = true
			if err := checkPatchNode(v, appendSeg(segs, k.Value)); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for i, c := range n.Content {
			if err := checkPatchNode(c, appendSeg(segs, fmt.Sprint(i))); err != nil {
				return err
			}
		}
	}
	return nil
}

// prepare makes each node of the song that patch changes its own (own), top
// down and before anything is merged, so every copy an alias or an anchor
// needs holds the value from before the patch. It follows the patch the way
// mergeMapping does and leaves what does not fit for mergeMapping to refuse.
func prepare(doc, target, patch *yaml.Node, segs []string) {
	for i := 0; i+1 < len(patch.Content); i += 2 {
		key, val := patch.Content[i].Value, patch.Content[i+1]
		if val.Kind != yaml.MappingNode {
			continue
		}
		existing, idx := mapLookup(target, key)
		if idx < 0 {
			continue
		}
		existing = own(doc, existing)
		target.Content[idx] = existing
		childSegs := appendSeg(segs, key)
		spec := idListSpecFor(segs, key)
		switch {
		case spec != nil && existing.Kind == yaml.SequenceNode:
			for j := 0; j+1 < len(val.Content); j += 2 {
				id, entryPatch := val.Content[j].Value, val.Content[j+1]
				if entryPatch.Kind != yaml.MappingNode {
					continue
				}
				for k, item := range existing.Content {
					if deref(item).Kind == yaml.MappingNode && spec.entryID(item) == id {
						item = own(doc, item)
						existing.Content[k] = item
						prepare(doc, item, entryPatch, appendSeg(childSegs, id))
						break
					}
				}
			}
		case spec == nil && existing.Kind == yaml.MappingNode:
			prepare(doc, existing, val, childSegs)
		}
	}
}

// own makes n safe to change in place and returns the node to change, which
// the caller puts where n was. An alias becomes a copy of the value it names,
// so the change stays in this place. The aliases of an anchored n become
// copies of n as it is now, so they keep their value. The merge keys (<<) of a
// mapping become the keys they bring, so a patch can change or remove those
// like the mapping's own.
func own(doc, n *yaml.Node) *yaml.Node {
	switch {
	case n.Kind == yaml.AliasNode:
		n = expandAlias(n)
	case n.Anchor != "":
		expandAliasesOf(doc, n)
	}
	if n.Kind == yaml.MappingNode {
		expandMergeKeys(n)
	}
	return n
}

// expandAlias returns a copy of the value alias names, without anchors, that
// carries the alias's comments.
func expandAlias(alias *yaml.Node) *yaml.Node {
	cp := detachedCopy(alias.Alias)
	cp.HeadComment, cp.LineComment, cp.FootComment = alias.HeadComment, alias.LineComment, alias.FootComment
	return cp
}

// expandAliasesOf replaces every alias of target in doc with a copy of target.
func expandAliasesOf(doc, target *yaml.Node) {
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		for i, c := range n.Content {
			if c.Kind == yaml.AliasNode && c.Alias == target {
				n.Content[i] = expandAlias(c)
				continue
			}
			walk(c)
		}
	}
	walk(doc)
}

// relinkAliases makes every alias in doc name the value it named before the
// patch. An alias whose anchored value the patch removed or replaced, or moved
// after it, or that would now name another value of the same anchor, becomes
// a copy of the value it named.
func relinkAliases(doc *yaml.Node) error {
	anchored := map[string]*yaml.Node{}
	var expanding []*yaml.Node
	var walk func(n *yaml.Node) error
	walk = func(n *yaml.Node) error {
		if n.Anchor != "" {
			anchored[n.Anchor] = n
		}
		for i, c := range n.Content {
			if c.Kind != yaml.AliasNode || anchored[c.Value] == c.Alias {
				if err := walk(c); err != nil {
					return err
				}
				continue
			}
			if slices.Contains(expanding, c.Alias) {
				return fmt.Errorf("parse song: alias *%s is inside the value it names", c.Value)
			}
			cp := expandAlias(c)
			n.Content[i] = cp
			expanding = append(expanding, c.Alias)
			err := walk(cp)
			expanding = expanding[:len(expanding)-1]
			if err != nil {
				return err
			}
		}
		return nil
	}
	return walk(doc)
}

// detachedCopy copies n without its anchors. Aliases inside n stay aliases.
func detachedCopy(n *yaml.Node) *yaml.Node {
	cp := cloneNode(n)
	var clear func(n *yaml.Node)
	clear = func(n *yaml.Node) {
		n.Anchor = ""
		for _, c := range n.Content {
			clear(c)
		}
	}
	clear(cp)
	return cp
}

// deref follows an alias to the value it names.
func deref(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	return n
}

// isMergeKey tells a merge key (<<), as yaml.v3 does.
func isMergeKey(k *yaml.Node) bool {
	return k.Kind == yaml.ScalarNode && k.Value == "<<" && (k.Tag == "" || k.Tag == "!" || k.Tag == "!!merge")
}

// expandMergeKeys writes out the merge keys of mapping m as the keys they
// bring that m does not set itself, in the merge key's place.
func expandMergeKeys(m *yaml.Node) {
	if !slices.ContainsFunc(keysOf(m), isMergeKey) {
		return
	}
	have := map[string]bool{}
	for _, k := range keysOf(m) {
		if !isMergeKey(k) {
			have[keyName(k)] = true
		}
	}
	out := make([]*yaml.Node, 0, len(m.Content))
	seen := map[*yaml.Node]bool{m: true}
	for i := 0; i+1 < len(m.Content); i += 2 {
		k, v := m.Content[i], m.Content[i+1]
		if !isMergeKey(k) {
			out = append(out, k, v)
			continue
		}
		for _, kv := range mergedPairs(v, seen) {
			if name := keyName(kv[0]); !have[name] {
				have[name] = true
				out = append(out, detachedCopy(kv[0]), detachedCopy(kv[1]))
			}
		}
	}
	m.Content = out
}

// mergedPairs returns the key-value pairs a merge key's value brings, in the
// order that decides which one wins: a mapping's own keys before those it
// merges, an earlier mapping before a later one.
func mergedPairs(v *yaml.Node, seen map[*yaml.Node]bool) [][2]*yaml.Node {
	v = deref(v)
	srcs := []*yaml.Node{v}
	if v.Kind == yaml.SequenceNode {
		srcs = srcs[:0]
		for _, c := range v.Content {
			srcs = append(srcs, deref(c))
		}
	}
	var out [][2]*yaml.Node
	for _, src := range srcs {
		if src.Kind != yaml.MappingNode || seen[src] {
			continue
		}
		seen[src] = true
		var merged [][2]*yaml.Node
		for i := 0; i+1 < len(src.Content); i += 2 {
			if k := src.Content[i]; isMergeKey(k) {
				merged = append(merged, mergedPairs(src.Content[i+1], seen)...)
			} else {
				out = append(out, [2]*yaml.Node{k, src.Content[i+1]})
			}
		}
		out = append(out, merged...)
	}
	return out
}

func keysOf(m *yaml.Node) []*yaml.Node {
	keys := make([]*yaml.Node, 0, len(m.Content)/2)
	for i := 0; i+1 < len(m.Content); i += 2 {
		keys = append(keys, m.Content[i])
	}
	return keys
}

// keyName is the string a mapping key stands for, through an alias.
func keyName(k *yaml.Node) string { return deref(k).Value }

type idListSpec struct {
	label             string
	requireExplicitID bool
}

// entryID is the id of a list entry, read through an alias and merge keys.
func (s *idListSpec) entryID(m *yaml.Node) string {
	if id := mapScalar(m, "id"); id != "" {
		return id
	}
	if !s.requireExplicitID {
		return mapScalar(m, "plugin")
	}
	return ""
}

var (
	trackListSpec     = idListSpec{label: "tracks", requireExplicitID: true}
	effectListSpec    = idListSpec{label: "effects", requireExplicitID: false}
	modulatorListSpec = idListSpec{label: "modulators", requireExplicitID: false}
	masterListSpec    = idListSpec{label: "master effects", requireExplicitID: false}
)

func idListSpecFor(segs []string, key string) *idListSpec {
	switch {
	case len(segs) == 0 && key == "tracks":
		return &trackListSpec
	case len(segs) == 0 && key == "master":
		return &masterListSpec
	case len(segs) == 2 && segs[0] == "tracks" && key == "effects":
		return &effectListSpec
	case len(segs) == 2 && segs[0] == "tracks" && key == "modulators":
		return &modulatorListSpec
	case len(segs) == 2 && segs[0] == "stems" && key == "effects":
		return &effectListSpec
	default:
		return nil
	}
}

func mergeMapping(targetMap, patchMap *yaml.Node, segs []string) error {
	for i := 0; i+1 < len(patchMap.Content); i += 2 {
		kNode, vNode := patchMap.Content[i], patchMap.Content[i+1]
		key := kNode.Value
		childSegs := appendSeg(segs, key)

		if isNullNode(vNode) {
			mapDelete(targetMap, key)
			continue
		}

		if spec := idListSpecFor(segs, key); spec != nil {
			if vNode.Kind != yaml.MappingNode {
				return fmt.Errorf("%s: in a patch, %s are keyed by id ({<id>: {...}}), not a %s",
					pointerOf(childSegs), spec.label, nodeKindName(vNode))
			}
			existing, _ := mapLookup(targetMap, key)
			var seq *yaml.Node
			switch {
			case existing == nil || isNullNode(existing):
				seq = &yaml.Node{Kind: yaml.SequenceNode}
			case existing.Kind == yaml.SequenceNode:
				seq = existing
			default:
				return fmt.Errorf("%s: expected a list in the song document, found %s",
					pointerOf(childSegs), nodeKindName(existing))
			}
			if err := patchIDList(seq, vNode, childSegs, spec); err != nil {
				return err
			}
			switch {
			case len(seq.Content) > 0:
				targetMap.Style &^= yaml.FlowStyle
				seq.Style &^= yaml.FlowStyle
				mapSet(targetMap, kNode, seq)
			case len(segs) == 0 && key == "tracks":
				mapSet(targetMap, kNode, &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle})
			default:
				mapDelete(targetMap, key)
			}
			continue
		}

		if vNode.Kind == yaml.MappingNode {
			existing, _ := mapLookup(targetMap, key)
			var dst *yaml.Node
			if existing != nil && existing.Kind == yaml.MappingNode {
				dst = existing
			} else {
				dst = &yaml.Node{Kind: yaml.MappingNode}
			}
			if err := mergeMapping(dst, vNode, childSegs); err != nil {
				return err
			}
			if dst.Style == 0 {
				targetMap.Style &^= yaml.FlowStyle
			}
			mapSet(targetMap, kNode, dst)
			continue
		}

		val := normalizePatchNode(vNode)
		if val.Kind == yaml.SequenceNode && val.Style == 0 {
			targetMap.Style &^= yaml.FlowStyle
		}
		mapSet(targetMap, kNode, val)
	}
	return nil
}

type placeKind int

const (
	placeNone placeKind = iota
	placeFirst
	placeLast
	placeAfter
	placeBefore
)

type placement struct {
	kind   placeKind
	anchor string
}

type pendingItem struct {
	id    string
	node  *yaml.Node
	place placement
	segs  []string
}

func patchIDList(seq, patchMap *yaml.Node, segs []string, spec *idListSpec) error {
	items := make([]*yaml.Node, 0, len(seq.Content))
	seenExisting := make(map[string]bool, len(seq.Content))
	for idx, item := range seq.Content {
		if deref(item).Kind != yaml.MappingNode {
			return fmt.Errorf("%s/%d: expected an object entry, found %s", pointerOf(segs), idx, nodeKindName(item))
		}
		id := spec.entryID(item)
		if id == "" {
			return fmt.Errorf("%s/%d: entry has no id", pointerOf(segs), idx)
		}
		if seenExisting[id] {
			return fmt.Errorf("%s/%d/id: duplicate entry %q", pointerOf(segs), idx, id)
		}
		seenExisting[id] = true
		items = append(items, item)
	}

	findItem := func(id string) int {
		for i, it := range items {
			if spec.entryID(it) == id {
				return i
			}
		}
		return -1
	}

	// Pass 1: apply null removals so freed ids or removed anchors are settled
	// before updates and insertions run. Removing an id the list does not have
	// is refused, so a typo or an index ("0") does not pass for a removal.
	stored := idsOf(items, spec)
	for i := 0; i+1 < len(patchMap.Content); i += 2 {
		key, val := patchMap.Content[i].Value, patchMap.Content[i+1]
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("%s: entry id must not be empty", pointerOf(segs))
		}
		if !isNullNode(val) {
			continue
		}
		idx := findItem(key)
		if idx < 0 {
			return fmt.Errorf("%s: no entry %q to remove (have %s)", pointerOf(appendSeg(segs, key)), key, stored)
		}
		items = slices.Delete(items, idx, idx+1)
	}

	// Pass 2: merge updates into existing entries and prepare new or moved
	// entries for placement.
	var pending []pendingItem
	for i := 0; i+1 < len(patchMap.Content); i += 2 {
		key, val := patchMap.Content[i].Value, patchMap.Content[i+1]
		if isNullNode(val) {
			continue
		}
		entrySegs := appendSeg(segs, key)
		if val.Kind != yaml.MappingNode {
			return fmt.Errorf("%s: expected an object or null, found %s", pointerOf(entrySegs), nodeKindName(val))
		}
		cleanPatch, pl, err := extractPlacement(val, key, entrySegs)
		if err != nil {
			return err
		}

		idx := findItem(key)
		isExisting := idx >= 0
		if err := checkPatchEntryID(cleanPatch, key, isExisting, entrySegs, spec); err != nil {
			return err
		}

		var entryMap *yaml.Node
		if isExisting {
			entryMap = items[idx]
		} else {
			entryMap = &yaml.Node{Kind: yaml.MappingNode}
		}
		if err := mergeMapping(entryMap, cleanPatch, entrySegs); err != nil {
			return err
		}
		if err := ensureEntryID(entryMap, key, entrySegs, spec); err != nil {
			return err
		}

		if isExisting {
			if pl.kind == placeNone {
				continue
			}
			items = slices.Delete(items, idx, idx+1)
		} else if pl.kind == placeNone {
			pl.kind = placeLast
		}
		pending = append(pending, pendingItem{id: key, node: entryMap, place: pl, segs: entrySegs})
	}

	// Pass 3: place the "first" and "last" entries before any anchored one, so
	// several of them keep the patch's key order as one block at either end,
	// and an entry anchored on one of them ("after: hp") stays right next to it
	// however the keys are ordered: a later "first" entry cannot land between
	// hp and the entry chained after it, as a later "last" one never could.
	firsts := 0
	anchored := pending[:0]
	for _, p := range pending {
		switch p.place.kind {
		case placeFirst:
			items = slices.Insert(items, firsts, p.node)
			firsts++
		case placeLast:
			items = append(items, p.node)
		default:
			anchored = append(anchored, p)
		}
	}
	pending = anchored

	// Pass 4: resolve anchored placements across passes so chained insertions
	// (e.g. comp "after: hp" alongside hp "first") work regardless of key order.
	for len(pending) > 0 {
		progressed := false
		rest := pending[:0]
		for _, p := range pending {
			switch p.place.kind {
			case placeAfter:
				if idx := findItem(p.place.anchor); idx >= 0 {
					items = slices.Insert(items, idx+1, p.node)
					progressed = true
				} else {
					rest = append(rest, p)
				}
			case placeBefore:
				if idx := findItem(p.place.anchor); idx >= 0 {
					items = slices.Insert(items, idx, p.node)
					progressed = true
				} else {
					rest = append(rest, p)
				}
			}
		}
		if !progressed {
			first := rest[0]
			for _, other := range rest[1:] {
				if other.id == first.place.anchor {
					return fmt.Errorf("%s/place: circular placement involving %q and %q",
						pointerOf(first.segs), first.id, first.place.anchor)
				}
			}
			return fmt.Errorf("%s/place: no entry %q (have %s)", pointerOf(first.segs), first.place.anchor, idsOf(items, spec))
		}
		pending = rest
	}

	seq.Content = items
	return nil
}

// idsOf names the ids of a list's entries, or "none".
func idsOf(items []*yaml.Node, spec *idListSpec) string {
	if len(items) == 0 {
		return "none"
	}
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = spec.entryID(it)
	}
	return strings.Join(ids, ", ")
}

func checkPatchEntryID(cleanPatch *yaml.Node, key string, existing bool, segs []string, spec *idListSpec) error {
	idVal, idx := mapLookup(cleanPatch, "id")
	if idx < 0 {
		return nil
	}
	if isNullNode(idVal) {
		if spec.requireExplicitID {
			if existing {
				return fmt.Errorf("%s/id: cannot remove id %q (remove %q with null and add the new id instead)", pointerOf(segs), key, key)
			}
			return fmt.Errorf("%s/id: id cannot be null", pointerOf(segs))
		}
		return nil
	}
	if idVal.Kind != yaml.ScalarNode || idVal.Tag != "!!str" {
		return fmt.Errorf("%s/id: expected a string id", pointerOf(segs))
	}
	if idVal.Value != key {
		if existing {
			return fmt.Errorf("%s/id: cannot change id %q to %q (remove %q with null and add %q instead)",
				pointerOf(segs), key, idVal.Value, key, idVal.Value)
		}
		return fmt.Errorf("%s/id: %q does not match patch key %q", pointerOf(segs), idVal.Value, key)
	}
	return nil
}

func ensureEntryID(entryMap *yaml.Node, key string, segs []string, spec *idListSpec) error {
	if got := spec.entryID(entryMap); got == key {
		return nil
	}
	if idVal, idx := mapLookup(entryMap, "id"); idx >= 0 && idVal.Kind == yaml.ScalarNode && idVal.Value != "" && idVal.Value != key {
		return fmt.Errorf("%s/id: %q does not match patch key %q", pointerOf(segs), idVal.Value, key)
	}
	mapPrependScalar(entryMap, "id", key)
	return nil
}

func extractPlacement(entryPatch *yaml.Node, entryID string, segs []string) (*yaml.Node, placement, error) {
	clean := &yaml.Node{Kind: yaml.MappingNode}
	var found []struct {
		key string
		val *yaml.Node
	}
	for i := 0; i+1 < len(entryPatch.Content); i += 2 {
		k, v := entryPatch.Content[i], entryPatch.Content[i+1]
		switch k.Value {
		case "place", "after", "before":
			found = append(found, struct {
				key string
				val *yaml.Node
			}{key: k.Value, val: v})
		default:
			clean.Content = append(clean.Content, cloneNode(k), cloneNode(v))
		}
	}
	if len(found) == 0 {
		return clean, placement{}, nil
	}
	if len(found) > 1 {
		return nil, placement{}, fmt.Errorf("%s/place: specify placement once (place, after or before)", pointerOf(segs))
	}
	f := found[0]
	if isNullNode(f.val) {
		return clean, placement{}, nil
	}
	pl, err := parsePlacement(f.key, f.val, pointerOf(segs))
	if err != nil {
		return nil, placement{}, err
	}
	if (pl.kind == placeAfter || pl.kind == placeBefore) && pl.anchor == entryID {
		return nil, placement{}, fmt.Errorf("%s/place: %q cannot be placed relative to itself", pointerOf(segs), entryID)
	}
	return clean, pl, nil
}

func parsePlacement(field string, n *yaml.Node, entryPtr string) (placement, error) {
	at := entryPtr + "/place"
	switch field {
	case "after":
		id, ok := nonEmptyStringScalar(n)
		if !ok {
			return placement{}, fmt.Errorf("%s: name the entry id after %q", at, field)
		}
		return placement{kind: placeAfter, anchor: id}, nil
	case "before":
		id, ok := nonEmptyStringScalar(n)
		if !ok {
			return placement{}, fmt.Errorf("%s: name the entry id after %q", at, field)
		}
		return placement{kind: placeBefore, anchor: id}, nil
	}

	switch n.Kind {
	case yaml.ScalarNode:
		if n.Tag != "!!str" {
			return placement{}, fmt.Errorf("%s: expected \"first\", \"last\", \"after: <id>\" or \"before: <id>\"", at)
		}
		s := strings.TrimSpace(n.Value)
		switch {
		case s == "first":
			return placement{kind: placeFirst}, nil
		case s == "last":
			return placement{kind: placeLast}, nil
		case strings.HasPrefix(s, "after:"):
			id := strings.TrimSpace(strings.TrimPrefix(s, "after:"))
			if id == "" {
				return placement{}, fmt.Errorf("%s: name the entry id in %q", at, n.Value)
			}
			return placement{kind: placeAfter, anchor: id}, nil
		case strings.HasPrefix(s, "after "):
			id := strings.TrimSpace(strings.TrimPrefix(s, "after "))
			if id == "" {
				return placement{}, fmt.Errorf("%s: name the entry id in %q", at, n.Value)
			}
			return placement{kind: placeAfter, anchor: id}, nil
		case strings.HasPrefix(s, "before:"):
			id := strings.TrimSpace(strings.TrimPrefix(s, "before:"))
			if id == "" {
				return placement{}, fmt.Errorf("%s: name the entry id in %q", at, n.Value)
			}
			return placement{kind: placeBefore, anchor: id}, nil
		case strings.HasPrefix(s, "before "):
			id := strings.TrimSpace(strings.TrimPrefix(s, "before "))
			if id == "" {
				return placement{}, fmt.Errorf("%s: name the entry id in %q", at, n.Value)
			}
			return placement{kind: placeBefore, anchor: id}, nil
		default:
			return placement{}, fmt.Errorf("%s: %q is not \"first\", \"last\", \"after: <id>\" or \"before: <id>\"", at, n.Value)
		}
	case yaml.MappingNode:
		if len(n.Content) == 2 && n.Content[0].Kind == yaml.ScalarNode {
			rel := n.Content[0].Value
			id, ok := nonEmptyStringScalar(n.Content[1])
			switch {
			case (rel == "after" || rel == "before") && !ok:
				return placement{}, fmt.Errorf("%s: name the entry id for %q", at, rel)
			case rel == "after":
				return placement{kind: placeAfter, anchor: id}, nil
			case rel == "before":
				return placement{kind: placeBefore, anchor: id}, nil
			}
		}
		return placement{}, fmt.Errorf("%s: expected \"first\", \"last\", \"after: <id>\" or \"before: <id>\"", at)
	default:
		return placement{}, fmt.Errorf("%s: expected \"first\", \"last\", \"after: <id>\" or \"before: <id>\"", at)
	}
}

func nonEmptyStringScalar(n *yaml.Node) (string, bool) {
	if n == nil || n.Kind != yaml.ScalarNode || isNullNode(n) || n.Tag != "!!str" {
		return "", false
	}
	s := strings.TrimSpace(n.Value)
	return s, s != ""
}

func isNullNode(n *yaml.Node) bool {
	return n != nil && n.Kind == yaml.ScalarNode && n.Tag == "!!null"
}

func nodeKindName(n *yaml.Node) string {
	n = deref(n)
	if n == nil || isNullNode(n) {
		return "null"
	}
	switch n.Kind {
	case yaml.MappingNode:
		return "object"
	case yaml.SequenceNode:
		return "list"
	default:
		return "scalar"
	}
}

// mapLookup finds the value of one of m's own keys and its index in m.Content.
func mapLookup(m *yaml.Node, key string) (*yaml.Node, int) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if !isMergeKey(m.Content[i]) && keyName(m.Content[i]) == key {
			return m.Content[i+1], i + 1
		}
	}
	return nil, -1
}

// mapScalar reads a scalar of m, through aliases and the keys m merges.
func mapScalar(m *yaml.Node, key string) string {
	m = deref(m)
	v, _ := mapLookup(m, key)
	if v == nil {
		for _, kv := range mergedPairs(m, map[*yaml.Node]bool{}) {
			if keyName(kv[0]) == key {
				v = kv[1]
				break
			}
		}
	}
	v = deref(v)
	if v == nil || v.Kind != yaml.ScalarNode || isNullNode(v) {
		return ""
	}
	return v.Value
}

func mapSet(m *yaml.Node, keyNode, valNode *yaml.Node) {
	if _, valIdx := mapLookup(m, keyNode.Value); valIdx >= 0 {
		m.Content[valIdx] = valNode
		return
	}
	k := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: keyNode.Value}
	m.Content = append(m.Content, k, valNode)
}

func mapPrependScalar(m *yaml.Node, key, val string) {
	if _, valIdx := mapLookup(m, key); valIdx >= 0 {
		m.Content[valIdx] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: val}
		return
	}
	k := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	v := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: val}
	m.Content = append([]*yaml.Node{k, v}, m.Content...)
}

func mapDelete(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if !isMergeKey(m.Content[i]) && keyName(m.Content[i]) == key {
			m.Content = slices.Delete(m.Content, i, i+2)
			return
		}
	}
}

func cloneNode(n *yaml.Node) *yaml.Node {
	if n == nil {
		return nil
	}
	cp := *n
	if len(n.Content) > 0 {
		cp.Content = make([]*yaml.Node, len(n.Content))
		for i, c := range n.Content {
			cp.Content[i] = cloneNode(c)
		}
	}
	return &cp
}

func normalizePatchNode(n *yaml.Node) *yaml.Node {
	cp := cloneNode(n)
	normalizePatchTree(cp)
	return cp
}

func normalizePatchTree(n *yaml.Node) {
	if n == nil {
		return
	}
	switch n.Kind {
	case yaml.MappingNode:
		n.Style &^= yaml.FlowStyle
		for i := 0; i+1 < len(n.Content); i += 2 {
			// Clear JSON quotes on plain keys; yaml.v3 re-quotes any key that needs quoting.
			n.Content[i].Style = 0
			normalizePatchTree(n.Content[i+1])
		}
	case yaml.SequenceNode:
		allScalar := len(n.Content) > 0
		for _, c := range n.Content {
			if c.Kind != yaml.ScalarNode {
				allScalar = false
			}
			normalizePatchTree(c)
		}
		if allScalar {
			n.Style = yaml.FlowStyle
		} else {
			n.Style &^= yaml.FlowStyle
		}
	}
}

func appendSeg(segs []string, seg string) []string {
	out := make([]string, len(segs)+1)
	copy(out, segs)
	out[len(segs)] = seg
	return out
}

func pointerOf(segs []string) string {
	if len(segs) == 0 {
		return "/"
	}
	escaped := make([]string, len(segs))
	for i, s := range segs {
		s = strings.ReplaceAll(s, "~", "~0")
		s = strings.ReplaceAll(s, "/", "~1")
		escaped[i] = s
	}
	return "/" + strings.Join(escaped, "/")
}
