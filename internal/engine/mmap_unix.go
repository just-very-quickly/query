// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package engine

import (
	"os"
	"syscall"
)

// mapFile maps a regular file into memory for reading, so chunks are cut
// from it without copying. ok is false when that isn't possible (empty
// files, pipes, devices); the caller then reads the file normally.
func mapFile(path string) (data []byte, unmap func(), ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() == 0 || st.Size() > 1<<46 {
		return nil, nil, false
	}
	b, err := syscall.Mmap(int(f.Fd()), 0, int(st.Size()), syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return nil, nil, false
	}
	return b, func() { syscall.Munmap(b) }, true
}
