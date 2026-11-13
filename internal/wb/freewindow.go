package wb

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// The free-token window, ported from magpie-community/plugins'
// packages/workbuddy (freeWindowOf, windowReset, pinned, freeWindowRows).
//
// A free account's usage() reports only the credits window, so a plan with
// 160 credits untouched reads 0% while chats to deepseek-v4.1-flash are
// already refused: the model has a token allowance of its own, counted
// apart from credits, and nothing the vendor answers with reports it. The
// refusal is the only place it appears, and only as a time:
//
//	429 usage exceeds frequency limit, but don't worry, your usage will
//	reset at 2026-10-20 19:47:41 UTC+8, alternatively, you can switch to
//	the other models to continue using it.
//
// The China build says the same in its own words:
//
//	429 WorkBuddy: 您的使用量已超出频率限制，将在 2026-11-02 19:17:02
//	UTC+8 重置，您也可以切换其他模型继续使用。
//
// The window is a chain: it begins at the first request answered after the
// previous one ended, so where this window began depends on every window
// before it. A window ends at a refusal's reset when the ledger holds one,
// and otherwise a day after it began.
//
// Two differences from magpie's port, both forced by the host:
//   - magpie reads its own usage ledger (usage.jsonl) and routing traces;
//     CPA keeps no such ledger a plugin can read, so this port reads the
//     rows the plugin itself records for every request it relays
//     (plugin/freewin.go). Rows are per-request, as magpie's are; the
//     plugin's hourly usage buckets are too coarse for the chain.
//   - magpie's rows come from the gateway, where one account's requests
//     may arrive on either build; here rows are keyed by CPA auth id, so
//     one auth id's rows are its own on both builds.

// FreeWindowSpec is one build's free-token window.
type FreeWindowSpec struct {
	Provider string
	Model    string
	Aliases  []string
	Cap      float64
	Name     string
	Short    string
	Unit     string
}

// CNFreeWindow is the China build's window: deepseek-v4.1-flash at about
// 2e8 tokens (magpie's CN_ESTIMATED_WINDOWS, measured 2026-11-02). The cap
// is an estimate — the vendor never reports it — so the card is marked
// aside (see FreeWindowOf).
var CNFreeWindow = FreeWindowSpec{
	Provider: "workbuddy", Model: "deepseek-v4.1-flash",
	Aliases: []string{"deepseek-v4.1-flash-sg"},
	Cap:     2e8, Name: "Free model tokens", Short: "~200M", Unit: "tokens",
}

// AIFreeWindow is WorkBuddy AI's window: the same model at 1e8 tokens,
// served at x0.00, so "Free model tokens" is what it is.
var AIFreeWindow = FreeWindowSpec{
	Provider: "workbuddy-ai", Model: "deepseek-v4.1-flash",
	Aliases: []string{"deepseek-v4.1-flash-sg"},
	Cap:     1e8, Name: "Free model tokens", Short: "100M", Unit: "tokens",
}

// FreeWindowSpecFor is the window spec of a build.
func FreeWindowSpecFor(ai bool) FreeWindowSpec {
	if ai {
		return AIFreeWindow
	}
	return CNFreeWindow
}

const (
	// FreeWindowSpan is a window's length: 24h.
	FreeWindowSpan = 24 * time.Hour
	// FreeWindowGap is the gap between two of an account's answered rows
	// that ends every window before it.
	FreeWindowGap = FreeWindowSpan
)

// FreeRow is one relayed request of the window's model, in the shape
// magpie's usage rows have. In is the prompt tokens outside the cache
// (prompt minus cache_read), so counted tokens are In + CacheRead + Out
// with nothing double-counted. Err is a 429's message (the reset is read
// off it); RestUntil is a reset instant handed over as one, when the
// message names none.
type FreeRow struct {
	Provider  string    `json:"provider"`
	Account   string    `json:"account"`
	Model     string    `json:"model"`
	At        time.Time `json:"at"`
	Status    int       `json:"status"`
	In        int64     `json:"in,omitempty"`
	CacheRead int64     `json:"cache_read,omitempty"`
	Out       int64     `json:"out,omitempty"`
	Err       string    `json:"err,omitempty"`
	RestUntil int64     `json:"rest_until,omitempty"`
}

// windowModel says model is the window's model or one of its aliases.
func windowModel(model string, win FreeWindowSpec) bool {
	if model == "" {
		return false
	}
	if strings.HasPrefix(model, win.Model) {
		return true
	}
	for _, alias := range win.Aliases {
		if strings.HasPrefix(model, alias) {
			return true
		}
	}
	return false
}

// WindowModelOf says model is any build's window model.
func WindowModelOf(model string) bool {
	return windowModel(model, CNFreeWindow) || windowModel(model, AIFreeWindow)
}

// resetOfRow is the vendor reset a row names, whether its words spell it
// out (ResetOf, which reads both builds' refusal texts) or it was handed
// over as the instant itself.
func resetOfRow(r FreeRow) int64 {
	if t := ResetOf(r.Err); t > 0 {
		return t
	}
	return r.RestUntil
}

// tokensShort says a token count small: 11.9M, 812k.
func tokensShort(n float64) string {
	if n >= 1e6 {
		return fmt.Sprintf("%.1fM", n/1e6)
	}
	if n >= 1e3 {
		return fmt.Sprintf("%dk", int64(n/1e3))
	}
	return fmt.Sprintf("%d", int64(n))
}

// FreeWindowOf is the window account is in now, from rows, or nil when it
// has lapsed — the next request opens a new one. now is passed in so the
// reading is testable; win is the build's window (which build's rows count,
// what cap they fill).
func FreeWindowOf(rows []FreeRow, account string, now time.Time, win FreeWindowSpec) *Window {
	var mine []FreeRow
	for _, r := range rows {
		if r.Provider != win.Provider || r.Account != account || !windowModel(r.Model, win) || r.At.IsZero() {
			continue
		}
		mine = append(mine, r)
	}
	if len(mine) == 0 {
		return nil
	}
	counted := func(r FreeRow) float64 { return float64(r.In + r.CacheRead + r.Out) }
	apart := func(a, b time.Time) bool { return !a.Before(b.Add(FreeWindowSpan)) }

	// A refusal names the end of the window it refused in.
	var edges []int64
	for _, r := range mine {
		if r.Status != 429 {
			continue
		}
		if t := resetOfRow(r); t > 0 {
			edges = append(edges, t)
		}
	}
	sort.Slice(edges, func(i, j int) bool { return edges[i] < edges[j] })

	var answered []FreeRow
	for _, r := range mine {
		if r.Status == 200 {
			answered = append(answered, r)
		}
	}
	sort.Slice(answered, func(i, j int) bool { return answered[i].At.Before(answered[j].At) })
	if len(answered) == 0 {
		return nil
	}

	head := func(from int) (start time.Time, used float64, ok bool) {
		var edge int
		for i := from; i < len(answered); i++ {
			r := answered[i]
			t := r.At
			// Every refusal this row's window ended at closes it.
			for edge < len(edges) && edges[edge] <= t.Unix() {
				start = time.Time{}
				used = 0
				edge++
			}
			if start.IsZero() {
				start = t
			} else if apart(t, start) {
				start = t
				used = 0
			}
			used += counted(r)
		}
		if start.IsZero() {
			return time.Time{}, 0, false
		}
		return start, used, true
	}

	// The rows may begin before the window now: the run of rows that opens
	// a window can start a day after the first row kept. Read from the
	// first row kept and the chain begins shifted, a day early. So the
	// chain is read from each row that has no answered row of this
	// account's within a day before it.
	var wStart time.Time
	var wUsed float64
	found := false
	for i := range answered {
		if i > 0 && !apart(answered[i].At, answered[i-1].At) {
			continue
		}
		h, used, ok := head(i)
		if !ok {
			continue
		}
		if !h.Add(FreeWindowSpan).After(now) {
			continue // lapsed before now
		}
		wStart, wUsed, found = h, used, true
		break
	}
	if !found {
		return nil
	}
	end := wStart.Add(FreeWindowSpan)
	if !now.Before(end) {
		return nil // lapsed: nothing is exhausted, a new window opens on the next request
	}

	card := func(amount float64, resetsAt time.Time, refused bool) *Window {
		used := 100 * amount / win.Cap
		if used > 100 {
			used = 100
		}
		w := &Window{
			Name:     win.Name,
			Used:     used,
			Amount:   amount,
			Limit:    win.Cap,
			Unit:     win.Unit,
			Display:  tokensShort(amount) + " / " + win.Short,
			ResetsAt: resetsAt.UTC().Format("2006-01-02T15:04:05.000Z"),
			Span:     float64(FreeWindowSpan / time.Second),
			Models:   append([]string{win.Model}, win.Aliases...),
		}
		if refused {
			w.Refused = true
		} else {
			// The ledger's sum is an estimate, shown and set aside; the
			// vendor's refusal is the one that stops the model.
			w.Aside = true
		}
		return w
	}

	// A refusal still in force is exact: the account is spending this
	// window from elsewhere too, so the sum undercounts it, and the
	// refusal is the answer the vendor would give again. Its reset is
	// the window's end, named by the vendor rather than derived from start.
	var live int64
	for _, r := range mine {
		if r.Status != 429 {
			continue
		}
		t := resetOfRow(r)
		if t <= 0 || !time.Unix(t, 0).After(now) || !time.Unix(t, 0).After(wStart) {
			continue
		}
		if live == 0 || t > live {
			live = t
		}
	}
	if live > 0 {
		return card(win.Cap, time.Unix(live, 0), true)
	}
	return card(wUsed, end, false)
}
