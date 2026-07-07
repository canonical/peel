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
	"context"
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// udpSessionIdle is how long a UDP "session" (see Proxy.udpLoop) may go
// without any traffic before it's torn down. UDP has no equivalent of a
// TCP FIN to signal the end of a conversation, so idle sessions have to be
// reaped on a timer instead.
const udpSessionIdle = 2 * time.Minute

// udpSessionSweep is how often idle UDP sessions are reaped.
const udpSessionSweep = 30 * time.Second

// Proxy is a local v4 or v6 loopback listener, opened on behalf of a
// listen a peer reported over /watch, that forwards everything it
// receives to that peer's /connect endpoint.
type Proxy struct {
	watcher *Watcher
	listen  Listen

	ln net.Listener   // set for TCP.
	pc net.PacketConn // set for UDP.

	cancel context.CancelFunc
}

// StartProxy opens a local v4/v6 loopback listener matching l and forwards
// everything it receives to peerSock's /connect endpoint. l is excluded
// from watcher's own /watch output for as long as the returned Proxy is
// running.
func StartProxy(ctx context.Context, watcher *Watcher, peerSock string, l Listen) (*Proxy, error) {
	watcher.Exclude(l)

	pctx, cancel := context.WithCancel(ctx)
	p := &Proxy{watcher: watcher, listen: l, cancel: cancel}

	fail := func(err error) (*Proxy, error) {
		cancel()
		watcher.Unexclude(l)
		return nil, err
	}

	addr := bindAddr(l)
	switch l.Proto {
	case TCP:
		ln, err := net.Listen(dialNetwork(l.Stack, TCP), addr)
		if err != nil {
			return fail(err)
		}
		p.ln = ln
		go p.acceptLoop(pctx, peerSock)
	case UDP:
		pc, err := net.ListenPacket(dialNetwork(l.Stack, UDP), addr)
		if err != nil {
			return fail(err)
		}
		p.pc = pc
		go p.udpLoop(pctx, peerSock)
	default:
		return fail(fmt.Errorf("lo: unknown proto %q", l.Proto))
	}
	return p, nil
}

// rejectsRemote reports whether remote (the source of a connection or
// packet observed on p's listener) should be rejected: only possible when
// p.listen is on an unspecified ("all interfaces") address, since binding
// that same way locally means accepting traffic from this container's
// other, real network interfaces too — which loopback sharing has no
// business forwarding on to a peer. remote must be loopback or,
// defensively, unspecified to be accepted.
func (p *Proxy) rejectsRemote(remote net.Addr) bool {
	return isUnspecified(p.listen.Addr) && !isLocalPeer(remote)
}

// Close stops the proxy's listener and un-excludes its Listen.
func (p *Proxy) Close() {
	p.cancel()
	if p.ln != nil {
		p.ln.Close()
	}
	if p.pc != nil {
		p.pc.Close()
	}
	p.watcher.Unexclude(p.listen)
}

// acceptLoop accepts TCP connections on p.ln for as long as ctx is live,
// forwarding each to a fresh /connect stream against peerSock.
//
// When p.listen is on an unspecified ("all interfaces") address, p.ln is
// itself bound the same way, so that local processes reaching it via any
// local address behave exactly as they would against the original
// listener. But that also means it's reachable from this container's
// other, real network interfaces — which loopback sharing has no
// business exposing to. Connections whose remote address isn't loopback
// (or, defensively, unspecified) are rejected outright.
func (p *Proxy) acceptLoop(ctx context.Context, peerSock string) {
	for {
		conn, err := p.ln.Accept()
		if err != nil {
			return
		}
		if p.rejectsRemote(conn.RemoteAddr()) {
			log.Printf("lo: rejecting non-local connection from %s to wildcard-bound %s", conn.RemoteAddr(), p.listen)
			conn.Close()
			continue
		}
		go func() {
			defer conn.Close()
			tunnel, err := dialConnect(ctx, peerSock, p.listen)
			if err != nil {
				log.Printf("lo: %s: connecting for %s: %v", peerSock, p.listen, err)
				return
			}
			defer tunnel.Close()
			pipeTCP(conn, tunnel)
		}()
	}
}

// udpSession is a single client's ongoing "conversation" with the proxied
// UDP service, tunnelled over one /connect stream against the peer that
// actually owns it.
type udpSession struct {
	tunnel     net.Conn
	lastActive atomic.Int64 // UnixNano
}

func (s *udpSession) touch() { s.lastActive.Store(time.Now().UnixNano()) }

// udpLoop reads datagrams from p.pc, mapping each distinct source address
// onto its own udpSession (and thus its own /connect stream against
// peerSock), for as long as ctx is live.
func (p *Proxy) udpLoop(ctx context.Context, peerSock string) {
	var mu sync.Mutex
	sessions := make(map[string]*udpSession)

	go p.reapIdleSessions(ctx, &mu, sessions)

	buf := make([]byte, maxDatagram)
	for {
		n, addr, err := p.pc.ReadFrom(buf)
		if err != nil {
			mu.Lock()
			for _, sess := range sessions {
				sess.tunnel.Close()
			}
			mu.Unlock()
			return
		}
		// See rejectsRemote for why a wildcard-bound listener still
		// rejects packets from outside this container.
		if p.rejectsRemote(addr) {
			continue
		}
		payload := append([]byte(nil), buf[:n]...)
		key := addr.String()

		mu.Lock()
		sess, ok := sessions[key]
		if !ok {
			tunnel, err := dialConnect(ctx, peerSock, p.listen)
			if err != nil {
				mu.Unlock()
				log.Printf("lo: %s: connecting for %s: %v", peerSock, p.listen, err)
				continue
			}
			sess = &udpSession{tunnel: tunnel}
			sessions[key] = sess
			go p.udpReturnLoop(addr, key, sess, &mu, sessions)
		}
		sess.touch()
		mu.Unlock()

		if err := writeFrame(sess.tunnel, payload); err != nil {
			mu.Lock()
			if sessions[key] == sess {
				delete(sessions, key)
			}
			mu.Unlock()
			sess.tunnel.Close()
		}
	}
}

// udpReturnLoop reads framed datagrams sent back over sess's tunnel and
// writes them back to addr on p.pc, until the tunnel errors or is closed.
func (p *Proxy) udpReturnLoop(addr net.Addr, key string, sess *udpSession, mu *sync.Mutex, sessions map[string]*udpSession) {
	buf := make([]byte, maxDatagram)
	for {
		frame, err := readFrame(sess.tunnel, buf)
		if err != nil {
			break
		}
		sess.touch()
		if _, err := p.pc.WriteTo(frame, addr); err != nil {
			break
		}
	}

	mu.Lock()
	if sessions[key] == sess {
		delete(sessions, key)
	}
	mu.Unlock()
	sess.tunnel.Close()
}

// reapIdleSessions periodically closes (and forgets) any session that's
// had no traffic in either direction for udpSessionIdle.
func (p *Proxy) reapIdleSessions(ctx context.Context, mu *sync.Mutex, sessions map[string]*udpSession) {
	ticker := time.NewTicker(udpSessionSweep)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-udpSessionIdle).UnixNano()
			mu.Lock()
			for key, sess := range sessions {
				if sess.lastActive.Load() < cutoff {
					delete(sessions, key)
					sess.tunnel.Close()
				}
			}
			mu.Unlock()
		}
	}
}
