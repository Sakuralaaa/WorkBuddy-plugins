package wb

// The port of magpie's sent.test.mjs (plugins#461): what goes to WorkBuddy
// for a set of realistic chats is held to what magpie's workbuddy 0.1.43
// sent, byte for byte — testdata/sent-0.1.43.json has each body by its
// sha256 and each derived id by which requests share it. The chats and the
// fake upstream are testdata/sent.mjs's.

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sakuralaaa/WorkBuddy-plugins/internal/jsonx"
)

const sentConfig = `{"code":0,"data":{"agents":[{"name":"cli","models":["deepseek-v4.1-flash","hy3","gemini-sent-461","glm-5.2"]}],"models":[{"id":"deepseek-v4.1-flash","credits":"x0.00","supportsReasoning":true,"onlyReasoning":true,"reasoning":{"canDisableThinking":true,"defaultEffort":"high","supportedEfforts":["low","high","max"]}},{"id":"hy3","credits":"x0.00"},{"id":"gemini-sent-461","credits":"x0.00"},{"id":"glm-5.2","credits":"x0.79","supportsReasoning":true,"reasoning":{"defaultEffort":"xhigh","supportedEfforts":["high","xhigh"]}}]}}`

const afterToolsAnswer = `{"error":{"message":"the conversation must end with a user message","code":11153}}`
const refusalAnswer = `{"error":{"message":"unapproved channel","code":14018}}`

var grown = regexp.MustCompile(`,"extra_vars":(\{"growthEvent":"(?:[^"\\]|\\.)*","initiator":"user"\})\}\s*$`)

type sentRow struct {
	URL          string `json:"url"`
	Encoding     any    `json:"encoding"`
	Body         string `json:"body"`
	Conversation any    `json:"conversation"`
	Turn         any    `json:"turn"`
	Growth       any    `json:"growth,omitempty"`
}

type sentChat struct {
	Label  string    `json:"label"`
	Status int       `json:"status"`
	Sent   []sentRow `json:"sent"`
}

type fakeUpstream struct {
	mu   sync.Mutex
	sent []sentRow
}

func (f *fakeUpstream) answer(req *Request) (int, http.Header, []byte) {
	u := req.URL
	h := http.Header{}
	if strings.HasSuffix(u, "/v3/config") {
		h.Set("Content-Type", "application/json")
		return 200, h, []byte(sentConfig)
	}
	raw := req.Body
	if len(raw) > 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		zr, _ := gzip.NewReader(bytes.NewReader(raw))
		raw, _ = io.ReadAll(zr)
	}
	s := string(raw)
	row := sentRow{URL: u, Conversation: nilIfEmpty(req.Header.Get("X-Conversation-ID")), Turn: nilIfEmpty(req.Header.Get("X-Conversation-Request-ID"))}
	if enc := req.Header.Get("Content-Encoding"); enc != "" {
		row.Encoding = enc
	}
	if m := grown.FindStringSubmatchIndex(s); m != nil {
		vars := s[m[2]:m[3]]
		s = s[:m[0]] + s[m[1]-1:]
		var v struct {
			GrowthEvent string `json:"growthEvent"`
		}
		_ = json.Unmarshal([]byte(vars), &v)
		var ev []map[string]any
		_ = json.Unmarshal([]byte(v.GrowthEvent), &ev)
		if len(ev) != 1 || ev[0]["eventCode"] != "chat_request_send" || ev[0]["id"] != req.Header.Get("X-Conversation-ID") {
			row.Growth = ev
		}
	}
	sum := sha256.Sum256([]byte(s))
	row.Body = hex.EncodeToString(sum[:])
	f.mu.Lock()
	f.sent = append(f.sent, row)
	f.mu.Unlock()
	h.Set("Content-Type", "application/json")
	if strings.Contains(s, "REFUSE-461") && strings.Contains(s, `"role":"system","content":"REFUSE-461`) {
		return 400, h, []byte(refusalAnswer)
	}
	if strings.Contains(s, `"gemini-sent-461"`) {
		var b struct {
			Messages []map[string]any `json:"messages"`
		}
		_ = json.Unmarshal([]byte(s), &b)
		i := len(b.Messages) - 1
		for i >= 0 && b.Messages[i]["role"] == "user" {
			i--
		}
		if i < len(b.Messages)-1 && i >= 0 && b.Messages[i]["role"] == "tool" {
			return 400, h, []byte(afterToolsAnswer)
		}
	}
	h.Set("Content-Type", "text/event-stream")
	return 200, h, []byte("data: [DONE]\n\n")
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (f *fakeUpstream) Do(_ context.Context, req *Request) (*Response, error) {
	st, h, b := f.answer(req)
	return &Response{Status: st, Header: h, Body: b}, nil
}

func (f *fakeUpstream) DoStream(_ context.Context, req *Request) (*StreamResponse, error) {
	st, h, b := f.answer(req)
	return &StreamResponse{Status: st, Header: h, Body: io.NopCloser(bytes.NewReader(b))}, nil
}

func sentChats(t *testing.T) [][3]string {
	system := strings.Repeat("You are opencode, an interactive CLI tool.\n", 40)
	tools := `[{"type":"function","function":{"name":"read","description":"Read a file","parameters":{"type":"object","properties":{"filePath":{"type":"string"},"offset":{"type":["number","null"]}},"required":["filePath"]}}},{"type":"function","function":{"name":"github.create_issue","description":"MCP","parameters":{"type":"object","properties":{"title":{"type":"string"},"labels":{"type":"array","items":{"type":"string"}}}}}},{"type":"function","function":{"name":"bash","parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"],"additionalProperties":false}}}]`
	call := `{"role":"assistant","content":"","reasoning_content":"I should list them.","tool_calls":[{"id":"call_1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"ls\"}"}}]}`
	result := `{"role":"tool","tool_call_id":"call_1","content":"a.txt\nb.txt\n中文.md 🙂"}`
	image := `{"type":"image_url","image_url":{"url":"data:image/png;base64,` + base64.StdEncoding.EncodeToString([]byte("not really a png")) + `"}}`
	sys := mustJSON(t, system)
	refuse := mustJSON(t, "REFUSE-461 "+system)
	ai := "https://www.workbuddy.ai/v2/chat/completions"
	return [][3]string{
		{"plain", ai, `{"model": "hy3",  "stream": true, "messages": [{"role": "system", "content": "Be brief."}, {"role": "user", "content": "hi"}], "reasoning_effort": "low"}`},
		{"opencode tools", ai, `{"model":"deepseek-v4.1-flash","stream":true,"stream_options":{"include_usage":true},"max_tokens":32000,"messages":[{"role":"system","content":` + sys + `},{"role":"user","content":"List the files."},` + call + `,` + result + `,{"role":"user","content":[{"type":"text","text":"and this"},` + image + `]}],"tools":` + tools + `,"tool_choice":"auto"}`},
		{"default effort", ai, `{"model":"deepseek-v4.1-flash","stream":true,"messages":[{"role":"user","content":"hi 🙂"}],"tools":` + tools + `,"tool_choice":{"type":"function","function":{"name":"github.create_issue"}}}`},
		{"developer, no system", ai, `{"model":"glm-5.2","messages":[{"role":"developer","content":"d"},{"role":"user","content":"x"}],"max_completion_tokens":9,"reasoning":{"effort":"high"}}`},
		{"after tools, refused", ai, `{"model":"gemini-sent-461","stream":true,"messages":[{"role":"system","content":"s"},{"role":"user","content":"List the files."},` + call + `,` + result + `,{"role":"user","content":"now"}],"tools":` + tools + `}`},
		{"after tools, known", ai, `{"model":"gemini-sent-461","stream":true,"messages":[{"role":"system","content":"s"},{"role":"user","content":"List the files."},` + call + `,` + result + `,{"role":"user","content":[{"type":"text","text":"<system-reminder>r</system-reminder>"}]},{"role":"user","content":"now"}],"tools":` + tools + `}`},
		{"system refused", ai, `{"model":"hy3","messages":[{"role":"system","content":` + refuse + `},{"role":"user","content":"hi"}]}`},
		{"system known refused", ai, `{"model":"hy3","messages":[{"role":"system","content":` + refuse + `},{"role":"user","content":"hi again"}],"tools":` + tools + `}`},
		{"both", ai, `{"model":"gemini-sent-461","messages":[{"role":"system","content":` + refuse + `},{"role":"user","content":"go"},` + call + `,` + result + `,{"role":"system","content":"late"},{"role":"user","content":"now"}]}`},
		{"messages", "https://www.workbuddy.ai/v1/messages", `{"model":"claude-opus-5.5","max_tokens":64,"system":[{"type":"text","text":"s"}],"messages":[{"role":"user","content":"q"},{"role":"assistant","content":[{"type":"thinking","thinking":"t"},{"type":"text","text":"a"}]},{"role":"user","content":"q2"}]}`},
		{"bytes", ai, `{"model":"hy3","messages":[{"role":"user","content":"bytes ü"}],"tools":` + tools + `}`},
		{"not json", ai, "{not json"},
	}
}

func mustJSON(t *testing.T, s string) string {
	b := jsonx.Marshal(s)
	return string(b)
}

func sentBy(t *testing.T, ch *Channel, up *fakeUpstream) []sentChat {
	a := &Account{Access: "test-only", Refresh: "test-only", UID: "u", Expires: time.Now().Add(time.Hour).UnixMilli()}
	var out []sentChat
	for _, c := range sentChats(t) {
		from := len(up.sent)
		native := strings.HasSuffix(c[1], "/messages")
		res, err := ch.Fetch(context.Background(), up, &ChatRequest{AuthID: "x", Account: a, URL: c[1], Body: []byte(c[2]), Native: native})
		if err != nil {
			t.Fatalf("%s: %v", c[0], err)
		}
		if res.Stream != nil {
			_, _ = io.ReadAll(res.Stream)
			res.Close()
		}
		out = append(out, sentChat{Label: c[0], Status: res.Status, Sent: append([]sentRow{}, up.sent[from:]...)})
	}
	// name each derived id by where it first showed up
	seen := map[string]string{}
	name := func(v any) any {
		s, ok := v.(string)
		if !ok {
			return nil
		}
		if n, ok := seen[s]; ok {
			return n
		}
		n := fmt.Sprintf("id%d", len(seen))
		seen[s] = n
		return n
	}
	for i := range out {
		for j := range out[i].Sent {
			out[i].Sent[j].Conversation = name(out[i].Sent[j].Conversation)
			out[i].Sent[j].Turn = name(out[i].Sent[j].Turn)
		}
	}
	return out
}

func TestSentAsMagpie0143(t *testing.T) {
	raw, err := os.ReadFile("testdata/sent-0.1.43.json")
	if err != nil {
		t.Fatal(err)
	}
	var was struct {
		Off      []sentChat `json:"off"`
		FreeOnly []sentChat `json:"freeOnly"`
	}
	if err := json.Unmarshal(raw, &was); err != nil {
		t.Fatal(err)
	}
	if len(was.Off) != 12 || len(was.FreeOnly) != 12 {
		t.Fatalf("testdata: %d and %d chats", len(was.Off), len(was.FreeOnly))
	}
	free := false
	ch := NewChannel(SiteAI, func() Options { return Options{FreeOnly: free} })
	up := &fakeUpstream{}
	check := func(label string, got, want []sentChat) {
		for i := range want {
			if i >= len(got) {
				t.Errorf("%s: missing %s", label, want[i].Label)
				continue
			}
			g, w := got[i], want[i]
			if g.Status != w.Status || len(g.Sent) != len(w.Sent) {
				t.Errorf("%s %q: status %d sends %d, want %d sends %d", label, w.Label, g.Status, len(g.Sent), w.Status, len(w.Sent))
				continue
			}
			for j := range w.Sent {
				gs, ws := g.Sent[j], w.Sent[j]
				if gs.URL != ws.URL || !reflect.DeepEqual(gs.Encoding, ws.Encoding) || gs.Body != ws.Body || gs.Conversation != ws.Conversation || gs.Turn != ws.Turn || gs.Growth != nil {
					t.Errorf("%s %q send %d:\n got %+v\nwant %+v", label, w.Label, j, gs, ws)
				}
			}
		}
	}
	check("off", sentBy(t, ch, up), was.Off)
	free = true
	check("freeOnly", sentBy(t, ch, up), was.FreeOnly)
}
