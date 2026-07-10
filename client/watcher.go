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

package client

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/canonical/peel/internal/lo"
)

const watchBufSize = 64 * 1024

// portKey is the external identity of a port: (stack, port-number). We
// intentionally omit protocol and exact address so that a TCP and UDP
// listener on the same port number are counted as a single "open"/"close"
// event, matching the change-string format which carries only stack and port.
type portKey struct {
	stack lo.Stack
	port  uint16
}

// containerWatcher watches a single container's /watch HTTP stream and
// translates lo.Events into change strings that are emitted on the parent
// client's outCh.
type containerWatcher struct {
	name     string
	sockPath string
	client   *portsClient

	// ctx/cancel let the parent stop this watcher independently of others.
	ctx    context.Context
	cancel context.CancelFunc

	mu sync.Mutex
	// portCounts maps each (stack, port) key to the number of individual
	// lo.Listen entries (distinct protocol/address combinations) currently
	// reporting that port as open. An "open" event is emitted when the
	// count rises from 0 to 1; a "close" event when it drops back to 0.
	portCounts map[portKey]int
}

func newContainerWatcher(name, sockPath string, c *portsClient) *containerWatcher {
	ctx, cancel := context.WithCancel(c.ctx)
	return &containerWatcher{
		name:       name,
		sockPath:   sockPath,
		client:     c,
		ctx:        ctx,
		cancel:     cancel,
		portCounts: make(map[portKey]int),
	}
}

// run watches the container's /watch endpoint until the context is done,
// reconnecting after each failure with a configurable delay.
//
// startupCallback, if non-nil, is called exactly once — after the first
// connection's "sync" marker arrives — with the initial set of open port
// strings. On connection failure before sync it is called with nil. It is
// not called on subsequent reconnections; those operate in reconciliation
// mode instead.
func (cw *containerWatcher) run(startupCallback func([]string)) {
	defer func() {
		// Emit close events for every port still tracked as open so the
		// consumer's view stays consistent when a container is removed or
		// the client shuts down.
		cw.mu.Lock()
		counts := cw.portCounts
		cw.portCounts = make(map[portKey]int)
		cw.mu.Unlock()

		if len(counts) > 0 {
			changes := make([]string, 0, len(counts))
			for k := range counts {
				changes = append(changes, makeChange("close", k.stack, cw.name, k.port))
			}
			cw.client.emit(changes)
		}
	}()

	cb := startupCallback
	for cw.ctx.Err() == nil {
		if err := cw.watchOnce(cb); err != nil && cw.ctx.Err() == nil {
			log.Printf("portsclient: %s: watch: %v", cw.name, err)
		}
		// Startup callback is consumed (or skipped on error) after the first
		// attempt; reconnects always use reconciliation mode.
		cb = nil

		select {
		case <-cw.ctx.Done():
			return
		case <-time.After(cw.client.cfg.ReconnectDelay):
		}
	}
}

// watchOnce dials the container's Unix socket, issues a single GET /watch
// request, and processes events until the stream closes or the context is
// cancelled.
//
// syncCallback is the startup callback described on run(). When non-nil
// (first connection), it is called at the "sync" marker with the initial
// open port strings; on reconnections it is nil and reconciliation diffs are
// emitted directly.
//
// The callback is guaranteed to be called exactly once before watchOnce
// returns, even on early errors (with nil in that case), so the startup
// coordinator always gets its signal.
func (cw *containerWatcher) watchOnce(syncCallback func([]string)) error {
	hc := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", cw.sockPath)
			},
		},
	}
	defer hc.CloseIdleConnections()

	req, err := http.NewRequestWithContext(cw.ctx, http.MethodGet, "http://peel/watch", nil)
	if err != nil {
		if syncCallback != nil {
			syncCallback(nil)
		}
		return err
	}

	resp, err := hc.Do(req)
	if err != nil {
		if syncCallback != nil {
			syncCallback(nil)
		}
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if syncCallback != nil {
			syncCallback(nil)
		}
		return fmt.Errorf("unexpected status %s", resp.Status)
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, watchBufSize), watchBufSize)

	// incoming accumulates every lo.Listen seen before the "sync" marker.
	incoming := make(map[lo.Listen]bool)
	synced := false

	// cbFired ensures syncCallback is called at most once even if we exit
	// early; the defer below fires it with nil if it hasn't been called yet.
	cbFired := false
	ensureCB := func(ports []string) {
		if !cbFired {
			cbFired = true
			if syncCallback != nil {
				syncCallback(ports)
			}
		}
	}
	defer ensureCB(nil)

	for sc.Scan() {
		var ev lo.Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			log.Printf("portsclient: %s: malformed watch event: %v", cw.name, err)
			continue
		}

		// Skip listens on loopback addresses (127.x.x.x, ::1): those are
		// local to the container. Wildcard (0.0.0.0, ::) and specific
		// non-loopback addresses both pass through.
		// "sync" events carry no Listen, so they must always pass through.
		if ev.Op != "sync" && !wantListen(ev.Listen) {
			continue
		}

		k := portKey{stack: ev.Stack, port: ev.Port}

		switch ev.Op {
		case "add":
			if !synced {
				// Pre-sync: just accumulate for the upcoming snapshot.
				incoming[ev.Listen] = true
			} else {
				// Post-sync: update refcount and emit if newly open.
				cw.mu.Lock()
				prev := cw.portCounts[k]
				cw.portCounts[k]++
				cw.mu.Unlock()
				if prev == 0 {
					cw.client.emit([]string{makeChange("open", ev.Stack, cw.name, ev.Port)})
				}
			}

		case "remove":
			if !synced {
				delete(incoming, ev.Listen)
			} else {
				cw.mu.Lock()
				cw.portCounts[k]--
				n := cw.portCounts[k]
				if n <= 0 {
					delete(cw.portCounts, k)
					n = 0
				}
				cw.mu.Unlock()
				if n == 0 {
					cw.client.emit([]string{makeChange("close", ev.Stack, cw.name, ev.Port)})
				}
			}

		case "sync":
			synced = true

			// Derive reference counts from the snapshot.
			newCounts := make(map[portKey]int, len(incoming))
			for l := range incoming {
				newCounts[portKey{stack: l.Stack, port: l.Port}]++
			}

			cw.mu.Lock()
			oldCounts := cw.portCounts
			cw.portCounts = newCounts
			cw.mu.Unlock()

			if syncCallback != nil {
				// Startup mode: hand the initial open-port strings to the
				// coordinator; do not emit them directly.
				ports := make([]string, 0, len(newCounts))
				for k := range newCounts {
					ports = append(ports, makeChange("open", k.stack, cw.name, k.port))
				}
				ensureCB(ports)
			} else {
				// Reconnect mode: emit a reconciliation diff so the consumer
				// learns about ports that changed while we were disconnected.
				var changes []string
				for k := range newCounts {
					if oldCounts[k] == 0 {
						changes = append(changes, makeChange("open", k.stack, cw.name, k.port))
					}
				}
				for k := range oldCounts {
					if newCounts[k] == 0 {
						changes = append(changes, makeChange("close", k.stack, cw.name, k.port))
					}
				}
				if len(changes) > 0 {
					cw.client.emit(changes)
				}
			}
		}
	}
	return sc.Err()
}

// wantListen reports whether l should be tracked and reported by the client.
// Listens on loopback addresses (127.x.x.x, ::1) are excluded because they
// are local to the container. Wildcard (0.0.0.0, ::) and specific
// non-loopback addresses are both included.
func wantListen(l lo.Listen) bool {
	ip := net.ParseIP(l.Addr)
	return ip != nil && !ip.IsLoopback()
}

// makeChange formats a single port-change string.
//
//	"open:ipv4:mycontainer:8080"
//	"close:ipv6:other:443"
func makeChange(op string, stack lo.Stack, name string, port uint16) string {
	stackStr := "ipv4"
	if stack == lo.V6 {
		stackStr = "ipv6"
	}
	return fmt.Sprintf("%s:%s:%s:%d", op, stackStr, name, port)
}
