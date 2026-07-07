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
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// connectDialTimeout bounds how long the /connect handler waits to dial
// the requested local target.
const connectDialTimeout = 5 * time.Second

// Server serves /watch and /connect on top of a Watcher.
type Server struct {
	watcher *Watcher
}

// NewServer returns a Server that answers from watcher.
func NewServer(watcher *Watcher) *Server {
	return &Server{watcher: watcher}
}

// Handler returns the http.Handler to serve on the unix socket.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/watch", s.handleWatch)
	mux.HandleFunc("/connect", s.handleConnect)
	return mux
}

// handleWatch streams a snapshot of every currently known listen, followed
// by a "sync" marker, followed by incremental add/remove events for as
// long as the caller stays connected. Each line is a JSON-encoded Event.
func (s *Server) handleWatch(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	events, snapshot, cancel := s.watcher.Subscribe()
	defer cancel()

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)

	enc := json.NewEncoder(w)
	for _, l := range snapshot {
		if err := enc.Encode(Event{Op: "add", Listen: l}); err != nil {
			return
		}
	}
	if err := enc.Encode(Event{Op: "sync"}); err != nil {
		return
	}
	flusher.Flush()

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return
			}
			if err := enc.Encode(ev); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// handleConnect dials the requested local target and, after hijacking the
// HTTP connection, forwards raw bytes (tcp) or length-prefixed datagrams
// (udp) between the caller and it.
func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	l, err := parseListenParams(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		log.Printf("lo: connect: hijack: %v", err)
		return
	}
	defer conn.Close()

	target, err := net.DialTimeout(dialNetwork(l.Stack, l.Proto), dialTarget(l), connectDialTimeout)
	if err != nil {
		fmt.Fprintf(conn, "HTTP/1.1 502 Bad Gateway\r\nConnection: close\r\n\r\n%v\n", err)
		return
	}
	defer target.Close()

	io.WriteString(conn, "HTTP/1.1 200 OK\r\nConnection: close\r\n\r\n")
	tunnel := &bufferedConn{Conn: conn, r: rw.Reader}

	switch l.Proto {
	case TCP:
		pipeTCP(tunnel, target)
	case UDP:
		pipeUDPServer(tunnel, target.(*net.UDPConn))
	}
}

// parseListenParams parses the stack/proto/addr/port query parameters
// shared by /connect requests. addr is restricted to loopback/unspecified
// addresses of the right family: /connect is only ever meant to reach a
// local service, never an arbitrary address.
func parseListenParams(q url.Values) (Listen, error) {
	stack := Stack(q.Get("stack"))
	if stack != V4 && stack != V6 {
		return Listen{}, fmt.Errorf("invalid or missing stack %q", q.Get("stack"))
	}
	proto := Proto(q.Get("proto"))
	if proto != TCP && proto != UDP {
		return Listen{}, fmt.Errorf("invalid or missing proto %q", q.Get("proto"))
	}
	port, err := strconv.ParseUint(q.Get("port"), 10, 16)
	if err != nil {
		return Listen{}, fmt.Errorf("invalid or missing port %q", q.Get("port"))
	}
	addr := q.Get("addr")
	if !isLoopbackOrUnspecified(addr) {
		return Listen{}, fmt.Errorf("invalid or missing addr %q: must be loopback or unspecified", addr)
	}
	if !addrMatchesStack(stack, addr) {
		return Listen{}, fmt.Errorf("addr %q does not match stack %q", addr, stack)
	}
	return Listen{Stack: stack, Proto: proto, Addr: addr, Port: uint16(port)}, nil
}
