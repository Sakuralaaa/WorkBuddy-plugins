package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ---- streak manager (连登管家) -------------------------------------------------

// StreakTier is one streak redemption tier.
type StreakTier struct {
	Tier    string `json:"tier"`
	Days    int    `json:"days"`
	Credit  int    `json:"credit"`
	Energy  int    `json:"energy"`
	Cards   int    `json:"cards"`
	Chances int    `json:"chances"`
}

// StreakState is GET /activity/growth/streak.
type StreakState struct {
	Streak struct {
		Days              int    `json:"days"`
		MonthTotalDays    int    `json:"month_total_days"`
		NextTier          string `json:"next_tier"`
		NextTierRemaining int    `json:"next_tier_remaining"`
	} `json:"streak"`
	MakeupCards struct {
		Balance int `json:"balance"`
		Max     int `json:"max"`
	} `json:"makeup_cards"`
	Redemption struct {
		Tier7  string       `json:"tier_7d_status"`
		Tier14 string       `json:"tier_14d_status"`
		Tier28 string       `json:"tier_28d_status"`
		Tiers  []StreakTier `json:"tiers"`
	} `json:"redemption_status"`
}

// TierStatus is each tier's status by name.
func (s *StreakState) TierStatus() map[string]string {
	return map[string]string{"7d": s.Redemption.Tier7, "14d": s.Redemption.Tier14, "28d": s.Redemption.Tier28}
}

// Redeemable are the tiers that are neither locked nor claimed.
func (s *StreakState) Redeemable() []StreakTier {
	st := s.TierStatus()
	var out []StreakTier
	for _, t := range s.Redemption.Tiers {
		v := st[t.Tier]
		if v == "locked" || v == "claimed" || v == "" {
			continue
		}
		out = append(out, t)
	}
	return out
}

// Streak reads the streak state.
func (c *Client) Streak(ctx context.Context) (*StreakState, error) {
	env, err := c.growth(ctx, http.MethodGet, "/activity/growth/streak", nil, false)
	if err != nil {
		return nil, err
	}
	s := &StreakState{}
	env.data(s)
	return s, nil
}

// LotteryChances reads the lottery draws left.
func (c *Client) LotteryChances(ctx context.Context) (int, error) {
	env, err := c.growth(ctx, http.MethodGet, "/activity/growth/lottery/summary", nil, false)
	if err != nil {
		return 0, err
	}
	var d struct {
		Chances int `json:"chances"`
	}
	env.data(&d)
	return d.Chances, nil
}

// YesterdayMissed reads the heatmap: was yesterday (Beijing) left empty.
func (c *Client) YesterdayMissed(ctx context.Context, now time.Time) (bool, string, error) {
	y := now.In(beijing).AddDate(0, 0, -1).Format("2006-01-02")
	env, err := c.growth(ctx, http.MethodGet, "/activity/growth/heatmap", nil, false)
	if err != nil {
		return false, y, err
	}
	var d struct {
		Cells []struct {
			Date  string  `json:"date"`
			Score float64 `json:"score"`
		} `json:"cells"`
	}
	env.data(&d)
	for _, cell := range d.Cells {
		if len(cell.Date) >= 10 && cell.Date[:10] == y {
			return cell.Score == 0, y, nil
		}
	}
	return false, y, nil
}

// billing is a POST to the billing host with the billing headers.
func (c *Client) billing(ctx context.Context, path string, body any) (*envelope, error) {
	base := BillBase
	if c.Site != nil && c.Site.AI {
		base = IntlBase
	}
	status, env, err := c.call(ctx, http.MethodPost, base+path, c.billingHeaders(), body)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 || !env.ok() {
		return env, &Error{Status: status, Msg: env.why()}
	}
	return env, nil
}

// Compensation is /billing/meter/compensation-status.
type Compensation struct {
	Active    bool    `json:"active"`
	Claimed   bool    `json:"claimed"`
	CreditNum float64 `json:"credit_num"`
	EndTime   string  `json:"end_time"`
}

// RunRewards is the streak manager (wb_tasks.run_streak_bonus, panel
// streak.go): a make-up card for a missed yesterday, the gift and
// compensation packs, every unlocked streak tier redeemed and every lottery
// draw used. Claims (gift, compensation, tiers, draws) only with claim on.
func (c *Client) RunRewards(ctx context.Context, now time.Time, claim bool) Result {
	r := Result{Job: "rewards", At: time.Now(), OK: true, Outcome: "done"}
	if c.Key() || c.Enterprise() || c.Site == nil || c.Site.AI {
		r.Outcome, r.Message = "skipped", "连登管家只适用于国内版个人账号"
		return r
	}
	var did []string
	// 0. make-up card
	if missed, day, err := c.YesterdayMissed(ctx, now); err != nil {
		r.logf("热力图读取失败: %v", err)
	} else if missed {
		if s, err := c.Streak(ctx); err == nil && s.MakeupCards.Balance > 0 {
			if _, err := c.growthPost(ctx, "/activity/growth/makeup-cards/use", map[string]any{"target_date": day}); err != nil {
				r.logf("补签 %s 失败: %v", day, err)
			} else {
				did = append(did, "补签 "+day)
			}
		} else {
			r.logf("昨日 %s 漏签，但没有补签卡", day)
		}
	}
	if !claim {
		r.logf("领奖开关关闭：礼包、补偿、档位兑换与抽奖留待手动")
	}
	if claim {
		// 1. gift pack, compensation (status first: it has a deadline)
		if env, err := c.billing(ctx, "/billing/meter/claim-gift", map[string]any{}); err == nil {
			var d struct {
				Credit float64 `json:"credit"`
			}
			env.data(&d)
			did = append(did, fmt.Sprintf("新手礼包 +%.0f", d.Credit))
			r.Credits += d.Credit
		}
		if env, err := c.billing(ctx, "/billing/meter/compensation-status", map[string]any{}); err != nil {
			r.logf("补偿状态查询失败: %v", err)
		} else {
			var st Compensation
			env.data(&st)
			if st.Active && !st.Claimed && st.CreditNum > 0 {
				if env2, err := c.billing(ctx, "/billing/meter/claim-compensation", map[string]any{}); err != nil {
					r.OK = false
					r.logf("补偿 %.0f 积分（截止 %s）领取失败: %v", st.CreditNum, st.EndTime, err)
				} else {
					var d struct {
						Credit float64 `json:"credit"`
					}
					env2.data(&d)
					if d.Credit == 0 {
						d.Credit = st.CreditNum
					}
					r.Credits += d.Credit
					did = append(did, fmt.Sprintf("活动补偿 +%.0f", d.Credit))
				}
			}
		}
		// 2. streak tiers
		if s, err := c.Streak(ctx); err != nil {
			r.logf("连登状态读取失败: %v", err)
		} else {
			r.logf("连续登录 %d 天，补签卡 %d 张", s.Streak.Days, s.MakeupCards.Balance)
			for _, t := range s.Redeemable() {
				if _, err := c.growthPost(ctx, "/activity/growth/redeem", map[string]any{"tier": t.Tier, "client_token": uuid4()}); err != nil {
					r.logf("兑换 %s 档失败: %v", t.Tier, err)
					continue
				}
				r.Credits += float64(t.Credit)
				did = append(did, fmt.Sprintf("兑换 %s 档（+%d 积分 +%d 能量 补签卡×%d 抽奖×%d）", t.Tier, t.Credit, t.Energy, t.Cards, t.Chances))
				_ = c.sleepGap(ctx)
			}
		}
		// 3. lottery
		if n, err := c.LotteryChances(ctx); err != nil {
			r.logf("抽奖次数读取失败: %v", err)
		} else {
			for i := 0; i < n; i++ {
				env, err := c.growthPost(ctx, "/activity/growth/lottery/draw", map[string]any{"client_token": uuid4()})
				if err != nil {
					r.logf("第 %d 抽失败: %v", i+1, err)
					break
				}
				did = append(did, "抽奖："+prizeText(env.Data))
				_ = c.sleepGap(ctx)
			}
		}
	}
	if len(did) == 0 {
		r.Message = "连登状态正常，无需补签/兑换，没有抽奖次数"
	} else {
		r.Outcome = "claimed"
		r.Message = strings.Join(did, "；")
	}
	return r
}

func prizeText(raw json.RawMessage) string {
	var d map[string]any
	if json.Unmarshal(raw, &d) == nil {
		for _, k := range []string{"prize_name", "name", "title"} {
			if s, ok := d[k].(string); ok && s != "" {
				return s
			}
		}
		if p, ok := d["prize"].(map[string]any); ok {
			for _, k := range []string{"name", "title", "prize_name"} {
				if s, ok := p[k].(string); ok && s != "" {
					return s
				}
			}
		}
	}
	return snippet(raw)
}

// ---- cat travel (猫猫旅行) ---------------------------------------------------------

// TravelState is /activity/growth/buddy/travel/status.
type TravelState struct {
	State             string  `json:"state"`
	DailyLimitReached bool    `json:"daily_limit_reached"`
	RecordID          int64   `json:"record_id"`
	RewardCredit      float64 `json:"reward_credit"`
	ArriveAt          string  `json:"arrive_at,omitempty"`
}

// TravelStatus reads the cat's travel.
func (c *Client) TravelStatus(ctx context.Context) (*TravelState, error) {
	env, err := c.growth(ctx, http.MethodGet, "/activity/growth/buddy/travel/status", nil, false)
	if err != nil {
		return nil, err
	}
	t := &TravelState{}
	env.data(t)
	return t, nil
}

// RunTravel moves the cat's travel one step (wb_tasks.do_cat_travel, panel
// scheduler/travel.go): claim an arrival (claim on), send an idle cat off
// (once a Beijing day), adopt the first Buddy when there is none; then open
// Buddy boxes with spare energy (do_buddy_open, claim on).
func (c *Client) RunTravel(ctx context.Context, claim bool) Result {
	r := Result{Job: "cat_travel", At: time.Now(), OK: true, Outcome: "done"}
	if c.Key() || c.Enterprise() || c.Site == nil || c.Site.AI {
		r.Outcome, r.Message = "skipped", "猫猫旅行只适用于国内版个人账号"
		return r
	}
	st, err := c.TravelStatus(ctx)
	if err != nil {
		r.OK, r.Outcome, r.Message = false, "failed", "旅行状态读取失败: "+err.Error()
		return r
	}
	var msgs []string
	switch st.State {
	case "arrived":
		if !claim {
			msgs = append(msgs, "猫猫已归来，领奖开关关闭，等待手动领取")
			break
		}
		env, err := c.growthPost(ctx, "/activity/growth/buddy/travel/claim", map[string]any{"record_id": st.RecordID})
		if err != nil {
			r.OK, r.Outcome = false, "failed"
			msgs = append(msgs, "旅行领奖失败: "+err.Error())
			break
		}
		var d struct {
			RewardCredit float64 `json:"reward_credit"`
		}
		env.data(&d)
		r.Credits += d.RewardCredit
		r.Outcome = "claimed"
		msgs = append(msgs, fmt.Sprintf("旅行归来领奖 +%.0f 积分", d.RewardCredit))
		_ = c.sleepGap(ctx)
		if st2, err := c.TravelStatus(ctx); err == nil {
			st = st2
		}
		if st.State == "idle" && !st.DailyLimitReached {
			msgs = append(msgs, c.depart(ctx, &r))
		}
	case "idle":
		if st.DailyLimitReached {
			msgs = append(msgs, "今天已旅行过，北京时间 0 点刷新")
		} else {
			msgs = append(msgs, c.depart(ctx, &r))
		}
	case "traveling":
		msgs = append(msgs, "猫猫正在旅行途中")
	default:
		msgs = append(msgs, "旅行状态: "+st.State)
	}
	if claim {
		if m := c.openBoxes(ctx, &r); m != "" {
			msgs = append(msgs, m)
		}
	}
	r.Message = strings.Join(msgs, "；")
	return r
}

func (c *Client) depart(ctx context.Context, r *Result) string {
	lid := any(4)
	if env, err := c.growth(ctx, http.MethodGet, "/activity/growth/buddy/travel/config", nil, false); err == nil {
		var d struct {
			Locations []struct {
				ID   any    `json:"id"`
				Name string `json:"name"`
			} `json:"locations"`
		}
		env.data(&d)
		if len(d.Locations) > 0 && d.Locations[0].ID != nil {
			lid = d.Locations[0].ID
		}
	}
	env, err := c.growthPost(ctx, "/activity/growth/buddy/travel/depart", map[string]any{"location_id": lid})
	if err != nil && containsFold(err.Error(), "no active buddy") {
		msg, aerr := c.AdoptBuddy(ctx)
		if aerr != nil {
			r.OK, r.Outcome = false, "failed"
			return "还没有 Buddy，领养失败: " + aerr.Error()
		}
		r.logf("%s", msg)
		env, err = c.growthPost(ctx, "/activity/growth/buddy/travel/depart", map[string]any{"location_id": lid})
	}
	if err != nil {
		r.OK, r.Outcome = false, "failed"
		return "派出旅行失败: " + err.Error()
	}
	var d struct {
		Location struct {
			Name string `json:"name"`
		} `json:"location"`
	}
	env.data(&d)
	if d.Location.Name != "" {
		return "猫猫已出发前往「" + d.Location.Name + "」"
	}
	return "猫猫已出发旅行"
}

func (c *Client) openBoxes(ctx context.Context, r *Result) string {
	env, err := c.growth(ctx, http.MethodGet, "/v2/activity/growth/buddy/quota", nil, false)
	if err != nil {
		return ""
	}
	var q struct {
		Affordable int `json:"affordable"`
		MaxOpen    int `json:"max_open_count"`
	}
	env.data(&q)
	if q.Affordable <= 0 {
		return ""
	}
	n := q.Affordable
	if q.MaxOpen > 0 && n > q.MaxOpen {
		n = q.MaxOpen
	}
	env, err = c.growthPost(ctx, "/v2/activity/growth/buddy/open", map[string]any{"count": n, "client_token": uuid4()})
	if err != nil {
		r.logf("开 Buddy 盲盒失败: %v", err)
		return ""
	}
	var d map[string]any
	env.data(&d)
	name := ""
	for _, k := range []string{"buddy", "name", "buddies"} {
		if v, ok := d[k]; ok && v != nil {
			name = fmt.Sprint(v)
			break
		}
	}
	return fmt.Sprintf("用能量开 Buddy 盲盒 ×%d %s", n, name)
}

// ---- activity report (对话活跃上报) ----------------------------------------------

// RunActivityReport sends the day's chat_request_send (lights the growth
// streak), then reads the streak back.
func (c *Client) RunActivityReport(ctx context.Context) Result {
	r := Result{Job: "activity_report", At: time.Now()}
	if c.Key() || c.Enterprise() {
		r.OK, r.Outcome, r.Message = true, "skipped", "API Key / 企业账号没有成长体系"
		return r
	}
	if err := c.ReportChat(ctx, ChatSendEvent(c.A.UID, "deepseek-v4-flash", "DeepSeek V4 Flash", "craft", 0, nowMS())); err != nil {
		r.Outcome, r.Message = "failed", "活跃上报被拒绝: "+err.Error()
		return r
	}
	r.OK, r.Outcome = true, "done"
	_ = pause(ctx, 2)
	if s, err := c.Streak(ctx); err == nil {
		if s.Streak.Days <= 0 {
			r.Message = "活跃上报成功，但连登仍是 0 天（可能被静默丢弃）"
		} else {
			r.Message = fmt.Sprintf("活跃上报成功，连续登录 %d 天", s.Streak.Days)
		}
	} else {
		r.Message = "活跃上报成功（连登天数未知）"
	}
	return r
}

// ---- night owl (夜猫子) -------------------------------------------------------------

// RunNight runs the night-owl task when inside its window.
func (c *Client) RunNight(ctx context.Context, claim bool) Result {
	r := c.RunGrowthTask(ctx, "black_cat", claim)
	r.Job = "night"
	return r
}
