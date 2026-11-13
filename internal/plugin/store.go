package plugin

import (
	"crypto/sha256"
	"encoding/hex"
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

// StickyBinding is a client session bound to the account that first served
// it. CPA picks the credential for a request; the plugin cannot override
// that choice, so a binding is a record, not a route: when a session comes
// back on another account the hop is counted and logged, and the request
// keeps going (its conversation ids are re-derived for the new account, so
// the prompt cache is never shared across accounts). A binding expires
// after a day without being seen, after which the next account binds it.
type StickyBinding struct {
	Session string `json:"session"` // sha256 of the session id, first 16 hex digits
	AuthID  string `json:"auth"`
	First   int64  `json:"first_unix"`
	Last    int64  `json:"last_unix"`
	Hops    int    `json:"hops"`
}

// CheckinRetry is a failed check-in's retry state for one Beijing day.
type CheckinRetry struct {
	Day   string `json:"day"`
	Tries int    `json:"tries"`
	Next  int64  `json:"next_unix"`
}

type persisted struct {
	Slots   map[string]map[string]string `json:"slots"`                   // account → job → slot
	OKDay   map[string]map[string]string `json:"ok_day"`                  // account → job → Beijing day
	Last    map[string]map[string]Run    `json:"last"`                    // account → job[:task] → run
	History []Run                        `json:"history"`                 // newest last, bounded
	Sticky  map[string]StickyBinding     `json:"sticky,omitempty"`        // session hash → binding
	Retry   map[string]CheckinRetry      `json:"checkin_retry,omitempty"` // account → retry state
}

var state = struct {
	sync.Mutex
	loaded bool
	dirty  bool
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
	if p.Sticky == nil {
		p.Sticky = map[string]StickyBinding{}
	}
	if p.Retry == nil {
		p.Retry = map[string]CheckinRetry{}
	}
}

const stickyTTL = 24 * time.Hour

func stickyKey(session string) string {
	sum := sha256.Sum256([]byte(session))
	return hex.EncodeToString(sum[:])[:16]
}

// stickyBind records that session was served by authID. It reports whether
// the session hopped accounts (it was bound to another auth and the binding
// has not expired) and how many hops it has made in total. A hop rebinds the
// session to the account now serving it, so the turns that follow on that
// account count no further hops. Only a new binding or a hop is written at
// once; a binding merely seen again is saved by the 30-second saver.
func stickyBind(session, authID string) (hopped bool, hops int) {
	if session == "" || authID == "" {
		return false, 0
	}
	key := stickyKey(session)
	now := time.Now()
	state.Lock()
	defer state.Unlock()
	loadStateLocked()
	b, ok := state.p.Sticky[key]
	if !ok || stickyExpired(b, now) {
		pruneStickyLocked(now)
		state.p.Sticky[key] = StickyBinding{Session: key, AuthID: authID, First: now.Unix(), Last: now.Unix()}
		saveStateLocked()
		return false, 0
	}
	b.Last = now.Unix()
	if b.AuthID == authID {
		state.p.Sticky[key] = b
		state.dirty = true
		startUsageSaver()
		return false, b.Hops
	}
	b.Hops++
	b.AuthID = authID
	state.p.Sticky[key] = b
	saveStateLocked()
	return true, b.Hops
}

func stickyExpired(b StickyBinding, now time.Time) bool {
	return time.Unix(b.Last, 0).Add(stickyTTL).Before(now)
}

// pruneStickyLocked drops the bindings that have expired.
func pruneStickyLocked(now time.Time) {
	for k, b := range state.p.Sticky {
		if stickyExpired(b, now) {
			delete(state.p.Sticky, k)
		}
	}
}

// saveStateIfDirty writes the state a binding left unsaved.
func saveStateIfDirty() {
	state.Lock()
	defer state.Unlock()
	if state.dirty {
		saveStateLocked()
	}
}

// stickyBindings are the live session→account bindings, newest first.
func stickyBindings() []StickyBinding {
	state.Lock()
	defer state.Unlock()
	loadStateLocked()
	out := make([]StickyBinding, 0, len(state.p.Sticky))
	now := time.Now()
	for _, b := range state.p.Sticky {
		if stickyExpired(b, now) {
			continue
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Last > out[j].Last })
	return out
}

// checkinRetryOf is a failed check-in's retry state for account and day.
func checkinRetryOf(account, day string) (tries int, next time.Time, ok bool) {
	state.Lock()
	defer state.Unlock()
	loadStateLocked()
	r, ok := state.p.Retry[account]
	if !ok || r.Day != day {
		return 0, time.Time{}, false
	}
	return r.Tries, time.Unix(r.Next, 0), true
}

// noteCheckinRetry records a failed check-in: tries so far and when the
// next attempt may run (the caller picks the backoff).
func noteCheckinRetry(account, day string, tries int, next time.Time) {
	state.Lock()
	defer state.Unlock()
	loadStateLocked()
	state.p.Retry[account] = CheckinRetry{Day: day, Tries: tries, Next: next.Unix()}
	saveStateLocked()
}

// clearCheckinRetry drops a day's retry state (the check-in settled).
func clearCheckinRetry(account, day string) {
	state.Lock()
	defer state.Unlock()
	loadStateLocked()
	if r, ok := state.p.Retry[account]; ok && r.Day == day {
		delete(state.p.Retry, account)
		saveStateLocked()
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
	if os.WriteFile(tmp, raw, 0o600) == nil && os.Rename(tmp, state.path) == nil {
		state.dirty = false
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

// lastRunAt is when an account last ran a job (zero when never).
func lastRunAt(account, job string) time.Time {
	state.Lock()
	defer state.Unlock()
	loadStateLocked()
	if r, ok := state.p.Last[account][job]; ok {
		return r.At
	}
	return time.Time{}
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
