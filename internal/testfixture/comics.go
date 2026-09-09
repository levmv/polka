// Package testfixture contains tiny, freely redistributable binary fixtures
// shared by tests in more than one internal package.
package testfixture

import "encoding/base64"

// CBR3 returns a 381-byte RAR3 comic containing one 2x2 JPEG and one 2x2 PNG.
func CBR3() []byte {
	return decode("UmFyIRoHAM+QcwAADQAAAAAAAAAMJXQggCwAtgAAANwAAAAAbLFw2gAAISodNQwAIAAAAHRlc3RmaWxlLmpwZ+cYFf7V/ydkeNQh1MKmm6OAexVPlvVHrzqPE5mVHC08ghs85LfafCcldrlGYJPnjkgNzK9t4fZEpePKwmfeq9nqNRVssG8auWw3ppmChio3P4QobbIIu+aDvAnlNhtw7eU/yPyMBuPEssZjwTehsh4DZgC5HXWGFUVxgDrn+6KnVDXP/2B26ds102b/eZa5elob/BycnuXvN92AXobBxHJ4Ebq+7rCITbK7Lz6UAAC/iGf2qf/UUW50IIAsAFQAAABXAAAAAGKssK8AACEqHTUMACAAAAB0ZXN0ZmlsZS5wbmenGIjF+7VC0fPe1feyyXAlT4G/SVtSdAyd7pMHsE3FAkIqbrYBRgyQp7m1pxzv+HOHpfwCaeA8jA41QCxmUCvsyGsqR5gONgQCwAAAAL+IZ/ap/9TEPXsAQAcA")
}

// CBR5 returns a 407-byte solid RAR5 comic with the same source images as CBR3.
func CBR5() []byte {
	return decode("UmFyIRoHAQAgtvoRCgEFBgQFAQGAgABGxEvRIgIC7AEG3AG2gwLQDlA6bLFw2oAdAQx0ZXN0ZmlsZS5qcGfEd+knZURDL1cESsmZlt5eZkl3MRgQQeEoIogjHMi4iQxSamImCiDodCCCpNi6ngLqQdS7HgaCC8AQkBkf4GbXU7HUlgpKq9U4xd3X8X6vV6q5NXxRVUVRXjx99r/cXXeXgBOrXlrg00AD4nL9g0gboM4x7f/OGc/s5jOmeOhAs88iClAh+i+MsKoaNJKVJEKnFTTEU6V+APnCgPRW1Cp5BjsXcKchclIjOiTFpCWOPHE4w3y/USMoaAZVup59/47829uo/75xuLM6r21duXLv12+PPaNn+mzuZfHV4bW90jvgcDrkc2upUX2u+NMc19ciAgK+AAbXALaDAtAOUDpirLCvwB0BDHRlc3RmaWxlLnBuZ0UkO+iaxpwoqoJahl1I9hipjHbJ8m/+5BQNNrWZCita+y/1/BSwSx6Nnla3Z53KxfQbzcHIy4CWSxH3Vp+YHXdWUQMFBAA=")
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
