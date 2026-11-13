package jobs

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Sakuralaaa/WorkBuddy-plugins/internal/wb"
)

// WorkBuddy AI's daily activity (官方每日活跃): a short chat as the desktop
// client, and — the leg that is counted on the web channel — a real cloud
// agent session run to completion (wb_accounts.daily_chat /
// daily_chat_web, wb_webagent.run_turn).

func webConvs() string { return IntlBase + "/console/as/conversations/" }

// webTurnWait is how long a web session may take (tests shorten it).
var webTurnWait = 120 * time.Second

func (c *Client) intlWebHeaders() http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+c.A.Access)
	h.Set("X-User-Id", c.A.UID)
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/json, text/plain, */*")
	h.Set("Origin", IntlBase)
	h.Set("Referer", IntlBase+"/app")
	h.Set("User-Agent", webUA)
	return h
}

// RunIntlDaily runs the daily activity on a WorkBuddy AI account: the
// desktop chat on the first model that answers, then (web) the web session.
func (c *Client) RunIntlDaily(ctx context.Context, models []string, web bool, prompt string) Result {
	r := Result{Job: "intl_daily", At: time.Now()}
	if c.Site == nil || !c.Site.AI {
		r.OK, r.Outcome, r.Message = true, "skipped", "每日活跃只适用于国际版账号"
		return r
	}
	if c.Key() {
		r.OK, r.Outcome, r.Message = true, "skipped", "API Key 账号没有每日活跃"
		return r
	}
	if len(models) == 0 {
		models = []string{"deepseek-v4.1-flash"}
	}
	if prompt == "" {
		prompt = "Hi"
	}
	var used string
	var lastErr error
	for _, m := range models {
		if _, err := c.realChat(ctx, m, prompt, nil); err != nil {
			lastErr = err
			r.logf("桌面通道 %s: %v", m, err)
			if containsFold(err.Error(), "11140") {
				break
			}
			continue
		}
		used = m
		break
	}
	if used == "" {
		r.Outcome, r.Message = "failed", fmt.Sprintf("桌面通道对话失败: %v", lastErr)
		return r
	}
	r.logf("桌面通道对话完成（%s）", used)
	if !web {
		r.OK, r.Outcome, r.Message = true, "done", fmt.Sprintf("每日活跃对话完成（%s，网页通道未开启）", used)
		return r
	}
	res, err := c.WebSession(ctx, prompt, used)
	if err != nil {
		r.Outcome, r.Message = "failed", "网页通道失败: "+err.Error()
		return r
	}
	r.OK, r.Outcome = true, "done"
	r.Message = fmt.Sprintf("每日活跃完成：网页会话跑完（%s，%d 段输出，%d ms）", used, res.Chunks, res.ElapsedMS)
	return r
}

// WebResult is how a web session went.
type WebResult struct {
	Conversation string `json:"conversation"`
	Status       string `json:"status"`
	Chunks       int64  `json:"chunks"`
	Events       int64  `json:"events"`
	ElapsedMS    int64  `json:"elapsed_ms"`
}

func (c *Client) webJSON(ctx context.Context, method, u string, body any) (int, *envelope, error) {
	return c.call(ctx, method, u, c.intlWebHeaders(), body)
}

// WebSession creates a web conversation, drives its sandbox over ACP
// (streamable HTTP: one SSE stream for events, POSTs for requests) until the
// console says completed, then stops it (deleting a failed one).
func (c *Client) WebSession(ctx context.Context, prompt, model string) (*WebResult, error) {
	body := map[string]any{
		"prompt": prompt, "model": model, "conversationOrigin": "workbuddy-app",
		"plugins": []map[string]string{{"name": "weixinpay", "marketplace": "codebuddy-builtin"}},
	}
	conv, err := c.webCreate(ctx, body)
	if err != nil && strings.Contains(err.Error(), "limit") {
		if n := c.webPrune(ctx, prompt); n > 0 {
			conv, err = c.webCreate(ctx, body)
		}
	}
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		c.webAction(cctx, conv, "stop")
		if !keep {
			c.webAction(cctx, conv, "delete")
		}
	}()
	_, env, err := c.webJSON(ctx, http.MethodGet, webConvs()+url.PathEscape(conv)+"/session", nil)
	if err != nil {
		return nil, fmt.Errorf("沙箱查询失败: %w", err)
	}
	var s struct {
		Link      string `json:"link"`
		Endpoint  string `json:"endpoint"`
		Token     string `json:"token"`
		SessionID string `json:"sessionId"`
		Session2  string `json:"session_id"`
		Cwd       string `json:"cwd"`
	}
	env.data(&s)
	link := firstOf(s.Link, s.Endpoint)
	sid := firstOf(s.SessionID, s.Session2, conv)
	cwd := firstOf(s.Cwd, "/workspace")
	if link == "" || s.Token == "" {
		return nil, fmt.Errorf("沙箱未就绪（没有 link/token）")
	}
	res, err := c.runTurn(ctx, conv, link, s.Token, sid, cwd, prompt, model)
	if err != nil {
		return res, err
	}
	keep = true
	return res, nil
}

func firstOf(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

func (c *Client) webCreate(ctx context.Context, body any) (string, error) {
	status, env, err := c.webJSON(ctx, http.MethodPost, webConvs(), body)
	if err != nil {
		return "", err
	}
	if status == 429 || env.codeText() == "10105" {
		return "", fmt.Errorf("active conversation limit: %s", env.why())
	}
	if status < 200 || status >= 300 {
		return "", &Error{Status: status, Msg: env.why()}
	}
	var d struct {
		ID string `json:"id"`
	}
	env.data(&d)
	if (env.codeText() != "" && !env.ok()) || d.ID == "" {
		return "", fmt.Errorf("建会话失败: %s", env.why())
	}
	return d.ID, nil
}

func (c *Client) webAction(ctx context.Context, conv, action string) bool {
	if conv == "" {
		return false
	}
	_, env, err := c.webJSON(ctx, http.MethodPost, webConvs()+url.PathEscape(conv)+"/"+action, map[string]any{})
	return err == nil && (env.codeText() == "" || env.ok())
}

// webPrune stops the oldest still-working sessions this activity left (the
// account holds ten at most).
func (c *Client) webPrune(ctx context.Context, prompt string) int {
	_, env, err := c.webJSON(ctx, http.MethodGet, webConvs()+"?page=1&pageSize=50", nil)
	if err != nil {
		return 0
	}
	var d struct {
		Conversations []map[string]any `json:"conversations"`
	}
	env.data(&d)
	n := 0
	for i := len(d.Conversations) - 1; i >= 0 && n < 3; i-- {
		cv := d.Conversations[i]
		st := fmt.Sprint(cv["status"])
		title := fmt.Sprint(cv["title"])
		if st != "working" && st != "CREATING" && st != "creating" {
			continue
		}
		if title != prompt && fmt.Sprint(cv["prompt"]) != prompt {
			continue
		}
		if c.webAction(ctx, fmt.Sprint(cv["id"]), "stop") {
			n++
		}
	}
	return n
}

func (c *Client) webStatus(ctx context.Context, conv string) string {
	_, env, err := c.webJSON(ctx, http.MethodGet, webConvs()+url.PathEscape(conv), nil)
	if err != nil {
		return ""
	}
	var d struct {
		Status string `json:"status"`
	}
	env.data(&d)
	return d.Status
}

func (c *Client) runTurn(ctx context.Context, conv, link, token, sid, cwd, prompt, model string) (*WebResult, error) {
	started := time.Now()
	res := &WebResult{Conversation: conv}
	ctx, cancel := context.WithTimeout(ctx, webTurnWait+30*time.Second)
	defer cancel()
	h := http.Header{}
	h.Set("Accept", "text/event-stream")
	h.Set("Authorization", "Bearer "+token)
	h.Set("User-Agent", webUA)
	stream, err := c.D.DoStream(ctx, &wb.Request{Method: http.MethodGet, URL: link, Header: h})
	if err != nil {
		return res, fmt.Errorf("SSE 通道: %w", err)
	}
	defer stream.Body.Close()
	if stream.Status != 200 {
		return res, fmt.Errorf("SSE 通道返回 HTTP %d", stream.Status)
	}
	connID := stream.Header.Get("Acp-Connection-Id")
	if connID == "" {
		return res, fmt.Errorf("SSE 通道没有返回 Acp-Connection-Id")
	}
	var events, chunks atomic.Int64
	go func() {
		sc := bufio.NewScanner(stream.Body)
		sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			var m struct {
				Method string `json:"method"`
				Params struct {
					Update struct {
						Kind string `json:"sessionUpdate"`
					} `json:"update"`
				} `json:"params"`
			}
			if json.Unmarshal([]byte(strings.TrimSpace(line[5:])), &m) != nil {
				continue
			}
			if m.Method == "session/update" {
				events.Add(1)
				if m.Params.Update.Kind == "agent_message_chunk" {
					chunks.Add(1)
				}
			}
		}
	}()
	rpc := func(id int, method string, params any) error {
		ph := http.Header{}
		ph.Set("Content-Type", "application/json")
		ph.Set("Accept", "application/json, text/event-stream")
		ph.Set("Acp-Connection-Id", connID)
		ph.Set("Authorization", "Bearer "+token)
		ph.Set("User-Agent", webUA)
		b := marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		rctx, rcancel := context.WithTimeout(ctx, 30*time.Second)
		defer rcancel()
		out, err := c.D.Do(rctx, &wb.Request{Method: http.MethodPost, URL: link, Header: ph, Body: b})
		if err != nil {
			return err
		}
		if out.Status != 200 && out.Status != 202 {
			return fmt.Errorf("%s 返回 HTTP %d", method, out.Status)
		}
		return nil
	}
	caps := map[string]any{"fs": map[string]bool{"readTextFile": false, "writeTextFile": false}, "terminal": false}
	if err := rpc(1, "initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": caps}); err != nil {
		return res, err
	}
	if err := rpc(2, "session/load", map[string]any{"sessionId": sid, "cwd": cwd, "mcpServers": []any{}}); err != nil {
		return res, err
	}
	if model != "" {
		_ = rpc(3, "session/set_model", map[string]any{"sessionId": sid, "modelId": model})
	}
	if err := rpc(4, "session/prompt", map[string]any{"sessionId": sid, "prompt": []map[string]string{{"type": "text", "text": prompt}}}); err != nil {
		return res, err
	}
	deadline := time.Now().Add(webTurnWait)
	for time.Now().Before(deadline) {
		st := c.webStatus(ctx, conv)
		res.Status = st
		if st == "completed" {
			break
		}
		if st == "failed" || st == "error" {
			break
		}
		if pause(ctx, 3) != nil {
			break
		}
	}
	res.Chunks, res.Events = chunks.Load(), events.Load()
	res.ElapsedMS = time.Since(started).Milliseconds()
	if res.Status != "completed" {
		return res, fmt.Errorf("会话在 %s 内没有跑完（状态=%s）", webTurnWait, firstOf(res.Status, "未知"))
	}
	return res, nil
}
