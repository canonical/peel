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
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	whiteoutPrefix = ".wh."
	whiteoutOpaque = ".wh..wh..opq"
)

// protectedFiles are exact root-relative paths that a layer may never
// create, modify or delete.
var protectedFiles = map[string]bool{
	"sbin/init": true, // peel itself.
}

// mustRemainDirs are top-level root-relative paths that must always stay a
// real directory. Many Debian/Ubuntu-derived images ("usrmerge") ship /sbin
// (and /bin, /lib, ...) as a symlink into /usr; if a layer were allowed to
// replace our /sbin directory with such a symlink, /sbin/init would stop
// resolving to peel, even though the literal path "sbin/init" was never
// touched.
var mustRemainDirs = map[string]bool{
	"sbin": true,
	"peel": true,
}

// protectedDirs are root-relative directories (and everything under them)
// that a layer may never create, modify or delete.
var protectedDirs = []string{
	"peel", // peel's own config and state.
}

// isProtected reports whether the root-relative path rel (no leading slash)
// must be left untouched by layer extraction.
func isProtected(rel string) bool {
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		return false
	}
	if protectedFiles[rel] {
		return true
	}
	for _, d := range protectedDirs {
		if rel == d || strings.HasPrefix(rel, d+"/") {
			return true
		}
	}
	return false
}

// ApplyLayer extracts an uncompressed OCI/Docker layer tar stream onto root,
// applying whiteout files as it goes per:
// https://github.com/opencontainers/image-spec/blob/main/layer.md
func ApplyLayer(root string, r io.Reader) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("rootfs: reading tar stream: %w", err)
		}

		rel := strings.TrimPrefix(path.Clean("/"+filepath.ToSlash(hdr.Name)), "/")
		if rel == "" || rel == "." {
			continue // an entry for the layer root itself; nothing to do.
		}

		dir := path.Dir(rel)
		if dir == "." {
			dir = ""
		}
		base := path.Base(rel)

		switch {
		case base == whiteoutOpaque:
			if isProtected(dir) {
				continue
			}
			if err := clearDir(filepath.Join(root, dir), dir); err != nil {
				return fmt.Errorf("rootfs: applying opaque whiteout in /%s: %w", dir, err)
			}
		case strings.HasPrefix(base, whiteoutPrefix):
			deleted := path.Join(dir, strings.TrimPrefix(base, whiteoutPrefix))
			if isProtected(deleted) || mustRemainDirs[deleted] {
				continue
			}
			if err := os.RemoveAll(filepath.Join(root, deleted)); err != nil {
				return fmt.Errorf("rootfs: applying whiteout for /%s: %w", deleted, err)
			}
		case isProtected(rel):
			log.Printf("rootfs: refusing to let a layer write to protected path /%s", rel)
		case mustRemainDirs[rel] && hdr.Typeflag != tar.TypeDir:
			log.Printf("rootfs: refusing to replace protected directory /%s with a non-directory (type %q)", rel, string(hdr.Typeflag))
		default:
			if err := extractEntry(root, rel, hdr, tr); err != nil {
				return fmt.Errorf("rootfs: extracting %s: %w", hdr.Name, err)
			}
		}
	}
}

// clearDir removes the current contents of dirPath (an opaque whiteout),
// without removing protected paths even if they happen to live under it.
func clearDir(dirPath, relDir string) error {
	entries, err := os.ReadDir(dirPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		childRel := path.Join(relDir, e.Name())
		if isProtected(childRel) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dirPath, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func extractEntry(root, rel string, hdr *tar.Header, r io.Reader) error {
	target := filepath.Join(root, rel)

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}

	// If an incompatible entry already exists (e.g. a layer replaces a
	// directory with a file), get rid of it first. Existing directories are
	// kept so that files added to them by earlier layers survive.
	if fi, err := os.Lstat(target); err == nil {
		if !(hdr.Typeflag == tar.TypeDir && fi.IsDir()) {
			if err := os.RemoveAll(target); err != nil {
				return err
			}
		}
	}

	switch hdr.Typeflag {
	case tar.TypeDir:
		if err := os.MkdirAll(target, fs.FileMode(hdr.Mode&0o7777)); err != nil {
			return err
		}
	case tar.TypeReg, tar.TypeRegA:
		f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fs.FileMode(hdr.Mode&0o7777))
		if err != nil {
			return err
		}
		_, cerr := io.Copy(f, r)
		if err := f.Close(); err != nil && cerr == nil {
			cerr = err
		}
		if cerr != nil {
			return cerr
		}
	case tar.TypeSymlink:
		if err := os.Symlink(hdr.Linkname, target); err != nil {
			return err
		}
	case tar.TypeLink:
		linkRel := strings.TrimPrefix(path.Clean("/"+filepath.ToSlash(hdr.Linkname)), "/")
		if err := os.Link(filepath.Join(root, linkRel), target); err != nil {
			return err
		}
	case tar.TypeChar:
		dev := int(unix.Mkdev(uint32(hdr.Devmajor), uint32(hdr.Devminor)))
		if err := unix.Mknod(target, uint32(hdr.Mode&0o7777)|unix.S_IFCHR, dev); err != nil {
			return err
		}
	case tar.TypeBlock:
		dev := int(unix.Mkdev(uint32(hdr.Devmajor), uint32(hdr.Devminor)))
		if err := unix.Mknod(target, uint32(hdr.Mode&0o7777)|unix.S_IFBLK, dev); err != nil {
			return err
		}
	case tar.TypeFifo:
		if err := unix.Mknod(target, uint32(hdr.Mode&0o7777)|unix.S_IFIFO, 0); err != nil {
			return err
		}
	case tar.TypeXHeader, tar.TypeXGlobalHeader, tar.TypeGNULongName, tar.TypeGNULongLink:
		// Handled transparently by archive/tar; should not surface here,
		// but ignore defensively rather than fail the whole unpack.
		return nil
	default:
		log.Printf("rootfs: skipping unsupported tar entry %q (type %q)", hdr.Name, string(hdr.Typeflag))
		return nil
	}

	if err := os.Lchown(target, hdr.Uid, hdr.Gid); err != nil {
		log.Printf("rootfs: chown %s: %v", target, err)
	}
	if hdr.Typeflag != tar.TypeSymlink {
		if err := os.Chmod(target, fs.FileMode(hdr.Mode&0o7777)); err != nil {
			log.Printf("rootfs: chmod %s: %v", target, err)
		}
	}
	applyXattrs(target, hdr)
	setTimes(target, hdr.ModTime)

	return nil
}

func applyXattrs(target string, hdr *tar.Header) {
	const prefix = "SCHILY.xattr."
	for k, v := range hdr.PAXRecords {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		name := strings.TrimPrefix(k, prefix)
		if err := unix.Lsetxattr(target, name, []byte(v), 0); err != nil {
			log.Printf("rootfs: setxattr %s %s: %v", target, name, err)
		}
	}
}

func setTimes(target string, t time.Time) {
	if t.IsZero() {
		return
	}
	ts := unix.NsecToTimespec(t.UnixNano())
	if err := unix.UtimesNanoAt(unix.AT_FDCWD, target, []unix.Timespec{ts, ts}, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		log.Printf("rootfs: setting mtime on %s: %v", target, err)
	}
}
