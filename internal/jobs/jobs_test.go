package jobs

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sakuralaaa/WorkBuddy-plugins/internal/wb"
)

func TestParseTasks(t *testing.T) {
	raw := json.RawMessage(`{"tasks":[
		{"task_code":"chat_5","title":"对话5次","reward_credit":100,"reward_energy":5,"accept_status":"accepted","progress":{"current":5,"target":5}},
		{"task_code":"first_buddy","task_type":"auto","current":0,"target":1},
		{"task_code":"x","accept_status":"claimed","progress":null,"target":1,"current":1},
		{"task_code":"","title":"dropped"}]}`)
	list, err := ParseTasks(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("got %d tasks", len(list))
	}
	if !list[0].Claimable || list[0].Credit != 100 || list[0].Energy != 5 || list[0].Current != 5 {
		t.Errorf("chat_5: %+v", list[0])
	}
	if list[1].AcceptStatus != "not_accepted" || list[1].Type != "auto" || list[1].Claimable {
		t.Errorf("first_buddy: %+v", list[1])
	}
	if !list[2].Claimed || list[2].Claimable || !list[2].Done() {
		t.Errorf("claimed: %+v", list[2])
	}
}

func TestExpertFromJump(t *testing.T) {
	cases := map[string]string{
		"workbuddy://expert?expertId=ex_xQHHD1u5Jkua":                                     "ex_xQHHD1u5Jkua",
		"workbuddy://open?deeplink=workbuddy%3A%2F%2Fexpert%3FexpertId%3Dex_GDl31WahyIH0": "ex_GDl31WahyIH0",
		"workbuddy://expert?expertId=WorkspaceBuilder":                                    "",
		"": "",
	}
	for in, want := range cases {
		got, ok := ExpertFromJump("x", in, "")
		if got != want || ok != (want != "") {
			t.Errorf("%q: got %q %v want %q", in, got, ok, want)
		}
	}
}

func TestEnvelopedKeepsEventKeys(t *testing.T) {
	out := Enveloped(Event{"a": 1, "userId": "u"}, []Event{{"a": 2, "eventCode": "x"}})
	if out[0]["a"] != 2 || out[0]["userId"] != "u" || out[0]["eventCode"] != "x" {
		t.Fatalf("%v", out[0])
	}
	fp := DesktopFingerprint(wb.SiteCN(), "uid1", "nick", 5)
	if fp["ideVersion"] != wb.SiteCN().App || fp["machineId"] != DeriveID("uid1", "machine") {
		t.Fatalf("%v", fp)
	}
}

func TestRedeemable(t *testing.T) {
	var s StreakState
	_ = json.Unmarshal([]byte(`{"redemption_status":{"tier_7d_status":"available","tier_14d_status":"locked","tier_28d_status":"claimed","tiers":[{"tier":"7d"},{"tier":"14d"},{"tier":"28d"}]}}`), &s)
	r := s.Redeemable()
	if len(r) != 1 || r[0].Tier != "7d" {
		t.Fatalf("%v", r)
	}
}

func TestEventShapes(t *testing.T) {
	seq := DesktopChatSequence("c", "r", "m", "fast-model", "fast-model", 1)
	if len(seq) != 6 || seq[0]["eventCode"] != "agent_task_created" || seq[5]["eventCode"] != "chat_request_response" {
		t.Fatal("chat sequence")
	}
	if n := len(TemplateEvents("4", "深度研究", "c", "r", "e")); n != 4 {
		t.Fatalf("template group %d", n)
	}
	if ev := MPChatEvent("c", "abcdefghij"); ev["messageId"] != "msg-cdefghij" || ev["agentName"] != "mp" {
		t.Fatalf("%v", ev)
	}
	use := ExpertUseEvent(Expert{ID: "ex_1", Type: "agent", Name: "n"}, "c", "cmb-0123456789abcdef0123456789abcdef", "craft")
	if use["requestId"] != "cmb-0123456789abcdef0123456789abcdef" || use["messageId"] != "msg-89abcdef" {
		t.Fatalf("%v", use)
	}
	for _, h := range HumanTasks {
		if ActionFor(h.Code) != nil {
			t.Errorf("%s is human-only but has an action", h.Code)
		}
	}
}

// fake is a growth centre: one chat_5 task that each reported chat event
// moves, and a claim that pays once.
type fake struct {
	mu       sync.Mutex
	current  int
	accepted bool
	claimed  bool
	reports  int
	paths    []string
}

func (f *fake) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.paths = append(f.paths, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("%s without the account's token", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		ok := func(data any) {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "OK", "data": data})
		}
		switch {
		case r.URL.Path == "/v2/activity/growth/tasks":
			st := "not_accepted"
			if f.accepted {
				st = "accepted"
			}
			if f.claimed {
				st = "claimed"
			}
			ok(map[string]any{"tasks": []any{map[string]any{"task_code": "chat_5", "title": "对话5次", "accept_status": st, "reward_credit": 100, "progress": map[string]any{"current": f.current, "target": 5}}}})
		case r.URL.Path == "/v2/activity/growth/tasks/accept":
			if !strings.Contains(string(body), "chat_5") {
				t.Errorf("accept body %s", body)
			}
			f.accepted = true
			ok(map[string]any{"results": []any{map[string]any{"task_code": "chat_5", "status": "accepted"}}})
		case r.URL.Path == "/v2/report":
			var evs []map[string]any
			if err := json.Unmarshal(body, &evs); err != nil || len(evs) == 0 || evs[0]["userId"] != "u1" {
				t.Errorf("report body %s", body)
			}
			f.reports++
			if f.accepted && f.current < 5 {
				f.current++
			}
			ok(nil)
		case r.URL.Path == "/activity/growth/tasks/chat_5/claim":
			if f.current < 5 {
				w.WriteHeader(400)
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 400, "msg": "task not completed"})
				return
			}
			f.claimed = true
			ok(map[string]any{"credit": 100, "energy": 5})
		default:
			w.WriteHeader(404)
		}
	})
}

func testClient(url string) *Client {
	ChatBase, BillBase, WebBase, CampaignBase, IntlBase = url, url, url, url, url
	SetGapScale(0)
	return &Client{D: wb.NetDoer{}, Site: wb.SiteCN(), A: &wb.Account{Access: "tok", UID: "u1"}, Nick: "n"}
}

func TestRunGrowthTaskChat5(t *testing.T) {
	f := &fake{current: 2}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	c := testClient(srv.URL)
	r := c.RunGrowthTask(context.Background(), "chat_5", true)
	if !r.OK || r.Outcome != "claimed" || r.Credits != 100 {
		t.Fatalf("result %+v", r)
	}
	if f.reports != 3 || !f.claimed {
		t.Fatalf("reports %d claimed %v", f.reports, f.claimed)
	}
	// claimed: a second run does nothing
	f.paths = nil
	r = c.RunGrowthTask(context.Background(), "chat_5", true)
	if r.Outcome != "done" || len(f.paths) != 1 {
		t.Fatalf("second run %+v %v", r, f.paths)
	}
}

func TestRunGrowthTaskNoClaim(t *testing.T) {
	f := &fake{current: 5, accepted: true}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	c := testClient(srv.URL)
	r := c.RunGrowthTask(context.Background(), "chat_5", false)
	if f.claimed || r.Outcome != "done" {
		t.Fatalf("claimed with claim off: %+v", r)
	}
}

func TestGrowthSkipsOtherBuilds(t *testing.T) {
	c := &Client{Site: wb.SiteAI(), A: &wb.Account{Access: "t"}}
	if r := c.RunGrowthTask(context.Background(), "chat_5", true); r.Outcome != "skipped" {
		t.Fatal(r)
	}
	c = &Client{Site: wb.SiteCN(), A: &wb.Account{Source: "key"}}
	if r := c.RunGrowthAll(context.Background(), false, true); r.Outcome != "skipped" {
		t.Fatal(r)
	}
}

func TestHumanTaskNotFaked(t *testing.T) {
	var reported bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/report" {
			reported = true
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"tasks": []any{map[string]any{"task_code": "Expert_Philanthropy", "accept_status": "accepted", "progress": map[string]any{"current": 0, "target": 1}}}}})
	}))
	defer srv.Close()
	c := testClient(srv.URL)
	r := c.RunGrowthTask(context.Background(), "Expert_Philanthropy", true)
	if r.Outcome != "pending" || reported || len(r.Pending) == 0 {
		t.Fatalf("%+v reported=%v", r, reported)
	}
}

// TestWebSession drives a fake cloud agent: the SSE stream must stay open
// while the JSON-RPC posts go out, and the session completes.
func TestWebSession(t *testing.T) {
	var mu sync.Mutex
	var rpc []string
	status := "working"
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/console/as/conversations/":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"id": "cv1"}})
		case r.URL.Path == "/console/as/conversations/cv1/session":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"link": srv.URL + "/acp", "token": "sbx", "sessionId": "s1"}})
		case r.URL.Path == "/console/as/conversations/cv1":
			mu.Lock()
			st := status
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"status": st}})
		case r.URL.Path == "/console/as/conversations/cv1/stop", r.URL.Path == "/console/as/conversations/cv1/delete":
			mu.Lock()
			rpc = append(rpc, r.URL.Path)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0})
		case r.Method == "GET" && r.URL.Path == "/acp":
			if r.Header.Get("Authorization") != "Bearer sbx" {
				t.Error("sandbox token")
			}
			w.Header().Set("Acp-Connection-Id", "conn1")
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			fl := w.(http.Flusher)
			fl.Flush()
			for i := 0; i < 3; i++ {
				fmt.Fprintf(w, "data: {\"method\":\"session/update\",\"params\":{\"update\":{\"sessionUpdate\":\"agent_message_chunk\"}}}\n\n")
				fl.Flush()
				time.Sleep(20 * time.Millisecond)
			}
			<-r.Context().Done()
		case r.Method == "POST" && r.URL.Path == "/acp":
			if r.Header.Get("Acp-Connection-Id") != "conn1" {
				t.Error("connection id")
			}
			var m struct {
				Method string `json:"method"`
			}
			_ = json.NewDecoder(bufio.NewReader(r.Body)).Decode(&m)
			mu.Lock()
			rpc = append(rpc, m.Method)
			if m.Method == "session/prompt" {
				status = "completed"
			}
			mu.Unlock()
			w.WriteHeader(202)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := testClient(srv.URL)
	c.Site = wb.SiteAI()
	res, err := c.WebSession(context.Background(), "Hi", "deepseek-v4.1-flash")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "completed" {
		t.Fatalf("%+v", res)
	}
	mu.Lock()
	defer mu.Unlock()
	got := strings.Join(rpc, ",")
	if !strings.HasPrefix(got, "initialize,session/load,session/set_model,session/prompt") || !strings.Contains(got, "/stop") || strings.Contains(got, "/delete") {
		t.Fatalf("calls %s", got)
	}
}

func TestIntlDailyNeedsChat(t *testing.T) {
	c := &Client{Site: wb.SiteAI(), A: &wb.Account{Access: "t", UID: "u"}}
	SetGapScale(0)
	r := c.RunIntlDaily(context.Background(), []string{"m"}, false, "Hi")
	if r.OK || r.Outcome != "failed" {
		t.Fatalf("%+v", r)
	}
	c.Chat = func(ctx context.Context, model, prompt string, h http.Header) (ChatReply, error) {
		return ChatReply{RequestID: "0123456789abcdef0123456789abcdef"}, nil
	}
	r = c.RunIntlDaily(context.Background(), []string{"m"}, false, "Hi")
	if !r.OK || r.Outcome != "done" {
		t.Fatalf("%+v", r)
	}
}
