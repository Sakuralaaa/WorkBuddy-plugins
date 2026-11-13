package wb

import (
	"encoding/json"
	"strings"
	"testing"
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
