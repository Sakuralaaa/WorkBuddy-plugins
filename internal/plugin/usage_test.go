package plugin

import (
	"strings"
	"testing"
	"time"
)

func TestMeterChatAndMessages(t *testing.T) {
	m := newMeter()
	m.see(`data: {"choices":[{"delta":{"content":"hi"}}]}`)
	m.see(`data: {"choices":[],"usage":{"prompt_tokens":120,"completion_tokens":30,"total_tokens":150,"credit":0.42,"prompt_tokens_details":{"cached_tokens":100}}}`)
	m.see("data: [DONE]")
	if m.pt != 120 || m.ct != 30 || m.tt != 150 || m.ch != 100 || !m.hasCredit || m.credit != 0.42 || m.first.IsZero() {
		t.Fatalf("chat meter = %+v", m)
	}
	c := newMeter()
	c.see("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":50,\"cache_read_input_tokens\":40}}}")
	c.see("event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":9}}")
	if c.pt != 50 || c.ct != 9 || c.ch != 40 || c.hasCredit {
		t.Fatalf("messages meter = %+v", c)
	}
}

func TestRollup(t *testing.T) {
	usage.Lock()
	usage.loaded = true
	usage.b = map[usageKey]*usageBucket{}
	now := time.Now()
	old := now.Add(-10 * 24 * time.Hour).Truncate(time.Hour)
	usage.b[usageKey{T: old.Unix(), Acct: "a", Build: "cn", Model: "m"}] = &usageBucket{N: 2, TT: 10}
	usage.b[usageKey{T: old.Add(time.Hour).Unix(), Acct: "a", Build: "cn", Model: "m"}] = &usageBucket{N: 1, TT: 5}
	usage.b[usageKey{T: now.Add(-100 * 24 * time.Hour).Unix(), Acct: "a", Build: "cn", Model: "m"}] = &usageBucket{N: 9}
	rollupLocked(now)
	var n, tt int64
	for k, b := range usage.b {
		if !k.Day {
			t.Fatalf("hour bucket left: %+v", k)
		}
		n += b.N
		tt += b.TT
	}
	usage.Unlock()
	if n != 3 || tt != 15 {
		t.Fatalf("rolled = %d requests %d tokens", n, tt)
	}
}

func TestCleanNote(t *testing.T) {
	if got := cleanNote("  主力号\n "); got != "主力号" {
		t.Fatalf("got %q", got)
	}
	long := strings.Repeat("备", 60)
	if got := cleanNote(long); len([]rune(got)) != noteMaxRunes {
		t.Fatalf("len %d", len([]rune(got)))
	}
}
