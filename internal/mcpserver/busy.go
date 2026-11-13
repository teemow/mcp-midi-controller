package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// What the daemon is in the middle of, for GET /busy: a restart (the macOS
// self-update, scripts/deploy-mac.sh) asks it first and waits while it is
// busy, so a merge does not cut a song, a recording or a measurement short.
//
// Two kinds of work count. A job that runs past its tool call (a song playing,
// a recording at a tap, a stem recording, a mix measurement) holds the host
// awake for as long as it runs, so it takes that hold through hold, which
// names it too. And a tool call counts until it returns: load_song pulling a
// session onto the iPad is cut by a restart as surely as a song is.

// hold marks the daemon busy with what and keeps its host awake, until the
// release it returns is called.
func (s *Server) hold(what string) (release func()) {
	done := s.busy.Begin(what)
	awake := s.holdAwake()
	return func() {
		awake()
		done()
	}
}

// busyWhileCalled is the receiving middleware that marks the daemon busy for
// as long as a tool call runs.
func (s *Server) busyWhileCalled(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if method != "tools/call" {
			return next(ctx, method, req)
		}
		what := "a tool call"
		if r, ok := req.(*mcp.CallToolRequest); ok && r.Params != nil {
			what = "tool call " + r.Params.Name
		}
		defer s.busy.Begin(what)()
		return next(ctx, method, req)
	}
}
