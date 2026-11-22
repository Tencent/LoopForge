//go:build !unix

package httpapi

// diskSpace reports free and total bytes of the filesystem containing path.
func diskSpace(string) (free int64, total int64) { return 0, 0 }
