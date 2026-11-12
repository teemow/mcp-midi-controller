package mcpserver

// This file is about the iPad's lock screen.
//
// AUM keeps the iPad awake. Other apps need not, and the AUv3 Probe app does
// not: an iPad left with it in front locks itself after its auto-lock time,
// and with a passcode set nothing reaches the rig until the code is entered
// on the device. The rig's iPad did that a quarter of an hour after a session
// had left the probe app in front.
//
// So the daemon says when another app than AUM is in front because it brought
// it there (frontNote), brings AUM back where a call ends with the probe app
// in front for good (load_song's failed pull, see song_pull.go), and names a
// locked iPad instead of passing on what WebDriverAgent makes of it ("Unable
// to launch … because the device was not, or could not be, unlocked").
//
// The Home Screen keeps the iPad awake no more than the probe app does, and
// it is what an app leaves in front that goes away: AUM crashed on the rig
// while it replaced the open session. WebDriverAgent then says of every
// further call that the app "is not running, possibly crashed". load_song's
// load step starts AUM again and loads the session once more (see
// ipad.AUMOpenSession and aumGone); every other tool says what that error
// means and what to do (appGone), and the tools after it say that the Home
// Screen is in front until AUM is brought forward again. An app that does not
// start ("Cannot launch … application") leaves what was in front before, and
// that error is passed on as it is.
//
// The daemon never unlocks the device and never types a code: it only asks
// WebDriverAgent whether the iPad is locked.

import (
	"context"
	"fmt"
	"time"

	"github.com/teemow/mcp-midi-controller/internal/ipad"
)

const (
	// ipadLockedText names a locked iPad, ipadUnlock what to do about it.
	ipadLockedText = "the iPad is locked"
	ipadUnlock     = "unlock the iPad on the device, with its passcode if it asks for one (the daemon never unlocks it and never types a code)"

	// ipadLockedTimeout bounds the question whether the iPad is locked.
	ipadLockedTimeout = 5 * time.Second

	// springBoard is the Home Screen: what WebDriverAgent names as the app in
	// front on a locked iPad, and in windowed multitasking whatever is shown.
	springBoard = ipad.HomeScreenBundleID

	// ipadHomeScreen is what an app that went away leaves behind.
	ipadHomeScreen = "the iPad is on its Home Screen, where nothing keeps it awake: it locks itself after its auto-lock time and then needs its passcode entered on the device"
)

// ipadLocked asks WebDriverAgent whether the iPad is on its lock screen. It is
// false when it is not, and when WebDriverAgent does not say.
func (s *Server) ipadLocked(ctx context.Context) bool {
	if s.ipad == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, ipadLockedTimeout)
	defer cancel()
	locked, err := s.ipad.Locked(ctx)
	return err == nil && locked
}

// ipadFailure words what WebDriverAgent made of a call for a report: by the
// lock screen when that is what stands in the way (locked), by the app that
// went away under the call when WebDriverAgent said that, else as it is.
func (s *Server) ipadFailure(ctx context.Context, err error) (text string, locked bool) {
	if !ipad.Unreachable(err) && s.ipadLocked(ctx) {
		return fmt.Sprintf("%s (%v)", ipadLockedText, err), true
	}
	if ipad.AppWentAway(err) {
		return appGone(err), false
	}
	return err.Error(), false
}

// appGone words WebDriverAgent's word that the app went away under the call
// that drove it (ipad.AppWentAway): what that means on the iPad, and what to
// do before it locks itself. The call is not made again in an app started
// anew: what it was to act on went with the app. An app that did not start
// ("Cannot launch") is not worded here: it left what was in front before.
func appGone(err error) string {
	return fmt.Sprintf("%v. The app crashed or was closed, and what it had open went with it: in AUM that is the session, which has to be loaded again (load_song for a song). "+
		"Now %s. Bring AUM to the front before that: ipad_launch without app starts it.", err, ipadHomeScreen)
}

// frontNote says that the daemon brought another app than AUM to the front
// and left it there. It is "" when it did not, or brought AUM forward since.
func (s *Server) frontNote() string {
	if s.ipad == nil {
		return ""
	}
	app, since := s.ipad.Front()
	return frontNoteFor(app, since)
}

// frontNoteFor is the note for an app in front, "" for AUM and for no app.
// since is when the daemon brought it there, zero when it is in front by
// other means.
func frontNoteFor(app string, since time.Time) string {
	if app == "" || app == ipad.AUMBundleID {
		return ""
	}
	if app == springBoard {
		// What an app that went away left in front (ipad.Client.Front).
		return "Note: the app the daemon last drove on the iPad was not running any more (it crashed or was closed), so " + ipadHomeScreen + ". " +
			"Bring AUM to the front: ipad_launch without app starts it."
	}
	name, why := app, "Only AUM is known to keep the iPad awake: with another app left in front the iPad can lock itself"
	if ipad.IsApp("probe", app) {
		name, why = "the AUv3 Probe app ("+app+")", "It does not keep the iPad awake as AUM does: left in front, the iPad locks itself"
	}
	how := ""
	if !since.IsZero() {
		age := "less than a minute"
		if m := int(time.Since(since).Minutes()); m >= 1 {
			age = fmt.Sprintf("%d min", m)
		}
		how = fmt.Sprintf(" (the daemon brought it there %s ago)", age)
	}
	return fmt.Sprintf("Note: %s is in front on the iPad, not AUM%s. %s after its auto-lock time, and then needs its passcode entered on the device. "+
		"Bring AUM back as soon as you are done: ipad_launch without app.", name, how, why)
}

// withFrontNote adds frontNote to a tool's text.
func (s *Server) withFrontNote(text string) string {
	if note := s.frontNote(); note != "" {
		return text + "\n" + note
	}
	return text
}
