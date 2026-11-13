package plugin

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"github.com/Sakuralaaa/WorkBuddy-plugins/internal/wb"
)

func fwCall(auth string, site *wb.Site, model string) *call {
	return &call{req: executorRequest{ExecutorRequest: pluginapi.ExecutorRequest{AuthID: auth}}, site: site, model: model}
}

func TestNoteFreeRowRecordsAndWindow(t *testing.T) {
	// Point the free-window store at a temp file so the test never
	// touches the real rows.
	freewin.Lock()
	freewin.loaded = true
	freewin.path = filepath.Join(t.TempDir(), "freewin")
	freewin.rows = nil
	freewin.dirty = false
	freewin.Unlock()
	t.Cleanup(func() {
		freewin.Lock()
		freewin.loaded = false
		freewin.path = ""
		freewin.rows = nil
		freewin.Unlock()
	})

	site := wb.SiteCN()
	now := time.Now()
	c1 := fwCall("auth-1", site, "deepseek-v4.1-flash")
	noteFreeRow(c1, &usageMeter{pt: 8_000_000, ch: 2_000_000, ct: 0}, nil, now)
	noteFreeRow(c1, &usageMeter{pt: 15_000_000, ch: 5_000_000, ct: 0}, nil, now.Add(time.Minute))
	// Another account, and another model, must not count for auth-1.
	c2 := fwCall("auth-2", site, "deepseek-v4.1-flash")
	noteFreeRow(c2, &usageMeter{pt: 90_000_000}, nil, now)
	c3 := fwCall("auth-1", site, "glm-5.2")
	noteFreeRow(c3, &usageMeter{pt: 10_000_000}, nil, now)
	if got := freeRows("auth-1", false); len(got) != 2 {
		t.Fatalf("auth-1 rows = %d, want 2", len(got))
	}
	w := freeWindowFor("auth-1", site)
	if w == nil {
		t.Fatal("want a window, got nil")
	}
	// Counted is prompt + completion, with the cache read once: the
	// first row is 8M prompt (2M of it cached) and the second 15M
	// prompt (5M cached), so 8M + 15M = 23M, not 30M.
	if w.Amount != 23_000_000 {
		t.Fatalf("amount = %v, want 23000000", w.Amount)
	}
	if !w.Aside || w.Refused {
		t.Fatalf("aside=%v refused=%v", w.Aside, w.Refused)
	}
	if w := freeWindowFor("auth-2", site); w == nil || w.Amount != 90_000_000 {
		t.Fatalf("auth-2 window = %+v, want 90000000", w)
	}
}

func TestNoteFreeRow429KeepsReset(t *testing.T) {
	freewin.Lock()
	freewin.loaded = true
	freewin.path = filepath.Join(t.TempDir(), "freewin")
	freewin.rows = nil
	freewin.dirty = false
	freewin.Unlock()
	t.Cleanup(func() {
		freewin.Lock()
		freewin.loaded = false
		freewin.path = ""
		freewin.rows = nil
		freewin.Unlock()
	})

	site := wb.SiteCN()
	now := time.Now()
	c := fwCall("auth-1", site, "deepseek-v4.1-flash")
	noteFreeRow(c, &usageMeter{}, nil, now)
	err := fail("rate_limited", "429 usage exceeds frequency limit, your usage will reset at 2030-01-02 03:04:05 UTC+8", 429)
	noteFreeRow(c, &usageMeter{}, err, now.Add(time.Minute))
	rows := freeRows("auth-1", false)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (one answered, one 429)", len(rows))
	}
	if rows[1].Status != 429 {
		t.Fatalf("second row status = %d, want 429", rows[1].Status)
	}
	w := freeWindowFor("auth-1", site)
	if w == nil || !w.Refused {
		t.Fatalf("want a refused window, got %+v", w)
	}
	if w.ResetsAt != "2030-01-01T19:04:05.000Z" {
		t.Fatalf("resetsAt = %q", w.ResetsAt)
	}
}

func TestFreewinPersists(t *testing.T) {
	freewin.Lock()
	freewin.loaded = true
	freewin.path = filepath.Join(t.TempDir(), "freewin")
	freewin.rows = nil
	freewin.dirty = false
	freewin.Unlock()
	t.Cleanup(func() {
		freewin.Lock()
		freewin.loaded = false
		freewin.path = ""
		freewin.rows = nil
		freewin.Unlock()
	})

	site := wb.SiteCN()
	c := fwCall("auth-1", site, "deepseek-v4.1-flash")
	noteFreeRow(c, &usageMeter{pt: 1_000_000}, nil, time.Now())
	freewin.Lock()
	saveFreewinLocked()
	freewin.loaded = false
	freewin.rows = nil
	freewin.Unlock()
	if got := freeRows("auth-1", false); len(got) != 1 {
		t.Fatalf("reloaded rows = %d, want 1", len(got))
	}
}
