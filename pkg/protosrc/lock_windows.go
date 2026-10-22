// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package protosrc

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockFile takes an exclusive lock on the first byte of f, blocking until it
// is held; the whole file is never written, so one byte is the whole lock.
func lockFile(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &windows.Overlapped{})
}

func unlockFile(f *os.File) {
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &windows.Overlapped{})
}
