//go:build unix

package main

import (
	"os"
	"syscall"
)

func fileOwnedByCurrentUser(_ string, info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}
