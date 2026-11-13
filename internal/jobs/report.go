package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Sakuralaaa/WorkBuddy-plugins/internal/wb"
)

// Event is one telemetry event for /v2/report; the client's fingerprint is
// laid under it when it is sent.
type Event map[string]any

// DesktopFingerprint is the desktop client's envelope every desktop event
// carries (wb_fingerprint.get_desktop_fingerprint, panel desktopFingerprint),
// at the build's own app version.
func DesktopFingerprint(site *wb.Site, uid, nick string, now int64) Event {
	ver := "5.9.3"
	if site != nil && site.App != "" {
		ver = site.App
	}
	return Event{
		"timezone": "Asia/Shanghai", "reportDelay": 2000,
		"userId": uid, "username": nick, "userNickname": nick,
		"product": "SaaS", "releaseDate": int64(1789036585355),
		"commit":  "5f9692923c93033111c51ad7b003eb80204a9b75",
		"ideName": "WorkBuddy", "ideType": "WorkBuddy", "ideVersion": ver,
		"machineId": DeriveID(uid, "machine"), "sessionId": DeriveID(uid, "session"),
		"extName": "workbuddy-desktop", "extVersion": ver,
		"os": "win32", "arch": "x64", "osVersion": "10.0.26220",
		"cpuCores": 20, "memorySize": 24,
		"timestamp": now, "presentAt": now,
	}
}

// MPFingerprint is the WeChat mini program's envelope (wb_tasks.mp_base).
func MPFingerprint(uid, nick string, now int64) Event {
	m := "guest"
	if uid != "" {
		m = uid
		if len(m) > 16 {
			m = m[:16]
		}
	}
	return Event{
		"timestamp": now, "ideType": "WorkBuddy_MP", "ideVersion": "2.4.0",
		"extName": "workbuddy-mp", "extVersion": "2.4.0", "product": "SaaS",
		"ideName": "wx_app_cloud", "platform": "mini_program", "source": "mini_program",
		"os": "android", "osVersion": "14", "arch": "arm64",
		"machineId": "mp-" + m, "timezone": "Asia/Shanghai",
		"userId": uid, "userNickname": nick,
	}
}

// Enveloped lays each event over the envelope (the event's own keys win).
func Enveloped(base Event, events []Event) []Event {
	out := make([]Event, 0, len(events))
	for _, ev := range events {
		m := Event{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range ev {
			m[k] = v
		}
		out = append(out, m)
	}
	return out
}

func marshal(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return bytes.TrimRight(buf.Bytes(), "\n")
}

// post sends a report and says whether the server took it: 2xx and, when
// there is a JSON envelope, code 0 (the desktop endpoint often answers with
// an empty body).
func (c *Client) send(ctx context.Context, url string, h http.Header, body []byte) error {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := c.D.Do(cctx, &wb.Request{Method: http.MethodPost, URL: url, Header: h, Body: body})
	if err != nil {
		return err
	}
	if res.Status < 200 || res.Status >= 300 {
		return &Error{Status: res.Status, Msg: snippet(res.Body)}
	}
	if b := bytes.TrimSpace(res.Body); len(b) > 0 && b[0] == '{' {
		env := &envelope{}
		if json.Unmarshal(b, env) == nil && len(env.Code) > 0 && !env.okOr200() {
			return &Error{Status: res.Status, Msg: env.why()}
		}
	}
	return nil
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// ReportDesktop sends events as the desktop client does (POST <chat
// host>/v2/report, the desktop fingerprint on each).
func (c *Client) ReportDesktop(ctx context.Context, events ...Event) error {
	if len(events) == 0 {
		return fmt.Errorf("no events")
	}
	arr := Enveloped(DesktopFingerprint(c.Site, c.A.UID, c.Nick, nowMS()), events)
	h := c.chatHeaders()
	h.Set("Content-Type", "application/json;charset=UTF-8")
	h.Set("Accept", "application/json, text/plain, */*")
	h.Set("User-Agent", wb.CLIAgent(c.Site))
	h.Set("X-Product", "SaaS")
	h.Set("X-Request-ID", RequestID())
	return c.send(ctx, c.chatBase()+"/v2/report", h, marshal(arr))
}

// mpReportHeaders are the mini program's report headers
// (wb_tasks.MP_REPORT_HEADERS).
func (c *Client) mpReportHeaders() http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+c.A.Access)
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/json")
	h.Set("X-Client-Product", "workbuddy-mp")
	h.Set("X-Client-Version", "2.4.0")
	h.Set("X-Client-Platform", "mp-weixin")
	h.Set("X-Platform", "wechatmp")
	h.Set("X-User-Id", c.A.UID)
	wb.InjectDeviceToken(h, c.A.DeviceToken, c.DeviceToken, "")
	return h
}

// ReportMP sends events as the mini program does to base/v2/report; page
// adds the page they came from (a campaign's events are keyed by it).
func (c *Client) ReportMP(ctx context.Context, base string, page map[string]any, events ...Event) error {
	if len(events) == 0 {
		return fmt.Errorf("no events")
	}
	env := MPFingerprint(c.A.UID, c.Nick, nowMS())
	for k, v := range page {
		env[k] = v
	}
	arr := Enveloped(env, events)
	if base == "" {
		base = c.chatBase()
	}
	return c.send(ctx, base+"/v2/report", c.mpReportHeaders(), marshal(arr))
}

// ReportWeb sends one web console event (Library_read's page click).
func (c *Client) ReportWeb(ctx context.Context, ev Event) error {
	ev["timestamp"] = nowMS()
	ev["reportDelay"] = 0
	ev["os"] = "Win32"
	ev["arch"] = ""
	ev["osVersion"] = "10.0"
	ev["userAgent"] = webUA
	ev["machineId"] = DeriveID(c.A.UID, "webmachine")
	ev["userId"] = c.A.UID
	ev["userNickname"] = c.Nick
	ev["enterpriseId"] = c.A.EnterpriseID
	h := c.webHeaders()
	h.Set("x-client-platform", "web")
	if u, ok := ev["pageURL"].(string); ok && u != "" {
		h.Set("Referer", u)
	}
	return c.send(ctx, c.webBase()+"/v2/report", h, marshal([]Event{ev}))
}

// ChatSendEvent is the client's chat_request_send (wb_tasks.build_event
// "chat"/"glmchat"/"cat"): it lights the growth streak, chat_5's count and,
// with GLM-5.2 in the night window, the night-owl task.
func ChatSendEvent(uid, model, modelName, mode string, idx int, now int64) Event {
	cid := fmt.Sprintf("wb-task-%d-%d", now, idx)
	return Event{
		"eventCode": "chat_request_send", "timestamp": now, "reportDelay": 0,
		"mode": mode, "conversationId": cid, "requestId": cid + "-req",
		"inputLength": 12, "requestModelId": model, "requestModelName": modelName,
		"isPlan": false, "agentName": "default", "agentType": "conversation",
		"userId": uid,
	}
}

// ReportChat sends chat_request_send events as the plain report the client
// sends after a chat (the chat host, the account's signed headers).
func (c *Client) ReportChat(ctx context.Context, events ...Event) error {
	return c.send(ctx, c.chatBase()+"/v2/report", c.chatHeaders(), marshal(events))
}
