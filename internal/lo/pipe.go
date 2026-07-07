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
	"io"
	"net"
	"sync"
)

// bufferedConn is a net.Conn whose reads are served from r first. Both the
// client side of /connect (a bufio.Reader used to parse the response
// headers) and the server side (the bufio.ReadWriter returned by
// http.Hijacker) may have buffered bytes past the point text parsing
// stopped at; wrapping the connection like this makes sure those bytes
// aren't lost before the raw byte/datagram forwarding takes over.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// pipeTCP copies bytes between a and b in both directions until either
// side is done, then closes both.
func pipeTCP(a, b net.Conn) {
	var once sync.Once
	closeBoth := func() {
		once.Do(func() {
			a.Close()
			b.Close()
		})
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer closeBoth()
		io.Copy(a, b)
	}()
	go func() {
		defer wg.Done()
		defer closeBoth()
		io.Copy(b, a)
	}()
	wg.Wait()
}

// pipeUDPServer forwards datagrams between a length-prefix-framed tunnel
// connection and target, a connected UDP socket, in both directions, until
// either side is done.
func pipeUDPServer(tunnel net.Conn, target *net.UDPConn) {
	var once sync.Once
	closeBoth := func() {
		once.Do(func() {
			tunnel.Close()
			target.Close()
		})
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer closeBoth()
		buf := make([]byte, maxDatagram)
		for {
			n, err := target.Read(buf)
			if err != nil {
				return
			}
			if err := writeFrame(tunnel, buf[:n]); err != nil {
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		defer closeBoth()
		buf := make([]byte, maxDatagram)
		for {
			frame, err := readFrame(tunnel, buf)
			if err != nil {
				return
			}
			if _, err := target.Write(frame); err != nil {
				return
			}
		}
	}()
	wg.Wait()
}
