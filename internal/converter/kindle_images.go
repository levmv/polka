package converter

import (
	"archive/zip"
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"

	"github.com/levmv/polka/internal/imagecodec"
)

type kindleImages struct {
	*kindleEPUB
	images   [][]byte
	imageIDs map[*zip.File]int
}

func (s *kindleImages) image(base, href string) (int, error) {
	file, _, external, err := s.reference(base, href)
	if err != nil {
		return 0, err
	}
	if external || file == nil {
		return 0, fmt.Errorf("Kindle image is not packaged: %q: %w", href, ErrUnsupportedContent)
	}
	if id, ok := s.imageIDs[file]; ok {
		return id, nil
	}
	data, err := kepubReadZipFile(s.ctx, file, maxConverterResourceBytes)
	if err != nil {
		return 0, err
	}
	if _, ok := s.prepareSVG(file, data); ok {
		return 0, fmt.Errorf("Kindle image requires SVG rasterization: %w", ErrUnsupportedContent)
	}
	config, kind, err := imagecodec.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, fmt.Errorf("decode image %s: %w", file.Name, err)
	}
	const maxPixels = 16 << 20
	if config.Width < 1 || config.Height < 1 || config.Width > maxPixels/config.Height {
		return 0, fmt.Errorf("Kindle image dimensions exceed limit: %w", ErrInputTooLarge)
	}
	if err := claimConversionDecodedBytes(s.ctx, int64(config.Width)*int64(config.Height)*4, "Kindle decoded image"); err != nil {
		return 0, err
	}
	img, _, err := imagecodec.Decode(bytes.NewReader(data))
	if err != nil {
		return 0, fmt.Errorf("decode image %s: %w", file.Name, err)
	}
	id, err := s.storeImage(img, data, kind)
	if err == nil {
		s.imageIDs[file] = id
	}
	return id, err
}

func (s *kindleEPUB) prepareSVG(file *zip.File, data []byte) ([]byte, bool) {
	name := file.Name
	if s.mediaTypes[file] == "image/svg+xml" {
		// EPUB resource types come from the manifest; names need no extension.
		// PrepareSVG still checks the bytes before accepting the type hint.
		name = "image.svg"
	}
	return imagecodec.PrepareSVG(data, name)
}

func (s *kindleImages) storeImage(img image.Image, data []byte, kind string) (int, error) {
	if kind != "jpeg" {
		// Classic readers have the broadest support for baseline JPEG. Preserve
		// resolution and flatten transparency onto the normal white page.
		if err := claimConversionDecodedBytes(s.ctx, int64(img.Bounds().Dx())*int64(img.Bounds().Dy())*4, "Kindle image white background"); err != nil {
			return 0, err
		}
		rgb := image.NewRGBA(image.Rect(0, 0, img.Bounds().Dx(), img.Bounds().Dy()))
		draw.Draw(rgb, rgb.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
		draw.Draw(rgb, rgb.Bounds(), img, img.Bounds().Min, draw.Over)
		var out bytes.Buffer
		if err := jpeg.Encode(&out, rgb, &jpeg.Options{Quality: 90}); err != nil {
			return 0, err
		}
		data = out.Bytes()
	}
	// Go's JPEG encoder omits APP0. Some Kindle renderers require JFIF even
	// for an otherwise valid baseline JPEG; adding it leaves image data intact.
	if !bytes.Contains(data[:min(len(data), 64)], []byte("JFIF\x00")) {
		jfif := []byte{0xff, 0xe0, 0, 16, 'J', 'F', 'I', 'F', 0, 1, 1, 0, 0, 1, 0, 1, 0, 0}
		withJFIF := append([]byte{0xff, 0xd8}, jfif...)
		data = append(withJFIF, data[2:]...)
	}
	id := len(s.images)
	s.images = append(s.images, data)
	return id, checkContext(s.ctx)
}
