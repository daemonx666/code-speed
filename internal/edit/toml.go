// Ported from magpie (internal/edit/toml.go, toml_without.go), MIT License,
// (c) 2026 yetone. The kept-parse caches, Inline merging, top-level key
// editing and Mark notes are left out; table headers are found by the
// parser, and an edit only moves whole tables.

package edit

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

// Whole TOML tables are treated as units: the caller owns the tables it
// writes (one per MCP server) and replaces them verbatim, while every other
// line of the file stays untouched.

// Raw is a TOML value written as it is: a string, array or inline table the
// caller has already spelled out.
type Raw string

// tomlTableSpan is one table block of a file, as line numbers.
type tomlTableSpan struct {
	from  int // the header's line
	top   int // from, or the first line of the comment block right above it
	to    int // one past the last line of the block: the next block's top
	name  string
	array bool
}

// parseTOMLTables finds both ordinary and array-table blocks in one parser
// pass. Only top-level expressions delimit blocks, so lines inside multiline
// values cannot be mistaken for comments or table headers.
//
// A comment block that sits right against a header (no blank line between)
// belongs to that header, not to the table above it: removing or replacing a
// table must leave the next table's own comment where it is.
func parseTOMLTables(lines []string) ([]tomlTableSpan, error) {
	p := unstable.Parser{}
	p.Reset([]byte(strings.Join(lines, "\n")))
	shapeOf := tomlShaper(lines)
	var tables []tomlTableSpan
	for p.NextExpression() {
		expr := p.Expression()
		if expr.Kind != unstable.Table && expr.Kind != unstable.ArrayTable {
			continue
		}
		var rawParts []string
		var first unstable.Range
		for keys := expr.Key(); keys.Next(); {
			key := keys.Node()
			if len(rawParts) == 0 {
				first = key.Raw
			}
			// A table's name is the parser's own key text, so a quoted part
			// keeps its quotes, while the whitespace a header may put around
			// its dots, as in `[ a . b ]`, is not part of the name.
			rawParts = append(rawParts, string(p.Raw(key.Raw)))
		}
		i := shapeOf(first).Start.Line - 1
		top := i
		for top > 0 && strings.HasPrefix(strings.TrimSpace(lines[top-1]), "#") {
			top--
		}
		if top == 0 {
			top = i // a banner at the top of the file is the file's
		}
		tables = append(tables, tomlTableSpan{
			from: i, top: top, name: strings.Join(rawParts, "."),
			array: expr.Kind == unstable.ArrayTable,
		})
	}
	if err := p.Error(); err != nil {
		return nil, tomlParseError(&p, err)
	}
	for i := range tables {
		tables[i].to = len(lines)
		if i+1 < len(tables) {
			tables[i].to = tables[i+1].top
		}
	}
	return tables, nil
}

func tomlTableNamed(tables []tomlTableSpan, name string) (tomlTableSpan, bool) {
	for _, t := range tables {
		if t.name == name {
			return t, true
		}
	}
	return tomlTableSpan{}, false
}

// tomlShaper is the parser's Shape for the document of lines, found from
// where each line starts rather than by counting the newlines before each
// range, as Shape does: that is a pass over the file for each header, which
// takes a third of a second on a Codex config.toml with thousands of projects.
func tomlShaper(lines []string) func(unstable.Range) unstable.Shape {
	starts := make([]int, len(lines))
	for i, off := 1, 0; i < len(lines); i++ {
		off += len(lines[i-1]) + 1
		starts[i] = off
	}
	at := func(off int) unstable.Position {
		i := sort.Search(len(starts), func(i int) bool { return starts[i] > off }) - 1
		return unstable.Position{Offset: off, Line: i + 1, Column: off - starts[i] + 1}
	}
	return func(r unstable.Range) unstable.Shape {
		return unstable.Shape{Start: at(int(r.Offset)), End: at(int(r.Offset + r.Length))}
	}
}

func tomlParseError(p *unstable.Parser, err error) error {
	pe, ok := err.(*unstable.ParserError)
	if !ok {
		return err
	}
	data := p.Data()
	// Empty highlights are used for EOF errors. Do not pass them to Range:
	// the parser's slice-offset calculation can panic on a nil highlight.
	pos := unstable.Position{
		Offset: len(data), Line: bytes.Count(data, []byte{'\n'}) + 1,
		Column: len(data) - bytes.LastIndexByte(data, '\n'),
	}
	if len(pe.Highlight) > 0 {
		pos = p.Shape(p.Range(pe.Highlight)).Start
	}
	return fmt.Errorf("line %d, column %d: %w", pos.Line, pos.Column, err)
}

// readTOMLLines reads path and checks it parses, so an edit is never made
// on a file that is already broken.
func readTOMLLines(path string) (lines []string, orig string, tables []tomlTableSpan, err error) {
	raw, err := Read(path)
	if err != nil {
		return nil, "", nil, err
	}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := validateTOML(raw); err != nil {
			return nil, "", nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	lines = splitLines(string(raw))
	tables, err = parseTOMLTables(lines)
	if err != nil {
		return nil, "", nil, fmt.Errorf("%s: %w", path, err)
	}
	return lines, string(raw), tables, nil
}

// SetTOMLTable replaces table `name` with the given keys, or appends it.
// Comments right above the old header stay; an existing array table with
// that name is rejected. Tables below `name` ([name.sub]) are not touched.
func SetTOMLTable(path, name string, kvs ...KV) error {
	lines, orig, tables, err := readTOMLLines(path)
	if err != nil {
		return err
	}
	table, found := tomlTableNamed(tables, name)
	if found && table.array {
		return fmt.Errorf("%s: line %d: %q is an array table, expected an ordinary table", path, table.from+1, name)
	}
	block := []string{"[" + name + "]"}
	for _, kv := range kvs {
		block = append(block, kv.Path+" = "+tomlLiteral(kv.Value))
	}
	var out []string
	if !found {
		out = trimBlank(lines)
		if len(out) > 0 {
			out = append(out, "")
		}
		out = append(out, block...)
		out = append(out, "")
	} else {
		out = append(out, lines[:table.from]...)
		out = append(out, block...)
		if table.to < len(lines) && strings.TrimSpace(lines[table.to]) != "" {
			out = append(out, "")
		}
		out = append(out, lines[table.to:]...)
	}
	return writeTOML(path, out, orig)
}

// DelTOMLTable removes table `name` (header, body and the comment right
// above it) if present. Child table and array-table blocks are outside that
// range and remain.
func DelTOMLTable(path, name string) error {
	lines, orig, tables, err := readTOMLLines(path)
	if err != nil {
		return err
	}
	table, found := tomlTableNamed(tables, name)
	if !found {
		return nil
	}
	if table.array {
		return fmt.Errorf("%s: line %d: %q is an array table, expected an ordinary table", path, table.from+1, name)
	}
	from := table.top
	// Take the blank lines before the header with it so no gap is left.
	for from > 0 && strings.TrimSpace(lines[from-1]) == "" {
		from--
	}
	out := append([]string{}, lines[:from]...)
	if table.to < len(lines) && len(out) > 0 && strings.TrimSpace(lines[table.to]) != "" {
		out = append(out, "")
	}
	out = append(out, lines[table.to:]...)
	return writeTOML(path, out, orig)
}

// DelTOMLTablesUnder removes every ordinary or array table whose name begins
// with prefix ("mcp_servers.fs." takes [mcp_servers.fs.env] and
// [[mcp_servers.fs.x]], not [mcp_servers.fs]) in one write.
func DelTOMLTablesUnder(path, prefix string) error {
	lines, orig, tables, err := readTOMLLines(path)
	if err != nil {
		return err
	}
	if len(tables) == 0 {
		return nil
	}
	out := append([]string{}, lines[:tables[0].top]...)
	dropped, drop := false, false
	for _, t := range tables {
		was := drop
		drop = strings.HasPrefix(t.name, prefix)
		if drop {
			dropped = true
			out = trimBlank(out)
			continue
		}
		if was && len(out) > 0 {
			out = append(out, "")
		}
		out = append(out, lines[t.top:t.to]...)
	}
	if !dropped {
		return nil
	}
	if out = trimBlank(out); len(out) > 0 {
		out = append(out, "")
	}
	return writeTOML(path, out, orig)
}

// writeTOML validates the exact bytes before any filesystem changes. Decoding
// also checks duplicate keys and conflicting definitions; it never rewrites
// the document, so comments and formatting survive validation unchanged.
func writeTOML(path string, lines []string, orig string) error {
	data := []byte(joinLinesLike(lines, orig))
	if err := validateTOML(data); err != nil {
		return fmt.Errorf("%s: edited TOML is invalid: %w", path, err)
	}
	return WriteAtomic(path, data)
}

func validateTOML(data []byte) error {
	// Check syntax first so EOF errors use the same nil-highlight-safe
	// diagnostics as reads, before the decoder checks TOML definitions.
	var p unstable.Parser
	p.Reset(data)
	for p.NextExpression() {
	}
	if err := p.Error(); err != nil {
		return tomlParseError(&p, err)
	}
	if err := validateTOMLDefinitions(data); err != nil {
		if de, ok := err.(*toml.DecodeError); ok {
			line, column := de.Position()
			return fmt.Errorf("line %d, column %d: %w", line, column, err)
		}
		return err
	}
	return nil
}

func trimBlank(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func tomlLiteral(v any) string {
	switch x := v.(type) {
	case Raw:
		return string(x)
	case bool:
		return strconv.FormatBool(x)
	case int:
		return strconv.Itoa(x)
	case string:
		return TOMLString(x)
	}
	return TOMLString(fmt.Sprint(v))
}

// TOMLString spells s as a TOML basic string. Go's strconv.Quote is not a
// substitute: it escapes a control byte as \x01, and TOML defines no \x
// escape, so such a value leaves a file no TOML parser reads. Escapes taken
// from the TOML spec; anything else that is printable, including non-ASCII,
// is written as it is.
func TOMLString(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if r == utf8.RuneError && size == 1 {
			// One byte that is not part of a rune: TOML has no escape for a
			// single byte, so a value that is not valid UTF-8 cannot be
			// written. It is left as it is, and writeTOML refuses the file,
			// rather than being quietly replaced by U+FFFD.
			b.WriteByte(s[i-size])
			continue
		}
		switch r {
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
