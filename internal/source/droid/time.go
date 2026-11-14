package droid

import "time"

// parseTime reads a timestamp by METRICS 3.1: YYYY-MM-DD, T/t/space,
// hh:mm:ss, optional fraction, then Z/z or ±hh:mm / ±hhmm. ASCII only,
// nothing around it, at most 64 bytes. It returns Unix milliseconds.
func parseTime(s string) (int64, bool) {
	n := len(s)
	if n < 20 || n > 64 {
		return 0, false
	}
	num := func(i, k int) (int, bool) {
		v := 0
		for j := i; j < i+k; j++ {
			if j >= n || s[j] < '0' || s[j] > '9' {
				return 0, false
			}
			v = v*10 + int(s[j]-'0')
		}
		return v, true
	}
	y, ok1 := num(0, 4)
	mo, ok2 := num(5, 2)
	d, ok3 := num(8, 2)
	h, ok4 := num(11, 2)
	mi, ok5 := num(14, 2)
	sec, ok6 := num(17, 2)
	if !(ok1 && ok2 && ok3 && ok4 && ok5 && ok6) {
		return 0, false
	}
	if s[4] != '-' || s[7] != '-' || (s[10] != 'T' && s[10] != 't' && s[10] != ' ') || s[13] != ':' || s[16] != ':' {
		return 0, false
	}
	if mo < 1 || mo > 12 || d < 1 || h > 23 || mi > 59 || sec > 59 {
		return 0, false
	}
	leap := y%4 == 0 && (y%100 != 0 || y%400 == 0)
	dim := [...]int{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}[mo-1]
	if mo == 2 && leap {
		dim = 29
	}
	if d > dim {
		return 0, false
	}
	i := 19
	var ms int64
	if i < n && s[i] == '.' {
		i++
		start := i
		for i < n && s[i] >= '0' && s[i] <= '9' {
			if i-start < 3 {
				ms = ms*10 + int64(s[i]-'0')
			}
			i++
		}
		if i == start {
			return 0, false
		}
		for k := i - start; k < 3; k++ {
			ms *= 10
		}
	}
	var off int64
	switch {
	case i == n-1 && (s[i] == 'Z' || s[i] == 'z'):
	case (i == n-6 && s[i+3] == ':') || i == n-5:
		if s[i] != '+' && s[i] != '-' {
			return 0, false
		}
		oh, a := num(i+1, 2)
		j := i + 3
		if s[j] == ':' {
			j++
		}
		om, b := num(j, 2)
		if !a || !b || oh > 23 || om > 59 {
			return 0, false
		}
		off = int64(oh*3600 + om*60)
		if s[i] == '-' {
			off = -off
		}
	default:
		return 0, false
	}
	u := time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.UTC).Unix()
	secs := u + int64(h*3600+mi*60+sec) - off
	return secs*1000 + ms, true
}
