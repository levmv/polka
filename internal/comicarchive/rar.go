// Package comicarchive owns RAR decoder construction shared by comic inspection
// and conversion paths.
package comicarchive

import (
	"io"

	"github.com/nwaples/rardecode/v2"
)

const maxRARDictionaryBytes int64 = 128 << 20

// NewRARReader opens one RAR archive with the same bounded decoder policy used
// by import, metadata inspection, cover extraction, and conversion.
func NewRARReader(src io.ReaderAt, size int64) (*rardecode.Reader, error) {
	return rardecode.NewReader(
		io.NewSectionReader(src, 0, size),
		rardecode.MaxDictionarySize(maxRARDictionaryBytes),
	)
}
