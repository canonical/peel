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

// Package client provides a PortsClient for watching the ports that peel
// containers expose through their shared lo socket directory.
//
// Each container in a peel environment places a Unix socket at
// /peel/lo/<hostname>. The client scans that directory, dials every socket
// it finds, and subscribes to each container's /watch stream to learn which
// ports it is currently listening on.
//
// Typical usage:
//
//	c := client.New(client.DefaultDir)
//	defer func() { c.Kill(); c.Wait() }()
//
//	for changes := range c.Changes() {
//	    for _, ch := range changes {
//	        // e.g. "open:ipv4:mycontainer:8080"
//	        //   or "close:ipv6:other:9999"
//	        fmt.Println(ch)
//	    }
//	}
package client

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DefaultDir is the shared directory where peel containers place their lo
// sockets.
const DefaultDir = "/peel/lo"

const (
	defaultReconnectDelay = 5 * time.Second
	defaultStartupTimeout = 5 * time.Second

	// outBuf is how many []string batches the internal forwarding channel
	// can hold before a slow consumer causes container-watcher goroutines
	// to block.
	outBuf = 128
)

// PortsClient watches port exposure across peel containers.
type PortsClient interface {
	// Kill asks the client to stop and returns immediately.
	Kill()

	// Wait waits for the client to complete and returns any
	// error encountered when it was running or stopping.
	Wait() error

	// Changes returns a channel of type []string, that will be closed when
	// the client is stopped/killed. Each string in the slice is a port
	// opened/closed, in the form "close:ipv6:my-container:8080" or
	// "open:ipv4:other-container:9999". The first send on the channel is
	// all the currently open ports across all containers active at startup,
	// or a nil slice if none are open.
	Changes() <-chan []string
}

// Config customises a PortsClient created via NewWithConfig.
type Config struct {
	// Dir is the directory to watch for lo sockets.
	// Defaults to DefaultDir.
	Dir string

	// ReconnectDelay is the pause between reconnect attempts after a
	// container watch stream disconnects or errors.
	// Defaults to 5s.
	ReconnectDelay time.Duration

	// StartupTimeout caps how long the client waits for every container
	// present at startup to report its initial port snapshot before the
	// combined result is sent on Changes. Containers that do not respond
	// in time are not included in the startup snapshot; their ports appear
	// later as incremental "open" events.
	// Defaults to 5s.
	StartupTimeout time.Duration
}

// New creates and starts a PortsClient that watches dir. Call Wait
// (optionally preceded by Kill) to release resources.
func New(dir string) PortsClient {
	return NewWithConfig(Config{Dir: dir})
}

// NewWithConfig is like New but accepts a Config for fine-tuning.
func NewWithConfig(cfg Config) PortsClient {
	if cfg.Dir == "" {
		cfg.Dir = DefaultDir
	}
	if cfg.ReconnectDelay == 0 {
		cfg.ReconnectDelay = defaultReconnectDelay
	}
	if cfg.StartupTimeout == 0 {
		cfg.StartupTimeout = defaultStartupTimeout
	}

	ctx, cancel := context.WithCancel(context.Background())
	c := &portsClient{
		cfg:        cfg,
		changes:    make(chan []string, outBuf),
		outCh:      make(chan []string, outBuf),
		ctx:        ctx,
		cancel:     cancel,
		containers: make(map[string]*containerWatcher),
	}
	c.mainWg.Add(1)
	go c.run()
	return c
}

type portsClient struct {
	cfg Config

	// changes is the channel exposed to the consumer via Changes().
	changes chan []string
	// outCh is where container-watcher goroutines post change batches.
	// run() serialises these onto changes, ensuring the startup snapshot
	// is always the very first send.
	outCh chan []string

	ctx    context.Context
	cancel context.CancelFunc

	// mainWg tracks the single run() goroutine; Wait blocks on it.
	mainWg sync.WaitGroup
	// cwg tracks every per-container goroutine; run() waits on it before
	// closing the changes channel so that final close-events are visible.
	cwg sync.WaitGroup

	mu         sync.Mutex
	containers map[string]*containerWatcher
}

// Kill asks the client to stop and returns immediately.
func (c *portsClient) Kill() { c.cancel() }

// Wait waits for the client to fully stop and returns any error.
func (c *portsClient) Wait() error { c.mainWg.Wait(); return nil }

// Changes returns the port-change channel.
func (c *portsClient) Changes() <-chan []string { return c.changes }

// run is the single main goroutine. It:
//  1. Scans the directory for container sockets present at startup.
//  2. Starts per-container goroutines and waits for their initial snapshots.
//  3. Sends the combined startup snapshot as the guaranteed first item on
//     changes (buffering any incremental events that race in before it).
//  4. Continuously scans for newly appearing/disappearing containers and
//     forwards incremental change batches from outCh onto changes.
func (c *portsClient) run() {
	defer c.mainWg.Done()
	// Defers execute LIFO: cancel → wait for containers → close channel → done.
	defer close(c.changes)
	defer c.cwg.Wait()
	defer c.cancel()

	// ── Startup snapshot phase ────────────────────────────────────────────
	initial := c.scanDir()

	type startupResult struct{ ports []string }
	startupCh := make(chan startupResult, len(initial)+1)

	for _, name := range initial {
		c.startContainer(name, func(ports []string) {
			startupCh <- startupResult{ports}
		})
	}

	var (
		snapshot   []string   // combined ports from all startup containers
		pendingOut [][]string // incremental events that raced in before snapshot
	)

	deadline := time.NewTimer(c.cfg.StartupTimeout)
	defer deadline.Stop()

	for remaining := len(initial); remaining > 0; {
		select {
		case r := <-startupCh:
			snapshot = append(snapshot, r.ports...)
			remaining--
		case ev := <-c.outCh:
			// Buffer events that arrive while we still wait for startup syncs.
			pendingOut = append(pendingOut, ev)
		case <-deadline.C:
			remaining = 0
		case <-c.ctx.Done():
			return
		}
	}

	// Send the startup snapshot — this is always the first send on changes.
	select {
	case c.changes <- snapshot:
	case <-c.ctx.Done():
		return
	}

	// Flush any incremental events buffered during the startup wait.
	for _, ev := range pendingOut {
		select {
		case c.changes <- ev:
		case <-c.ctx.Done():
			return
		}
	}

	// ── Incremental phase ─────────────────────────────────────────────────
	start := time.Now()
	scanTimer := time.NewTimer(discoveryScanInterval(0))
	for {
		select {
		case <-c.ctx.Done():
			scanTimer.Stop()
			return
		case ev := <-c.outCh:
			select {
			case c.changes <- ev:
			case <-c.ctx.Done():
				return
			}
		case <-scanTimer.C:
			c.scan()
			scanTimer.Reset(discoveryScanInterval(time.Since(start)))
		}
	}
}

// startContainer creates a containerWatcher, registers it, and launches its
// goroutine. startupCallback, if non-nil, is forwarded to the watcher to be
// called once with the container's initial open port strings (or nil on
// connection failure) so the startup coordinator can assemble its snapshot.
func (c *portsClient) startContainer(name string, startupCallback func([]string)) *containerWatcher {
	cw := newContainerWatcher(name, filepath.Join(c.cfg.Dir, name), c)
	c.mu.Lock()
	c.containers[name] = cw
	c.mu.Unlock()

	c.cwg.Add(1)
	go func() {
		defer c.cwg.Done()
		cw.run(startupCallback)
	}()
	return cw
}

// stopContainer cancels and unregisters a container watcher.
func (c *portsClient) stopContainer(name string) {
	c.mu.Lock()
	cw, ok := c.containers[name]
	if ok {
		delete(c.containers, name)
	}
	c.mu.Unlock()
	if ok {
		cw.cancel()
	}
}

// scanDir reads the socket directory once and returns the basenames of all
// non-directory entries. Errors (other than the directory not existing) are
// logged.
func (c *portsClient) scanDir() []string {
	entries, err := os.ReadDir(c.cfg.Dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("portsclient: listing %s: %v", c.cfg.Dir, err)
		}
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

// scan compares the current directory contents with the known containers,
// starting watchers for newly appearing sockets and stopping them for gone
// ones.
func (c *portsClient) scan() {
	entries, err := os.ReadDir(c.cfg.Dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("portsclient: listing %s: %v", c.cfg.Dir, err)
		}
		return
	}

	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		seen[name] = true

		c.mu.Lock()
		_, known := c.containers[name]
		c.mu.Unlock()
		if !known {
			log.Printf("portsclient: discovered %s", name)
			c.startContainer(name, nil)
		}
	}

	c.mu.Lock()
	var gone []string
	for name := range c.containers {
		if !seen[name] {
			gone = append(gone, name)
		}
	}
	c.mu.Unlock()

	for _, name := range gone {
		log.Printf("portsclient: %s gone", name)
		c.stopContainer(name)
	}
}

// emit posts a batch of change strings to outCh. It blocks until either the
// send succeeds or the client context is cancelled (e.g. after Kill).
func (c *portsClient) emit(changes []string) {
	select {
	case c.outCh <- changes:
	case <-c.ctx.Done():
	}
}

// discoveryScanInterval returns the polling interval for the directory scan
// given how long the incremental phase has been running, matching the
// fast-then-slow schedule used by the internal lo watcher.
func discoveryScanInterval(elapsed time.Duration) time.Duration {
	if elapsed < time.Minute {
		return time.Second
	}
	return 5 * time.Second
}
