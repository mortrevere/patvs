package patvs

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const privateScanTotal = 17891328 // Every IPv4 address in the three RFC1918 ranges.

type privateScanUpdate struct {
	Checked uint64
	Prefix  string
	Phase   string
	Peer    *ReceiverInfo
	Done    bool
}

type privateScanJob struct {
	address netip.Addr
}

// scanPrivate probes only patvs health on TCP 7411. The producer and worker
// pool stay bounded even though RFC1918 contains nearly 18 million hosts.
func scanPrivate(ctx context.Context, port int, report func(privateScanUpdate)) {
	if port == 0 {
		port = 7411
	}
	jobs := make(chan privateScanJob, 256)
	var checked atomic.Uint64
	var position atomic.Value
	position.Store(privateScanUpdate{Phase: "common", Prefix: "10.0.0.0/24"})
	client := &http.Client{
		Timeout: 500 * time.Millisecond,
		Transport: &http.Transport{
			Proxy:             nil,
			DialContext:       (&net.Dialer{Timeout: 350 * time.Millisecond}).DialContext,
			DisableKeepAlives: true,
		},
	}
	var workers sync.WaitGroup
	for range 256 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for job := range jobs {
				endpoint := net.JoinHostPort(job.address.String(), strconv.Itoa(port))
				peer, ok := probeReceiver(ctx, client, endpoint)
				count := checked.Add(1)
				if ok {
					current := position.Load().(privateScanUpdate)
					report(privateScanUpdate{Checked: count, Prefix: current.Prefix, Phase: current.Phase, Peer: &peer})
				}
			}
		}()
	}
	progressDone := make(chan struct{})
	stopProgress := make(chan struct{})
	go func() {
		defer close(progressDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopProgress:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				current := position.Load().(privateScanUpdate)
				current.Checked = checked.Load()
				report(current)
			}
		}
	}()
	rate := time.NewTicker(time.Second / 512)
	defer rate.Stop()
	forEachPrivateAddress(ctx, func(address netip.Addr, prefix, phase string) bool {
		position.Store(privateScanUpdate{Prefix: prefix, Phase: phase})
		select {
		case <-ctx.Done():
			return false
		case <-rate.C:
		}
		select {
		case <-ctx.Done():
			return false
		case jobs <- privateScanJob{address: address}:
			return true
		}
	})
	close(jobs)
	workers.Wait()
	close(stopProgress)
	<-progressDone
	if ctx.Err() == nil {
		current := position.Load().(privateScanUpdate)
		current.Checked = checked.Load()
		current.Done = true
		report(current)
	}
}

func forEachPrivateAddress(ctx context.Context, visit func(netip.Addr, string, string) bool) {
	prefixes, common := privatePrefixes()
	priority := [256]bool{}
	for _, host := range []byte{1, 2, 3, 4, 5, 10, 19, 20, 29, 30, 50, 100, 200, 254} {
		priority[host] = true
	}
	emit := func(prefix netip.Prefix, host byte, phase string) bool {
		if ctx.Err() != nil {
			return false
		}
		address := prefix.Addr().As4()
		address[3] = host
		return visit(netip.AddrFrom4(address), prefix.String(), phase)
	}
	for _, prefix := range prefixes[:common] {
		for host := 1; host <= 254; host++ {
			if !emit(prefix, byte(host), "common") {
				return
			}
		}
	}
	for _, prefix := range prefixes[common:] {
		for host := 1; host <= 254; host++ {
			if priority[host] && !emit(prefix, byte(host), "sample") {
				return
			}
		}
	}
	for _, prefix := range prefixes[common:] {
		for host := 1; host <= 254; host++ {
			if !priority[host] && !emit(prefix, byte(host), "full") {
				return
			}
		}
	}
	for _, prefix := range prefixes {
		if !emit(prefix, 0, "edge") || !emit(prefix, 255, "edge") {
			return
		}
	}
}

func privatePrefixes() ([]netip.Prefix, int) {
	var prefixes []netip.Prefix
	seen := make(map[netip.Prefix]bool)
	add := func(a, b, c byte) {
		prefix := netip.PrefixFrom(netip.AddrFrom4([4]byte{a, b, c, 0}), 24)
		if !seen[prefix] {
			seen[prefix] = true
			prefixes = append(prefixes, prefix)
		}
	}
	for _, parts := range [][3]byte{
		{10, 0, 0}, {192, 168, 1}, {192, 168, 0}, {172, 16, 0},
		{10, 0, 1}, {10, 1, 1}, {192, 168, 2}, {192, 168, 50},
		{192, 168, 178}, {10, 42, 0}, {172, 20, 0},
	} {
		add(parts[0], parts[1], parts[2])
	}
	common := len(prefixes)
	for third := 0; third < 256; third++ {
		add(192, 168, byte(third))
	}
	for second := 0; second < 256; second++ {
		for third := 0; third < 256; third++ {
			add(10, byte(second), byte(third))
		}
	}
	for second := 16; second < 32; second++ {
		for third := 0; third < 256; third++ {
			add(172, byte(second), byte(third))
		}
	}
	return prefixes, common
}

func (c controllerClient) findReceivers(ctx context.Context, wait time.Duration) []ReceiverInfo {
	scanCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	lan := make(chan []ReceiverInfo, 1)
	found := make(chan ReceiverInfo, 32)
	go func() {
		peers, _ := discover(scanCtx, c.cfg, wait-200*time.Millisecond)
		lan <- peers
	}()
	go scanPrivate(scanCtx, apiPort(c.cfg.APIAddr), func(update privateScanUpdate) {
		if update.Peer != nil {
			select {
			case found <- *update.Peer:
			case <-scanCtx.Done():
			}
		}
	})
	known := make(map[string]ReceiverInfo)
	add := func(peer ReceiverInfo) {
		if current, ok := known[peer.ID]; !ok || peer.LastSeen.After(current.LastSeen) {
			known[peer.ID] = peer
		}
	}
	for {
		select {
		case peers := <-lan:
			for _, peer := range peers {
				add(peer)
			}
			lan = nil
		case peer := <-found:
			add(peer)
		case <-scanCtx.Done():
			for {
				select {
				case peer := <-found:
					add(peer)
				default:
					result := make([]ReceiverInfo, 0, len(known))
					for _, peer := range known {
						result = append(result, peer)
					}
					return result
				}
			}
		}
	}
}
