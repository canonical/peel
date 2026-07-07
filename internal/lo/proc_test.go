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

package lo

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestParseHexIP(t *testing.T) {
	cases := []struct {
		hex  string
		want string
	}{
		{"0100007F", "127.0.0.1"},
		{"00000000", "0.0.0.0"},
		{"00000000000000000000000001000000", "::1"},
		{"00000000000000000000000000000000", "::"},
	}
	for _, c := range cases {
		ip, err := parseHexIP(c.hex)
		if err != nil {
			t.Fatalf("parseHexIP(%q): %v", c.hex, err)
		}
		if ip.String() != c.want {
			t.Fatalf("parseHexIP(%q) = %s, want %s", c.hex, ip, c.want)
		}
	}
}

func TestParseHexIPInvalid(t *testing.T) {
	if _, err := parseHexIP("zz"); err == nil {
		t.Fatal("expected an error for non-hex input")
	}
	if _, err := parseHexIP("0011"); err == nil {
		t.Fatal("expected an error for a length that isn't a multiple of 4 bytes")
	}
}

func writeProcFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "net")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestScanProcFileTCP(t *testing.T) {
	// A LISTEN socket on 0.0.0.0:8080, an ESTABLISHED connection on
	// 127.0.0.1:9090 (should be ignored: wrong state), and a LISTEN
	// socket bound to 10.0.0.1, a specific, non-loopback address (should
	// be ignored: not loopback/wildcard).
	content := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: 00000000:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12345 1 0 100 0 0 10 0\n" +
		"   1: 0100007F:2382 0100007F:1234 01 00000000:00000000 00:00000000 00000000     0        0 12346 1 0 100 0 0 10 0\n" +
		"   2: 0100000A:1F91 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12347 1 0 100 0 0 10 0\n"
	path := writeProcFile(t, content)

	got, err := scanProcFile(path, TCP, V4)
	if err != nil {
		t.Fatalf("scanProcFile: %v", err)
	}
	want := map[Listen]bool{{Stack: V4, Proto: TCP, Addr: "0.0.0.0", Port: 8080}: true}
	if len(got) != len(want) || !got[Listen{Stack: V4, Proto: TCP, Addr: "0.0.0.0", Port: 8080}] {
		t.Fatalf("scanProcFile = %v, want %v", got, want)
	}
}

func TestScanProcFileUDP(t *testing.T) {
	// Two unconnected (state 07) UDP sockets: one bound to the wildcard
	// address (::) on port 0x1F90 (8080), one bound to loopback (::1) on
	// port 0x1F91 (8081).
	content := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: 00000000000000000000000000000000:1F90 00000000000000000000000000000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 12345 2 0 0 0 0\n" +
		"   1: 00000000000000000000000001000000:1F91 00000000000000000000000000000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 12346 2 0 0 0 0\n"
	path := writeProcFile(t, content)

	got, err := scanProcFile(path, UDP, V6)
	if err != nil {
		t.Fatalf("scanProcFile: %v", err)
	}
	want := map[Listen]bool{
		{Stack: V6, Proto: UDP, Addr: "::", Port: 8080}:  true,
		{Stack: V6, Proto: UDP, Addr: "::1", Port: 8081}: true,
	}
	if len(got) != len(want) {
		t.Fatalf("scanProcFile = %v, want %v", got, want)
	}
	for l := range want {
		if !got[l] {
			t.Fatalf("scanProcFile = %v, want it to contain %v", got, l)
		}
	}
}

func TestScanProcFileMissing(t *testing.T) {
	got, err := scanProcFile(filepath.Join(t.TempDir(), "does-not-exist"), TCP, V6)
	if err != nil {
		t.Fatalf("scanProcFile: %v", err)
	}
	if got != nil {
		t.Fatalf("scanProcFile of a missing file = %v, want nil", got)
	}
}

func TestScanProcNetIntegration(t *testing.T) {
	// Best-effort sanity check against the real /proc/net/tcp, when
	// available: it should parse without error and, since this test
	// itself doesn't open sockets, is not required to find anything in
	// particular.
	if _, err := os.Stat("/proc/net/tcp"); err != nil {
		t.Skip("no /proc/net/tcp on this system")
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := uint16(ln.Addr().(*net.TCPAddr).Port)

	got, err := scanProcNet()
	if err != nil {
		t.Fatalf("scanProcNet: %v", err)
	}
	if !got[Listen{Stack: V4, Proto: TCP, Addr: "127.0.0.1", Port: port}] {
		t.Fatalf("scanProcNet did not find our own listener on port %d: %v", port, got)
	}
}
