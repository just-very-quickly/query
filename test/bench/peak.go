// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

//go:build ignore

// peak runs a command and writes its wall time and peak memory to standard
// error as "peak: <seconds> <KiB>". scripts/bench.py runs every command
// through it: a command started straight from Python would be charged
// Python's own memory, which the system counts for the new process until it
// starts the command.
//
//	go build -o peak test/bench/peak.go
//	peak jq . file > out
package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: peak command [args...]")
		os.Exit(2)
	}
	cmd := exec.Command(os.Args[1], os.Args[2:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	start := time.Now()
	err := cmd.Run()
	wall := time.Since(start)
	code := 0
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			code = e.ExitCode()
		} else {
			fmt.Fprintln(os.Stderr, "peak:", err)
			os.Exit(127)
		}
	}
	kib := cmd.ProcessState.SysUsage().(*syscall.Rusage).Maxrss
	if runtime.GOOS == "darwin" {
		kib /= 1024 // bytes there
	}
	fmt.Fprintf(os.Stderr, "peak: %.6f %d\n", wall.Seconds(), kib)
	os.Exit(code)
}
