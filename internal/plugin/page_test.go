package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"github.com/Sakuralaaa/WorkBuddy-plugins/internal/wb"
)

func TestDueSlot(t *testing.T) {
	at := func(s string) time.Time {
		tm, err := time.ParseInLocation("2006-01-02 15:04", s, beijing)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	cases := []struct {
		now   string
		slots []string
		want  string
	}{
		{"2026-11-13 08:59", []string{"09:00", "21:00"}, ""},
		{"2026-11-13 09:00", []string{"09:00", "21:00"}, "2026-11-13 09:00"},
		{"2026-11-13 15:30", []string{"21:00", "9:00"}, "2026-11-13 09:00"},
		{"2026-11-13 23:10", []string{"09:00", "21:00"}, "2026-11-13 21:00"},
		{"2026-11-13 10:31", []string{"10:30", "bad"}, "2026-11-13 10:30"},
	}
	for _, c := range cases {
		if got := DueSlot(at(c.now).UTC(), c.slots); got != c.want {
			t.Errorf("%s %v: got %q want %q", c.now, c.slots, got, c.want)
		}
	}
}

func TestSlotsAndSwitches(t *testing.T) {
	c := (&Config{}).withDefaults()
	if got := strings.Join(c.slotsOf("cat_travel"), ","); got != "09:00,21:00" {
		t.Errorf("travel %s", got)
	}
	if got := c.slotsOf("checkin"); len(got) != 1 || got[0] != "00:05" {
		t.Errorf("checkin %v", got)
	}
	c.Schedule.CheckinAt = "8:30"
	if got := c.slotsOf("checkin"); got[0] != "08:30" {
		t.Errorf("checkin %v", got)
	}
	for _, j := range JobCatalog {
		if c.jobOn(j.Key) {
			t.Errorf("%s on by default", j.Key)
		}
	}
	c.Tasks.Growth = true
	if !c.jobOn("growth") || c.jobOn("campaigns") {
		t.Error("switches")
	}
}

func TestRing(t *testing.T) {
	r := Ring{Max: 3}
	for i := 0; i < 5; i++ {
		x := Run{Account: fmt.Sprint(i % 2)}
		x.Message = fmt.Sprint(i)
		r.Add(x)
	}
	if len(r.Runs) != 3 || r.Runs[0].Message != "2" {
		t.Fatalf("%v", r.Runs)
	}
	n := r.Newest(10, "0")
	if len(n) != 2 || n[0].Message != "4" || n[1].Message != "2" {
		t.Fatalf("%v", n)
	}
}

func TestPackageView(t *testing.T) {
	p := PackageView(map[string]any{"PackageName": "Pro", "CycleCapacitySizePrecise": "1000", "CycleCapacityRemainPrecise": "250.5",
		"AccountAttributes": []any{map[string]any{"Key": "grantReason", "Value": "活动发放"}}})
	if p.Size != 1000 || p.Remain != 250.5 || p.Used != 749.5 || p.Reason != "活动发放" {
		t.Fatalf("%+v", p)
	}
}

func TestNormKey(t *testing.T) {
	a := normKey(&wb.Account{Type: wb.ProviderAI, Source: "key", Key: "ck_abcdefgh12345678"})
	if a.Access != "ck_abcdefgh12345678" || !strings.HasPrefix(a.UID, "anonymous_") || a.Type != wb.ProviderAI {
		t.Fatalf("%+v", a)
	}
	o := &wb.Account{Access: "x", UID: "u"}
	if normKey(o) != o {
		t.Fatal("oauth account changed")
	}
}

func TestRegistrationHasPage(t *testing.T) {
	raw, _ := json.Marshal(managementRoutes())
	var reg struct {
		Routes    []pluginapi.ManagementRoute `json:"routes"`
		Resources []pluginapi.ResourceRoute   `json:"resources"`
	}
	if err := json.Unmarshal(raw, &reg); err != nil {
		t.Fatal(err)
	}
	if len(reg.Resources) != 1 || reg.Resources[0].Menu != "WorkBuddy" || reg.Resources[0].Path != "/index.html" {
		t.Fatalf("%+v", reg.Resources)
	}
	for _, r := range reg.Routes {
		if r.Menu != "" {
			t.Errorf("route %s carries a menu: it would become an unauthenticated resource", r.Path)
		}
		if !strings.HasPrefix(r.Path, ID+"/") || strings.ContainsAny(r.Path, ":*") {
			t.Errorf("bad route %s", r.Path)
		}
	}
}

func mgmtCall(t *testing.T, method, path string, body any) (int, []byte) {
	raw, _ := json.Marshal(map[string]any{"Method": method, "Path": path, "Body": mustJSON(body)})
	out, err := Handle(pluginabi.MethodManagementHandle, raw)
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		OK     bool `json:"ok"`
		Result struct {
			StatusCode int
			Body       []byte
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &env); err != nil || !env.OK {
		t.Fatalf("%s %s", out, err)
	}
	return env.Result.StatusCode, env.Result.Body
}

func mustJSON(v any) []byte {
	if v == nil {
		return nil
	}
	b, _ := json.Marshal(v)
	return b
}

// fakeHost answers the host calls the page's routes make.
func fakeHost(files map[string]string) Caller {
	return func(method string, payload any) (json.RawMessage, error) {
		switch method {
		case pluginabi.MethodHostAuthList:
			var out []pluginapi.HostAuthFileEntry
			for name, raw := range files {
				var head struct{ Type string }
				_ = json.Unmarshal([]byte(raw), &head)
				out = append(out, pluginapi.HostAuthFileEntry{ID: name, AuthIndex: "idx-" + name, Name: name, Provider: head.Type, Path: os.TempDir() + "/wbtest/" + name})
			}
			return json.Marshal(map[string]any{"files": out})
		case pluginabi.MethodHostAuthGet:
			p := payload.(map[string]string)
			name := strings.TrimPrefix(p["auth_index"], "idx-")
			return json.Marshal(map[string]any{"name": name, "json": json.RawMessage(files[name])})
		case pluginabi.MethodHostLog:
			return nil, nil
		}
		return nil, fmt.Errorf("unexpected host call %s", method)
	}
}

func TestPageRoutes(t *testing.T) {
	_ = os.MkdirAll(os.TempDir()+"/wbtest", 0o700)
	defer os.RemoveAll(os.TempDir() + "/wbtest")
	SetCaller(fakeHost(map[string]string{
		"workbuddy-magpie-u1.json":   `{"type":"workbuddy-magpie","access":"a","refresh":"r","expires":4102444800000,"uid":"u1","accountId":"张三 · 个人"}`,
		"workbuddy-magpie-ai-k.json": `{"type":"workbuddy-magpie-ai","source":"key","key":"ck_abcdefgh12345678"}`,
		"codex-someone.json":         `{"type":"codex","access_token":"x"}`,
	}))
	defer SetCaller(nil)
	_ = Configure(nil)

	code, body := mgmtCall(t, "GET", "/v0/resource/plugins/workbuddy-magpie/index.html", nil)
	if code != 200 || !strings.Contains(string(body), "cli-proxy-auth") {
		t.Fatalf("page %d", code)
	}

	code, body = mgmtCall(t, "GET", "/v0/management/workbuddy-magpie/accounts", nil)
	var acc struct{ Accounts []accountView }
	_ = json.Unmarshal(body, &acc)
	if code != 200 || len(acc.Accounts) != 2 {
		t.Fatalf("accounts %d %s", code, body)
	}
	byBuild := map[string]accountView{}
	for _, a := range acc.Accounts {
		byBuild[a.Build] = a
	}
	if cn := byBuild["cn"]; cn.UID != "u1" || cn.Source != "oauth" || len(cn.Applies) < 5 {
		t.Errorf("cn %+v", cn)
	}
	if ai := byBuild["ai"]; ai.Source != "key" || len(ai.Applies) != 0 {
		t.Errorf("ai key %+v", ai)
	}

	code, body = mgmtCall(t, "GET", "/v0/management/workbuddy-magpie/status", nil)
	if code != 200 || !strings.Contains(string(body), `"jobs"`) || !strings.Contains(string(body), "Expert_Philanthropy") {
		t.Fatalf("status %d %s", code, body)
	}

	code, _ = mgmtCall(t, "POST", "/v0/management/workbuddy-magpie/run", map[string]string{"job": "nope"})
	if code != 400 {
		t.Errorf("unknown job %d", code)
	}
	code, _ = mgmtCall(t, "GET", "/v0/management/workbuddy-magpie/nothing", nil)
	if code != 404 {
		t.Errorf("unknown route %d", code)
	}
}
