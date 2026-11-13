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

// The plugin keeps its job state (last results, schedule slots, a bounded
// run history) in one file beside CPA's credential files. Its name has no
// .json suffix, so CPA never reads it as a credential.
const stateFileName = ".workbuddy-magpie-state"

// Ring is a bounded run history, newest last.
type Ring struct {
	Max  int
	Runs []Run
}

// Add appends a run, dropping the oldest past Max.
func (r *Ring) Add(x Run) {
	r.Runs = append(r.Runs, x)
	if r.Max > 0 && len(r.Runs) > r.Max {
		r.Runs = append([]Run(nil), r.Runs[len(r.Runs)-r.Max:]...)
	}
}

// Newest are up to n runs, newest first, those of account only when set.
func (r *Ring) Newest(n int, account string) []Run {
	var out []Run
	for i := len(r.Runs) - 1; i >= 0 && (n <= 0 || len(out) < n); i-- {
		if account != "" && r.Runs[i].Account != account {
			continue
		}
		out = append(out, r.Runs[i])
	}
	return out
}

type persisted struct {
	Slots   map[string]map[string]string `json:"slots"`   // account → job → slot
	OKDay   map[string]map[string]string `json:"ok_day"`  // account → job → Beijing day
	Last    map[string]map[string]Run    `json:"last"`    // account → job[:task] → run
	History []Run                        `json:"history"` // newest last, bounded
}

var state = struct {
	sync.Mutex
	loaded bool
	path   string
	p      persisted
	ring   Ring
}{ring: Ring{Max: 300}}

const persistHistory = 120

func (p *persisted) init() {
	if p.Slots == nil {
		p.Slots = map[string]map[string]string{}
	}
	if p.OKDay == nil {
		p.OKDay = map[string]map[string]string{}
	}
	if p.Last == nil {
		p.Last = map[string]map[string]Run{}
	}
}

// stateDir is where the state file goes: CPA's auth directory (from a
// credential's path), else the user's config directory.
func stateDir() string {
	if entries, err := authList(); err == nil {
		for _, e := range entries {
			if e.Path != "" {
				return filepath.Dir(e.Path)
			}
		}
	}
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "cliproxyapi-workbuddy-magpie")
	}
	return os.TempDir()
}

// loadState reads the state file once (locked by the caller).
func loadStateLocked() {
	if state.loaded {
		return
	}
	state.loaded = true
	state.path = filepath.Join(stateDir(), stateFileName)
	if raw, err := os.ReadFile(state.path); err == nil {
		_ = json.Unmarshal(raw, &state.p)
	}
	state.p.init()
	state.ring.Runs = append(state.ring.Runs[:0], state.p.History...)
}

func saveStateLocked() {
	if state.path == "" {
		return
	}
	h := state.ring.Runs
	if len(h) > persistHistory {
		h = h[len(h)-persistHistory:]
	}
	state.p.History = h
	raw, err := json.Marshal(state.p)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(state.path), 0o700)
	tmp := state.path + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, state.path)
	}
}

func lastKey(r Run) string {
	if r.Task != "" {
		return r.Job + ":" + r.Task
	}
	return r.Job
}

// record keeps a finished run.
func record(r Run) {
	// logs in the history stay short
	if len(r.Log) > 60 {
		r.Log = append(r.Log[:60:60], "…")
	}
	state.Lock()
	defer state.Unlock()
	loadStateLocked()
	state.ring.Add(r)
	if state.p.Last[r.Account] == nil {
		state.p.Last[r.Account] = map[string]Run{}
	}
	state.p.Last[r.Account][lastKey(r)] = r
	saveStateLocked()
}

func lastSlot(account, job string) string {
	state.Lock()
	defer state.Unlock()
	loadStateLocked()
	return state.p.Slots[account][job]
}

func markSlot(account, job, slot string) {
	state.Lock()
	defer state.Unlock()
	loadStateLocked()
	if state.p.Slots[account] == nil {
		state.p.Slots[account] = map[string]string{}
	}
	state.p.Slots[account][job] = slot
	saveStateLocked()
}

func okDay(account, job string) string {
	state.Lock()
	defer state.Unlock()
	loadStateLocked()
	return state.p.OKDay[account][job]
}

func markOK(account, job, day string) {
	state.Lock()
	defer state.Unlock()
	loadStateLocked()
	if state.p.OKDay[account] == nil {
		state.p.OKDay[account] = map[string]string{}
	}
	state.p.OKDay[account][job] = day
	saveStateLocked()
}

// lastRuns are an account's last run of each job.
func lastRuns(account string) []Run {
	state.Lock()
	defer state.Unlock()
	loadStateLocked()
	var out []Run
	for _, r := range state.p.Last[account] {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return lastKey(out[i]) < lastKey(out[j]) })
	return out
}

// lastRunsAll are every account's last runs.
func lastRunsAll() map[string][]Run {
	state.Lock()
	ids := make([]string, 0)
	loadStateLocked()
	for id := range state.p.Last {
		ids = append(ids, id)
	}
	state.Unlock()
	out := map[string][]Run{}
	for _, id := range ids {
		out[id] = lastRuns(id)
	}
	return out
}

func history(n int, account string) []Run {
	state.Lock()
	defer state.Unlock()
	loadStateLocked()
	return state.ring.Newest(n, account)
}

func clearHistory() {
	state.Lock()
	defer state.Unlock()
	loadStateLocked()
	state.ring.Runs = nil
	saveStateLocked()
}

func statePath() string {
	state.Lock()
	defer state.Unlock()
	loadStateLocked()
	return state.path
}

var _ = strings.TrimSpace
var _ = time.Now
