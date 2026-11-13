package jobs

import (
	"fmt"
	"strings"
)

// The event shapes below are those workbuddy2api-panel (upstream/desktop.go,
// school.go) and workbuddy2api-hub (wb_tasks.py) recorded from the desktop
// client and the mini program; each lights the growth task its comment
// names.

func mk(code string, extra map[string]any) Event {
	ev := Event{"eventCode": code}
	for k, v := range extra {
		ev[k] = v
	}
	return ev
}

// DesktopChatSequence is one successful desktop chat (agent_task_created →
// chat_message_send → chat_request_send → chat_message_response →
// chat_message_status → chat_request_response): RichMeow_Chat, and the chat
// leg of the expert, template, playbook and canvas groups.
func DesktopChatSequence(conv, req, msg, model, modelName string, now int64) []Event {
	return []Event{
		mk("agent_task_created", map[string]any{
			"source": "LOCAL", "name": "working", "task_target": "local", "mode": "craft",
			"requestModelId": model, "requestModelName": modelName,
			"has_repo": false, "repo_type": "none", "workspace_type": "empty",
			"has_connector": false, "connector_types": []any{},
			"has_mention": false, "mention_types": []any{},
			"has_template": false, "action": "", "template_name": "",
			"has_expert": false, "expert_id": "", "expert_name": "", "expert_industry_id": "",
			"has_skill": false, "skill_names": []any{},
			"conversationId": conv, "messageId": msg, "buddyId": "", "buddyName": "",
		}),
		mk("chat_message_send", map[string]any{
			"messageId": msg + "-assistant", "historyCount": 0, "isContextTruncated": false,
			"currentStepCount": 1, "traceId": req, "rootRequestId": req,
			"parentConversationId": conv, "agentName": "cli", "agentType": "main",
		}),
		mk("chat_request_send", map[string]any{
			"inputLength": 24, "isPlan": false, "isAutoExecuteTerminal": false,
			"isAutoModify": false, "codebaseEnable": false, "maxToken": 0,
			"maxSteps": 500, "temperature": 0, "maxRetries": 0,
			"mentionContexts": []any{}, "knowledgeId": []any{}, "knowledgeName": []any{},
			"codebaseId": "", "mentionContextCount": 0, "command": "",
			"recommendId": "", "skillId": "", "skillCount": 0, "totalCount": 0,
			"traceId": req, "rootRequestId": req, "parentConversationId": conv,
			"agentName": "cli", "agentType": "main",
			"codebuddy.session_id": conv, "codebuddy.conversation_request_id": req,
		}),
		mk("chat_message_response", map[string]any{
			"messageId": msg + "-assistant", "responseModelId": model,
			"inputToken": 120, "outputToken": 80, "totalToken": 200,
			"cachedTokens": 0, "cachedWriteTokens": 0, "cachedMissTokens": 0,
			"isSuccessful": true, "messageErrorCode": "", "finishReason": "stop",
			"firstTokenAt": now, "traceId": req, "conversationId": conv,
			"rootRequestId": req, "parentConversationId": conv,
			"agentName": "cli", "agentType": "main",
			"codebuddy.session_id": conv, "codebuddy.conversation_request_id": req,
		}),
		mk("chat_message_status", map[string]any{
			"messageId": msg + "-assistant", "messageErrorCode": "0",
			"traceId": req, "rootRequestId": req, "parentConversationId": conv,
			"agentName": "cli", "agentType": "main",
		}),
		mk("chat_request_response", map[string]any{
			"mode": "craft", "toolCallCount": 0,
			"inputToken": 120, "outputToken": 80, "totalToken": 200,
			"cachedTokens": 0, "cachedWriteTokens": 0, "cachedMissTokens": 0,
			"isSuccessful": true, "messageErrorCode": "", "finishReason": "stop",
			"rootRequestId": req, "parentConversationId": conv,
		}),
	}
}

// BuddyAppSequence is entering a Buddy app (discover → show → enter →
// auth confirm → skip binding): Buddy_App and, with 企鹅教师助手,
// Buddy_App_QQ.
func BuddyAppSequence(buddyID, buddyName string) []Event {
	base := func(code string, extra map[string]any) Event {
		ev := Event{"eventCode": code, "mode": "LOCAL", "buddyId": buddyID, "buddyName": buddyName}
		for k, v := range extra {
			ev[k] = v
		}
		return ev
	}
	el := map[string]any{"elementId": buddyID, "elementName": buddyName}
	with := func(extra map[string]any) map[string]any {
		m := map[string]any{}
		for k, v := range el {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	return []Event{
		base("buddyapp_discover_click", nil),
		base("buddyapp_show", with(map[string]any{"position": 2})),
		base("buddyapp_enter_click", with(map[string]any{"position": 2, "isFirstPage": "1"})),
		base("buddyapp_auth_confirm_click", with(nil)),
		base("buddyapp_bindaccount_skip_click", with(nil)),
	}
}

// AutomationEvent is a scheduled task created (automation_1).
func AutomationEvent(name string) Event {
	return mk("automated_task_create_suc", map[string]any{
		"name": name, "source": "manually", "modelId": "fast-model", "modelIsThinking": true,
		"connectorCount": 0, "skills": "", "skillCount": 0, "scheduleType": "once", "mode": "LOCAL",
	})
}

// TemplateEvents is one template-created task (template_5 counts one per
// group): the server's scene id, as wb_tasks._template_events sends it.
func TemplateEvents(sceneID, sceneName, conv, req, ext string) []Event {
	return []Event{
		mk("agent_task_created", map[string]any{
			"source": "CLOUD", "name": "", "mode": "craft", "requestModelId": "default",
			"action": sceneID, "has_template": true, "template_id": sceneID,
			"template_name": sceneName, "requestId": req, "conversationId": conv,
		}),
		mk("agent_task_created_with_template", map[string]any{
			"templateId": sceneID, "templateName": sceneName, "template_id": sceneID,
			"mode": "working", "isCustomModel": true, "id": sceneID, "name": sceneName,
			"requestId": req, "conversationId": conv,
		}),
		mk("template_used", map[string]any{
			"template_id": sceneID, "templateId": sceneID, "templateName": sceneName,
			"task_mode": "working", "id": sceneID, "name": sceneName,
			"source": "growth-center", "conversationId": conv,
		}),
		mk("playbook_prompt_send", map[string]any{
			"ext1": ext, "requestId": req, "id": sceneID, "name": sceneName,
			"type": "other", "promptLength": 30, "isOfficial": 1,
			"source": "growth-center", "conversationId": conv,
		}),
	}
}

// PlaybookSequence is "make the same" from an inspiration case
// (playbook_prompt): its chat, the CTA click and the prompt sent.
func PlaybookSequence(conv, req, caseID, caseName string, now int64) []Event {
	evs := DesktopChatSequence(conv, req, "msg-pb", "fast-model", "fast-model", now)
	payload := map[string]any{"id": caseID, "name": caseName, "type": "document", "categoryId": "", "categoryName": ""}
	cta := mk("playbook_cta_click", map[string]any{"source": "discover", "position": 0})
	send := mk("playbook_prompt_send", map[string]any{"conversationId": conv, "requestId": req})
	for k, v := range payload {
		cta[k] = v
		send[k] = v
	}
	return append(evs,
		mk("web_element_click", map[string]any{"pageName": "playbook_detail", "elementId": "playbook_ctaClick", "elementName": caseName, "source": "discover"}),
		cta, send)
}

// CanvasSequence is a design canvas created and opened (create_canvas).
func CanvasSequence(conv, req string, now int64) []Event {
	evs := DesktopChatSequence(conv, req, "msg-canvas", "fast-model", "fast-model", now)
	tail := req
	if len(tail) > 8 {
		tail = tail[len(tail)-8:]
	}
	return append(evs,
		mk("wbx_design_canvas_task_create", map[string]any{"conversationId": conv, "requestId": req, "source": "summon_keyword", "cost": 12000, "isSuccessful": true}),
		mk("wbx_design_canvas_open", map[string]any{"conversationId": conv, "requestId": req, "id": "ardot-file-" + tail, "source": "summon_keyword", "type": "page", "cost": 13000, "isSuccessful": true}),
	)
}

// Expert is one of the expert market's experts (or teams).
type Expert struct {
	ID         string `json:"expert_id"`
	Type       string `json:"expert_type"`
	Name       string `json:"display_name_zh"`
	Profession string `json:"profession_zh"`
	Version    string `json:"version"`
	Categories []any  `json:"categories"`
}

func (e Expert) category() string {
	if len(e.Categories) > 0 {
		if s, ok := e.Categories[0].(string); ok && s != "" {
			return s
		}
	}
	return "expert-all"
}

func (e Expert) version() string {
	if e.Version == "" {
		return "1.0.0"
	}
	return e.Version
}

// ExpertSummonSequence is summoning an expert from the market.
func ExpertSummonSequence(e Expert) []Event {
	return []Event{
		mk("web_element_click", map[string]any{
			"source": e.ID, "type": e.category(), "version": e.version(),
			"elementId": "expert_summon_click", "elementName": "立即召唤",
			"pageURL": "/C:/Program%20Files/WorkBuddy/resources/app.asar/renderer/index.html",
		}),
		mk("expert_summon_click", map[string]any{
			"id": e.ID, "name": e.Name, "expertTitle": e.Profession, "type": "expert-all",
			"position": 0, "expertType": e.Type, "version": e.version(), "mode": "LOCAL",
		}),
		mk("expert_summoned", map[string]any{"id": e.ID, "name": e.Name, "expertTitle": e.Profession, "type": "expert-all"}),
	}
}

// ExpertUseEvent is an expert actually used in a chat whose request id is
// the server's own (expert_5, Expert_team_use_3; mode LOCAL for
// Expert_lighthouse).
func ExpertUseEvent(e Expert, conv, req, mode string) Event {
	tail := req
	if len(tail) > 8 {
		tail = tail[len(tail)-8:]
	}
	return mk("expert_actual_use", map[string]any{
		"id": e.ID, "name": e.Name, "expertTitle": e.Profession,
		"type": e.category(), "expertType": e.Type, "source": "builtin", "version": e.version(),
		"cost": 9000, "characterCount": 14, "mode": mode,
		"conversationId": conv, "requestId": req, "messageId": "msg-" + tail,
		"requestModelId": "fast-model", "requestModelName": "fast-model",
	})
}

// SkillInfoEvent is a skill loaded in a real chat (skill_1).
func SkillInfoEvent(conv, req, msg string) Event {
	return mk("skill_info", map[string]any{
		"id": "润泽小馆·日报撰写", "skillId": "skill_2097350077599879168", "skillVersion": "1.0.0",
		"toolStatus": "success", "fileCount": 56, "source": "workbuddy-desktop",
		"conversationId": conv, "requestId": req, "messageId": msg,
		"requestModelId": "fast-model", "requestModelName": "fast-model", "traceId": req,
	})
}

// AppearanceEvent is a theme applied on leaving settings (Hp_Appearance).
func AppearanceEvent(themeKey string) Event {
	return mk("appearance_skin_apply", map[string]any{
		"action": "apply", "source": "settings_close", "id": themeKey,
		"vipLevel": 0, "series": "", "type": "unknown",
	})
}

// MPChatEvent is the mini program's chat_request_send (Sequential_Tasks_1,
// _3, _6, school_season with its activity id, _5 with GLM-5.2's model).
func MPChatEvent(conv, req string) Event {
	tail := req
	if len(tail) > 8 {
		tail = tail[len(tail)-8:]
	}
	return mk("chat_request_send", map[string]any{
		"mode": "chat", "inputLength": 12, "isPlan": false, "isAutoExecuteTerminal": false,
		"isAutoModify": false, "codebaseEnable": false, "maxToken": 0, "maxSteps": 500,
		"temperature": 0, "maxRetries": 0, "mentionContexts": []any{}, "knowledgeId": []any{},
		"knowledgeName": []any{}, "codebaseId": "", "mentionContextCount": 0, "command": "",
		"recommendId": "", "skillId": "", "skillCount": 0, "totalCount": 0,
		"requestId": req, "traceId": req, "rootRequestId": req,
		"parentConversationId": conv, "conversationId": conv,
		"messageId": "msg-" + tail, "agentName": "mp", "agentType": "main",
		"codebuddy.session_id": conv, "codebuddy.conversation_request_id": req,
	})
}

// MPExpertUseEvent is the mini program's expert_actual_use
// (Sequential_Tasks_2): no conversation, the mini program's own version.
func MPExpertUseEvent(id, title, typ string) Event {
	if typ == "" {
		typ = "agent"
	}
	if title == "" {
		title = id
	}
	return mk("expert_actual_use", map[string]any{
		"reportDelay": 0, "extVersion": "2.2.8", "source": "mini_program",
		"id": id, "name": id, "expertTitle": title, "type": "send_message",
		"characterCount": 12, "expertType": typ,
	})
}

// MPAutomationEvent is a scheduled task created in the mini program
// (Sequential_Tasks_4).
func MPAutomationEvent() Event {
	return mk("automated_task_create_suc", map[string]any{
		"mode": "CLOUD", "name": "每日任务提醒", "source": "manually", "skills": "",
		"skillCount": 0, "scheduleType": "daily",
	})
}

// newIDs are a fresh conversation id and request id.
func newIDs(prefix string) (conv, req string) {
	ms := nowMS()
	return fmt.Sprintf("wb-%s-%d", prefix, ms), messageID()
}

// ExpertFromJump reads the expert a task's deep link names
// (wb_tasks.extract_expert_from_task): expertId in the link or its nested
// deeplink, or the code's expert_use_<slug>.
func ExpertFromJump(code, jump, title string) (string, bool) {
	find := func(s string) string {
		i := strings.Index(s, "expertId=")
		if i < 0 {
			return ""
		}
		v := s[i+len("expertId="):]
		if j := strings.IndexAny(v, "&#"); j >= 0 {
			v = v[:j]
		}
		return v
	}
	for _, s := range []string{jump, unescape(jump), unescape(unescape(jump))} {
		if id := find(s); validExpertID(id) {
			return id, true
		}
	}
	if strings.HasPrefix(code, "expert_use_") {
		return "", false // slugs are not market ids; the market list is asked instead
	}
	return "", false
}

func validExpertID(id string) bool {
	if !strings.HasPrefix(id, "ex_") || len(id) < 9 || len(id) > 67 {
		return false
	}
	for _, r := range id[3:] {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func unescape(s string) string {
	r := strings.NewReplacer("%3D", "=", "%3d", "=", "%26", "&", "%3F", "?", "%3f", "?", "%2F", "/", "%2f", "/", "%3A", ":", "%3a", ":", "%25", "%")
	return r.Replace(s)
}
