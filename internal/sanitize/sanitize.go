// Package sanitize normalizes free-form text (plugin names, session titles,
// agent- or LAN-supplied ids) into a single filesystem- and path-traversal-safe
// identifier shape, so every layer that derives a staging filename or logical
// name uses the same rule.
package sanitize

import "strings"

// ID lowercases s and collapses every run of characters outside [a-z0-9] to a
// single underscore, trimming leading and trailing underscores. The result is
// safe to use as a bare filename component: it contains no path separators,
// no "..", and no spaces. An input with no usable characters yields "".
func ID(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevUnderscore := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevUnderscore = false
		default:
			if !prevUnderscore && b.Len() > 0 {
				b.WriteByte('_')
				prevUnderscore = true
			}
		}
	}
	return strings.Trim(b.String(), "_")
}
