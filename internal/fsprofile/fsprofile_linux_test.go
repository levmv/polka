//go:build linux

package fsprofile

import (
	"strings"
	"testing"
)

func TestMountInfoFSTypeFromReaderUsesLongestMount(t *testing.T) {
	mountInfo := strings.Join([]string{
		"24 0 8:1 / / rw,relatime - ext4 /dev/sda1 rw",
		"25 24 0:42 / /mnt/books rw,relatime - nfs4 server:/books rw",
		"26 24 0:43 / /mnt/books/local rw,relatime - ext4 /dev/sdb1 rw",
	}, "\n")

	if got := mountInfoFSTypeFromReader("/mnt/books/a/library.db", strings.NewReader(mountInfo)); got != "nfs4" {
		t.Fatalf("fstype for nfs path = %q; want nfs4", got)
	}
	if got := mountInfoFSTypeFromReader("/mnt/books/local/a.db", strings.NewReader(mountInfo)); got != "ext4" {
		t.Fatalf("fstype for nested local path = %q; want ext4", got)
	}
}

func TestMountInfoFSTypeFromReaderUnescapesMountPath(t *testing.T) {
	mountInfo := "25 24 0:42 / /mnt/My\\040Books rw,relatime - cifs //nas/books rw\n"

	if got := mountInfoFSTypeFromReader("/mnt/My Books/library.db", strings.NewReader(mountInfo)); got != "cifs" {
		t.Fatalf("fstype for escaped path = %q; want cifs", got)
	}
}
