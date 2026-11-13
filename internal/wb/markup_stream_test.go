package wb

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestChatStreamRepairSplitMarkup(t *testing.T) {
	block := markupOpen + "\n" + "<" + markupPipe + "DSML" + markupPipe + ` invoke name="exec_command">` + "\n" +
		"<" + markupPipe + "DSML" + markupPipe + ` parameter name="cmd" string="true">ls -la` + markupParamClose + "\n" +
		markupInvokeClose + "\n" + markupClose
	text := "好的，我来看看。" + block
	cut := len("好的，我来看看。") + 7
	frame := func(s string) string {
		b, _ := json.Marshal(map[string]any{"id": "c1", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": s}}}})
		return "data: " + string(b) + "\n\n"
	}
	in := frame(text[:cut]) + frame(text[cut:]) +
		`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" + "data: [DONE]\n\n"
	var got []string
	if err := ChatStreamRepair(strings.NewReader(in), nil, NewMarkupRepair(nil, true), func(ev string) error { got = append(got, ev); return nil }); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(got, "\n")
	if strings.Contains(all, "DSML") {
		t.Fatalf("markup leaked: %s", all)
	}
	if !strings.Contains(all, `"name":"exec_command"`) || !strings.Contains(all, `"finish_reason":"tool_calls"`) || !strings.Contains(all, "好的，我来看看。") {
		t.Fatalf("not restored: %s", all)
	}
	if got[len(got)-1] != "data: [DONE]" {
		t.Fatalf("last = %q", got[len(got)-1])
	}
}

func TestChatStreamRepairPlainUntouched(t *testing.T) {
	in := `data: {"id":"c1","choices":[{"index":0,"delta":{"content":"hi"}}]}` + "\n\n" + "data: [DONE]\n\n"
	var got []string
	_ = ChatStreamRepair(strings.NewReader(in), nil, NewMarkupRepair(nil, true), func(ev string) error { got = append(got, ev); return nil })
	if len(got) != 2 || got[0] != `data: {"id":"c1","choices":[{"index":0,"delta":{"content":"hi"}}]}` {
		t.Fatalf("got %q", got)
	}
}

func TestBadgesOf(t *testing.T) {
	cfg := []byte(`{"agents":[{"name":"cli","models":["ds","hy3"]}],
	"models":[{"id":"ds","name":"DS","credits":"x0.51","supportsToolCall":true,"supportsReasoning":true,"reasoning":{"supportedEfforts":["low","high"],"canDisableThinking":true}},
	          {"id":"hy3","name":"Hy3","credits":"x0.2","supportsReasoning":true,"onlyReasoning":true,"reasoning":{"supportedEfforts":["low","high"],"canDisableThinking":false}}],
	"modelPromotions":[
	 {"kind":"discount","enabled":true,"priority":10,"modelIds":["ds"],"badge":{"label":"错峰使用"},"hover":{"textZh":"夜间五折"},"schedule":{"daily":[{"start":"23:00","end":"7:50"}]},"discount":{"factor":0.5}},
	 {"kind":"discount","enabled":true,"priority":5,"modelIds":["hy3"],"badge":{"label":"限时免费"},"discount":{"discountedCredits":"0x"}}]}`)
	var c Config
	if err := json.Unmarshal(cfg, &c); err != nil {
		t.Fatal(err)
	}
	noon := time.Date(2026, 11, 13, 12, 0, 0, 0, promoZone).UnixMilli()
	list := ParseConfig(SiteCN(), &c, noon)
	by := map[string]Model{}
	for _, m := range list {
		by[m.ID] = m
	}
	ds := by["ds"]
	if !ds.Tools || ds.AlwaysThink || len(ds.Badges) != 1 || ds.Badges[0].Label != "错峰使用" || ds.Badges[0].Active || ds.Badges[0].Hours[0] != "23:00-07:50" {
		t.Fatalf("ds %+v", ds)
	}
	if r := ds.Badges[0].Rate; r == nil || *r != 0.255 {
		t.Fatalf("ds rate %v", r)
	}
	hy := by["hy3"]
	if !hy.AlwaysThink || !hy.Free || len(hy.Badges) != 1 || !hy.Badges[0].Active || hy.RateWas != 0.2 {
		t.Fatalf("hy3 %+v", hy)
	}
	night := time.Date(2026, 11, 13, 23, 30, 0, 0, promoZone).UnixMilli()
	if b := BadgesOf(c.Models[0], c.ModelPromotions, night); len(b) != 1 || !b[0].Active {
		t.Fatalf("night %+v", b)
	}
}
