package engine

import (
	"math"
	"sort"

	"github.com/daemonx666/code-speed/internal/usage"
)

// Dist is how a model's replies spread over output speed and over time to
// the first content: two log-scale histograms, four buckets an octave
// (each about 19 % wide), so a day of a model keeps a few dozen numbers
// however many replies it had, and percentiles merge across days.
type Dist struct {
	Speed map[int]int64 `json:"s,omitempty"` // bucket of tok/s → replies
	TTFT  map[int]int64 `json:"f,omitempty"` // bucket of ms → replies
}

const (
	perOctave  = 4
	speedMax   = 64 // 2^16 tok/s
	ttftBase   = 10 // ms of bucket 0
	ttftMax    = 64 // 10 ms · 2^16, about 11 minutes
	minReplies = 5  // fewer and a percentile says little
)

func bucket(v float64, max int) int {
	if v <= 1 {
		return 0
	}
	return min(int(math.Floor(perOctave*math.Log2(v))), max)
}

// mid is a bucket's middle (geometric) value.
func mid(b int) float64 { return math.Exp2((float64(b) + 0.5) / perOctave) }

func speedBucket(tps float64) int { return bucket(tps, speedMax) }
func ttftBucket(ms int64) int     { return bucket(float64(ms)/ttftBase, ttftMax) }

// ttftOf is a response's time to first content, 0 when its log doesn't say.
func ttftOf(r usage.Response) int64 {
	if r.FirstMs > 0 && r.StartMs > 0 && r.FirstMs >= r.StartMs {
		return r.FirstMs - r.StartMs
	}
	return 0
}

func bump(m *map[int]int64, b int, sign int64) {
	if *m == nil {
		*m = map[int]int64{}
	}
	(*m)[b] += sign
	if (*m)[b] <= 0 {
		delete(*m, b)
	}
}

// apply counts (sign 1) or uncounts (-1) a response. It reports whether
// the response had anything to count.
func (d *Dist) apply(r usage.Response, sign int64) bool {
	any := false
	if r.Measured() {
		if s, ok := r.Speed(); ok && s > 0 {
			bump(&d.Speed, speedBucket(s), sign)
			any = true
		}
	}
	if t := ttftOf(r); t > 0 {
		bump(&d.TTFT, ttftBucket(t), sign)
		any = true
	}
	return any
}

func (d *Dist) add(o *Dist) {
	for b, n := range o.Speed {
		bump(&d.Speed, b, n)
	}
	for b, n := range o.TTFT {
		bump(&d.TTFT, b, n)
	}
}

// quantiles returns the count and the values at qs of a histogram.
func quantiles(h map[int]int64, scale float64, qs ...float64) (int64, []float64) {
	var n int64
	keys := make([]int, 0, len(h))
	for b, c := range h {
		n += c
		keys = append(keys, b)
	}
	out := make([]float64, len(qs))
	if n == 0 {
		return 0, out
	}
	sort.Ints(keys)
	for i, q := range qs {
		want := int64(math.Ceil(q * float64(n)))
		want = max(want, 1)
		var seen int64
		for _, b := range keys {
			seen += h[b]
			if seen >= want {
				out[i] = mid(b) * scale
				break
			}
		}
	}
	return n, out
}

// SpeedRow is one model's (or agent's) speed and first-content spread.
type SpeedRow struct {
	Source usage.Source `json:"source"`
	Label  string       `json:"label"` // the agent's name
	Model  string       `json:"model"`
	// over the timed replies, output tok/s: the slow tenth, the median and
	// the fast tenth
	Timed int64   `json:"timed"`
	P10   float64 `json:"p10,omitempty"`
	P50   float64 `json:"p50,omitempty"`
	P90   float64 `json:"p90,omitempty"`
	// over the replies whose log gives the first content's time, ms
	FirstN   int64   `json:"firstN"`
	First50  float64 `json:"first50,omitempty"`
	First95  float64 `json:"first95,omitempty"`
	Requests int64   `json:"requests"`
}

// speedRows ranks models by median speed; models with too few timed
// replies come last, by first-content time, or are left out.
func speedRows(dists map[modelKey]*Dist, reqs map[modelKey]*Agg) []SpeedRow {
	var out []SpeedRow
	for k, d := range dists {
		r := SpeedRow{Source: k.Source, Label: k.Source.Label(), Model: k.Model}
		var v []float64
		if r.Timed, v = quantiles(d.Speed, 1, 0.1, 0.5, 0.9); r.Timed >= minReplies {
			r.P10, r.P50, r.P90 = v[0], v[1], v[2]
		}
		if r.FirstN, v = quantiles(d.TTFT, ttftBase, 0.5, 0.95); r.FirstN >= minReplies {
			r.First50, r.First95 = v[0], v[1]
		}
		if r.P50 == 0 && r.First50 == 0 {
			continue
		}
		if a := reqs[k]; a != nil {
			r.Requests = a.Requests
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.P50 != b.P50 {
			return a.P50 > b.P50
		}
		if a.First50 != b.First50 {
			return a.First50 < b.First50
		}
		return a.Label+a.Model < b.Label+b.Model
	})
	return out
}
