package engine

import "time"

// ActivityDays is how far back the activity calendar goes: 53 weeks, so
// the grid is full whichever weekday today is.
const ActivityDays = 53 * 7

// ActDay is one day of the activity calendar.
type ActDay struct {
	Date      string `json:"date"`
	Requests  int64  `json:"requests"`
	Tokens    int64  `json:"tokens"`
	CostMicro int64  `json:"costMicro"`
}

// Activity is a calendar of the last ActivityDays days, oldest first, with
// streaks of days that had any reply.
type Activity struct {
	Days       []ActDay `json:"days"`
	ActiveDays int      `json:"activeDays"`
	// Current counts back from today, or from yesterday while today has
	// nothing yet, so a streak doesn't break before the day is over
	Current     int    `json:"current"`
	Longest     int    `json:"longest"`
	LongestTo   string `json:"longestTo,omitempty"` // the longest streak's last day
	Best        ActDay `json:"best"`                // the day with the most tokens
	TotalTokens int64  `json:"totalTokens"`
}

// Activity computes the calendar at nowMs.
func (e *Engine) Activity(nowMs int64) Activity {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.UnixMilli(nowMs).In(e.loc)
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, e.loc)
	a := Activity{Days: make([]ActDay, ActivityDays)}
	run := 0
	for i := range a.Days {
		date := today.AddDate(0, 0, i-ActivityDays+1).Format("2006-01-02")
		x := ActDay{Date: date}
		if dd := e.days[date]; dd != nil {
			x.Requests, x.Tokens, x.CostMicro = dd.total.Requests, dd.total.Total(), dd.total.CostMicro
		}
		a.Days[i] = x
		if x.Requests <= 0 {
			run = 0
			continue
		}
		a.ActiveDays++
		a.TotalTokens += x.Tokens
		run++
		if run >= a.Longest {
			a.Longest, a.LongestTo = run, date
		}
		if x.Tokens > a.Best.Tokens {
			a.Best = x
		}
	}
	last := len(a.Days) - 1
	if a.Days[last].Requests <= 0 {
		last-- // today is not over
	}
	for i := last; i >= 0 && a.Days[i].Requests > 0; i-- {
		a.Current++
	}
	return a
}
