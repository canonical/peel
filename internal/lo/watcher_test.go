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
	"testing"
	"time"
)

func TestWatcherSubscribeSnapshot(t *testing.T) {
	w := NewWatcher()
	w.current[Listen{Stack: V4, Proto: TCP, Port: 80}] = true

	events, snapshot, cancel := w.Subscribe()
	defer cancel()

	if len(snapshot) != 1 || snapshot[0].Port != 80 {
		t.Fatalf("snapshot = %v", snapshot)
	}

	select {
	case ev := <-events:
		t.Fatalf("unexpected event before any change: %v", ev)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestWatcherPublishesAddAndRemove(t *testing.T) {
	w := NewWatcher()
	events, _, cancel := w.Subscribe()
	defer cancel()

	l := Listen{Stack: V4, Proto: TCP, Port: 8080}

	w.mu.Lock()
	w.publish(Event{Op: "add", Listen: l})
	w.current[l] = true
	w.mu.Unlock()

	select {
	case ev := <-events:
		if ev.Op != "add" || ev.Listen != l {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for add event")
	}

	w.mu.Lock()
	w.publish(Event{Op: "remove", Listen: l})
	delete(w.current, l)
	w.mu.Unlock()

	select {
	case ev := <-events:
		if ev.Op != "remove" || ev.Listen != l {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for remove event")
	}
}

func TestWatcherExcludeHidesFromSnapshot(t *testing.T) {
	w := NewWatcher()
	l := Listen{Stack: V6, Proto: UDP, Port: 53}

	w.Exclude(l)
	w.mu.Lock()
	if w.excluded[l] != 1 {
		t.Fatalf("excluded[%v] = %d, want 1", l, w.excluded[l])
	}
	w.mu.Unlock()

	w.Unexclude(l)
	w.mu.Lock()
	if _, ok := w.excluded[l]; ok {
		t.Fatalf("excluded still contains %v after Unexclude", l)
	}
	w.mu.Unlock()
}

func TestScanInterval(t *testing.T) {
	cases := []struct {
		elapsed time.Duration
		want    time.Duration
	}{
		{0, ProcScanFastInterval},
		{30 * time.Second, ProcScanFastInterval},
		{ProcScanFastWindow - time.Millisecond, ProcScanFastInterval},
		{ProcScanFastWindow, ProcScanSlowInterval},
		{ProcScanFastWindow + time.Hour, ProcScanSlowInterval},
	}
	for _, c := range cases {
		if got := scanInterval(c.elapsed); got != c.want {
			t.Errorf("scanInterval(%v) = %v, want %v", c.elapsed, got, c.want)
		}
	}
}

func TestWatcherCancelClosesChannel(t *testing.T) {
	w := NewWatcher()
	events, _, cancel := w.Subscribe()
	cancel()

	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("expected channel to be closed")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for channel to close")
	}
}
