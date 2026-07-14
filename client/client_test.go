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

package client_test

import (
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/canonical/peel/client"
	"github.com/canonical/peel/internal/lo"
)

// ── Mock watch server ─────────────────────────────────────────────────────────

// watchServer is a minimal /watch server that listens on a Unix socket. Each
// accepted HTTP connection is paired with a chan lo.Event supplied by the test
// via connect(): events sent to that channel are streamed as NDJSON to the
// client, and closing the channel ends the connection (triggering a reconnect
// on the client side). Multiple connect() calls queue up channels for
// successive connections.
type watchServer struct {
	t      *testing.T
	httpSv *http.Server
	connCh chan chan lo.Event
}

func newWatchServer(t *testing.T, dir, name string) *watchServer {
	t.Helper()

	ln, err := net.Listen("unix", filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}

	s := &watchServer{
		t:      t,
		connCh: make(chan chan lo.Event, 8),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/watch", s.handleWatch)
	s.httpSv = &http.Server{Handler: mux}

	go s.httpSv.Serve(ln) //nolint:errcheck

	t.Cleanup(func() { s.httpSv.Close() })
	return s
}

// connect queues evCh as the event source for the next incoming connection.
// Events written to evCh are streamed to the HTTP client; closing evCh ends
// that HTTP response (the client will reconnect after its ReconnectDelay).
// Must be called before the client side connects or reconnects.
func (s *watchServer) connect(evCh chan lo.Event) {
	s.connCh <- evCh
}

func (s *watchServer) handleWatch(w http.ResponseWriter, r *http.Request) {
	var evCh chan lo.Event
	select {
	case evCh = <-s.connCh:
	case <-r.Context().Done():
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	enc := json.NewEncoder(w)
	for {
		select {
		case ev, open := <-evCh:
			if !open {
				return // closed channel → EOF on the client side
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

// ── Small helpers ─────────────────────────────────────────────────────────────

func recvChanges(t *testing.T, ch <-chan []string, timeout time.Duration) []string {
	t.Helper()
	select {
	case got, ok := <-ch:
		if !ok {
			t.Fatal("Changes channel closed unexpectedly")
		}
		return got
	case <-time.After(timeout):
		t.Fatalf("timed out after %v waiting for a change batch", timeout)
		return nil
	}
}

func expectClosed(t *testing.T, ch <-chan []string, timeout time.Duration) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-deadline.C:
			t.Fatalf("Changes channel was not closed within %v", timeout)
		}
	}
}

func sortedStrings(ss []string) []string {
	out := make([]string, len(ss))
	copy(out, ss)
	sort.Strings(out)
	return out
}

func equalSorted(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	sa, sb := sortedStrings(a), sortedStrings(b)
	for i := range sa {
		if sa[i] != sb[i] {
			return false
		}
	}
	return true
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// addrFor returns the wildcard/unspecified address for the given stack,
// which is what the client actually tracks (loopback addresses are filtered).
func addrFor(stack lo.Stack) string {
	if stack == lo.V6 {
		return "::"
	}
	return "0.0.0.0"
}

// lo.Event constructors used throughout the tests.
func evAdd(stack lo.Stack, proto lo.Proto, addr string, port uint16) lo.Event {
	return lo.Event{Op: "add", Listen: lo.Listen{Stack: stack, Proto: proto, Addr: addr, Port: port}}
}
func evRemove(stack lo.Stack, proto lo.Proto, addr string, port uint16) lo.Event {
	return lo.Event{Op: "remove", Listen: lo.Listen{Stack: stack, Proto: proto, Addr: addr, Port: port}}
}
func evSync() lo.Event { return lo.Event{Op: "sync"} }

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestEmptyStartupSnapshot checks that a nil/empty first send arrives quickly
// when no sockets exist in the watched directory.
func TestEmptyStartupSnapshot(t *testing.T) {
	dir := t.TempDir()
	c := client.New(dir)
	defer func() { c.Kill(); c.Wait() }() //nolint:errcheck

	got := recvChanges(t, c.Changes(), time.Second)
	if len(got) != 0 {
		t.Fatalf("expected empty startup snapshot, got %v", got)
	}
}

// TestStartupSnapshotWithPorts checks that ports already open in a container
// appear in the very first send on Changes.
func TestStartupSnapshotWithPorts(t *testing.T) {
	dir := t.TempDir()

	evCh := make(chan lo.Event, 8)
	evCh <- evAdd(lo.V4, lo.TCP, "0.0.0.0", 8080)
	evCh <- evAdd(lo.V6, lo.TCP, "::", 443)
	evCh <- evSync()

	srv := newWatchServer(t, dir, "mycontainer")
	srv.connect(evCh)

	c := client.NewWithConfig(client.Config{
		Dir:            dir,
		StartupTimeout: 3 * time.Second,
	})
	defer func() { c.Kill(); c.Wait() }() //nolint:errcheck

	got := recvChanges(t, c.Changes(), 5*time.Second)
	want := []string{"open:ipv4:mycontainer:8080", "open:ipv6:mycontainer:443"}
	if !equalSorted(got, want) {
		t.Fatalf("startup snapshot = %v, want %v (any order)", got, want)
	}
}

// TestMultiContainerStartupSnapshot checks that ports from multiple containers
// are merged into the single first send.
func TestMultiContainerStartupSnapshot(t *testing.T) {
	dir := t.TempDir()

	for _, tc := range []struct {
		name  string
		port  uint16
		stack lo.Stack
	}{
		{"alpha", 1111, lo.V4},
		{"beta", 2222, lo.V6},
	} {
		evCh := make(chan lo.Event, 4)
		evCh <- evAdd(tc.stack, lo.TCP, addrFor(tc.stack), tc.port)
		evCh <- evSync()

		srv := newWatchServer(t, dir, tc.name)
		srv.connect(evCh)
	}

	c := client.NewWithConfig(client.Config{
		Dir:            dir,
		StartupTimeout: 3 * time.Second,
	})
	defer func() { c.Kill(); c.Wait() }() //nolint:errcheck

	got := recvChanges(t, c.Changes(), 5*time.Second)
	want := []string{"open:ipv4:alpha:1111", "open:ipv6:beta:2222"}
	if !equalSorted(got, want) {
		t.Fatalf("startup snapshot = %v, want %v (any order)", got, want)
	}
}

// TestIncrementalAddEvent checks that a port opened after the startup snapshot
// produces an "open" change.
func TestIncrementalAddEvent(t *testing.T) {
	dir := t.TempDir()

	evCh := make(chan lo.Event, 8)
	evCh <- evSync() // empty initial snapshot

	srv := newWatchServer(t, dir, "app")
	srv.connect(evCh)

	c := client.NewWithConfig(client.Config{
		Dir:            dir,
		StartupTimeout: time.Second,
	})
	defer func() { c.Kill(); c.Wait() }() //nolint:errcheck

	recvChanges(t, c.Changes(), 3*time.Second) // startup snapshot

	evCh <- evAdd(lo.V4, lo.TCP, "0.0.0.0", 9090)

	got := recvChanges(t, c.Changes(), time.Second)
	if len(got) != 1 || got[0] != "open:ipv4:app:9090" {
		t.Fatalf("got %v, want [open:ipv4:app:9090]", got)
	}
}

// TestIncrementalRemoveEvent checks that a port closed after the startup
// snapshot produces a "close" change.
func TestIncrementalRemoveEvent(t *testing.T) {
	dir := t.TempDir()

	evCh := make(chan lo.Event, 8)
	evCh <- evAdd(lo.V4, lo.TCP, "0.0.0.0", 7070)
	evCh <- evSync()

	srv := newWatchServer(t, dir, "app")
	srv.connect(evCh)

	c := client.NewWithConfig(client.Config{
		Dir:            dir,
		StartupTimeout: time.Second,
	})
	defer func() { c.Kill(); c.Wait() }() //nolint:errcheck

	recvChanges(t, c.Changes(), 3*time.Second) // startup snapshot

	evCh <- evRemove(lo.V4, lo.TCP, "0.0.0.0", 7070)

	got := recvChanges(t, c.Changes(), time.Second)
	if len(got) != 1 || got[0] != "close:ipv4:app:7070" {
		t.Fatalf("got %v, want [close:ipv4:app:7070]", got)
	}
}

// TestRefCountingKeepsPortOpenAcrossProtocols checks that when both TCP and UDP
// use the same port number, only one "open" is emitted and "close" is held
// until both listeners are gone.
func TestRefCountingKeepsPortOpenAcrossProtocols(t *testing.T) {
	dir := t.TempDir()

	evCh := make(chan lo.Event, 16)
	evCh <- evSync()

	srv := newWatchServer(t, dir, "app")
	srv.connect(evCh)

	c := client.NewWithConfig(client.Config{
		Dir:            dir,
		StartupTimeout: time.Second,
	})
	defer func() { c.Kill(); c.Wait() }() //nolint:errcheck

	recvChanges(t, c.Changes(), 3*time.Second)

	evCh <- evAdd(lo.V4, lo.TCP, "0.0.0.0", 5000)
	got := recvChanges(t, c.Changes(), time.Second)
	if len(got) != 1 || got[0] != "open:ipv4:app:5000" {
		t.Fatalf("after tcp add: got %v", got)
	}

	// UDP on same port — no second "open".
	evCh <- evAdd(lo.V4, lo.UDP, "0.0.0.0", 5000)
	select {
	case extra := <-c.Changes():
		t.Fatalf("unexpected event after duplicate open: %v", extra)
	case <-time.After(200 * time.Millisecond):
	}

	// Remove TCP — port still open via UDP, no "close".
	evCh <- evRemove(lo.V4, lo.TCP, "0.0.0.0", 5000)
	select {
	case extra := <-c.Changes():
		t.Fatalf("unexpected close after first remove: %v", extra)
	case <-time.After(200 * time.Millisecond):
	}

	// Remove UDP — now truly gone.
	evCh <- evRemove(lo.V4, lo.UDP, "0.0.0.0", 5000)
	got = recvChanges(t, c.Changes(), time.Second)
	if len(got) != 1 || got[0] != "close:ipv4:app:5000" {
		t.Fatalf("after both removes: got %v", got)
	}
}

// TestNewContainerDiscoveredAfterStartup checks that a container whose socket
// appears after startup is picked up by the incremental scan and its ports
// arrive as "open" events.
func TestNewContainerDiscoveredAfterStartup(t *testing.T) {
	dir := t.TempDir()

	c := client.NewWithConfig(client.Config{
		Dir:            dir,
		StartupTimeout: 100 * time.Millisecond,
	})
	defer func() { c.Kill(); c.Wait() }() //nolint:errcheck

	recvChanges(t, c.Changes(), time.Second) // empty startup snapshot

	evCh := make(chan lo.Event, 8)
	evCh <- evAdd(lo.V4, lo.TCP, "0.0.0.0", 3000)
	evCh <- evSync()

	srv := newWatchServer(t, dir, "latecomer")
	srv.connect(evCh)

	// Discovery scan fires every ~1 s; allow up to 3 s.
	got := recvChanges(t, c.Changes(), 3*time.Second)
	if !contains(got, "open:ipv4:latecomer:3000") {
		t.Fatalf("expected open:ipv4:latecomer:3000 in %v", got)
	}
}

// TestContainerGoneEmitsCloseEvents checks that when the socket file for a
// container disappears, the client emits "close" events for its open ports.
func TestContainerGoneEmitsCloseEvents(t *testing.T) {
	dir := t.TempDir()

	evCh := make(chan lo.Event, 8)
	evCh <- evAdd(lo.V4, lo.TCP, "0.0.0.0", 6000)
	evCh <- evSync()

	srv := newWatchServer(t, dir, "mortal")
	srv.connect(evCh)

	c := client.NewWithConfig(client.Config{
		Dir:            dir,
		StartupTimeout: 3 * time.Second,
	})
	defer func() { c.Kill(); c.Wait() }() //nolint:errcheck

	recvChanges(t, c.Changes(), 5*time.Second) // startup snapshot

	// Closing the HTTP server also closes its Unix listener, which removes
	// the socket file — so no explicit os.Remove is needed.
	srv.httpSv.Close()

	// Discovery scan fires every ~1 s; allow up to 3 s.
	got := recvChanges(t, c.Changes(), 3*time.Second)
	if !contains(got, "close:ipv4:mortal:6000") {
		t.Fatalf("expected close:ipv4:mortal:6000 in %v", got)
	}
}

// TestKillClosesChannel checks that Kill causes Changes to be closed and
// Wait to return.
func TestKillClosesChannel(t *testing.T) {
	dir := t.TempDir()
	c := client.New(dir)

	recvChanges(t, c.Changes(), time.Second) // drain startup snapshot

	c.Kill()
	expectClosed(t, c.Changes(), 5*time.Second)

	if err := c.Wait(); err != nil {
		t.Fatalf("Wait() = %v, want nil", err)
	}
}

// TestReconnectReconciles checks that after a connection drop the client
// reconnects, diffs the new snapshot against the old one, and emits the
// correct reconciliation batch.
func TestReconnectReconciles(t *testing.T) {
	dir := t.TempDir()

	// First connection: port 8080 open. Closing the channel triggers EOF.
	conn1 := make(chan lo.Event, 4)
	conn1 <- evAdd(lo.V4, lo.TCP, "0.0.0.0", 8080)
	conn1 <- evSync()
	close(conn1)

	// Second connection: 8080 gone, 9090 new.
	conn2 := make(chan lo.Event, 4)
	conn2 <- evAdd(lo.V4, lo.TCP, "0.0.0.0", 9090)
	conn2 <- evSync()

	srv := newWatchServer(t, dir, "svc")
	srv.connect(conn1)
	srv.connect(conn2)

	c := client.NewWithConfig(client.Config{
		Dir:            dir,
		StartupTimeout: 3 * time.Second,
		ReconnectDelay: 50 * time.Millisecond,
	})
	defer func() { c.Kill(); c.Wait() }() //nolint:errcheck

	snap := recvChanges(t, c.Changes(), 5*time.Second)
	if !equalSorted(snap, []string{"open:ipv4:svc:8080"}) {
		t.Fatalf("startup snapshot = %v, want [open:ipv4:svc:8080]", snap)
	}

	// After reconnect the reconciliation diff arrives as a single batch.
	reconcile := recvChanges(t, c.Changes(), 3*time.Second)
	want := []string{"close:ipv4:svc:8080", "open:ipv4:svc:9090"}
	if !equalSorted(reconcile, want) {
		t.Fatalf("reconcile batch = %v, want %v (any order)", reconcile, want)
	}
}

// TestStartupSnapshotIsFirstSend verifies that Changes always delivers the
// combined startup snapshot as its very first item, even when incremental
// events race in immediately after a container's first sync.
func TestStartupSnapshotIsFirstSend(t *testing.T) {
	dir := t.TempDir()

	evCh := make(chan lo.Event, 16)
	evCh <- evAdd(lo.V4, lo.TCP, "0.0.0.0", 1234)
	evCh <- evSync()
	evCh <- evAdd(lo.V4, lo.TCP, "0.0.0.0", 5678) // incremental, post-sync

	srv := newWatchServer(t, dir, "fast")
	srv.connect(evCh)

	c := client.NewWithConfig(client.Config{
		Dir:            dir,
		StartupTimeout: 3 * time.Second,
	})
	defer func() { c.Kill(); c.Wait() }() //nolint:errcheck

	first := recvChanges(t, c.Changes(), 5*time.Second)
	if !equalSorted(first, []string{"open:ipv4:fast:1234"}) {
		t.Fatalf("first send = %v, want startup snapshot [open:ipv4:fast:1234]", first)
	}

	second := recvChanges(t, c.Changes(), time.Second)
	if !contains(second, "open:ipv4:fast:5678") {
		t.Fatalf("second send = %v, want open:ipv4:fast:5678", second)
	}
}
