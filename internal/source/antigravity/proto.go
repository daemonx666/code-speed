package antigravity

import (
	"strings"
	"unicode/utf8"
)

// field is one decoded protobuf field. Wire types 0, 1 and 5 keep their
// value in v; wire type 2 keeps its payload in b.
type field struct {
	num int
	wt  int
	v   uint64
	b   []byte
}

func readVarint(b []byte) (v uint64, n int, ok bool) {
	for i := 0; i < len(b) && i < 10; i++ {
		v |= uint64(b[i]&0x7f) << (7 * uint(i))
		if b[i] < 0x80 {
			return v, i + 1, true
		}
	}
	return 0, 0, false
}

// walk decodes a message into its fields; ok is false when it is malformed
// (field 0, wire types 3, 4, 6 or 7, a long varint, a field past the end).
func walk(b []byte) (fs []field, ok bool) {
	for len(b) > 0 {
		k, n, good := readVarint(b)
		if !good {
			return nil, false
		}
		b = b[n:]
		f := field{num: int(k >> 3), wt: int(k & 7)}
		if k>>3 == 0 || k>>3 > 1<<29 {
			return nil, false
		}
		switch f.wt {
		case 0:
			v, n, good := readVarint(b)
			if !good {
				return nil, false
			}
			f.v, b = v, b[n:]
		case 1:
			if len(b) < 8 {
				return nil, false
			}
			b = b[8:]
		case 2:
			l, n, good := readVarint(b)
			if !good || l > uint64(len(b)-n) {
				return nil, false
			}
			f.b, b = b[n:n+int(l)], b[n+int(l):]
		case 5:
			if len(b) < 4 {
				return nil, false
			}
			b = b[4:]
		default:
			return nil, false
		}
		fs = append(fs, f)
	}
	return fs, true
}

type msg []field

// num is the last varint occurrence of field n, as a signed 64-bit value.
func (m msg) num(n int) int64 {
	var out int64
	for _, f := range m {
		if f.num == n && f.wt == 0 {
			out = int64(f.v)
		}
	}
	return out
}

// str is the last string occurrence of field n.
func (m msg) str(n int) string {
	s := ""
	for _, f := range m {
		if f.num == n && f.wt == 2 {
			s = strings.ToValidUTF8(string(f.b), "�")
		}
	}
	return s
}

// sub decodes the last occurrence of message field n. present is false when
// it is absent; ok is false when it is malformed.
func (m msg) sub(n int) (out msg, present, ok bool) {
	for _, f := range m {
		if f.num == n && f.wt == 2 {
			fs, good := walk(f.b)
			if !good {
				return nil, true, false
			}
			out, present = fs, true
		}
	}
	return out, present, true
}

// firstSub decodes the first occurrence of repeated message field n.
func (m msg) firstSub(n int) (out msg, present, ok bool) {
	for _, f := range m {
		if f.num == n && f.wt == 2 {
			fs, good := walk(f.b)
			return fs, good, good
		}
	}
	return nil, false, true
}

func (m msg) firstStr(n int) string {
	for _, f := range m {
		if f.num == n && f.wt == 2 {
			return strings.ToValidUTF8(string(f.b), "�")
		}
	}
	return ""
}

// timestamp reads a Timestamp message field as Unix ms; 0 when absent or invalid.
func (m msg) timestamp(n int) (ms int64, ok bool) {
	t, present, good := m.sub(n)
	if !good {
		return 0, false
	}
	if !present {
		return 0, true
	}
	sec := t.num(1)
	nanos := t.num(2)
	if nanos < 0 || nanos > 999_999_999 {
		nanos = 0
	}
	if sec <= 0 || sec > 1<<45 {
		return 0, true
	}
	return sec*1000 + nanos/1_000_000, true
}

// indices reads a repeated uint32 field, packed or not.
func (m msg) indices(n int) []int64 {
	var out []int64
	for _, f := range m {
		if f.num != n {
			continue
		}
		switch f.wt {
		case 0:
			out = append(out, int64(f.v))
		case 2:
			b := f.b
			for len(b) > 0 {
				v, k, ok := readVarint(b)
				if !ok {
					break
				}
				out = append(out, int64(v))
				b = b[k:]
			}
		}
	}
	return out
}

// projectOf is the basename of a workspace URI or path.
func projectOf(u string) string {
	if i := strings.Index(u, ":"); i >= 2 {
		ok := true
		for _, c := range u[:i] {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.') {
				ok = false
			}
		}
		if ok {
			u = u[i+1:]
			if strings.HasPrefix(u, "//") {
				if j := strings.Index(u[2:], "/"); j >= 0 {
					u = u[2+j:]
				} else {
					u = ""
				}
			}
			u = unescape(u)
		}
	}
	parts := strings.FieldsFunc(u, func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

func hexv(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// unescape percent-decodes as UTF-8; invalid escapes and invalid UTF-8 stay.
func unescape(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b []byte
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s)+0 && hexv(s[i+1]) >= 0 && hexv(s[i+2]) >= 0 {
			b = append(b, byte(hexv(s[i+1])<<4|hexv(s[i+2])))
			i += 2
			continue
		}
		b = append(b, s[i])
	}
	if !utf8.Valid(b) {
		return s
	}
	return string(b)
}
