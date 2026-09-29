package testfixture

import (
	"archive/zip"
	"bytes"
	_ "embed"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"unicode/utf8"
)

type kfxSymbol uint32
type kfxFields []any
type kfxEntity struct {
	id, kind uint32
	data     []byte
}

//go:embed kfx-font.ttf
var kfxFont []byte

// KFX builds a project-owned book with two chapters, a cover, a table, Unicode,
// inline emphasis and a footnote/backlink. The bundle puts the symbol table last
// so readers must resolve book-wide references independently of archive order.
func KFX(t testing.TB, bundle bool) []byte {
	t.Helper()
	sym := func(n int) kfxSymbol { return kfxSymbol(860 + n) }
	fields := func(kv ...any) kfxFields { return kv }
	position := func(id, offset int) kfxFields { return fields(155, id, 143, offset) }
	text := "😀 Bold & text. Note"
	noteStart := utf8.RuneCountInString(text[:strings.Index(text, "Note")])
	paragraph := func(id int, text string) kfxFields { return fields(155, id, 159, kfxSymbol(269), 145, text) }
	entries := []kfxEntity{}
	add := func(id, k int, v any) { entries = append(entries, kfxEntity{uint32(id), uint32(k), kfxStream(v)}) }
	metadata := []any{}
	for _, pair := range [][2]string{{"title", "Polka KFX Test"}, {"author", "Lovelace, Ada"}, {"language", "en"}, {"publisher", "Polka"}, {"cover_image", "image"}, {"ASIN", "TEST-KFX"}} {
		metadata = append(metadata, fields(492, pair[0], 307, pair[1]))
	}
	add(348, 490, fields(491, []any{fields(495, "kindle_title_metadata", 258, metadata)}))
	add(348, 538, fields(192, kfxSymbol(376), 169, []any{fields(178, kfxSymbol(351), 170, []any{sym(0), sym(2)})}))
	add(int(sym(0)), 260, fields(174, sym(0), 141, []any{fields(155, 1, 159, kfxSymbol(269), 176, sym(1))}))
	add(int(sym(2)), 260, fields(174, sym(2), 141, []any{fields(155, 2, 159, kfxSymbol(269), 176, sym(3))}))
	add(int(sym(4)), 157, fields(173, sym(4), 13, kfxSymbol(361), 16, fields(307, 2, 306, kfxSymbol(308))))
	add(int(sym(5)), 157, fields(173, sym(5), 13, kfxSymbol(361)))
	add(int(sym(1)), 259, fields(176, sym(1), 155, 501, 146, []any{
		fields(155, 101, 159, kfxSymbol(269), 790, 1, 157, sym(4), 145, "First chapter"),
		fields(155, 102, 159, kfxSymbol(269), 145, text, 142, []any{fields(143, 2, 144, 4, 157, sym(5)), fields(143, noteStart, 144, 4, 179, sym(8))}),
		fields(159, kfxSymbol(269), 146, []any{
			fields(155, 103, 159, kfxSymbol(271), 175, sym(6), 584, "A red square", 580, kfxSymbol(320)),
		}, 142, []any{fields(143, 0, 144, 1, 179, sym(8))}),
		fields(155, 104, 159, kfxSymbol(278), 179, sym(8), 146, []any{fields(155, 105, 159, kfxSymbol(454), 146, []any{fields(155, 106, 159, kfxSymbol(279), 146, []any{
			fields(155, 107, 159, kfxSymbol(270), 633, kfxSymbol(58), 146, []any{paragraph(108, "Left cell")}),
			fields(155, 109, 159, kfxSymbol(270), 633, kfxSymbol(58), 146, []any{paragraph(110, "Right cell")}),
		})})}),
		fields(155, 111, 159, kfxSymbol(269), 145, "First line\nSecond  line", 142, []any{fields(143, 11, 144, 6, 157, sym(5))}),
		fields(155, 116, 159, kfxSymbol(269), 11, "Polka Test", 16, 20, 145, "AAA"),
	}))
	add(int(sym(3)), 259, fields(176, sym(3), 146, []any{
		fields(155, 201, 159, kfxSymbol(269), 790, 1, 157, sym(4), 145, "Second chapter"),
		fields(155, 202, 159, kfxSymbol(269), 682, kfxSymbol(375), 10, "ar", 145, "نص عربي محفوظ"),
		fields(155, 203, 159, kfxSymbol(269), 145, "Note body. Back", 142, []any{fields(143, 11, 144, 4, 179, sym(9))}),
	}))
	add(int(sym(8)), 266, fields(180, sym(8), 183, position(203, 0)))
	add(int(sym(9)), 266, fields(180, sym(9), 183, position(102, 2)))
	add(348, 389, []any{fields(178, kfxSymbol(351), 392, []any{sym(10), sym(11)})})
	add(int(sym(10)), 391, fields(239, sym(10), 235, kfxSymbol(212), 247, []any{
		fields(241, fields(244, "First chapter"), 246, position(501, 0)),
		fields(241, fields(244, "Second chapter"), 246, position(201, 0)),
	}))
	add(int(sym(11)), 391, fields(239, sym(11), 235, kfxSymbol(237), 247, []any{
		fields(241, fields(244, "1"), 246, position(102, 2)),
		fields(241, fields(244, "2"), 246, position(104, 0)),
		fields(241, fields(244, "3"), 246, position(111, 11)),
		fields(241, fields(244, "4"), 246, position(202, 0)),
	}))
	add(348, 262, fields(11, "Polka Test", 12, kfxSymbol(350), 13, kfxSymbol(350), 165, "raw-font"))
	add(348, 262, fields(11, "Polka Alias", 165, "raw-font"))
	entries = append(entries, kfxEntity{uint32(sym(12)), 418, kfxFont})
	add(int(sym(6)), 164, fields(175, sym(6), 161, kfxSymbol(284), 165, "raw-image", 422, 4, 423, 4))
	var cover bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := range 4 {
		for x := range 4 {
			img.Set(x, y, color.RGBA{220, 50, 50, 255})
		}
	}
	if err := png.Encode(&cover, img); err != nil {
		t.Fatal(err)
	}
	entries = append(entries, kfxEntity{uint32(sym(7)), 417, cover.Bytes()})
	symbols := []any{"chapter-one", "story-one", "chapter-two", "story-two", "heading-style", "emphasis-style", "image", "raw-image", "note-link", "back-link", "toc", "pages", "raw-font"}
	table := kfxSymbolTable(symbols)
	if !bundle {
		return kfxContainer(entries, table)
	}
	var out bytes.Buffer
	z := zip.NewWriter(&out)
	parts := []struct {
		name string
		data []byte
	}{
		{"book/resources.res", kfxContainer(entries[len(entries)-2:], nil)},
		{"book/content.azw", kfxContainer(entries[2:len(entries)-2], nil)},
		{"book/metadata.kfx", kfxContainer(entries[:2], table)},
	}
	for _, part := range parts {
		w, err := z.Create(part.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write(part.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// KFXJapanese combines vertical text, ruby and navigation after annotated text.
// Its structures follow independently generated, project-owned Japanese EPUBs.
func KFXJapanese(t testing.TB) []byte {
	t.Helper()
	f := func(kv ...any) kfxFields { return kv }
	s := func(n int) kfxSymbol { return kfxSymbol(n) }
	var entries []kfxEntity
	add := func(id, kind int, v any) {
		entries = append(entries, kfxEntity{uint32(id), uint32(kind), kfxStream(v)})
	}
	add(348, 490, f(491, []any{f(495, "kindle_title_metadata", 258, []any{
		f(492, "title", 307, "Polka Japanese Test"), f(492, "language", 307, "ja"),
	})}))
	add(348, 538, f(192, s(376), 560, s(559), 169, []any{f(178, s(351), 170, []any{s(860), s(861)})}))
	add(860, 260, f(174, s(860), 141, []any{f(159, s(270), 146, []any{
		f(155, 101, 159, s(269), 790, 1, 145, "縦書き"),
		f(155, 102, 159, s(269), 145, "😀 日本 読む", 142, []any{
			f(143, 2, 144, 2, 757, s(862), 758, 1),
			f(143, 3, 144, 1, 13, s(361)),
			f(143, 5, 144, 2, 179, s(863)),
		}),
		f(155, 103, 159, s(269), 146, []any{"番号", f(159, s(269), 601, s(283), 560, s(557), 707, s(573), 145, "12"), " とABC"},
			142, []any{f(143, 5, 144, 3, 706, s(778))}),
		f(155, 105, 159, s(269), 145, "圏点のある文字", 142, []any{
			f(143, 0, 144, 2, 717, s(734), 718, 0xffb00040, 719, s(60), 720, s(59)),
		}),
		f(155, 104, 159, s(269), 145, strings.Repeat("縦書きの本文です。", 100)),
	})}))
	add(861, 260, f(174, s(861), 141, []any{f(159, s(270), 146, []any{
		f(155, 201, 159, s(269), 790, 1, 145, "終わり"),
		f(155, 202, 159, s(269), 145, "戻る", 142, []any{f(143, 0, 144, 2, 179, s(864))}),
	})}))
	add(862, 756, f(757, s(862), 146, []any{f(155, 301, 758, 1, 159, s(269), 145, "にほん")}))
	add(863, 266, f(180, s(863), 183, f(155, 201, 143, 0)))
	add(864, 266, f(180, s(864), 183, f(155, 102, 143, 5)))
	add(348, 389, []any{f(392, []any{s(865)})})
	add(865, 391, f(239, s(865), 235, s(212), 247, []any{
		f(241, f(244, "縦書き"), 246, f(155, 101, 143, 0)),
		f(241, f(244, "終わり"), 246, f(155, 201, 143, 0)),
	}))
	return kfxContainer(entries, kfxSymbolTable([]any{"first", "last", "readings", "forward", "back", "toc"}))
}

func kfxSymbolTable(symbols []any) []byte {
	table := kfxStream(kfxFields{6, []any{kfxFields{4, "YJ_symbols", 5, 10, 8, 859}}, 7, symbols})
	annotation := append([]byte{0x81, 0x83}, table[4:]...)
	table = append([]byte{0xe0, 1, 0, 0xea, 0xee}, kfxVar(uint64(len(annotation)))...)
	return append(table, annotation...)
}

func kfxContainer(entries []kfxEntity, symbols []byte) []byte {
	var index, payload bytes.Buffer
	for _, entry := range entries {
		info := kfxStream(kfxFields{410, 0, 411, 0})
		entity := make([]byte, 10)
		copy(entity, "ENTY")
		binary.LittleEndian.PutUint16(entity[4:], 1)
		binary.LittleEndian.PutUint32(entity[6:], uint32(10+len(info)))
		entity = append(entity, info...)
		entity = append(entity, entry.data...)
		binary.Write(&index, binary.LittleEndian, entry.id)
		binary.Write(&index, binary.LittleEndian, entry.kind)
		binary.Write(&index, binary.LittleEndian, uint64(payload.Len()))
		binary.Write(&index, binary.LittleEndian, uint64(len(entity)))
		payload.Write(entity)
	}
	info := kfxStream(kfxFields{410, 0, 411, 0, 413, 18, 414, index.Len(), 415, 18 + index.Len(), 416, len(symbols)})
	header := make([]byte, 18)
	copy(header, "CONT")
	binary.LittleEndian.PutUint16(header[4:], 2)
	infoOffset := 18 + index.Len() + len(symbols)
	binary.LittleEndian.PutUint32(header[6:], uint32(infoOffset+len(info)+2))
	binary.LittleEndian.PutUint32(header[10:], uint32(infoOffset))
	binary.LittleEndian.PutUint32(header[14:], uint32(len(info)))
	header = append(header, index.Bytes()...)
	header = append(header, symbols...)
	header = append(header, info...)
	header = append(header, '[', ']') // Empty generator metadata.
	return append(header, payload.Bytes()...)
}

func kfxStream(value any) []byte { return append([]byte{0xe0, 1, 0, 0xea}, kfxIon(value)...) }

func kfxIon(value any) []byte {
	var typ byte
	var body []byte
	switch v := value.(type) {
	case bool:
		if v {
			return []byte{0x11}
		}
		return []byte{0x10}
	case int:
		typ = 2
		body = make([]byte, 8)
		binary.BigEndian.PutUint64(body, uint64(v))
	case kfxSymbol:
		typ = 7
		body = make([]byte, 4)
		binary.BigEndian.PutUint32(body, uint32(v))
	case string:
		typ = 8
		body = []byte(v)
	case []any:
		typ = 11
		for _, child := range v {
			body = append(body, kfxIon(child)...)
		}
	case kfxFields:
		typ = 13
		for i := 0; i < len(v); i += 2 {
			body = append(body, kfxVar(uint64(v[i].(int)))...)
			body = append(body, kfxIon(v[i+1])...)
		}
	default:
		panic("unsupported synthetic Ion value")
	}
	if typ == 2 || typ == 7 {
		for len(body) > 0 && body[0] == 0 {
			body = body[1:]
		}
	}
	if len(body) < 14 {
		return append([]byte{typ<<4 | byte(len(body))}, body...)
	}
	out := []byte{typ<<4 | 14}
	out = append(out, kfxVar(uint64(len(body)))...)
	return append(out, body...)
}

func kfxVar(n uint64) []byte {
	out := []byte{byte(n&127) | 128}
	for n >>= 7; n > 0; n >>= 7 {
		out = append([]byte{byte(n & 127)}, out...)
	}
	return out
}
