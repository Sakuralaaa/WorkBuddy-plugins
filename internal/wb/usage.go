package wb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

var beijingZone = time.FixedZone("UTC+8", 8*3600)

func parseTime(s string) (int64, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0, err
	}
	return t.Unix(), nil
}

// MeterAsked is the billing API's reply to path, asked with a's sign-in.
func MeterAsked(ctx context.Context, d Doer, site *Site, a *Account, path string, body any) (*Response, error) {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/json")
	h.Set("User-Agent", "WorkBuddy/"+site.App)
	h.Set("Authorization", "Bearer "+a.Access)
	h.Set("X-User-Id", a.UID)
	h.Set("X-Domain", DomainOf(site, a))
	h.Set("X-Product", "SaaS")
	h.Set("X-IDE-Type", "WorkBuddy")
	for k, v := range EnterpriseHeaders(a) {
		h.Set(k, v)
	}
	b, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return d.Do(ctx, &Request{Method: http.MethodPost, URL: EndpointOf(site, a) + path, Header: h, Body: b})
}

// MeterError is an error the billing API answered.
type MeterError struct {
	Msg          string
	Status       int
	Code         int
	InvalidToken bool
	Unreadable   bool
}

func (e *MeterError) Error() string { return e.Msg }

// Meter asks the billing API as magpie's built-in did, its errors said the
// same: WorkBuddy's message, else its code, else the HTTP status.
func Meter(ctx context.Context, d Doer, site *Site, a *Account, path string, body any, out any) error {
	if body == nil {
		body = map[string]any{}
	}
	res, err := MeterAsked(ctx, d, site, a, path, body)
	if err != nil {
		return err
	}
	var env envelope
	parsed := json.Valid(res.Body)
	if parsed {
		_ = json.Unmarshal(res.Body, &env)
	}
	ok := res.Status >= 200 && res.Status < 300
	if ok && !parsed {
		un := strings.TrimSpace(string(res.Body))
		if len(un) > 120 {
			un = un[:120]
		}
		if un == "" {
			un = "(empty)"
		}
		return &MeterError{Msg: "unreadable reply " + un, Status: res.Status, Unreadable: true}
	}
	code := codeOf(env.Code)
	e := &MeterError{Status: res.Status, Code: code}
	if !ok {
		e.InvalidToken = invalidToken(res.Status, res.Header)
		switch {
		case code != 0 && env.Msg != "":
			e.Msg = env.Msg
		case code != 0:
			e.Msg = fmt.Sprintf("error %d", code)
		case env.Msg != "":
			e.Msg = env.Msg
		default:
			e.Msg = StatusText(res.Status)
		}
		return e
	}
	if code != 0 {
		e.Msg = env.Msg
		if e.Msg == "" {
			e.Msg = fmt.Sprintf("error %d", code)
		}
		return e
	}
	if out != nil && len(env.Data) > 0 {
		return json.Unmarshal(env.Data, out)
	}
	return nil
}

// ---- credits ------------------------------------------------------------------

// Window is a usage window as magpie's card shows one.
type Window struct {
	Name      string   `json:"name"`
	Used      float64  `json:"used"` // percent
	Display   string   `json:"display"`
	Amount    float64  `json:"amount"`
	Limit     float64  `json:"limit"`
	Balance   *float64 `json:"balance,omitempty"`
	Unit      string   `json:"unit"`
	ResetsAt  string   `json:"resetsAt,omitempty"`
	Span      float64  `json:"span,omitempty"`
	RunsOutAt string   `json:"runsOutAt,omitempty"`
	RunsOut   float64  `json:"runsOut,omitempty"`
	Aside     bool     `json:"aside,omitempty"`
	// Refused is set when a refusal still in force names the window's
	// end: the vendor's answer, exact, where the ledger's sum is only an
	// estimate (freewindow.go).
	Refused   bool     `json:"refused,omitempty"`
	NotModels []string `json:"notModels,omitempty"`
	Models    []string `json:"models,omitempty"`
}

// Usage is an account's credits.
type Usage struct {
	Plan    string    `json:"plan,omitempty"`
	Windows []*Window `json:"windows"`
	Error   string    `json:"error,omitempty"`
	SignIn  SignIn    `json:"signIn,omitempty"`
}

// capacity reads a capacity the billing API sends as a string or number.
func capacity(v any) (float64, error) {
	switch t := v.(type) {
	case nil:
		return 0, nil
	case float64:
		return t, nil
	case json.Number:
		return t.Float64()
	case string:
		if t == "" {
			return 0, nil
		}
		n, ok := jsNumber(t)
		if strings.TrimSpace(t) != t || !ok {
			return 0, fmt.Errorf("strconv.ParseFloat: parsing %q: invalid syntax", t)
		}
		return n, nil
	}
	return 0, nil
}

func capOf(v any) float64 {
	n, _ := capacity(v)
	return n
}

// Compact is a count as magpie says one: whole, else to two places.
func Compact(n float64) string {
	if n == math.Trunc(n) {
		return strconv.FormatFloat(n, 'f', -1, 64)
	}
	s := strconv.FormatFloat(n, 'f', 2, 64)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

func code(n, s string) string { return "TCACA_code_" + n + "_" + s }

// Package codes, as WorkBuddy's own client lists them.
var (
	PaidCodes = []string{code("002", "AkiJS3ZHF5"), code("005", "maRGyrHhw1"), code("003", "FAnt7lcmRT"), code("023", "4xbGhMrE6q"),
		code("026", "BaESVICNoi"), code("027", "0FCGVA6vSa"), code("043", "6oIkxT7Z0S"), code("009", "0XmEQc2xOf"), code("038", "OhvqZtiPKr"), code("036", "lupO5WgNdG")}
	DailyCode = code("001", "PqouKr6QWV")
	FreeCodes = []string{DailyCode, code("008", "cfWoLwvjU4"), code("035", "ArVxJcGDsm"), code("006", "DbXS0lrypC"), code("039", "KRcQj7wUat"),
		code("040", "mi9rCYg46x"), code("007", "nzdH5h4Nl0"), code("028", "NtpWi0jzXs"), code("037", "WxOD3MpI2o"), code("029", "6wCGEWquYy"),
		code("030", "BjSt89qTvr")}
	PlanCodes = []string{code("002", "AkiJS3ZHF5"), code("005", "maRGyrHhw1"), code("003", "FAnt7lcmRT"), code("023", "4xbGhMrE6q"),
		code("026", "BaESVICNoi"), code("027", "0FCGVA6vSa"), code("043", "6oIkxT7Z0S"), code("035", "ArVxJcGDsm"),
		code("039", "KRcQj7wUat"), code("040", "mi9rCYg46x")}
	FreeMon    = code("008", "cfWoLwvjU4")
	ExtraCodes = []string{code("028", "NtpWi0jzXs"), code("037", "WxOD3MpI2o"), code("009", "0XmEQc2xOf"), code("038", "OhvqZtiPKr"),
		code("036", "lupO5WgNdG"), code("007", "nzdH5h4Nl0"), code("029", "6wCGEWquYy"), code("030", "BjSt89qTvr")}
	ExtraAI = []string{FreeMon, code("006", "DbXS0lrypC")}
)

const slice = "4"

// Summary is /billing/meter/get-user-resource-summary's data.
type Summary struct {
	IsPaidUser              bool             `json:"IsPaidUser"`
	SubscriptionPackageCode string           `json:"SubscriptionPackageCode"`
	CreditGroups            []map[string]any `json:"CreditGroups"`
	Packages                []map[string]any `json:"Packages"`
}

func present(p map[string]any, k string) bool {
	v, ok := p[k]
	return ok && v != nil && v != ""
}

// PlanCodesOf says whether a summary's package is the plan's own.
func PlanCodesOf(sum *Summary, site *Site) func(string) bool {
	sub := ""
	if sum != nil {
		sub = sum.SubscriptionPackageCode
	}
	return func(c string) bool {
		return c != "" && (c == sub || containsStr(PlanCodes, c) || (site != nil && !site.AI && c == FreeMon))
	}
}

type kinds struct {
	plan, extra struct{ total, used float64 }
	left        float64
	bags        int
}

func kindsOf(sum *Summary, site *Site) kinds {
	var k kinds
	if sum == nil {
		return k
	}
	ofPlan := PlanCodesOf(sum, site)
	hasTotal := func(p map[string]any) bool { return present(p, "CycleTotalCapacity") }
	var groups []map[string]any
	for _, g := range sum.CreditGroups {
		if hasTotal(g) {
			groups = append(groups, g)
		}
	}
	for _, g := range groups {
		if g["Kind"] == "plan_base" {
			k.plan.total += capOf(g["CycleTotalCapacity"])
			k.plan.used += capOf(g["CycleUsedCapacity"])
		} else {
			k.extra.total += capOf(g["CycleTotalCapacity"])
			k.extra.used += capOf(g["CycleUsedCapacity"])
		}
	}
	for _, p := range sum.Packages {
		if hasTotal(p) {
			if len(groups) == 0 {
				c, _ := p["PackageCode"].(string)
				if ofPlan(c) {
					k.plan.total += capOf(p["CycleTotalCapacity"])
					k.plan.used += capOf(p["CycleUsedCapacity"])
				} else {
					k.extra.total += capOf(p["CycleTotalCapacity"])
					k.extra.used += capOf(p["CycleUsedCapacity"])
				}
			}
		} else if present(p, "CycleRemainCapacity") {
			k.left += capOf(p["CycleRemainCapacity"])
			k.bags++
		}
	}
	return k
}

func creditWindow(name string, used, total float64) *Window {
	return &Window{Name: name, Used: 100 * used / total, Display: Compact(used) + " / " + Compact(total), Amount: used, Limit: total, Unit: "credits"}
}

// TotalOf is the plan's and the extra credits as the window that stops the
// account, the two set aside.
func TotalOf(plan, extra *Window) *Window {
	plan.Aside, extra.Aside = true, true
	w := creditWindow("Total credits", plan.Amount+extra.Amount, plan.Limit+extra.Limit)
	w.ResetsAt, w.Span = plan.ResetsAt, plan.Span
	at := func(x *Window) int64 {
		if x.RunsOutAt == "" {
			return 0
		}
		t, err := time.Parse(time.RFC3339Nano, x.RunsOutAt)
		if err != nil {
			return 0
		}
		return t.UnixMilli()
	}
	a, b := at(plan), at(extra)
	var soon *Window
	if a > 0 && (!(b > 0) || a <= b) {
		soon = plan
	} else if b > 0 {
		soon = extra
	}
	if soon != nil {
		w.RunsOutAt = soon.RunsOutAt
		w.RunsOut = soon.RunsOut
		if a > 0 && a == b {
			w.RunsOut += extra.RunsOut
		}
	}
	return w
}

// UsageOf is the summary's credits as windows.
func UsageOf(sum *Summary, plan string, site *Site) *Usage {
	out := &Usage{Plan: plan}
	if out.Plan == "" {
		if sum != nil && sum.IsPaidUser {
			out.Plan = "Pro"
		} else {
			out.Plan = "Free plan"
		}
	}
	k := kindsOf(sum, site)
	if k.plan.total > 0 && k.extra.total > 0 {
		credits := creditWindow("Credits", k.plan.used, k.plan.total)
		more := creditWindow("Extra credits", k.extra.used, k.extra.total)
		out.Windows = append(out.Windows, credits, more, TotalOf(credits, more))
	} else {
		total, used := k.plan.total+k.extra.total, k.plan.used+k.extra.used
		if total > 0 {
			out.Windows = append(out.Windows, creditWindow("Credits", used, total))
		}
	}
	total := k.plan.total + k.extra.total
	if k.bags > 0 && (total == 0 || k.left > 0) {
		left := k.left
		out.Windows = append(out.Windows, &Window{Name: "Prepaid credits", Balance: &left, Display: Compact(left) + " credits", Unit: "credits"})
	}
	return out
}

func beijingMS(s string) int64 {
	s = strings.TrimSpace(s)
	if len(s) != 19 {
		return 0
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", s, beijingZone)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

func isoOf(ms int64) string { return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z") }

// EndOf is when a package's credits are lost.
func EndOf(p map[string]any) int64 {
	if ms, ok := numberOf(p["DeductionEndTime"]); ok && ms > 0 {
		return int64(ms)
	}
	for _, k := range []string{"ExpiredTime", "CycleEndTime"} {
		v, ok := p[k].(string)
		if !ok || strings.TrimSpace(v) == "" {
			continue
		}
		if t := beijingMS(v); t > 0 {
			return t
		}
		if t, ok := jsDateParse(strings.TrimSpace(v)); ok && t > 0 {
			return t
		}
	}
	return 0
}

func numberOf(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case string:
		f, ok := jsNumber(strings.TrimSpace(t))
		return f, ok
	}
	return 0, false
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// ListedExtra is the extra credits of the detail lists the summary doesn't
// list; nil when there are none.
func ListedExtra(sum *Summary, packages []map[string]any, site *Site, now int64) *Window {
	summed := map[string]bool{}
	if sum != nil {
		for _, p := range sum.Packages {
			summed[str(p["PackageCode"])] = true
		}
	}
	codes := ExtraCodes
	if site != nil && site.AI {
		codes = append(append([]string{}, ExtraCodes...), ExtraAI...)
	}
	var total, left float64
	var at int64
	var lapsing float64
	of := func(v any) float64 {
		n, ok := numberOf(v)
		if ok && !math.IsInf(n, 0) && n > 0 {
			return n
		}
		return 0
	}
	for _, p := range packages {
		c := str(p["PackageCode"])
		if !containsStr(codes, c) || summed[c] {
			continue
		}
		end := EndOf(p)
		if end > 0 && end <= now {
			continue
		}
		var sl map[string]any
		if fmt.Sprint(p["CapacityType"]) == slice {
			if arr, ok := p["SlicePeriodUsageDetails"].([]any); ok && len(arr) > 0 {
				sl, _ = arr[0].(map[string]any)
			}
		}
		var t, l float64
		if sl != nil {
			t = of(sl["SlicePeriodCapacitySizePrecise"])
			l = math.Min(t, of(sl["SlicePeriodCapacityRemainPrecise"]))
		} else {
			t = of(p["CycleCapacitySizePrecise"])
			l = math.Min(t, of(p["CycleCapacityRemainPrecise"]))
		}
		if !(t > 0) {
			continue
		}
		total += t
		left += l
		if l > 0 && end > 0 && sl == nil {
			if at == 0 || end < at {
				at, lapsing = end, l
			} else if end == at {
				lapsing += l
			}
		}
	}
	if !(total > 0) {
		return nil
	}
	w := creditWindow("Extra credits", total-left, total)
	if at > 0 {
		w.RunsOutAt, w.RunsOut = isoOf(at), lapsing
	}
	return w
}

// RunsOutOf is the soonest moment credits left in a counted package are
// lost, and how many.
func RunsOutOf(sum *Summary, packages []map[string]any, now int64, only func(string) bool, site *Site) (string, float64, bool) {
	counted := map[string]bool{}
	if sum != nil {
		for _, p := range sum.Packages {
			counted[str(p["PackageCode"])] = true
		}
	}
	if site != nil {
		codes := ExtraCodes
		if site.AI {
			codes = append(append([]string{}, ExtraCodes...), ExtraAI...)
		}
		for _, c := range codes {
			counted[c] = true
		}
	}
	var at int64
	var amount float64
	for _, p := range packages {
		c := str(p["PackageCode"])
		if !counted[c] || !only(c) || c == DailyCode || fmt.Sprint(p["CapacityType"]) == slice {
			continue
		}
		v := p["CycleCapacityRemainPrecise"]
		if v == nil {
			v = p["CycleCapacityRemain"]
		}
		left := capOf(v)
		end := EndOf(p)
		if !(left > 0) || end <= now {
			continue
		}
		if at == 0 || end < at {
			at, amount = end, left
		} else if end == at {
			amount += left
		}
	}
	if at == 0 {
		return "", 0, false
	}
	return isoOf(at), amount, true
}

// CycleOf is when the plan's credits start again, and the cycle's length.
func CycleOf(sum *Summary, packages []map[string]any, now int64) (string, float64, bool) {
	of := func(p map[string]any) (string, float64, bool) {
		end, start := beijingMS(str(p["CycleEndTime"])), beijingMS(str(p["CycleStartTime"]))
		if !(end > now) {
			return "", 0, false
		}
		span := 0.0
		if start > 0 && start < end {
			span = float64(end-start) / 1000
		}
		return isoOf(end), span, true
	}
	if sum != nil && sum.SubscriptionPackageCode != "" {
		for _, p := range packages {
			if str(p["PackageCode"]) == sum.SubscriptionPackageCode {
				return of(p)
			}
		}
		return "", 0, false
	}
	for _, p := range packages {
		if fmt.Sprint(p["CapacityType"]) == slice {
			return of(p)
		}
	}
	return "", 0, false
}

// EnterpriseUsageOf is an enterprise account's credits; nil when it grants
// none.
func EnterpriseUsageOf(u map[string]any) *Window {
	limit := capOf(u["limitNum"])
	if !(limit > 0) {
		return nil
	}
	used := capOf(u["credit"])
	w := creditWindow("Credits", used, limit)
	reset, start := beijingMS(str(u["cycleResetTime"])), beijingMS(str(u["cycleStartTime"]))
	if reset > 0 {
		w.ResetsAt = isoOf(reset)
	}
	if reset > start && start > 0 {
		w.Span = float64(reset-start) / 1000
	}
	return w
}

// PackagesOf is every package of the account with credits, paid and free.
func PackagesOf(ctx context.Context, d Doer, site *Site, a *Account, now int64) ([]map[string]any, error) {
	day := time.UnixMilli(now).In(beijingZone).Format("2006-01-02")
	lists := []struct {
		path  string
		extra map[string]any
	}{
		{"/billing/meter/get-user-resource-paid-packages", map[string]any{"PackageCodes": PaidCodes, "NeedRenewInfo": true}},
		{"/billing/meter/get-user-resource-free-packages", map[string]any{"PackageCodes": FreeCodes, "SlicePeriodStartTime": day + " 00:00:00", "SlicePeriodEndTime": day + " 23:59:59"}},
	}
	// The paid and free lists are asked at once; each is paged on its own.
	got := make([][]map[string]any, len(lists))
	errs := make([]error, len(lists))
	var wg sync.WaitGroup
	for i, l := range lists {
		wg.Add(1)
		go func(i int, path string, extra map[string]any) {
			defer wg.Done()
			for page := 1; page <= 10; page++ {
				body := map[string]any{"PageNumber": page, "PageSize": 200, "Status": []int{0, 3}}
				for k, v := range extra {
					body[k] = v
				}
				var res struct {
					Accounts   []map[string]any `json:"Accounts"`
					TotalCount float64          `json:"TotalCount"`
				}
				if err := Meter(ctx, d, site, a, path, body, &res); err != nil {
					errs[i] = err
					return
				}
				got[i] = append(got[i], res.Accounts...)
				if len(res.Accounts) < 200 || float64(len(got[i])) >= res.TotalCount {
					return
				}
			}
		}(i, l.path, l.extra)
	}
	wg.Wait()
	var out []map[string]any
	for i := range lists {
		if errs[i] != nil {
			return nil, errs[i]
		}
		out = append(out, got[i]...)
	}
	return out, nil
}

// Credits reads an account's credits as magpie's usage hook does.
func (c *Channel) Credits(ctx context.Context, d Doer, authID string, a *Account) *Usage {
	site := c.Site()
	if a == nil {
		return &Usage{Error: "not signed in", SignIn: SignInKept}
	}
	if a.Source == "key" {
		return &Usage{Error: fmt.Sprintf("credits unknown with an API key (WorkBuddy's own client reads none with one); see %s", strings.TrimSuffix(site.Keys, "/profile/keys")), SignIn: SignInKept}
	}
	cur, err := Fresh(ctx, d, site, a, func(n *Account) {
		if c.Save != nil {
			c.Save(authID, n)
		}
	})
	if err != nil {
		return &Usage{Error: err.Error(), SignIn: SignInKept}
	}
	a = cur
	const summaryPath = "/billing/meter/get-user-resource-summary"
	said := SignInKept
	var sum Summary
	if err := Meter(ctx, d, site, a, summaryPath, nil, &sum); err != nil {
		var me *MeterError
		if !errors.As(err, &me) || me.Status != 401 {
			return &Usage{Error: err.Error(), SignIn: SignInKept}
		}
		r := c.reissued(ctx, d, authID, a, a.Access)
		if r.a == nil {
			u := &Usage{Error: refusedText(site, r.why, ""), SignIn: SignInKept}
			if gone(r, me.InvalidToken) {
				u.SignIn = SignInExpired
			}
			return u
		}
		a = r.a
		sum = Summary{}
		if err := Meter(ctx, d, site, a, summaryPath, nil, &sum); err != nil {
			if errors.As(err, &me) && me.Status == 401 {
				u := &Usage{Error: refusedText(site, "it refused the token renewed for it too", ""), SignIn: SignInKept}
				if me.InvalidToken {
					u.SignIn = SignInExpired
				}
				return u
			}
			return &Usage{Error: err.Error(), SignIn: SignInKept}
		}
		said = SignInRenewed
	}
	out := UsageOf(&sum, a.Plan, site)
	if len(out.Windows) == 0 {
		var eu map[string]any
		if Meter(ctx, d, site, a, "/billing/meter/get-enterprise-user-usage", nil, &eu) == nil {
			if w := EnterpriseUsageOf(eu); w != nil {
				plan := a.Plan
				if plan == "" {
					plan = "Enterprise"
				}
				return &Usage{Plan: plan, Windows: c.spareFree(ctx, d, a, []*Window{w}), SignIn: said}
			}
		}
	}
	var credits, extra *Window
	for _, w := range out.Windows {
		switch w.Name {
		case "Credits":
			credits = w
		case "Extra credits":
			extra = w
		}
	}
	if credits != nil {
		now := time.Now().UnixMilli()
		if packages, err := PackagesOf(ctx, d, site, a, now); err == nil {
			if at, span, ok := CycleOf(&sum, packages, now); ok {
				credits.ResetsAt, credits.Span = at, span
			}
			ofPlan := PlanCodesOf(&sum, site)
			if extra != nil {
				if at, n, ok := RunsOutOf(&sum, packages, now, ofPlan, nil); ok {
					credits.RunsOutAt, credits.RunsOut = at, n
				}
				if at, n, ok := RunsOutOf(&sum, packages, now, func(c string) bool { return !ofPlan(c) }, site); ok {
					extra.RunsOutAt, extra.RunsOut = at, n
				}
				for i, w := range out.Windows {
					if w.Name == "Total credits" {
						out.Windows[i] = TotalOf(credits, extra)
					}
				}
			} else {
				k := kindsOf(&sum, site)
				var more *Window
				if k.plan.total > 0 && k.extra.total == 0 {
					more = ListedExtra(&sum, packages, site, now)
				}
				var lapseSite *Site
				if more == nil {
					lapseSite = site
				}
				if at, n, ok := RunsOutOf(&sum, packages, now, func(string) bool { return true }, lapseSite); ok {
					credits.RunsOutAt, credits.RunsOut = at, n
				}
				if more != nil {
					idx := 0
					for i, w := range out.Windows {
						if w == credits {
							idx = i
						}
					}
					rest := append([]*Window{more, TotalOf(credits, more)}, out.Windows[idx+1:]...)
					out.Windows = append(out.Windows[:idx+1:idx+1], rest...)
				}
			}
		}
	}
	out.Windows = c.spareFree(ctx, d, a, out.Windows)
	free := c.freeWindow(authID)
	out.Windows = append(out.Windows, free...)
	// A live refusal is already the free window's own card (Used 100,
	// refused until the vendor's reset); the in-memory refusal window
	// for the same model would only duplicate it.
	for _, w := range c.refusalWindows(authID) {
		covered := false
		for _, f := range free {
			for _, m := range f.Models {
				for _, n := range w.Models {
					if m == n {
						covered = true
					}
				}
			}
		}
		if !covered {
			out.Windows = append(out.Windows, w)
		}
	}
	out.SignIn = said
	return out
}

// freeWindow is the account's free-token window from the rows the host
// recorded for the window's model (nil when the host keeps none, or no
// row of this account's is in a live window).
func (c *Channel) freeWindow(authID string) []*Window {
	if c.FreeRows == nil {
		return nil
	}
	site := c.Site()
	win := FreeWindowOf(c.FreeRows(authID, site.AI), authID, time.Now(), FreeWindowSpecFor(site.AI))
	if win == nil {
		return nil
	}
	return []*Window{win}
}

// spareFree has the credits windows not count the models that cost none.
func (c *Channel) spareFree(ctx context.Context, d Doer, a *Account, windows []*Window) []*Window {
	if len(windows) == 0 {
		return windows
	}
	list, err := LiveModels(ctx, d, c.Site(), a)
	if err != nil {
		return windows
	}
	var free []string
	for _, m := range list {
		if m.Free {
			free = append(free, m.ID)
		}
	}
	if len(free) > 0 {
		for _, w := range windows {
			w.NotModels = free
		}
	}
	return windows
}

// ---- a model's own window, as a refusal names it ----------------------------------

// The window WorkBuddy rate limits deepseek-v4.1-flash (and its -sg id) by
// is shown by no meter: a 429 names its reset ("your usage will reset at …
// UTC+8", "将在 … UTC+8 重置"). magpie reads it off its request ledger; CPA
// keeps no such ledger a plugin can read, so this port reads it off the
// refusals its own executor met, which is the one exact signal there is
// (magpie: "a refusal wins").
type refusal struct {
	until  time.Time
	models []string
}

var windowModels = map[string][]string{
	"deepseek-v4.1-flash":    {"deepseek-v4.1-flash", "deepseek-v4.1-flash-sg"},
	"deepseek-v4.1-flash-sg": {"deepseek-v4.1-flash", "deepseek-v4.1-flash-sg"},
}

// NoteRefusal keeps a 429's named reset for an auth and model.
func (c *Channel) NoteRefusal(authID, model string, until int64) {
	if until <= 0 {
		return
	}
	ms := windowModels[model]
	if ms == nil {
		ms = []string{model}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.refusals == nil {
		c.refusals = map[string]refusal{}
	}
	key := authID + "\n" + ms[0]
	c.refusals[key] = refusal{until: time.Unix(until, 0), models: ms}
}

func (c *Channel) refusalWindows(authID string) []*Window {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*Window
	now := time.Now()
	for k, r := range c.refusals {
		if !strings.HasPrefix(k, authID+"\n") {
			continue
		}
		if !r.until.After(now) {
			delete(c.refusals, k)
			continue
		}
		out = append(out, &Window{
			Name: r.models[0] + " window", Used: 100, Display: "refused until reset",
			Unit: "tokens", ResetsAt: r.until.UTC().Format("2006-01-02T15:04:05.000Z"),
			Span: 86400, Models: r.models,
		})
	}
	return out
}
