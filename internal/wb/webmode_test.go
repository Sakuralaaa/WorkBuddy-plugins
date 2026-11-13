package wb

// Web mode (webmode.go): the international build's chats sent as the web
// app's browser would. The unit test holds the header set; the Fetch test
// holds what actually goes upstream in each mode, against a capturing
// Doer, so a desktop marker cannot slip back in through another path.

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWebModeHeaders(t *testing.T) {
	site := SiteAI()
	a := &Account{Access: "test-only", UID: "u", EnterpriseID: "e", DeviceToken: "account-dt"}
	h := http.Header{}
	// A desktop Sign first, so SignWeb must clean up after it.
	Sign(site, a, h)
	if h.Get("X-Device-Token") != "account-dt" {
		t.Fatalf("setup: desktop Sign should set the account device token, got %q", h.Get("X-Device-Token"))
	}
	SignWeb(site, a, h)
	ChatHeadersWeb(h)

	if got := h.Get("User-Agent"); got != WebBrowserUA {
		t.Errorf("User-Agent = %q, want the browser UA", got)
	}
	if got := h.Get("Origin"); got != "https://www.workbuddy.ai" {
		t.Errorf("Origin = %q", got)
	}
	if got := h.Get("Referer"); got != "https://www.workbuddy.ai/app" {
		t.Errorf("Referer = %q", got)
	}
	if got := h.Get("Authorization"); got != "Bearer test-only" {
		t.Errorf("Authorization = %q", got)
	}
	if got := h.Get("X-Product"); got != "SaaS" {
		t.Errorf("X-Product = %q", got)
	}
	if got := h.Get("Accept"); got != "text/event-stream, application/json" {
		t.Errorf("Accept = %q", got)
	}
	for _, k := range append(append([]string{}, webDesktopHeaders...), "X-Root-Request-ID") {
		if got := h.Get(k); got != "" {
			t.Errorf("web headers must not carry %s, got %q", k, got)
		}
	}
}

// captureUpstream records every request it is sent, answering the model
// list from sentConfig like sent_test.go's fake does.
type captureUpstream struct {
	mu   sync.Mutex
	reqs []*Request
}

func (c *captureUpstream) capture(req *Request) {
	c.mu.Lock()
	c.reqs = append(c.reqs, req)
	c.mu.Unlock()
}

func (c *captureUpstream) Do(_ context.Context, req *Request) (*Response, error) {
	c.capture(req)
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	if strings.HasSuffix(req.URL, "/v3/config") {
		return &Response{Status: 200, Header: h, Body: []byte(sentConfig)}, nil
	}
	return &Response{Status: 200, Header: h, Body: []byte(`{"code":0,"data":{}}`)}, nil
}

func (c *captureUpstream) DoStream(_ context.Context, req *Request) (*StreamResponse, error) {
	c.capture(req)
	h := http.Header{}
	h.Set("Content-Type", "text/event-stream")
	return &StreamResponse{Status: 200, Header: h, Body: io.NopCloser(bytes.NewReader([]byte("data: [DONE]\n\n")))}, nil
}

func (c *captureUpstream) chatReq(t *testing.T) *Request {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.reqs) - 1; i >= 0; i-- {
		if strings.HasSuffix(c.reqs[i].URL, "/v2/chat/completions") {
			return c.reqs[i]
		}
	}
	t.Fatal("no chat request was sent upstream")
	return nil
}

func webModeFetch(t *testing.T, web bool) *Request {
	t.Helper()
	up := &captureUpstream{}
	ch := NewChannel(SiteAI, func() Options {
		return Options{WebMode: web, CacheKey: true, DeviceToken: "global-dt"}
	})
	a := &Account{Access: "test-only", Refresh: "test-only", UID: "u", DeviceToken: "account-dt", Expires: time.Now().Add(time.Hour).UnixMilli()}
	body := `{"model":"hy3","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	res, err := ch.Fetch(context.Background(), up, &ChatRequest{AuthID: "x", Account: a, URL: "https://www.workbuddy.ai/v2/chat/completions", Body: []byte(body)})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.Stream != nil {
		_, _ = io.ReadAll(res.Stream)
		res.Close()
	}
	if res.Status != 200 {
		t.Fatalf("Fetch status = %d, want 200", res.Status)
	}
	return up.chatReq(t)
}

func TestWebModeFetch(t *testing.T) {
	req := webModeFetch(t, true)
	h := req.Header
	if got := h.Get("User-Agent"); got != WebBrowserUA {
		t.Errorf("web User-Agent = %q", got)
	}
	if got := h.Get("Origin"); got != "https://www.workbuddy.ai" {
		t.Errorf("web Origin = %q", got)
	}
	if got := h.Get("Referer"); got != "https://www.workbuddy.ai/app" {
		t.Errorf("web Referer = %q", got)
	}
	for _, k := range []string{"X-Device-Token", "X-IDE-Type", "X-IDE-Name", "X-IDE-Version", "X-Agent-Intent", "X-Agent-Purpose", "X-Agent-Type", "X-Agent-Runtime", "X-Stainless-Lang", "X-Stainless-OS", "X-Root-Request-ID"} {
		if got := h.Get(k); got != "" {
			t.Errorf("web chat must not carry %s, got %q", k, got)
		}
	}
	if got := h.Get("X-Conversation-ID"); got == "" {
		t.Error("web chat must still carry X-Conversation-ID")
	}
	if got := h.Get("X-Conversation-Request-ID"); got == "" {
		t.Error("web chat must still carry X-Conversation-Request-ID")
	}
	if got := h.Get("Content-Encoding"); got != "" {
		t.Errorf("web chat Content-Encoding = %q, want none", got)
	}
	if len(req.Body) > 2 && req.Body[0] == 0x1f && req.Body[1] == 0x8b {
		t.Error("web chat body must not be gzipped")
	}
	if strings.Contains(string(req.Body), "prompt_cache_key") {
		t.Error("web chat body must not carry prompt_cache_key")
	}
}

func TestDesktopModeFetch(t *testing.T) {
	req := webModeFetch(t, false)
	h := req.Header
	if got := h.Get("User-Agent"); !strings.HasPrefix(got, "WorkBuddy/") {
		t.Errorf("desktop User-Agent = %q", got)
	}
	if got := h.Get("X-Device-Token"); got != "account-dt" {
		t.Errorf("desktop X-Device-Token = %q, want the account token", got)
	}
	if got := h.Get("Content-Encoding"); got != "gzip" {
		t.Errorf("desktop Content-Encoding = %q, want gzip", got)
	}
	body := req.Body
	if len(body) > 2 && body[0] == 0x1f && body[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			t.Fatalf("desktop body is not valid gzip: %v", err)
		}
		body, _ = io.ReadAll(zr)
	}
	if !strings.Contains(string(body), "prompt_cache_key") {
		t.Error("desktop chat body should carry prompt_cache_key with CacheKey on")
	}
}
