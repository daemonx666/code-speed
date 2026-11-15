// Ported from magpie (internal/edit/text.go), MIT License, (c) 2026 yetone.

package edit

import "strings"

// splitLines splits on '\n' but keeps a trailing newline as an empty final
// element so joinLines can restore the file exactly.
func splitLines(s string) []string {
	if s == "" {
		return []string{""}
	}
	return strings.Split(s, "\n")
}

// joinLinesLike is joinLines for lines edited out of orig: when orig used
// \r\n for every line break, the lines added or replaced get \r\n too, so the
// file never ends up with mixed endings. Untouched lines already carry their
// \r, and a file that mixed endings is left as it was.
func joinLinesLike(lines []string, orig string) string {
	s := joinLines(lines)
	if n := strings.Count(orig, "\n"); n > 0 && strings.Count(orig, "\r\n") == n {
		s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")
	}
	return s
}

func joinLines(lines []string) string {
	s := strings.Join(lines, "\n")
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return s
}
