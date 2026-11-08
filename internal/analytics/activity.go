package analytics

import (
	"sort"
	"time"

	"github.com/ht2803-max/tokenmanager/internal/storage"
)

// ActivityDay is one local calendar day of the activity heatmap.
type ActivityDay struct {
	Date   time.Time
	Tokens int64
}

// Activity is daily token usage for a GitHub-style heatmap: whole weeks
// (Monday first) ending with the current week.
type Activity struct {
	Days          []ActivityDay // Monday of the first week … today, contiguous
	Thresholds    [3]int64      // quartile cut points over non-zero days
	Total         int64
	ActiveDays    int
	Streak        int // consecutive active days ending today (or yesterday)
	LongestStreak int
	Peak          ActivityDay
}

// Activity builds the heatmap data for the last `weeks` weeks. Only the
// filter's agent/project/model/session fields are used.
func (e *Engine) Activity(f storage.Filter, weeks int) (Activity, error) {
	now := e.Now()
	loc := now.Location()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	start := PeriodFor("week", now).From.AddDate(0, 0, -7*(max(1, weeks)-1))
	f.From, f.To = start, today.AddDate(0, 0, 1)
	buckets, err := e.DB.TokenBuckets(f)
	if err != nil {
		return Activity{}, err
	}
	return buildActivity(buckets, start, today), nil
}

func buildActivity(buckets map[int64]int64, start, today time.Time) Activity {
	loc := today.Location()
	var a Activity
	idx := map[string]int{}
	for d := start; !d.After(today); d = d.AddDate(0, 0, 1) {
		idx[d.Format("2006-01-02")] = len(a.Days)
		a.Days = append(a.Days, ActivityDay{Date: d})
	}
	for b, n := range buckets {
		t := time.UnixMilli(b * storage.BucketMillis).In(loc)
		if i, ok := idx[t.Format("2006-01-02")]; ok {
			a.Days[i].Tokens += n
		}
	}
	var active []int64
	run := 0
	for _, d := range a.Days {
		a.Total += d.Tokens
		if d.Tokens > 0 {
			active = append(active, d.Tokens)
			run++
			a.LongestStreak = max(a.LongestStreak, run)
			if d.Tokens > a.Peak.Tokens {
				a.Peak = d
			}
		} else {
			run = 0
		}
	}
	a.ActiveDays = len(active)
	// Current streak: today counts if active; otherwise it may end yesterday.
	n := len(a.Days)
	i := n - 1
	if i >= 0 && a.Days[i].Tokens == 0 {
		i--
	}
	for ; i >= 0 && a.Days[i].Tokens > 0; i-- {
		a.Streak++
	}
	if len(active) > 0 {
		sort.Slice(active, func(i, j int) bool { return active[i] < active[j] })
		for q := range a.Thresholds {
			a.Thresholds[q] = active[(q+1)*len(active)/4]
		}
	}
	return a
}

// Level maps a day's tokens to a heatmap intensity 0 (none) … 4 (busiest).
func (a Activity) Level(tokens int64) int {
	if tokens <= 0 {
		return 0
	}
	l := 1
	for _, t := range a.Thresholds {
		if tokens >= t {
			l++
		}
	}
	return min(l, 4)
}
