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
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

// reconnectDelay is how long peerManager waits before retrying a peer's
// /watch endpoint after it errors out or disconnects.
const reconnectDelay = 5 * time.Second

// watchScannerBuffer bounds a single /watch line (a JSON Event): generous,
// since events are tiny, but large enough to never legitimately truncate
// one.
const watchScannerBuffer = 64 * 1024

// peerManager watches a single peer's /watch endpoint and keeps a local
// Proxy running for every listen it currently reports.
type peerManager struct {
	name     string // the peer's /peel/lo file name, purely for logging.
	sockPath string
	watcher  *Watcher

	mu      sync.Mutex
	proxies map[Listen]*Proxy
}

func newPeerManager(name, sockPath string, watcher *Watcher) *peerManager {
	return &peerManager{
		name:     name,
		sockPath: sockPath,
		watcher:  watcher,
		proxies:  make(map[Listen]*Proxy),
	}
}

// run watches the peer's /watch endpoint until ctx is done, reconnecting
// with a fixed delay on every disconnect or error.
func (p *peerManager) run(ctx context.Context) {
	defer p.stopAll()
	for ctx.Err() == nil {
		if err := p.watchOnce(ctx); err != nil {
			log.Printf("lo: %s: watch: %v", p.name, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(reconnectDelay):
		}
	}
}

// watchOnce makes a single connection to the peer's /watch endpoint and
// processes events from it until it ends.
func (p *peerManager) watchOnce(ctx context.Context) error {
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", p.sockPath)
			},
		},
	}
	defer client.CloseIdleConnections()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://peel/watch", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %s", resp.Status)
	}

	log.Printf("lo: %s: watching", p.name)

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, watchScannerBuffer), watchScannerBuffer)

	seen := make(map[Listen]bool)
	synced := false
	for sc.Scan() {
		var ev Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			log.Printf("lo: %s: malformed watch event: %v", p.name, err)
			continue
		}
		switch ev.Op {
		case "sync":
			p.reconcile(ctx, seen)
			synced = true
		case "add":
			if !synced {
				seen[ev.Listen] = true
			}
			p.add(ctx, ev.Listen)
		case "remove":
			p.remove(ev.Listen)
		}
	}
	return sc.Err()
}

// add starts proxying l, unless it already is.
func (p *peerManager) add(ctx context.Context, l Listen) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.proxies[l]; ok {
		return
	}
	proxy, err := StartProxy(ctx, p.watcher, p.sockPath, l)
	if err != nil {
		log.Printf("lo: %s: proxying %s: %v", p.name, l, err)
		return
	}
	p.proxies[l] = proxy
	log.Printf("lo: %s: proxying %s", p.name, l)
}

// remove stops proxying l, if it currently is.
func (p *peerManager) remove(l Listen) {
	p.mu.Lock()
	proxy, ok := p.proxies[l]
	if ok {
		delete(p.proxies, l)
	}
	p.mu.Unlock()

	if ok {
		proxy.Close()
		log.Printf("lo: %s: stopped proxying %s", p.name, l)
	}
}

// reconcile stops proxying anything not in seen: it's called right after a
// fresh connection's snapshot is fully received, to drop any proxy kept
// from a previous connection whose "remove" event was missed while
// disconnected.
func (p *peerManager) reconcile(ctx context.Context, seen map[Listen]bool) {
	p.mu.Lock()
	var stale []Listen
	for l := range p.proxies {
		if !seen[l] {
			stale = append(stale, l)
		}
	}
	p.mu.Unlock()

	for _, l := range stale {
		p.remove(l)
	}
}

// stopAll stops every proxy currently running for this peer.
func (p *peerManager) stopAll() {
	p.mu.Lock()
	proxies := p.proxies
	p.proxies = make(map[Listen]*Proxy)
	p.mu.Unlock()

	for l, proxy := range proxies {
		proxy.Close()
		log.Printf("lo: %s: stopped proxying %s", p.name, l)
	}
}
