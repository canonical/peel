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
	"log"
	"os"
	"path/filepath"
	"time"
)

// discovery tracks every peer socket found in dir (other than self) and
// keeps a peerManager running for each one for as long as its socket file
// exists.
type discovery struct {
	dir     string
	self    string
	watcher *Watcher

	peers   map[string]*peerManager
	cancels map[string]context.CancelFunc
}

// runDiscovery polls dir for peer sockets until ctx is done, on the same
// fast-then-slow schedule as the Watcher's own /proc/net polling (see
// scanInterval): quickly at first, so peers that show up shortly after
// boot are picked up promptly, then less often once things have settled.
func runDiscovery(ctx context.Context, dir, self string, watcher *Watcher) {
	d := &discovery{
		dir:     dir,
		self:    self,
		watcher: watcher,
		peers:   make(map[string]*peerManager),
		cancels: make(map[string]context.CancelFunc),
	}

	d.scan(ctx)
	start := time.Now()
	for {
		timer := time.NewTimer(scanInterval(time.Since(start)))
		select {
		case <-ctx.Done():
			timer.Stop()
			for _, cancel := range d.cancels {
				cancel()
			}
			return
		case <-timer.C:
			d.scan(ctx)
		}
	}
}

// scan lists dir once, starting a peerManager for every newly-seen socket
// file and stopping one for every socket file that's gone.
func (d *discovery) scan(ctx context.Context) {
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		log.Printf("lo: listing %s: %v", d.dir, err)
		return
	}

	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || name == d.self {
			continue
		}
		seen[name] = true
		if _, ok := d.peers[name]; ok {
			continue
		}

		pctx, cancel := context.WithCancel(ctx)
		pm := newPeerManager(name, filepath.Join(d.dir, name), d.watcher)
		d.peers[name] = pm
		d.cancels[name] = cancel

		log.Printf("lo: discovered peer %s", name)
		go pm.run(pctx)
	}

	for name, cancel := range d.cancels {
		if seen[name] {
			continue
		}
		log.Printf("lo: peer %s gone", name)
		cancel()
		delete(d.cancels, name)
		delete(d.peers, name)
	}
}
