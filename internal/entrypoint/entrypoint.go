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
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/canonical/peel/internal/config"
	"github.com/canonical/peel/internal/state"
)

// Spec is a fully resolved command ready to be started.
type Spec struct {
	Path   string // absolute path to the executable, under Root.
	Argv   []string
	Env    []string
	Dir    string
	UID    uint32
	GID    uint32
	Groups []uint32
}

// defaultPath is used when neither the image nor peel's configuration set
// a PATH environment variable, mirroring most container runtimes' default.
const defaultPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// Merge combines an image's cached runtime configuration with any
// overrides from cfg, producing the runtime configuration that should be
// persisted to state.json. This is only ever done once, at the point an
// image is (re)pulled: peel's own configuration file is deleted once the
// image is unpacked (see cmd/peel), so overrides are baked into state.json
// rather than being re-applied from configuration on every boot.
func Merge(rt state.Runtime, cfg *config.Config) state.Runtime {
	out := rt
	if len(cfg.Entrypoint) > 0 {
		out.Entrypoint = cfg.Entrypoint
	}
	if len(cfg.Cmd) > 0 {
		out.Cmd = cfg.Cmd
	}
	out.Env = mergeEnv(rt.Env, cfg.Env)
	if cfg.WorkingDir != "" {
		out.WorkingDir = cfg.WorkingDir
	}
	if cfg.User != "" {
		out.User = cfg.User
	}
	return out
}

// Resolve resolves rt — normally loaded straight from state.json, already
// reflecting any configuration overrides applied by Merge at the time the
// image was pulled — against the filesystem rooted at root (normally
// "/"), including looking up the executable and any named user/group.
func Resolve(root string, rt state.Runtime) (*Spec, error) {
	argv := make([]string, 0, len(rt.Entrypoint)+len(rt.Cmd))
	argv = append(argv, rt.Entrypoint...)
	argv = append(argv, rt.Cmd...)
	if len(argv) == 0 {
		return nil, fmt.Errorf("entrypoint: image has no Entrypoint or Cmd, and none was configured as an override")
	}

	dir := rt.WorkingDir
	if dir == "" {
		dir = "/"
	}

	env := append([]string{}, rt.Env...)
	if lookupEnv(env, "PATH") == "" {
		env = append(env, "PATH="+defaultPath)
	}

	uid, gid, err := resolveUser(root, rt.User)
	if err != nil {
		return nil, err
	}

	path, err := lookPath(root, argv[0], lookupEnv(env, "PATH"))
	if err != nil {
		return nil, fmt.Errorf("entrypoint: %w", err)
	}

	return &Spec{
		Path: path,
		Argv: argv,
		Env:  env,
		Dir:  dir,
		UID:  uid,
		GID:  gid,
	}, nil
}

// mergeEnv appends override entries (as "KEY=VALUE") on top of base,
// replacing any existing entry for the same key.
func mergeEnv(base, override []string) []string {
	out := append([]string{}, base...)
	for _, kv := range override {
		key, _, _ := strings.Cut(kv, "=")
		replaced := false
		for i, existing := range out {
			ek, _, _ := strings.Cut(existing, "=")
			if ek == key {
				out[i] = kv
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, kv)
		}
	}
	return out
}

func lookupEnv(env []string, key string) string {
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if ok && k == key {
			return v
		}
	}
	return ""
}

// lookPath resolves file to an absolute path under root, using pathEnv (a
// colon-separated list of root-relative directories) when file doesn't
// itself contain a slash.
func lookPath(root, file, pathEnv string) (string, error) {
	if strings.Contains(file, "/") {
		p := filepath.Join(root, file)
		if err := checkExecutable(p); err != nil {
			return "", err
		}
		return p, nil
	}
	for dir := range strings.SplitSeq(pathEnv, ":") {
		if dir == "" {
			continue
		}
		p := filepath.Join(root, dir, file)
		if err := checkExecutable(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("%q: executable file not found in $PATH", file)
}

func checkExecutable(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return fmt.Errorf("%s is a directory", path)
	}
	if fi.Mode()&0o111 == 0 {
		return fmt.Errorf("%s is not executable", path)
	}
	return nil
}

// resolveUser resolves a "user[:group]" spec (each of which may be numeric
// or a name from /etc/passwd or /etc/group) against the filesystem rooted
// at root. An empty spec resolves to root (0:0). Supplementary groups are
// not resolved.
func resolveUser(root, spec string) (uid, gid uint32, err error) {
	if spec == "" {
		return 0, 0, nil
	}

	userPart, groupPart, hasGroup := strings.Cut(spec, ":")

	var found bool
	if n, cerr := strconv.ParseUint(userPart, 10, 32); cerr == nil {
		uid, gid = uint32(n), 0
		found = true
	}
	if pu, ok, perr := lookupPasswd(root, userPart); perr == nil && ok {
		uid, gid = pu.uid, pu.gid
		found = true
	} else if perr != nil {
		return 0, 0, perr
	}
	if !found {
		return 0, 0, fmt.Errorf("entrypoint: user %q not found in /etc/passwd", userPart)
	}

	if hasGroup {
		if n, cerr := strconv.ParseUint(groupPart, 10, 32); cerr == nil {
			gid = uint32(n)
		} else if g, ok, gerr := lookupGroup(root, groupPart); gerr == nil && ok {
			gid = g
		} else if gerr != nil {
			return 0, 0, gerr
		} else {
			return 0, 0, fmt.Errorf("entrypoint: group %q not found in /etc/group", groupPart)
		}
	}

	return uid, gid, nil
}

type passwdUser struct {
	uid, gid uint32
}

// lookupPasswd looks up name in root/etc/passwd. A missing /etc/passwd is
// not an error: it just means the lookup can't succeed by name.
func lookupPasswd(root, name string) (passwdUser, bool, error) {
	f, err := os.Open(filepath.Join(root, "etc", "passwd"))
	if os.IsNotExist(err) {
		return passwdUser{}, false, nil
	}
	if err != nil {
		return passwdUser{}, false, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) < 4 || fields[0] != name {
			continue
		}
		uid, err := strconv.ParseUint(fields[2], 10, 32)
		if err != nil {
			continue
		}
		gid, err := strconv.ParseUint(fields[3], 10, 32)
		if err != nil {
			continue
		}
		return passwdUser{uid: uint32(uid), gid: uint32(gid)}, true, nil
	}
	return passwdUser{}, false, sc.Err()
}

// lookupGroup looks up name in root/etc/group. A missing /etc/group is not
// an error: it just means the lookup can't succeed by name.
func lookupGroup(root, name string) (uint32, bool, error) {
	f, err := os.Open(filepath.Join(root, "etc", "group"))
	if os.IsNotExist(err) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) < 3 || fields[0] != name {
			continue
		}
		gid, err := strconv.ParseUint(fields[2], 10, 32)
		if err != nil {
			continue
		}
		return uint32(gid), true, nil
	}
	return 0, false, sc.Err()
}
