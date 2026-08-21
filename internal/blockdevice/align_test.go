/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package blockdevice

import (
	"fmt"
	"testing"
	"unsafe"
)

func TestAlignedAlloc_Alignment(t *testing.T) {
	for _, alignment := range []int{512, 4096, 8192} {
		for _, size := range []int{512, 4096, 65536, 1024 * 1024} {
			buf := AlignedAlloc(size, alignment)
			if len(buf) != size {
				t.Errorf("AlignedAlloc(%d, %d): len=%d, want %d", size, alignment, len(buf), size)
			}
			addr := uintptr(unsafe.Pointer(&buf[0]))
			if addr%uintptr(alignment) != 0 {
				t.Errorf("AlignedAlloc(%d, %d): addr %#x not aligned to %d", size, alignment, addr, alignment)
			}
		}
	}
}

func TestAlignedAlloc_ZeroOrNegative(t *testing.T) {
	if buf := AlignedAlloc(0, 4096); buf != nil {
		t.Errorf("AlignedAlloc(0, 4096) = %v, want nil", buf)
	}
	if buf := AlignedAlloc(-1, 4096); buf != nil {
		t.Errorf("AlignedAlloc(-1, 4096) = %v, want nil", buf)
	}
}

func TestAlignedAlloc_InvalidAlignment(t *testing.T) {
	for _, alignment := range []int{0, -1, 3, 5, 6, 100} {
		alignment := alignment
		t.Run(fmt.Sprintf("alignment=%d", alignment), func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("AlignedAlloc(4096, %d) did not panic for invalid alignment", alignment)
				}
			}()
			AlignedAlloc(4096, alignment)
		})
	}
}

func TestDirectIOAlloc_Alignment(t *testing.T) {
	for _, size := range []int{4096, 65536, 1024 * 1024} {
		buf := DirectIOAlloc(size)
		if len(buf) != size {
			t.Errorf("DirectIOAlloc(%d): len=%d, want %d", size, len(buf), size)
		}
		addr := uintptr(unsafe.Pointer(&buf[0]))
		if addr%DirectIOAlignment != 0 {
			t.Errorf("DirectIOAlloc(%d): addr %#x not aligned to %d", size, addr, DirectIOAlignment)
		}
	}
}

func TestDirectIOAlloc_Writable(t *testing.T) {
	buf := DirectIOAlloc(4096)
	// Verify the buffer is writable end-to-end
	for i := range buf {
		buf[i] = byte(i)
	}
	for i := range buf {
		if buf[i] != byte(i) {
			t.Fatalf("DirectIOAlloc buffer not writable at index %d", i)
		}
	}
}
