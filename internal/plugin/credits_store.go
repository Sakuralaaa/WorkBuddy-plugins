package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Credit readings are kept on disk beside the state file: the newest reading
// of each account (so the page has numbers the moment CPA restarts) and a
// history of what each account had left, a point at most every 30 minutes
// unless it changed, for 90 days.
const creditsFileName = ".workbuddy-magpie-credits"

type creditPoint struct {
	T     int64   `json:"t"` // unix ms
	Left  float64 `json:"l"`
	Total float64 `json:"s,omitempty"`
}

type creditsFile struct {
	Snapshot map[string]struct {
		At int64       `json:"at"`
		V  creditsView `json:"v"`
	} `json:"snapshot"`
	History map[string][]creditPoint `json:"history"`
}

var creditStore = struct {
	sync.Mutex
	loaded bool
	dirty  bool
	hist   map[string][]creditPoint
}{}

func creditsPath() string { return filepath.Join(stateDir(), creditsFileName) }

// loadCreditStore reads the file once; creditsMu is taken inside.
func loadCreditStore() {
	creditStore.Lock()
	defer creditStore.Unlock()
	if creditStore.loaded {
		return
	}
	creditStore.loaded = true
	creditStore.hist = map[string][]creditPoint{}
	raw, err := os.ReadFile(creditsPath())
	if err != nil {
		return
	}
	var f creditsFile
	if json.Unmarshal(raw, &f) != nil {
		return
	}
	if f.History != nil {
		creditStore.hist = f.History
	}
	creditsMu.Lock()
	for id, e := range f.Snapshot {
		if _, ok := creditsCache[id]; !ok {
			creditsCache[id] = creditsEntry{at: time.UnixMilli(e.At), v: e.V}
		}
	}
	creditsMu.Unlock()
}

const (
	creditPointGap  = 30 * time.Minute
	creditHistKeep  = 90 * 24 * time.Hour
	creditPointsMax = 90 * 48
)

// noteCredits adds a reading to the history.
func noteCredits(id string, v creditsView, at time.Time) {
	if v.Left == nil {
		return
	}
	loadCreditStore()
	p := creditPoint{T: at.UnixMilli(), Left: *v.Left}
	if v.Total != nil {
		p.Total = *v.Total
	}
	creditStore.Lock()
	h := creditStore.hist[id]
	if n := len(h); n > 0 {
		last := h[n-1]
		if last.Left == p.Left && last.Total == p.Total && at.Sub(time.UnixMilli(last.T)) < creditPointGap {
			creditStore.Unlock()
			return
		}
	}
	h = append(h, p)
	cut := at.Add(-creditHistKeep).UnixMilli()
	i := 0
	for i < len(h) && h[i].T < cut {
		i++
	}
	h = h[i:]
	if len(h) > creditPointsMax {
		h = h[len(h)-creditPointsMax:]
	}
	creditStore.hist[id] = h
	creditStore.dirty = true
	creditStore.Unlock()
	startUsageSaver()
}

// saveCreditStore writes the snapshot and history if they changed.
func saveCreditStore() {
	creditStore.Lock()
	if !creditStore.loaded || !creditStore.dirty {
		creditStore.Unlock()
		return
	}
	f := creditsFile{History: map[string][]creditPoint{}}
	for id, h := range creditStore.hist {
		f.History[id] = h
	}
	creditStore.dirty = false
	creditStore.Unlock()
	f.Snapshot = map[string]struct {
		At int64       `json:"at"`
		V  creditsView `json:"v"`
	}{}
	creditsMu.Lock()
	for id, e := range creditsCache {
		f.Snapshot[id] = struct {
			At int64       `json:"at"`
			V  creditsView `json:"v"`
		}{e.at.UnixMilli(), e.v}
	}
	creditsMu.Unlock()
	raw, err := json.Marshal(f)
	if err != nil {
		return
	}
	p := creditsPath()
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	tmp := p + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) != nil || os.Rename(tmp, p) != nil {
		creditStore.Lock()
		creditStore.dirty = true
		creditStore.Unlock()
	}
}

// handleCreditHistory is the history over the last hours: each account's
// points, and the sum per build at each point in time.
func handleCreditHistory(hours int, account string) (any, error) {
	if hours <= 0 {
		hours = 24 * 7
	}
	if hours > 24*90 {
		hours = 24 * 90
	}
	loadCreditStore()
	from := time.Now().Add(-time.Duration(hours) * time.Hour).UnixMilli()
	list, _ := accounts()
	type series struct {
		Account string        `json:"account"`
		Name    string        `json:"name"`
		Build   string        `json:"build"`
		Points  []creditPoint `json:"points"`
	}
	var out []series
	creditStore.Lock()
	for _, x := range list {
		if account != "" && x.ID != account {
			continue
		}
		h := creditStore.hist[x.ID]
		var pts []creditPoint
		var before *creditPoint
		for i := range h {
			if h[i].T < from {
				before = &h[i]
				continue
			}
			pts = append(pts, h[i])
		}
		if before != nil {
			// what it had when the window opened
			b := *before
			b.T = from
			pts = append([]creditPoint{b}, pts...)
		}
		if len(pts) == 0 {
			continue
		}
		name := x.Name
		if x.A != nil && x.A.AccountID != "" {
			name = x.A.AccountID
		}
		out = append(out, series{Account: x.ID, Name: name, Build: buildOf(x.Site), Points: pts})
	}
	creditStore.Unlock()
	// the sum per build: step through every point in time, carrying each
	// account's last value forward
	type sumPt struct {
		T  int64   `json:"t"`
		CN float64 `json:"cn"`
		AI float64 `json:"ai"`
	}
	var times []int64
	seen := map[int64]bool{}
	for _, s := range out {
		for _, p := range s.Points {
			if !seen[p.T] {
				seen[p.T] = true
				times = append(times, p.T)
			}
		}
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	idx := make([]int, len(out))
	sums := make([]sumPt, 0, len(times))
	for _, t := range times {
		sp := sumPt{T: t}
		for i, s := range out {
			for idx[i]+1 < len(s.Points) && s.Points[idx[i]+1].T <= t {
				idx[i]++
			}
			if s.Points[idx[i]].T > t {
				continue
			}
			if s.Build == "ai" {
				sp.AI += s.Points[idx[i]].Left
			} else {
				sp.CN += s.Points[idx[i]].Left
			}
		}
		sums = append(sums, sp)
	}
	return reply(200, map[string]any{"hours": hours, "from": from, "accounts": out, "sum": sums})
}

// ---- background readings ------------------------------------------------------------

var creditBG = struct {
	sync.Mutex
	last time.Time
	busy bool
}{}

// creditsTick reads every account's credits when the interval has passed.
func creditsTick(now time.Time) {
	every := cfg().creditsRefresh()
	if every <= 0 {
		return
	}
	creditBG.Lock()
	if creditBG.busy || now.Sub(creditBG.last) < every-30*time.Second {
		creditBG.Unlock()
		return
	}
	creditBG.busy = true
	creditBG.last = now
	creditBG.Unlock()
	go func() {
		defer func() {
			creditBG.Lock()
			creditBG.busy = false
			creditBG.Unlock()
		}()
		_, _ = handleCredits(HostDoer{}, "", true)
	}()
}
