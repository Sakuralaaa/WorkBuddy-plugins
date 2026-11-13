package jobs

import (
	"context"
	"fmt"
	"net/http"
)

// ChatReply is what a real chat came back with: the server's own request id
// (the id events that reference the chat must carry) and its text.
type ChatReply struct {
	RequestID string
	Text      string
}

// ChatFunc sends one real chat for the account through the plugin's own
// request path (signing, the host's proxy, the free-only switch); extra
// headers ride along (X-Expert-Id for an expert's chat).
type ChatFunc func(ctx context.Context, model, prompt string, extra http.Header) (ChatReply, error)

// realChat runs a chat; the jobs refuse to stand one in.
func (c *Client) realChat(ctx context.Context, model, prompt string, extra http.Header) (ChatReply, error) {
	if c.Chat == nil {
		return ChatReply{}, fmt.Errorf("no chat channel")
	}
	cctx, cancel := context.WithTimeout(ctx, 90*1e9)
	defer cancel()
	r, err := c.Chat(cctx, model, prompt, extra)
	if err != nil {
		return r, fmt.Errorf("真实对话（%s）失败: %w", model, err)
	}
	if r.RequestID == "" {
		return r, fmt.Errorf("真实对话（%s）没有返回服务端 requestId", model)
	}
	return r, nil
}

// Experts reads the expert market (expertType "agent" or "team"; "" for
// all): events that use an expert must name a real one.
func (c *Client) Experts(ctx context.Context, expertType string) ([]Expert, error) {
	body := map[string]any{"page": 1, "page_size": 20, "sort_by": "reco_rank", "sort_order": "desc"}
	if expertType != "" {
		body["expert_type"] = expertType
	}
	h := c.chatHeaders()
	status, env, err := c.call(ctx, http.MethodPost, c.chatBase()+"/portal/operation-platform/market/expert/list", h, body)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 || !env.okOr200() {
		return nil, &Error{Status: status, Msg: env.why()}
	}
	var got struct {
		Experts []Expert `json:"experts"`
	}
	env.data(&got)
	var out []Expert
	for _, e := range got.Experts {
		if e.ID != "" {
			if e.Type == "" {
				e.Type = expertType
			}
			if e.Name == "" {
				e.Name = e.Profession
			}
			out = append(out, e)
		}
	}
	return out, nil
}

// Scene is a task template scene.
type Scene struct{ ID, Name string }

// fallbackScenes are wb_tasks.TEMPLATE_FALLBACK_SCENES.
var fallbackScenes = []Scene{{"0", "幻灯片"}, {"4", "深度研究"}, {"8", "数据分析"}, {"16", "设计"}, {"20", "日常开发"}}

// Scenes reads the server's template scenes (their ids are what
// template_used counts), the measured table when the list can't be read.
func (c *Client) Scenes(ctx context.Context) []Scene {
	status, env, err := c.call(ctx, http.MethodGet, WebBase+"/console/as/support/scenes?locale=zh-CN", c.chatHeaders(), nil)
	if err == nil && status >= 200 && status < 300 {
		var got struct {
			Scenes []struct {
				ID   any    `json:"id"`
				Name string `json:"name"`
			} `json:"scenes"`
		}
		env.data(&got)
		var out []Scene
		for _, s := range got.Scenes {
			if s.ID == nil {
				continue
			}
			out = append(out, Scene{ID: fmt.Sprint(s.ID), Name: s.Name})
		}
		if len(out) > 0 {
			return out
		}
	}
	return append([]Scene(nil), fallbackScenes...)
}

// SetTheme applies an appearance theme (Hp_Appearance's theme).
func (c *Client) SetTheme(ctx context.Context, key string) error {
	h := c.chatHeaders()
	h.Set("Content-Type", "application/json;charset=UTF-8")
	return c.send(ctx, c.chatBase()+"/v2/user-asset/appearance/set", h, marshal(map[string]string{"kind": "theme", "resource_key": key}))
}

// growthPost is a growth-centre write on the chat host.
func (c *Client) growthPost(ctx context.Context, path string, body any) (*envelope, error) {
	return c.growth(ctx, http.MethodPost, path, body, false)
}

// AdoptBuddy agrees to the Buddy terms and takes the first Buddy
// (first_buddy's reward): a chat activity is reported first, which the
// adoption requires that day.
func (c *Client) AdoptBuddy(ctx context.Context) (string, error) {
	if err := c.ReportChat(ctx, ChatSendEvent(c.A.UID, "deepseek-v4-flash", "DeepSeek V4 Flash", "craft", 0, nowMS())); err != nil {
		return "", fmt.Errorf("前置活跃上报: %w", err)
	}
	if err := c.sleepGap(ctx); err != nil {
		return "", err
	}
	if _, err := c.growthPost(ctx, "/activity/growth/buddy/agreement", map[string]any{"agree": true}); err != nil {
		return "", fmt.Errorf("同意 Buddy 协议: %w", err)
	}
	if _, err := c.growthPost(ctx, "/activity/growth/buddy/first", map[string]any{}); err != nil {
		if e, ok := err.(*Error); ok && e.Status == http.StatusBadRequest && containsFold(e.Msg, "first_buddy task not completed") {
			return "领养门槛今天未达（需要当日活跃），明天会再试", nil
		}
		return "", fmt.Errorf("领取 Buddy: %w", err)
	}
	return "已领养第一只 Buddy", nil
}
