package mdns

// dns-sd path (macOS): register with the system's mDNSResponder through its
// `dns-sd -R` command-line client. The registration lives exactly as long as
// the dns-sd process — mDNSResponder withdraws it, with goodbye packets, once
// the client's connection closes — so the process is supervised: killed when
// ctx is cancelled, restarted when it dies while ctx is live (an mDNSResponder
// restart ends every client connection). The driver itself is platform neutral
// so its supervision is tested everywhere against a fake dns-sd; only macOS
// wires it to Announce (announce_darwin.go).

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	// dnssdRegistered is the status dns-sd prints once mDNSResponder has
	// probed the name and the service is live on the network.
	dnssdRegistered = "Name now registered and active"
	// dnssdReplyMarker starts every registration reply dns-sd prints:
	// "<time>  Got a reply for service <name>.<type>.<domain>.: <status>".
	dnssdReplyMarker = "Got a reply for service "
)

// dnssd drives one service registration through a dns-sd binary.
type dnssd struct {
	bin      string   // dns-sd binary
	args     []string // `dns-sd -R` arguments for the service
	instance string
	port     uint16

	// confirmTimeout bounds how long one dns-sd start may take to report the
	// registration (probing takes about a second).
	confirmTimeout time.Duration
	// minRetry and maxRetry bound the exponential backoff between restarts;
	// a registration that stayed up for stableAfter resets it to minRetry.
	minRetry, maxRetry, stableAfter time.Duration
}

func newDNSSD(bin, instance string, port uint16, txt []string) *dnssd {
	return &dnssd{
		bin:            bin,
		args:           dnssdArgs(instance, port, txt),
		instance:       instance,
		port:           port,
		confirmTimeout: 10 * time.Second,
		minRetry:       time.Second,
		maxRetry:       30 * time.Second,
		stableAfter:    time.Minute,
	}
}

// dnssdArgs builds `dns-sd -R <instance> <type> local <port> [<txt>...]`, one
// argument per TXT entry. dns-sd reads a backslash in a TXT argument as an
// escape (`\xHH`, or `\c` for c), so literal backslashes are doubled to
// register each entry's bytes exactly as given.
func dnssdArgs(instance string, port uint16, txt []string) []string {
	args := []string{"-R", instance, ServiceType, "local", strconv.Itoa(int(port))}
	for _, t := range txt {
		args = append(args, strings.ReplaceAll(t, `\`, `\\`))
	}
	return args
}

// parseReply reads a registration reply from a line of dns-sd output. name is
// the instance name mDNSResponder reports (after any automatic rename on a
// name conflict) and status what happened to it; ok is false for every other
// line (the banner, timestamps, client errors).
func parseReply(line string) (name, status string, ok bool) {
	_, rest, found := strings.Cut(line, dnssdReplyMarker)
	if !found {
		return "", "", false
	}
	// The status never contains ": ", the instance name may.
	i := strings.LastIndex(rest, ": ")
	if i < 0 {
		return "", "", false
	}
	name, status = rest[:i], rest[i+2:]
	if j := strings.LastIndex(name, "."+ServiceType+"."); j >= 0 {
		name = name[:j]
	}
	return name, status, true
}

// announce starts the registration, waits for mDNSResponder to confirm it,
// and leaves it to a supervisor until ctx is cancelled. It returns an error
// only when that first registration fails.
func (d *dnssd) announce(ctx context.Context) error {
	p, name, err := d.start(ctx)
	if err != nil {
		return err
	}
	log.Printf("mdns: announced %q (%s port %d) via dns-sd", name, ServiceType, d.port)
	go d.supervise(ctx, p)
	return nil
}

// dnssdProc is one running dns-sd and its output, line by line; lines closes
// once the process has closed its output (it exited).
type dnssdProc struct {
	cmd   *exec.Cmd
	lines chan string
}

// start launches dns-sd and waits until mDNSResponder confirms the
// registration, returning the running process and the registered instance
// name. A dns-sd that exits, reports an error, or stays silent for
// confirmTimeout is stopped and reported as an error. Cancelling ctx kills
// the process at any time.
func (d *dnssd) start(ctx context.Context) (*dnssdProc, string, error) {
	cmd := exec.CommandContext(ctx, d.bin, d.args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, "", fmt.Errorf("mdns: dns-sd: %w", err)
	}
	cmd.Stderr = cmd.Stdout // one pipe: dns-sd reports client errors on stderr
	if err := cmd.Start(); err != nil {
		return nil, "", fmt.Errorf("mdns: start dns-sd: %w", err)
	}
	p := &dnssdProc{cmd: cmd, lines: make(chan string, 16)}
	go p.read(out)

	timer := time.NewTimer(d.confirmTimeout)
	defer timer.Stop()
	var last string // last non-reply output line, for the error
	for {
		select {
		case line, ok := <-p.lines:
			if !ok {
				return nil, "", fmt.Errorf("mdns: dns-sd exited before registering (%v)%s", p.cmd.Wait(), lastLine(last))
			}
			name, status, isReply := parseReply(line)
			if !isReply {
				if strings.TrimSpace(line) != "" {
					last = line
				}
				continue
			}
			if status == dnssdRegistered {
				return p, name, nil
			}
			p.stop()
			return nil, "", fmt.Errorf("mdns: dns-sd register %q: %s", name, status)
		case <-timer.C:
			p.stop()
			return nil, "", fmt.Errorf("mdns: dns-sd: no registration reply within %s%s", d.confirmTimeout, lastLine(last))
		case <-ctx.Done():
			p.stop()
			return nil, "", fmt.Errorf("mdns: dns-sd: %w", ctx.Err())
		}
	}
}

// supervise keeps the registration alive until ctx is cancelled (the
// CommandContext kill then withdraws it): it logs what mDNSResponder reports
// about the running registration and, whenever dns-sd dies while ctx is live,
// starts a fresh one with exponential backoff. Losing this would silently
// strand the iPad at "waiting for mcp-midi-controller on the LAN" even though
// the receiver is healthy.
func (d *dnssd) supervise(ctx context.Context, p *dnssdProc) {
	delay := d.minRetry
	for {
		up := time.Now()
		var last string
		for line := range p.lines {
			if name, status, ok := parseReply(line); ok {
				log.Printf("mdns: dns-sd: %q: %s", name, status)
			} else if strings.TrimSpace(line) != "" {
				last = line
			}
		}
		err := p.cmd.Wait()
		if ctx.Err() != nil {
			return
		}
		if time.Since(up) >= d.stableAfter {
			delay = d.minRetry
		}
		log.Printf("mdns: dns-sd exited (%v)%s; re-announcing %q", err, lastLine(last), d.instance)

		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			delay = min(2*delay, d.maxRetry)
			np, name, err := d.start(ctx)
			if err == nil {
				p = np
				log.Printf("mdns: re-announced %q via dns-sd", name)
				break
			}
			if ctx.Err() != nil {
				return
			}
			log.Printf("%v (retrying)", err)
		}
	}
}

// read feeds dns-sd's output to lines and keeps draining it, so dns-sd can
// never block on a full pipe.
func (p *dnssdProc) read(out io.Reader) {
	defer close(p.lines)
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		p.lines <- sc.Text()
	}
	_, _ = io.Copy(io.Discard, out)
}

// stop kills the process, drains its output and reaps it.
func (p *dnssdProc) stop() {
	_ = p.cmd.Process.Kill()
	for range p.lines {
	}
	_ = p.cmd.Wait()
}

func lastLine(line string) string {
	if line == "" {
		return ""
	}
	return ": " + strings.TrimSpace(line)
}
