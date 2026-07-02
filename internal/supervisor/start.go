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

package supervisor

import (
	"os"
	"os/exec"
	"syscall"

	"github.com/canonical/peel/internal/entrypoint"
)

// start launches the resolved entrypoint. It intentionally does not put the
// child into its own session: it inherits peel's session and controlling
// terminal (LXD's /dev/console), so that "lxc console" works as expected.
func start(spec *entrypoint.Spec) (*exec.Cmd, error) {
	cmd := &exec.Cmd{
		Path:   spec.Path,
		Args:   spec.Argv,
		Env:    spec.Env,
		Dir:    spec.Dir,
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		SysProcAttr: &syscall.SysProcAttr{
			Credential: &syscall.Credential{
				Uid:    spec.UID,
				Gid:    spec.GID,
				Groups: spec.Groups,
			},
		},
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}
