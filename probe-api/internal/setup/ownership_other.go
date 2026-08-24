//go:build !unix

package setup

import "io/fs"

func fileOwnerUID(fs.FileInfo) (uint32, bool) {
	return 0, false
}
