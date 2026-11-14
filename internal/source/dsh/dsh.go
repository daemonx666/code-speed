// Package dsh reads DeepSeek Harness session logs (Agent Speed
// spec/METRICS.md §4.3, §5.1 and §2, MIT). A session is a file
// session.v<N>.jsonl.zstd under <DSH_HOME or ~/.dsh>/sessions: independent
// zstd frames appended over time, whose decoded bytes are JSONL. Plain
// session.v<N>.jsonl files are read too. Frames can't be tailed line by
// line, so the source is a Poller that keeps each file's offset.
package dsh

import (
	"bytes"
	"context"
	"encoding/binary"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/source/zstdread"
	"github.com/daemonx666/code-speed/internal/usage"
)

// DSH is the source id.
const DSH usage.Source = "dsh"

const (
	maxDepth     = 6
	maxFiles     = 20_000
	maxFrameOut  = 64 << 20
	maxSeedBytes = 256 << 20
	recentWindow = 15 * time.Minute
)

func init() {
	source.Register(source.Spec{
		Source: DSH,
		Label:  "DeepSeek Harness",
		Color:  "#2563eb",
		Detect: func(home string) bool {
			for _, r := range roots(home) {
				if _, err := os.Stat(r); err == nil {
					return true
				}
			}
			return false
		},
		Poller: func() source.Poller { return &poller{} },
		Every:  2 * time.Second,
	})
}

func roots(home string) []string {
	if v := os.Getenv("DSH_HOME"); v != "" {
		return []string{filepath.Join(v, "sessions")}
	}
	if home == "" {
		return nil
	}
	return []string{filepath.Join(home, ".dsh", "sessions")}
}

var fileRe = regexp.MustCompile(`^session\.v\d+\.jsonl(\.zstd)?$`)

func match(path string) (ok, zst bool) {
	name := filepath.Base(path)
	if !fileRe.MatchString(name) {
		return false, false
	}
	return true, strings.HasSuffix(name, ".zstd")
}

type fileState struct {
	info    os.FileInfo
	off     int64
	zst     bool
	partial []byte // an incomplete trailing line
	skip    bool   // drop bytes up to the first newline (entered mid-line)
	p       *parser
}

type poller struct {
	roots []string // set by tests
	files map[string]*fileState
	first bool
}

type entry struct {
	path string
	info os.FileInfo
	zst  bool
}

func (p *poller) discover() []entry {
	rs := p.roots
	if rs == nil {
		home, _ := os.UserHomeDir()
		rs = roots(home)
	}
	var out []entry
	for _, root := range rs {
		root = filepath.Clean(root)
		_ = filepath.WalkDir(root, func(path string, de fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if de.IsDir() {
				rel, _ := filepath.Rel(root, path)
				if rel != "." && strings.Count(filepath.ToSlash(rel), "/") >= maxDepth {
					return filepath.SkipDir
				}
				return nil
			}
			if !de.Type().IsRegular() || len(out) >= maxFiles {
				return nil
			}
			ok, zst := match(path)
			if !ok {
				return nil
			}
			if info, err := de.Info(); err == nil {
				out = append(out, entry{path, info, zst})
			}
			return nil
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].info.ModTime().Equal(out[j].info.ModTime()) {
			return out[i].info.ModTime().Before(out[j].info.ModTime())
		}
		return out[i].path < out[j].path
	})
	return out
}

func (p *poller) Poll(ctx context.Context, since, now time.Time, d *source.Dedup, sink source.Sink) error {
	if p.files == nil {
		p.files = map[string]*fileState{}
		p.first = true
	}
	first := p.first
	p.first = false
	seen := map[string]bool{}
	for _, e := range p.discover() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		seen[e.path] = true
		fs := p.files[e.path]
		if fs != nil && (e.info.Size() < fs.off || !os.SameFile(fs.info, e.info)) {
			fs = nil // shrunk or replaced: start over, keep the dedup set
			delete(p.files, e.path)
		}
		if fs == nil {
			fs = &fileState{info: e.info, zst: e.zst, p: newParser(e.path, d, sink)}
			p.files[e.path] = fs
			if (first && e.info.ModTime().Before(since.Add(-recentWindow))) || e.info.Size() > maxSeedBytes {
				// old or huge files start at their end
				fs.off = e.info.Size()
				if !e.zst && fs.off > 0 {
					fs.skip = lastByte(e.path, fs.off) != '\n'
				}
				continue
			}
		} else if e.info.Size() == fs.off {
			fs.info = e.info
			continue
		}
		fs.info = e.info
		p.read(e.path, fs)
		fs.p.flush()
	}
	for path := range p.files {
		if !seen[path] {
			delete(p.files, path)
		}
	}
	return nil
}

func lastByte(path string, size int64) byte {
	f, err := os.Open(path)
	if err != nil {
		return '\n'
	}
	defer f.Close()
	var b [1]byte
	if _, err := f.ReadAt(b[:], size-1); err != nil {
		return '\n'
	}
	return b[0]
}

func (p *poller) read(path string, fs *fileState) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.Seek(fs.off, 0); err != nil {
		return
	}
	buf, err := readAll(f, fs.info.Size()-fs.off)
	if err != nil && len(buf) == 0 {
		return
	}
	if !fs.zst {
		fs.off += int64(fs.feed(buf, true))
		return
	}
	dec, err := zstdread.Decoder()
	if err != nil {
		return
	}
	defer dec.Close()
	pos := 0
	for pos < len(buf) {
		n, st := frameSize(buf[pos:])
		switch st {
		case frameIncomplete:
			fs.off += int64(pos)
			return
		case frameInvalid:
			fs.p.malformed++
			next := nextMagic(buf, pos+1)
			if next < 0 {
				fs.off += int64(pos)
				return
			}
			pos = next
			continue
		}
		frame := buf[pos : pos+n]
		pos += n
		if isSkippable(frame) {
			continue
		}
		out, err := dec.DecodeAll(frame, nil)
		if err != nil || len(out) > maxFrameOut {
			fs.p.malformed++
			continue
		}
		fs.feed(out, false)
	}
	fs.off += int64(pos)
}

func readAll(f *os.File, want int64) ([]byte, error) {
	if want <= 0 {
		return nil, nil
	}
	buf := make([]byte, want)
	n := 0
	for n < len(buf) {
		m, err := f.Read(buf[n:])
		n += m
		if err != nil {
			return buf[:n], err
		}
	}
	return buf, nil
}

// feed gives the parser every complete line of b. It returns how many
// bytes were consumed up to the last newline when tail is set (plain
// files re-read an incomplete trailing line); frame streams buffer it.
func (fs *fileState) feed(b []byte, tail bool) int {
	consumed := len(b)
	if tail {
		i := bytes.LastIndexByte(b, '\n')
		if i < 0 {
			if len(b) > source.MaxLine && !fs.skip {
				return len(b)
			}
			return 0
		}
		consumed = i + 1
		b = b[:consumed]
	} else if len(fs.partial) > 0 {
		b = append(fs.partial, b...)
		fs.partial = nil
	}
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			if !tail && len(b) <= source.MaxLine {
				fs.partial = append([]byte(nil), b...)
			}
			break
		}
		line := b[:i]
		b = b[i+1:]
		if fs.skip {
			fs.skip = false
			continue
		}
		if len(line) > source.MaxLine {
			fs.p.malformed++
			continue
		}
		fs.p.line(line)
	}
	return consumed
}

// ---- zstd frames ----

const (
	frameOK = iota
	frameIncomplete
	frameInvalid
)

const (
	zstdMagic = 0xFD2FB528
	skipMagic = 0x184D2A50 // …50 to …5F
)

func isSkippable(f []byte) bool {
	return len(f) >= 4 && binary.LittleEndian.Uint32(f)&0xFFFFFFF0 == skipMagic
}

// frameSize finds the compressed size of the frame at the start of b.
func frameSize(b []byte) (int, int) {
	if len(b) < 4 {
		if len(b) > 0 && !couldBeMagic(b) {
			return 0, frameInvalid
		}
		return 0, frameIncomplete
	}
	m := binary.LittleEndian.Uint32(b)
	if m&0xFFFFFFF0 == skipMagic {
		if len(b) < 8 {
			return 0, frameIncomplete
		}
		n := 8 + int(binary.LittleEndian.Uint32(b[4:]))
		if n > len(b) || n < 8 {
			return 0, frameIncomplete
		}
		return n, frameOK
	}
	if m != zstdMagic {
		return 0, frameInvalid
	}
	if len(b) < 5 {
		return 0, frameIncomplete
	}
	fhd := b[4]
	if fhd&0x08 != 0 { // reserved bit
		return 0, frameInvalid
	}
	single := fhd&0x20 != 0
	pos := 5
	if !single {
		pos++
	}
	pos += [...]int{0, 1, 2, 4}[fhd&3]
	switch fhd >> 6 {
	case 0:
		if single {
			pos++
		}
	case 1:
		pos += 2
	case 2:
		pos += 4
	case 3:
		pos += 8
	}
	for {
		if pos+3 > len(b) {
			return 0, frameIncomplete
		}
		h := uint32(b[pos]) | uint32(b[pos+1])<<8 | uint32(b[pos+2])<<16
		last, typ, size := h&1 != 0, (h>>1)&3, int(h>>3)
		pos += 3
		switch typ {
		case 0, 2:
			pos += size
		case 1:
			pos++
		default:
			return 0, frameInvalid
		}
		if last {
			break
		}
	}
	if fhd&0x04 != 0 {
		pos += 4
	}
	if pos > len(b) {
		return 0, frameIncomplete
	}
	return pos, frameOK
}

func couldBeMagic(b []byte) bool {
	z := []byte{0x28, 0xB5, 0x2F, 0xFD}
	s := []byte{0x50, 0x2A, 0x4D, 0x18}
	return bytes.HasPrefix(z, b) || (b[0]&0xF0 == 0x50 && bytes.HasPrefix(s[1:], b[1:]))
}

// nextMagic finds the next zstd or skippable frame magic at or after from.
func nextMagic(b []byte, from int) int {
	for i := from; i+4 <= len(b); i++ {
		if b[i] == 0x28 && b[i+1] == 0xB5 && b[i+2] == 0x2F && b[i+3] == 0xFD {
			return i
		}
		if b[i]&0xF0 == 0x50 && b[i+1] == 0x2A && b[i+2] == 0x4D && b[i+3] == 0x18 {
			return i
		}
	}
	return -1
}

// ---- events ----

type parser struct {
	path string
	d    *source.Dedup
	sink source.Sink

	sessionID  string
	hasHeader  bool
	createdMs  int64
	model      string
	provider   string
	project    string
	subagent   bool
	pending    int64 // pendingStartMs, 0 when null
	phase      usage.Phase
	inFlight   int64
	lastLineMs int64
	context    int64
	dirty      bool
	malformed  int
}

func newParser(path string, d *source.Dedup, sink source.Sink) *parser {
	return &parser{path: path, d: d, sink: sink, phase: usage.Idle}
}

func (p *parser) state() usage.AgentState {
	st := usage.AgentState{
		Agent:      p.path,
		Source:     DSH,
		Project:    p.project,
		Model:      p.model,
		Subagent:   p.subagent,
		Phase:      p.phase,
		LastLineMs: p.lastLineMs,
		Context:    p.context,
	}
	if p.phase == usage.Generating {
		st.InFlightMs = p.inFlight
	}
	return st
}

func (p *parser) flush() {
	if p.dirty {
		p.dirty = false
		p.sink.Agent(p.state())
	}
}

func (p *parser) line(b []byte) {
	if !source.IsObject(b) || !gjson.ValidBytes(b) {
		p.malformed++
		return
	}
	root := gjson.ParseBytes(b)
	switch root.Get("type").String() {
	case "event":
		if ev := root.Get("event"); ev.IsObject() {
			p.event(ev)
		}
	case "snapshot":
		if h := root.Get("header"); h.IsObject() {
			p.event(h)
		}
		root.Get("records").ForEach(func(_, r gjson.Result) bool {
			if r.IsObject() {
				if r.Get("type").String() == "event" && r.Get("event").IsObject() {
					p.event(r.Get("event"))
				} else {
					p.event(r)
				}
			}
			return true
		})
	default:
		p.event(root)
	}
}

func num(r gjson.Result) (int64, bool) {
	if r.Type != gjson.Number {
		return 0, false
	}
	f := r.Float()
	if f != f || f > 1e15 || f < -1e15 {
		return 0, false
	}
	return int64(f), true
}

func tok(r gjson.Result) int64 {
	if r.Type != gjson.Number {
		return 0
	}
	f := r.Float()
	switch {
	case f != f || f <= 0:
		return 0
	case f >= source.TokCap:
		return source.TokCap
	}
	return int64(f)
}

func (p *parser) header(h gjson.Result) {
	if p.hasHeader {
		return
	}
	p.hasHeader = true
	p.sessionID = strings.Clone(h.Get("id").String())
	p.createdMs, _ = num(h.Get("createdAt"))
	p.project = source.Project(h.Get("cwd").String())
	parent := h.Get("parentSession")
	depth, _ := num(h.Get("delegationDepth"))
	p.subagent = h.Get("origin").String() == "subagent" || depth > 0 ||
		(parent.Type == gjson.String && parent.String() != "")
	p.dirty = true
}

func (p *parser) event(ev gjson.Result) {
	typ := ev.Get("type").String()
	if typ == "session" {
		p.header(ev)
		return
	}
	t, ok := num(ev.Get("time"))
	if !ok {
		return
	}
	if p.hasHeader && p.createdMs > 0 && t < p.createdMs-2000 {
		return // history a fork inherited
	}
	p.dirty = true
	p.lastLineMs = max(p.lastLineMs, t)
	switch typ {
	case "request/header":
		if m := ev.Get("data.header.config.model").String(); m != "" {
			p.model = strings.Clone(m)
		}
		if v := ev.Get("data.header.config.provider").String(); v != "" {
			p.provider = strings.Clone(v)
		}
	case "step/start":
		p.pending = t
		p.phase, p.inFlight = usage.Generating, t
	case "assistant/message":
		p.message(ev, t)
	case "turn/end":
		p.phase, p.inFlight, p.pending = usage.Idle, 0, 0
	}
}

func (p *parser) message(ev gjson.Result, t int64) {
	data := ev.Get("data")
	key := data.Get("message.id").String()
	if key == "" {
		seq, _ := num(ev.Get("seq"))
		key = "dsh:" + p.sessionID + ":" + strconv.FormatInt(seq, 10)
	}
	key = strings.Clone(key)
	if _, seen := p.d.Claim(key, p.path); seen {
		return
	}
	if u := data.Get("usage"); u.IsObject() {
		cr, cw := tok(u.Get("cacheReadTokens")), tok(u.Get("cacheWriteTokens"))
		in := source.Sat(source.Sat(tok(u.Get("inputTokens")), cr), cw)
		model := data.Get("message.source.model").String()
		if model == "" {
			model = p.model
		}
		provider := data.Get("message.source.provider").String()
		if provider == "" {
			provider = p.provider
		}
		p.sink.Response(usage.Response{
			Key:      key,
			Source:   DSH,
			Agent:    p.path,
			Project:  p.project,
			Model:    strings.Clone(model),
			Provider: strings.Clone(provider),
			StartMs:  p.pending,
			EndMs:    t,
			Tokens: usage.Tokens{
				Input:       in,
				CachedInput: cr,
				CacheWrite:  cw,
				Output:      tok(u.Get("outputTokens")),
			},
			Final: true,
		})
		p.context = in
	}
	p.pending, p.inFlight = 0, 0
	p.phase = usage.Idle
	data.Get("message.content").ForEach(func(_, b gjson.Result) bool {
		if b.Get("type").String() == "tool-call" {
			p.phase = usage.Tools
			return false
		}
		return true
	})
}
