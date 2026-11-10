//go:build !darwin

package hostawake

// command is empty off macOS: no other host is known to doze under the
// running daemon, so none is held awake.
func command(int) []string { return nil }
