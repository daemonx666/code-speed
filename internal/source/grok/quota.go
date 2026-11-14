package grok

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/usage"
)

const quotaTail = 2 << 20

// logPath is logs/unified.jsonl of the data folder that holds the
// sessions folder this file lives in.
func (p *parser) logPath() string {
	// updates.jsonl, <session>, <cwd>, sessions, <data>
	data := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(p.path))))
	return filepath.Join(data, "logs", "unified.jsonl")
}

// reportQuota reads the newest "billing: fetched credits config" line of
// the log's last 2 MiB (QUOTAS §9.2, log route; no network).
func (p *parser) reportQuota() {
	f, err := os.Open(p.logPath())
	if err != nil {
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return
	}
	off := int64(0)
	if st.Size() > quotaTail {
		off = st.Size() - quotaTail
	}
	buf := make([]byte, st.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
		return
	}
	if off > 0 { // skip the first, partial line
		i := 0
		for i < len(buf) && buf[i] != '\n' {
			i++
		}
		buf = buf[min(i+1, len(buf)):]
	}
	var best int64 = -1
	var cfg string
	for len(buf) > 0 {
		var line []byte
		i := 0
		for i < len(buf) && buf[i] != '\n' {
			i++
		}
		line, buf = buf[:i], buf[min(i+1, len(buf)):]
		if !source.IsObject(line) || !bytes.Contains(line, []byte("billing: fetched credits config")) {
			continue
		}
		r := gjson.ParseBytes(line)
		if r.Get("msg").String() != "billing: fetched credits config" {
			continue
		}
		ms, ok := source.ParseTime(r.Get("ts").String())
		if !ok || ms < best {
			continue
		}
		c := r.Get("ctx.config")
		if !c.IsObject() {
			continue
		}
		best, cfg = ms, c.Raw
	}
	if best < 0 || best == p.quotaMs {
		return
	}
	w, ok := quotaWindow(gjson.Parse(cfg))
	if !ok {
		return
	}
	p.quotaMs = best
	p.sink.Quota(usage.Quota{Source: Source, ObservedMs: best, Windows: []usage.QuotaWindow{w}})
}

// quotaWindow turns a billing config into its one window (QUOTAS §9.2).
func quotaWindow(c gjson.Result) (usage.QuotaWindow, bool) {
	var w usage.QuotaWindow
	period := c.Get("currentPeriod")
	month := false
	if t := period.Get("type").String(); strings.Contains(strings.ToUpper(t), "MONTH") {
		month = true
	}
	if pct := c.Get("creditUsagePercent"); pct.Type == gjson.Number {
		w.UsedPercent = min(max(pct.Float(), 0), 100)
	} else if c.Get("isUnifiedBillingUser").Type == gjson.True &&
		period.Get("start").Exists() && period.Get("end").Exists() {
		w.UsedPercent = 0
	} else {
		return w, false
	}
	endStr := period.Get("end").String()
	if endStr == "" {
		endStr = c.Get("billingPeriodEnd").String()
	}
	end, endOK := source.ParseTime(endStr)
	if endOK {
		w.ResetsAtMs = end
	}
	if month {
		w.Kind = "month"
		if s, ok := source.ParseTime(period.Get("start").String()); ok && endOK && end > s {
			w.WindowMinutes = int64(time.Duration((end - s) * int64(time.Millisecond)).Minutes())
		}
	} else {
		w.Kind = "sevenDay"
		w.WindowMinutes = 7 * 24 * 60
	}
	return w, true
}
