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
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

// startTestServer starts a Server backed by a fresh Watcher on a unix
// socket in a temp directory, and returns its path.
func startTestServer(t *testing.T) (sockPath string, watcher *Watcher) {
	t.Helper()
	sockPath = filepath.Join(t.TempDir(), "test.sock")

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	watcher = NewWatcher()

	httpServer := &http.Server{Handler: NewServer(watcher).Handler()}
	go httpServer.Serve(ln)
	t.Cleanup(func() { httpServer.Close() })

	return sockPath, watcher
}

func TestConnectRoundTripTCP(t *testing.T) {
	sockPath, watcher := startTestServer(t)

	// A fake "real" service listening on loopback, whose sole job is to
	// echo back whatever it receives.
	echo, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			conn, err := echo.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				buf := make([]byte, 1024)
				n, err := conn.Read(buf)
				if err != nil {
					return
				}
				conn.Write(buf[:n])
			}()
		}
	}()
	port := uint16(echo.Addr().(*net.TCPAddr).Port)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tunnel, err := dialConnect(ctx, sockPath, Listen{Stack: V4, Proto: TCP, Addr: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatalf("dialConnect: %v", err)
	}
	defer tunnel.Close()

	if _, err := tunnel.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := tunnel.Read(buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "hello" {
		t.Fatalf("got %q, want %q", buf, "hello")
	}

	_ = watcher
}

func TestConnectRejectsBadParams(t *testing.T) {
	sockPath, _ := startTestServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := dialConnect(ctx, sockPath, Listen{Stack: "bogus", Proto: TCP, Addr: "127.0.0.1", Port: 1}); err == nil {
		t.Fatal("expected an error for an invalid stack")
	}
}

func TestWatchStreamsSnapshotAndUpdates(t *testing.T) {
	sockPath, watcher := startTestServer(t)

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sockPath)
			},
		},
	}
	resp, err := client.Get("http://peel/watch")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	sc := bufio.NewScanner(resp.Body)
	readEvent := func() Event {
		if !sc.Scan() {
			t.Fatalf("scan: %v", sc.Err())
		}
		var ev Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("unmarshal %q: %v", sc.Text(), err)
		}
		return ev
	}

	// Empty snapshot: just the sync marker.
	if ev := readEvent(); ev.Op != "sync" {
		t.Fatalf("first event = %+v, want sync", ev)
	}

	l := Listen{Stack: V4, Proto: TCP, Addr: "127.0.0.1", Port: 1234}
	watcher.mu.Lock()
	watcher.publish(Event{Op: "add", Listen: l})
	watcher.current[l] = true
	watcher.mu.Unlock()

	if ev := readEvent(); ev.Op != "add" || ev.Listen != l {
		t.Fatalf("event = %+v, want add %v", ev, l)
	}
}
