package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/Sakuralaaa/WorkBuddy-plugins/internal/wb"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const summaryPath = "/billing/meter/get-user-resource-summary"

// quotaFetch reads an account's credits as magpie's usage card does.
func quotaFetch(req []byte) (any, error) {
	var r pluginapi.QuotaFetchRequest
	if err := json.Unmarshal(req, &r); err != nil {
		return nil, err
	}
	site, a, err := accountFrom(r.Provider, r.AuthID, r.StorageJSON, r.Metadata)
	if err != nil {
		return nil, fail("bad_auth", err.Error(), http.StatusBadRequest)
	}
	out := pluginapi.QuotaFetchResponse{}
	if a.Source == "key" {
		out.Groups = []pluginapi.QuotaGroup{{DisplayName: "credits unknown with an API key"}}
		return out, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var sum wb.Summary
	if err := wb.Meter(ctx, HostDoer{}, site, a, summaryPath, nil, &sum); err != nil {
		return nil, fail("quota_failed", err.Error(), http.StatusBadGateway)
	}
	u := wb.UsageOf(&sum, a.Plan, site)
	if u.Plan != "" {
		out.Subscription = &pluginapi.QuotaSubscription{Plan: u.Plan, TierName: u.Plan}
	}
	g := pluginapi.QuotaGroup{DisplayName: "WorkBuddy credits"}
	for _, w := range u.Windows {
		if w == nil {
			continue
		}
		left := 1 - w.Used/100
		if left < 0 {
			left = 0
		}
		g.Buckets = append(g.Buckets, pluginapi.QuotaBucket{Window: w.Name, RemainingFraction: left, ResetTime: w.ResetsAt, Description: w.Display})
		if w.Limit > 0 {
			out.Summary = append(out.Summary, pluginapi.QuotaMetric{Key: w.Name, Label: w.Name, Value: w.Limit - w.Amount, Unit: w.Unit})
		}
	}
	out.Groups = append(out.Groups, g)
	if jobs := jobStatus(r.AuthID); len(jobs) > 0 {
		jg := pluginapi.QuotaGroup{DisplayName: "Jobs"}
		for _, j := range jobs {
			jg.Buckets = append(jg.Buckets, pluginapi.QuotaBucket{Window: j.Job, RemainingFraction: 1, ResetTime: j.At.Format(time.RFC3339), Description: j.Result})
		}
		out.Groups = append(out.Groups, jg)
	}
	return out, nil
}

// jobRun is the last run of one job for one account.
type jobRun struct {
	Job    string    `json:"job"`
	At     time.Time `json:"at"`
	Result string    `json:"result"`
}

var runs = struct {
	sync.Mutex
	m map[string]map[string]jobRun
}{m: map[string]map[string]jobRun{}}

func noteRun(authID, job, result string) {
	runs.Lock()
	defer runs.Unlock()
	if runs.m[authID] == nil {
		runs.m[authID] = map[string]jobRun{}
	}
	runs.m[authID][job] = jobRun{Job: job, At: time.Now(), Result: result}
}

func jobStatus(authID string) []jobRun {
	runs.Lock()
	defer runs.Unlock()
	var out []jobRun
	for _, r := range runs.m[authID] {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Job < out[j].Job })
	return out
}

// managementRoutes exposes the jobs' status and a run-now trigger.
func managementRoutes() any {
	return map[string]any{"routes": []map[string]string{
		{"method": "GET", "path": "workbuddy-magpie/status", "description": "WorkBuddy jobs: last run and result per account"},
		{"method": "POST", "path": "workbuddy-magpie/run", "description": "Run the WorkBuddy jobs now"},
	}}
}

func managementHandle(req []byte) (any, error) {
	var r struct {
		Method string `json:"method"`
		Path   string `json:"path"`
	}
	_ = json.Unmarshal(req, &r)
	var body any
	switch {
	case r.Method == http.MethodPost:
		go runJobs(time.Now(), true)
		body = map[string]string{"status": "started"}
	default:
		runs.Lock()
		cp := map[string]map[string]jobRun{}
		for k, v := range runs.m {
			cp[k] = v
		}
		runs.Unlock()
		body = cp
	}
	b, _ := json.Marshal(body)
	return map[string]any{"StatusCode": 200, "Headers": map[string][]string{"Content-Type": {"application/json"}}, "Body": b}, nil
}

// --- scheduler ---

var sched struct {
	sync.Mutex
	stop chan struct{}
}

func startScheduler() {
	sched.Lock()
	defer sched.Unlock()
	if sched.stop != nil || !cfg().schedulerOn() {
		return
	}
	stop := make(chan struct{})
	sched.stop = stop
	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		runJobs(time.Now(), false)
		for {
			select {
			case <-stop:
				return
			case now := <-t.C:
				runJobs(now, false)
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
}

var checkedDay = struct {
	sync.Mutex
	m map[string]string
}{m: map[string]string{}}

// runJobs runs what is due: the China build's daily check-in, once a
// Beijing day per account (magpie's switch, off unless on).
func runJobs(now time.Time, force bool) {
	c := cfg()
	if !c.Checkin && !force {
		return
	}
	entries, err := authList()
	if err != nil {
		hostLog("warn", "workbuddy jobs: list auths", map[string]any{"error": err.Error()})
		return
	}
	day := wb.CheckinDay(now)
	for _, e := range entries {
		if e.Provider != wb.ProviderCN {
			continue
		}
		raw, _, err := authGet(e.AuthIndex)
		if err != nil {
			continue
		}
		site, a, err := accountFrom(e.Provider, e.ID, raw, nil)
		if err != nil || a.Source == "key" {
			continue
		}
		checkedDay.Lock()
		done := checkedDay.m[e.ID] == day
		checkedDay.Unlock()
		if done && !force {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		res, err := wb.CheckinOf(ctx, HostDoer{}, site, a, []time.Duration{2 * time.Second, 4 * time.Second})
		cancel()
		if err != nil {
			noteRun(e.ID, "checkin", "error: "+err.Error())
			continue
		}
		noteRun(e.ID, "checkin", fmt.Sprintf("%s %s", res.Outcome, res.Message))
		if res.Outcome == "done" || res.Outcome == "claimed" || res.Outcome == "inactive" {
			checkedDay.Lock()
			checkedDay.m[e.ID] = day
			checkedDay.Unlock()
		}
	}
}
