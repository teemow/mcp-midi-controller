// Package mdns announces the daemon's iPad-facing LAN receiver as a
// Bonjour/mDNS service via the host's Avahi daemon (system D-Bus), so the
// auv3-probe app and its AUv3 extensions can auto-discover the daemon
// (`ProbeKit/DaemonDiscovery.swift` browses `_mcpmidi._tcp`). Registering with
// Avahi — instead of binding port 5353 ourselves — avoids fighting the mDNS
// responder every Linux desktop already runs.
//
// The TXT record carries `version=<semver>` and `capabilities=<comma list>`,
// which the iPad surfaces in its shared daemon-status element.
package mdns

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/godbus/dbus/v5"
)

// ServiceType is the Bonjour service type the iPad browses for.
const ServiceType = "_mcpmidi._tcp"

const (
	avahiBus         = "org.freedesktop.Avahi"
	avahiServerIface = "org.freedesktop.Avahi.Server"
	avahiGroupIface  = "org.freedesktop.Avahi.EntryGroup"

	// AVAHI_IF_UNSPEC / AVAHI_PROTO_UNSPEC: publish on every interface and
	// protocol; Avahi handles per-interface visibility.
	ifUnspec    = int32(-1)
	protoUnspec = int32(-1)
)

// Announce registers instance as a ServiceType service on port with Avahi and
// keeps the registration alive until ctx is cancelled: if the Avahi daemon
// restarts (which silently drops every registered entry group), the service is
// re-registered as soon as the daemon is back on the bus. It returns an error
// only when the initial registration fails — e.g. no system bus, or no Avahi —
// in which case discovery needs the manual fallback
// (`avahi-publish-service <name> _mcpmidi._tcp <port> ...` or the iPad's
// manual-host override).
func Announce(ctx context.Context, instance string, port uint16, txt []string) error {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return fmt.Errorf("mdns: connect system bus: %w", err)
	}

	group, err := register(conn, instance, port, txt)
	if err != nil {
		_ = conn.Close()
		return err
	}
	log.Printf("mdns: announced %q (%s port %d) via avahi", instance, ServiceType, port)

	go keepAlive(ctx, conn, group, instance, port, txt)
	return nil
}

// register creates one Avahi entry group carrying the service and commits it,
// returning the group's object for the eventual Free.
func register(conn *dbus.Conn, instance string, port uint16, txt []string) (dbus.BusObject, error) {
	server := conn.Object(avahiBus, "/")
	var groupPath dbus.ObjectPath
	if err := server.Call(avahiServerIface+".EntryGroupNew", 0).Store(&groupPath); err != nil {
		return nil, fmt.Errorf("mdns: avahi EntryGroupNew: %w", err)
	}
	group := conn.Object(avahiBus, groupPath)

	if err := group.Call(avahiGroupIface+".AddService", 0,
		ifUnspec, protoUnspec, uint32(0),
		instance, ServiceType, "", "", port, txtBytes(txt),
	).Err; err != nil {
		return nil, fmt.Errorf("mdns: avahi AddService: %w", err)
	}
	if err := group.Call(avahiGroupIface+".Commit", 0).Err; err != nil {
		return nil, fmt.Errorf("mdns: avahi Commit: %w", err)
	}
	return group, nil
}

// keepAlive re-registers the service whenever Avahi reacquires its bus name (a
// daemon restart frees all entry groups server-side), and frees the group on
// ctx cancellation. Losing this would silently strand the iPad at "waiting for
// mcp-midi-controller on the LAN" even though the receiver is healthy.
func keepAlive(ctx context.Context, conn *dbus.Conn, group dbus.BusObject, instance string, port uint16, txt []string) {
	defer func() { _ = conn.Close() }()

	if err := conn.AddMatchSignal(
		dbus.WithMatchSender("org.freedesktop.DBus"),
		dbus.WithMatchInterface("org.freedesktop.DBus"),
		dbus.WithMatchMember("NameOwnerChanged"),
		dbus.WithMatchArg(0, avahiBus),
	); err != nil {
		log.Printf("mdns: watch avahi restarts: %v (announcement won't survive an avahi restart)", err)
		<-ctx.Done()
		free(group)
		return
	}
	signals := make(chan *dbus.Signal, 8)
	conn.Signal(signals)

	for {
		select {
		case <-ctx.Done():
			free(group)
			return
		case sig, ok := <-signals:
			if !ok {
				return
			}
			// NameOwnerChanged(name, oldOwner, newOwner): a non-empty new
			// owner means Avahi is (back) up with an empty entry-group set.
			if len(sig.Body) != 3 {
				continue
			}
			newOwner, _ := sig.Body[2].(string)
			if newOwner == "" {
				continue
			}
			g, err := register(conn, instance, port, txt)
			if err != nil {
				// Avahi may own its bus name before it can serve requests;
				// give it a moment and retry once before giving up until the
				// next restart.
				select {
				case <-ctx.Done():
					free(group)
					return
				case <-time.After(2 * time.Second):
				}
				if g, err = register(conn, instance, port, txt); err != nil {
					log.Printf("mdns: re-announce after avahi restart: %v", err)
					continue
				}
			}
			group = g
			log.Printf("mdns: re-announced %q after avahi restart", instance)
		}
	}
}

// free releases the entry group so the service disappears from the network on
// shutdown instead of lingering until the TTL expires.
func free(group dbus.BusObject) {
	if err := group.Call(avahiGroupIface+".Free", 0).Err; err != nil {
		log.Printf("mdns: free entry group: %v", err)
	}
}

// txtBytes encodes TXT records the way Avahi's AddService expects them (one
// byte slice per "key=value" entry).
func txtBytes(txt []string) [][]byte {
	out := make([][]byte, 0, len(txt))
	for _, t := range txt {
		out = append(out, []byte(t))
	}
	return out
}
