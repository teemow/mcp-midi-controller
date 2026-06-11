package aum

// The staging revision is a monotonic change counter for the AUM staging dir,
// shared by every writer: the LAN receiver (internal/aumreceiver upload/delete)
// and the MCP tools (internal/mcpserver stageAUMFile). The GET /aum-session
// manifest reports it so the iPad app can poll cheaply — same rev, nothing
// changed, skip all sync work. It lives here (not in aumreceiver) for the same
// reason as the rest of this file's siblings: both layers write the staging
// dir, so both must bump the same counter.
//
// The counter is persisted as a ".rev" file inside the staging dir itself, so
// it survives daemon restarts (the app's "last seen rev" stays meaningful) and
// naturally travels with the dir. The hidden name keeps it out of the staged
// listing: WalkStaged skips non-session files and SafeRelPath rejects hidden
// segments, so it can never be listed, downloaded, or deleted via the API.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/teemow/mcp-midi-controller/internal/lanhttp"
)

// revFile is the counter's filename inside the staging dir.
const revFile = ".rev"

// revMu serializes read-modify-write cycles across all writers in-process.
var revMu sync.Mutex

// StagingRev returns the staging dir's current revision (0 when nothing has
// ever been staged or the counter file is missing/corrupt).
func StagingRev(dir string) int64 {
	revMu.Lock()
	defer revMu.Unlock()
	return readRev(dir)
}

// BumpStagingRev increments and persists the staging dir's revision. Call it
// after every write to the staging dir (upload, delete, MCP-tool
// author/edit/instrument/export). The counter is written atomically
// (temp-file + rename) so a crash can never leave a torn file that readRev
// would reset to 0 — a regressed counter climbing back to a client's
// last-seen rev would 304 on changed content. Persistence is otherwise
// best-effort: a failed write leaves the old counter behind, which only ever
// causes an extra (harmless) sync cycle, never a missed one — the next
// successful bump moves past it.
func BumpStagingRev(dir string) {
	revMu.Lock()
	defer revMu.Unlock()
	rev := readRev(dir) + 1
	if err := os.MkdirAll(dir, 0o755); err == nil {
		_ = lanhttp.WriteFileAtomic(filepath.Join(dir, revFile), []byte(strconv.FormatInt(rev, 10)), 0o644)
	}
}

// readRev reads the persisted counter; callers hold revMu.
func readRev(dir string) int64 {
	data, err := os.ReadFile(filepath.Join(dir, revFile))
	if err != nil {
		return 0
	}
	rev, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || rev < 0 {
		return 0
	}
	return rev
}
