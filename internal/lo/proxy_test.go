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
	"net"
	"testing"
)

// TestStartProxyBindConflictUnexcludes exercises StartProxy's failure
// path: a real end-to-end proxy necessarily listens on the very same
// port as the peer's own service (that's the whole point: elsewhere,
// that's a different container/network namespace, so there's no
// conflict), which single-process tests can't reproduce without a real
// second network namespace. What can be tested here is that a failed
// bind still leaves the Watcher's exclude set exactly as it found it.
func TestStartProxyBindConflictUnexcludes(t *testing.T) {
	blocker, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	port := uint16(blocker.Addr().(*net.TCPAddr).Port)
	l := Listen{Stack: V4, Proto: TCP, Addr: "127.0.0.1", Port: port}

	watcher := NewWatcher()
	if _, err := StartProxy(context.Background(), watcher, "/nonexistent.sock", l); err == nil {
		t.Fatal("expected a bind conflict error")
	}

	watcher.mu.Lock()
	defer watcher.mu.Unlock()
	if _, ok := watcher.excluded[l]; ok {
		t.Fatalf("excluded still contains %v after a failed StartProxy", l)
	}
}

func TestProxyRejectsRemoteOnlyWhenUnspecified(t *testing.T) {
	remote := &net.TCPAddr{IP: net.ParseIP("203.0.113.1"), Port: 1234}
	localhost := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1234}

	wildcard := &Proxy{listen: Listen{Stack: V4, Addr: "0.0.0.0", Port: 80}}
	if !wildcard.rejectsRemote(remote) {
		t.Error("a wildcard-bound proxy should reject a remote, non-local address")
	}
	if wildcard.rejectsRemote(localhost) {
		t.Error("a wildcard-bound proxy should accept a loopback address")
	}

	specific := &Proxy{listen: Listen{Stack: V4, Addr: "127.0.0.2", Port: 80}}
	if specific.rejectsRemote(remote) {
		t.Error("a specific-loopback-bound proxy should never reject based on remote address")
	}
}

func TestRunDirNoopWithoutDirectory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := RunDir(ctx, t.TempDir()+"/does-not-exist"); err != nil {
		t.Fatalf("RunDir: %v", err)
	}
}
