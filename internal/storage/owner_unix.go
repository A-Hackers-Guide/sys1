//go:build unix

package storage

import (
	"io/fs"
	"os"
	"syscall"
)

func fileOwner(info fs.FileInfo) []int {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return []int{int(st.Uid), int(st.Gid)}
	}
	return nil
}

// restoreOwner gives a restored path back its owner. Only root can do
// that; anyone else keeps the files as their own.
func restoreOwner(path string, own []int) {
	if len(own) == 2 && os.Geteuid() == 0 {
		os.Lchown(path, own[0], own[1])
	}
}
