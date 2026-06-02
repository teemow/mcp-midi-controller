// Command blespike is a THROWAWAY hardware spike (plan Phase A). It proves the
// full BLE-MIDI path end-to-end against a real device (the Boss MD-200, reached
// over a CME WIDI-class BLE-MIDI dongle) before any of it is productionized into
// internal/transport/blemidi. It deliberately talks to BlueZ over the system
// D-Bus by hand (godbus, no helper packages) so we learn exactly which D-Bus
// calls, properties and signals the real transport will need.
//
// What it does, in order:
//  1. system bus -> org.bluez Adapter1 on -adapter (default hci0), power it on
//  2. register a NoInputNoOutput Agent1 so most BLE-MIDI dongles "just work"
//  3. SetDiscoveryFilter on the BLE-MIDI service UUID + StartDiscovery
//  4. find a Device1 matching -addr / -name (or the first advertising the
//     BLE-MIDI service), Pair (unless already bonded), then Connect
//  5. wait for ServicesResolved, resolve the MIDI I/O GattCharacteristic1
//  6. StartNotify + subscribe to PropertiesChanged -> decode inbound BLE-MIDI
//  7. drive the MD-200: flip On/Off (CC 28: 127 then 0) and sweep Rate (CC 17)
//
// This binary is removed once internal/transport/blemidi lands (plan Phase B).
//
// Run it (BLE is Linux-only by design):
//
//	go run ./cmd/blespike -name WIDI            # match the dongle by name
//	go run ./cmd/blespike -addr AA:BB:CC:DD:EE:FF -channel 1
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
)

// BLE-MIDI GATT identifiers (from the BLE-MIDI specification). BlueZ reports
// UUIDs lowercased, so we compare lowercased throughout.
const (
	serviceUUID = "03b80e5a-ede8-4b33-a751-6ce34ec4c700"
	ioCharUUID  = "7772e5db-3868-4112-a1a9-f2669d106bf3"
)

// BlueZ D-Bus names / interfaces.
const (
	bluezBus          = "org.bluez"
	adapterIface      = "org.bluez.Adapter1"
	deviceIface       = "org.bluez.Device1"
	gattCharIface     = "org.bluez.GattCharacteristic1"
	agentManagerIface = "org.bluez.AgentManager1"
	agentIface        = "org.bluez.Agent1"
	propsIface        = "org.freedesktop.DBus.Properties"
	objMgrIface       = "org.freedesktop.DBus.ObjectManager"
	agentPath         = dbus.ObjectPath("/midi/blespike/agent")
)

func main() {
	log.SetFlags(log.Ltime)

	adapterName := flag.String("adapter", "hci0", "BlueZ adapter to use")
	addr := flag.String("addr", "", "exact BLE address to target (AA:BB:CC:DD:EE:FF); overrides -name")
	name := flag.String("name", "", "case-insensitive substring of the device name/alias to target")
	channel := flag.Int("channel", 1, "MIDI channel 1-16 the MD-200 receives on (RCH)")
	discoverTimeout := flag.Duration("discover-timeout", 30*time.Second, "how long to scan for a matching device")
	listenAfter := flag.Duration("listen", 8*time.Second, "how long to keep listening for inbound MIDI after the drive sequence")
	skipDrive := flag.Bool("no-drive", false, "discover/pair/connect/notify only; do not send any MIDI")
	capability := flag.String("capability", "KeyboardDisplay", "Agent1 IO capability: NoInputNoOutput | KeyboardDisplay | DisplayYesNo | KeyboardOnly | DisplayOnly")
	repair := flag.Bool("repair", false, "remove any existing bond and pair from scratch")
	flag.Parse()

	if *channel < 1 || *channel > 16 {
		log.Fatalf("-channel must be 1-16, got %d", *channel)
	}
	midiChannel := byte(*channel - 1)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, runOpts{
		adapter:         *adapterName,
		addr:            strings.ToUpper(*addr),
		name:            *name,
		midiChannel:     midiChannel,
		discoverTimeout: *discoverTimeout,
		listenAfter:     *listenAfter,
		skipDrive:       *skipDrive,
		capability:      *capability,
		repair:          *repair,
	}); err != nil {
		log.Fatalf("spike failed: %v", err)
	}
	log.Printf("done")
}

type runOpts struct {
	adapter         string
	addr            string
	name            string
	midiChannel     byte
	discoverTimeout time.Duration
	listenAfter     time.Duration
	skipDrive       bool
	capability      string
	repair          bool
}

func run(ctx context.Context, opts runOpts) error {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return fmt.Errorf("connect system bus: %w", err)
	}
	defer conn.Close()

	adapterPath := dbus.ObjectPath("/org/bluez/" + opts.adapter)
	adapter := conn.Object(bluezBus, adapterPath)
	if err := powerOn(adapter); err != nil {
		return err
	}
	log.Printf("adapter %s powered on", opts.adapter)

	cancelAgent, err := registerAgent(conn, opts.capability)
	if err != nil {
		return err
	}
	defer cancelAgent()
	log.Printf("registered %s agent at %s", opts.capability, agentPath)

	devicePath, err := discover(ctx, conn, adapter, opts)
	if err != nil {
		return err
	}
	dev := conn.Object(bluezBus, devicePath)
	log.Printf("target device: %s", devicePath)

	if opts.repair {
		log.Printf("-repair: removing existing bond and re-discovering...")
		if call := adapter.Call(adapterIface+".RemoveDevice", 0, devicePath); call.Err != nil {
			log.Printf("warning: remove device: %v", call.Err)
		}
		devicePath, err = discover(ctx, conn, adapter, opts)
		if err != nil {
			return err
		}
		dev = conn.Object(bluezBus, devicePath)
		log.Printf("re-discovered after repair: %s", devicePath)
	}

	if err := pairAndConnect(ctx, conn, dev); err != nil {
		return err
	}

	charPath, err := resolveIOChar(ctx, conn, devicePath)
	if err != nil {
		return err
	}
	log.Printf("MIDI I/O characteristic: %s", charPath)

	if err := startNotify(conn, charPath); err != nil {
		return err
	}
	log.Printf("notifications enabled; decoding inbound BLE-MIDI")

	// Subscribe before we drive, so we catch CC-OUT echoes from the pedal.
	sigCh := make(chan *dbus.Signal, 64)
	conn.Signal(sigCh)
	if err := conn.AddMatchSignal(
		dbus.WithMatchObjectPath(charPath),
		dbus.WithMatchInterface(propsIface),
		dbus.WithMatchMember("PropertiesChanged"),
	); err != nil {
		return fmt.Errorf("watch PropertiesChanged: %w", err)
	}
	go logInbound(ctx, sigCh, charPath)

	char := conn.Object(bluezBus, charPath)
	if opts.skipDrive {
		log.Printf("-no-drive set: listening only for %s", opts.listenAfter)
	} else {
		// BLE-MIDI is write-without-response; for such characteristics BlueZ's
		// WriteValue is frequently rejected ("Not Authorized"), so we acquire a
		// write file descriptor and push raw packets through it (the low-latency
		// path the real transport will use too).
		w, mtu, err := acquireWrite(ctx, char)
		if err != nil {
			return err
		}
		defer w.Close()
		log.Printf("acquired write fd (mtu=%d)", mtu)
		if err := drive(ctx, w, opts.midiChannel); err != nil {
			return err
		}
		log.Printf("drive sequence sent; listening %s for inbound/echo", opts.listenAfter)
	}

	select {
	case <-ctx.Done():
	case <-time.After(opts.listenAfter):
	}
	return nil
}

// powerOn ensures the adapter is powered (Adapter1.Powered = true).
func powerOn(adapter dbus.BusObject) error {
	if err := setProp(adapter, adapterIface, "Powered", dbus.MakeVariant(true)); err != nil {
		return fmt.Errorf("power on adapter: %w", err)
	}
	return nil
}

// discover filters on the BLE-MIDI service UUID, starts discovery, and polls the
// object manager until a Device1 matching opts (addr/name, else the BLE-MIDI
// service) appears. Discovery is stopped before returning.
func discover(ctx context.Context, conn *dbus.Conn, adapter dbus.BusObject, opts runOpts) (dbus.ObjectPath, error) {
	// Only constrain by the BLE-MIDI service UUID when we have no explicit
	// target. Many BLE-MIDI dongles (WIDI-class) do not advertise the service
	// UUID in their advertisement packet, so a UUID filter would hide them; if
	// the user named an address/name we scan all LE devices instead and match
	// on that.
	filter := map[string]dbus.Variant{
		"Transport": dbus.MakeVariant("le"),
	}
	if opts.addr == "" && opts.name == "" {
		filter["UUIDs"] = dbus.MakeVariant([]string{serviceUUID})
	}
	if call := adapter.Call(adapterIface+".SetDiscoveryFilter", 0, filter); call.Err != nil {
		return "", fmt.Errorf("set discovery filter: %w", call.Err)
	}
	if call := adapter.Call(adapterIface+".StartDiscovery", 0); call.Err != nil {
		return "", fmt.Errorf("start discovery: %w", call.Err)
	}
	defer adapter.Call(adapterIface+".StopDiscovery", 0)

	if opts.addr != "" {
		log.Printf("scanning for address %s (timeout %s)", opts.addr, opts.discoverTimeout)
	} else if opts.name != "" {
		log.Printf("scanning for name ~%q (timeout %s)", opts.name, opts.discoverTimeout)
	} else {
		log.Printf("scanning for any BLE-MIDI device (timeout %s)", opts.discoverTimeout)
	}

	deadline := time.After(opts.discoverTimeout)
	ticker := time.NewTicker(750 * time.Millisecond)
	defer ticker.Stop()
	seen := map[dbus.ObjectPath]bool{}

	for {
		objects, err := managedObjects(conn)
		if err != nil {
			return "", err
		}
		for path, ifaces := range objects {
			props, ok := ifaces[deviceIface]
			if !ok {
				continue
			}
			if !seen[path] {
				seen[path] = true
				log.Printf("  found %s  addr=%s name=%q uuids=%d",
					path, strProp(props, "Address"), deviceLabel(props), len(uuidProp(props)))
			}
			if deviceMatches(props, opts) {
				return path, nil
			}
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline:
			return "", fmt.Errorf("no matching BLE-MIDI device found within %s", opts.discoverTimeout)
		case <-ticker.C:
		}
	}
}

// deviceMatches decides whether a Device1's properties satisfy the target. With
// -addr or -name we honor the explicit filter; otherwise any device advertising
// the BLE-MIDI service qualifies.
func deviceMatches(props map[string]dbus.Variant, opts runOpts) bool {
	switch {
	case opts.addr != "":
		return strings.EqualFold(strProp(props, "Address"), opts.addr)
	case opts.name != "":
		return strings.Contains(strings.ToLower(deviceLabel(props)), strings.ToLower(opts.name))
	default:
		for _, u := range uuidProp(props) {
			if strings.EqualFold(u, serviceUUID) {
				return true
			}
		}
		return false
	}
}

// pairAndConnect bonds (unless already paired) then opens an encrypted GATT
// connection. The BLE-MIDI characteristic requires an encrypted link, so:
//   - mark the device Trusted (so BlueZ elevates security automatically), and
//   - if a stale (likely unencrypted) connection already exists, drop it and
//     reconnect, so the link that carries our writes is encrypted by the bond.
// Skipping this yields "Operation Not Authorized" on WriteValue.
func pairAndConnect(ctx context.Context, conn *dbus.Conn, dev dbus.BusObject) error {
	wasConnected, _ := boolProp(dev, deviceIface, "Connected")

	paired, _ := boolProp(dev, deviceIface, "Paired")
	if paired {
		log.Printf("already paired")
	} else {
		log.Printf("pairing...")
		if call := dev.CallWithContext(ctx, deviceIface+".Pair", 0); call.Err != nil {
			// org.bluez.Error.AlreadyExists means a bond raced in; treat as paired.
			if !strings.Contains(call.Err.Error(), "AlreadyExists") {
				return fmt.Errorf("pair: %w", call.Err)
			}
		}
		log.Printf("paired")
	}

	if err := setProp(dev, deviceIface, "Trusted", dbus.MakeVariant(true)); err != nil {
		log.Printf("warning: set Trusted: %v", err)
	}

	// Drop a pre-existing connection so the reconnect runs over the bond and is
	// encrypted (the BLE-MIDI char rejects writes on an unencrypted link).
	if wasConnected {
		log.Printf("dropping stale connection to force an encrypted reconnect...")
		if call := dev.CallWithContext(ctx, deviceIface+".Disconnect", 0); call.Err != nil {
			log.Printf("warning: disconnect: %v", call.Err)
		}
		sleep(ctx, 1500*time.Millisecond)
	}

	log.Printf("connecting...")
	if call := dev.CallWithContext(ctx, deviceIface+".Connect", 0); call.Err != nil {
		return fmt.Errorf("connect: %w", call.Err)
	}
	log.Printf("connected (encrypted via bond)")
	return nil
}

// resolveIOChar waits for GATT services to resolve, then finds the BLE-MIDI I/O
// characteristic object under the target device.
func resolveIOChar(ctx context.Context, conn *dbus.Conn, devicePath dbus.ObjectPath) (dbus.ObjectPath, error) {
	dev := conn.Object(bluezBus, devicePath)
	deadline := time.After(20 * time.Second)
	for {
		resolved, _ := boolProp(dev, deviceIface, "ServicesResolved")
		if resolved {
			break
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline:
			return "", fmt.Errorf("services did not resolve in time")
		case <-time.After(300 * time.Millisecond):
		}
	}

	objects, err := managedObjects(conn)
	if err != nil {
		return "", err
	}
	prefix := string(devicePath) + "/"
	for path, ifaces := range objects {
		if !strings.HasPrefix(string(path), prefix) {
			continue
		}
		props, ok := ifaces[gattCharIface]
		if !ok {
			continue
		}
		if strings.EqualFold(strProp(props, "UUID"), ioCharUUID) {
			return path, nil
		}
	}
	return "", fmt.Errorf("BLE-MIDI I/O characteristic %s not found on %s", ioCharUUID, devicePath)
}

// startNotify enables notifications on the characteristic. BlueZ then delivers
// inbound payloads via the Value property in PropertiesChanged signals.
func startNotify(conn *dbus.Conn, charPath dbus.ObjectPath) error {
	char := conn.Object(bluezBus, charPath)
	if call := char.Call(gattCharIface+".StartNotify", 0); call.Err != nil {
		return fmt.Errorf("start notify: %w", call.Err)
	}
	return nil
}

// acquireWrite obtains a write file descriptor for a write-without-response
// characteristic via GattCharacteristic1.AcquireWrite, returning an *os.File to
// push raw BLE-MIDI packets through and the negotiated MTU.
func acquireWrite(ctx context.Context, char dbus.BusObject) (*os.File, uint16, error) {
	var fd dbus.UnixFD
	var mtu uint16
	call := char.CallWithContext(ctx, gattCharIface+".AcquireWrite", 0, map[string]dbus.Variant{})
	if call.Err != nil {
		return nil, 0, fmt.Errorf("acquire write: %w", call.Err)
	}
	if err := call.Store(&fd, &mtu); err != nil {
		return nil, 0, fmt.Errorf("decode acquire-write reply: %w", err)
	}
	return os.NewFile(uintptr(fd), "ble-midi-write"), mtu, nil
}

// drive runs the MD-200 proof sequence: flip the effect On (CC 28 = 127) then
// Off (= 0), then sweep the Rate knob (CC 17) across its range.
func drive(ctx context.Context, w *os.File, ch byte) error {
	log.Printf("CC28 (On/Off) -> 127 (on)")
	if err := writeCC(w, ch, 28, 127); err != nil {
		return err
	}
	sleep(ctx, 1200*time.Millisecond)

	log.Printf("CC28 (On/Off) -> 0 (off)")
	if err := writeCC(w, ch, 28, 0); err != nil {
		return err
	}
	sleep(ctx, 1200*time.Millisecond)

	log.Printf("CC17 (Rate) sweep 0..127")
	for v := 0; v <= 127; v += 16 {
		if err := writeCC(w, ch, 17, byte(v)); err != nil {
			return err
		}
		sleep(ctx, 120*time.Millisecond)
	}
	return writeCC(w, ch, 17, 127)
}

// writeCC frames a Control Change as a BLE-MIDI packet and writes it to the
// acquired write fd (write-without-response).
func writeCC(w *os.File, ch, cc, value byte) error {
	status := byte(0xB0) | (ch & 0x0F)
	packet := frameBLEMIDI([]byte{status, cc & 0x7F, value & 0x7F})
	if _, err := w.Write(packet); err != nil {
		return fmt.Errorf("write CC%d=%d: %w", cc, value, err)
	}
	return nil
}

// frameBLEMIDI wraps one MIDI message in the minimal BLE-MIDI packet: a header
// byte carrying the high 6 timestamp bits, a timestamp byte carrying the low 7,
// then the MIDI bytes. Timestamp is a 13-bit millisecond counter.
func frameBLEMIDI(midi []byte) []byte {
	ts := uint16(time.Now().UnixMilli()) & 0x1FFF
	header := byte(0x80) | byte(ts>>7)
	tsLow := byte(0x80) | byte(ts&0x7F)
	out := make([]byte, 0, len(midi)+2)
	out = append(out, header, tsLow)
	out = append(out, midi...)
	return out
}

// logInbound consumes PropertiesChanged signals for the characteristic and
// prints decoded inbound MIDI messages (this is the feedback channel the real
// transport will turn into transport.Event).
func logInbound(ctx context.Context, sigCh <-chan *dbus.Signal, charPath dbus.ObjectPath) {
	for {
		select {
		case <-ctx.Done():
			return
		case sig := <-sigCh:
			if sig == nil || sig.Path != charPath || sig.Name != propsIface+".PropertiesChanged" {
				continue
			}
			if len(sig.Body) < 2 {
				continue
			}
			iface, _ := sig.Body[0].(string)
			if iface != gattCharIface {
				continue
			}
			changed, _ := sig.Body[1].(map[string]dbus.Variant)
			val, ok := changed["Value"]
			if !ok {
				continue
			}
			raw, ok := val.Value().([]byte)
			if !ok {
				continue
			}
			log.Printf("inbound raw: % X", raw)
			for _, m := range decodeBLEMIDI(raw) {
				log.Printf("  inbound MIDI: %s", m)
			}
		}
	}
}

// decodeBLEMIDI strips BLE-MIDI timestamp framing and returns the contained
// MIDI messages. Handles running status; ignores SysEx (not needed for the
// spike's CC/PC proof).
func decodeBLEMIDI(p []byte) []string {
	var msgs []string
	if len(p) < 2 {
		return msgs
	}
	i := 1 // skip header byte
	var status byte
	for i < len(p) {
		if p[i]&0x80 != 0 { // timestamp byte
			i++
		}
		if i >= len(p) {
			break
		}
		if p[i]&0x80 != 0 { // new status byte
			status = p[i]
			i++
		}
		if status == 0 {
			break
		}
		n := midiDataLen(status)
		if i+n > len(p) {
			break
		}
		msgs = append(msgs, describeMIDI(status, p[i:i+n]))
		i += n
	}
	return msgs
}

func midiDataLen(status byte) int {
	switch status & 0xF0 {
	case 0xC0, 0xD0: // program change, channel pressure
		return 1
	default: // note on/off, poly aftertouch, CC, pitch bend
		return 2
	}
}

func describeMIDI(status byte, data []byte) string {
	ch := (status & 0x0F) + 1
	switch status & 0xF0 {
	case 0xB0:
		return fmt.Sprintf("CC ch%d cc%d=%d", ch, data[0], data[1])
	case 0xC0:
		return fmt.Sprintf("ProgramChange ch%d program=%d", ch, data[0])
	case 0x90:
		return fmt.Sprintf("NoteOn ch%d note=%d vel=%d", ch, data[0], data[1])
	case 0x80:
		return fmt.Sprintf("NoteOff ch%d note=%d vel=%d", ch, data[0], data[1])
	default:
		return fmt.Sprintf("status 0x%02X data % X", status, data)
	}
}

// --- Agent1: a NoInputNoOutput pairing agent that accepts everything ---------

type agent struct{}

func (agent) Release() *dbus.Error { return nil }
func (agent) RequestPinCode(dbus.ObjectPath) (string, *dbus.Error) {
	log.Printf("agent: RequestPinCode -> 0000")
	return "0000", nil
}
func (agent) DisplayPinCode(_ dbus.ObjectPath, pin string) *dbus.Error {
	log.Printf("agent: DisplayPinCode %s", pin)
	return nil
}
func (agent) RequestPasskey(dbus.ObjectPath) (uint32, *dbus.Error) {
	log.Printf("agent: RequestPasskey -> 0")
	return 0, nil
}
func (agent) DisplayPasskey(_ dbus.ObjectPath, passkey uint32, entered uint16) *dbus.Error {
	log.Printf("agent: DisplayPasskey %06d (entered %d)", passkey, entered)
	return nil
}
func (agent) RequestConfirmation(_ dbus.ObjectPath, passkey uint32) *dbus.Error {
	log.Printf("agent: RequestConfirmation %06d -> accept", passkey)
	return nil
}
func (agent) RequestAuthorization(dbus.ObjectPath) *dbus.Error {
	log.Printf("agent: RequestAuthorization -> accept")
	return nil
}
func (agent) AuthorizeService(_ dbus.ObjectPath, uuid string) *dbus.Error {
	log.Printf("agent: AuthorizeService %s -> accept", uuid)
	return nil
}
func (agent) Cancel() *dbus.Error {
	log.Printf("agent: Cancel")
	return nil
}

// registerAgent exports the agent and makes it the default. The returned func
// unregisters it.
func registerAgent(conn *dbus.Conn, capability string) (func(), error) {
	a := agent{}
	if err := conn.Export(a, agentPath, agentIface); err != nil {
		return nil, fmt.Errorf("export agent: %w", err)
	}
	mgr := conn.Object(bluezBus, dbus.ObjectPath("/org/bluez"))
	if call := mgr.Call(agentManagerIface+".RegisterAgent", 0, agentPath, capability); call.Err != nil {
		return nil, fmt.Errorf("register agent: %w", call.Err)
	}
	if call := mgr.Call(agentManagerIface+".RequestDefaultAgent", 0, agentPath); call.Err != nil {
		// Non-fatal: pairing can still succeed with a registered (non-default) agent.
		log.Printf("warning: RequestDefaultAgent: %v", call.Err)
	}
	return func() {
		mgr.Call(agentManagerIface+".UnregisterAgent", 0, agentPath)
		conn.Export(nil, agentPath, agentIface)
	}, nil
}

// --- small D-Bus helpers -----------------------------------------------------

func managedObjects(conn *dbus.Conn) (map[dbus.ObjectPath]map[string]map[string]dbus.Variant, error) {
	var objects map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	root := conn.Object(bluezBus, dbus.ObjectPath("/"))
	if call := root.Call(objMgrIface+".GetManagedObjects", 0); call.Err != nil {
		return nil, fmt.Errorf("get managed objects: %w", call.Err)
	} else if err := call.Store(&objects); err != nil {
		return nil, fmt.Errorf("decode managed objects: %w", err)
	}
	return objects, nil
}

func setProp(obj dbus.BusObject, iface, name string, val dbus.Variant) error {
	call := obj.Call(propsIface+".Set", 0, iface, name, val)
	return call.Err
}

func boolProp(obj dbus.BusObject, iface, name string) (bool, error) {
	v, err := obj.GetProperty(iface + "." + name)
	if err != nil {
		return false, err
	}
	b, _ := v.Value().(bool)
	return b, nil
}

func strProp(props map[string]dbus.Variant, name string) string {
	if v, ok := props[name]; ok {
		s, _ := v.Value().(string)
		return s
	}
	return ""
}

func uuidProp(props map[string]dbus.Variant) []string {
	if v, ok := props["UUIDs"]; ok {
		u, _ := v.Value().([]string)
		return u
	}
	return nil
}

// deviceLabel prefers Alias, falling back to Name.
func deviceLabel(props map[string]dbus.Variant) string {
	if s := strProp(props, "Alias"); s != "" {
		return s
	}
	return strProp(props, "Name")
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
