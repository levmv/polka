package format

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
)

var rtfPageCountRE = regexp.MustCompile(`^\\nofpages([0-9]+)\b`)

func rtfDeclaredPageCount(info []byte) int {
	// Accept the control directly in info or in its own child group. Escaped
	// text, binary payloads and unrelated destinations are not declarations.
	for i := 1; i < len(info); {
		var candidate []byte
		switch info[i] {
		case '{':
			group := rtfBalancedGroup(info, i)
			if len(group) == 0 {
				return 0
			}
			if word, _ := rtfGroupControlWord(group); word == "nofpages" {
				candidate = group[1:]
			}
			i += len(group)
		case '\\':
			if end, ok := rtfBinaryPayloadEnd(info, i); ok {
				i = end
				continue
			}
			candidate = info[i:]
			i += 2
		default:
			i++
		}
		if match := rtfPageCountRE.FindSubmatch(candidate); len(match) == 2 {
			if count := positivePageCount(string(match[1])); count > 0 {
				return count
			}
		}
	}
	return 0
}

func rtfPageCount(ctx context.Context, r io.ReaderAt, size int64) (int, error) {
	raw, err := readAllLimited(io.NewSectionReader(r, 0, size), "RTF page count", maxPageCountBytes)
	if err != nil {
		return 0, err
	}
	codepage, err := rtfCodepage(raw)
	if err != nil {
		return 0, err
	}
	var body bytes.Buffer
	for i := 0; i < len(raw); {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if raw[i] == '\n' || raw[i] == '\r' {
			i++
			continue
		}
		if raw[i] == '\\' && i+1 < len(raw) {
			if end, ok := rtfBinaryPayloadEnd(raw, i); ok {
				i = end
				continue
			}
			body.Write(raw[i : i+2])
			i += 2
			continue
		}
		if raw[i] == '{' {
			word, _ := rtfGroupControlWord(raw[i:])
			skip := i+2 < len(raw) && string(raw[i+1:i+3]) == `\*`
			switch word {
			case "fonttbl", "colortbl", "stylesheet", "info", "pict", "object", "header", "footer", "nonshppict":
				skip = true
			}
			if skip {
				group := rtfBalancedGroup(raw, i)
				if len(group) == 0 {
					return 0, fmt.Errorf("page count: unbalanced RTF group")
				}
				i += len(group)
				continue
			}
		}
		body.WriteByte(raw[i])
		i++
	}
	var extent pageExtent
	for line := range strings.SplitSeq(rtfDecodeText(body.Bytes(), codepage), "\n") {
		extent.Text(line)
		extent.Break()
	}
	return extent.Pages(), ctx.Err()
}
