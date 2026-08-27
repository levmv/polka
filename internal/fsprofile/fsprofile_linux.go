//go:build linux

package fsprofile

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func detect(path string) Info {
	existing, err := nearestExisting(path)
	info := Info{Path: existing, Kind: KindUnknown}
	if err != nil {
		return info
	}

	if typ := mountInfoFSType(existing); typ != "" {
		info.Type = typ
		info.Kind = kindForFSType(typ)
	}

	var st unix.Statfs_t
	if err := unix.Statfs(existing, &st); err != nil {
		return info
	}
	if info.Type == "" {
		info.Type = typeForMagic(st.Type)
	}
	if info.Kind == KindUnknown && info.Type != "" {
		info.Kind = kindForFSType(info.Type)
	}
	return info
}

func mountInfoFSType(path string) string {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return ""
	}
	defer f.Close()
	return mountInfoFSTypeFromReader(path, f)
}

func mountInfoFSTypeFromReader(path string, r io.Reader) string {
	path = filepath.Clean(path)
	scanner := bufio.NewScanner(r)
	var bestMount, bestType string
	for scanner.Scan() {
		line := scanner.Text()
		before0, after0, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}
		before := strings.Fields(before0)
		after := strings.Fields(after0)
		if len(before) < 5 || len(after) < 1 {
			continue
		}
		mountPoint := filepath.Clean(unescapeMountInfoPath(before[4]))
		if !pathUnderMount(path, mountPoint) {
			continue
		}
		if len(mountPoint) > len(bestMount) {
			bestMount = mountPoint
			bestType = after[0]
		}
	}
	return bestType
}

func unescapeMountInfoPath(path string) string {
	r := strings.NewReplacer(
		`\040`, " ",
		`\011`, "\t",
		`\012`, "\n",
		`\134`, `\`,
	)
	return r.Replace(path)
}

func pathUnderMount(path, mountPoint string) bool {
	rel, err := filepath.Rel(mountPoint, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func typeForMagic(t int64) string {
	switch magic := uint64(t) & 0xffffffff; magic {
	case 0x00006969:
		return "nfs"
	case 0xff534d42:
		return "cifs"
	case 0x0000517b:
		return "smb"
	case 0x65735546:
		return "fuse"
	case 0x01021997:
		return "9p"
	case 0x5346414f:
		return "afs"
	case 0x73757245:
		return "coda"
	case 0x0000564c:
		return "ncp"
	case 0x00c36400:
		return "ceph"
	case 0x0000ef53:
		return "ext"
	case 0x58465342:
		return "xfs"
	case 0x9123683e:
		return "btrfs"
	case 0x01021994:
		return "tmpfs"
	case 0x794c7630:
		return "overlay"
	default:
		return ""
	}
}
