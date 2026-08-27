package cli

import (
	"os"

	"github.com/levmv/polka/internal/format"
)

type assetReaderCapability struct {
	Format  format.Format
	CanRead bool
}

func detectAssetReaderCapability(detectPath, absPath string) (assetReaderCapability, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return assetReaderCapability{}, err
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return assetReaderCapability{}, err
	}

	detected := format.DetectFormat(detectPath, f, stat.Size())
	return assetReaderCapability{
		Format:  detected,
		CanRead: format.CanRead(detected),
	}, nil
}
