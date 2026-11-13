package jobs

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// RunTaskSweep is panel's tasksweep (internal/panel/tasksweep.go): accept
// and claim only, no behaviour chains, and no whitelist — it claims any
// task whose progress reached its target. That is the half the growth job
// misses: a task completed by hand in the official client, or by ordinary
// chatting after the growth hour, or a task the growth job has no action
// for, would otherwise sit unclaimed until the next day's growth run (or
// forever). Judgement uses progress-based Claimable only, never
// accept_status == "completed": that status flips to completed and rolls
// back, and claiming on it was the old false-claim path.
func (c *Client) RunTaskSweep(ctx context.Context, claim bool) Result {
	r := Result{Job: "sweep", At: time.Now(), OK: true, Outcome: "done"}
	if c.Key() || c.Enterprise() || c.Site == nil || c.Site.AI {
		r.Outcome, r.Message = "skipped", "补领清扫只适用于国内版个人账号"
		return r
	}
	byCode := map[string]*Task{}
	for _, mp := range []bool{false, true} {
		list, err := c.ListTasks(ctx, mp)
		if err != nil {
			r.logf("任务列表读取失败 (%s): %v", map[bool]string{false: "默认", true: "小程序"}[mp], err)
			continue
		}
		for i := range list {
			t := list[i]
			if old, ok := byCode[t.Code]; !ok {
				cp := t
				byCode[t.Code] = &cp
			} else if t.MP && !old.MP {
				// The mini-program list is the superset; keep its copy so
				// MP-only tasks are accepted and claimed with the MP header.
				cp := t
				byCode[t.Code] = &cp
			}
		}
	}
	if len(byCode) == 0 {
		r.Outcome, r.Message = "failed", "两个任务列表都读取失败"
		return r
	}
	codes := make([]string, 0, len(byCode))
	for code := range byCode {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	// Batch-accept what is not accepted yet, MP and default submitted
	// separately (an MP task accepted without the MP header is not
	// registered at all).
	for _, mp := range []bool{false, true} {
		var accept []string
		for _, code := range codes {
			t := byCode[code]
			if t.AcceptStatus == "not_accepted" && !t.Locked && MPTask(code) == mp {
				accept = append(accept, code)
			}
		}
		if len(accept) == 0 {
			continue
		}
		ok, failed, why := c.Accept(ctx, accept, mp)
		r.logf("接取 %d 个任务，成功 %d", len(accept), len(ok))
		if len(failed) > 0 {
			r.logf("接取未成功 %d 个：%s", len(failed), why)
		}
		_ = c.sleepGap(ctx)
	}
	// Re-read, then claim everything progress-Claimable.
	var claimed, credits, energy int
	for _, mp := range []bool{false, true} {
		list, err := c.ListTasks(ctx, mp)
		if err != nil {
			r.logf("领奖前重读任务列表失败: %v", err)
			continue
		}
		for i := range list {
			t := list[i]
			// Progress only: accept_status == "completed" flips to
			// completed and rolls back, so it is not a claim signal.
			if MPTask(t.Code) != mp || t.Claimed || !(t.Target > 0 && t.Current >= t.Target) {
				continue
			}
			if !claim {
				r.logf("「%s」已达成（%s），领奖开关关闭，等待手动领取", firstNonEmpty(t.Title, t.Code), progressText(&t))
				continue
			}
			cr, en, err := c.Claim(ctx, t.Code, mp)
			if err != nil {
				r.logf("「%s」领奖失败: %v", firstNonEmpty(t.Title, t.Code), err)
				continue
			}
			claimed++
			credits += int(cr)
			energy += int(en)
			r.Credits += float64(cr)
			r.logf("「%s」已领奖 +%d 积分 +%d 能量", firstNonEmpty(t.Title, t.Code), cr, en)
		}
	}
	if claimed > 0 {
		r.Outcome = "claimed"
		r.Message = fmt.Sprintf("补领 %d 个任务，+%d 积分 +%d 能量", claimed, credits, energy)
	} else {
		r.Message = "没有可补领的任务"
	}
	return r
}
