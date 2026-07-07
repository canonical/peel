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
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// dialConnect dials sockPath (a peer's /peel/lo unix socket) and issues a
// /connect request for l, returning a net.Conn ready for raw byte (tcp) or
// framed datagram (udp) forwarding once the peer accepts it.
func dialConnect(ctx context.Context, sockPath string, l Listen) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", sockPath)
	if err != nil {
		return nil, err
	}

	q := url.Values{}
	q.Set("stack", string(l.Stack))
	q.Set("proto", string(l.Proto))
	q.Set("addr", l.Addr)
	q.Set("port", strconv.Itoa(int(l.Port)))
	req := fmt.Sprintf("POST /connect?%s HTTP/1.1\r\nHost: peel\r\n\r\n", q.Encode())
	if _, err := io.WriteString(conn, req); err != nil {
		conn.Close()
		return nil, fmt.Errorf("lo: sending connect request: %w", err)
	}

	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("lo: reading connect response: %w", err)
	}
	if fields := strings.Fields(status); len(fields) < 2 || fields[1] != "200" {
		conn.Close()
		return nil, fmt.Errorf("lo: peer refused connect: %s", strings.TrimSpace(status))
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("lo: reading connect response headers: %w", err)
		}
		if strings.TrimSpace(line) == "" {
			break
		}
	}

	return &bufferedConn{Conn: conn, r: br}, nil
}
