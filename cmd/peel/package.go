// copyright (c) 2026 canonical ltd
//
// this program is free software: you can redistribute it and/or modify
// it under the terms of the gnu general public license version 3 as
// published by the free software foundation.
//
// this program is distributed in the hope that it will be useful,
// but without any warranty; without even the implied warranty of
// merchantability or fitness for a particular purpose.  see the
// gnu general public license for more details.
//
// you should have received a copy of the gnu general public license
// along with this program.  if not, see <http://www.gnu.org/licenses/>.

// Command peel is a minimal init (PID 1) for LXD containers that pulls an
// OCI image from a registry, unpacks its layers onto the container's
// rootfs, and execs its entrypoint.
//
// See https://canonical.com/lxd/docs/default/container-environment/ for the
// environment LXD guarantees for whatever it finds at /sbin/init, which is
// where peel expects to be installed.
package main
