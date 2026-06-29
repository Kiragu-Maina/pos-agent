// Package scan discovers thermal receipt printers on the local network.
//
// The browser sandbox cannot open raw TCP sockets, so all network discovery
// happens here in the local agent. We sweep the machine's own /24 networks and
// try to open a TCP connection to the ports thermal printers commonly listen
// on. A successful connect proves both that a host is alive and that something
// is listening on a printer port, in a single step. No separate ping sweep is
// needed.
package scan

import (
	"context"
	"fmt"
	"net"
	"sort"
	"sync"
	"time"
)

// Printer ports we probe, in priority order.
//
//	9100 - RAW / JetDirect, the usual ESC/POS port. Strongest signal.
//	515  - LPR / LPD.
//	631  - IPP.
var printerPorts = []int{9100, 515, 631}

// dialTimeout is deliberately short. A reachable host on the LAN answers in a
// few milliseconds; an unused address simply times out. Keeping this small is
// what lets a full /24 sweep finish in well under a second.
const dialTimeout = 400 * time.Millisecond

// maxConcurrent caps simultaneous dials so we stay light on resource
// constrained machines while still finishing the sweep quickly.
const maxConcurrent = 256

// Device is a host that answered on at least one printer port.
type Device struct {
	IP    string `json:"ip"`
	Ports []int  `json:"ports"`
	// Likely is true when 9100 is open, the strongest signal that this really
	// is a raw ESC/POS receipt printer.
	Likely bool `json:"likely"`
}

// Addr returns the host:port the printer package should print to, preferring
// the raw ESC/POS port when present.
func (d Device) Addr() string {
	port := d.Ports[0]
	for _, p := range d.Ports {
		if p == 9100 {
			port = 9100
			break
		}
	}
	return net.JoinHostPort(d.IP, fmt.Sprintf("%d", port))
}

// Scan sweeps every private IPv4 /24 the machine is attached to and returns the
// devices listening on a printer port. The context bounds the whole operation;
// callers should pass one with a sensible deadline.
func Scan(ctx context.Context) ([]Device, error) {
	hosts, err := localHosts()
	if err != nil {
		return nil, err
	}

	type hit struct {
		ip   string
		port int
	}

	sem := make(chan struct{}, maxConcurrent)
	hits := make(chan hit)
	var wg sync.WaitGroup

	for _, host := range hosts {
		for _, port := range printerPorts {
			host, port := host, port
			wg.Add(1)
			go func() {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
					defer func() { <-sem }()
				case <-ctx.Done():
					return
				}
				if probe(ctx, host, port) {
					select {
					case hits <- hit{host, port}:
					case <-ctx.Done():
					}
				}
			}()
		}
	}

	go func() {
		wg.Wait()
		close(hits)
	}()

	byIP := map[string]*Device{}
	for h := range hits {
		d, ok := byIP[h.ip]
		if !ok {
			d = &Device{IP: h.ip}
			byIP[h.ip] = d
		}
		d.Ports = append(d.Ports, h.port)
		if h.port == 9100 {
			d.Likely = true
		}
	}

	devices := make([]Device, 0, len(byIP))
	for _, d := range byIP {
		sort.Ints(d.Ports)
		devices = append(devices, *d)
	}
	// Most likely printers first, then by address for stable ordering.
	sort.Slice(devices, func(i, j int) bool {
		if devices[i].Likely != devices[j].Likely {
			return devices[i].Likely
		}
		return devices[i].IP < devices[j].IP
	})
	return devices, nil
}

// probe reports whether a TCP connection to host:port can be opened.
func probe(ctx context.Context, host string, port int) bool {
	d := net.Dialer{Timeout: dialTimeout}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, fmt.Sprintf("%d", port)))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// localHosts returns every usable IPv4 host address across the machine's
// private /24 networks, excluding the machine's own addresses and the network
// and broadcast addresses.
func localHosts() ([]string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	seen := map[string]bool{}
	var hosts []string

	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipnet.IP.To4()
			if ip4 == nil || !ip4.IsPrivate() {
				continue
			}
			// Only sweep /24 (or narrower) to keep the host count bounded.
			ones, bits := ipnet.Mask.Size()
			if bits != 32 || ones < 24 {
				continue
			}
			base := ip4.Mask(ipnet.Mask)
			for i := 1; i < 255; i++ {
				h := net.IPv4(base[0], base[1], base[2], byte(i)).String()
				if h == ip4.String() || seen[h] {
					continue
				}
				seen[h] = true
				hosts = append(hosts, h)
			}
		}
	}
	return hosts, nil
}
