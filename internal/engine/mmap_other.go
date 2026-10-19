// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package engine

// Memory mapping is used on Linux only; elsewhere files are read normally.
func mapFile(string) ([]byte, func(), bool) { return nil, nil, false }

func dropPages([]byte) {}
