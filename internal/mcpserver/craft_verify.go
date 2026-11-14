package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teemow/mcp-midi-controller/internal/craft"
)

func (s *Server) startVerify(_ context.Context, _ applyArgs, _ *craft.Library, _ craft.Rig, _ *craft.Technique) (*mcp.CallToolResult, error) {
	return textResult("verify: not yet", true), nil
}
