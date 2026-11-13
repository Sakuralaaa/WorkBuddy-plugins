package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// CampaignTask is one task of an activity campaign.
type CampaignTask struct {
	Task       string `json:"task"`
	Label      string `json:"label"`
	Kind       string `json:"kind"` // chat, expert, skill, desktop, app (a real phone only)
	Times      int    `json:"times,omitempty"`
	Credit     int    `json:"credit,omitempty"`
	ExpertID   string `json:"expert_id,omitempty"`
	ExpertName string `json:"expert_name,omitempty"`
	SkillID    string `json:"skill_id,omitempty"`
	SkillName  string `json:"skill_name,omitempty"`
	CaseID     string `json:"case_id,omitempty"`
}

// Campaign is an activity campaign (/portal/activity/*), apart from the
// growth list (wb_tasks.CAMPAIGN_SPECS).
type Campaign struct {
	Code  string         `json:"code"`
	Label string         `json:"label"`
	Page  map[string]any `json:"page"`
	Tasks []CampaignTask `json:"tasks"`
}

// Campaigns are the campaigns the plugin knows how to run.
var Campaigns = []Campaign{
	{
		Code: "campaign_starbucks_2026", Label: "星巴克协作",
		Page: map[string]any{"pageURL": "subpackages/secondary/starbucks-activity-428/index", "pageName": "starbucks_activity"},
		Tasks: []CampaignTask{
			{Task: "ai_chat", Label: "与 AI 对话 3 次", Kind: "chat", Times: 3, Credit: 100},
			{Task: "expert", Label: "召唤专家并对话", Kind: "expert", Credit: 100},
			{Task: "app", Label: "解锁 App 强大能力（需手机 App 真实对话）", Kind: "app", Credit: 100},
		},
	},
	{
		Code: "campaign_reporter_2026", Label: "记者节福利活动",
		Page: map[string]any{"pageURL": "subpackages/journalist-benefit/index", "pageName": "journalist_benefit"},
		Tasks: []CampaignTask{
			{Task: "expert_chat", Label: "体验「媒体工作助理」专家", Kind: "expert", ExpertID: "ex_NaiDbO06Adf3", ExpertName: "媒体工作助理", Credit: 50},
			{Task: "skill_use", Label: "体验「采访素材整理」技能", Kind: "skill", SkillID: "skill_2117491159283752960", SkillName: "interview-material-organizer", Credit: 50},
			{Task: "desktop_same", Label: "体验「内容发布排期」灵感", Kind: "desktop", CaseID: "content-calendar", Credit: 100},
		},
	},
}

// CampaignRow is one campaign task's state.
type CampaignRow struct {
	Task      string `json:"task"`
	Target    int    `json:"target"`
	Progress  int    `json:"progress"`
	Completed bool   `json:"completed"`
	Claimed   bool   `json:"claimed"`
	NeedView  bool   `json:"need_view"`
	Credit    int    `json:"credit"`
}

func (c *Client) campaignCall(ctx context.Context, method, path string, body any) (int, *envelope, error) {
	h := c.mpReportHeaders()
	if method == http.MethodGet {
		body = nil
	} else if body == nil {
		body = map[string]any{}
	}
	return c.call(ctx, method, CampaignBase+path, h, body)
}

// CampaignTasks reads a campaign's tasks and progress; an error is a read
// failure, an empty map a campaign not issued (or over).
func (c *Client) CampaignTasks(ctx context.Context, campaign string) (map[string]CampaignRow, error) {
	status, env, err := c.campaignCall(ctx, http.MethodGet, "/portal/activity/tasks?activity_code="+url.QueryEscape(campaign), nil)
	if err != nil {
		return nil, err
	}
	if status != 200 || (env.codeText() != "" && !env.ok()) {
		return nil, &Error{Status: status, Msg: env.why()}
	}
	var d struct {
		Tasks []struct {
			Code     string  `json:"task_code"`
			Target   float64 `json:"target_count"`
			NeedView *bool   `json:"need_view"`
			Credit   float64 `json:"reward_credit"`
		} `json:"tasks"`
		Progress []struct {
			Code        string          `json:"task_code"`
			Progress    float64         `json:"progress"`
			CompletedAt json.RawMessage `json:"completed_at"`
			ClaimedAt   json.RawMessage `json:"claimed_at"`
		} `json:"progress"`
	}
	env.data(&d)
	set := func(raw json.RawMessage) bool {
		s := strings.TrimSpace(string(raw))
		return s != "" && s != "null" && s != `""` && s != "0"
	}
	prog := map[string]CampaignRow{}
	for _, p := range d.Progress {
		prog[p.Code] = CampaignRow{Progress: int(p.Progress), Completed: set(p.CompletedAt), Claimed: set(p.ClaimedAt)}
	}
	out := map[string]CampaignRow{}
	for _, t := range d.Tasks {
		if t.Code == "" {
			continue
		}
		row := prog[t.Code]
		row.Task, row.Target, row.Credit = t.Code, int(t.Target), int(t.Credit)
		row.NeedView = t.NeedView == nil || *t.NeedView
		out[t.Code] = row
	}
	return out, nil
}

func (c *Client) campaignPath(action, campaign, task string) string {
	return fmt.Sprintf("/portal/activity/tasks/%s/%s?activity_code=%s", url.PathEscape(task), action, url.QueryEscape(campaign))
}

func (c *Client) campaignPoll(ctx context.Context, campaign, task string, target, tries int, gap float64) (int, bool) {
	cur := 0
	for i := 0; i < tries; i++ {
		if pause(ctx, gap) != nil {
			break
		}
		rows, err := c.CampaignTasks(ctx, campaign)
		if err != nil {
			continue
		}
		row := rows[task]
		cur = row.Progress
		if row.Completed || cur >= target {
			return target, true
		}
	}
	return cur, false
}

func campaignChatEvent() Event {
	conv, req := uuid4(), messageID()
	ev := MPChatEvent(conv, req)
	return ev
}

func campaignExpertEvents(id, name string) []Event {
	conv, req := uuid4(), messageID()
	const cat = "16-BackToSchool"
	chat := MPChatEvent(conv, req)
	chat["inputLength"] = 14
	chat["expertId"] = id
	chat["expertName"] = name
	delete(chat, "mode")
	return []Event{
		mk("expert_summon_click", map[string]any{"id": id, "name": id, "expertTitle": name, "type": cat, "position": 0}),
		mk("expert_summoned", map[string]any{"id": id, "name": id, "expertTitle": name}),
		mk("expert_actual_use", map[string]any{"id": id, "name": id, "expertTitle": name, "type": cat, "characterCount": 14, "expertType": "builtin"}),
		chat,
	}
}

func campaignSkillEvents(id, name string) []Event {
	conv, req := uuid4(), messageID()
	chat := MPChatEvent(conv, req)
	chat["inputLength"] = 14
	chat["skillId"] = id
	chat["skillCount"] = 1
	delete(chat, "mode")
	return []Event{
		mk("skill_info", map[string]any{"id": name, "skillId": id, "skillName": name, "skillType": "market", "skillSource": "market", "position": 0, "conversationId": conv}),
		chat,
	}
}

var campaignCases = map[string]Event{
	"content-calendar": {"id": "cnb-content-calendar", "name": "内容发布排期表", "type": "table", "promptLength": 359},
}

func (c *Client) campaignFire(ctx context.Context, cp Campaign, t CampaignTask) error {
	switch t.Kind {
	case "desktop":
		cs, ok := campaignCases[t.CaseID]
		if !ok {
			return fmt.Errorf("未内置案例 %s", t.CaseID)
		}
		ev := mk("playbook_prompt_send", map[string]any{"isOfficial": 1, "source": "discover", "query": "", "ext1": "discover", "requestId": messageID(), "conversationId": uuid4()})
		for k, v := range cs {
			ev[k] = v
		}
		return c.ReportDesktop(ctx, ev)
	case "chat":
		return c.ReportMP(ctx, CampaignBase, cp.Page, campaignChatEvent())
	case "expert":
		id, name := t.ExpertID, t.ExpertName
		if id == "" {
			list, err := c.Experts(ctx, "agent")
			if err == nil && len(list) > 0 {
				id, name = list[0].ID, list[0].Name
			} else {
				id, name = "WorkspaceBuilder", "工作台搭建师"
			}
		}
		return c.ReportMP(ctx, CampaignBase, cp.Page, campaignExpertEvents(id, name)...)
	case "skill":
		return c.ReportMP(ctx, CampaignBase, cp.Page, campaignSkillEvents(t.SkillID, t.SkillName)...)
	}
	return fmt.Errorf("需要真人完成")
}

// campaign poll budget (tests shorten)
var (
	campaignGap  = 3.0
	campaignPoll = 6
)

func (c *Client) campaignClaim(ctx context.Context, cp Campaign, task string) error {
	status, env, err := c.campaignCall(ctx, http.MethodPost, c.campaignPath("claim", cp.Code, task), nil)
	if err != nil {
		return err
	}
	if status != 200 || !env.ok() {
		return &Error{Status: status, Msg: env.why()}
	}
	for i := 0; i < 5; i++ {
		if i > 0 && pause(ctx, 4*campaignGap/3) != nil {
			break
		}
		if rows, err := c.CampaignTasks(ctx, cp.Code); err == nil && rows[task].Claimed {
			return nil
		}
	}
	return fmt.Errorf("已提交领奖但未回读到领取记录，下轮重试")
}

// RunCampaigns runs the issued campaign tasks and claims what they earn
// (wb_tasks.run_campaign_tasks); the phone-app task is left to a person.
func (c *Client) RunCampaigns(ctx context.Context, claim bool) Result {
	r := Result{Job: "campaigns", At: time.Now(), OK: true, Outcome: "done"}
	if c.Key() || c.Enterprise() || c.Site == nil || c.Site.AI {
		r.Outcome, r.Message = "skipped", "运营活动只适用于国内版个人账号"
		return r
	}
	issued := 0
	for _, cp := range Campaigns {
		rows, err := c.CampaignTasks(ctx, cp.Code)
		if err != nil {
			r.logf("%s：读取失败 %v", cp.Label, err)
			r.OK = false
			continue
		}
		if len(rows) == 0 {
			r.logf("%s：未下发或已结束", cp.Label)
			continue
		}
		issued++
		for _, t := range cp.Tasks {
			row, ok := rows[t.Task]
			if !ok {
				r.logf("%s / %s：未下发", cp.Label, t.Label)
				continue
			}
			target := row.Target
			if target <= 0 {
				target = t.Times
			}
			if target <= 0 {
				target = 1
			}
			credit := row.Credit
			if credit == 0 {
				credit = t.Credit
			}
			label := cp.Label + " / " + t.Label
			if row.Claimed {
				r.logf("%s：已领奖", label)
				continue
			}
			done := row.Completed || row.Progress >= target
			if !done {
				if t.Kind == "app" {
					msg := label + "：需在手机 App 登录后与 AI 对话一次，不会伪造；完成后重跑会自动领奖"
					r.Pending = append(r.Pending, msg)
					r.logf("%s", msg)
					continue
				}
				if row.NeedView {
					_, _, _ = c.campaignCall(ctx, http.MethodPost, c.campaignPath("viewed", cp.Code, t.Task), nil)
				}
				cur := row.Progress
				if t.Kind == "desktop" {
					if err := c.campaignFire(ctx, cp, t); err != nil {
						r.logf("%s：上报失败 %v", label, err)
						continue
					}
					cur, done = c.campaignPoll(ctx, cp.Code, t.Task, target, 10, 2*campaignGap)
				} else {
					for i := 0; i < target-row.Progress && !done; i++ {
						if err := c.campaignFire(ctx, cp, t); err != nil {
							r.logf("%s：上报失败 %v", label, err)
							break
						}
						cur, done = c.campaignPoll(ctx, cp.Code, t.Task, target, campaignPoll, campaignGap)
					}
					if !done {
						cur, done = c.campaignPoll(ctx, cp.Code, t.Task, target, campaignPoll, campaignGap*10/3)
					}
				}
				if !done {
					r.logf("%s：判据未落库（%d/%d），下轮自动补领", label, cur, target)
					continue
				}
			}
			if !claim {
				r.logf("%s：已达成，领奖开关关闭", label)
				continue
			}
			if err := c.campaignClaim(ctx, cp, t.Task); err != nil {
				r.OK = false
				r.logf("%s：领奖失败 %v", label, err)
				continue
			}
			r.Credits += float64(credit)
			r.logf("%s：领奖 +%d 积分", label, credit)
		}
	}
	if issued == 0 {
		r.Outcome = "skipped"
		r.Message = "当前没有下发的运营活动"
		return r
	}
	if r.Credits > 0 {
		r.Outcome = "claimed"
	}
	if !r.OK {
		r.Outcome = "partial"
	}
	r.Message = fmt.Sprintf("运营活动 %d 个，本次 +%.0f 积分", issued, r.Credits)
	return r
}

// ---- skill share (skill_share) --------------------------------------------------------

// MarketSkill is a skill of the skill market.
type MarketSkill struct {
	ID, Name, Description string
}

// MarketSkill reads a shareable market skill, the measured one first.
func (c *Client) MarketSkill(ctx context.Context) (MarketSkill, error) {
	status, env, err := c.call(ctx, http.MethodPost, BillBase+"/v2/operation-platform/market/skill/list", c.chatHeaders(), map[string]any{"page": 1, "page_size": 20})
	if err != nil {
		return MarketSkill{}, err
	}
	if status < 200 || status >= 300 || !env.okOr200() {
		return MarketSkill{}, &Error{Status: status, Msg: env.why()}
	}
	var d struct {
		Skills []map[string]any `json:"skills"`
	}
	env.data(&d)
	str := func(m map[string]any, keys ...string) string {
		for _, k := range keys {
			if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
		return ""
	}
	var list []MarketSkill
	for _, row := range d.Skills {
		s := MarketSkill{ID: str(row, "skill_id", "id"), Name: str(row, "display_name_zh", "name", "display_name_en"), Description: str(row, "description_zh", "description", "description_en")}
		if s.ID == "" || s.Name == "" {
			continue
		}
		if r := []rune(s.Description); len(r) > 60 {
			s.Description = string(r[:60])
		}
		if s.ID == "skill_2097350077599879168" {
			return s, nil
		}
		list = append(list, s)
	}
	if len(list) == 0 {
		return MarketSkill{}, fmt.Errorf("技能市场没有可分享的技能，不伪造分享")
	}
	return list[0], nil
}

// ShareSkill creates a real share channel for a skill; its share code.
func (c *Client) ShareSkill(ctx context.Context, s MarketSkill) (string, error) {
	ref, _ := json.Marshal(map[string]string{"asset_id": s.ID})
	body := map[string]any{
		"asset_kind": "skill", "asset_source": "market", "asset_ref": string(ref),
		"ways":  []string{"link", "wxacode", "wxapath"},
		"extra": map[string]any{"name": s.Name, "description": s.Description, "shareKey": s.ID, "marketplaceSource": "builtin-market"},
	}
	status, env, err := c.call(ctx, http.MethodPost, OpenBase+"/api/v2/open-platform/asset-shares/c2c/channels", c.chatHeaders(), body)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 || !env.okOr200() {
		return "", fmt.Errorf("创建分享渠道失败: %s", (&Error{Status: status, Msg: env.why()}).Error())
	}
	var d struct {
		ShareCode string `json:"share_code"`
	}
	env.data(&d)
	if d.ShareCode == "" {
		return "", fmt.Errorf("分享接口没有返回 share_code")
	}
	return d.ShareCode, nil
}

// SkillShareEvents are the share panel's link actions for a share code.
func SkillShareEvents(id, name, code string, now int64) []Event {
	ev := func(action string) Event {
		return mk("skill_share_channel_action", map[string]any{
			"skillId": id, "skillName": name, "skillType": "market", "action": "link",
			"panelAction": action, "panelType": "clipboard", "isSuccessful": true,
			"cost": 0, "id": code, "actionAt": now,
		})
	}
	return []Event{ev("click"), ev("success")}
}
