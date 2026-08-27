package converter

import "github.com/levmv/polka/internal/xmlutil"

func validXML10Char(r rune) bool {
	return xmlutil.ValidXML10Char(r)
}

func removeInvalidXML10Chars(raw []byte) []byte {
	return xmlutil.RemoveInvalidXML10Chars(raw)
}
