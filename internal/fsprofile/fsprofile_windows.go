//go:build windows

package fsprofile

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func diskUsage(path string) (Usage, bool) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return Usage{}, false
	}
	var freeToCaller, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &freeToCaller, &total, &totalFree); err != nil {
		return Usage{}, false
	}
	return Usage{TotalBytes: total, FreeBytes: freeToCaller}, true
}

func detect(path string) Info {
	existing, err := nearestExisting(path)
	info := Info{Path: existing, Kind: KindUnknown}
	if err != nil {
		return info
	}

	root, err := windowsVolumeRoot(existing)
	if err != nil {
		return info
	}
	if isUNCPath(root) {
		info.Type = "unc"
		info.Kind = KindNetwork
		return info
	}
	info.Type = windowsDriveTypeName(root)
	switch info.Type {
	case "remote":
		info.Kind = KindNetwork
	case "":
		info.Kind = KindUnknown
	default:
		info.Kind = KindLocal
	}
	return info
}

func isUNCPath(path string) bool {
	path = strings.ReplaceAll(path, `/`, `\`)
	if strings.HasPrefix(path, `\\?\UNC\`) || strings.HasPrefix(path, `\\.\UNC\`) {
		return true
	}
	if strings.HasPrefix(path, `\\?\`) || strings.HasPrefix(path, `\\.\`) {
		return false
	}
	return strings.HasPrefix(path, `\\`)
}

func windowsVolumeRoot(path string) (string, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	buf := make([]uint16, windows.MAX_PATH+1)
	err = windows.GetVolumePathName(p, &buf[0], uint32(len(buf)))
	if err == nil {
		return windows.UTF16ToString(buf), nil
	}

	volume := filepath.VolumeName(path)
	if volume == "" {
		return "", err
	}
	return volume + `\`, nil
}

func windowsDriveTypeName(root string) string {
	p, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return ""
	}
	switch windows.GetDriveType(p) {
	case windows.DRIVE_REMOTE:
		return "remote"
	case windows.DRIVE_FIXED:
		return "fixed"
	case windows.DRIVE_REMOVABLE:
		return "removable"
	case windows.DRIVE_CDROM:
		return "cdrom"
	case windows.DRIVE_RAMDISK:
		return "ramdisk"
	case windows.DRIVE_NO_ROOT_DIR:
		return "no_root"
	case windows.DRIVE_UNKNOWN:
		return "unknown"
	default:
		return ""
	}
}
