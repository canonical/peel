// Copyright (c) 2026 Canonical Ltd
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License version 3 as
// published by the Free Software Foundation.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package network

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4/nclient4"
	"github.com/insomniacslk/dhcp/dhcpv6"
	"github.com/insomniacslk/dhcp/dhcpv6/nclient6"
	"github.com/insomniacslk/dhcp/iana"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// DefaultTimeout is how long Configure waits for IPv4/IPv6 configuration on
// each interface before giving up on it.
const DefaultTimeout = 15 * time.Second

// Configure brings up every non-loopback network interface and configures
// it for both IPv4 (via DHCPv4) and IPv6 (via SLAAC, plus a best-effort
// DHCPv6 Information-Request for nameservers), then writes every
// nameserver it obtained to <root>/etc/resolv.conf.
//
// It is best-effort: failures on any individual interface or protocol (no
// DHCP server, timeout, etc.) are logged but never prevent boot, since peel
// has no way to know whether the entrypoint it's about to start needs a
// network at all. Interfaces (and IPv4/IPv6 on each of them) are
// configured concurrently so that one slow/absent server doesn't delay the
// others.
//
// The returned nameservers can be passed to WriteResolvConf again later
// (e.g. after unpacking a new image, which may have reset /etc) without
// repeating any of the above.
func Configure(ctx context.Context, root string, timeout time.Duration) []net.IP {
	links, err := netlink.LinkList()
	if err != nil {
		log.Printf("network: listing interfaces: %v", err)
		return nil
	}

	var (
		mu          sync.Mutex
		nameservers []net.IP
		wg          sync.WaitGroup
	)
	for _, link := range links {
		name := link.Attrs().Name
		if name == "lo" {
			continue
		}
		wg.Add(1)
		go func(link netlink.Link, name string) {
			defer wg.Done()
			dns := configureLink(ctx, link, timeout)
			mu.Lock()
			nameservers = append(nameservers, dns...)
			mu.Unlock()
		}(link, name)
	}
	wg.Wait()

	nameservers = dedup(nameservers)
	if err := WriteResolvConf(root, nameservers); err != nil {
		log.Printf("network: writing resolv.conf: %v", err)
	}
	return nameservers
}

// configureLink brings up a single interface and configures it for both
// IPv4 and IPv6, concurrently. Each protocol logs its own failures; neither
// blocks the other.
func configureLink(ctx context.Context, link netlink.Link, timeout time.Duration) []net.IP {
	name := link.Attrs().Name

	if err := netlink.LinkSetUp(link); err != nil {
		log.Printf("network: %s: bringing up interface: %v", name, err)
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var (
		mu  sync.Mutex
		dns []net.IP
		wg  sync.WaitGroup
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		v4, err := configureLinkV4(ctx, link)
		if err != nil {
			log.Printf("network: %s: dhcpv4: %v", name, err)
			return
		}
		mu.Lock()
		dns = append(dns, v4...)
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		v6, err := configureLinkV6(ctx, link)
		if err != nil {
			log.Printf("network: %s: dhcpv6: %v", name, err)
			return
		}
		mu.Lock()
		dns = append(dns, v6...)
		mu.Unlock()
	}()
	wg.Wait()

	return dns
}

// configureLinkV4 runs the DHCPv4 discover-offer-request-ack handshake
// against link, and applies the offered address and default route.
func configureLinkV4(ctx context.Context, link netlink.Link) ([]net.IP, error) {
	name := link.Attrs().Name

	client, err := nclient4.New(name, nclient4.WithTimeout(4*time.Second), nclient4.WithRetry(3))
	if err != nil {
		return nil, fmt.Errorf("creating client: %w", err)
	}
	defer client.Close()

	lease, err := client.Request(ctx)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	ack := lease.ACK

	mask := ack.SubnetMask()
	if len(mask) == 0 {
		mask = ack.YourIPAddr.DefaultMask()
	}
	addr := &netlink.Addr{IPNet: &net.IPNet{IP: ack.YourIPAddr, Mask: mask}}
	if err := netlink.AddrReplace(link, addr); err != nil {
		return nil, fmt.Errorf("setting address %s: %w", addr, err)
	}
	log.Printf("network: %s: %s", name, addr)

	for _, gw := range ack.Router() {
		route := &netlink.Route{LinkIndex: link.Attrs().Index, Gw: gw}
		if err := netlink.RouteReplace(route); err != nil {
			log.Printf("network: %s: adding default route via %s: %v", name, gw, err)
			continue
		}
		log.Printf("network: %s: default route via %s", name, gw)
		break // one default route is enough.
	}

	return ack.DNS(), nil
}

// configureLinkV6 fetches IPv6 nameservers for link via a DHCPv6
// Information-Request. It never touches addresses or routes: those are
// left entirely to the kernel's own SLAAC handling of router
// advertisements, which needs no userspace help once the link is up.
func configureLinkV6(ctx context.Context, link netlink.Link) ([]net.IP, error) {
	name := link.Attrs().Name

	// The DHCPv6 client binds a UDP socket to the interface's link-local
	// address, which the kernel assigns as soon as the link comes up but
	// keeps "tentative" (unusable) until duplicate address detection
	// finishes a moment later. Binding too early fails outright, so wait
	// for a usable link-local address first.
	if err := waitForUsableLinkLocal(ctx, link); err != nil {
		return nil, fmt.Errorf("waiting for a link-local address: %w", err)
	}

	client, err := nclient6.New(name, nclient6.WithTimeout(4*time.Second), nclient6.WithRetry(3))
	if err != nil {
		return nil, fmt.Errorf("creating client: %w", err)
	}
	defer client.Close()

	duid := &dhcpv6.DUIDLL{HWType: iana.HWTypeEthernet, LinkLayerAddr: client.InterfaceAddr()}
	msg, err := dhcpv6.NewMessage(
		dhcpv6.WithClientID(duid),
		dhcpv6.WithRequestedOptions(dhcpv6.OptionDNSRecursiveNameServer),
	)
	if err != nil {
		return nil, fmt.Errorf("building information-request: %w", err)
	}
	msg.MessageType = dhcpv6.MessageTypeInformationRequest

	reply, err := client.SendAndRead(ctx, client.RemoteAddr(), msg, nclient6.IsMessageType(dhcpv6.MessageTypeReply))
	if err != nil {
		return nil, fmt.Errorf("information-request: %w", err)
	}

	dns := reply.Options.DNS()
	if len(dns) > 0 {
		log.Printf("network: %s: dns via dhcpv6: %v", name, dns)
	}
	return dns, nil
}

// waitForUsableLinkLocal polls until link has a link-local IPv6 address
// that has finished duplicate address detection, or ctx is done.
func waitForUsableLinkLocal(ctx context.Context, link netlink.Link) error {
	for {
		addrs, err := netlink.AddrList(link, netlink.FAMILY_V6)
		if err != nil {
			return err
		}
		for _, a := range addrs {
			if a.IP.IsLinkLocalUnicast() && a.Flags&unix.IFA_F_TENTATIVE == 0 {
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// WriteResolvConf (re)writes <root>/etc/resolv.conf with the given
// nameservers (IPv4 and/or IPv6). It does nothing if nameservers is empty,
// so that a failed handshake never clobbers a resolv.conf that was already
// there (e.g. one shipped by the image itself).
func WriteResolvConf(root string, nameservers []net.IP) error {
	if len(nameservers) == 0 {
		return nil
	}

	path := filepath.Join(root, "etc", "resolv.conf")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}

	var buf []byte
	buf = append(buf, "# written by peel\n"...)
	for _, ns := range nameservers {
		buf = append(buf, "nameserver "+ns.String()+"\n"...)
	}

	if err := os.WriteFile(path, buf, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// dedup returns ips with duplicates removed, in a stable sorted order.
func dedup(ips []net.IP) []net.IP {
	seen := make(map[string]bool, len(ips))
	out := make([]net.IP, 0, len(ips))
	for _, ip := range ips {
		s := ip.String()
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, ip)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}
