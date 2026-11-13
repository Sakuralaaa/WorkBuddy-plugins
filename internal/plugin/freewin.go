package plugin

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/Sakuralaaa/WorkBuddy-plugins/internal/wb"
)

// The free-token window's rows: one per relayed request of the window's
// model (deepseek-v4.1-flash and its -sg id, both builds), kept in a file
// beside the usage ledger. magpie reads its own usage ledger and routing
// traces for these; CPA keeps no ledger a plugin can read, so the plugin
// records its own rows as requests go through — per-request, as magpie's
// are, because the window is a chain and the hourly usage buckets are too
// coarse for it (a chain read from bucket starts would be shifted by up
// to an hour).
//
// Only numbers and a refusal's message are kept, never a prompt or an
// answer. Rows older than 30 days are dropped on save (a window is 24h;
// a chain's edges live well inside that), and the file is capped at
// 20,000 rows, oldest first.
const freewinFileName = ".workbuddy-magpie-freewin"

const (
	freewinKeepDays = 30 * 24 * time.Hour
	freewinMaxRows  = 20000
	freewinErrCap   = 1000
)

var freewin = struct {
	sync.Mutex
	loaded bool
	dirty  bool
	path   string
	rows   []wb.FreeRow
}{}

func loadFreewinLocked() {
	if freewin.loaded {
		return
	}
	freewin.loaded = true
	freewin.rows = nil
	if freewin.path == "" {
		freewin.path = filepath.Join(stateDir(), freewinFileName)
	}
	raw, err := os.ReadFile(freewin.path)
	if err != nil {
		return
	}
	var rows []wb.FreeRow
	if json.Unmarshal(raw, &rows) != nil {
		return
	}
	freewin.rows = rows
}

func saveFreewinLocked() {
	if freewin.path == "" || !freewin.dirty {
		return
	}
	now := time.Now()
	kept := freewin.rows[:0]
	for _, r := range freewin.rows {
		if now.Sub(r.At) <= freewinKeepDays {
			kept = append(kept, r)
		}
	}
	if len(kept) > freewinMaxRows {
		kept = append([]wb.FreeRow(nil), kept[len(kept)-freewinMaxRows:]...)
	}
	freewin.rows = kept
	raw, err := json.Marshal(freewin.rows)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(freewin.path), 0o700)
	tmp := freewin.path + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil && os.Rename(tmp, freewin.path) == nil {
		freewin.dirty = false
	}
}

// noteFreeRow records one relayed request of the window's model. It is
// called from noteUsage, which has already turned an error frame's text
// into err when there was one.
func noteFreeRow(c *call, m *usageMeter, err error, now time.Time) {
	if c == nil || c.site == nil || !wb.WindowModelOf(c.model) {
		return
	}
	row := wb.FreeRow{
		Provider: c.site.ID,
		Account:  c.req.AuthID,
		Model:    c.model,
		At:       now,
		Status:   200,
	}
	if err != nil {
		var f *Failure
		if errors.As(err, &f) && f.E != nil {
			row.Status = f.E.HTTPStatus
			row.Err = f.E.Message
		} else {
			row.Status = 0 // failed, status unknown (a stream broke off)
			row.Err = err.Error()
		}
	}
	if m != nil {
		if m.errMsg != "" {
			row.Err = m.errMsg
		}
		if err == nil {
			row.In = m.pt - m.ch
			if row.In < 0 {
				row.In = 0
			}
			row.CacheRead = m.ch
			row.Out = m.ct
		}
	}
	if len(row.Err) > freewinErrCap {
		row.Err = row.Err[:freewinErrCap]
	}
	// A row that is neither answered nor a 429, and carries no message,
	// says nothing about the window.
	if row.Status != 200 && row.Status != 429 && row.Err == "" {
		return
	}
	freewin.Lock()
	loadFreewinLocked()
	freewin.rows = append(freewin.rows, row)
	freewin.dirty = true
	freewin.Unlock()
	startUsageSaver()
}

// freeRows are an auth's recorded rows of the window's model on one build,
// oldest first.
func freeRows(authID string, ai bool) []wb.FreeRow {
	provider := wb.CNFreeWindow.Provider
	if ai {
		provider = wb.AIFreeWindow.Provider
	}
	freewin.Lock()
	defer freewin.Unlock()
	loadFreewinLocked()
	var out []wb.FreeRow
	for _, r := range freewin.rows {
		if r.Account == authID && r.Provider == provider {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// freeWindowFor is an auth's free-token window on a build, or nil when no
// row of its is in a live window.
func freeWindowFor(authID string, site *wb.Site) *wb.Window {
	if site == nil {
		return nil
	}
	return wb.FreeWindowOf(freeRows(authID, site.AI), authID, time.Now(), wb.FreeWindowSpecFor(site.AI))
}

// clearFreewin drops the free window's rows with the usage ledger they sit
// beside, so a cleared ledger shows no stale estimate.
func clearFreewin() {
	freewin.Lock()
	defer freewin.Unlock()
	loadFreewinLocked()
	freewin.rows = nil
	freewin.dirty = true
	saveFreewinLocked()
}
