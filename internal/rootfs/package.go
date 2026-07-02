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

// Package rootfs unpacks OCI/Docker image layers directly onto the
// container's root filesystem, and resets it in preparation for a
// different image.
//
// Because peel already *is* the container's PID 1, there is no separate
// "container filesystem" to assemble out-of-band and later switch into:
// the layers are extracted straight onto "/". The only special handling
// needed is (a) OCI whiteout files, and (b) refusing to let a layer
// overwrite the handful of paths peel needs to keep control of the
// container across restarts.
package rootfs
