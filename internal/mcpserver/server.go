// Package mcpserver is the thin MCP layer on top of the engine. It exposes the
// global tools and generates one control_<logical> tool per bound device, using
// the official github.com/modelcontextprotocol/go-sdk over streamable-HTTP.
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teemow/mcp-midi-controller/internal/engine"
)

// Version is reported to MCP clients.
const Version = "0.0.1-scaffold"

// Server wraps the engine and an mcp.Server.
type Server struct {
	eng *engine.Engine
	mcp *mcp.Server
}

// New builds the MCP server, registers global tools, and generates a tool for
// each currently bound device.
func New(eng *engine.Engine) *Server {
	s := &Server{
		eng: eng,
		mcp: mcp.NewServer(&mcp.Implementation{Name: "mcp-midi-controller", Version: Version}, nil),
	}
	s.registerGlobalTools()
	for _, b := range eng.Bindings() {
		s.AddDeviceTool(b)
	}
	return s
}

// Handler returns the streamable-HTTP handler to mount on a loopback listener.
func (s *Server) Handler() http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.mcp }, nil)
}

// AddDeviceTool generates and registers control_<logical> for a binding. Adding
// the tool also emits notifications/tools/list_changed to connected clients.
func (s *Server) AddDeviceTool(b engine.Binding) {
	def, ok := s.eng.Registry().Get(b.DeviceID)
	if !ok {
		return
	}
	s.mcp.AddTool(&mcp.Tool{
		Name:        "control_" + b.Logical,
		Description: fmt.Sprintf("Set one or more controls on %q (%s). Use describe_device for ranges/enums.", b.Logical, def.Name),
		InputSchema: controlToolSchema(def.ControlNames()),
	}, s.handleControl(b.Logical))
}

// RemoveDeviceTool removes control_<logical> (emits list_changed).
func (s *Server) RemoveDeviceTool(logical string) {
	s.mcp.RemoveTools("control_" + logical)
}

// handleControl validates control names + values against the device definition
// in-handler and applies them via the engine. Failures are returned as
// CallToolResult{IsError:true} with an RFC-6901 JSON-pointer path (SEP-1303) so
// the model can self-correct, rather than as protocol errors.
func (s *Server) handleControl(logical string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			Settings []struct {
				Control string `json:"control"`
				Value   any    `json:"value"`
			} `json:"settings"`
		}
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return textResult("invalid arguments: "+err.Error(), true), nil
		}
		var applied int
		for i, set := range args.Settings {
			if err := s.eng.SetControl(ctx, logical, set.Control, set.Value); err != nil {
				// TODO: distinguish validation errors (→ precise /settings/N/value
				// or /settings/N/control pointer) from transport errors.
				return textResult(fmt.Sprintf("/settings/%d: %v", i, err), true), nil
			}
			applied++
		}
		return textResult(fmt.Sprintf("applied %d setting(s) to %s", applied, logical), false), nil
	}
}

func textResult(msg string, isErr bool) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
		IsError: isErr,
	}
}
