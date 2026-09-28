package patvs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const discoveryMagic = "patvs-discovery"

func startDiscoveryResponder(ctx context.Context, cfg Config, identity Identity) error {
	port := apiPort(cfg.APIAddr)
	if port == 0 {
		return fmt.Errorf("receiver listen address %q has no TCP port", cfg.APIAddr)
	}
	packet := discoveryPacket{Magic: discoveryMagic, Version: protocolVersion, Kind: "receiver", ID: identity.ID, Name: identity.Name, API: port}
	listeners, err := discoveryListeners(cfg.DiscoveryPort)
	if err != nil {
		return err
	}
	for _, conn := range listeners {
		go serveDiscovery(ctx, conn, packet)
	}
	return nil
}

func discoveryListeners(port int) ([]*net.UDPConn, error) {
	var listeners []*net.UDPConn
	v4, err := net.ListenUDP("udp4", &net.UDPAddr{Port: port})
	if err != nil {
		return nil, fmt.Errorf("listen for IPv4 discovery: %w", err)
	}
	listeners = append(listeners, v4)
	interfaces, _ := net.Interfaces()
	group := &net.UDPAddr{IP: net.ParseIP("ff02::114"), Port: port}
	for i := range interfaces {
		if interfaces[i].Flags&(net.FlagUp|net.FlagMulticast) != net.FlagUp|net.FlagMulticast || interfaces[i].Flags&net.FlagLoopback != 0 {
			continue
		}
		conn, listenErr := net.ListenMulticastUDP("udp6", &interfaces[i], group)
		if listenErr != nil {
			slog.Debug("IPv6 discovery unavailable", "interface", interfaces[i].Name, "error", listenErr)
			continue
		}
		listeners = append(listeners, conn)
	}
	return listeners, nil
}

func serveDiscovery(ctx context.Context, conn *net.UDPConn, response discoveryPacket) {
	defer conn.Close()
	data, _ := json.Marshal(response)
	buffer := make([]byte, 1024)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		n, source, err := conn.ReadFromUDP(buffer)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				continue
			}
			slog.Debug("discovery read failed", "error", err)
			continue
		}
		var probe discoveryPacket
		if json.Unmarshal(buffer[:n], &probe) != nil || probe.Magic != discoveryMagic || probe.Version != protocolVersion || probe.Kind != "probe" {
			continue
		}
		if _, err := conn.WriteToUDP(data, source); err != nil {
			slog.Debug("discovery response failed", "to", source, "error", err)
		}
	}
}

func discover(ctx context.Context, cfg Config, wait time.Duration) ([]ReceiverInfo, error) {
	discoveryCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	results := make(chan ReceiverInfo, 32)
	var wg sync.WaitGroup
	wg.Add(4)
	go func() { defer wg.Done(); discoverUDP4(discoveryCtx, cfg.DiscoveryPort, results) }()
	go func() { defer wg.Done(); discoverUDP6(discoveryCtx, cfg.DiscoveryPort, results) }()
	go func() { defer wg.Done(); discoverSeeds(discoveryCtx, cfg.Seeds, results) }()
	go func() {
		defer wg.Done()
		select {
		case <-discoveryCtx.Done():
			return
		case <-time.After(1200 * time.Millisecond):
			discoverDirect(discoveryCtx, apiPort(cfg.APIAddr), results)
		}
	}()
	go func() { wg.Wait(); close(results) }()

	peers := make(map[string]ReceiverInfo)
	for peer := range results {
		if old, ok := peers[peer.ID]; !ok || peer.LastSeen.After(old.LastSeen) {
			peers[peer.ID] = peer
		}
	}
	answer := make([]ReceiverInfo, 0, len(peers))
	for _, peer := range peers {
		answer = append(answer, peer)
	}
	return answer, nil
}

func discoverDirect(ctx context.Context, port int, results chan<- ReceiverInfo) {
	if port == 0 {
		port = 7411
	}
	jobs := make(chan netip.Addr)
	var workers sync.WaitGroup
	client := &http.Client{Timeout: 300 * time.Millisecond}
	for range 64 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for address := range jobs {
				endpoint := net.JoinHostPort(address.String(), strconv.Itoa(port))
				if peer, ok := probeReceiver(ctx, client, endpoint); ok {
					select {
					case results <- peer:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}
	ticker := time.NewTicker(time.Second / 128)
	defer ticker.Stop()
	for _, candidate := range directCandidates() {
		select {
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return
		case <-ticker.C:
		}
		select {
		case jobs <- candidate:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return
		}
	}
	close(jobs)
	workers.Wait()
}

func directCandidates() []netip.Addr {
	interfaces, _ := net.Interfaces()
	seen := make(map[netip.Addr]bool)
	var result []netip.Addr
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, _ := iface.Addrs()
		for _, raw := range addresses {
			prefix, err := netip.ParsePrefix(raw.String())
			if err != nil || !prefix.Addr().Is4() {
				continue
			}
			local := prefix.Addr()
			scanPrefix := netip.PrefixFrom(local, 24).Masked()
			if prefix.Bits() > 24 {
				scanPrefix = prefix.Masked()
			}
			for address := scanPrefix.Addr().Next(); scanPrefix.Contains(address); address = address.Next() {
				next := address.Next()
				if next.IsValid() && scanPrefix.Contains(next) && address != local && !seen[address] {
					seen[address] = true
					result = append(result, address)
				}
			}
		}
	}
	return result
}

func discoverUDP4(ctx context.Context, port int, results chan<- ReceiverInfo) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		slog.Debug("IPv4 discovery disabled", "error", err)
		return
	}
	defer conn.Close()
	_ = setBroadcast(conn)
	go readDiscovery(ctx, conn, results)

	targets := []*net.UDPAddr{{IP: net.IPv4(127, 0, 0, 1), Port: port}, {IP: net.IPv4bcast, Port: port}}
	targets = append(targets, interfaceBroadcasts(port)...)
	sendProbes(ctx, conn, targets)
}

func discoverUDP6(ctx context.Context, port int, results chan<- ReceiverInfo) {
	conn, err := net.ListenUDP("udp6", &net.UDPAddr{})
	if err != nil {
		slog.Debug("IPv6 discovery disabled", "error", err)
		return
	}
	defer conn.Close()
	go readDiscovery(ctx, conn, results)
	interfaces, _ := net.Interfaces()
	var targets []*net.UDPAddr
	for _, iface := range interfaces {
		if iface.Flags&(net.FlagUp|net.FlagMulticast) == net.FlagUp|net.FlagMulticast && iface.Flags&net.FlagLoopback == 0 {
			targets = append(targets, &net.UDPAddr{IP: net.ParseIP("ff02::114"), Port: port, Zone: iface.Name})
		}
	}
	sendProbes(ctx, conn, targets)
}

func sendProbes(ctx context.Context, conn *net.UDPConn, targets []*net.UDPAddr) {
	probe, _ := json.Marshal(discoveryPacket{Magic: discoveryMagic, Version: protocolVersion, Kind: "probe"})
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		for _, target := range targets {
			_, _ = conn.WriteToUDP(probe, target)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func readDiscovery(ctx context.Context, conn *net.UDPConn, results chan<- ReceiverInfo) {
	buffer := make([]byte, 1024)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, source, err := conn.ReadFromUDP(buffer)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		var packet discoveryPacket
		if json.Unmarshal(buffer[:n], &packet) != nil || packet.Magic != discoveryMagic || packet.Version != protocolVersion || packet.Kind != "receiver" || packet.ID == "" || packet.API == 0 {
			continue
		}
		host := source.IP.String()
		if source.Zone != "" {
			host += "%" + source.Zone
		}
		select {
		case results <- ReceiverInfo{ID: packet.ID, Name: packet.Name, Address: net.JoinHostPort(host, strconv.Itoa(packet.API)), LastSeen: time.Now()}:
		case <-ctx.Done():
			return
		}
	}
}

func interfaceBroadcasts(port int) []*net.UDPAddr {
	interfaces, _ := net.Interfaces()
	var targets []*net.UDPAddr
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, _ := iface.Addrs()
		for _, address := range addresses {
			prefix, err := netip.ParsePrefix(address.String())
			if err != nil || !prefix.Addr().Is4() {
				continue
			}
			bits := prefix.Bits()
			if bits < 8 || bits > 30 {
				continue
			}
			addr := prefix.Addr().As4()
			mask := uint32(0xffffffff) << (32 - bits)
			value := uint32(addr[0])<<24 | uint32(addr[1])<<16 | uint32(addr[2])<<8 | uint32(addr[3])
			broadcast := value | ^mask
			targets = append(targets, &net.UDPAddr{IP: net.IPv4(byte(broadcast>>24), byte(broadcast>>16), byte(broadcast>>8), byte(broadcast)), Port: port})
		}
	}
	return targets
}

func setBroadcast(conn *net.UDPConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var socketErr error
	err = raw.Control(func(fd uintptr) {
		socketErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
	})
	if err != nil {
		return err
	}
	return socketErr
}

func discoverSeeds(ctx context.Context, seeds []string, results chan<- ReceiverInfo) {
	client := http.Client{Timeout: 800 * time.Millisecond}
	for _, seed := range seeds {
		if !strings.Contains(seed, ":") {
			seed = net.JoinHostPort(seed, "7411")
		}
		if peer, ok := probeReceiver(ctx, &client, seed); ok {
			select {
			case results <- peer:
			case <-ctx.Done():
				return
			}
		}
	}
}

func probeReceiver(ctx context.Context, client *http.Client, endpoint string) (ReceiverInfo, bool) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+endpoint+"/v1/health", nil)
	if err != nil {
		return ReceiverInfo{}, false
	}
	response, err := client.Do(request)
	if err != nil {
		return ReceiverInfo{}, false
	}
	defer response.Body.Close()
	var identity Identity
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&identity) != nil || identity.ID == "" {
		return ReceiverInfo{}, false
	}
	return ReceiverInfo{ID: identity.ID, Name: identity.Name, Address: endpoint, LastSeen: time.Now()}, true
}

func apiPort(address string) int {
	_, portText, err := net.SplitHostPort(address)
	if err != nil {
		return 0
	}
	port, _ := strconv.Atoi(portText)
	return port
}
