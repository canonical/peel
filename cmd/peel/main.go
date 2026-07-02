// copyright (c) 2026 canonical ltd
//
// this program is free software: you can redistribute it and/or modify
// it under the terms of the gnu general public license version 3 as
// published by the free software foundation.
//
// this program is distributed in the hope that it will be useful,
// but without any warranty; without even the implied warranty of
// merchantability or fitness for a particular purpose.  see the
// gnu general public license for more details.
//
// you should have received a copy of the gnu general public license
// along with this program.  if not, see <http://www.gnu.org/licenses/>.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"time"

	"github.com/canonical/peel/internal/config"
	"github.com/canonical/peel/internal/entrypoint"
	"github.com/canonical/peel/internal/network"
	"github.com/canonical/peel/internal/pull"
	"github.com/canonical/peel/internal/rootfs"
	"github.com/canonical/peel/internal/state"
	"github.com/canonical/peel/internal/supervisor"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("peel: ")

	root := flag.String("rootfs", "/", "root of the container filesystem to unpack images onto")
	configPath := flag.String("config", config.DefaultPath, "path to peel's rendered configuration file")
	statePath := flag.String("state", state.DefaultPath, "path to peel's persisted state file")
	pullOnly := flag.Bool("pull-only", false, "pull and unpack the image (if needed) and print the resolved "+
		"entrypoint, then exit, without execing it or acting as PID 1. Intended for testing outside of a "+
		"container, where becoming a supervisor that signals every process on the system would be unsafe.")
	flag.Parse()

	if err := run(*root, *configPath, *statePath, *pullOnly); err != nil {
		log.Fatalf("%v", err)
	}
}

func run(root, configPath, statePath string, pullOnly bool) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	ctx := context.Background()

	// The rootfs starts out with no network configuration at all (no
	// address, no resolv.conf): bring it up ourselves, since there is
	// nothing else in the container to do it before the very first pull.
	nameservers := network.Configure(ctx, root, network.DefaultTimeout)

	rt, err := ensureUnpacked(ctx, root, statePath, cfg)
	if err != nil {
		return err
	}

	// The entrypoint to run is now fully determined by state.json (rt);
	// peel's own configuration file is no longer needed for this boot.
	// Delete it so registry credentials don't linger in the rootfs any
	// longer than necessary. It will be rendered afresh by LXD's own
	// templating the next time the container starts. Skipped in
	// pull-only mode so repeated manual test runs don't require a fresh
	// config file each time.
	if !pullOnly {
		if err := os.Remove(configPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Printf("removing %s: %v", configPath, err)
		}
	}

	// Unpacking a (new) image may have reset /etc, wiping the
	// resolv.conf written above; make sure it's still there for the
	// entrypoint we're about to start.
	if err := network.WriteResolvConf(root, nameservers); err != nil {
		log.Printf("network: writing resolv.conf: %v", err)
	}

	spec, err := entrypoint.Resolve(root, *rt)
	if err != nil {
		return err
	}

	if pullOnly {
		log.Printf("pull-only: resolved entrypoint path=%s argv=%v dir=%s uid=%d gid=%d",
			spec.Path, spec.Argv, spec.Dir, spec.UID, spec.GID)
		return nil
	}

	return supervisor.Run(spec)
}

// ensureUnpacked returns the runtime configuration to exec, pulling and
// unpacking cfg.Image onto root first unless it's already unpacked there.
func ensureUnpacked(ctx context.Context, root, statePath string, cfg *config.Config) (*state.Runtime, error) {
	st, err := state.Load(statePath)
	if err != nil {
		return nil, err
	}
	if st != nil && st.Reference == cfg.Image {
		log.Printf("image %q already unpacked at %s, skipping pull", cfg.Image, root)
		rt := st.Runtime
		return &rt, nil
	}

	log.Printf("pulling image %q", cfg.Image)
	img, err := pull.Image(ctx, cfg)
	if err != nil {
		return nil, err
	}

	digest, err := img.Digest()
	if err != nil {
		return nil, fmt.Errorf("reading image digest: %w", err)
	}

	cf, err := img.ConfigFile()
	if err != nil {
		return nil, fmt.Errorf("reading image config: %w", err)
	}
	// Overrides from cfg are merged in now and baked into state.json,
	// since peel's configuration file is deleted once the image is
	// unpacked (see run above): on future boots that skip the pull, the
	// entrypoint is resolved from this cached, already-merged runtime
	// alone.
	rt := entrypoint.Merge(state.Runtime{
		Entrypoint: cf.Config.Entrypoint,
		Cmd:        cf.Config.Cmd,
		Env:        cf.Config.Env,
		WorkingDir: cf.Config.WorkingDir,
		User:       cf.Config.User,
	}, cfg)

	layers, err := img.Layers()
	if err != nil {
		return nil, fmt.Errorf("reading image layers: %w", err)
	}
	for i, layer := range layers {
		log.Printf("unpacking layer %d/%d", i+1, len(layers))
		rc, err := layer.Uncompressed()
		if err != nil {
			return nil, fmt.Errorf("reading layer %d: %w", i+1, err)
		}
		err = rootfs.ApplyLayer(root, rc)
		closeErr := rc.Close()
		if err != nil {
			return nil, fmt.Errorf("unpacking layer %d: %w", i+1, err)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("closing layer %d: %w", i+1, closeErr)
		}
	}

	newState := &state.State{
		Reference:  cfg.Image,
		Digest:     digest.String(),
		Runtime:    rt,
		UnpackedAt: time.Now().UTC(),
	}
	if err := state.Save(statePath, newState); err != nil {
		return nil, err
	}

	log.Printf("unpacked %q (%s)", cfg.Image, digest)
	return &rt, nil
}
