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

// Package supervisor implements the minimal init behaviour peel needs to
// act as a well-behaved PID 1 for an LXD container:
//
//   - it starts the resolved entrypoint as a child process;
//   - it reaps every child, including re-parented orphans, for as long as
//     the container runs;
//   - it treats SIGINT as "reboot the container" and SIGPWR (or SIGRTMIN+3,
//     used by some libc's in place of SIGPWR) as "shut the container down
//     cleanly", per
//     https://canonical.com/lxd/docs/default/container-environment/#pid1
//   - having signalled every remaining process and waited for the
//     container's process tree to empty out, it calls reboot(2), which LXD
//     intercepts to actually restart or stop the container.
package supervisor
