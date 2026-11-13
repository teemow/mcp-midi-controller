package mcpserver

// This file brings back a plugin that AUM drew as failed when it loaded a
// song's session. Some plugins fail as AUM initialises them on nearly every
// load of a session, and come up when the node is loaded again: RELOAD in the
// node's menu, which aum_node reload taps. That is cheaper than loading the
// whole session again, which only throws the dice once more.

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/teemow/mcp-midi-controller/internal/ipad"
	"github.com/teemow/mcp-midi-controller/internal/song"
)

// The reload of a failed node. They are variables so the tests need not sit
// them out.
var (
	// songReloadRounds bounds how often the nodes AUM draws as failed are
	// reloaded before the load gives up on them.
	songReloadRounds = 3
	// songReloadWait is how long the reloaded plugins are given to come up
	// before the mixer is read again.
	songReloadWait = 3 * time.Second
)

// reloadFailed reloads every node AUM draws as failed in the session just
// loaded, gives the plugins songReloadWait to come up and reads the mixer
// again, up to songReloadRounds times or until no node is drawn failed.
// loaded is what AUM showed after the load, and is updated with every read.
// ok says that no node is drawn failed any more. A reload starts the plugin
// anew (on the rig Perforator came back at its Init preset); the song's
// opening sound puts its preset and parameters back: the check sends it, and
// a sequenced pass at the transport's start.
func (s *Server) reloadFailed(ctx context.Context, cs *compiledSong, loaded *ipad.AUMLoaded) (ok bool, detail string) {
	if len(loaded.FailedNodes) == 0 {
		// A read that names the strips but not the nodes (it has no
		// screenshot of its own to tell) leaves nothing to reload.
		return false, fmt.Sprintf("AUM draws a node of %s as failed, and which node is not known: reload it with aum_node reload", quoteList(loaded.Failed))
	}
	first := slices.Clone(loaded.FailedNodes)
	var trouble []string
	rounds := 0
	for rounds < songReloadRounds && len(loaded.FailedNodes) > 0 {
		rounds++
		for _, n := range loaded.FailedNodes {
			res, err := s.ipad.AUMNode(ctx, ipad.AUMRequest{Action: "reload", Channel: n.Strip, Node: n.Node})
			switch {
			case ctx.Err() != nil:
				return false, fmt.Sprintf("stopped while %s was reloaded", failedName(cs, n))
			case err != nil:
				why, _ := s.ipadFailure(ctx, err)
				trouble = append(trouble, fmt.Sprintf("the reload of %s did not go through: %s", failedName(cs, n), why))
			case !res.Verified:
				trouble = append(trouble, fmt.Sprintf("the reload of %s did not go through: %s", failedName(cs, n), res.Message))
			}
		}
		if err := sleepContext(ctx, songReloadWait); err != nil {
			return false, "stopped while the reloaded plugins came up"
		}
		res, err := s.ipad.AUMMixer(ctx)
		if err != nil {
			why, _ := s.ipadFailure(ctx, err)
			return false, fmt.Sprintf("reloaded %s; AUM's mixer could not be read after that: %s", failedNames(cs, first), why)
		}
		now, _ := res.Data.(ipad.AUMLoaded)
		loaded.Strips, loaded.Problems = now.Strips, now.Problems
		loaded.Failed, loaded.FailedNodes = now.Failed, now.FailedNodes
	}
	times := func(n int) string {
		if n == 1 {
			return "once"
		}
		return strconv.Itoa(n) + " times"
	}
	if len(loaded.FailedNodes) == 0 && len(loaded.Failed) == 0 {
		detail = fmt.Sprintf("AUM drew %s as failed; reloaded %s, it came up", failedNames(cs, first), times(rounds))
		if len(first) > 1 {
			detail = fmt.Sprintf("AUM drew %s as failed; reloaded %s, they came up", failedNames(cs, first), times(rounds))
		}
		return true, detail
	}
	still := failedNames(cs, loaded.FailedNodes)
	if len(loaded.FailedNodes) == 0 {
		still = "a node of " + quoteList(loaded.Failed)
	}
	detail = fmt.Sprintf("AUM drew %s as failed; reloaded %s, and %s is still drawn failed", failedNames(cs, first), times(rounds), still)
	if len(trouble) > 0 {
		detail += " (" + strings.Join(dedupe(trouble), "; ") + ")"
	}
	return false, detail
}

// failedName names a failed node by the song's plugin there when the plan
// says which one it is: "pad's Perforator (insert slot 1)", "the master's
// Hall (insert slot 2)". A node the plan does not place (the brain's strip,
// a slot the plan has no plugin for) is named by AUM's strip and slot.
func failedName(cs *compiledSong, n ipad.AUMFailedNode) string {
	if cs == nil || cs.plan == nil {
		return n.String()
	}
	var owner string
	var nodes []*song.NodePlan
	if strings.EqualFold(n.Strip, "Master") {
		owner, nodes = "the master's", cs.plan.Master
	} else if tp := stripTrack(cs.plan, n.Strip); tp != nil {
		owner, nodes = tp.ID+"'s", tp.Nodes
	}
	var at *song.NodePlan
	switch n.Role {
	case "source":
		for _, np := range nodes {
			if np.Slot == 0 {
				at = np
			}
		}
	case "insert":
		// A strip's inserts follow its source in the order the plan lists
		// them: the k-th insert slot holds the k-th plugin after the source.
		var inserts []*song.NodePlan
		for _, np := range nodes {
			if np.Slot > 0 {
				inserts = append(inserts, np)
			}
		}
		slices.SortStableFunc(inserts, func(a, b *song.NodePlan) int { return a.Slot - b.Slot })
		if n.Slot >= 1 && n.Slot <= len(inserts) {
			at = inserts[n.Slot-1]
		}
	}
	if at == nil || at.Name == "" {
		return n.String()
	}
	if n.Role == "insert" {
		return fmt.Sprintf("%s %s (insert slot %d)", owner, at.Name, n.Slot)
	}
	return fmt.Sprintf("%s %s (%s)", owner, at.Name, n.Role)
}

// stripTrack finds the track whose strip AUM titles title: the strip's title
// is the track's name, or carries it.
func stripTrack(plan *song.Plan, title string) *song.TrackPlan {
	var hits []*song.TrackPlan
	for _, tp := range plan.Tracks {
		if strings.EqualFold(tp.Name, title) {
			return tp
		}
		if strings.Contains(strings.ToLower(title), strings.ToLower(tp.Name)) {
			hits = append(hits, tp)
		}
	}
	if len(hits) == 1 {
		return hits[0]
	}
	return nil
}

// failedNames names failed nodes for a message.
func failedNames(cs *compiledSong, nodes []ipad.AUMFailedNode) string {
	names := make([]string, len(nodes))
	for i, n := range nodes {
		names[i] = failedName(cs, n)
	}
	return strings.Join(names, ", ")
}

func quoteList(items []string) string {
	q := make([]string, len(items))
	for i, it := range items {
		q[i] = strconv.Quote(it)
	}
	return strings.Join(q, ", ")
}

func dedupe(items []string) []string {
	var out []string
	for _, it := range items {
		if !slices.Contains(out, it) {
			out = append(out, it)
		}
	}
	return out
}
