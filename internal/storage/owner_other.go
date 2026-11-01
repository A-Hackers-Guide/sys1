//go:build !unix

package storage

import "io/fs"

func fileOwner(fs.FileInfo) []int { return nil }

func restoreOwner(string, []int) {}
