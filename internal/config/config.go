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

package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Config is peel's resolved configuration for a single boot.
type Config struct {
	// Image is the OCI image reference to run, e.g.
	// "docker.io/library/nginx:1.27" or "registry.example.com/app@sha256:...".
	Image string

	// Platform optionally overrides the platform to pull, in "os/arch" or
	// "os/arch/variant" form (e.g. "linux/arm64"). Defaults to the host's.
	Platform string

	// Insecure allows talking to the registry over plain HTTP.
	Insecure bool

	// Registry credentials. Username/Password are used verbatim if set.
	// Auth, IdentityToken and RegistryToken mirror the equivalent fields of
	// a Docker config.json auth entry and are used if Username/Password are
	// not set.
	Username      string
	Password      string
	Auth          string
	IdentityToken string
	RegistryToken string

	// Entrypoint, Cmd, Env, WorkingDir and User override the equivalent
	// value from the image configuration when non-empty/non-nil.
	Entrypoint []string
	Cmd        []string
	Env        []string
	WorkingDir string
	User       string
}

// DefaultPath is the default location of peel's rendered configuration file
// inside the container's rootfs.
const DefaultPath = "/peel/config.json"

// wireConfig mirrors the JSON document produced by
// image/templates/config.tpl.
type wireConfig struct {
	Image         string   `json:"image"`
	Platform      string   `json:"platform"`
	Insecure      string   `json:"insecure"`
	Username      string   `json:"username"`
	Password      string   `json:"password"`
	Auth          string   `json:"auth"`
	IdentityToken string   `json:"identity_token"`
	RegistryToken string   `json:"registry_token"`
	WorkingDir    string   `json:"working_dir"`
	User          string   `json:"user"`
	Entrypoint    []string `json:"entrypoint"`
	Cmd           []string `json:"cmd"`
	Env           []string `json:"env"`
}

// Load reads and parses the configuration file at path.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: reading %s: %w", path, err)
	}

	var w wireConfig
	if err := json.Unmarshal(b, &w); err != nil {
		return nil, fmt.Errorf("config: parsing %s: %w", path, err)
	}

	if w.Image == "" {
		return nil, fmt.Errorf("config: %q is required (set the LXD instance config key %q)", "image", "user.oci.image")
	}

	return &Config{
		Image:         w.Image,
		Platform:      w.Platform,
		Insecure:      parseBool(w.Insecure),
		Username:      w.Username,
		Password:      w.Password,
		Auth:          w.Auth,
		IdentityToken: w.IdentityToken,
		RegistryToken: w.RegistryToken,
		WorkingDir:    w.WorkingDir,
		User:          w.User,
		Entrypoint:    orNil(w.Entrypoint),
		Cmd:           orNil(w.Cmd),
		Env:           orNil(w.Env),
	}, nil
}

func parseBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "yes":
		return true
	default:
		return false
	}
}

// orNil normalizes an empty (but non-nil) slice to nil, so callers can
// treat "not set" and "set to an empty array" the same way.
func orNil(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}
