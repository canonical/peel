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

package entrypoint

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/canonical/peel/internal/config"
	"github.com/canonical/peel/internal/state"
)

func newFakeRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mustMkdirAll(t, filepath.Join(root, "usr", "bin"))
	mustWriteExecutable(t, filepath.Join(root, "usr", "bin", "app"))
	mustMkdirAll(t, filepath.Join(root, "etc"))
	mustWriteFile(t, filepath.Join(root, "etc", "passwd"),
		"root:x:0:0:root:/root:/bin/sh\napp:x:1000:1000:App:/home/app:/bin/sh\n")
	mustWriteFile(t, filepath.Join(root, "etc", "group"),
		"root:x:0:\napp:x:1000:\nstaff:x:50:\n")
	return root
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustWriteExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestResolveUsesImageDefaults(t *testing.T) {
	root := newFakeRoot(t)
	rt := state.Runtime{
		Entrypoint: []string{"/usr/bin/app"},
		Cmd:        []string{"--flag"},
		Env:        []string{"FOO=bar"},
		WorkingDir: "/home/app",
		User:       "app",
	}

	spec, err := Resolve(root, rt)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if spec.Path != filepath.Join(root, "usr/bin/app") {
		t.Fatalf("Path = %q", spec.Path)
	}
	if !reflect.DeepEqual(spec.Argv, []string{"/usr/bin/app", "--flag"}) {
		t.Fatalf("Argv = %v", spec.Argv)
	}
	if spec.Dir != "/home/app" {
		t.Fatalf("Dir = %q", spec.Dir)
	}
	if spec.UID != 1000 || spec.GID != 1000 {
		t.Fatalf("UID:GID = %d:%d, want 1000:1000", spec.UID, spec.GID)
	}
	if lookupEnv(spec.Env, "FOO") != "bar" {
		t.Fatalf("FOO env missing/wrong: %v", spec.Env)
	}
	if lookupEnv(spec.Env, "PATH") == "" {
		t.Fatalf("PATH should have a default value: %v", spec.Env)
	}
}

func TestResolveConfigOverrides(t *testing.T) {
	root := newFakeRoot(t)
	rt := state.Runtime{
		Entrypoint: []string{"/usr/bin/app"},
		Env:        []string{"FOO=bar"},
		User:       "app",
	}
	cfg := &config.Config{
		Image:      "example.com/app:latest",
		Cmd:        []string{"--override"},
		Env:        []string{"FOO=baz"},
		WorkingDir: "/tmp",
		User:       "app:staff",
	}

	spec, err := Resolve(root, Merge(rt, cfg))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if !reflect.DeepEqual(spec.Argv, []string{"/usr/bin/app", "--override"}) {
		t.Fatalf("Argv = %v", spec.Argv)
	}
	if lookupEnv(spec.Env, "FOO") != "baz" {
		t.Fatalf("FOO should be overridden: %v", spec.Env)
	}
	if spec.Dir != "/tmp" {
		t.Fatalf("Dir = %q", spec.Dir)
	}
	if spec.UID != 1000 || spec.GID != 50 {
		t.Fatalf("UID:GID = %d:%d, want 1000:50", spec.UID, spec.GID)
	}
}

func TestResolveNoEntrypointFails(t *testing.T) {
	root := newFakeRoot(t)
	_, err := Resolve(root, state.Runtime{})
	if err == nil {
		t.Fatal("expected an error when there is no entrypoint or cmd")
	}
}

func TestResolveUnknownUserFails(t *testing.T) {
	root := newFakeRoot(t)
	rt := state.Runtime{Entrypoint: []string{"/usr/bin/app"}, User: "nosuchuser"}
	_, err := Resolve(root, rt)
	if err == nil {
		t.Fatal("expected an error for an unknown user")
	}
}

func TestMergeAppliesOverrides(t *testing.T) {
	rt := state.Runtime{
		Entrypoint: []string{"/usr/bin/app"},
		Cmd:        []string{"--default"},
		Env:        []string{"FOO=bar"},
		WorkingDir: "/home/app",
		User:       "app",
	}
	cfg := &config.Config{
		Image:      "example.com/app:latest",
		Cmd:        []string{"--override"},
		Env:        []string{"FOO=baz"},
		WorkingDir: "/tmp",
		User:       "app:staff",
	}

	merged := Merge(rt, cfg)

	if !reflect.DeepEqual(merged.Entrypoint, []string{"/usr/bin/app"}) {
		t.Fatalf("Entrypoint = %v, want unchanged", merged.Entrypoint)
	}
	if !reflect.DeepEqual(merged.Cmd, []string{"--override"}) {
		t.Fatalf("Cmd = %v", merged.Cmd)
	}
	if lookupEnv(merged.Env, "FOO") != "baz" {
		t.Fatalf("FOO should be overridden: %v", merged.Env)
	}
	if merged.WorkingDir != "/tmp" {
		t.Fatalf("WorkingDir = %q", merged.WorkingDir)
	}
	if merged.User != "app:staff" {
		t.Fatalf("User = %q", merged.User)
	}
}

func TestMergeWithoutOverridesKeepsBase(t *testing.T) {
	rt := state.Runtime{
		Entrypoint: []string{"/usr/bin/app"},
		Cmd:        []string{"--flag"},
		Env:        []string{"FOO=bar"},
		WorkingDir: "/home/app",
		User:       "app",
	}
	cfg := &config.Config{Image: "example.com/app:latest"}

	merged := Merge(rt, cfg)

	if !reflect.DeepEqual(merged.Entrypoint, rt.Entrypoint) {
		t.Fatalf("Entrypoint = %v", merged.Entrypoint)
	}
	if !reflect.DeepEqual(merged.Cmd, rt.Cmd) {
		t.Fatalf("Cmd = %v", merged.Cmd)
	}
	if merged.WorkingDir != rt.WorkingDir {
		t.Fatalf("WorkingDir = %q", merged.WorkingDir)
	}
	if merged.User != rt.User {
		t.Fatalf("User = %q", merged.User)
	}
	if lookupEnv(merged.Env, "FOO") != "bar" {
		t.Fatalf("FOO env missing/wrong: %v", merged.Env)
	}
}

func TestMergeEnv(t *testing.T) {
	got := mergeEnv([]string{"A=1", "B=2"}, []string{"B=3", "C=4"})
	want := map[string]string{"A": "1", "B": "3", "C": "4"}
	if len(got) != len(want) {
		t.Fatalf("mergeEnv = %v", got)
	}
	for _, kv := range got {
		k, v, _ := cut(kv)
		if want[k] != v {
			t.Fatalf("mergeEnv: %s = %s, want %s", k, v, want[k])
		}
	}
}

func cut(kv string) (string, string, bool) {
	for i := 0; i < len(kv); i++ {
		if kv[i] == '=' {
			return kv[:i], kv[i+1:], true
		}
	}
	return kv, "", false
}
