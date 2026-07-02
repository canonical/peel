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
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeConfigFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadRequiresImage(t *testing.T) {
	path := writeConfigFile(t, `{"image":""}`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected an error when image is empty")
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "does-not-exist.json")); err == nil {
		t.Fatal("expected an error when the config file doesn't exist")
	}
}

func TestLoadDefaults(t *testing.T) {
	path := writeConfigFile(t, `{"image":"example.com/app:latest"}`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Image != "example.com/app:latest" {
		t.Fatalf("Image = %q", cfg.Image)
	}
	if cfg.Insecure {
		t.Fatalf("Insecure should default to false")
	}
	if cfg.Entrypoint != nil || cfg.Cmd != nil || cfg.Env != nil {
		t.Fatalf("array overrides should default to nil: %+v", cfg)
	}
}

func TestLoadFullConfig(t *testing.T) {
	path := writeConfigFile(t, `{
		"image": "example.com/app:latest",
		"platform": "linux/arm64",
		"insecure": "true",
		"username": "alice",
		"password": "s3cret",
		"working_dir": "/srv",
		"user": "1000:1000",
		"entrypoint": ["/bin/app"],
		"cmd": ["--flag", "value"],
		"env": ["FOO=bar"]
	}`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Platform != "linux/arm64" || !cfg.Insecure || cfg.Username != "alice" ||
		cfg.Password != "s3cret" || cfg.WorkingDir != "/srv" || cfg.User != "1000:1000" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if !reflect.DeepEqual(cfg.Entrypoint, []string{"/bin/app"}) {
		t.Fatalf("Entrypoint = %v", cfg.Entrypoint)
	}
	if !reflect.DeepEqual(cfg.Cmd, []string{"--flag", "value"}) {
		t.Fatalf("Cmd = %v", cfg.Cmd)
	}
	if !reflect.DeepEqual(cfg.Env, []string{"FOO=bar"}) {
		t.Fatalf("Env = %v", cfg.Env)
	}
}

func TestLoadEmptyArrayOverridesAreNil(t *testing.T) {
	path := writeConfigFile(t, `{"image":"example.com/app:latest","cmd":[]}`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Cmd != nil {
		t.Fatalf("Cmd = %v, want nil", cfg.Cmd)
	}
}

func TestLoadRejectsInvalidJSON(t *testing.T) {
	path := writeConfigFile(t, `{not json`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected an error for invalid JSON")
	}
}
