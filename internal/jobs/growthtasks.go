package jobs

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"sort"
	"time"
)

// Action is how one growth task is completed by API.
type Action struct {
	Code string `json:"task_code"`
	Name string `json:"name"`
	// How is what the action does, as the task center shows it.
	How string `json:"how"`
	// Chat says it spends real chats (a few short ones).
	Chat bool `json:"chat,omitempty"`
	// Night says it counts only 23:00–08:00 Beijing time.
	Night bool `json:"night,omitempty"`
	// MP says it is a mini-program task.
	MP  bool `json:"mp,omitempty"`
	run func(ctx context.Context, c *Client, t *Task, r *Result) error
}

// Human is a task only a real person can complete; it is shown with what it
// takes and never faked.
type Human struct {
	Code   string `json:"task_code"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// HumanTasks are the tasks this plugin leaves to a person.
var HumanTasks = []Human{
	{"Expert_Philanthropy", "公益爱心捐赠", "需要真实捐款，领奖时服务端校验捐赠回执，不会自动化"},
	{"wb_wechat_oa_subscribe_task", "关注官方公众号", "需在微信关注「腾讯 WorkBuddy」公众号满 24 小时"},
}

// HumanFor is the human-only entry of a code, nil for none.
func HumanFor(code string) *Human {
	for i := range HumanTasks {
		if HumanTasks[i].Code == code {
			return &HumanTasks[i]
		}
	}
	return nil
}

// mpCodes are the mini program's own tasks (issued only on its list).
var mpCodes = map[string]bool{
	"school_season": true, "Sequential_Tasks_1": true, "Sequential_Tasks_2": true,
	"Sequential_Tasks_3": true, "Sequential_Tasks_4": true, "Sequential_Tasks_5": true,
	"Sequential_Tasks_6": true, "Sequential_Tasks_7": true,
}

// MPTask says code is a mini-program task.
func MPTask(code string) bool { return mpCodes[code] }

// Actions are the growth tasks done by API, in the order a full run takes
// them (first_buddy and the chat count first: later tasks depend on them).
var Actions = []Action{
	{Code: "chat_5", Name: "发起 5 次对话", How: "上报对话活跃事件，补足差额", run: runChat5},
	{Code: "first_buddy", Name: "领取一只 Buddy", How: "活跃上报 → 同意协议 → 领养第一只 Buddy", run: runFirstBuddy},
	{Code: "Model_chat_GLM5.2", Name: "体验 GLM-5.2", How: "用 glm-5.2 真实对话一次并上报", Chat: true, run: runModelChat},
	{Code: "RichMeow_Chat", Name: "桌面端对话 1 次", How: "桌面端成功对话事件链", run: runRichMeow},
	{Code: "Buddy_App", Name: "进入 Buddy 应用", How: "Buddy 应用进入事件链（未点亮时请在桌面端「发现应用」进入任一应用）", run: runBuddyApp},
	{Code: "Buddy_App_QQ", Name: "企鹅教师助手", How: "企鹅教师助手进入事件链（未点亮时请在桌面端进入「企鹅教师助手」）", run: runBuddyApp},
	{Code: "automation_1", Name: "创建自动化任务", How: "定时任务创建成功事件", run: runAutomation},
	{Code: "Library_read", Name: "浏览资料库", How: "资料库介绍阅读事件（Web 端）", run: runLibrary},
	{Code: "template_5", Name: "使用模板创建任务", How: "服务端模板场景的创建事件组，按差额逐组补发", run: runTemplates},
	{Code: "playbook_prompt", Name: "灵感案例做同款", How: "灵感案例「做同款」发送事件组", run: runPlaybook},
	{Code: "create_canvas", Name: "创建设计画布", How: "设计画布创建/打开事件组", run: runCanvas},
	{Code: "expert_5", Name: "使用 5 位专家", How: "专家市场真实专家：召唤 → 真实对话 → 使用事件", Chat: true, run: runExperts("agent")},
	{Code: "Expert_team_use_3", Name: "使用 3 个专家团", How: "专家团：召唤 → 真实对话 → 使用事件", Chat: true, run: runExperts("team")},
	{Code: "Hp_Appearance", Name: "应用主题外观", How: "设置主题 + 皮肤生效事件", run: runAppearance},
	{Code: "skill_1", Name: "尝鲜热门技能", How: "真实对话 + 技能加载事件", Chat: true, run: runSkill},
	{Code: "Expert_lighthouse", Name: "体验腾讯轻量云专家", How: "轻量云专家：召唤 → 真实对话 → 使用事件", Chat: true, run: runLighthouse},
	{Code: "black_cat", Name: "夜猫子", How: "23:00–08:00 内 glm-5.2 真实对话 + 夜间上报，每晚 1 次", Chat: true, Night: true, run: runNight},
	{Code: "skill_share", Name: "分享技能给好友", How: "创建真实的技能分享渠道并上报分享动作", run: runSkillShare},
	{Code: "school_season", Name: "校园日（小程序）", How: "小程序对话事件（带活动 id），真人节奏间隔", MP: true, run: runMPChat("school_open_day_2026", "", "")},
	{Code: "Sequential_Tasks_1", Name: "小程序首对话", How: "小程序对话事件", MP: true, run: runMPChat("", "", "")},
	{Code: "Sequential_Tasks_2", Name: "小程序选专家对话", How: "市场真实专家的小程序使用事件", MP: true, run: runMPExpert},
	{Code: "Sequential_Tasks_3", Name: "小程序对话 5 次", How: "小程序对话事件 ×差额，每条间隔约 45 秒（防反作弊回滚）", MP: true, run: runMPChat("", "", "")},
	{Code: "Sequential_Tasks_4", Name: "小程序定时任务", How: "小程序定时任务创建事件", MP: true, run: runMPAutomation},
	{Code: "Sequential_Tasks_5", Name: "小程序使用 GLM-5.2", How: "带 glm-5.2 模型字段的小程序对话事件", MP: true, run: runMPChat("", "glm-5.2", "GLM-5.2")},
	{Code: "Sequential_Tasks_6", Name: "小程序对话 10 次", How: "小程序对话事件 ×差额，每条间隔约 45 秒", MP: true, run: runMPChat("", "", "")},
	{Code: "Sequential_Tasks_7", Name: "体验灵感功能", How: "灵感案例事件组", MP: true, run: runPlaybook},
}

// ActionFor is a code's action, nil for none.
func ActionFor(code string) *Action {
	for i := range Actions {
		if Actions[i].Code == code {
			return &Actions[i]
		}
	}
	return nil
}

func actionIndex(code string) int {
	for i := range Actions {
		if Actions[i].Code == code {
			return i
		}
	}
	return len(Actions)
}

// gaps that tests shorten
var (
	mpChatGap = 45.0 // seconds between mini-program chat events
	pollTries = 4
	pollGap   = 3.0
)

// SetMPChatGap sets the mini program's chat spacing in seconds.
func SetMPChatGap(s float64) { mpChatGap = s }

func need(t *Task, def int64) int64 {
	tgt := t.Target
	if tgt <= 0 {
		tgt = def
	}
	n := tgt - t.Current
	if n < 1 {
		n = 1
	}
	return n
}

func runChat5(ctx context.Context, c *Client, t *Task, r *Result) error {
	n := need(t, 5)
	for i := int64(0); i < n; i++ {
		if err := c.ReportChat(ctx, ChatSendEvent(c.A.UID, "deepseek-v4-flash", "DeepSeek V4 Flash", "craft", int(i), nowMS())); err != nil {
			return fmt.Errorf("第 %d/%d 条上报失败: %w", i+1, n, err)
		}
		if err := c.sleepGap(ctx); err != nil {
			return err
		}
	}
	r.logf("已上报 %d 条对话活跃事件", n)
	return nil
}

func runFirstBuddy(ctx context.Context, c *Client, _ *Task, r *Result) error {
	msg, err := c.AdoptBuddy(ctx)
	if err != nil {
		return err
	}
	r.logf("%s", msg)
	return nil
}

func runModelChat(ctx context.Context, c *Client, _ *Task, r *Result) error {
	if _, err := c.realChat(ctx, "glm-5.2", "你好，请用一句话介绍你自己。", nil); err != nil {
		return err
	}
	if err := c.sleepGap(ctx); err != nil {
		return err
	}
	if err := c.ReportChat(ctx, ChatSendEvent(c.A.UID, "glm-5.2", "GLM-5.2", "craft", 0, nowMS())); err != nil {
		return fmt.Errorf("对话完成但上报失败: %w", err)
	}
	r.logf("glm-5.2 真实对话一次并已上报")
	return nil
}

func runRichMeow(ctx context.Context, c *Client, _ *Task, r *Result) error {
	conv, req := newIDs("rm")
	if err := c.ReportDesktop(ctx, DesktopChatSequence(conv, req, "msg-"+req[len(req)-8:], "fast-model", "fast-model", nowMS())...); err != nil {
		return err
	}
	r.logf("已上报桌面端对话事件链")
	return nil
}

func runBuddyApp(ctx context.Context, c *Client, _ *Task, r *Result) error {
	if err := c.ReportDesktop(ctx, BuddyAppSequence("cb_y5Dy46tPQGGWtueMxXbe", "企鹅教师助手")...); err != nil {
		return err
	}
	r.logf("已上报 Buddy 应用进入事件链")
	return nil
}

func runAutomation(ctx context.Context, c *Client, _ *Task, r *Result) error {
	if err := c.ReportDesktop(ctx, AutomationEvent("每周工作整理")); err != nil {
		return err
	}
	r.logf("已上报定时任务创建事件")
	return nil
}

func runLibrary(ctx context.Context, c *Client, _ *Task, r *Result) error {
	if err := c.ReportWeb(ctx, Event{"eventCode": "web_element_click", "pageURL": "https://www.workbuddy.cn/space/d/o0KWYeynteVv06UnAZqIFm", "elementId": "library_doc_intro_click", "elementName": "WorkBuddy资料库介绍"}); err != nil {
		return err
	}
	r.logf("已上报资料库阅读事件")
	return nil
}

func runTemplates(ctx context.Context, c *Client, t *Task, r *Result) error {
	scenes := c.Scenes(ctx)
	target := t.Target
	if target <= 0 {
		target = 5
	}
	cur := t.Current
	for cur < target {
		s := scenes[int(cur)%len(scenes)]
		conv, req := uuid4(), messageID()
		if err := c.ReportDesktop(ctx, TemplateEvents(s.ID, s.Name, conv, req, uuid4())...); err != nil {
			return fmt.Errorf("模板「%s」事件上报失败: %w", s.Name, err)
		}
		fresh := c.waitTask(ctx, t.Code, pollTries, 2)
		if fresh == nil {
			break
		}
		if fresh.Done() {
			cur = target
			break
		}
		if fresh.Current <= cur {
			r.logf("服务端进度未增长（%d/%d），停止本轮补发", fresh.Current, target)
			break
		}
		cur = fresh.Current
	}
	r.logf("模板任务进度 %d/%d", cur, target)
	return nil
}

func runPlaybook(ctx context.Context, c *Client, _ *Task, r *Result) error {
	conv, req := newIDs("pb")
	if err := c.ReportDesktop(ctx, PlaybookSequence(conv, req, "pm-gtm-launch-plan", "新产品上市 GTM 发布计划一页纸", nowMS())...); err != nil {
		return err
	}
	r.logf("已上报灵感案例做同款事件组")
	return nil
}

func runCanvas(ctx context.Context, c *Client, _ *Task, r *Result) error {
	conv, req := newIDs("canvas")
	if err := c.ReportDesktop(ctx, CanvasSequence(conv, req, nowMS())...); err != nil {
		return err
	}
	r.logf("已上报设计画布事件组")
	return nil
}

// useExpert summons an expert, chats with it for real and reports the use
// with the server's request id.
func useExpert(ctx context.Context, c *Client, e Expert, lighthouse bool) error {
	if err := c.ReportDesktop(ctx, ExpertSummonSequence(e)...); err != nil {
		return fmt.Errorf("召唤事件: %w", err)
	}
	h := http.Header{}
	h.Set("X-Expert-Id", e.ID)
	reply, err := c.realChat(ctx, "fast-model", "1+1等于几？直接回答。", h)
	if err != nil {
		return err
	}
	conv := fmt.Sprintf("wb-conv-%d", time.Now().UnixNano())
	req := reply.RequestID
	evs := DesktopChatSequence(conv, req, "msg-"+tail8(req), "fast-model", "fast-model", nowMS())
	mode := "craft"
	if lighthouse {
		mode = "LOCAL"
		for _, ev := range evs {
			if ev["eventCode"] == "agent_task_created" {
				ev["has_expert"] = true
				ev["expert_id"] = e.ID
				ev["expert_name"] = e.Name
			}
		}
	}
	use := ExpertUseEvent(e, conv, req, mode)
	if lighthouse {
		use["type"] = ""
		use["cost"] = 0
	}
	return c.ReportDesktop(ctx, append(evs, use)...)
}

func tail8(s string) string {
	if len(s) > 8 {
		return s[len(s)-8:]
	}
	return s
}

func runExperts(kind string) func(ctx context.Context, c *Client, t *Task, r *Result) error {
	return func(ctx context.Context, c *Client, t *Task, r *Result) error {
		def := int64(5)
		if kind == "team" {
			def = 3
		}
		n := need(t, def)
		list, err := c.Experts(ctx, kind)
		if err != nil {
			return fmt.Errorf("专家市场: %w", err)
		}
		if len(list) == 0 {
			return fmt.Errorf("专家市场没有返回专家")
		}
		// experts already counted go first in the list too; start past them
		start := int(t.Current) % len(list)
		ok := 0
		for i := 0; i < len(list) && int64(ok) < n; i++ {
			e := list[(start+i)%len(list)]
			if err := useExpert(ctx, c, e, false); err != nil {
				r.logf("专家 %s: %v", e.Name, err)
				continue
			}
			ok++
			r.logf("已使用专家「%s」", e.Name)
			if pause(ctx, 6) != nil {
				break
			}
		}
		if ok == 0 {
			return fmt.Errorf("没有一位专家完成召唤+使用")
		}
		return nil
	}
}

func runLighthouse(ctx context.Context, c *Client, _ *Task, r *Result) error {
	e := Expert{ID: "ex_2cvvUZQhDyeJ", Type: "agent", Name: "腾讯轻量云专家", Profession: "腾讯轻量云专家", Version: "1.0.2"}
	if list, err := c.Experts(ctx, "agent"); err == nil {
		for _, x := range list {
			if x.ID == e.ID {
				e = x
			}
		}
	}
	if err := useExpert(ctx, c, e, true); err != nil {
		return err
	}
	r.logf("已使用腾讯轻量云专家")
	return nil
}

func runSkill(ctx context.Context, c *Client, _ *Task, r *Result) error {
	reply, err := c.realChat(ctx, "fast-model", "帮我写一句今天的工作日报开头。", nil)
	if err != nil {
		return err
	}
	conv := fmt.Sprintf("wb-conv-%d", time.Now().UnixNano())
	msg := "msg-" + tail8(reply.RequestID)
	evs := DesktopChatSequence(conv, reply.RequestID, msg, "fast-model", "fast-model", nowMS())
	for _, ev := range evs {
		if ev["eventCode"] == "chat_message_response" {
			ev["finishReason"] = "tool_calls"
		}
	}
	if err := c.ReportDesktop(ctx, append(evs, SkillInfoEvent(conv, reply.RequestID, msg))...); err != nil {
		return err
	}
	r.logf("真实对话 + 技能加载事件已上报")
	return nil
}

func runAppearance(ctx context.Context, c *Client, _ *Task, r *Result) error {
	const theme = "theme-tkmw7j"
	if err := c.SetTheme(ctx, theme); err != nil {
		return fmt.Errorf("设置主题: %w", err)
	}
	if err := pause(ctx, 2); err != nil {
		return err
	}
	if err := c.ReportDesktop(ctx, AppearanceEvent(theme)); err != nil {
		return err
	}
	r.logf("已设置主题并上报生效事件")
	return nil
}

func runNight(ctx context.Context, c *Client, _ *Task, r *Result) error {
	if !InNightWindow(time.Now()) {
		return errNotNow
	}
	if _, err := c.realChat(ctx, "glm-5.2", "现在几点适合休息？一句话回答。", nil); err != nil {
		return err
	}
	if err := c.sleepGap(ctx); err != nil {
		return err
	}
	if err := c.ReportChat(ctx, ChatSendEvent(c.A.UID, "glm-5.2", "GLM-5.2", "night", 0, nowMS())); err != nil {
		return err
	}
	r.logf("今晚的夜间对话已完成并上报（每晚计 1 次）")
	return nil
}

// errNotNow is a task outside its window.
var errNotNow = fmt.Errorf("不在 23:00–08:00（北京时间）计数窗口，夜间调度会自动执行")

func runSkillShare(ctx context.Context, c *Client, _ *Task, r *Result) error {
	skill, err := c.MarketSkill(ctx)
	if err != nil {
		return err
	}
	code, err := c.ShareSkill(ctx, skill)
	if err != nil {
		return err
	}
	if err := c.ReportDesktop(ctx, SkillShareEvents(skill.ID, skill.Name, code, nowMS())...); err != nil {
		return fmt.Errorf("分享渠道已创建但上报失败: %w", err)
	}
	r.logf("已创建技能「%s」的分享渠道并上报", skill.Name)
	return nil
}

func runMPChat(activity, model, modelName string) func(ctx context.Context, c *Client, t *Task, r *Result) error {
	return func(ctx context.Context, c *Client, t *Task, r *Result) error {
		n := need(t, 1)
		for i := int64(0); i < n; i++ {
			// real-person spacing: rapid events are counted, then rolled back
			if err := pause(ctx, mpChatGap+rand.Float64()*10*boolf(mpChatGap > 0)); err != nil {
				return err
			}
			conv, req := uuid4(), messageID()
			ev := MPChatEvent(conv, req)
			if activity != "" {
				ev["activityId"] = activity
			}
			if model != "" {
				ev["requestModelId"] = model
				ev["requestModelName"] = modelName
			}
			if err := c.ReportMP(ctx, "", nil, ev); err != nil {
				return fmt.Errorf("第 %d/%d 条小程序事件失败: %w", i+1, n, err)
			}
		}
		r.logf("已按真人节奏上报 %d 条小程序对话事件", n)
		return nil
	}
}

func boolf(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func runMPExpert(ctx context.Context, c *Client, _ *Task, r *Result) error {
	list, err := c.Experts(ctx, "agent")
	if err != nil || len(list) == 0 {
		return fmt.Errorf("专家市场不可用（%v），为避免半程状态本轮不上报", err)
	}
	e := list[0]
	if err := c.ReportMP(ctx, "", nil, MPExpertUseEvent(e.ID, e.Name, e.Type)); err != nil {
		return err
	}
	r.logf("已上报小程序专家「%s」使用事件", e.Name)
	return nil
}

func runMPAutomation(ctx context.Context, c *Client, _ *Task, r *Result) error {
	if err := c.ReportMP(ctx, "", nil, MPAutomationEvent()); err != nil {
		return err
	}
	r.logf("已上报小程序定时任务创建事件")
	return nil
}

// RunGrowthTask completes one growth task: accept it, run its action, wait
// for the asynchronous score and claim the reward (with claim on).
func (c *Client) RunGrowthTask(ctx context.Context, code string, claim bool) Result {
	r := Result{Job: "growth:" + code, At: time.Now()}
	if c.Key() || c.Enterprise() || c.Site == nil || c.Site.AI {
		r.Outcome, r.Message, r.OK = "skipped", "成长任务只适用于国内版个人账号", true
		return r
	}
	t, err := c.TaskByCode(ctx, code)
	if err != nil {
		r.Outcome, r.Message = "failed", "读取任务失败: "+err.Error()
		return r
	}
	if t == nil {
		r.Outcome, r.Message, r.OK = "skipped", "该账号没有这个任务（未下发或已下线）", true
		return r
	}
	c.progressTask(ctx, t, claim, &r)
	return r
}

// progressTask moves one task as far as it goes this run.
func (c *Client) progressTask(ctx context.Context, t *Task, claim bool, r *Result) {
	name := t.Title
	if name == "" {
		name = t.Code
	}
	mp := MPTask(t.Code)
	if t.Claimed {
		r.OK, r.Outcome, r.Message = true, "done", fmt.Sprintf("「%s」已领奖", name)
		return
	}
	if t.Done() {
		c.claimInto(ctx, t, name, mp, claim, r)
		return
	}
	if h := HumanFor(t.Code); h != nil {
		r.OK, r.Outcome = true, "pending"
		r.Message = fmt.Sprintf("「%s」需真人完成：%s", name, h.Reason)
		r.Pending = append(r.Pending, r.Message)
		return
	}
	act := ActionFor(t.Code)
	expert := ""
	if act == nil {
		if id, ok := ExpertFromJump(t.Code, t.JumpURL, name); ok {
			expert = id
		}
	}
	if act == nil && expert == "" {
		r.OK, r.Outcome = true, "pending"
		r.Message = fmt.Sprintf("「%s」没有可用的接口完成，请按任务说明在官方客户端操作：%s", name, firstNonEmpty(t.Description, t.TaskDesc, t.JumpURL))
		r.Pending = append(r.Pending, r.Message)
		return
	}
	if act != nil && act.Night && !InNightWindow(time.Now()) {
		r.OK, r.Outcome, r.Message = true, "skipped", fmt.Sprintf("「%s」%s", name, errNotNow.Error())
		return
	}
	if t.Locked {
		r.OK, r.Outcome, r.Message = true, "skipped", fmt.Sprintf("「%s」尚未解锁", name)
		return
	}
	if t.AcceptStatus == "not_accepted" && t.Type != "auto" {
		acc, _, why := c.Accept(ctx, []string{t.Code}, mp)
		if len(acc) == 0 {
			r.Outcome, r.Message = "failed", fmt.Sprintf("「%s」接取失败：%s（未接取的任务上报不计进度）", name, why)
			return
		}
		r.logf("已接取「%s」", name)
		_ = c.sleepGap(ctx)
		if fresh, err := c.TaskByCode(ctx, t.Code); err == nil && fresh != nil {
			t = fresh
		}
	}
	before := progressText(t)
	var err error
	if act != nil {
		err = act.run(ctx, c, t, r)
	} else {
		e := Expert{ID: expert, Type: "agent", Name: name, Profession: name}
		err = useExpert(ctx, c, e, false)
		if err == nil {
			r.logf("已使用专家 %s", expert)
		}
	}
	if err != nil {
		if err == errNotNow {
			r.OK, r.Outcome, r.Message = true, "skipped", fmt.Sprintf("「%s」%s", name, err.Error())
			return
		}
		r.Outcome, r.Message = "failed", fmt.Sprintf("「%s」执行失败：%v", name, err)
		return
	}
	fresh := c.waitTask(ctx, t.Code, pollTries, pollGap)
	if fresh == nil {
		r.OK, r.Outcome, r.Message = true, "pending", fmt.Sprintf("「%s」已上报，回读进度失败，下次运行会领奖", name)
		return
	}
	if !fresh.Done() {
		r.OK, r.Outcome = true, "pending"
		r.Message = fmt.Sprintf("「%s」已上报，进度 %s → %s，服务端未落账，下次运行继续", name, before, progressText(fresh))
		if act != nil && (act.Code == "Buddy_App" || act.Code == "Buddy_App_QQ") {
			r.Message += "；若一直不点亮，请在桌面端「发现应用」进入对应应用"
		}
		return
	}
	c.claimInto(ctx, fresh, name, mp, claim, r)
}

func (c *Client) claimInto(ctx context.Context, t *Task, name string, mp, claim bool, r *Result) {
	if !claim {
		r.OK, r.Outcome, r.Message = true, "done", fmt.Sprintf("「%s」已达成（%s），领奖开关关闭，等待手动领取", name, progressText(t))
		return
	}
	credit, energy, err := c.Claim(ctx, t.Code, mp)
	if err != nil {
		r.Outcome, r.Message = "failed", fmt.Sprintf("「%s」已达成但领奖失败：%v", name, err)
		return
	}
	r.OK, r.Outcome, r.Credits = true, "claimed", r.Credits+float64(credit)
	r.Message = fmt.Sprintf("「%s」已领奖 +%d 积分 +%d 能量", name, credit, energy)
}

func progressText(t *Task) string {
	if t == nil {
		return "?"
	}
	if t.Target > 0 {
		return fmt.Sprintf("%d/%d", t.Current, t.Target)
	}
	return t.AcceptStatus
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return "（无说明）"
}

// RunGrowthAll runs every growth task the account has (mp: the mini
// program's tasks instead): auto tasks first (first_buddy unlocks the
// rest), a batch accept, then each in the actions' order.
func (c *Client) RunGrowthAll(ctx context.Context, mp, claim bool) Result {
	job := "growth"
	if mp {
		job = "miniprogram"
	}
	r := Result{Job: job, At: time.Now(), OK: true, Outcome: "done"}
	if c.Key() || c.Enterprise() || c.Site == nil || c.Site.AI {
		r.Outcome, r.Message = "skipped", "成长任务只适用于国内版个人账号"
		return r
	}
	list, err := c.ListTasks(ctx, mp)
	if err != nil {
		r.OK, r.Outcome, r.Message = false, "failed", "读取任务列表失败: "+err.Error()
		return r
	}
	var tasks []Task
	for _, t := range list {
		if MPTask(t.Code) == mp {
			tasks = append(tasks, t)
		}
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		ai, aj := tasks[i].Type == "auto", tasks[j].Type == "auto"
		if ai != aj {
			return ai
		}
		return actionIndex(tasks[i].Code) < actionIndex(tasks[j].Code)
	})
	var accept []string
	for _, t := range tasks {
		if t.AcceptStatus == "not_accepted" && t.Type != "auto" && !t.Locked && HumanFor(t.Code) == nil && t.Code != "black_cat" {
			accept = append(accept, t.Code)
		}
	}
	if len(accept) > 0 {
		ok, failed, why := c.Accept(ctx, accept, mp)
		r.logf("批量接取 %d 个任务，成功 %d", len(accept), len(ok))
		if len(failed) > 0 {
			r.logf("接取未成功 %d 个：%s", len(failed), why)
		}
		_ = c.sleepGap(ctx)
		if fresh, err := c.ListTasks(ctx, mp); err == nil {
			byCode := map[string]Task{}
			for _, t := range fresh {
				byCode[t.Code] = t
			}
			for i := range tasks {
				if t, ok := byCode[tasks[i].Code]; ok {
					tasks[i] = t
				}
			}
		}
	}
	var failed, pending, claimed int
	for i := range tasks {
		if ctx.Err() != nil {
			break
		}
		t := tasks[i]
		if t.Claimed {
			continue
		}
		sub := Result{}
		c.progressTask(ctx, &t, claim, &sub)
		r.Log = append(r.Log, sub.Log...)
		if sub.Message != "" {
			r.Log = append(r.Log, sub.Message)
		}
		r.Pending = append(r.Pending, sub.Pending...)
		r.Credits += sub.Credits
		switch sub.Outcome {
		case "failed":
			failed++
		case "pending":
			pending++
		case "claimed":
			claimed++
		}
		_ = c.sleepGap(ctx)
	}
	r.Message = fmt.Sprintf("共 %d 个任务：领奖 %d，待完成 %d，失败 %d，本次 +%.0f 积分", len(tasks), claimed, pending, failed, r.Credits)
	if failed > 0 {
		r.OK = false
		r.Outcome = "partial"
	}
	if claimed > 0 && failed == 0 {
		r.Outcome = "claimed"
	}
	return r
}
