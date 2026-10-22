// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package protosrc

import (
	"os"
	"syscall"
)

// lockFile takes an exclusive advisory lock on f, blocking until it is held.
func lockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}

func unlockFile(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
