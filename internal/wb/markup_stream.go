package wb

import (
	"encoding/json"
	"strings"
)

// markupStream runs a MarkupRepair over a chat SSE stream: the text the
// repair holds back or keeps is written as content, each call it restores is
// a delta.tool_calls frame after it, and a stop that ends a turn with
// restored calls says tool_calls.
type markupStream struct {
	r       *MarkupRepair
	synth   int
	maxIdx  int
	tpl     map[string]any
	flushed bool
}

func dataOf(ev string) (string, bool) {
	var payload string
	n := 0
	for _, line := range strings.Split(ev, "\n") {
		if strings.HasPrefix(line, "data:") {
			payload = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			n++
		} else if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, ":") && !strings.HasPrefix(line, "event:") {
			return "", false
		}
	}
	return payload, n == 1
}

func frameEv(obj map[string]any) string {
	raw, err := json.Marshal(obj)
	if err != nil {
		return ""
	}
	return "data: " + string(raw)
}

func (m *markupStream) contentFrame(text string) map[string]any {
	f := map[string]any{"object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": text}}}}
	for _, k := range []string{"id", "created", "model"} {
		if v, ok := m.tpl[k]; ok && v != nil {
			f[k] = v
		}
	}
	return f
}

func (m *markupStream) event(ev string) []string {
	p, ok := dataOf(ev)
	if !ok {
		return []string{ev}
	}
	if p == "[DONE]" {
		return append(m.flush(), ev)
	}
	if m.synth == 0 && m.r.hold == "" && !m.r.inBlock && !strings.Contains(p, "\uFF5C") && !strings.Contains(p, "\\uff5c") && !strings.Contains(p, `"tool_calls"`) {
		return []string{ev}
	}
	var obj map[string]any
	if json.Unmarshal([]byte(p), &obj) != nil {
		return []string{ev}
	}
	chs, ok := obj["choices"].([]any)
	if !ok || len(chs) == 0 {
		// a usage-only or error frame: what the repair holds goes first
		if _, e := obj["error"]; e {
			return []string{ev}
		}
		if _, u := obj["usage"]; u {
			return append(m.flush(), ev)
		}
		return []string{ev}
	}
	if m.tpl == nil {
		m.tpl = map[string]any{"id": obj["id"], "created": obj["created"], "model": obj["model"]}
	}
	var calls []MarkupCall
	for _, ci := range chs {
		c, _ := ci.(map[string]any)
		if c == nil {
			continue
		}
		d, _ := c["delta"].(map[string]any)
		if d == nil {
			continue
		}
		if tcs, ok := d["tool_calls"].([]any); ok {
			for _, tci := range tcs {
				if tc, ok := tci.(map[string]any); ok {
					if v, ok := tc["index"].(float64); ok && int(v) > m.maxIdx {
						m.maxIdx = int(v)
					}
				}
			}
		}
		txt, ok := d["content"].(string)
		if !ok || txt == "" {
			continue
		}
		emit, got := m.r.Feed(txt)
		calls = append(calls, got...)
		if emit == "" {
			delete(d, "content")
		} else {
			d["content"] = emit
		}
	}
	if len(calls) == 0 {
		if m.synth > 0 {
			if fr := takeFinishReason(obj); fr != "" {
				// what the repair still holds belongs before the end
				out := []string{frameEv(obj)}
				if rest := m.r.Flush(); rest != "" {
					out = append(out, frameEv(m.contentFrame(rest)))
				}
				if fr == "stop" {
					fr = "tool_calls"
				}
				return append(out, frameEv(finishOnlyFrame(m.tpl, fr)))
			}
		} else if hasFinish(obj) {
			if rest := m.r.Flush(); rest != "" {
				fr := takeFinishReason(obj)
				return []string{frameEv(obj), frameEv(m.contentFrame(rest)), frameEv(finishOnlyFrame(m.tpl, fr))}
			}
		}
		return []string{frameEv(obj)}
	}
	fr := takeFinishReason(obj)
	out := []string{frameEv(obj)}
	for _, call := range calls {
		idx := m.maxIdx + 1 + m.synth
		m.synth++
		out = append(out, frameEv(markupCallFrame(m.tpl, idx, call)))
	}
	if fr != "" {
		if rest := m.r.Flush(); rest != "" {
			out = append(out, frameEv(m.contentFrame(rest)))
		}
		if fr == "stop" {
			fr = "tool_calls"
		}
		out = append(out, frameEv(finishOnlyFrame(m.tpl, fr)))
	}
	return out
}

func hasFinish(obj map[string]any) bool {
	chs, _ := obj["choices"].([]any)
	for _, ci := range chs {
		if c, ok := ci.(map[string]any); ok {
			if v, ok := c["finish_reason"].(string); ok && v != "" {
				return true
			}
		}
	}
	return false
}

func (m *markupStream) flush() []string {
	rest := m.r.Flush()
	if rest == "" {
		return nil
	}
	return []string{frameEv(m.contentFrame(rest))}
}

func takeFinishReason(obj map[string]any) string {
	fr := ""
	chs, _ := obj["choices"].([]any)
	for _, ci := range chs {
		c, _ := ci.(map[string]any)
		if c == nil {
			continue
		}
		if v, ok := c["finish_reason"].(string); ok && v != "" && fr == "" {
			fr = v
		}
		delete(c, "finish_reason")
	}
	return fr
}

func markupCallFrame(src map[string]any, idx int, call MarkupCall) map[string]any {
	frame := map[string]any{
		"object": "chat.completion.chunk",
		"choices": []any{map[string]any{
			"index": 0,
			"delta": map[string]any{"tool_calls": markupToolCalls([]MarkupCall{call}, idx)},
		}},
	}
	for _, k := range []string{"id", "created", "model"} {
		if v, ok := src[k]; ok && v != nil {
			frame[k] = v
		}
	}
	return frame
}

func finishOnlyFrame(src map[string]any, finish string) map[string]any {
	frame := map[string]any{
		"object":  "chat.completion.chunk",
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}},
	}
	for _, k := range []string{"id", "created", "model"} {
		if v, ok := src[k]; ok && v != nil {
			frame[k] = v
		}
	}
	return frame
}
