// Command pwmidispike is a THROWAWAY proof (companion to cmd/blespike) that
// validates the *PipeWire* BLE-MIDI data path. On a PipeWire desktop the
// WirePlumber bluez5 plugin claims every bonded BLE-MIDI endpoint and bridges
// it into the ALSA sequencer (e.g. client "teemow-pedals", port "... Bluetooth"
// [In/Out]). That makes the device reachable as an ordinary ALSA-seq MIDI port,
// which gomidi/ALSA can drive in Phase B without our daemon owning the GATT
// (raw GATT WriteValue is rejected with "Not Authorized" because PipeWire holds
// the characteristic).
//
// This binary opens an ALSA-seq client, subscribes to the target port both ways,
// drives the MD-200 (CC28 On/Off, CC17 Rate sweep) and prints any inbound MIDI
// (the feedback channel). It uses libasound directly via cgo; it is removed once
// the real transport lands.
//
//	go run ./cmd/pwmidispike -name teemow-pedals -channel 1
//
// Requires: cgo + libasound (pkg-config alsa) + the ALSA sequencer (/dev/snd/seq).
package main

/*
#cgo pkg-config: alsa
#include <alsa/asoundlib.h>
#include <poll.h>
#include <string.h>
#include <errno.h>

static snd_seq_t* g_seq = NULL;
static int g_out = -1, g_in = -1;

static int spike_open(void) {
    int err = snd_seq_open(&g_seq, "default", SND_SEQ_OPEN_DUPLEX, 0);
    if (err < 0) return err;
    snd_seq_set_client_name(g_seq, "pwmidispike");
    g_out = snd_seq_create_simple_port(g_seq, "out",
        SND_SEQ_PORT_CAP_READ | SND_SEQ_PORT_CAP_SUBS_READ,
        SND_SEQ_PORT_TYPE_MIDI_GENERIC | SND_SEQ_PORT_TYPE_APPLICATION);
    if (g_out < 0) return g_out;
    g_in = snd_seq_create_simple_port(g_seq, "in",
        SND_SEQ_PORT_CAP_WRITE | SND_SEQ_PORT_CAP_SUBS_WRITE,
        SND_SEQ_PORT_TYPE_MIDI_GENERIC | SND_SEQ_PORT_TYPE_APPLICATION);
    if (g_in < 0) return g_in;
    return 0;
}

// spike_find locates the first seq port on a client whose name contains namesub
// and that we can subscribe to for writing. Returns 1 on hit.
static int spike_find(const char* namesub, int* oclient, int* oport, char* oname, int onamelen) {
    snd_seq_client_info_t* cinfo;
    snd_seq_port_info_t* pinfo;
    snd_seq_client_info_alloca(&cinfo);
    snd_seq_port_info_alloca(&pinfo);
    snd_seq_client_info_set_client(cinfo, -1);
    while (snd_seq_query_next_client(g_seq, cinfo) >= 0) {
        int client = snd_seq_client_info_get_client(cinfo);
        const char* cname = snd_seq_client_info_get_name(cinfo);
        if (cname == NULL || strstr(cname, namesub) == NULL) continue;
        snd_seq_port_info_set_client(pinfo, client);
        snd_seq_port_info_set_port(pinfo, -1);
        while (snd_seq_query_next_port(g_seq, pinfo) >= 0) {
            unsigned int caps = snd_seq_port_info_get_capability(pinfo);
            if (caps & SND_SEQ_PORT_CAP_SUBS_WRITE) {
                *oclient = client;
                *oport = snd_seq_port_info_get_port(pinfo);
                snprintf(oname, onamelen, "%s : %s", cname, snd_seq_port_info_get_name(pinfo));
                return 1;
            }
        }
    }
    return 0;
}

// spike_connect subscribes our out->dest and dest->our in. The reverse link is
// best-effort (the device may not transmit).
static int spike_connect(int dclient, int dport, int* gotIn) {
    int err = snd_seq_connect_to(g_seq, g_out, dclient, dport);
    if (err < 0) return err;
    *gotIn = (snd_seq_connect_from(g_seq, g_in, dclient, dport) >= 0) ? 1 : 0;
    return 0;
}

// spike_send_rt sends a MIDI System Real-Time message (clock/start/stop/...).
// These are channel-less; they go to every device on the link.
static int spike_send_rt(int type) {
    snd_seq_event_t ev;
    snd_seq_ev_clear(&ev);
    snd_seq_ev_set_source(&ev, g_out);
    snd_seq_ev_set_subs(&ev);
    snd_seq_ev_set_direct(&ev);
    ev.type = type;
    return snd_seq_event_output_direct(g_seq, &ev);
}

static int spike_send_pc(int ch, int prog) {
    snd_seq_event_t ev;
    snd_seq_ev_clear(&ev);
    snd_seq_ev_set_source(&ev, g_out);
    snd_seq_ev_set_subs(&ev);
    snd_seq_ev_set_direct(&ev);
    snd_seq_ev_set_pgmchange(&ev, ch, prog);
    int err = snd_seq_event_output(g_seq, &ev);
    if (err < 0) return err;
    return snd_seq_drain_output(g_seq);
}

static int spike_send_cc(int ch, int cc, int val) {
    snd_seq_event_t ev;
    snd_seq_ev_clear(&ev);
    snd_seq_ev_set_source(&ev, g_out);
    snd_seq_ev_set_subs(&ev);
    snd_seq_ev_set_direct(&ev);
    snd_seq_ev_set_controller(&ev, ch, cc, val);
    int err = snd_seq_event_output(g_seq, &ev);
    if (err < 0) return err;
    return snd_seq_drain_output(g_seq);
}

// spike_recv polls for one inbound event up to timeout_ms. Returns 1 on event
// (filling etype/ch/d1/d2), 0 on timeout, <0 on error.
static int spike_recv(int* etype, int* ch, int* d1, int* d2, int timeout_ms) {
    struct pollfd pfd[16];
    int npfd = snd_seq_poll_descriptors_count(g_seq, POLLIN);
    if (npfd > 16) npfd = 16;
    snd_seq_poll_descriptors(g_seq, pfd, npfd, POLLIN);
    // Go's scheduler interrupts blocking syscalls with signals (SIGURG); retry
    // poll on EINTR so we don't surface spurious "Operation not permitted".
    int r;
    do { r = poll(pfd, npfd, timeout_ms); } while (r < 0 && errno == EINTR);
    if (r <= 0) return r;
    snd_seq_event_t* ev = NULL;
    int err = snd_seq_event_input(g_seq, &ev);
    if (err < 0) return err;
    *etype = ev->type;
    *ch = 0; *d1 = 0; *d2 = 0;
    switch (ev->type) {
    case SND_SEQ_EVENT_CONTROLLER:
        *ch = ev->data.control.channel; *d1 = ev->data.control.param; *d2 = ev->data.control.value; break;
    case SND_SEQ_EVENT_PGMCHANGE:
        *ch = ev->data.control.channel; *d1 = ev->data.control.value; break;
    case SND_SEQ_EVENT_NOTEON:
    case SND_SEQ_EVENT_NOTEOFF:
        *ch = ev->data.note.channel; *d1 = ev->data.note.note; *d2 = ev->data.note.velocity; break;
    }
    return 1;
}

static const char* spike_strerror(int err) { return snd_strerror(err); }
*/
import "C"

import (
	"flag"
	"fmt"
	"log"
	"time"
	"unsafe"
)

func main() {
	log.SetFlags(log.Ltime)
	name := flag.String("name", "teemow-pedals", "substring of the ALSA-seq client name to target (the PipeWire BLE-MIDI bridge)")
	channel := flag.Int("channel", 1, "MIDI channel 1-16 the MD-200 receives on (RCH)")
	listenAfter := flag.Duration("listen", 6*time.Second, "how long to listen for inbound MIDI after the drive sequence")
	pc := flag.Bool("pc", false, "program-change mode: cycle Program Changes 0..-pc-max on -channel (verifies preset-based pedals like the H90)")
	pcMax := flag.Int("pc-max", 4, "highest program number to cycle to in -pc mode")
	program := flag.Int("program", -1, "send a single Program Change (wire value) on -channel and exit")
	ccNum := flag.Int("cc", -1, "send a single CC (controller number) on -channel with -value and exit")
	ccVal := flag.Int("value", 0, "value (0-127) for -cc")
	clock := flag.Bool("clock", false, "send MIDI clock (system real-time, channel-less) at -bpm for -dwell; for tempo-synced gear like the Boss SL-2")
	bpm := flag.Float64("bpm", 60, "tempo (BPM) for -clock mode")
	identify := flag.Bool("identify", false, "single-channel mode: slowly blink On/Off (CC28) + sweep Rate (CC17) on -channel for -dwell, so you can clearly watch one pedal")
	sweep := flag.Bool("sweep", false, "channel-discovery mode: blink On/Off (CC28) on each channel in [-from,-to] and pause, so you can see which pedal reacts")
	sweepFrom := flag.Int("from", 4, "sweep start channel (1-16)")
	sweepTo := flag.Int("to", 16, "sweep end channel (1-16)")
	dwell := flag.Duration("dwell", 3*time.Second, "pause per channel during sweep")
	flag.Parse()
	if *channel < 1 || *channel > 16 {
		log.Fatalf("-channel must be 1-16")
	}
	ch := C.int(*channel - 1)

	if err := C.spike_open(); err < 0 {
		log.Fatalf("alsa seq open: %s", C.GoString(C.spike_strerror(err)))
	}
	log.Printf("opened ALSA-seq client 'pwmidispike'")

	var dclient, dport C.int
	nameBuf := make([]byte, 128)
	csub := C.CString(*name)
	defer C.free(unsafe.Pointer(csub))
	if C.spike_find(csub, &dclient, &dport, (*C.char)(unsafe.Pointer(&nameBuf[0])), C.int(len(nameBuf))) != 1 {
		log.Fatalf("no ALSA-seq port found matching %q (is PipeWire bridging the BLE-MIDI device?)", *name)
	}
	target := C.GoString((*C.char)(unsafe.Pointer(&nameBuf[0])))
	log.Printf("target ALSA-seq port %d:%d  (%s)", int(dclient), int(dport), target)

	var gotIn C.int
	if err := C.spike_connect(dclient, dport, &gotIn); err < 0 {
		log.Fatalf("subscribe: %s", C.GoString(C.spike_strerror(err)))
	}
	log.Printf("subscribed out->device; inbound link: %v", gotIn == 1)

	sendCCon := func(c, cc, val int) {
		if err := C.spike_send_cc(C.int(c), C.int(cc), C.int(val)); err < 0 {
			log.Fatalf("send ch%d CC%d=%d: %s", c+1, cc, val, C.GoString(C.spike_strerror(err)))
		}
	}
	sendCC := func(cc, val int) { sendCCon(int(ch), cc, val) }

	if *ccNum >= 0 {
		log.Printf("ch%d CC%d -> %d (single)", *channel, *ccNum, *ccVal)
		if err := C.spike_send_cc(ch, C.int(*ccNum), C.int(*ccVal)); err < 0 {
			log.Fatalf("send CC%d=%d: %s", *ccNum, *ccVal, C.GoString(C.spike_strerror(err)))
		}
		log.Printf("sent")
		return
	}

	if *clock {
		interval := time.Duration(float64(time.Minute) / (*bpm * 24)) // 24 MIDI clocks per quarter note
		log.Printf("CLOCK mode: %.0f BPM for %s (MIDI clock is channel-less -- watch the SL-2's tempo LED sync)", *bpm, *dwell)
		if C.spike_send_rt(C.SND_SEQ_EVENT_START) < 0 {
			log.Fatalf("send MIDI Start failed")
		}
		end := time.Now().Add(*dwell)
		next := time.Now()
		for time.Now().Before(end) {
			if C.spike_send_rt(C.SND_SEQ_EVENT_CLOCK) < 0 {
				log.Fatalf("send MIDI Clock failed")
			}
			next = next.Add(interval)
			if d := time.Until(next); d > 0 {
				time.Sleep(d)
			}
		}
		C.spike_send_rt(C.SND_SEQ_EVENT_STOP)
		log.Printf("clock done")
		return
	}

	if *program >= 0 {
		log.Printf("ch%d ProgramChange -> %d (single)", *channel, *program)
		if err := C.spike_send_pc(ch, C.int(*program)); err < 0 {
			log.Fatalf("send PC%d: %s", *program, C.GoString(C.spike_strerror(err)))
		}
		log.Printf("sent")
		return
	}

	if *pc {
		log.Printf("PC mode: cycling Program Change 0..%d on channel %d (watch the pedal's preset/display change)", *pcMax, *channel)
		for p := 0; p <= *pcMax; p++ {
			log.Printf("ch%d ProgramChange -> %d", *channel, p)
			if err := C.spike_send_pc(ch, C.int(p)); err < 0 {
				log.Fatalf("send PC%d: %s", p, C.GoString(C.spike_strerror(err)))
			}
			drainInbound(1800)
		}
		log.Printf("pc done (channel %d)", *channel)
		return
	}

	if *identify {
		log.Printf("IDENTIFY mode: channel %d, slow On/Off blink + Rate sweep for %s -- watch which pedal reacts", *channel, *dwell)
		end := time.Now().Add(*dwell)
		for time.Now().Before(end) {
			log.Printf("ch%d CC28 -> 127 (ON)", *channel)
			sendCC(28, 127)
			drainInbound(1200)
			log.Printf("ch%d CC28 -> 0 (OFF)", *channel)
			sendCC(28, 0)
			drainInbound(1200)
			log.Printf("ch%d CC17 (Rate) sweep", *channel)
			for v := 0; v <= 127; v += 8 {
				sendCC(17, v)
				drainInbound(60)
			}
		}
		log.Printf("identify done (channel %d)", *channel)
		return
	}

	if *sweep {
		if *sweepFrom < 1 || *sweepTo > 16 || *sweepFrom > *sweepTo {
			log.Fatalf("invalid sweep range %d-%d", *sweepFrom, *sweepTo)
		}
		log.Printf("SWEEP mode: blinking On/Off (CC28) per channel %d..%d; watch which pedal reacts", *sweepFrom, *sweepTo)
		for c := *sweepFrom; c <= *sweepTo; c++ {
			log.Printf("=== CHANNEL %d ===", c)
			for i := 0; i < 3; i++ { // blink: on/off a few times
				sendCCon(c-1, 28, 127)
				drainInbound(350)
				sendCCon(c-1, 28, 0)
				drainInbound(350)
			}
			drainInbound(int(dwell.Milliseconds())) // pause + capture any echo
		}
		log.Printf("sweep done")
		return
	}

	log.Printf("CC28 (On/Off) -> 127 (on)")
	sendCC(28, 127)
	drainInbound(800)
	log.Printf("CC28 (On/Off) -> 0 (off)")
	sendCC(28, 0)
	drainInbound(800)

	log.Printf("CC17 (Rate) sweep 0..127")
	for v := 0; v <= 127; v += 16 {
		sendCC(17, v)
		drainInbound(100)
	}
	sendCC(17, 127)

	log.Printf("listening %s for inbound MIDI (feedback)...", *listenAfter)
	deadline := time.Now().Add(*listenAfter)
	for time.Now().Before(deadline) {
		drainInbound(200)
	}
	log.Printf("done")
}

// drainInbound prints any inbound events arriving within the window (ms).
func drainInbound(windowMS int) {
	end := time.Now().Add(time.Duration(windowMS) * time.Millisecond)
	for {
		remain := int(time.Until(end).Milliseconds())
		if remain <= 0 {
			return
		}
		var etype, ch, d1, d2 C.int
		r := C.spike_recv(&etype, &ch, &d1, &d2, C.int(remain))
		if r < 0 {
			log.Printf("recv error: %s", C.GoString(C.spike_strerror(r)))
			return
		}
		if r == 0 {
			return
		}
		log.Printf("  inbound: %s", describe(int(etype), int(ch), int(d1), int(d2)))
	}
}

func describe(etype, ch, d1, d2 int) string {
	switch etype {
	case int(C.SND_SEQ_EVENT_CONTROLLER):
		return fmt.Sprintf("CC ch%d cc%d=%d", ch+1, d1, d2)
	case int(C.SND_SEQ_EVENT_PGMCHANGE):
		return fmt.Sprintf("ProgramChange ch%d program=%d", ch+1, d1)
	case int(C.SND_SEQ_EVENT_NOTEON):
		return fmt.Sprintf("NoteOn ch%d note=%d vel=%d", ch+1, d1, d2)
	case int(C.SND_SEQ_EVENT_NOTEOFF):
		return fmt.Sprintf("NoteOff ch%d note=%d vel=%d", ch+1, d1, d2)
	default:
		return fmt.Sprintf("event type=%d ch%d d1=%d d2=%d", etype, ch+1, d1, d2)
	}
}
