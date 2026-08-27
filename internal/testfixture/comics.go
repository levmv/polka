// Package testfixture contains tiny, freely redistributable binary fixtures
// shared by tests in more than one internal package.
package testfixture

import "encoding/base64"

// CBR3 returns a 381-byte RAR3 comic containing one 2x2 JPEG and one 2x2 PNG.
func CBR3() []byte {
	return decode("UmFyIRoHAM+QcwAADQAAAAAAAAAMJXQggCwAtgAAANwAAAAAbLFw2gAAISodNQwAIAAAAHRlc3RmaWxlLmpwZ+cYFf7V/ydkeNQh1MKmm6OAexVPlvVHrzqPE5mVHC08ghs85LfafCcldrlGYJPnjkgNzK9t4fZEpePKwmfeq9nqNRVssG8auWw3ppmChio3P4QobbIIu+aDvAnlNhtw7eU/yPyMBuPEssZjwTehsh4DZgC5HXWGFUVxgDrn+6KnVDXP/2B26ds102b/eZa5elob/BycnuXvN92AXobBxHJ4Ebq+7rCITbK7Lz6UAAC/iGf2qf/UUW50IIAsAFQAAABXAAAAAGKssK8AACEqHTUMACAAAAB0ZXN0ZmlsZS5wbmenGIjF+7VC0fPe1feyyXAlT4G/SVtSdAyd7pMHsE3FAkIqbrYBRgyQp7m1pxzv+HOHpfwCaeA8jA41QCxmUCvsyGsqR5gONgQCwAAAAL+IZ/ap/9TEPXsAQAcA")
}

// CBR5 returns a 410-byte RAR5 comic with the same two source images as CBR3.
func CBR5() []byte {
	return decode("UmFyIRoHAQAzkrXlCgEFBgAFAQGAgAD0HZqmIgIC1gEG3AG2gwLQDlA6bLFw2oAFAQx0ZXN0ZmlsZS5qcGfFTNMmZEUy9lA12dPGdnOSIQEQfCUEEQaFIC1LaxGhBraEEGiWvg7U3PhaCC+AISAx6AW1qbGzgKOyVPjG9Jpm5ubvk/5obhmGBh//X94ueU/gC3YLkAIIAAxNQ/ULQJIztPVL7JkT+apCKEWVXXGVll2XGV2G2BtaM024UpiiE686V6a8p9wMKgiLivmFTgFGdX+VZhaZRGUEn/iE4eUedQHYfoJJ8FkKkT/Hb7U7xYn2O1cmetrlwyRxyedm/TCSxywY6uilmhyah/QZVE7d6+Yz3o/M4lDcHCICAtcABtcAtoMC0A5QOmKssK+AAAEMdGVzdGZpbGUucG5niVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAMAAABFaP0WAAAABlBMVEUAAAD///+l2Z/dAAAADElEQVR4AWNgYARCAAAMAAM54HcDAAAAAElFTkSuQmCCHXdWUQMFBAA=")
}

// CB7 returns a 352-byte solid LZMA2 archive containing ComicInfo.xml, one
// named PNG, and one PNG whose .bin extension exercises content sniffing.
func CB7() []byte {
	return decode("N3q8ryccAAQ16WHsHgEAAAAAAAAiAAAAAAAAAIVzVnzgAQcAn10ARJQFxHon9vfuiY5QkIizqtVQIJYzd/penA8ly9BWbAoXPA/CpcaQAiPmAPjre0eOVS4YfspvntftQ+BuAvoyLNQk4WLR9kzQRIARKCLuVUXTe9E4vI2kQk8QgPOvYpCK2twrsKRwoaPDCr7rR2qkeiv7f8MoZsW+4piEtW0ebP19owsD4n9p/xZffQI81xf98hS42dQsM/d36V8MzLwAAAAAgTMHrg/VMISu1yTT/rNwGIFAHkP9bguZW83Rh7zVWt7DBe9PzseTyhPgGIDcXqOz1Jdp1e2iEiu/6KR1cXI04Oog/DN5phENqF4fEYvQ4cWO/1DQ1xdQULgurGqYmg/q5J6lbZhpFk9FDfMZnqy0guDNugAAFwaApwEJdwAHCwEAASMDAQEFXQAQAAAMgKIKAZkK8/AAAA==")
}

// AVIF returns a synthetic lossless 2x2 AVIF with four coloured pixels.
func AVIF() []byte {
	return decode("AAAAIGZ0eXBhdmlmAAAAAGF2aWZtaWYxbWlhZk1BMUEAAADrbWV0YQAAAAAAAAAhaGRscgAAAAAAAAAAcGljdAAAAAAAAAAAAAAAAAAAAAAOcGl0bQAAAAAAAQAAAB5pbG9jAAAAAEQAAAEAAQAAAAEAAAETAAAATgAAAChpaW5mAAAAAAABAAAAGmluZmUCAAAAAAEAAGF2MDFDb2xvcgAAAABqaXBycAAAAEtpcGNvAAAAFGlzcGUAAAAAAAAAAgAAAAIAAAAQcGl4aQAAAAADCAgIAAAADGF2MUOBIAAAAAAAE2NvbHJuY2x4AAIAAgAAgAAAABdpcG1hAAAAAAAAAAEAAQQBAoMEAAAAVm1kYXQSAAoHOAA+UCAgCTJBEAAA/GkD1f4mq+MwFQdpuClS0/3si/n0TTr9ev16QLv////Vm4Y8wLMNz6Ex1IdSQsuhMdSHUh1IdSHUjV2E5TI=")
}

func decode(value string) []byte {
	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		panic(err)
	}
	return data
}
