package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"github.com/Sakuralaaa/WorkBuddy-plugins/internal/jobs"
	"github.com/Sakuralaaa/WorkBuddy-plugins/internal/wb"
)

// JobInfo describes one background job as the page lists it.
type JobInfo struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	Desc  string `json:"desc"`
	Build string `json:"build"` // cn, ai, both
	// Schedule is the config key of its times.
	Schedule string `json:"schedule"`
}

// JobCatalog are the jobs, in the order a full run takes them.
var JobCatalog = []JobInfo{
	{"checkin", "每日签到", "国内版 Buddy 加油站签到（每北京日一次）", "cn", "checkin_at"},
	{"activity_report", "对话活跃上报", "每天一条 chat_request_send，点亮成长中心连登", "both", "activity_hours"},
	{"rewards", "连登管家", "补签卡保连登 → 新手礼包/活动补偿 → 兑换已解锁档位 → 抽完抽奖次数", "cn", "rewards_hours"},
	{"cat_travel", "猫猫旅行", "到站领奖 → 派出旅行（无猫先领养）→ 用能量开 Buddy 盲盒", "cn", "travel_hours"},
	{"growth", "成长任务", "一次性成长任务：接取 → 完成 → 等待落账 → 领奖", "cn", "growth_hours"},
	{"campaigns", "运营活动", "星巴克协作 / 记者节等活动任务（手机 App 任务只提示）", "cn", "growth_hours"},
	{"miniprogram", "小程序任务链", "Sequential_Tasks / 校园日（真人节奏间隔，较慢）", "cn", "growth_hours"},
	{"night", "夜猫子", "23:00–08:00 内 glm-5.2 对话，每晚 1 次，累计 3 晚", "cn", "night_hours"},
	{"intl_daily", "国际版每日活跃", "桌面通道短对话；开启网页通道时再跑完一条云端会话（计入积分的那条）", "ai", "intl_daily_times"},
	{"sweep", "补领清扫", "每 sweep_minutes 接取并领取已达标的任务（只做接取+领奖，不跑行为链，不看任务白名单）", "cn", "sweep_minutes"},
}

func jobInfo(key string) *JobInfo {
	for i := range JobCatalog {
		if JobCatalog[i].Key == key {
			return &JobCatalog[i]
		}
	}
	return nil
}

// jobOn says whether the config switches a job on for the scheduler.
func (c *Config) jobOn(key string) bool {
	t := c.Tasks
	switch key {
	case "checkin":
		return c.Checkin
	case "activity_report":
		return t.ActivityReport
	case "rewards":
		return t.Rewards
	case "cat_travel":
		return t.CatTravel
	case "growth":
		return t.Growth
	case "campaigns":
		return t.Campaigns
	case "miniprogram":
		return t.MiniProgram
	case "night":
		return t.Night
	case "intl_daily":
		return t.IntlDaily
	case "sweep":
		return t.Sweep
	}
	return false
}

// slotsOf are a job's times of day (Beijing), as "HH:MM".
func (c *Config) slotsOf(key string) []string {
	s := c.Schedule
	hours := func(h []int) []string {
		var out []string
		for _, x := range h {
			if x >= 0 && x < 24 {
				out = append(out, fmt.Sprintf("%02d:00", x))
			}
		}
		return out
	}
	switch key {
	case "checkin":
		if t := normTime(s.CheckinAt); t != "" {
			return []string{t}
		}
		return []string{"00:05"}
	case "activity_report":
		return hours(s.ActivityHours)
	case "rewards":
		return hours(s.RewardsHours)
	case "cat_travel":
		return hours(s.TravelHours)
	case "growth", "campaigns", "miniprogram":
		return hours(s.GrowthHours)
	case "night":
		return hours(s.NightHours)
	case "intl_daily":
		var out []string
		for _, t := range s.DailyTimes {
			if n := normTime(t); n != "" {
				out = append(out, n)
			}
		}
		return out
	}
	return nil
}

// oncePerDay jobs stop for the day once one run succeeded.
func oncePerDay(key string) bool {
	return key == "checkin" || key == "activity_report" || key == "intl_daily"
}

func normTime(s string) string {
	s = strings.TrimSpace(s)
	var h, m int
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil {
		return ""
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return ""
	}
	return fmt.Sprintf("%02d:%02d", h, m)
}

var beijing = time.FixedZone("UTC+8", 8*3600)

// DueSlot is the latest slot of the Beijing day at or before now, as
// "2006-01-02 15:04", "" when none has come yet today.
func DueSlot(now time.Time, slots []string) string {
	bj := now.In(beijing)
	cur := bj.Format("15:04")
	best := ""
	for _, s := range slots {
		if n := normTime(s); n != "" && n <= cur && n > best {
			best = n
		}
	}
	if best == "" {
		return ""
	}
	return bj.Format("2006-01-02") + " " + best
}

// ---- accounts ------------------------------------------------------------

// acct is one of the plugin's accounts as the jobs see it.
type acct struct {
	ID, Index, Name string
	Disabled        bool
	Status          string
	Site            *wb.Site
	A               *wb.Account
	Err             string
}

// accounts are every WorkBuddy credential CPA holds, read from its files.
func accounts() ([]acct, error) {
	entries, err := authList()
	if err != nil {
		return nil, err
	}
	var out []acct
	for _, e := range entries {
		if !ours(e.Provider) && !ours(e.Type) {
			continue
		}
		x := acct{ID: e.ID, Index: e.AuthIndex, Name: e.Name, Disabled: e.Disabled, Status: e.Status}
		raw, name, err := authGet(e.AuthIndex)
		if name != "" {
			x.Name = name
			heldMu.Lock()
			if strings.HasSuffix(strings.ToLower(name), ".json") {
				files[e.ID] = name
			}
			heldMu.Unlock()
		}
		if err != nil {
			x.Err = err.Error()
			x.Site = wb.SiteFor(e.Provider)
			out = append(out, x)
			continue
		}
		provider := e.Provider
		if !ours(provider) {
			provider = e.Type
		}
		site, a, err := accountFrom(provider, e.ID, raw, nil)
		x.Site, x.A = site, a
		if err != nil {
			x.Err = err.Error()
		}
		out = append(out, x)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func accountByID(id string) (*acct, error) {
	list, err := accounts()
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].ID == id || list[i].Index == id || list[i].Name == id {
			return &list[i], nil
		}
	}
	return nil, fmt.Errorf("没有这个 WorkBuddy 账号: %s", id)
}

// client is a jobs client for an account with a fresh token.
func client(ctx context.Context, x *acct) (*jobs.Client, error) {
	if x.A == nil {
		return nil, fmt.Errorf("凭据无法读取: %s", x.Err)
	}
	d := HostDoer{}
	a := x.A
	if a.Pending {
		next, err := completeImport(ctx, d, x.Site, a)
		if err != nil {
			return nil, err
		}
		holdAccount(x.ID, next)
		persist(x.ID, next)
		a = next
	}
	if !a.IsKey() {
		ch := channelFor(x.Site)
		fresh, err := wb.Fresh(ctx, d, x.Site, a, func(n *wb.Account) { ch.Save(x.ID, n) })
		if err != nil {
			return nil, err
		}
		a = fresh
	}
	nick := a.AccountID
	if i := strings.Index(nick, " · "); i > 0 {
		nick = nick[:i]
	}
	c := &jobs.Client{D: d, Site: x.Site, A: a, Nick: nick}
	// The jobs' headers carry the same device token the chats do: the
	// account's own token (set by wb.Sign) wins, this is the global/file
	// fallback.
	cf := cfg()
	c.DeviceToken = wb.DeviceTokenOf("", cf.DeviceToken, cf.DeviceTokenFile)
	c.Chat = chatFor(x.ID, x.Site, a)
	return c, nil
}

var serverID = regexp.MustCompile(`"id"\s*:\s*"((?:cmb-)?[0-9a-f]{32})"`)
var contentRe = regexp.MustCompile(`"content"\s*:\s*"((?:[^"\\]|\\.)*)"`)

// chatFor sends a real chat through the plugin's own request path.
func chatFor(authID string, site *wb.Site, a *wb.Account) jobs.ChatFunc {
	return func(ctx context.Context, model, prompt string, extra http.Header) (jobs.ChatReply, error) {
		body, _ := json.Marshal(map[string]any{
			"model":          model,
			"messages":       []map[string]string{{"role": "user", "content": prompt}},
			"stream":         true,
			"stream_options": map[string]any{"include_usage": true},
		})
		h := http.Header{}
		for k, v := range extra {
			h[k] = v
		}
		ch := channelFor(site)
		res, err := ch.Fetch(ctx, HostDoer{}, &wb.ChatRequest{AuthID: authID, Account: a, URL: site.API() + "/chat/completions", Body: body, Header: h})
		if err != nil {
			return jobs.ChatReply{}, err
		}
		defer res.Close()
		if res.Status >= 400 || res.Stream == nil {
			return jobs.ChatReply{}, fmt.Errorf("HTTP %d: %s", res.Status, truncate(string(res.Body), 200))
		}
		raw, _ := io.ReadAll(io.LimitReader(res.Stream, 2<<20))
		out := jobs.ChatReply{}
		if m := serverID.FindSubmatch(raw); m != nil {
			out.RequestID = string(m[1])
		}
		var text bytes.Buffer
		for _, m := range contentRe.FindAllSubmatch(raw, -1) {
			var s string
			if json.Unmarshal(append(append([]byte{'"'}, m[1]...), '"'), &s) == nil {
				text.WriteString(s)
			}
		}
		out.Text = text.String()
		return out, nil
	}
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// ---- running ---------------------------------------------------------------

// Run is one job run for one account, as the history keeps it.
type Run struct {
	jobs.Result
	ID      string `json:"id"`
	Account string `json:"account"`
	Name    string `json:"name"`
	Build   string `json:"build"`
	Trigger string `json:"trigger"` // schedule, manual
	Task    string `json:"task,omitempty"`
	Took    int64  `json:"took_ms"`
}

var engine = struct {
	sync.Mutex
	locks   map[string]*sync.Mutex
	running map[string]Run // by run id
	seq     int64
}{locks: map[string]*sync.Mutex{}, running: map[string]Run{}}

func accountLock(id string) *sync.Mutex {
	engine.Lock()
	defer engine.Unlock()
	m := engine.locks[id]
	if m == nil {
		m = &sync.Mutex{}
		engine.locks[id] = m
	}
	return m
}

func buildOf(s *wb.Site) string {
	if s != nil && s.AI {
		return "ai"
	}
	return "cn"
}

// applies says whether a job applies to an account at all.
func applies(key string, x *acct) bool {
	info := jobInfo(key)
	if info == nil || x.Site == nil {
		return false
	}
	if x.A != nil && x.A.IsKey() {
		return false
	}
	switch info.Build {
	case "cn":
		return !x.Site.AI && (x.A == nil || x.A.EnterpriseID == "")
	case "ai":
		return x.Site.AI
	}
	return x.A == nil || x.A.EnterpriseID == ""
}

// runJob runs one job (task: one growth task) for an account, one at a time
// per account, and records it.
func runJob(ctx context.Context, x *acct, key, task, trigger string) Run {
	lock := accountLock(x.ID)
	lock.Lock()
	defer lock.Unlock()
	engine.Lock()
	engine.seq++
	id := fmt.Sprintf("%d-%d", time.Now().UnixMilli(), engine.seq)
	run := Run{ID: id, Account: x.ID, Name: x.Name, Build: buildOf(x.Site), Trigger: trigger, Task: task}
	run.Job, run.At, run.Outcome = key, time.Now(), "running"
	engine.running[id] = run
	engine.Unlock()
	defer func() {
		engine.Lock()
		delete(engine.running, id)
		engine.Unlock()
	}()
	started := time.Now()
	res := execJob(ctx, x, key, task)
	if res.Job == "" {
		res.Job = key
	}
	res.At = started
	run.Result = res
	run.Job = key
	run.Took = time.Since(started).Milliseconds()
	record(run)
	lvl := "info"
	if !res.OK {
		lvl = "warn"
	}
	hostLog(lvl, fmt.Sprintf("job %s %s: %s %s", key, x.Name, res.Outcome, res.Message), map[string]any{"auth": x.ID})
	return run
}

func execJob(ctx context.Context, x *acct, key, task string) jobs.Result {
	fail := func(msg string) jobs.Result {
		return jobs.Result{Job: key, At: time.Now(), Outcome: "failed", Message: msg}
	}
	if !applies(key, x) {
		return jobs.Result{Job: key, At: time.Now(), OK: true, Outcome: "skipped", Message: "该任务不适用于此账号（构建/企业/API Key）"}
	}
	c, err := client(ctx, x)
	if err != nil {
		return fail("登录状态不可用: " + err.Error())
	}
	cf := cfg()
	claim := cf.claim()
	switch key {
	case "checkin":
		res, err := wb.CheckinOf(ctx, c.D, c.Site, c.A, []time.Duration{2 * time.Second, 4 * time.Second})
		if err != nil {
			return fail(err.Error())
		}
		r := jobs.Result{Job: key, At: time.Now(), Credits: res.Credit}
		r.OK = res.Outcome != "failed"
		r.Outcome = res.Outcome
		r.Message = checkinText(res)
		return r
	case "activity_report":
		return c.RunActivityReport(ctx)
	case "rewards":
		return c.RunRewards(ctx, time.Now(), claim)
	case "cat_travel":
		return c.RunTravel(ctx, claim)
	case "growth":
		if task != "" {
			return c.RunGrowthTask(ctx, task, claim)
		}
		return c.RunGrowthAll(ctx, false, claim)
	case "miniprogram":
		if task != "" {
			return c.RunGrowthTask(ctx, task, claim)
		}
		return c.RunGrowthAll(ctx, true, claim)
	case "campaigns":
		return c.RunCampaigns(ctx, claim)
	case "night":
		return c.RunNight(ctx, claim)
	case "intl_daily":
		return c.RunIntlDaily(ctx, intlModels(ctx, c), cf.Tasks.IntlDailyWeb, "Hi")
	case "sweep":
		return c.RunTaskSweep(ctx, claim)
	}
	return fail("未知任务 " + key)
}

func checkinText(c wb.Checkin) string {
	switch c.Outcome {
	case "claimed":
		s := fmt.Sprintf("签到成功 +%.0f 积分", c.Credit)
		if c.Streak > 0 {
			s += fmt.Sprintf("，连续 %.0f 天", c.Streak)
		}
		return s
	case "done":
		return "今日已签到"
	case "inactive":
		return "签到活动未开启"
	case "ineligible":
		return "该账号不能签到：" + c.Message
	}
	if c.Message != "" {
		return c.Message
	}
	return c.Outcome
}

// intlModels are the daily activity's candidates: DeepSeek Flash, then the
// account's free models.
func intlModels(ctx context.Context, c *jobs.Client) []string {
	out := []string{"deepseek-v4.1-flash"}
	if list, err := channelFor(c.Site).Listed(ctx, c.D, c.A, false); err == nil {
		for _, m := range list {
			if m.Free && m.ID != out[0] {
				out = append(out, m.ID)
			}
		}
	}
	if len(out) > 4 {
		out = out[:4]
	}
	return out
}

// startRun runs a job in the background for the page; its run id.
func startRun(x acct, key, task string) string {
	engine.Lock()
	n := engine.seq + 1
	engine.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
		defer cancel()
		runJob(ctx, &x, key, task, "manual")
	}()
	return fmt.Sprintf("%s/%s#%d", x.ID, key, n)
}

// runningRuns are the runs under way.
func runningRuns() []Run {
	engine.Lock()
	defer engine.Unlock()
	out := make([]Run, 0, len(engine.running))
	for _, r := range engine.running {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// ---- scheduler -----------------------------------------------------------------

var sched struct {
	sync.Mutex
	stop    chan struct{}
	running bool
	lastAt  time.Time
	nextAt  time.Time
	lastErr string
	busy    bool
}

func startScheduler() {
	sched.Lock()
	defer sched.Unlock()
	if sched.stop != nil {
		return
	}
	stop := make(chan struct{})
	sched.stop = stop
	sched.running = true
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		select {
		case <-stop:
			return
		case <-time.After(20 * time.Second):
		}
		tick(time.Now(), false)
		if cfg().schedulerOn() {
			creditsTick(time.Now())
		}
		for {
			select {
			case <-stop:
				return
			case now := <-t.C:
				if cfg().schedulerOn() {
					creditsTick(now)
				}
				tick(now, false)
			}
		}
	}()
}

func stopScheduler() {
	sched.Lock()
	defer sched.Unlock()
	if sched.stop != nil {
		close(sched.stop)
		sched.stop = nil
	}
	sched.running = false
}

// tick runs the jobs due (all switched-on jobs with force). Accounts run
// in parallel (tasks_concurrency, 1–4); each account still runs one job at
// a time (runJob's per-account lock). A tick still running makes the next
// one wait.
func tick(now time.Time, force bool) {
	sched.Lock()
	if sched.busy {
		sched.Unlock()
		return
	}
	sched.busy = true
	sched.lastAt = now
	sched.nextAt = now.Add(time.Minute)
	sched.Unlock()
	defer func() {
		sched.Lock()
		sched.busy = false
		sched.Unlock()
	}()
	c := cfg()
	if !c.schedulerOn() && !force {
		return
	}
	due := false
	for _, info := range JobCatalog {
		if !c.jobOn(info.Key) {
			continue
		}
		if info.Key == "sweep" {
			if force || c.sweepMinutes() > 0 {
				due = true
			}
			continue
		}
		if force || DueSlot(now, c.slotsOf(info.Key)) != "" {
			due = true
		}
	}
	if !due {
		return
	}
	list, err := accounts()
	sched.Lock()
	sched.lastErr = ""
	if err != nil {
		sched.lastErr = err.Error()
	}
	sched.Unlock()
	if err != nil {
		return
	}
	day := now.In(beijing).Format("2006-01-02")
	for _, info := range JobCatalog {
		if !c.jobOn(info.Key) {
			continue
		}
		slot := DueSlot(now, c.slotsOf(info.Key))
		if info.Key == "sweep" {
			// Interval job, not a time-of-day slot: run when the last
			// sweep is older than sweep_minutes (or never ran).
			if !force && c.sweepMinutes() <= 0 {
				continue
			}
			slot = now.In(beijing).Format("2006-01-02 15:04")
		} else if slot == "" && !force {
			continue
		}
		runAccounts(list, info.Key, slot, day, force, now, c.concurrency())
	}
}

// runAccounts runs one job for every account it applies to, in parallel
// up to n at a time.
func runAccounts(list []acct, key, slot, day string, force bool, now time.Time, n int) {
	var targets []acct
	for i := range list {
		x := list[i]
		if x.Disabled || !applies(key, &x) {
			continue
		}
		if !force {
			if key == "sweep" {
				// Sweep by elapsed time since the last sweep run.
				if last := lastRunAt(x.ID, key); !last.IsZero() && now.Sub(last) < time.Duration(cfg().sweepMinutes())*time.Minute {
					continue
				}
			} else if lastSlot(x.ID, key) == slot {
				continue
			}
			if oncePerDay(key) && okDay(x.ID, key) == day {
				markSlot(x.ID, key, slot)
				continue
			}
			if key == "checkin" {
				// A failed check-in retries on a doubling backoff
				// (15m, 30m, 1h, 2h, cap 4h) instead of waiting for
				// tomorrow: the old code marked the slot after a
				// failed run, so a network failure lost the whole day.
				if tries, next, ok := checkinRetryOf(x.ID, day); ok && tries > 0 && now.Before(next) {
					continue
				}
			}
		}
		targets = append(targets, x)
	}
	if len(targets) == 0 {
		return
	}
	if n < 1 {
		n = 1
	}
	sem := make(chan struct{}, n)
	var wg sync.WaitGroup
	for _, x := range targets {
		x := x
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
			trigger := "schedule"
			if force {
				trigger = "manual"
			}
			run := runJob(ctx, &x, key, "", trigger)
			cancel()
			if key == "checkin" {
				// Settled outcomes (claimed/done/ineligible/inactive)
				// stand for the day; a failure keeps the slot open
				// and schedules the next retry.
				if run.OK {
					clearCheckinRetry(x.ID, day)
					markSlot(x.ID, key, slot)
					if run.Outcome != "skipped" {
						markOK(x.ID, key, day)
					}
				} else {
					tries, _, _ := checkinRetryOf(x.ID, day)
					tries++
					backoff := 15 * time.Minute << uint(min(tries-1, 4))
					if backoff > 4*time.Hour {
						backoff = 4 * time.Hour
					}
					noteCheckinRetry(x.ID, day, tries, now.Add(backoff))
				}
				return
			}
			markSlot(x.ID, key, slot)
			if run.OK && run.Outcome != "skipped" {
				markOK(x.ID, key, day)
			}
		}()
	}
	wg.Wait()
}

// ensure the import is used
var _ = pluginapi.QuotaBucket{}
