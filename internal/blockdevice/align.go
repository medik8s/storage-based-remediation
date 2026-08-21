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

import "unsafe"

// DirectIOAlignment is the alignment used for SBR block-device I/O with
// O_DIRECT. It matches BlockSectorSize and is required by the SBR block
// format, including support for 4Kn devices.
const DirectIOAlignment = 4096

// AlignedAlloc allocates a byte slice of the given size whose starting
// address is aligned to the given boundary. alignment must be a positive
// power of 2; the function panics otherwise.
func AlignedAlloc(size, alignment int) []byte {
	if size <= 0 {
		return nil
	}
	if alignment <= 0 || alignment&(alignment-1) != 0 {
		panic("blockdevice.AlignedAlloc: alignment must be a positive power of 2")
	}
	buf := make([]byte, size+alignment)
	addr := uintptr(unsafe.Pointer(&buf[0]))
	offset := int((uintptr(alignment) - addr%uintptr(alignment)) % uintptr(alignment))
	return buf[offset : offset+size]
}

// DirectIOAlloc allocates a buffer aligned to DirectIOAlignment (4096 bytes).
// Use this for all block mode I/O buffers when the device is opened with O_DIRECT.
func DirectIOAlloc(size int) []byte {
	return AlignedAlloc(size, DirectIOAlignment)
}
