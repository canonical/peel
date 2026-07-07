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
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
)

// DefaultDir is the shared directory peel looks for peer sockets in. It is
// expected to be a disk device the operator has attached identically to
// every container that should share its loopback listeners.
const DefaultDir = "/peel/lo"

// Run starts loopback sharing rooted at DefaultDir. It is a no-op (nothing
// is listened on, nothing is watched) if DefaultDir doesn't exist: not
// every container need opt into this.
//
// Run itself never blocks: everything it starts runs in the background for
// as long as ctx is live.
func Run(ctx context.Context) error {
	return RunDir(ctx, DefaultDir)
}

// RunDir is Run, against an arbitrary directory (dir), for testing.
func RunDir(ctx context.Context, dir string) error {
	if _, err := os.Stat(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("lo: stat %s: %w", dir, err)
	}

	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("lo: hostname: %w", err)
	}
	sockPath := filepath.Join(dir, hostname)

	// Clear a socket left behind by a previous boot before listening on
	// the same path again.
	if err := os.Remove(sockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("lo: removing stale %s: %w", sockPath, err)
	}

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return fmt.Errorf("lo: listening on %s: %w", sockPath, err)
	}
	// Containers taking part in loopback sharing are mutually trusting
	// (they're being handed each other's loopback services outright), but
	// may run under different, unrelated uid mappings on the host: make
	// sure whichever uid every other peel instance happens to be mapped to
	// can still connect.
	if err := os.Chmod(sockPath, 0o666); err != nil {
		log.Printf("lo: chmod %s: %v", sockPath, err)
	}

	watcher := NewWatcher()
	go watcher.Run(ctx)

	server := NewServer(watcher)
	httpServer := &http.Server{Handler: server.Handler()}
	go func() {
		<-ctx.Done()
		httpServer.Close()
	}()
	go func() {
		if err := httpServer.Serve(ln); err != nil &&
			!errors.Is(err, net.ErrClosed) && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("lo: serving %s: %v", sockPath, err)
		}
	}()
	log.Printf("lo: listening on %s", sockPath)

	go runDiscovery(ctx, dir, hostname, watcher)

	return nil
}
