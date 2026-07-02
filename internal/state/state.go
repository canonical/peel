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

package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Runtime holds the parts of an OCI image's runtime configuration that
// peel needs in order to exec its entrypoint. It is cached in State so that
// a skipped pull doesn't lose this information.
type Runtime struct {
	Entrypoint []string `json:"entrypoint,omitempty"`
	Cmd        []string `json:"cmd,omitempty"`
	Env        []string `json:"env,omitempty"`
	WorkingDir string   `json:"working_dir,omitempty"`
	User       string   `json:"user,omitempty"`
}

// State describes the image that is currently unpacked onto the rootfs.
type State struct {
	// Reference is the exact image reference (as configured) that was last
	// unpacked. Unpacking is skipped when this matches the current
	// configuration, without ever contacting a registry.
	Reference string `json:"reference"`

	// Digest is the resolved manifest digest of Reference at the time it
	// was unpacked, kept only for diagnostics.
	Digest string `json:"digest"`

	// Runtime is the image's cached runtime configuration.
	Runtime Runtime `json:"runtime"`

	// UnpackedAt records when the rootfs was last (re)populated.
	UnpackedAt time.Time `json:"unpacked_at"`
}

// DefaultPath is the default location of the state file inside the
// container's rootfs.
const DefaultPath = "/peel/state.json"

// Load reads the state file at path. A missing file is not an error; it
// simply returns (nil, nil), which callers should treat as "nothing has
// been unpacked yet".
func Load(path string) (*State, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("state: reading %s: %w", path, err)
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		// A corrupt state file should never prevent peel from booting the
		// container: treat it the same as "nothing unpacked yet".
		return nil, nil
	}
	return &s, nil
}

// Save writes the state file at path, replacing it atomically.
func Save(path string, s *State) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("state: marshaling: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("state: creating %s: %w", filepath.Dir(path), err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("state: writing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("state: renaming %s to %s: %w", tmp, path, err)
	}
	return nil
}
