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
	"encoding/binary"
	"fmt"
	"io"
)

// maxDatagram is larger than any IP packet can ever carry, so it safely
// bounds every UDP datagram framed over a tunnel connection.
const maxDatagram = 65535

// writeFrame writes b to w prefixed with its length, so that the tunnel
// connection's byte stream preserves UDP datagram boundaries.
func writeFrame(w io.Writer, b []byte) error {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(b)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(b) == 0 {
		return nil
	}
	_, err := w.Write(b)
	return err
}

// readFrame reads a single length-prefixed frame written by writeFrame
// from r into buf, and returns the slice of buf it was read into.
func readFrame(r io.Reader, buf []byte) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if int(n) > len(buf) {
		return nil, fmt.Errorf("lo: frame of %d bytes exceeds buffer of %d", n, len(buf))
	}
	if n == 0 {
		return buf[:0], nil
	}
	if _, err := io.ReadFull(r, buf[:n]); err != nil {
		return nil, err
	}
	return buf[:n], nil
}
