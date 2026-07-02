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

package rootfs

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var fixedTime = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

func buildTar(t *testing.T, entries []tar.Header, contents map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, hdr := range entries {
		h := hdr
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(contents[h.Name]))
		}
		if h.ModTime.IsZero() {
			h.ModTime = fixedTime
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatalf("writing header %s: %v", h.Name, err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(contents[h.Name])); err != nil {
				t.Fatalf("writing content %s: %v", h.Name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("closing tar: %v", err)
	}
	return buf.Bytes()
}

func TestApplyLayerBasic(t *testing.T) {
	root := t.TempDir()

	data := buildTar(t, []tar.Header{
		{Name: "etc/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "etc/motd", Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: "bin/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "bin/app", Typeflag: tar.TypeReg, Mode: 0o755},
		{Name: "lib/libfoo.so", Typeflag: tar.TypeSymlink, Linkname: "libfoo.so.1", Mode: 0o777},
	}, map[string]string{
		"etc/motd": "hello\n",
		"bin/app":  "#!/bin/sh\necho hi\n",
	})

	if err := ApplyLayer(root, bytes.NewReader(data)); err != nil {
		t.Fatalf("ApplyLayer: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(root, "etc/motd"))
	if err != nil {
		t.Fatalf("reading etc/motd: %v", err)
	}
	if string(b) != "hello\n" {
		t.Fatalf("etc/motd content = %q", b)
	}

	fi, err := os.Stat(filepath.Join(root, "bin/app"))
	if err != nil {
		t.Fatalf("stat bin/app: %v", err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("bin/app mode = %v, want 0755", fi.Mode().Perm())
	}

	link, err := os.Readlink(filepath.Join(root, "lib/libfoo.so"))
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if link != "libfoo.so.1" {
		t.Fatalf("symlink target = %q", link)
	}
}

func TestApplyLayerWhiteout(t *testing.T) {
	root := t.TempDir()

	base := buildTar(t, []tar.Header{
		{Name: "app/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "app/keep", Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: "app/remove", Typeflag: tar.TypeReg, Mode: 0o644},
	}, map[string]string{
		"app/keep":   "a",
		"app/remove": "b",
	})
	if err := ApplyLayer(root, bytes.NewReader(base)); err != nil {
		t.Fatalf("ApplyLayer(base): %v", err)
	}

	overlay := buildTar(t, []tar.Header{
		{Name: "app/.wh.remove", Typeflag: tar.TypeReg, Mode: 0o644},
	}, nil)
	if err := ApplyLayer(root, bytes.NewReader(overlay)); err != nil {
		t.Fatalf("ApplyLayer(overlay): %v", err)
	}

	if _, err := os.Stat(filepath.Join(root, "app/remove")); !os.IsNotExist(err) {
		t.Fatalf("app/remove should have been removed by whiteout, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "app/.wh.remove")); !os.IsNotExist(err) {
		t.Fatalf("whiteout marker itself should not be materialized, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "app/keep")); err != nil {
		t.Fatalf("app/keep should still exist: %v", err)
	}
}

func TestApplyLayerOpaqueWhiteout(t *testing.T) {
	root := t.TempDir()

	base := buildTar(t, []tar.Header{
		{Name: "app/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "app/old1", Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: "app/old2", Typeflag: tar.TypeReg, Mode: 0o644},
	}, map[string]string{"app/old1": "x", "app/old2": "y"})
	if err := ApplyLayer(root, bytes.NewReader(base)); err != nil {
		t.Fatalf("ApplyLayer(base): %v", err)
	}

	overlay := buildTar(t, []tar.Header{
		{Name: "app/.wh..wh..opq", Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: "app/new", Typeflag: tar.TypeReg, Mode: 0o644},
	}, map[string]string{"app/new": "z"})
	if err := ApplyLayer(root, bytes.NewReader(overlay)); err != nil {
		t.Fatalf("ApplyLayer(overlay): %v", err)
	}

	for _, gone := range []string{"app/old1", "app/old2", "app/.wh..wh..opq"} {
		if _, err := os.Stat(filepath.Join(root, gone)); !os.IsNotExist(err) {
			t.Fatalf("%s should be gone after opaque whiteout, err = %v", gone, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "app/new")); err != nil {
		t.Fatalf("app/new should exist: %v", err)
	}
}

func TestApplyLayerProtectsPeel(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "peel"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "peel", "state.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sbin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sbin", "init"), []byte("peel-binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	data := buildTar(t, []tar.Header{
		{Name: "sbin/init", Typeflag: tar.TypeReg, Mode: 0o755},
		{Name: "peel/state.json", Typeflag: tar.TypeReg, Mode: 0o644},
	}, map[string]string{
		"sbin/init":       "malicious",
		"peel/state.json": "malicious",
	})
	if err := ApplyLayer(root, bytes.NewReader(data)); err != nil {
		t.Fatalf("ApplyLayer: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(root, "sbin", "init"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "peel-binary" {
		t.Fatalf("/sbin/init was overwritten by a layer: %q", b)
	}

	b, err = os.ReadFile(filepath.Join(root, "peel", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "{}" {
		t.Fatalf("/peel/state.json was overwritten by a layer: %q", b)
	}
}

func TestApplyLayerRefusesUsrmergeSymlinkForSbin(t *testing.T) {
	root := t.TempDir()
	mustMkdirAll(t, filepath.Join(root, "sbin"))
	mustWriteFile(t, filepath.Join(root, "sbin", "init"), "peel-binary")

	data := buildTar(t, []tar.Header{
		{Name: "sbin", Typeflag: tar.TypeSymlink, Linkname: "usr/sbin", Mode: 0o777},
	}, nil)
	if err := ApplyLayer(root, bytes.NewReader(data)); err != nil {
		t.Fatalf("ApplyLayer: %v", err)
	}

	fi, err := os.Lstat(filepath.Join(root, "sbin"))
	if err != nil {
		t.Fatalf("stat /sbin: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("/sbin was replaced with a symlink, usrmerge protection failed")
	}

	b, err := os.ReadFile(filepath.Join(root, "sbin", "init"))
	if err != nil {
		t.Fatalf("reading /sbin/init: %v", err)
	}
	if string(b) != "peel-binary" {
		t.Fatalf("/sbin/init content changed: %q", b)
	}
}

func TestApplyLayerRefusesWhiteoutOfSbin(t *testing.T) {
	root := t.TempDir()
	mustMkdirAll(t, filepath.Join(root, "sbin"))
	mustWriteFile(t, filepath.Join(root, "sbin", "init"), "peel-binary")

	data := buildTar(t, []tar.Header{
		{Name: ".wh.sbin", Typeflag: tar.TypeReg, Mode: 0o644},
	}, nil)
	if err := ApplyLayer(root, bytes.NewReader(data)); err != nil {
		t.Fatalf("ApplyLayer: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(root, "sbin", "init"))
	if err != nil {
		t.Fatalf("/sbin/init should still exist: %v", err)
	}
	if string(b) != "peel-binary" {
		t.Fatalf("/sbin/init content changed: %q", b)
	}
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
