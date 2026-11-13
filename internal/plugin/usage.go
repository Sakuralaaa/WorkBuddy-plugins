package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// The plugin keeps a usage ledger of the requests it relays: per hour, per
// account and model, how many there were, how many failed, the tokens and
// credits WorkBuddy's last usage frame named, and how long they took. Only
// numbers are kept, never a prompt or an answer. Hours older than a week are
// folded into days; days older than 90 are dropped.
const usageFileName = ".workbuddy-magpie-usage"

const (
	usageHourKeep = 7 * 24 * time.Hour
	usageDayKeep  = 90 * 24 * time.Hour
)

type usageKey struct {
	T     int64  `json:"t"` // bucket start, unix seconds
	Day   bool   `json:"d,omitempty"`
	Acct  string `json:"a"`
	Build string `json:"b"`
	Model string `json:"m"`
}

type usageBucket struct {
	N      int64   `json:"n"`             // requests
	Fail   int64   `json:"f,omitempty"`   // failed requests
	PT     int64   `json:"pt,omitempty"`  // prompt tokens
	CT     int64   `json:"ct,omitempty"`  // completion tokens
	TT     int64   `json:"tt,omitempty"`  // total tokens
	Cache  int64   `json:"ch,omitempty"`  // cached prompt tokens
	CR     float64 `json:"cr,omitempty"`  // credits charged (usage.credit)
	CRN    int64   `json:"crn,omitempty"` // requests that named a credit
	CRT    int64   `json:"crt,omitempty"` // tokens of those requests
	MS     int64   `json:"ms,omitempty"`  // total time of successful requests
	TTFB   int64   `json:"fb,omitempty"`  // time to first event, summed
	Timed  int64   `json:"tn,omitempty"`  // successful requests timed
	LastAt int64   `json:"la,omitempty"`  // last request, unix ms
}

func (b *usageBucket) add(o *usageBucket) {
	b.N += o.N
	b.Fail += o.Fail
	b.PT += o.PT
	b.CT += o.CT
	b.TT += o.TT
	b.Cache += o.Cache
	b.CR += o.CR
	b.CRN += o.CRN
	b.CRT += o.CRT
	b.MS += o.MS
	b.TTFB += o.TTFB
	b.Timed += o.Timed
	if o.LastAt > b.LastAt {
		b.LastAt = o.LastAt
	}
}

type usageRow struct {
	usageKey
	usageBucket
}

var usage = struct {
	sync.Mutex
	loaded bool
	dirty  bool
	path   string
	b      map[usageKey]*usageBucket
}{}

func loadUsageLocked() {
	if usage.loaded {
		return
	}
	usage.loaded = true
	usage.b = map[usageKey]*usageBucket{}
	usage.path = filepath.Join(stateDir(), usageFileName)
	raw, err := os.ReadFile(usage.path)
	if err != nil {
		return
	}
	var rows []usageRow
	if json.Unmarshal(raw, &rows) != nil {
		return
	}
	for i := range rows {
		b := rows[i].usageBucket
		usage.b[rows[i].usageKey] = &b
	}
}

// rollupLocked folds old hours into Beijing days and drops old days.
func rollupLocked(now time.Time) {
	for k, b := range usage.b {
		at := time.Unix(k.T, 0)
		if now.Sub(at) > usageDayKeep {
			delete(usage.b, k)
			continue
		}
		if !k.Day && now.Sub(at) > usageHourKeep {
			d := k
			d.Day = true
			y, m, dd := at.In(beijing).Date()
			d.T = time.Date(y, m, dd, 0, 0, 0, 0, beijing).Unix()
			if usage.b[d] == nil {
				usage.b[d] = &usageBucket{}
			}
			usage.b[d].add(b)
			delete(usage.b, k)
		}
	}
}

func saveUsageLocked() {
	if usage.path == "" || !usage.dirty {
		return
	}
	rollupLocked(time.Now())
	rows := make([]usageRow, 0, len(usage.b))
	for k, b := range usage.b {
		rows = append(rows, usageRow{k, *b})
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(usage.path), 0o700)
	tmp := usage.path + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil && os.Rename(tmp, usage.path) == nil {
		usage.dirty = false
	}
}

var usageSaver sync.Once

// startUsageSaver writes the ledger at most every 30 seconds while it changes.
func startUsageSaver() {
	usageSaver.Do(func() {
		go func() {
			t := time.NewTicker(30 * time.Second)
			defer t.Stop()
			for range t.C {
				usage.Lock()
				saveUsageLocked()
				usage.Unlock()
			}
		}()
	})
}

// usageMeter watches one relayed answer for its usage.
type usageMeter struct {
	start, first   time.Time
	pt, ct, tt, ch int64
	credit         float64
	hasCredit      bool
}

func newMeter() *usageMeter { return &usageMeter{start: time.Now()} }

func intOf(m map[string]any, keys ...string) (int64, bool) {
	for _, k := range keys {
		if f, ok := m[k].(float64); ok {
			return int64(f), true
		}
	}
	return 0, false
}

// see reads one upstream event (chat or Messages) for its usage.
func (m *usageMeter) see(ev string) {
	if m == nil {
		return
	}
	for _, line := range strings.Split(ev, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		p := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if p == "" || p == "[DONE]" {
			continue
		}
		if m.first.IsZero() {
			m.first = time.Now()
		}
		if !strings.Contains(p, "usage") {
			continue
		}
		m.seeJSON([]byte(p))
	}
}

func (m *usageMeter) seeJSON(raw []byte) {
	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil {
		return
	}
	u, _ := obj["usage"].(map[string]any)
	if u == nil {
		if msg, ok := obj["message"].(map[string]any); ok {
			u, _ = msg["usage"].(map[string]any)
		}
	}
	if u == nil {
		return
	}
	if v, ok := intOf(u, "prompt_tokens", "input_tokens"); ok && v > 0 {
		m.pt = v
	}
	if v, ok := intOf(u, "completion_tokens", "output_tokens"); ok && v > 0 {
		m.ct = v
	}
	if v, ok := intOf(u, "total_tokens"); ok && v > 0 {
		m.tt = v
	}
	if v, ok := intOf(u, "prompt_cache_hit_tokens", "cache_read_input_tokens"); ok && v > 0 {
		m.ch = v
	} else if d, ok := u["prompt_tokens_details"].(map[string]any); ok {
		if v, ok := intOf(d, "cached_tokens"); ok && v > 0 {
			m.ch = v
		}
	}
	if f, ok := u["credit"].(float64); ok {
		m.credit, m.hasCredit = f, true
	}
}

// noteUsage keeps one relayed request; ok false for one that failed.
func noteUsage(c *call, m *usageMeter, ok bool) {
	if c == nil || c.req.AuthID == "" {
		return
	}
	now := time.Now()
	k := usageKey{T: now.Truncate(time.Hour).Unix(), Acct: c.req.AuthID, Build: buildOf(c.site), Model: c.model}
	b := usageBucket{N: 1, LastAt: now.UnixMilli()}
	if !ok {
		b.Fail = 1
	}
	if m != nil {
		b.PT, b.CT, b.Cache = m.pt, m.ct, m.ch
		b.TT = m.tt
		if b.TT == 0 {
			b.TT = m.pt + m.ct
		}
		if m.hasCredit {
			b.CR, b.CRN, b.CRT = m.credit, 1, b.TT
		}
		if ok {
			b.MS = now.Sub(m.start).Milliseconds()
			if !m.first.IsZero() {
				b.TTFB = m.first.Sub(m.start).Milliseconds()
			}
			b.Timed = 1
		}
	}
	usage.Lock()
	loadUsageLocked()
	if usage.b[k] == nil {
		usage.b[k] = &usageBucket{}
	}
	usage.b[k].add(&b)
	usage.dirty = true
	usage.Unlock()
	startUsageSaver()
}

// ---- the page's view -------------------------------------------------------------

type usageAgg struct {
	Key   string `json:"key"`
	Name  string `json:"name,omitempty"`
	Build string `json:"build,omitempty"`
	usageBucket
	SuccessRate float64 `json:"success_rate"`
	CacheRate   float64 `json:"cache_rate"`
	AvgMS       float64 `json:"avg_ms"`
	AvgTTFB     float64 `json:"avg_ttfb_ms"`
	PerMillion  float64 `json:"credits_per_1m"` // credits per million tokens, of the requests that named a credit
}

func finish(a *usageAgg) {
	if a.N > 0 {
		a.SuccessRate = float64(a.N-a.Fail) / float64(a.N) * 100
	}
	if a.PT > 0 {
		a.CacheRate = float64(a.Cache) / float64(a.PT) * 100
	}
	if a.Timed > 0 {
		a.AvgMS = float64(a.MS) / float64(a.Timed)
		a.AvgTTFB = float64(a.TTFB) / float64(a.Timed)
	}
	if a.CRT > 0 {
		a.PerMillion = a.CR / float64(a.CRT) * 1e6
	}
}

func handleUsage(hours int, account string) (any, error) {
	if hours <= 0 {
		hours = 24
	}
	if hours > 24*90 {
		hours = 24 * 90
	}
	now := time.Now()
	from := now.Add(-time.Duration(hours) * time.Hour)
	names := map[string]string{}
	if list, err := accounts(); err == nil {
		for _, x := range list {
			names[x.ID] = x.Name
			if x.A != nil && x.A.AccountID != "" {
				names[x.ID] = x.A.AccountID
			}
		}
	}
	usage.Lock()
	loadUsageLocked()
	rows := make([]usageRow, 0, len(usage.b))
	for k, b := range usage.b {
		end := time.Unix(k.T, 0).Add(time.Hour)
		if k.Day {
			end = time.Unix(k.T, 0).Add(24 * time.Hour)
		}
		if end.Before(from) {
			continue
		}
		if account != "" && k.Acct != account {
			continue
		}
		rows = append(rows, usageRow{k, *b})
	}
	usage.Unlock()

	total := &usageAgg{Key: "total"}
	byAcct := map[string]*usageAgg{}
	byModel := map[string]*usageAgg{}
	daily := hours > 72
	series := map[int64]*usageAgg{}
	for _, r := range rows {
		b := r.usageBucket
		total.add(&b)
		if byAcct[r.Acct] == nil {
			n := names[r.Acct]
			if n == "" {
				n = r.Acct
			}
			byAcct[r.Acct] = &usageAgg{Key: r.Acct, Name: n, Build: r.Build}
		}
		byAcct[r.Acct].add(&b)
		mk := r.Build + ":" + r.Model
		if byModel[mk] == nil {
			byModel[mk] = &usageAgg{Key: r.Model, Build: r.Build}
		}
		byModel[mk].add(&b)
		t := r.T
		if daily || r.Day {
			y, m, d := time.Unix(t, 0).In(beijing).Date()
			t = time.Date(y, m, d, 0, 0, 0, 0, beijing).Unix()
		}
		if series[t] == nil {
			series[t] = &usageAgg{}
		}
		series[t].add(&b)
	}
	finish(total)
	list := func(m map[string]*usageAgg) []*usageAgg {
		out := make([]*usageAgg, 0, len(m))
		for _, a := range m {
			finish(a)
			out = append(out, a)
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].TT != out[j].TT {
				return out[i].TT > out[j].TT
			}
			return out[i].N > out[j].N
		})
		return out
	}
	type point struct {
		T  int64   `json:"t"`
		N  int64   `json:"n"`
		F  int64   `json:"f"`
		TT int64   `json:"tt"`
		CR float64 `json:"cr"`
	}
	pts := make([]point, 0, len(series))
	for t, a := range series {
		pts = append(pts, point{T: t * 1000, N: a.N, F: a.Fail, TT: a.TT, CR: a.CR})
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i].T < pts[j].T })
	step := "hour"
	if daily {
		step = "day"
	}
	return reply(200, map[string]any{
		"hours": hours, "from": from.UnixMilli(), "to": now.UnixMilli(), "step": step,
		"total": total, "by_account": list(byAcct), "by_model": list(byModel), "series": pts,
	})
}

func clearUsage() {
	usage.Lock()
	defer usage.Unlock()
	loadUsageLocked()
	usage.b = map[usageKey]*usageBucket{}
	usage.dirty = true
	saveUsageLocked()
}
