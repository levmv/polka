package fsprofile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type Kind string

const (
	KindUnknown Kind = "unknown"
	KindLocal   Kind = "local"
	KindNetwork Kind = "network"
)

// Info is a compact startup-time filesystem profile for one important path.
// Type is the kernel/mount fstype when available, and is intended for logs and
// tests; runtime behavior should usually key off Kind.
type Info struct {
	Path string
	Kind Kind
	Type string
}

func Detect(path string) Info {
	return detect(path)
}

func (i Info) IsNetwork() bool {
	return i.Kind == KindNetwork
}

func (i Info) TypeOrUnknown() string {
	if i.Type == "" {
		return "unknown"
	}
	return i.Type
}

// Usage is the total and caller-available space for a filesystem, in bytes.
type Usage struct {
	TotalBytes uint64
	FreeBytes  uint64
}

// DiskUsage reports total and available bytes for the filesystem holding path.
// ok is false when the platform cannot report it or the path is unreachable (an
// offline NAS mountpoint), which callers surface as "free space unknown" rather
// than as zero. Unlike Detect it does not walk up to an existing ancestor: free
// space is meaningful only for the exact path, so an unreachable root reports
// unknown rather than the free space of some local parent directory.
func DiskUsage(path string) (Usage, bool) {
	return diskUsage(path)
}

func kindForFSType(fsType string) Kind {
	fsType = strings.ToLower(strings.TrimSpace(fsType))
	switch {
	case fsType == "":
		return KindUnknown
	case fsType == "nfs", strings.HasPrefix(fsType, "nfs"):
		return KindNetwork
	case fsType == "cifs", fsType == "smbfs", fsType == "smb3", strings.HasPrefix(fsType, "smb"):
		return KindNetwork
	case fsType == "9p", fsType == "ceph", fsType == "glusterfs", fsType == "webdav", fsType == "afpfs":
		return KindNetwork
	case fsType == "virtiofs":
		// virtiofs is a host/guest shared filesystem, but it is designed to expose
		// local filesystem semantics. Keep WAL enabled unless a concrete runtime
		// issue proves this should become conservative for Polka's workload.
		return KindLocal
	case isNetworkFUSE(fsType):
		return KindNetwork
	case isLocalFUSE(fsType):
		return KindLocal
	case fsType == "fuse" || strings.HasPrefix(fsType, "fuse."):
		// Generic/unknown FUSE is conservative: a false network classification is
		// slower, but false local can leave SQLite WAL on SSHFS/rclone/WebDAV.
		return KindNetwork
	default:
		return KindLocal
	}
}

func isNetworkFUSE(fsType string) bool {
	for _, token := range []string{
		"sshfs",
		"rclone",
		"davfs",
		"curlftpfs",
		"ftpfs",
		"sftp",
		"s3fs",
		"goofys",
		"gcsfuse",
		"smbnetfs",
		"glusterfs",
	} {
		if strings.Contains(fsType, token) {
			return true
		}
	}
	return false
}

func isLocalFUSE(fsType string) bool {
	switch fsType {
	case "fuseblk",
		"ntfs",
		"ntfs3",
		"ntfs-3g",
		"fuse.ntfs-3g",
		"exfat",
		"exfat-fuse",
		"fuse.exfat",
		"fuse.exfat-fuse",
		"mergerfs",
		"fuse.mergerfs",
		"gocryptfs",
		"fuse.gocryptfs",
		"bindfs",
		"fuse.bindfs",
		"encfs",
		"fuse.encfs",
		"cryfs",
		"fuse.cryfs",
		"unionfs",
		"unionfs-fuse",
		"fuse.unionfs",
		"fuse.unionfs-fuse":
		return true
	default:
		return false
	}
}

func nearestExisting(path string) (string, error) {
	if path == "" {
		path = "."
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path, err
	}
	for {
		if _, err := os.Stat(abs); err == nil {
			return abs, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return abs, err
		}

		parent := filepath.Dir(abs)
		if parent == abs {
			return abs, os.ErrNotExist
		}
		abs = parent
	}
}
