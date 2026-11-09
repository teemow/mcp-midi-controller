//go:build darwin

package mdns

import "context"

// dnssdPath is the mDNSResponder client every macOS ships.
const dnssdPath = "/usr/bin/dns-sd"

// Announce registers instance as a ServiceType service on port with macOS's
// mDNSResponder (through a supervised `dns-sd -R`) and keeps the registration
// alive until ctx is cancelled: if dns-sd dies — e.g. because mDNSResponder
// restarted, which drops every client registration — the service is
// re-registered with backoff. Cancelling ctx kills dns-sd, which withdraws the
// service. It returns an error only when the initial registration fails, in
// which case discovery needs the manual fallback
// (`dns-sd -R <name> _mcpmidi._tcp local <port> ...` or the iPad's
// manual-host override).
func Announce(ctx context.Context, instance string, port uint16, txt []string) error {
	return newDNSSD(dnssdPath, instance, port, txt).announce(ctx)
}
