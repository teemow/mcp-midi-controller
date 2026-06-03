package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teemow/mcp-midi-controller/internal/widi"
)

// registerWIDITools registers the CME WIDI dongle configuration tools. WIDI
// config is persistent device flash settings (BLE role, TX power, MIDI-thru,
// wireless groups), distinct from MIDI performance control, so it lives in its
// own read/write tools addressed by endpoint + product rather than as bound
// device controls. A dongle must be paired/connected first (pair_endpoint).
func (s *Server) registerWIDITools() {
	productKeys := strings.Join(widi.ProductKeys(), " | ")
	settingKeys := strings.Join(widi.SettingKeys(), " | ")
	target := fmt.Sprintf(`"endpoint":{"type":"string","description":"WIDI BLE address"},"product":{"type":"string","description":"one of: %s"},"devid":{"type":"integer","description":"raw devID byte (alternative to product)"},"timeout_ms":{"type":"integer"}`, productKeys)

	s.mcp.AddTool(&mcp.Tool{
		Name:        "widi_read_config",
		Description: "Read a CME WIDI dongle's persistent settings (BLE role, TX power, power-saving, latency/jitter, MIDI-thru, wireless-group peers). Address it by endpoint + product (or devid). Pair the dongle first with pair_endpoint.",
		InputSchema: json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{%s},"required":["endpoint"]}`, target)),
	}, s.handleWIDIReadConfig)

	s.mcp.AddTool(&mcp.Tool{
		Name:        "widi_write_setting",
		Description: fmt.Sprintf("Write one persistent WIDI setting and read it back to verify. setting is one of: %s. value is a label (e.g. ble_role=peripheral, midi_in_thru=on, tx_power='+5') or wire number. Settings are flash config and survive power cycles. Tip: forcing every dongle to ble_role=peripheral stops them auto-connecting to each other (only a central like an iPad will connect).", settingKeys),
		InputSchema: json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{%s,"setting":{"type":"string","description":"one of: %s"},"value":{"description":"label or wire number"}},"required":["endpoint","setting","value"]}`, target, settingKeys)),
	}, s.handleWIDIWriteSetting)

	s.mcp.AddTool(&mcp.Tool{
		Name:        "widi_set_group",
		Description: "Create/replace a WIDI wireless-MIDI group: write up to 4 peer BLE MACs into the dongle's CONNECT_ADDRESS slots (unused slots cleared), optionally setting role and latency/jitter preference. Pairing-changing, multi-register write. Note: do NOT use this to pin an iPad — iOS uses a randomized BLE address; use ble_role=peripheral instead.",
		InputSchema: json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{%s,"peers":{"type":"array","items":{"type":"string"},"description":"peer BLE MACs (AA:BB:CC:DD:EE:FF)"},"role":{"type":"string","enum":["auto","peripheral"]},"prefer":{"type":"string","enum":["latency","jitter"]}},"required":["endpoint","peers"]}`, target)),
	}, s.handleWIDISetGroup)

	s.mcp.AddTool(&mcp.Tool{
		Name:        "widi_clear_group",
		Description: "Dissolve a WIDI wireless-MIDI group by clearing all four CONNECT_ADDRESS slots (FF×6).",
		InputSchema: json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{%s},"required":["endpoint"]}`, target)),
	}, s.handleWIDIClearGroup)
}

// widiTarget is the common endpoint+product addressing shared by the tools.
type widiTarget struct {
	Endpoint  string `json:"endpoint"`
	Product   string `json:"product"`
	DevID     *int   `json:"devid"`
	TimeoutMS int    `json:"timeout_ms"`
}

// resolve validates the target and returns the endpoint, devID and timeout.
func (t widiTarget) resolve() (string, byte, time.Duration, error) {
	if t.Endpoint == "" {
		return "", 0, 0, fmt.Errorf("/endpoint: required (the WIDI BLE address)")
	}
	dev := -1
	if t.DevID != nil {
		dev = *t.DevID
	}
	devID, err := widi.ResolveDevID(t.Product, dev)
	if err != nil {
		return "", 0, 0, err
	}
	return t.Endpoint, devID, time.Duration(t.TimeoutMS) * time.Millisecond, nil
}

func (s *Server) handleWIDIReadConfig(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args widiTarget
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	endpoint, devID, timeout, err := args.resolve()
	if err != nil {
		return textResult(err.Error(), true), nil
	}
	cfg, err := s.eng.ReadWIDIConfig(ctx, endpoint, devID, timeout)
	if err != nil {
		return textResult("widi_read_config failed: "+err.Error(), true), nil
	}
	return jsonResult(cfg)
}

func (s *Server) handleWIDIWriteSetting(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		widiTarget
		Setting string `json:"setting"`
		Value   any    `json:"value"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	endpoint, devID, timeout, err := args.resolve()
	if err != nil {
		return textResult(err.Error(), true), nil
	}
	if args.Setting == "" {
		return textResult("/setting: required", true), nil
	}
	res, err := s.eng.WriteWIDISetting(ctx, endpoint, devID, args.Setting, args.Value, timeout)
	if err != nil {
		return textResult("widi_write_setting failed: "+err.Error(), true), nil
	}
	return jsonResult(res)
}

func (s *Server) handleWIDISetGroup(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		widiTarget
		Peers  []string `json:"peers"`
		Role   string   `json:"role"`
		Prefer string   `json:"prefer"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	endpoint, devID, timeout, err := args.resolve()
	if err != nil {
		return textResult(err.Error(), true), nil
	}
	cfg, err := s.eng.SetWIDIGroup(ctx, endpoint, devID, args.Peers, args.Role, args.Prefer, timeout)
	if err != nil {
		return textResult("widi_set_group failed: "+err.Error(), true), nil
	}
	return jsonResult(cfg)
}

func (s *Server) handleWIDIClearGroup(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args widiTarget
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	endpoint, devID, timeout, err := args.resolve()
	if err != nil {
		return textResult(err.Error(), true), nil
	}
	cfg, err := s.eng.ClearWIDIGroup(ctx, endpoint, devID, timeout)
	if err != nil {
		return textResult("widi_clear_group failed: "+err.Error(), true), nil
	}
	return jsonResult(cfg)
}

// jsonResult marshals v to indented JSON as a (non-error) tool result.
func jsonResult(v any) (*mcp.CallToolResult, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return textResult("encode result: "+err.Error(), true), nil
	}
	return textResult(string(b), false), nil
}
