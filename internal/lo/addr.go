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

package lo

import (
	"fmt"
	"net"
)

// dialNetwork returns the net.Dial/net.Listen network name for
// stack/proto, e.g. "tcp6" or "udp4".
func dialNetwork(stack Stack, proto Proto) string {
	suffix := "4"
	if stack == V6 {
		suffix = "6"
	}
	return string(proto) + suffix
}

// hostPort formats addr (a bare IPv4 or IPv6 literal) and port as a
// dial/listen address, e.g. "127.0.0.1:80" or "[::1]:80".
func hostPort(stack Stack, addr string, port uint16) string {
	if stack == V6 {
		return fmt.Sprintf("[%s]:%d", addr, port)
	}
	return fmt.Sprintf("%s:%d", addr, port)
}

// bindAddr returns the address:port a proxy should listen on for l: the
// exact address it was reported on, so that a specific loopback address
// (e.g. 127.0.0.2) is preserved, and an unspecified ("all interfaces")
// address stays unspecified — other containers listen the same way this
// one does.
func bindAddr(l Listen) string {
	return hostPort(l.Stack, l.Addr, l.Port)
}

// dialTarget returns the address:port to dial in order to reach l on the
// container that actually owns it. An unspecified address can't be dialed
// directly, so it's resolved to the equivalent loopback address, which a
// wildcard listener always also accepts connections on.
func dialTarget(l Listen) string {
	addr := l.Addr
	if isUnspecified(addr) {
		if l.Stack == V6 {
			addr = "::1"
		} else {
			addr = "127.0.0.1"
		}
	}
	return hostPort(l.Stack, addr, l.Port)
}

// isUnspecified reports whether addr (a bare IP literal) is an
// unspecified ("all interfaces") address, e.g. "0.0.0.0" or "::".
func isUnspecified(addr string) bool {
	ip := net.ParseIP(addr)
	return ip != nil && ip.IsUnspecified()
}

// isLoopbackOrUnspecified reports whether addr is a valid IP literal that
// is either loopback or unspecified: the only two kinds of address this
// package ever deals in.
func isLoopbackOrUnspecified(addr string) bool {
	ip := net.ParseIP(addr)
	return ip != nil && (ip.IsLoopback() || ip.IsUnspecified())
}

// addrMatchesStack reports whether addr (a bare IP literal) belongs to
// the given address family.
func addrMatchesStack(stack Stack, addr string) bool {
	ip := net.ParseIP(addr)
	if ip == nil {
		return false
	}
	is4 := ip.To4() != nil
	if stack == V4 {
		return is4
	}
	return !is4
}

// isLocalPeer reports whether remote (the address of a connection or
// packet observed on a wildcard-bound proxy listener) is one this
// container should still forward: a loopback address, or (defensively)
// unspecified. Anything else is a connection arriving from "outside" —
// another interface entirely — which a wildcard bind would otherwise
// accept, but which loopback sharing has no business forwarding on to a
// peer.
func isLocalPeer(remote net.Addr) bool {
	host, _, err := net.SplitHostPort(remote.String())
	if err != nil {
		host = remote.String()
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsUnspecified())
}
