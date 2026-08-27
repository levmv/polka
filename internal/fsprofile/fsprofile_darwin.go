//go:build darwin

package fsprofile

import (
	"strings"

	"golang.org/x/sys/unix"
)

func detect(path string) Info {
	existing, err := nearestExisting(path)
	info := Info{Path: existing, Kind: KindUnknown}
	if err != nil {
		return info
	}

	var st unix.Statfs_t
	if err := unix.Statfs(existing, &st); err != nil {
		return info
	}
	info.Type = cString(st.Fstypename[:])
	info.Kind = kindForFSType(info.Type)
	return info
}

func cString(b []byte) string {
	if i := strings.IndexByte(string(b), 0); i >= 0 {
		return string(b[:i])
	}
	return string(b)
}
