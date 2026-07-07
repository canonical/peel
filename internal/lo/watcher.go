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
	"sort"
	"sync"
	"time"
)

// ProcScanFastInterval is how often the Watcher re-reads /proc/net/* for
// the first ProcScanFastWindow after it starts, so that listeners which
// come up shortly after boot are picked up quickly.
const ProcScanFastInterval = 1 * time.Second

// ProcScanFastWindow is how long ProcScanFastInterval applies for, before
// falling back to the slower, steady-state ProcScanSlowInterval.
const ProcScanFastWindow = 1 * time.Minute

// ProcScanSlowInterval is how often the Watcher re-reads /proc/net/* once
// ProcScanFastWindow has elapsed.
const ProcScanSlowInterval = 5 * time.Second

// subscriberBuffer is how many events a subscriber can lag behind by
// before it's dropped. A dropped subscriber's channel is closed; callers
// are expected to notice and resubscribe (getting a fresh snapshot).
const subscriberBuffer = 256

// Watcher polls /proc/net/{tcp,tcp6,udp,udp6} for loopback/wildcard
// listening sockets, and publishes add/remove events to any number of
// subscribers.
//
// It also doubles as the registry of ports proxied on a peer's behalf
// (see Exclude): those are never reported, so that a container's own
// /watch output only ever describes sockets its own entrypoint opened.
type Watcher struct {
	mu       sync.Mutex
	current  map[Listen]bool
	excluded map[Listen]int
	subs     map[chan Event]struct{}
}

// NewWatcher returns a Watcher with nothing yet discovered. Call Run to
// start it scanning.
func NewWatcher() *Watcher {
	return &Watcher{
		current:  make(map[Listen]bool),
		excluded: make(map[Listen]int),
		subs:     make(map[chan Event]struct{}),
	}
}

// Run scans /proc/net every ProcScanFastInterval for the first
// ProcScanFastWindow, then every ProcScanSlowInterval, until ctx is done.
// It never returns until then, so it should be started in its own
// goroutine.
func (w *Watcher) Run(ctx context.Context) {
	w.scanOnce()

	start := time.Now()
	for {
		timer := time.NewTimer(scanInterval(time.Since(start)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			w.scanOnce()
		}
	}
}

// scanInterval returns how long Run should wait before its next scan,
// given how long it's been running for: ProcScanFastInterval for the
// first ProcScanFastWindow, ProcScanSlowInterval after that.
func scanInterval(elapsed time.Duration) time.Duration {
	if elapsed < ProcScanFastWindow {
		return ProcScanFastInterval
	}
	return ProcScanSlowInterval
}

func (w *Watcher) scanOnce() {
	raw, err := scanProcNet()
	if err != nil {
		log.Printf("lo: scanning /proc/net: %v", err)
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	next := make(map[Listen]bool, len(raw))
	for l := range raw {
		if w.excluded[l] > 0 {
			continue
		}
		next[l] = true
	}

	for l := range next {
		if !w.current[l] {
			w.publish(Event{Op: "add", Listen: l})
		}
	}
	for l := range w.current {
		if !next[l] {
			w.publish(Event{Op: "remove", Listen: l})
		}
	}
	w.current = next
}

// publish fans ev out to every subscriber. w.mu must already be held.
// A subscriber that isn't keeping up is dropped rather than allowed to
// block the scanner.
func (w *Watcher) publish(ev Event) {
	for ch := range w.subs {
		select {
		case ch <- ev:
		default:
			close(ch)
			delete(w.subs, ch)
		}
	}
}

// Subscribe registers a new subscriber, returning a channel of events from
// this point on, a snapshot of every listen already known about, and a
// cancel function to unsubscribe. The channel is closed once cancel is
// called, or if the subscriber falls too far behind.
func (w *Watcher) Subscribe() (events <-chan Event, snapshot []Listen, cancel func()) {
	w.mu.Lock()
	defer w.mu.Unlock()

	snapshot = make([]Listen, 0, len(w.current))
	for l := range w.current {
		snapshot = append(snapshot, l)
	}
	sort.Slice(snapshot, func(i, j int) bool { return snapshot[i].String() < snapshot[j].String() })

	ch := make(chan Event, subscriberBuffer)
	w.subs[ch] = struct{}{}

	cancel = func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if _, ok := w.subs[ch]; ok {
			delete(w.subs, ch)
			close(ch)
		}
	}
	return ch, snapshot, cancel
}

// Exclude marks l as proxied on a peer's behalf: it will never appear in a
// snapshot or event again (even if actually listening) until every
// matching Unexclude call has been made. Excludes are reference-counted so
// that two peers independently reporting the same Listen don't fight over
// it.
func (w *Watcher) Exclude(l Listen) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.excluded[l]++
}

// Unexclude reverses a single Exclude call for l.
func (w *Watcher) Unexclude(l Listen) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.excluded[l] <= 1 {
		delete(w.excluded, l)
		return
	}
	w.excluded[l]--
}
