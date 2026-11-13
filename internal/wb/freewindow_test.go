package wb

import (
	"testing"
	"time"
)

func fwRow(at time.Time, acct, model string, status int, in, cache, out int64, err string) FreeRow {
	return FreeRow{Provider: "workbuddy", Account: acct, Model: model, At: at, Status: status, In: in, CacheRead: cache, Out: out, Err: err}
}

func TestFreeWindowOfSumsOneWindow(t *testing.T) {
	t0 := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	rows := []FreeRow{
		fwRow(t0.Add(1*time.Hour), "a1", "deepseek-v4.1-flash", 200, 8_000_000, 2_000_000, 0, ""),
		fwRow(t0.Add(2*time.Hour), "a1", "deepseek-v4.1-flash-sg", 200, 15_000_000, 5_000_000, 0, ""),
		fwRow(t0.Add(3*time.Hour), "a1", "deepseek-v4.1-flash", 200, 0, 0, 0, ""),
	}
	now := t0.Add(4 * time.Hour)
	w := FreeWindowOf(rows, "a1", now, CNFreeWindow)
	if w == nil {
		t.Fatal("want a window, got nil")
	}
	if w.Amount != 30_000_000 {
		t.Fatalf("amount = %v, want 30000000", w.Amount)
	}
	if w.Used != 15 {
		t.Fatalf("used = %v, want 15", w.Used)
	}
	if w.Display != "30.0M / ~200M" {
		t.Fatalf("display = %q", w.Display)
	}
	if !w.Aside || w.Refused {
		t.Fatalf("aside=%v refused=%v, want aside true refused false", w.Aside, w.Refused)
	}
	if w.ResetsAt != "2026-11-11T01:00:00.000Z" {
		t.Fatalf("resetsAt = %q, want start+24h", w.ResetsAt)
	}
	if len(w.Models) != 2 || w.Models[0] != "deepseek-v4.1-flash" {
		t.Fatalf("models = %v", w.Models)
	}
}

func TestFreeWindowOfGapOpensNewWindow(t *testing.T) {
	t0 := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	rows := []FreeRow{
		fwRow(t0, "a1", "deepseek-v4.1-flash", 200, 50_000_000, 0, 0, ""),
		fwRow(t0.Add(48*time.Hour), "a1", "deepseek-v4.1-flash", 200, 10_000_000, 0, 0, ""),
	}
	w := FreeWindowOf(rows, "a1", t0.Add(49*time.Hour), CNFreeWindow)
	if w == nil {
		t.Fatal("want a window, got nil")
	}
	if w.Amount != 10_000_000 {
		t.Fatalf("amount = %v, want 10000000 (only the post-gap window)", w.Amount)
	}
}

func TestFreeWindowOfLiveRefusalWins(t *testing.T) {
	t0 := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	rows := []FreeRow{
		fwRow(t0.Add(1*time.Hour), "a1", "deepseek-v4.1-flash", 200, 5_000_000, 0, 0, ""),
		fwRow(t0.Add(2*time.Hour), "a1", "deepseek-v4.1-flash", 429, 0, 0, 0,
			"429 usage exceeds frequency limit, but don't worry, your usage will reset at 2026-11-11 19:47:41 UTC+8"),
	}
	now := t0.Add(3 * time.Hour)
	w := FreeWindowOf(rows, "a1", now, CNFreeWindow)
	if w == nil {
		t.Fatal("want a window, got nil")
	}
	if !w.Refused || w.Aside {
		t.Fatalf("refused=%v aside=%v, want refused true aside false", w.Refused, w.Aside)
	}
	if w.Amount != CNFreeWindow.Cap || w.Used != 100 {
		t.Fatalf("amount=%v used=%v, want cap and 100", w.Amount, w.Used)
	}
	if w.ResetsAt != "2026-11-11T11:47:41.000Z" {
		t.Fatalf("resetsAt = %q, want the vendor's reset in UTC", w.ResetsAt)
	}
}

func TestFreeWindowOfChineseRefusalText(t *testing.T) {
	t0 := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	rows := []FreeRow{
		fwRow(t0.Add(1*time.Hour), "a1", "deepseek-v4.1-flash", 200, 5_000_000, 0, 0, ""),
		fwRow(t0.Add(2*time.Hour), "a1", "deepseek-v4.1-flash", 429, 0, 0, 0,
			"429 WorkBuddy: 您的使用量已超出频率限制，将在 2026-11-11 19:17:02 UTC+8 重置，您也可以切换其他模型继续使用。"),
	}
	w := FreeWindowOf(rows, "a1", t0.Add(3*time.Hour), CNFreeWindow)
	if w == nil || !w.Refused {
		t.Fatalf("want a refused window, got %+v", w)
	}
	if w.ResetsAt != "2026-11-11T11:17:02.000Z" {
		t.Fatalf("resetsAt = %q", w.ResetsAt)
	}
}

func TestFreeWindowOfRefusalEdgesTheChain(t *testing.T) {
	// A refusal's reset ends the window it refused in; requests after
	// it open a new one, and the old window's tokens are not counted.
	t0 := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	rows := []FreeRow{
		fwRow(t0.Add(1*time.Hour), "a1", "deepseek-v4.1-flash", 200, 50_000_000, 0, 0, ""),
		fwRow(t0.Add(2*time.Hour), "a1", "deepseek-v4.1-flash", 429, 0, 0, 0,
			"429 usage exceeds frequency limit, your usage will reset at 2026-11-10 12:00:00 UTC+8"),
		fwRow(t0.Add(6*time.Hour), "a1", "deepseek-v4.1-flash", 200, 7_000_000, 0, 0, ""),
	}
	// Now is after the reset but inside the new window.
	w := FreeWindowOf(rows, "a1", t0.Add(7*time.Hour), CNFreeWindow)
	if w == nil {
		t.Fatal("want a window, got nil")
	}
	if w.Amount != 7_000_000 {
		t.Fatalf("amount = %v, want 7000000 (post-reset window only)", w.Amount)
	}
	if w.Refused {
		t.Fatal("refusal is past, want an estimated window, not refused")
	}
}

func TestFreeWindowOfLapsedIsNil(t *testing.T) {
	t0 := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	rows := []FreeRow{
		fwRow(t0, "a1", "deepseek-v4.1-flash", 200, 5_000_000, 0, 0, ""),
	}
	if w := FreeWindowOf(rows, "a1", t0.Add(48*time.Hour), CNFreeWindow); w != nil {
		t.Fatalf("lapsed window should be nil, got %+v", w)
	}
}

func TestFreeWindowOfIsPerAccount(t *testing.T) {
	t0 := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	rows := []FreeRow{
		fwRow(t0.Add(1*time.Hour), "a1", "deepseek-v4.1-flash", 200, 10_000_000, 0, 0, ""),
		fwRow(t0.Add(1*time.Hour), "a2", "deepseek-v4.1-flash", 200, 90_000_000, 0, 0, ""),
		fwRow(t0.Add(2*time.Hour), "a1", "glm-5.2", 200, 10_000_000, 0, 0, ""),
	}
	w := FreeWindowOf(rows, "a1", t0.Add(3*time.Hour), CNFreeWindow)
	if w == nil {
		t.Fatal("want a window, got nil")
	}
	if w.Amount != 10_000_000 {
		t.Fatalf("amount = %v, want 10000000 (a2's rows and glm-5.2 must not count)", w.Amount)
	}
	if w := FreeWindowOf(rows, "a3", t0.Add(3*time.Hour), CNFreeWindow); w != nil {
		t.Fatalf("unknown account should be nil, got %+v", w)
	}
}

func TestFreeWindowOfAIUsesItsCap(t *testing.T) {
	t0 := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	rows := []FreeRow{
		{Provider: "workbuddy-ai", Account: "a1", Model: "deepseek-v4.1-flash", At: t0.Add(time.Hour), Status: 200, In: 50_000_000},
	}
	w := FreeWindowOf(rows, "a1", t0.Add(2*time.Hour), AIFreeWindow)
	if w == nil {
		t.Fatal("want a window, got nil")
	}
	if w.Name != "Free model tokens" || w.Limit != 1e8 || w.Used != 50 {
		t.Fatalf("name=%q limit=%v used=%v", w.Name, w.Limit, w.Used)
	}
	if w := FreeWindowOf(rows, "a1", t0.Add(2*time.Hour), CNFreeWindow); w != nil {
		t.Fatalf("AI rows must not count for the CN window, got %+v", w)
	}
}

func TestTokensShort(t *testing.T) {
	for n, want := range map[float64]string{42: "42", 812_000: "812k", 11_900_000: "11.9M", 200_000_000: "200.0M"} {
		if got := tokensShort(n); got != want {
			t.Fatalf("tokensShort(%v) = %q, want %q", n, got, want)
		}
	}
}
