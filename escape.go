package fakeconn

import (
	"fmt"
	"unicode"
	"unicode/utf8"
)

var hex = []byte("0123456789ABCDEF")

// returns where to split data, so that when escaped, it will be at most max bytes.
// will prefer to split on the first newline encountered,
// and will try not to split a unicode character.
// returns -1 if the escaped version is less than max.
func chooseEscapeBreak(data []byte, max int) int {
	escapeSize := 0
	for i := 0; i != len(data); {
		r, size := utf8.DecodeRune(data[i:])

		switch {
		case r == utf8.RuneError && size == 1:
			escapeSize += 4
		case r == 0, r == '\\', r == '\r':
			escapeSize += 2
		case r == '\n':
			if i+size > max {
				return i
			}
			return i + size
		case unicode.IsPrint(r):
			escapeSize += size
		case r < 0x10000:
			escapeSize += 6
		default:
			escapeSize += 10
		}

		if escapeSize > max {
			if i == 0 {
				return max
			}

			return i
		}

		i += size
	}

	if escapeSize == max {
		return len(data)
	}

	return -1
}

func escape(data []byte) (out []byte) {
	for i := 0; i != len(data); {
		r, size := utf8.DecodeRune(data[i:])

		switch {
		case r == 0 || (r == utf8.RuneError && size == 1):
			out = append(out, '\\', 'x', hex[(data[i]>>4)&15], hex[data[i]&15])
		case r == '\\':
			out = append(out, '\\', '\\')
		case r == '\r':
			out = append(out, '\\', 'r')
		case r == '\n':
			out = append(out, '\\', 'n')
		case unicode.IsPrint(r):
			out = append(out, data[i:i+size]...)
		case r < 0x10000:
			out = append(out, '\\', 'u', hex[r>>12], hex[(r>>8)&15], hex[(r>>4)&15], hex[r&15])
		default:
			out = append(out, '\\', 'U', hex[r>>28], hex[(r>>24)&15], hex[(r>>20)&15], hex[(r>>16)&15], hex[(r>>12)&15], hex[(r>>8)&15], hex[(r>>4)&15], hex[r&15])
		}

		i += size
	}
	return
}

func readHex(data []byte) (val uint32, err error) {
	for _, c := range data {
		val <<= 4
		switch {
		case c >= '0' && c <= '9':
			val |= uint32(c - '0')
		case c >= 'a' && c <= 'f':
			val |= uint32(c - 'a' + 10)
		case c >= 'A' && c <= 'F':
			val |= uint32(c - 'A' + 10)
		default:
			val, err = 0, fmt.Errorf("%w: %q", errBadHex, data)
			return
		}
	}
	return
}

func unescape(data []byte) (out []byte, err error) {
	for len(data) > 0 && err == nil {
		if data[0] == '\\' {
			switch {
			case len(data) > 1 && data[1] == '\\':
				out = append(out, '\\')
				data = data[2:]
			case len(data) > 1 && data[1] == 'r':
				out = append(out, '\r')
				data = data[2:]
			case len(data) > 1 && data[1] == 'n':
				out = append(out, '\n')
				data = data[2:]
			case len(data) > 3 && data[1] == 'x':
				var val uint32
				val, err = readHex(data[2:4])
				if err != nil {
					return
				}
				out = append(out, byte(val))
				data = data[4:]
			case len(data) > 5 && data[1] == 'u':
				var val uint32
				val, err = readHex(data[2:6])
				if err != nil {
					return
				}
				if !utf8.ValidRune(rune(val)) {
					err = fmt.Errorf("%w: %04x", errBadUnicode, val)
					return
				}
				l := len(out)
				out = append(out, 0, 0, 0, 0)
				out = out[:l+utf8.EncodeRune(out[l:l+4], rune(val))]
				data = data[6:]
			case len(data) > 9 && data[1] == 'U':
				var val uint32
				val, err = readHex(data[2:10])
				if err != nil {
					return
				}

				if !utf8.ValidRune(rune(val)) {
					err = fmt.Errorf("%w: %04x", errBadUnicode, val)
					return
				}

				l := len(out)
				out = append(out, 0, 0, 0, 0)
				out = out[:l+utf8.EncodeRune(out[l:l+4], rune(val))]
				data = data[10:]
			default:
				n := 2
				if n > len(data) {
					n = len(data)
				}
				err = fmt.Errorf("%w: %q", errBadEscape, data[:n])
			}
		} else {
			r, size := utf8.DecodeRune(data)
			if r == utf8.RuneError && size == 1 {
				err = errBadUTF8
			} else {
				out = append(out, data[:size]...)
				data = data[size:]
			}
		}
	}

	return
}
