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

import "fmt"

// Stack is an IP address family: v4 or v6.
type Stack string

const (
	V4 Stack = "v4"
	V6 Stack = "v6"
)

// Proto is a transport protocol: tcp or udp.
type Proto string

const (
	TCP Proto = "tcp"
	UDP Proto = "udp"
)

// Listen identifies a single listening socket by stack, protocol,
// address and port. Addr is always either loopback or unspecified ("all
// interfaces") — this package never tracks anything else — but which
// exact loopback address (e.g. 127.0.0.1 vs 127.0.0.2) matters just as
// much as which port, so it's part of Listen's identity.
type Listen struct {
	Stack Stack  `json:"stack"`
	Proto Proto  `json:"proto"`
	Addr  string `json:"addr"`
	Port  uint16 `json:"port"`
}

func (l Listen) String() string {
	return fmt.Sprintf("%s/%s/%s:%d", l.Stack, l.Proto, l.Addr, l.Port)
}

// Event is a single line of a /watch response.
//
// Op is one of:
//   - "add": Listen just started (or, for the first events of a new
//     subscription, was already) listening.
//   - "remove": Listen stopped listening.
//   - "sync": sent once, right after the initial snapshot of "add"
//     events; Listen is unset. Marks the point after which the receiver
//     has seen every listen that existed when it subscribed, so it can
//     reconcile any state it kept from a previous, now-stale connection.
type Event struct {
	Op string `json:"op"`
	Listen
}
