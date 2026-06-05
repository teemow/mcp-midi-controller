// Command aum-test is a throwaway hardware harness for exercising the AUM
// BLE-MIDI path end to end without the full MCP daemon. It uses the real engine
// code path (discover -> pair/connect -> bind -> SetControl), so a successful
// run proves the same thing the daemon would.
//
// Usage:
//
//	go run ./cmd/aum-test --scan                       # list reachable BLE endpoints
//	go run ./cmd/aum-test --endpoint <ADDR> --channel 9 --value start
//	go run ./cmd/aum-test --endpoint <ADDR> --channel 9 --repeat 5 --delay 700ms
//
// channel is the 0-based MIDI wire channel (human channel - 1); AUM human
// channel 10 == wire 9.
package main

import (
	"context"
	"flag"
	"log"
	"time"

	"github.com/teemow/mcp-midi-controller/internal/device"
	"github.com/teemow/mcp-midi-controller/internal/engine"
	"github.com/teemow/mcp-midi-controller/internal/transport"
	"github.com/teemow/mcp-midi-controller/internal/transport/blemidi"
	"gitlab.com/gomidi/midi/v2"
	_ "gitlab.com/gomidi/midi/v2/drivers/rtmididrv"
)

func main() {
	log.SetFlags(log.Ltime)

	scan := flag.Bool("scan", false, "discover and print BLE-MIDI endpoints, then exit")
	endpoint := flag.String("endpoint", "", "BLE address of the iPad/AUM endpoint")
	channel := flag.Int("channel", 9, "0-based MIDI wire channel (human - 1); AUM ch10 = 9")
	control := flag.String("control", "transport", "control name on the aum definition")
	value := flag.String("value", "start", "control value (transport: start|stop)")
	cc := flag.Int("cc", -1, "raw mode: send this CC number instead of using the aum definition")
	val := flag.Int("val", 127, "raw mode: CC value (0-127)")
	repeat := flag.Int("repeat", 1, "send the control N times (useful for AUM MIDI-learn)")
	delay := flag.Duration("delay", 600*time.Millisecond, "delay between repeats")
	listPorts := flag.Bool("list-ports", false, "list ALSA-seq MIDI out ports and exit")
	alsaPort := flag.String("alsaport", "", "send CC directly to this ALSA-seq out port by name (role-flip path; bypasses BLE pairing)")
	flag.Parse()

	// Role-flip path: AUM (central) connects to this host's BLE-MIDI server,
	// PipeWire bridges it to an ALSA-seq port, and we send straight to that port.
	if *listPorts {
		for _, p := range midi.GetOutPorts() {
			log.Printf("out port: %s", p.String())
		}
		return
	}
	if *alsaPort != "" {
		ccNum := *cc
		if ccNum < 0 {
			ccNum = 62 // AUM "play" on this rig
		}
		out, err := midi.FindOutPort(*alsaPort)
		if err != nil {
			log.Fatalf("find ALSA-seq out port %q: %v (try --list-ports)", *alsaPort, err)
		}
		if err := out.Open(); err != nil {
			log.Fatalf("open out port %q: %v", *alsaPort, err)
		}
		defer func() { _ = out.Close() }()
		status := byte(0xB0) | byte(*channel&0x0F)
		for i := 0; i < *repeat; i++ {
			msg := []byte{status, byte(ccNum), byte(*val)}
			if err := out.Send(msg); err != nil {
				log.Fatalf("send to %q: %v", *alsaPort, err)
			}
			log.Printf("sent CC %d = %d on wire ch %d (human %d) [% X] -> %q (%d/%d)", ccNum, *val, *channel, *channel+1, msg, out.String(), i+1, *repeat)
			if i < *repeat-1 {
				time.Sleep(*delay)
			}
		}
		log.Printf("done.")
		return
	}

	ble, err := blemidi.New()
	if err != nil {
		log.Fatalf("blemidi.New: %v", err)
	}
	reg, err := device.LoadBundled()
	if err != nil {
		log.Fatalf("load definitions: %v", err)
	}
	eng := engine.New(reg, ble)
	ctx := context.Background()

	if *scan {
		log.Printf("scanning for BLE endpoints (~8s)...")
		eps, err := eng.DiscoverEndpoints(ctx)
		if err != nil {
			log.Fatalf("discover: %v", err)
		}
		if len(eps) == 0 {
			log.Printf("no endpoints found")
			return
		}
		for _, ep := range eps {
			log.Printf("  %s  %q  (paired=%t connected=%t)", ep.ID, ep.Name, ep.Paired, ep.Connected)
		}
		return
	}

	if *endpoint == "" {
		log.Fatalf("need --endpoint <ADDR> (or --scan). e.g. --endpoint AA:BB:CC:DD:EE:FF")
	}

	log.Printf("pairing + connecting %s ...", *endpoint)
	if err := eng.PairEndpoint(ctx, "blemidi", *endpoint); err != nil {
		log.Fatalf("pair/connect %s: %v", *endpoint, err)
	}
	log.Printf("connected.")

	// Raw mode: send a CC verbatim (matches whatever AUM is actually mapped to,
	// e.g. CC 62 = play on channel 16). 0xB0|ch status, cc, value.
	if *cc >= 0 {
		if *cc > 127 || *val < 0 || *val > 127 {
			log.Fatalf("cc and val must be in [0,127]")
		}
		status := byte(0xB0) | byte(*channel&0x0F)
		data := []int{int(status), *cc, *val}
		for i := 0; i < *repeat; i++ {
			ev := transport.Event{Kind: transport.MIDIEvent, Data: []byte{byte(data[0]), byte(data[1]), byte(data[2])}}
			if err := eng.SendRaw(ctx, "blemidi", *endpoint, ev); err != nil {
				log.Fatalf("send CC %d=%d on wire ch %d: %v", *cc, *val, *channel, err)
			}
			log.Printf("sent CC %d = %d on wire channel %d (human %d) [% X] (%d/%d)", *cc, *val, *channel, *channel+1, ev.Data, i+1, *repeat)
			if i < *repeat-1 {
				time.Sleep(*delay)
			}
		}
		log.Printf("done.")
		return
	}

	// The AUM mixer device type is no longer bundled (it is session-derived now,
	// see internal/aum.MixerDeviceType). For this BLE-direct harness, register a
	// tiny standalone "aum" type carrying just the transport toggle on blemidi so
	// the convenience --control path keeps working without a session.
	cc20 := 20
	aumType := &device.DeviceType{
		ID:        "aum",
		Name:      "AUM (harness)",
		Transport: "blemidi",
		Controls: []device.Control{{
			Name: "transport", Type: device.ControlCC, CC: &cc20,
			Value: device.ValueSpec{Type: device.ValueEnum, Values: map[string]int{"stop": 0, "start": 127}},
		}},
	}
	if err := reg.AddDefinition(aumType); err != nil {
		log.Fatalf("register aum harness type: %v", err)
	}
	d := engine.Device{Name: "aum", DeviceID: "aum", Endpoint: *endpoint, Channel: *channel}
	if err := eng.Bind(d); err != nil {
		log.Fatalf("bind aum: %v", err)
	}
	log.Printf("bound aum -> endpoint %s, wire channel %d (human %d)", *endpoint, *channel, *channel+1)

	for i := 0; i < *repeat; i++ {
		if err := eng.SetControl(ctx, "aum", *control, *value); err != nil {
			log.Fatalf("SetControl %s=%s: %v", *control, *value, err)
		}
		log.Printf("sent %s=%s (%d/%d)", *control, *value, i+1, *repeat)
		if i < *repeat-1 {
			time.Sleep(*delay)
		}
	}
	log.Printf("done.")
}
