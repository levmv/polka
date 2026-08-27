//go:build linux || darwin

package fsprofile

import "golang.org/x/sys/unix"

func diskUsage(path string) (Usage, bool) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return Usage{}, false
	}
	// Bsize is the fundamental block size; Bavail is blocks available to an
	// unprivileged caller (Bfree includes root-reserved space, which overstates
	// what the library can actually use).
	bsize := uint64(st.Bsize)
	return Usage{
		TotalBytes: uint64(st.Blocks) * bsize,
		FreeBytes:  uint64(st.Bavail) * bsize,
	}, true
}
