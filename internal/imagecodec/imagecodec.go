// Package imagecodec owns raster decoders that are shared across format
// inspection and cover processing.
package imagecodec

import (
	"bytes"
	"encoding/binary"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"

	"github.com/gen2brain/avif"
	_ "golang.org/x/image/webp"
)

const sniffBytes = 4096

// IsAVIF reports whether data starts with an ISO BMFF file-type box that names
// AVIF as either its major or a compatible brand.
func IsAVIF(data []byte) bool {
	if len(data) < 16 || !bytes.Equal(data[4:8], []byte("ftyp")) {
		return false
	}

	boxSize := uint64(binary.BigEndian.Uint32(data[:4]))
	brandOffset := 8
	if boxSize == 1 {
		if len(data) < 24 {
			return false
		}
		boxSize = binary.BigEndian.Uint64(data[8:16])
		brandOffset = 16
	}
	if boxSize == 0 || boxSize > uint64(len(data)) {
		boxSize = uint64(len(data))
	}
	if boxSize < uint64(brandOffset+8) {
		return false
	}

	if isAVIFBrand(data[brandOffset : brandOffset+4]) {
		return true
	}
	for offset := brandOffset + 8; offset+4 <= int(boxSize); offset += 4 {
		if isAVIFBrand(data[offset : offset+4]) {
			return true
		}
	}
	return false
}

// DecodeConfig is image.DecodeConfig with AVIF support, including files that
// advertise AVIF as a compatible rather than the major ISO BMFF brand.
func DecodeConfig(r io.Reader) (image.Config, string, error) {
	prefix, rest, err := sniff(r)
	if err != nil {
		return image.Config{}, "", err
	}
	reader := io.MultiReader(bytes.NewReader(prefix), rest)
	if IsAVIF(prefix) {
		cfg, err := avif.DecodeConfig(reader)
		return cfg, "avif", err
	}
	return image.DecodeConfig(reader)
}

// Decode is image.Decode with the same AVIF detection policy as DecodeConfig.
func Decode(r io.Reader) (image.Image, string, error) {
	prefix, rest, err := sniff(r)
	if err != nil {
		return nil, "", err
	}
	reader := io.MultiReader(bytes.NewReader(prefix), rest)
	if IsAVIF(prefix) {
		img, err := avif.Decode(reader)
		return img, "avif", err
	}
	return image.Decode(reader)
}

func sniff(r io.Reader) ([]byte, io.Reader, error) {
	prefix, err := io.ReadAll(io.LimitReader(r, sniffBytes))
	if err != nil {
		return nil, nil, err
	}
	return prefix, r, nil
}

func isAVIFBrand(brand []byte) bool {
	return bytes.Equal(brand, []byte("avif")) || bytes.Equal(brand, []byte("avis"))
}
