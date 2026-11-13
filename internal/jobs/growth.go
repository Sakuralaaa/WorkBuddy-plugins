package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Sakuralaaa/WorkBuddy-plugins/internal/wb"
)

// Task is one growth-centre task as the task list shows it
// (/v2/activity/growth/tasks, panel upstream/tasks.go and wb_tasks.py).
type Task struct {
	Code         string `json:"task_code"`
	Title        string `json:"title,omitempty"`
	Description  string `json:"description,omitempty"`
	TaskDesc     string `json:"task_desc,omitempty"`
	Credit       int64  `json:"credit,omitempty"`
	Energy       int64  `json:"energy,omitempty"`
	RewardBuddy  bool   `json:"reward_buddy,omitempty"`
	Type         string `json:"task_type,omitempty"`
	JumpURL      string `json:"jump_url,omitempty"`
	Locked       bool   `json:"locked,omitempty"`
	AcceptStatus string `json:"accept_status,omitempty"`
	Status       string `json:"status,omitempty"`
	Current      int64  `json:"current"`
	Target       int64  `json:"target"`
	Claimable    bool   `json:"claimable,omitempty"`
	Claimed      bool   `json:"claimed,omitempty"`
	// MP marks a task only the mini-program list issues.
	MP bool `json:"mp,omitempty"`
}

// Done says the task's progress reached its target.
func (t *Task) Done() bool {
	if t == nil {
		return false
	}
	return t.Claimed || t.AcceptStatus == "completed" || (t.Target > 0 && t.Current >= t.Target)
}

// ParseTasks reads data.tasks of the task list; progress comes either as a
// {current,target} object or flat.
func ParseTasks(data json.RawMessage) ([]Task, error) {
	var resp struct {
		Tasks []struct {
			Code         string          `json:"task_code"`
			Title        string          `json:"title"`
			Description  string          `json:"description"`
			TaskDesc     string          `json:"task_desc"`
			RewardCredit json.Number     `json:"reward_credit"`
			RewardEnergy json.Number     `json:"reward_energy"`
			RewardBuddy  bool            `json:"reward_buddy"`
			TaskType     string          `json:"task_type"`
			JumpURL      string          `json:"jump_url"`
			Locked       bool            `json:"locked"`
			AcceptStatus string          `json:"accept_status"`
			Status       string          `json:"status"`
			Target       json.Number     `json:"target"`
			Current      json.Number     `json:"current"`
			Progress     json.RawMessage `json:"progress"`
		} `json:"tasks"`
	}
	if len(data) == 0 || string(data) == "null" {
		return nil, nil
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	num := func(n json.Number) int64 {
		if n == "" {
			return 0
		}
		if i, err := n.Int64(); err == nil {
			return i
		}
		f, _ := n.Float64()
		return int64(f)
	}
	out := make([]Task, 0, len(resp.Tasks))
	for _, t := range resp.Tasks {
		if t.Code == "" {
			continue
		}
		cur, tgt := num(t.Current), num(t.Target)
		if len(t.Progress) > 0 && string(t.Progress) != "null" {
			var pr struct {
				Current json.Number `json:"current"`
				Target  json.Number `json:"target"`
			}
			if json.Unmarshal(t.Progress, &pr) == nil && (num(pr.Target) > 0 || num(pr.Current) > 0) {
				cur, tgt = num(pr.Current), num(pr.Target)
			}
		}
		status := t.AcceptStatus
		if status == "" {
			status = "not_accepted"
		}
		claimed := status == "claimed"
		out = append(out, Task{
			Code: t.Code, Title: t.Title, Description: t.Description, TaskDesc: t.TaskDesc,
			Credit: num(t.RewardCredit), Energy: num(t.RewardEnergy), RewardBuddy: t.RewardBuddy,
			Type: t.TaskType, JumpURL: t.JumpURL, Locked: t.Locked,
			AcceptStatus: status, Status: t.Status, Current: cur, Target: tgt,
			Claimable: !claimed && ((tgt > 0 && cur >= tgt) || status == "completed"),
			Claimed:   claimed,
		})
	}
	return out, nil
}

// mpHeaders mark a request as the WeChat mini program's (wb_tasks.MP_HEADERS):
// the mini-program-only tasks are listed, accepted and claimed only with it.
func mpHeaders(h http.Header) {
	h.Set("X-Client-Platform", "miniprogram")
	h.Set("User-Agent", "Mozilla/5.0 (Linux; Android 14; MicroMessenger/8.0.49 WeChat/0.8.0 MiniProgramEnv/android; wkbrowser xweb)")
}

// growth sends a growth-centre request on the chat host with the desktop
// identity's headers and reads its envelope; a non-zero code is an error.
func (c *Client) growth(ctx context.Context, method, path string, body any, mp bool) (*envelope, error) {
	h := c.chatHeaders()
	if mp {
		mpHeaders(h)
	}
	status, env, err := c.call(ctx, method, c.chatBase()+path, h, body)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return env, &Error{Status: status, Msg: env.why()}
	}
	if !env.okOr200() && env.codeText() != "" {
		return env, &Error{Status: status, Msg: env.why()}
	}
	return env, nil
}

// Error is an upstream refusal: its HTTP status and what it said.
type Error struct {
	Status int
	Msg    string
}

func (e *Error) Error() string {
	if e.Status != 0 && e.Status != 200 {
		return fmt.Sprintf("HTTP %d: %s", e.Status, e.Msg)
	}
	return e.Msg
}

// ListTasks reads the growth task list (mp: the mini program's list, a
// superset holding its own tasks too).
func (c *Client) ListTasks(ctx context.Context, mp bool) ([]Task, error) {
	env, err := c.growth(ctx, http.MethodGet, "/v2/activity/growth/tasks", nil, mp)
	if err != nil {
		return nil, err
	}
	list, err := ParseTasks(env.Data)
	if err != nil {
		return nil, err
	}
	if mp {
		for i := range list {
			list[i].MP = MPTask(list[i].Code)
		}
	}
	return list, nil
}

// TaskByCode reads one task, from the mini program's list for its own tasks.
func (c *Client) TaskByCode(ctx context.Context, code string) (*Task, error) {
	list, err := c.ListTasks(ctx, MPTask(code))
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].Code == code {
			return &list[i], nil
		}
	}
	return nil, nil
}

// waitTask polls a task until it is done or the bounded wait runs out
// (scoring is asynchronous: a few seconds after the event).
func (c *Client) waitTask(ctx context.Context, code string, tries int, gap float64) *Task {
	var last *Task
	for i := 0; i < tries; i++ {
		if i > 0 || gap > 0 {
			if pause(ctx, gap) != nil {
				return last
			}
		}
		t, err := c.TaskByCode(ctx, code)
		if err != nil || t == nil {
			continue
		}
		last = t
		if t.Done() {
			return t
		}
	}
	return last
}

// Accept accepts tasks; it reports the codes the server took and those it
// did not, with why. For mini-program tasks it verifies the accept by
// re-reading accept_status afterwards (panel's acceptWithVerifyMP): the
// upstream has a "200 OK but not actually registered" form, where every
// event sent for the task afterwards goes uncounted. A code still
// not_accepted after the re-read is accepted once more, and only counted
// as accepted when the re-read confirms it.
func (c *Client) Accept(ctx context.Context, codes []string, mp bool) (accepted, failed []string, why string) {
	accepted, failed, why = c.acceptOnce(ctx, codes, mp)
	if !mp || len(accepted) == 0 {
		return accepted, failed, why
	}
	verified, unverified := c.verifyAccepted(ctx, accepted)
	if len(unverified) == 0 {
		return verified, failed, why
	}
	acc2, fail2, why2 := c.acceptOnce(ctx, unverified, mp)
	verified2, still := c.verifyAccepted(ctx, acc2)
	accepted = append(verified, verified2...)
	failed = append(failed, fail2...)
	failed = append(failed, still...)
	if len(still) > 0 {
		why = "接取后回读仍为未接取（上游未登记）: " + joinCodes(still)
	} else if why2 != "" {
		why = why2
	}
	return accepted, failed, why
}

func joinCodes(codes []string) string {
	out := ""
	for i, c := range codes {
		if i > 0 {
			out += ", "
		}
		out += c
	}
	return out
}

// verifyAccepted re-reads each accepted code and splits them into the ones
// the server actually registered and the ones still not_accepted.
func (c *Client) verifyAccepted(ctx context.Context, codes []string) (verified, unverified []string) {
	for _, code := range codes {
		t, err := c.TaskByCode(ctx, code)
		if err == nil && t != nil && t.AcceptStatus != "not_accepted" {
			verified = append(verified, code)
		} else {
			unverified = append(unverified, code)
		}
	}
	return verified, unverified
}

func (c *Client) acceptOnce(ctx context.Context, codes []string, mp bool) (accepted, failed []string, why string) {
	for i := 0; i < len(codes); i += 20 {
		end := i + 20
		if end > len(codes) {
			end = len(codes)
		}
		part := codes[i:end]
		env, err := c.growth(ctx, http.MethodPost, "/v2/activity/growth/tasks/accept", map[string]any{"task_codes": part}, mp)
		if err != nil {
			failed = append(failed, part...)
			why = err.Error()
			continue
		}
		var data struct {
			Results []struct {
				Code    string `json:"task_code"`
				Status  string `json:"status"`
				Message string `json:"message"`
			} `json:"results"`
		}
		env.data(&data)
		if len(data.Results) == 0 {
			accepted = append(accepted, part...)
			continue
		}
		seen := map[string]bool{}
		for _, r := range data.Results {
			seen[r.Code] = true
			if r.Status == "accepted" || r.Status == "already_accepted" {
				accepted = append(accepted, r.Code)
			} else {
				failed = append(failed, r.Code)
				if r.Message != "" {
					why = r.Code + ": " + r.Message
				} else {
					why = r.Code + ": " + r.Status
				}
			}
		}
		for _, code := range part {
			if !seen[code] {
				failed = append(failed, code)
			}
		}
	}
	return
}

// Claim claims a task's reward: the chat host's claim, then the web growth
// centre's when that says 400 (wb_tasks.claim_task, panel ClaimRewardMP).
func (c *Client) Claim(ctx context.Context, code string, mp bool) (credit, energy int64, err error) {
	path := "/activity/growth/tasks/" + url.PathEscape(code) + "/claim"
	env, err := c.growth(ctx, http.MethodPost, path, map[string]any{}, mp)
	if err != nil {
		var e *Error
		if ue, ok := err.(*Error); ok {
			e = ue
		}
		if e == nil || e.Status != http.StatusBadRequest {
			return 0, 0, err
		}
		h := c.webHeaders()
		h.Set("Referer", c.webBase()+"/profile/growth-center")
		h.Set("x-client-platform", "web")
		status, wenv, werr := c.call(ctx, http.MethodPost, c.webBase()+path, h, map[string]any{})
		if werr != nil {
			return 0, 0, werr
		}
		if status < 200 || status >= 300 || !wenv.ok() {
			return 0, 0, &Error{Status: status, Msg: wenv.why()}
		}
		env = wenv
	}
	var got struct {
		Already bool    `json:"already_claimed"`
		Credit  float64 `json:"credit"`
		Energy  float64 `json:"energy"`
	}
	env.data(&got)
	if got.Already {
		return 0, 0, nil
	}
	return int64(got.Credit), int64(got.Energy), nil
}

// webBase is the build's web console.
func (c *Client) webBase() string {
	if c.Site != nil && c.Site.AI {
		return IntlBase
	}
	return WebBase
}

// webHeaders are a browser's on the web console (bearer and user only).
func (c *Client) webHeaders() http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+c.A.Access)
	h.Set("Accept", "application/json, text/plain, */*")
	h.Set("Content-Type", "application/json")
	h.Set("Origin", c.webBase())
	h.Set("User-Agent", webUA)
	h.Set("X-User-Id", c.A.UID)
	wb.InjectDeviceToken(h, c.A.DeviceToken, c.DeviceToken, "")
	if c.A.EnterpriseID != "" {
		h.Set("X-Enterprise-Id", c.A.EnterpriseID)
		h.Set("X-Tenant-Id", c.A.EnterpriseID)
	}
	return h
}

// GrowthSummary is the growth centre's state shown with an account's tasks.
type GrowthSummary struct {
	Energy      float64           `json:"energy"`
	StreakDays  int               `json:"streak_days"`
	MonthDays   int               `json:"month_days,omitempty"`
	MakeupCards int               `json:"makeup_cards"`
	Lottery     int               `json:"lottery_chances"`
	Tiers       []StreakTier      `json:"tiers,omitempty"`
	TierStatus  map[string]string `json:"tier_status,omitempty"`
	Travel      *TravelState      `json:"travel,omitempty"`
	HasBuddy    *bool             `json:"has_buddy,omitempty"`
	Errors      []string          `json:"errors,omitempty"`
}

// Summary reads energy, streak, lottery and travel, each best effort.
func (c *Client) Summary(ctx context.Context) *GrowthSummary {
	out := &GrowthSummary{}
	if env, err := c.growth(ctx, http.MethodGet, "/v2/activity/growth/energy", nil, false); err == nil {
		var d struct {
			Balance float64 `json:"balance"`
		}
		env.data(&d)
		out.Energy = d.Balance
	} else {
		out.Errors = append(out.Errors, "能量: "+err.Error())
	}
	if s, err := c.Streak(ctx); err == nil {
		out.StreakDays = s.Streak.Days
		out.MonthDays = s.Streak.MonthTotalDays
		out.MakeupCards = s.MakeupCards.Balance
		out.Tiers = s.Redemption.Tiers
		out.TierStatus = s.TierStatus()
	} else {
		out.Errors = append(out.Errors, "连登: "+err.Error())
	}
	if n, err := c.LotteryChances(ctx); err == nil {
		out.Lottery = n
	}
	if t, err := c.TravelStatus(ctx); err == nil {
		out.Travel = t
	}
	return out
}

// sleepGap is the anti-abuse spacing between upstream writes (wb2a's >= 1s).
func (c *Client) sleepGap(ctx context.Context) error { return pause(ctx, 1.05) }

var _ = time.Second
var _ = strings.TrimSpace
