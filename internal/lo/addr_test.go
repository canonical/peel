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
	"net"
	"testing"
)

func TestBindAddrPreservesExactAddress(t *testing.T) {
	cases := []struct {
		l    Listen
		want string
	}{
		{Listen{Stack: V4, Addr: "0.0.0.0", Port: 80}, "0.0.0.0:80"},
		{Listen{Stack: V4, Addr: "127.0.0.1", Port: 80}, "127.0.0.1:80"},
		{Listen{Stack: V4, Addr: "127.0.0.2", Port: 80}, "127.0.0.2:80"},
		{Listen{Stack: V6, Addr: "::", Port: 80}, "[::]:80"},
		{Listen{Stack: V6, Addr: "::1", Port: 80}, "[::1]:80"},
	}
	for _, c := range cases {
		if got := bindAddr(c.l); got != c.want {
			t.Errorf("bindAddr(%+v) = %q, want %q", c.l, got, c.want)
		}
	}
}

func TestDialTargetResolvesUnspecifiedToLoopback(t *testing.T) {
	cases := []struct {
		l    Listen
		want string
	}{
		{Listen{Stack: V4, Addr: "0.0.0.0", Port: 80}, "127.0.0.1:80"},
		{Listen{Stack: V6, Addr: "::", Port: 80}, "[::1]:80"},
		// Specific addresses, loopback or not, are dialed exactly.
		{Listen{Stack: V4, Addr: "127.0.0.1", Port: 80}, "127.0.0.1:80"},
		{Listen{Stack: V4, Addr: "127.0.0.2", Port: 80}, "127.0.0.2:80"},
		{Listen{Stack: V6, Addr: "::1", Port: 80}, "[::1]:80"},
	}
	for _, c := range cases {
		if got := dialTarget(c.l); got != c.want {
			t.Errorf("dialTarget(%+v) = %q, want %q", c.l, got, c.want)
		}
	}
}

func TestAddrMatchesStack(t *testing.T) {
	if !addrMatchesStack(V4, "127.0.0.1") {
		t.Error("127.0.0.1 should match v4")
	}
	if addrMatchesStack(V4, "::1") {
		t.Error("::1 should not match v4")
	}
	if !addrMatchesStack(V6, "::1") {
		t.Error("::1 should match v6")
	}
	if addrMatchesStack(V6, "127.0.0.1") {
		t.Error("127.0.0.1 should not match v6")
	}
	if addrMatchesStack(V4, "not-an-ip") {
		t.Error("a malformed address should never match")
	}
}

func TestIsLoopbackOrUnspecified(t *testing.T) {
	for _, addr := range []string{"127.0.0.1", "127.0.0.2", "0.0.0.0", "::1", "::"} {
		if !isLoopbackOrUnspecified(addr) {
			t.Errorf("isLoopbackOrUnspecified(%q) = false, want true", addr)
		}
	}
	for _, addr := range []string{"10.0.0.1", "8.8.8.8", "2001:db8::1", "not-an-ip", ""} {
		if isLoopbackOrUnspecified(addr) {
			t.Errorf("isLoopbackOrUnspecified(%q) = true, want false", addr)
		}
	}
}

func TestIsLocalPeer(t *testing.T) {
	local := []net.Addr{
		&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1234},
		&net.TCPAddr{IP: net.ParseIP("127.0.0.2"), Port: 1234},
		&net.TCPAddr{IP: net.ParseIP("::1"), Port: 1234},
	}
	for _, a := range local {
		if !isLocalPeer(a) {
			t.Errorf("isLocalPeer(%v) = false, want true", a)
		}
	}

	remote := []net.Addr{
		&net.TCPAddr{IP: net.ParseIP("10.0.0.5"), Port: 1234},
		&net.TCPAddr{IP: net.ParseIP("203.0.113.1"), Port: 1234},
	}
	for _, a := range remote {
		if isLocalPeer(a) {
			t.Errorf("isLocalPeer(%v) = true, want false", a)
		}
	}
}
