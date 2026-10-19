// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

//go:build !unix

package engine

// mapFile isn't available here: files are read normally.
func mapFile(string) ([]byte, func(), bool) { return nil, nil, false }
