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
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/canonical/peel/internal/entrypoint"
)

// ShutdownGrace is how long a clean shutdown waits after signalling every
// process with SIGTERM before it escalates to SIGKILL.
var ShutdownGrace = 20 * time.Second

// rtmin3 is SIGRTMIN+3 assuming glibc's usual SIGRTMIN of 34, used by some
// non-glibc lxc-init builds instead of SIGPWR. Best effort only: SIGPWR
// remains the primary channel.
const rtmin3 = syscall.Signal(37)

// Run starts spec as a child process and then supervises the container's
// process tree until it is told (by LXD, via signal) to reboot or shut
// down, at which point it calls reboot(2) and never returns.
func Run(spec *entrypoint.Spec) error {
	sigCh := make(chan os.Signal, 64)
	signal.Notify(sigCh,
		unix.SIGCHLD,
		unix.SIGINT,
		unix.SIGTERM,
		unix.SIGPWR,
		rtmin3,
	)

	cmd, err := start(spec)
	if err != nil {
		return fmt.Errorf("supervisor: starting entrypoint: %w", err)
	}
	mainPID := cmd.Process.Pid
	log.Printf("supervisor: started entrypoint pid=%d argv=%v", mainPID, spec.Argv)

	shuttingDown := false
	rebootCmd := unix.LINUX_REBOOT_CMD_POWER_OFF

	beginShutdown := func(cmd int, reason string) {
		if shuttingDown {
			return
		}
		shuttingDown = true
		rebootCmd = cmd
		log.Printf("supervisor: %s: signalling all processes", reason)
		_ = unix.Kill(-1, unix.SIGTERM)
		go func() {
			time.Sleep(ShutdownGrace)
			log.Printf("supervisor: grace period elapsed, sending SIGKILL to all processes")
			_ = unix.Kill(-1, unix.SIGKILL)
		}()
	}

	for {
		sig := <-sigCh
		switch sig {
		case unix.SIGCHLD:
			sawMain, empty := reapAll(mainPID)
			if sawMain && !shuttingDown {
				beginShutdown(unix.LINUX_REBOOT_CMD_POWER_OFF, "entrypoint exited")
			}
			if shuttingDown && empty {
				goto done
			}
		case unix.SIGINT:
			beginShutdown(unix.LINUX_REBOOT_CMD_RESTART, "received SIGINT, rebooting container")
		case unix.SIGTERM, unix.SIGPWR, rtmin3:
			beginShutdown(unix.LINUX_REBOOT_CMD_POWER_OFF, "received shutdown signal")
		default:
			continue
		}

		if shuttingDown {
			if _, empty := reapAll(mainPID); empty {
				goto done
			}
		}
	}

done:
	log.Printf("supervisor: process tree empty, calling reboot(2)")
	if err := unix.Reboot(rebootCmd); err != nil {
		// LXD normally intercepts this syscall before it does anything, so
		// getting here at all means something unusual is going on (e.g.
		// running outside of a container). Exiting is the best fallback.
		log.Printf("supervisor: reboot(2) failed: %v", err)
	}
	os.Exit(0)
	return nil
}

// reapAll reaps every exited child without blocking, returning whether
// mainPID was among them and whether there are no children left at all.
func reapAll(mainPID int) (sawMain, empty bool) {
	for {
		var ws unix.WaitStatus
		pid, err := unix.Wait4(-1, &ws, unix.WNOHANG, nil)
		switch {
		case err == unix.EINTR:
			continue
		case err == unix.ECHILD:
			return sawMain, true
		case err != nil:
			log.Printf("supervisor: wait4: %v", err)
			return sawMain, false
		case pid == 0:
			return sawMain, false
		default:
			log.Printf("supervisor: reaped pid=%d status=%s", pid, describe(ws))
			if pid == mainPID {
				sawMain = true
			}
		}
	}
}

func describe(ws unix.WaitStatus) string {
	switch {
	case ws.Exited():
		return fmt.Sprintf("exited(%d)", ws.ExitStatus())
	case ws.Signaled():
		return fmt.Sprintf("killed(%s)", ws.Signal())
	default:
		return fmt.Sprintf("status=%d", int(ws))
	}
}
