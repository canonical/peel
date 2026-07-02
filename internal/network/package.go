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

// Package network brings up the container's network interfaces before peel
// tries to reach a registry.
//
// LXD hands the container a bare network interface (typically a veth
// attached to a bridge): the kernel-level link exists, but nothing has
// requested an address for it, and there is no /etc/resolv.conf. Since
// peel's own rootfs starts out essentially empty, there is no DHCP client,
// SLAAC-aware resolver, or other guest-side network tooling to do that
// either. This package is peel's own minimal IPv4 and IPv6 configurator, so
// that the very first boot (before any image has been unpacked) can
// already reach a registry over the network.
//
// IPv4 addressing, routing and DNS are configured via a DHCPv4
// discover-offer-request-ack handshake. IPv6 addressing is left to the
// kernel's own stateless address autoconfiguration (SLAAC), which requires
// no userspace involvement once the link is up; peel additionally makes a
// best-effort DHCPv6 Information-Request (RFC 8415 Section 5.4.1) to pick
// up IPv6 nameservers, since SLAAC alone (via router advertisements) only
// yields addresses and a default route, not DNS servers.
package network
