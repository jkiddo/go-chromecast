package simulator_test

import (
	"context"
	"fmt"
	"github.com/vishen/go-chromecast/dns"
	"github.com/vishen/go-chromecast/simulator"
	"net"
	"os"
	"testing"
	"time"
)

// Opt-in: regular test runs remain isolated from LAN multicast.
func TestMDNSDiscovery(t *testing.T) {
	ip := os.Getenv("SIMULATOR_MDNS_IP")
	if ip == "" {
		t.Skip("set SIMULATOR_MDNS_IP to this machine's LAN IPv4 address")
	}
	name := fmt.Sprintf("Simulator Test %d", time.Now().UnixNano())
	r, err := simulator.New(simulator.Config{Listen: "0.0.0.0:0", Name: name, AdvertiseIP: ip})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	var iface *net.Interface
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range interfaces {
		addresses, _ := candidate.Addrs()
		for _, address := range addresses {
			local, _, _ := net.ParseCIDR(address.String())
			if local.Equal(net.ParseIP(ip)) {
				copy := candidate
				iface = &copy
			}
		}
	}
	if iface == nil {
		t.Fatal("no interface for simulator IP")
	}
	entries, err := dns.DiscoverCastDNSEntries(ctx, iface)
	if err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case entry, ok := <-entries:
			if !ok {
				t.Fatal("discovery ended without simulator")
			}
			if entry.GetName() == name && entry.Port == r.Port() && entry.GetAddr() == ip {
				return
			}
		case <-ctx.Done():
			t.Fatal("simulator was not discovered")
		}
	}
}
