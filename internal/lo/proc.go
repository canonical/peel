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
	"bufio"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// procNetFiles describes where to find each of the four kernel tables that
// together enumerate every TCP and UDP socket on the system.
var procNetFiles = []struct {
	path  string
	proto Proto
	stack Stack
}{
	{"/proc/net/tcp", TCP, V4},
	{"/proc/net/tcp6", TCP, V6},
	{"/proc/net/udp", UDP, V4},
	{"/proc/net/udp6", UDP, V6},
}

// Socket states, as printed (hex, upper-case) in the "st" column of
// /proc/net/{tcp,udp}*. See include/net/tcp_states.h in the kernel source.
const (
	tcpStateListen = "0A" // TCP_LISTEN

	// UDP sockets have no notion of listening: a bound-but-unconnected
	// socket (one that can receive from any peer) is reported as
	// TCP_CLOSE, which for UDP just means "not connected to a specific
	// peer". That's what "listening" means for the purposes of this
	// package.
	udpStateUnconnected = "07" // TCP_CLOSE
)

// scanProcNet returns every currently loopback- or wildcard-bound
// listening socket on the system, across all four kernel tables.
func scanProcNet() (map[Listen]bool, error) {
	out := make(map[Listen]bool)
	for _, f := range procNetFiles {
		m, err := scanProcFile(f.path, f.proto, f.stack)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", f.path, err)
		}
		for l := range m {
			out[l] = true
		}
	}
	return out, nil
}

// scanProcFile parses a single /proc/net/{tcp,tcp6,udp,udp6}-shaped file.
func scanProcFile(path string, proto Proto, stack Stack) (map[Listen]bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			// e.g. IPv6 disabled entirely: tcp6/udp6 simply don't exist.
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	out := make(map[Listen]bool)
	sc := bufio.NewScanner(f)
	sc.Scan() // discard the header line.
	for sc.Scan() {
		l, ok, err := parseProcNetLine(sc.Text(), proto, stack)
		if err != nil {
			continue // a malformed line should never abort the whole scan.
		}
		if ok {
			out[l] = true
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// parseProcNetLine parses a single data line of /proc/net/{tcp,udp}*,
// returning the Listen it describes and whether it's one this package
// cares about (the right state, bound to a loopback or wildcard address).
func parseProcNetLine(line string, proto Proto, stack Stack) (Listen, bool, error) {
	fields := strings.Fields(line)
	if len(fields) < 4 {
		return Listen{}, false, fmt.Errorf("lo: too few fields: %q", line)
	}

	st := fields[3]
	switch proto {
	case TCP:
		if st != tcpStateListen {
			return Listen{}, false, nil
		}
	case UDP:
		if st != udpStateUnconnected {
			return Listen{}, false, nil
		}
	}

	ipHex, portHex, ok := strings.Cut(fields[1], ":")
	if !ok {
		return Listen{}, false, fmt.Errorf("lo: malformed local_address: %q", fields[1])
	}
	ip, err := parseHexIP(ipHex)
	if err != nil {
		return Listen{}, false, err
	}
	if !ip.IsLoopback() && !ip.IsUnspecified() {
		return Listen{}, false, nil
	}

	port, err := strconv.ParseUint(portHex, 16, 16)
	if err != nil {
		return Listen{}, false, fmt.Errorf("lo: malformed port: %q", portHex)
	}

	return Listen{Stack: stack, Proto: proto, Addr: ip.String(), Port: uint16(port)}, true, nil
}

// parseHexIP decodes an IPv4 or IPv6 address as printed by the kernel in
// /proc/net/{tcp,udp}*: each 32-bit word of the address is written as 8
// hex digits in host (little-endian, on every platform Linux runs peel
// on) byte order, with word order otherwise matching network byte order.
func parseHexIP(s string) (net.IP, error) {
	raw, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("lo: malformed address %q: %w", s, err)
	}
	if len(raw)%4 != 0 {
		return nil, fmt.Errorf("lo: malformed address %q: length %d not a multiple of 4", s, len(raw))
	}

	ip := make(net.IP, len(raw))
	for word := 0; word < len(raw); word += 4 {
		for b := 0; b < 4; b++ {
			ip[word+b] = raw[word+3-b]
		}
	}
	return ip, nil
}
