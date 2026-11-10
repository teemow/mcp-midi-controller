//go:build darwin

package hostawake

import "strconv"

// caffeinatePath is the power-assertion tool every macOS ships.
const caffeinatePath = "/usr/bin/caffeinate"

// command is the caffeinate that holds the host awake for the process pid:
// -i keeps the system from idle sleep and -s from sleeping at all while it is
// on mains power; the display may still sleep. The assertions live as long as
// caffeinate does, and -w makes it exit with pid, so a daemon that dies
// without releasing its hold leaves none behind.
//
// There is no -u (declare the user active) to leave dark wake. Measured on a
// laptop that dozed: with these two assertions taken in dark wake the sleep
// that was due did not come and timers fired within 3 ms, in dark wake still.
// -u took the host to full wake and lit its display, which a song does not
// need.
func command(pid int) []string {
	return []string{caffeinatePath, "-i", "-s", "-w", strconv.Itoa(pid)}
}
