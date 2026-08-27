//go:build !linux && !darwin && !windows

package fsprofile

func detect(path string) Info {
	existing, err := nearestExisting(path)
	info := Info{Path: existing, Kind: KindUnknown}
	if err != nil {
		return info
	}
	return info
}

func diskUsage(path string) (Usage, bool) {
	return Usage{}, false
}
