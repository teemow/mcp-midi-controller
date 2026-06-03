package aum

// This file centralizes the on-disk file conventions shared by the LAN receiver
// (internal/aumreceiver) and the MCP tools (internal/mcpserver): how a staged
// filename is classified (.aumproj vs .aum_midimap), how its id is recovered,
// and how a file is summarized for a listing. Keeping these in the aum package
// means the two layers cannot drift on what "a session file" is.

import "strings"

// File kinds returned by FileKind.
const (
	KindSession = "session" // a .aumproj AUM session
	KindMidiMap = "midimap" // a standalone .aum_midimap collection
)

// FileKind classifies a staged filename, or "" if it is neither a session nor a
// standalone midimap.
func FileKind(name string) string {
	switch {
	case strings.HasSuffix(name, ".aumproj"):
		return KindSession
	case strings.HasSuffix(name, ".aum_midimap"):
		return KindMidiMap
	default:
		return ""
	}
}

// StripExt strips the staging extension(s) from a filename to recover its id.
func StripExt(name string) string {
	return strings.TrimSuffix(strings.TrimSuffix(name, ".aumproj"), ".aum_midimap")
}

// FileSummary is the parsed, listing-level view of a staged session/midimap.
// Err is set (and the parsed fields left zero) when the file does not decode,
// so a caller can surface the failure rather than dropping the file silently.
type FileSummary struct {
	Kind     string
	Title    string
	Version  int
	Channels int
	Mappings int
	Err      error
}

// SummarizeFile classifies and decodes a staged file into a FileSummary. A file
// whose name is neither kind yields a zero summary with Kind "".
func SummarizeFile(path string) FileSummary {
	sum := FileSummary{Kind: FileKind(path)}
	switch sum.Kind {
	case KindSession:
		sess, err := OpenFile(path)
		if err != nil {
			sum.Err = err
			return sum
		}
		sm := sess.Map()
		sum.Title = sess.Title()
		sum.Version = sm.Version
		sum.Channels = len(sm.Channels)
		sum.Mappings = len(sm.Mappings)
	case KindMidiMap:
		mm, err := OpenMidiMapFile(path)
		if err != nil {
			sum.Err = err
			return sum
		}
		sum.Title = mm.Name
		sum.Mappings = len(mm.Mappings)
	}
	return sum
}
