package mcpserver

// This file is what the daemon reads from the names of a session's taps.
// Every ProbeAudioTap of a session the daemon authored dials in under the
// session's id and its strip (song.TapName). The taps that are connected
// therefore say which session AUM has open and which stream is which
// strip's, without a look at AUM's screen.
//
// A tap without such a name says nothing: one of a session authored before
// taps had names, or of a build of the plugin that does not send its name.
// The song tools then work as they did before, with the most recently
// connected tap and with what AUM's screen shows.

import (
	"context"
	"time"

	"github.com/teemow/mcp-midi-controller/internal/audiotap"
	"github.com/teemow/mcp-midi-controller/internal/song"
)

// songTapNameGrace is how long load_song waits for the loaded session's
// master tap to dial in under its name once the session's brain and some tap
// are there. A plugin that sends no name never does; a variable for the
// tests.
var songTapNameGrace = 3 * time.Second

// tapSession is the session AUM has open as its taps tell it: the session of
// the most recently connected master tap that names one, "" when there is
// none. Only the master's tap is asked: every session has exactly one, and a
// name that merely holds a slash (a tap named by hand) is not taken for one.
func (s *Server) tapSession() string {
	if s.audio == nil {
		return ""
	}
	for _, st := range s.audio.Connected() {
		if session, strip, ok := song.ParseTapName(st.Name()); ok && strip == song.MasterTap {
			return session
		}
	}
	return ""
}

// sessionTap is the connected tap of a session's strip (song.MasterTap or a
// track's id).
func (s *Server) sessionTap(session, strip string) (*audiotap.Store, bool) {
	if s.audio == nil || session == "" {
		return nil, false
	}
	name := song.TapName(session, strip)
	st, ok := s.audio.Get(name)
	if !ok || st.Name() != name || !st.Connected() {
		return nil, false
	}
	return st, true
}

// songTap is the tap a session's song is heard at: the session's master tap
// when it dialled in under its name (named), else the most recently connected
// tap that is not one of the session's tracks'. nil when there is none.
func (s *Server) songTap(session string) (tap *audiotap.Store, named bool) {
	if st, ok := s.sessionTap(session, song.MasterTap); ok {
		return st, true
	}
	if s.audio == nil {
		return nil, false
	}
	for _, st := range s.audio.Connected() {
		if of, _, ok := song.ParseTapName(st.Name()); ok && session != "" && of == session {
			continue // a track's tap carries that track alone
		}
		return st, false
	}
	return nil, false
}

// trackTaps are the connected taps of a session's tracks, by track id, and
// the tracks that have none.
func (s *Server) trackTaps(cs *compiledSong, session string) (taps map[string]*audiotap.Store, missing []string) {
	taps = map[string]*audiotap.Store{}
	for _, tp := range cs.plan.Tracks {
		if st, ok := s.sessionTap(session, tp.ID); ok {
			taps[tp.ID] = st
		} else {
			missing = append(missing, tp.ID)
		}
	}
	return taps, missing
}

// streaming reports whether a tap delivers audio right now: it is connected
// and its sample count moves. A stream that hiccups is given a few tenths of
// a second.
func (s *Server) streaming(ctx context.Context, st *audiotap.Store) bool {
	if st == nil || !st.Connected() {
		return false
	}
	before := st.MarkEpoch()
	for i := 0; i < 8; i++ {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(80 * time.Millisecond):
		}
		if st.MarkEpoch() > before {
			return true
		}
	}
	return false
}
