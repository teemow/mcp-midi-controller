package mcpserver

// A craft tool that plays (apply_technique with verify, profile_plugin) runs
// in the background like mix_song: it holds the host awake and counts as
// busy while it runs, one at a time, and get_craft_job reports its progress
// and then its report. stop_song ends it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// craftWaitMost bounds get_craft_job's wait.
const craftWaitMost = 55 * time.Second

// craftJob is one running or finished craft job.
type craftJob struct {
	mu      sync.Mutex
	tool    string
	song    string
	phase   string
	started time.Time
	text    string
	report  any
	err     string
	done    chan struct{}
	cancel  context.CancelFunc
}

func (j *craftJob) setPhase(format string, a ...any) {
	j.mu.Lock()
	j.phase = fmt.Sprintf(format, a...)
	j.mu.Unlock()
}

func (j *craftJob) finished() bool {
	select {
	case <-j.done:
		return true
	default:
		return false
	}
}

// craftRun is a craft job's work: its report as text and as data.
type craftRun func(ctx context.Context, job *craftJob) (text string, report any, err error)

// startCraftJob runs a craft job in the background, refusing while another
// one, a mix measurement or a stem recording runs.
func (s *Server) startCraftJob(tool, songID string, run craftRun) (*craftJob, *mcp.CallToolResult) {
	if busy := s.stemsBusy(tool); busy != nil {
		return nil, busy
	}
	s.mixMu.Lock()
	mixing := s.mix != nil && !s.mix.finished()
	s.mixMu.Unlock()
	if mixing {
		return nil, textResult(tool+": a mix measurement is running (get_song_mix follows it, stop_song ends it)", true)
	}
	s.craftMu.Lock()
	defer s.craftMu.Unlock()
	if j := s.craftJob; j != nil && !j.finished() {
		j.mu.Lock()
		phase := j.phase
		j.mu.Unlock()
		return nil, textResult(fmt.Sprintf("%s: %s is running (%s); get_craft_job follows it, stop_song ends it", tool, j.tool, phase), true)
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &craftJob{tool: tool, song: songID, phase: "starting", started: time.Now(), done: make(chan struct{}), cancel: cancel}
	s.craftJob = job
	go func() {
		defer close(job.done)
		defer cancel()
		release := s.hold(fmt.Sprintf("%s %q", tool, songID))
		defer release()
		text, rep, err := run(ctx, job)
		job.mu.Lock()
		defer job.mu.Unlock()
		job.text, job.report = text, rep
		switch {
		case errors.Is(err, context.Canceled):
			job.err = "stopped"
		case err != nil:
			job.err = err.Error()
		}
		job.phase = "done"
	}()
	return job, nil
}

func (s *Server) registerCraftJobTool() {
	s.mcp.AddTool(&mcp.Tool{
		Name:        "get_craft_job",
		Description: "The craft tool that plays (apply_technique with verify, profile_plugin), running or run last: how far it is, or its report once it is done. wait_ms waits for it to finish (up to 55 s per call). stop_song ends it.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"wait_ms": {"type": "integer", "minimum": 0, "maximum": 55000, "description": "Wait this long for the job to finish before answering."}
			}
		}`),
	}, s.handleGetCraftJob)
}

func (s *Server) handleGetCraftJob(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		WaitMS int `json:"wait_ms"`
	}
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return textResult("invalid arguments: "+err.Error(), true), nil
		}
	}
	s.craftMu.Lock()
	job := s.craftJob
	s.craftMu.Unlock()
	if job == nil {
		return textResult("no craft job has run: apply_technique with verify or profile_plugin starts one", true), nil
	}
	if wait := min(time.Duration(args.WaitMS)*time.Millisecond, craftWaitMost); wait > 0 {
		select {
		case <-job.done:
		case <-time.After(wait):
		case <-ctx.Done():
		}
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	out := map[string]any{"tool": job.tool, "song": job.song, "phase": job.phase, "seconds": time.Since(job.started).Round(time.Second).Seconds(), "done": job.finished()}
	if !job.finished() {
		return structResult(fmt.Sprintf("%s on %q: %s (%s so far)", job.tool, job.song, job.phase, clock(time.Since(job.started))), out), nil
	}
	if job.report != nil {
		out["report"] = job.report
	}
	text := job.text
	if job.err != "" {
		out["error"] = job.err
		text = fmt.Sprintf("%s on %q ended: %s\n%s", job.tool, job.song, job.err, text)
	}
	res := structResult(text, out)
	res.IsError = job.err != ""
	return res, nil
}

// callText is the text of a tool result a craft job called for itself; a nil
// result has none.
func callText(res *mcp.CallToolResult) string {
	if res == nil {
		return ""
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}
