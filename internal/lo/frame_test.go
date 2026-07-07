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
	"bytes"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	messages := [][]byte{
		[]byte("hello"),
		{},
		[]byte("a slightly longer message, to be sure"),
	}
	for _, m := range messages {
		if err := writeFrame(&buf, m); err != nil {
			t.Fatalf("writeFrame: %v", err)
		}
	}

	readBuf := make([]byte, maxDatagram)
	for _, want := range messages {
		got, err := readFrame(&buf, readBuf)
		if err != nil {
			t.Fatalf("readFrame: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("readFrame = %q, want %q", got, want)
		}
	}
}

func TestReadFrameTooLarge(t *testing.T) {
	var buf bytes.Buffer
	if err := writeFrame(&buf, make([]byte, 16)); err != nil {
		t.Fatalf("writeFrame: %v", err)
	}
	if _, err := readFrame(&buf, make([]byte, 4)); err == nil {
		t.Fatal("expected an error for a frame larger than the read buffer")
	}
}
