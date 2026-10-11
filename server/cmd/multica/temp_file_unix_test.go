//go:build unix

package main

import (
	"os"
	"syscall"
	"testing"
)

type foreignOwnerInfo struct {
	os.FileInfo
	stat syscall.Stat_t
}

func (info foreignOwnerInfo) Sys() any { return &info.stat }

func TestTempFileRejectsForeignOwner(t *testing.T) {
	info, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	foreign := foreignOwnerInfo{FileInfo: info, stat: syscall.Stat_t{Uid: uint32(os.Geteuid()) + 1}}
	if fileOwnedByCurrentUser("unused", foreign) {
		t.Fatal("another OS user's file accepted")
	}
}
