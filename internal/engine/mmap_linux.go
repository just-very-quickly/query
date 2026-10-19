// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"os"
	"syscall"
	"unsafe"
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
	syscall.Madvise(b, syscall.MADV_SEQUENTIAL)
	return b, func() { syscall.Munmap(b) }, true
}

// dropPages tells the system that the whole pages inside b, part of a
// mapped file, won't be needed again. They stay in the file cache but no
// longer count as the process's memory; reading them again still works.
func dropPages(b []byte) {
	ps := uintptr(os.Getpagesize())
	start := uintptr(unsafe.Pointer(unsafe.SliceData(b)))
	end := start + uintptr(len(b))
	first := (start + ps - 1) &^ (ps - 1)
	last := end &^ (ps - 1)
	if last <= first {
		return
	}
	syscall.Madvise(b[first-start:last-start], syscall.MADV_DONTNEED)
}
