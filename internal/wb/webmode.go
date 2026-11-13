package wb

import (
	"net/http"
)

// Web mode: the international build's chats sent the way the web app's
// browser would send them, instead of as the desktop client.
//
// What the web app (workbuddy.ai/app) actually sends was read off its own
// JS bundles (download.codebuddy.ai, 2026-11-13): it never calls
// /v2/chat/completions at all — its chat runs through /console/as/*
// (conversation create, message resolve) plus a queue check
// (/v2/chat/queue/status, /v2/chat/busy) and an ACP JSON-RPC stream to the
// agent backend, authenticated by browser session cookies and a Keycloak
// token. That whole flow is a different protocol from the completions API
// this plugin wraps, so it is not reproduced here. What IS reproduced is
// the request's visible shape on the completions endpoint: a plain browser
// fetch — browser User-Agent, the web app's Origin/Referer, no desktop
// User-Agent, no X-IDE-*, no X-Agent-*, no X-Stainless-*, no
// X-Device-Token, no prompt_cache_key, body uncompressed. An independent
// client (ErfanBagheri404/WorkBuddy2API) proves this shape passes the
// 11128 channel check on /v2/chat/completions with just Bearer +
// X-Product + X-User-Id + X-Domain + the X-Conversation-* family.
//
// The web app's own bundles confirm the negative half: X-Device-Token,
// "WorkBuddy/" and prompt_cache_key occur zero times across its main,
// chat-UI and account chunks, while X-Conversation-ID and
// X-Conversation-Request-ID are sent on the queue calls.

// WebBrowserUA is the User-Agent a current Chrome on Windows sends. The
// web app runs in the user's browser, so its requests carry the browser's
// UA, not the desktop client's.
const WebBrowserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"

// webAppOrigin / webAppReferer are what a browser sets on a POST from the
// web app: same-origin to www.workbuddy.ai, referred from /app.
const (
	webAppOrigin  = "https://www.workbuddy.ai"
	webAppReferer = "https://www.workbuddy.ai/app"
)

// webDesktopHeaders are the headers the desktop client adds that a browser
// fetch from the web app never carries. SignWeb and ChatHeadersWeb delete
// them so a mode switch cannot leave desktop markers behind.
var webDesktopHeaders = []string{
	"X-IDE-Type", "X-IDE-Name", "X-IDE-Version",
	"X-Agent-Intent", "X-Agent-Purpose", "X-Agent-Type", "X-Agent-Runtime",
	"X-Requested-With",
	"X-Stainless-Retry-Count", "X-Stainless-Timeout", "X-Stainless-Lang",
	"X-Stainless-Package-Version", "X-Stainless-OS", "X-Stainless-Arch",
	"X-Stainless-Runtime", "X-Stainless-Runtime-Version",
	deviceTokenHeader,
}

// SignWeb sets on headers what a browser fetch from the web app carries:
// the account's sign-in (Bearer, X-User-Id, X-Domain) and the product,
// with a browser User-Agent and the web app's Origin/Referer. It is for
// the international build only; the China build's web app is a different
// origin and is not imitated here.
func SignWeb(site *Site, a *Account, h http.Header) {
	h.Set("Authorization", "Bearer "+a.Access)
	h.Set("X-User-Id", a.UID)
	if a.Source == "key" {
		h.Set("X-API-Key", a.Key)
		h.Del("X-Domain")
	} else {
		h.Del("X-API-Key")
		h.Set("X-Domain", DomainOf(site, a))
	}
	h.Set("X-Product", "SaaS")
	h.Set("User-Agent", WebBrowserUA)
	h.Set("Origin", webAppOrigin)
	h.Set("Referer", webAppReferer)
	for k, v := range EnterpriseHeaders(a) {
		h.Set(k, v)
	}
	for _, k := range webDesktopHeaders {
		h.Del(k)
	}
}

// ChatHeadersWeb sets a web chat's Accept and drops the desktop SDK
// headers ChatHeaders would have set. The completions answer is a stream,
// so Accept matches what a streaming fetch asks for.
func ChatHeadersWeb(h http.Header) {
	h.Set("Accept", "text/event-stream, application/json")
	for _, k := range webDesktopHeaders {
		h.Del(k)
	}
}

// stripWebRootID drops X-Root-Request-ID, which Attend sets from a desktop
// capture (workbuddy2api-hub 5.8.4); the web app's requests do not carry it.
func stripWebRootID(h http.Header) {
	h.Del("X-Root-Request-ID")
}

// webModeOn is whether a chat goes out in web mode: the switch on, on the
// international build. The China build ignores the switch.
func webModeOn(site *Site, opts Options) bool {
	return site.AI && opts.WebMode
}
