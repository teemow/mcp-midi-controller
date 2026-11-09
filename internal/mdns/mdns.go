// Package mdns announces the daemon's iPad-facing LAN receiver as a
// Bonjour/mDNS service, so the auv3-probe app and its AUv3 extensions can
// auto-discover the daemon (`ProbeKit/DaemonDiscovery.swift` browses
// `_mcpmidi._tcp`).
//
// The service is registered with the mDNS responder the host already runs
// instead of binding port 5353 ourselves:
//
//   - macOS: mDNSResponder, through a supervised `/usr/bin/dns-sd -R`
//     subprocess (announce_darwin.go, dnssd.go).
//   - everywhere else (Linux): the Avahi daemon over the system D-Bus
//     (avahi.go).
//
// Announce(ctx, instance, port, txt) has the same contract on every platform:
// it fails only when the initial registration fails, and otherwise keeps the
// service registered (re-registering after a responder restart) until ctx is
// cancelled, when the service is withdrawn.
//
// The TXT record carries `version=<semver>` and `capabilities=<comma list>`,
// which the iPad surfaces in its shared daemon-status element.
package mdns

// ServiceType is the Bonjour service type the iPad browses for.
const ServiceType = "_mcpmidi._tcp"
