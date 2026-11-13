package plugin

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"github.com/Sakuralaaa/WorkBuddy-plugins/internal/jobs"
	"github.com/Sakuralaaa/WorkBuddy-plugins/internal/wb"
)

//go:embed web/index.html
var pageHTML []byte

// Management API routes: everything here is behind CPA's management key
// (the page reads the management center's own key). CPA's built-in
// endpoints do the rest — sign-in (/<provider>-auth-url, get-auth-status),
// credential files (auth-files list/upload/delete/status/refresh) and the
// plugin config (plugins/<id>/config) — and the page calls them directly.
const base = ID + "/"

type route struct {
	Method, Path, Description string
}

var routes = []route{
	{"GET", base + "status", "WorkBuddy: scheduler, running jobs, last results and run history"},
	{"POST", base + "run", "WorkBuddy: run jobs now ({account, job, task}; empty account = every due job)"},
	{"GET", base + "accounts", "WorkBuddy: sign-in details of every WorkBuddy credential"},
	{"GET", base + "credits", "WorkBuddy: credits windows of every account (?account=&refresh=1)"},
	{"GET", base + "credits/packages", "WorkBuddy: an account's credit packages (?account=&refresh=1)"},
	{"GET", base + "models", "WorkBuddy: live model list of a build (?build=cn|ai&account=)"},
	{"GET", base + "tasks", "WorkBuddy: an account's task center (?account=)"},
	{"POST", base + "keys/check", "WorkBuddy: check an API key with WorkBuddy before it is saved"},
	{"GET", base + "credits/history", "WorkBuddy: credits left over time (?hours=&account=)"},
	{"GET", base + "requests", "WorkBuddy: the last requests relayed, numbers only (?limit=&failed=1)"},
	{"GET", base + "usage", "WorkBuddy: requests, tokens and credits relayed (?hours=&account=)"},
	{"POST", base + "usage/clear", "WorkBuddy: clear the usage ledger"},
	{"GET", base + "credits/composition", "WorkBuddy: every account's credit packages, by expiry day (?refresh=1)"},
	{"POST", base + "history/clear", "WorkBuddy: clear the run history"},
}

func managementRoutes() any {
	var rs []map[string]string
	for _, r := range routes {
		rs = append(rs, map[string]string{"method": r.Method, "path": r.Path, "description": r.Description})
	}
	return map[string]any{
		"routes": rs,
		"resources": []map[string]string{{
			"Path": "/index.html", "Menu": "WorkBuddy",
			"Description": "WorkBuddy 账号、积分、模型与任务中心",
		}},
	}
}

type mgmtRequest struct {
	pluginapi.ManagementRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

func reply(status int, v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return map[string]any{"StatusCode": status, "Headers": map[string][]string{"Content-Type": {"application/json; charset=utf-8"}, "Cache-Control": {"no-store"}}, "Body": b}, nil
}

func replyErr(status int, msg string) (any, error) {
	return reply(status, map[string]string{"error": msg})
}

func managementHandle(raw []byte) (any, error) {
	var r mgmtRequest
	if err := json.Unmarshal(raw, &r); err != nil {
		return replyErr(400, err.Error())
	}
	path := r.Path
	if i := strings.Index(path, "/"+ID+"/"); i >= 0 {
		path = path[i+1:]
	}
	if strings.HasPrefix(r.Path, "/v0/resource/") || strings.HasSuffix(r.Path, "/index.html") {
		return map[string]any{"StatusCode": 200, "Headers": map[string][]string{
			"Content-Type":           {"text/html; charset=utf-8"},
			"Cache-Control":          {"no-cache"},
			"X-Content-Type-Options": {"nosniff"},
		}, "Body": pageHTML}, nil
	}
	q := url.Values(r.Query)
	d := HostDoer{CallbackID: r.HostCallbackID}
	method := strings.ToUpper(r.Method)
	switch {
	case method == "GET" && path == base+"status":
		return reply(200, statusBody(q))
	case method == "POST" && path == base+"run":
		return handleRun(r.Body)
	case method == "GET" && path == base+"accounts":
		return handleAccounts()
	case method == "GET" && path == base+"credits":
		return handleCredits(d, q.Get("account"), q.Get("refresh") != "")
	case method == "GET" && path == base+"credits/history":
		h, _ := strconv.Atoi(q.Get("hours"))
		return handleCreditHistory(h, q.Get("account"))
	case method == "GET" && path == base+"requests":
		n, _ := strconv.Atoi(q.Get("limit"))
		return reply(200, map[string]any{"requests": recentRequests(n, q.Get("failed") != ""), "size": reqLogSize})
	case method == "GET" && path == base+"usage":
		h, _ := strconv.Atoi(q.Get("hours"))
		return handleUsage(h, q.Get("account"))
	case method == "POST" && path == base+"usage/clear":
		clearUsage()
		return reply(200, map[string]any{"ok": true})
	case method == "GET" && path == base+"credits/composition":
		return handleComposition(q.Get("refresh") != "")
	case method == "GET" && path == base+"credits/packages":
		return handlePackages(q.Get("account"), q.Get("refresh") != "")
	case method == "GET" && path == base+"models":
		return handleModels(d, q.Get("build"), q.Get("account"))
	case method == "GET" && path == base+"tasks":
		return handleTasks(q.Get("account"))
	case method == "POST" && path == base+"keys/check":
		return handleKeyCheck(d, r.Body)
	case method == "POST" && path == base+"history/clear":
		clearHistory()
		return reply(200, map[string]string{"status": "ok"})
	}
	return replyErr(404, "unknown route "+method+" "+r.Path)
}

// ---- status ------------------------------------------------------------------

func statusBody(q url.Values) map[string]any {
	c := cfg()
	n, _ := strconv.Atoi(q.Get("limit"))
	if n <= 0 {
		n = 100
	}
	sched.Lock()
	s := map[string]any{
		"enabled": c.schedulerOn(), "running": sched.running, "busy": sched.busy,
		"last_tick": sched.lastAt, "next_tick": sched.nextAt, "error": sched.lastErr,
	}
	sched.Unlock()
	type jobView struct {
		JobInfo
		On    bool     `json:"on"`
		Slots []string `json:"slots"`
	}
	var jv []jobView
	for _, j := range JobCatalog {
		jv = append(jv, jobView{JobInfo: j, On: c.jobOn(j.Key), Slots: c.slotsOf(j.Key)})
	}
	return map[string]any{
		"version":   Version,
		"now":       time.Now(),
		"beijing":   time.Now().In(beijing).Format("2006-01-02 15:04:05"),
		"scheduler": s,
		"jobs":      jv,
		"claim":     c.claim(),
		"config":    c,
		"running":   runningRuns(),
		"last":      lastRunsAll(),
		"history":   history(n, q.Get("account")),
		"state":     statePath(),
		"actions":   jobs.Actions,
		"human":     jobs.HumanTasks,
		"campaigns": jobs.Campaigns,
	}
}

func handleRun(body []byte) (any, error) {
	var req struct {
		Account string `json:"account"`
		Job     string `json:"job"`
		Task    string `json:"task"`
	}
	_ = json.Unmarshal(body, &req)
	if req.Job != "" && jobInfo(req.Job) == nil {
		return replyErr(400, "未知任务 "+req.Job)
	}
	if req.Account == "" && req.Job == "" {
		go tick(time.Now(), true)
		return reply(202, map[string]any{"status": "started", "message": "已开始运行所有已开启的任务（全部账号）"})
	}
	list, err := accounts()
	if err != nil {
		return replyErr(502, err.Error())
	}
	var targets []acct
	for _, x := range list {
		if req.Account == "" || req.Account == "*" || x.ID == req.Account || x.Index == req.Account {
			targets = append(targets, x)
		}
	}
	if len(targets) == 0 {
		return replyErr(404, "没有匹配的账号")
	}
	keys := []string{req.Job}
	if req.Job == "" || req.Job == "all" {
		keys = nil
		for _, j := range JobCatalog {
			if j.Key != "night" || jobs.InNightWindow(time.Now()) {
				keys = append(keys, j.Key)
			}
		}
	}
	started := 0
	for _, x := range targets {
		if x.Disabled {
			continue
		}
		var mine []string
		for _, k := range keys {
			if applies(k, &x) {
				mine = append(mine, k)
			}
		}
		if len(mine) == 0 {
			continue
		}
		started++
		go func(x acct, mine []string) {
			for _, k := range mine {
				ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
				runJob(ctx, &x, k, req.Task, "manual")
				cancel()
			}
		}(x, mine)
	}
	if started == 0 {
		return replyErr(409, "所选任务不适用于这些账号（构建 / 企业 / API Key / 已停用）")
	}
	return reply(202, map[string]any{"status": "started", "accounts": started, "jobs": keys, "task": req.Task})
}

// ---- accounts ------------------------------------------------------------------

type accountView struct {
	ID             string   `json:"id"`
	AuthIndex      string   `json:"auth_index"`
	File           string   `json:"file"`
	Build          string   `json:"build"`
	Provider       string   `json:"provider"`
	Name           string   `json:"name"`
	UID            string   `json:"uid,omitempty"`
	Enterprise     string   `json:"enterprise,omitempty"`
	Plan           string   `json:"plan,omitempty"`
	Source         string   `json:"source"`
	Expires        int64    `json:"expires,omitempty"`
	RefreshExpires int64    `json:"refresh_expires,omitempty"`
	Pending        bool     `json:"pending,omitempty"`
	Disabled       bool     `json:"disabled"`
	Status         string   `json:"status,omitempty"`
	Error          string   `json:"error,omitempty"`
	Jobs           []Run    `json:"last,omitempty"`
	Applies        []string `json:"applies"`
}

func viewOf(x acct) accountView {
	v := accountView{ID: x.ID, AuthIndex: x.Index, File: x.Name, Disabled: x.Disabled, Status: x.Status, Error: x.Err, Build: buildOf(x.Site)}
	if x.Site != nil {
		v.Provider = x.Site.Provider
	}
	if a := x.A; a != nil {
		v.Name, v.UID, v.Enterprise, v.Plan = a.AccountID, a.UID, a.EnterpriseID, a.Plan
		v.Source = "oauth"
		if a.IsKey() {
			v.Source = "key"
		}
		v.Expires, v.RefreshExpires, v.Pending = a.Expires, a.RefreshExpiresAt, a.Pending
	}
	if v.Name == "" {
		v.Name = x.Name
	}
	for _, j := range JobCatalog {
		if applies(j.Key, &x) {
			v.Applies = append(v.Applies, j.Key)
		}
	}
	v.Jobs = lastRuns(x.ID)
	return v
}

func handleAccounts() (any, error) {
	list, err := accounts()
	if err != nil {
		return replyErr(502, err.Error())
	}
	out := make([]accountView, 0, len(list))
	for _, x := range list {
		out = append(out, viewOf(x))
	}
	return reply(200, map[string]any{"accounts": out})
}

// ---- credits -------------------------------------------------------------------

type pkgView struct {
	Name     string  `json:"name"`
	Code     string  `json:"code,omitempty"`
	Reason   string  `json:"reason,omitempty"`
	Size     float64 `json:"size"`
	Remain   float64 `json:"remain"`
	Used     float64 `json:"used"`
	ExpireAt int64   `json:"expire_at,omitempty"`
	Status   any     `json:"status,omitempty"`
}

func num(p map[string]any, keys ...string) float64 {
	for _, k := range keys {
		switch v := p[k].(type) {
		case float64:
			return v
		case string:
			if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				return f
			}
		}
	}
	return 0
}

// PackageView reads one billing package as the page shows it.
func PackageView(p map[string]any) pkgView {
	v := pkgView{
		Size:   num(p, "CycleCapacitySizePrecise", "CycleCapacitySize", "CapacitySizePrecise", "CapacitySize"),
		Remain: num(p, "CycleCapacityRemainPrecise", "CycleCapacityRemain", "CapacityRemainPrecise", "CapacityRemain"),
		Used:   num(p, "CycleCapacityUsedPrecise", "CycleCapacityUsed", "CapacityUsedPrecise", "CapacityUsed"),
		Status: p["Status"],
	}
	if v.Size > 0 && v.Used <= 0 && v.Remain <= v.Size {
		v.Used = v.Size - v.Remain
	} else if v.Size > 0 && v.Remain <= 0 && v.Used < v.Size {
		v.Remain = v.Size - v.Used
	}
	v.Name, _ = p["PackageName"].(string)
	if v.Name == "" {
		v.Name = "Package"
	}
	v.Code, _ = p["PackageCode"].(string)
	if attrs, ok := p["AccountAttributes"].([]any); ok {
		for _, a := range attrs {
			if m, ok := a.(map[string]any); ok && m["Key"] == "grantReason" {
				v.Reason = fmt.Sprint(m["Value"])
			}
		}
	}
	v.ExpireAt = wb.EndOf(p)
	return v
}

type creditsView struct {
	Account  string    `json:"account"`
	Name     string    `json:"name"`
	Build    string    `json:"build"`
	Usage    *wb.Usage `json:"usage,omitempty"`
	Packages []pkgView `json:"packages,omitempty"`
	Left     *float64  `json:"left,omitempty"`
	Total    *float64  `json:"total,omitempty"`
	Error    string    `json:"error,omitempty"`
}

func creditsOf(ctx context.Context, d HostDoer, x acct) creditsView {
	v := creditsView{Account: x.ID, Name: x.Name, Build: buildOf(x.Site)}
	if x.A == nil {
		v.Error = "凭据无法读取"
		return v
	}
	if x.A.AccountID != "" {
		v.Name = x.A.AccountID
	}
	ch := channelFor(x.Site)
	u := ch.Credits(ctx, d, x.ID, x.A)
	v.Usage = u
	if u.Error != "" {
		v.Error = u.Error
	}
	var left, total float64
	have := false
	pick := map[string]*wb.Window{}
	for _, w := range u.Windows {
		pick[w.Name] = w
	}
	for _, n := range []string{"Total credits", "Credits"} {
		if w := pick[n]; w != nil && w.Limit > 0 {
			left, total, have = w.Limit-w.Amount, w.Limit, true
			break
		}
	}
	if w := pick["Prepaid credits"]; w != nil && w.Balance != nil {
		left += *w.Balance
		have = true
	}
	if have {
		v.Left, v.Total = &left, &total
	}
	return v
}

// Credits are read from WorkBuddy per account (there is no batch endpoint), so
// each reading is kept for creditsTTL and served from memory until then. The
// page asks with ?refresh=1 to force a fresh read.
const creditsTTL = 5 * time.Minute

// creditsMaxAge is how old a kept reading may be: creditsTTL, or a little
// over the background interval when readings are taken in the background.
func creditsMaxAge() time.Duration {
	if every := cfg().creditsRefresh(); every > 0 && every+2*time.Minute > creditsTTL {
		return every + 2*time.Minute
	}
	return creditsTTL
}

type creditsEntry struct {
	at time.Time
	v  creditsView
}

var (
	creditsMu    sync.Mutex
	creditsCache = map[string]creditsEntry{}
)

func cachedCredits(id string) (creditsView, time.Time, bool) {
	loadCreditStore()
	creditsMu.Lock()
	defer creditsMu.Unlock()
	e, ok := creditsCache[id]
	if !ok || time.Since(e.at) > creditsMaxAge() {
		return creditsView{}, time.Time{}, false
	}
	return e.v, e.at, true
}

func storeCredits(id string, v creditsView) {
	if v.Error != "" {
		return
	}
	now := time.Now()
	creditsMu.Lock()
	creditsCache[id] = creditsEntry{at: now, v: v}
	creditsMu.Unlock()
	noteCredits(id, v, now)
}

func handleCredits(d HostDoer, account string, refresh bool) (any, error) {
	list, err := accounts()
	if err != nil {
		return replyErr(502, err.Error())
	}
	var targets []acct
	for _, x := range list {
		if x.A != nil && x.A.IsKey() && account == "" {
			continue // a key reads no credits
		}
		if account == "" || x.ID == account || x.Index == account {
			targets = append(targets, x)
		}
	}
	out := make([]creditsView, len(targets))
	stamp := make([]int64, len(targets))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, x := range targets {
		wg.Add(1)
		go func(i int, x acct) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if !refresh {
				if v, at, ok := cachedCredits(x.ID); ok {
					out[i] = v
					stamp[i] = at.UnixMilli()
					return
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			// background callbacks: the management call's id ends with it
			out[i] = creditsOf(ctx, HostDoer{}, x)
			storeCredits(x.ID, out[i])
			stamp[i] = time.Now().UnixMilli()
		}(i, x)
	}
	wg.Wait()
	sum := map[string]float64{}
	for _, v := range out {
		if v.Left != nil {
			sum[v.Build+"_left"] += *v.Left
			sum["left"] += *v.Left
		}
		if v.Total != nil {
			sum[v.Build+"_total"] += *v.Total
			sum["total"] += *v.Total
		}
	}
	_ = d
	var oldest int64
	for _, t := range stamp {
		if t > 0 && (oldest == 0 || t < oldest) {
			oldest = t
		}
	}
	return reply(200, map[string]any{"accounts": out, "summary": sum, "as_of": oldest, "ttl_ms": creditsMaxAge().Milliseconds(), "background_ms": cfg().creditsRefresh().Milliseconds()})
}

// Packages are read only when the page opens an account's list: two more
// requests per account that the totals do not need. Kept like the totals.
var (
	pkgMu    sync.Mutex
	pkgCache = map[string]struct {
		at time.Time
		v  []pkgView
	}{}
)

func handlePackages(account string, refresh bool) (any, error) {
	list, err := accounts()
	if err != nil {
		return replyErr(502, err.Error())
	}
	var x *acct
	for i := range list {
		if list[i].ID == account || list[i].Index == account {
			x = &list[i]
			break
		}
	}
	if x == nil || x.A == nil {
		return replyErr(404, "没有这个账号")
	}
	pk, at, err := packagesFor(*x, refresh)
	if err != nil {
		return replyErr(502, err.Error())
	}
	return reply(200, map[string]any{"account": x.ID, "packages": pk, "as_of": at.UnixMilli()})
}

// packagesFor is an account's packages, kept for creditsTTL.
func packagesFor(x acct, refresh bool) ([]pkgView, time.Time, error) {
	if x.A == nil || x.A.IsKey() {
		return []pkgView{}, time.Now(), nil
	}
	if !refresh {
		pkgMu.Lock()
		e, ok := pkgCache[x.ID]
		pkgMu.Unlock()
		if ok && time.Since(e.at) <= creditsTTL {
			return e.v, e.at, nil
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	a := newest(x.ID, x.A)
	pk, err := wb.PackagesOf(ctx, HostDoer{}, x.Site, a, time.Now().UnixMilli())
	if err != nil {
		return nil, time.Time{}, err
	}
	out := make([]pkgView, 0, len(pk))
	for _, p := range pk {
		out = append(out, PackageView(p))
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].ExpireAt, out[j].ExpireAt
		if a == 0 {
			return false
		}
		return b == 0 || a < b
	})
	now := time.Now()
	pkgMu.Lock()
	pkgCache[x.ID] = struct {
		at time.Time
		v  []pkgView
	}{now, out}
	pkgMu.Unlock()
	return out, now, nil
}

// handleComposition is every account's packages side by side, and what of
// them runs out on each Beijing day.
func handleComposition(refresh bool) (any, error) {
	list, err := accounts()
	if err != nil {
		return replyErr(502, err.Error())
	}
	type row struct {
		Account  string    `json:"account"`
		Name     string    `json:"name"`
		Build    string    `json:"build"`
		Remain   float64   `json:"remain"`
		Size     float64   `json:"size"`
		Packages []pkgView `json:"packages"`
		Error    string    `json:"error,omitempty"`
	}
	var targets []acct
	for _, x := range list {
		if x.A != nil && !x.A.IsKey() {
			targets = append(targets, x)
		}
	}
	out := make([]row, len(targets))
	stamp := make([]int64, len(targets))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, x := range targets {
		wg.Add(1)
		go func(i int, x acct) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			r := row{Account: x.ID, Name: x.Name, Build: buildOf(x.Site)}
			if x.A.AccountID != "" {
				r.Name = x.A.AccountID
			}
			pk, at, err := packagesFor(x, refresh)
			if err != nil {
				r.Error = err.Error()
			} else {
				stamp[i] = at.UnixMilli()
				r.Packages = pk
				for _, p := range pk {
					r.Remain += p.Remain
					r.Size += p.Size
				}
			}
			out[i] = r
		}(i, x)
	}
	wg.Wait()
	type day struct {
		Day      string  `json:"day"`
		At       int64   `json:"at"`
		Remain   float64 `json:"remain"`
		Packages int     `json:"packages"`
		Accounts int     `json:"accounts"`
	}
	days := map[string]*day{}
	seen := map[string]map[string]bool{}
	var never float64
	var sum, size float64
	for _, r := range out {
		sum += r.Remain
		size += r.Size
		for _, p := range r.Packages {
			if p.Remain <= 0 {
				continue
			}
			if p.ExpireAt == 0 {
				never += p.Remain
				continue
			}
			d := time.UnixMilli(p.ExpireAt).In(beijing).Format("2006-01-02")
			if days[d] == nil {
				days[d] = &day{Day: d, At: p.ExpireAt}
				seen[d] = map[string]bool{}
			}
			days[d].Remain += p.Remain
			days[d].Packages++
			if !seen[d][r.Account] {
				seen[d][r.Account] = true
				days[d].Accounts++
			}
		}
	}
	dl := make([]*day, 0, len(days))
	for _, d := range days {
		dl = append(dl, d)
	}
	sort.Slice(dl, func(i, j int) bool { return dl[i].Day < dl[j].Day })
	var oldest int64
	for _, t := range stamp {
		if t > 0 && (oldest == 0 || t < oldest) {
			oldest = t
		}
	}
	return reply(200, map[string]any{"accounts": out, "days": dl, "no_expiry": never, "remain": sum, "size": size, "as_of": oldest})
}

// ---- models --------------------------------------------------------------------

func handleModels(d HostDoer, build, account string) (any, error) {
	site := wb.SiteCN()
	if build == "ai" {
		site = wb.SiteAI()
	}
	c := cfg()
	resp := map[string]any{"build": buildOf(site), "free_only": c.freeOnly(site), "builtin": wb.BuiltinList(site), "versions": map[string]string{"app": site.App, "cli": site.CLI}, "sdk_os": wb.SDKOS, "sdk_arch": wb.SDKArch}
	list, err := accounts()
	if err != nil {
		resp["error"] = err.Error()
		return reply(200, resp)
	}
	var pick *acct
	for i := range list {
		x := &list[i]
		if x.Site != site || x.A == nil || x.Disabled {
			continue
		}
		if account != "" && x.ID != account {
			continue
		}
		if x.A.IsKey() && !wb.RemoteConfig(x.A.Key) {
			continue
		}
		pick = x
		break
	}
	if pick == nil {
		resp["error"] = "没有可用的" + site.Name + "账号读取实时模型，下面是内置表"
		return reply(200, resp)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	a := pick.A
	if !a.IsKey() {
		ch := channelFor(site)
		fresh, err := wb.Fresh(ctx, HostDoer{}, site, a, func(n *wb.Account) { ch.Save(pick.ID, n) })
		if err != nil {
			resp["error"] = err.Error()
			return reply(200, resp)
		}
		a = fresh
	}
	live, err := channelFor(site).Listed(ctx, HostDoer{}, a, true)
	if err != nil {
		resp["error"] = err.Error()
		return reply(200, resp)
	}
	resp["live"] = live
	resp["account"] = pick.ID
	resp["account_name"] = viewOf(*pick).Name
	_ = d
	return reply(200, resp)
}

// ---- tasks ---------------------------------------------------------------------

type taskView struct {
	jobs.Task
	Auto  bool   `json:"auto"`
	How   string `json:"how,omitempty"`
	Chat  bool   `json:"chat,omitempty"`
	Night bool   `json:"night,omitempty"`
	Human string `json:"human,omitempty"`
	Job   string `json:"job"`
	Last  *Run   `json:"last,omitempty"`
}

func handleTasks(account string) (any, error) {
	x, err := accountByID(account)
	if err != nil {
		return replyErr(404, err.Error())
	}
	v := viewOf(*x)
	out := map[string]any{"account": v}
	if !applies("growth", x) {
		out["growth_available"] = false
		out["note"] = "成长任务、连登、猫猫旅行与运营活动只适用于国内版个人账号；此账号可用的任务见下方"
		return reply(200, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c, err := client(ctx, x)
	if err != nil {
		out["error"] = err.Error()
		return reply(200, out)
	}
	last := map[string]Run{}
	for _, r := range lastRuns(x.ID) {
		last[lastKey(r)] = r
	}
	var (
		wg         sync.WaitGroup
		list, mp   []jobs.Task
		lerr, merr error
		summary    *jobs.GrowthSummary
		camps      = map[string]map[string]jobs.CampaignRow{}
		campErr    = map[string]string{}
		cmu        sync.Mutex
	)
	wg.Add(3)
	go func() { defer wg.Done(); list, lerr = c.ListTasks(ctx, false) }()
	go func() { defer wg.Done(); mp, merr = c.ListTasks(ctx, true) }()
	go func() { defer wg.Done(); summary = c.Summary(ctx) }()
	for _, cp := range jobs.Campaigns {
		wg.Add(1)
		go func(code string) {
			defer wg.Done()
			rows, err := c.CampaignTasks(ctx, code)
			cmu.Lock()
			defer cmu.Unlock()
			if err != nil {
				campErr[code] = err.Error()
				return
			}
			camps[code] = rows
		}(cp.Code)
	}
	wg.Wait()
	if lerr != nil {
		out["error"] = "任务列表读取失败: " + lerr.Error()
	}
	seen := map[string]bool{}
	var views []taskView
	add := func(t jobs.Task) {
		if seen[t.Code] {
			return
		}
		seen[t.Code] = true
		tv := taskView{Task: t, Job: "growth"}
		if jobs.MPTask(t.Code) {
			tv.Job = "miniprogram"
			tv.MP = true
		}
		if a := jobs.ActionFor(t.Code); a != nil {
			tv.Auto, tv.How, tv.Chat, tv.Night = true, a.How, a.Chat, a.Night
		} else if h := jobs.HumanFor(t.Code); h != nil {
			tv.Human = h.Reason
		} else if _, ok := jobs.ExpertFromJump(t.Code, t.JumpURL, t.Title); ok {
			tv.Auto, tv.How, tv.Chat = true, "任务指定专家：召唤 → 真实对话 → 使用事件", true
		} else {
			tv.Human = "没有可用的接口完成：" + firstOr(t.Description, t.TaskDesc, "请在官方客户端按任务说明操作")
		}
		if r, ok := last[tv.Job+":"+t.Code]; ok {
			r := r
			tv.Last = &r
		}
		views = append(views, tv)
	}
	for _, t := range list {
		add(t)
	}
	for _, t := range mp {
		if jobs.MPTask(t.Code) {
			add(t)
		}
	}
	out["tasks"] = views
	if merr != nil {
		out["mp_error"] = merr.Error()
	}
	out["summary"] = summary
	out["campaigns"] = camps
	out["campaign_errors"] = campErr
	out["growth_available"] = true
	return reply(200, out)
}

func firstOr(s ...string) string {
	for _, x := range s {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}

// ---- key check -----------------------------------------------------------------

func handleKeyCheck(d HostDoer, body []byte) (any, error) {
	var req struct {
		Build string `json:"build"`
		Key   string `json:"key"`
	}
	if err := json.Unmarshal(body, &req); err != nil || strings.TrimSpace(req.Key) == "" {
		return replyErr(400, "key is required")
	}
	site := wb.SiteCN()
	if req.Build == "ai" {
		site = wb.SiteAI()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := wb.CheckKey(ctx, d, site, req.Key); err != nil {
		return reply(200, map[string]any{"ok": false, "error": err.Error()})
	}
	a := wb.KeyAccount(req.Key)
	a.Type = site.Provider
	return reply(200, map[string]any{
		"ok": true, "remote_config": wb.RemoteConfig(req.Key),
		"file":    fileNameOf(site, a),
		"storage": storageOf(a),
	})
}
