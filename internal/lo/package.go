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

// Package lo lets several LXD containers share their loopback listeners,
// as if they were containers/pods in the same network namespace.
//
// If /peel/lo exists (normally a disk device shared, by the operator,
// across every container that should participate), peel listens on a unix
// socket at /peel/lo/<hostname> and serves two HTTP endpoints on it:
//
//   - GET /watch streams newline-delimited JSON events describing every
//     TCP/UDP port this container is currently listening on, on either a
//     loopback or a wildcard ("all interfaces") address. A fresh snapshot
//     is sent to every new subscriber, followed by incremental add/remove
//     events as sockets come and go, discovered by periodically polling
//     /proc/net/{tcp,tcp6,udp,udp6}.
//   - POST /connect?stack=v4|v6&proto=tcp|udp&port=N dials
//     127.0.0.1:N/[::1]:N (tcp or udp) and, after hijacking the HTTP
//     connection, forwards raw bytes (tcp) or length-prefixed datagrams
//     (udp) between the caller and that local socket.
//
// Symmetrically, this peel instance also watches every other socket
// found in /peel/lo: for every listen a peer reports, it opens a matching
// v4/v6 loopback listener of its own, and forwards anything it receives on
// it to that peer's /connect endpoint. From the point of view of a process
// inside this container, a service listening on loopback in another
// container becomes reachable on localhost, just as if the two containers
// shared a network namespace.
//
// Proxy listeners opened on a peer's behalf are deliberately excluded from
// this instance's own /watch output, so that a listen a container only
// has because it's proxying for a peer is never re-advertised (which would
// otherwise let it leak to, or loop back through, a third container).
package lo
