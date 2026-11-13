package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	_ "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator/builtin" // CPA's own translators, for the routes a client did not speak

	"github.com/Sakuralaaa/WorkBuddy-plugins/internal/wb"
)

// executorRequest is executor.execute[_stream]'s request.
type executorRequest struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

const (
	fmtChat   = sdktranslator.FormatOpenAI
	fmtClaude = sdktranslator.FormatClaude
)

// formatOf reads a format name CPA sends (its own names, or the plugin's).
func formatOf(name string) sdktranslator.Format {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "claude", "anthropic":
		return fmtClaude
	case "", "openai", "chat-completions", "chat_completions":
		return fmtChat
	}
	return sdktranslator.Format(strings.ToLower(strings.TrimSpace(name)))
}

// call is one model request made ready: which build and account, which of
// WorkBuddy's routes it goes on (chat completions, or Anthropic Messages for
// the China build's Claude models, as magpie routes each model), and what
// the client speaks on either side.
type call struct {
	req      executorRequest
	site     *wb.Site
	account  *wb.Account
	model    string
	upstream sdktranslator.Format // what WorkBuddy is sent and answers
	in       sdktranslator.Format // what CPA handed the plugin
	out      sdktranslator.Format // what CPA wants back
	body     []byte               // the request as WorkBuddy is sent it
	doer     HostDoer
}

func prepareCall(raw []byte) (*call, error) {
	var req executorRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	noteFile(req.AuthID, req.AuthAttributes, req.AuthMetadata)
	site, a, err := accountFrom(req.AuthProvider, req.AuthID, req.StorageJSON, req.AuthMetadata)
	if err != nil {
		return nil, fail("auth_unreadable", "the WorkBuddy credential could not be read: "+err.Error(), http.StatusUnauthorized)
	}
	if a == nil {
		return nil, fail("unauthorized", site.Name+" isn't signed in", http.StatusUnauthorized)
	}
	c := &call{req: req, site: site, account: a, doer: HostDoer{CallbackID: req.HostCallbackID}}
	c.in = formatOf(req.SourceFormat)
	c.out = formatOf(req.Format)
	if req.Format == "" {
		c.out = c.in
	}
	c.model = strings.TrimSpace(req.Model)
	if c.model == "" {
		c.model = modelOfBody(req.Payload)
	}
	c.upstream = fmtChat
	if wb.NativeOf(site, c.model) {
		c.upstream = fmtClaude
	}
	c.body = req.Payload
	if c.in != c.upstream {
		if !sdktranslator.HasRequestTransformer(c.in, c.upstream) {
			return nil, fail(pluginabi.ErrorCodeRequestScoped, fmt.Sprintf("%s serves %s on %s, and this request's %s can't be turned into it", site.Name, c.model, c.upstream, c.in), http.StatusBadRequest)
		}
		c.body = sdktranslator.TranslateRequest(c.in, c.upstream, c.model, req.Payload, req.Stream)
	}
	c.body = withModel(c.body, c.model)
	return c, nil
}

func modelOfBody(body []byte) string {
	var b struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &b)
	return b.Model
}

// withModel names the model CPA chose (an alias resolved, a suffix taken
// off) in the body, when the body names another.
func withModel(body []byte, model string) []byte {
	if model == "" || modelOfBody(body) == model {
		return body
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}
	q, _ := json.Marshal(model)
	m["model"] = q
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// sessionOf is the session a client names, which magpie keys a
// conversation's id by when it has one (else the first user message).
func sessionOf(h http.Header, meta map[string]any) string {
	for _, k := range []string{"X-Session-Id", "Session_id", "Session-Id", "X-Claude-Code-Session-Id", "X-Codex-Session-Id"} {
		if v := strings.TrimSpace(h.Get(k)); v != "" {
			return v
		}
	}
	for _, k := range []string{"session_id", "sessionId"} {
		if v, ok := meta[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// fetch sends the call as magpie's loader does and maps a refusal to the
// structured error CPA cools credentials by.
func (c *call) fetch(ctx context.Context) (*wb.ChatResult, error) {
	ch := channelFor(c.site)
	url := c.site.API() + "/chat/completions"
	if c.upstream == fmtClaude {
		url = c.site.APIMessages() + "/messages"
	}
	h := http.Header{}
	if c.upstream == fmtClaude {
		h.Set("Anthropic-Version", "2023-06-01")
		if b := c.req.Headers.Get("Anthropic-Beta"); b != "" {
			h.Set("Anthropic-Beta", b)
		}
	}
	res, err := ch.Fetch(ctx, c.doer, &wb.ChatRequest{
		AuthID: c.req.AuthID, Account: c.account, URL: url, Body: c.body,
		Native: c.upstream == fmtClaude, Session: sessionOf(c.req.Headers, c.req.Metadata), Header: h,
	})
	if err != nil {
		return nil, transportFailure(c.site, err)
	}
	if res.Status >= 400 {
		e := refusalOf(ch, c.site, c.req.AuthID, c.model, res)
		res.Close()
		return nil, &Failure{E: e}
	}
	return res, nil
}

func transportFailure(site *wb.Site, err error) error {
	if errors.Is(err, wb.ErrSignedOut) {
		return fail("unauthorized", err.Error(), http.StatusUnauthorized)
	}
	var we *wb.WBError
	if errors.As(err, &we) {
		return fail("upstream_error", err.Error(), http.StatusBadGateway)
	}
	if errors.Is(err, context.Canceled) {
		return fail(pluginabi.ErrorCodeConnectionLifecycle, err.Error(), 499)
	}
	return &Failure{E: &pluginabi.Error{Code: pluginabi.ErrorCodeTransientTransport, Message: site.Name + ": " + err.Error(), HTTPStatus: http.StatusBadGateway, Retryable: true}}
}

// refusalOf maps a WorkBuddy refusal to CPA's routing: credits exhausted
// (14018) is the whole credential's 402, a restricted account (11140) its
// 403, deepseek-v4.1-flash's 429 cools that model alone until the reset it
// names, a refused prompt is the request's own.
func refusalOf(ch *wb.Channel, site *wb.Site, authID, model string, r *wb.ChatResult) *pluginabi.Error {
	msg := wb.ErrorMessageOf(r.Body)
	if msg == "" {
		msg = fmt.Sprintf("%s answered %d", site.Name, r.Status)
	}
	e := &pluginabi.Error{Code: "upstream_error", Message: msg, HTTPStatus: r.Status}
	switch {
	case r.Explained == "exhausted":
		e.Code, e.HTTPStatus, e.CredentialScoped = "insufficient_quota", http.StatusPaymentRequired, true
		return e
	case r.Explained == "restricted":
		e.Code, e.HTTPStatus, e.CredentialScoped = "account_restricted", http.StatusForbidden, true
		return e
	case r.Explained == "refused":
		e.Code, e.HTTPStatus = pluginabi.ErrorCodeRequestScoped, http.StatusBadRequest
		return e
	case r.NotFree:
		e.Code = pluginabi.ErrorCodeRequestScoped
		return e
	}
	switch r.Status {
	case http.StatusUnauthorized:
		e.Code = "unauthorized"
		if r.SignIn == wb.SignInExpired {
			e.Code = "invalid_grant"
		}
	case http.StatusTooManyRequests:
		e.Retryable = true
		reset := wb.ResetOf(string(r.Body))
		if reset > 0 {
			if left := reset - time.Now().Unix(); left > 0 {
				e.RetryAfterSeconds = int(left)
			}
		} else if ra := r.Header.Get("Retry-After"); ra != "" {
			var n int
			if _, err := fmt.Sscanf(ra, "%d", &n); err == nil && n > 0 {
				e.RetryAfterSeconds = n
			}
		}
		if wb.OffTaken[model] {
			e.Code = pluginabi.ErrorCodeTransientRateLimit
			ch.NoteRefusal(authID, model, reset)
		} else {
			e.Code = "rate_limited"
		}
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		e.Code = pluginabi.ErrorCodeRequestScoped
	case http.StatusNotFound:
		e.Code = pluginabi.ErrorCodeModelNotFound
	default:
		if r.Status >= 500 {
			e.Retryable = true
		}
	}
	return e
}

// ---- answering --------------------------------------------------------------

// relay reads WorkBuddy's answer event by event (tool names given back and
// the filter's refusal taken out on chat, as magpie's loader) and hands on
// each SSE frame in the format CPA wants.
type relay struct {
	c     *call
	param any
	sawDone bool
}

func (r *relay) frames(ev string) [][]byte {
	if r.c.upstream == r.c.out {
		if r.c.out == fmtChat {
			if d, ok := wb.DataOf(ev); ok && strings.TrimSpace(d) == "[DONE]" {
				r.sawDone = true
			}
		}
		return [][]byte{[]byte(ev + "\n\n")}
	}
	var out [][]byte
	for _, line := range strings.Split(ev, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if r.c.upstream == fmtChat && strings.TrimSpace(strings.TrimPrefix(line, "data:")) == "[DONE]" {
			r.sawDone = true
		}
		out = append(out, r.translate([]byte(line))...)
	}
	return out
}

func (r *relay) translate(line []byte) [][]byte {
	c := r.c
	got := sdktranslator.TranslateStream(context.Background(), c.upstream, c.out, c.model, c.req.OriginalRequestOr(), c.body, line, &r.param)
	out := make([][]byte, 0, len(got))
	for _, f := range got {
		if len(bytes.TrimSpace(f)) == 0 {
			continue
		}
		if c.out == fmtChat && !bytes.HasPrefix(f, []byte("data:")) {
			f = append(append([]byte("data: "), f...), '\n', '\n')
		}
		out = append(out, f)
	}
	return out
}

// tail is what a stream ends with that WorkBuddy left unsaid: a chat
// stream's [DONE] (which the translators finish on).
func (r *relay) tail() [][]byte {
	if r.c.upstream != fmtChat || r.sawDone {
		return nil
	}
	r.sawDone = true
	if r.c.out == fmtChat {
		return [][]byte{[]byte("data: [DONE]\n\n")}
	}
	return r.translate([]byte("data: [DONE]"))
}

// OriginalRequestOr is the client's request, or the payload when CPA sent none.
func (r executorRequest) OriginalRequestOr() []byte {
	if len(r.OriginalRequest) > 0 {
		return r.OriginalRequest
	}
	return r.Payload
}

func (c *call) events(res *wb.ChatResult, emit func(ev string) error) error {
	if c.upstream == fmtChat {
		return wb.ChatStream(res.Stream, res.Names, emit)
	}
	return wb.SSEEvents(res.Stream, emit)
}

func isJSONAnswer(h http.Header) bool {
	ct := strings.ToLower(h.Get("Content-Type"))
	return strings.Contains(ct, "json") && !strings.Contains(ct, "event-stream")
}

func answerHeaders(h http.Header, stream bool) http.Header {
	out := http.Header{}
	if stream {
		out.Set("Content-Type", "text/event-stream")
	} else {
		out.Set("Content-Type", "application/json")
	}
	for _, k := range []string{"X-Request-Id", "X-Trace-Id"} {
		if v := h.Get(k); v != "" {
			out.Set(k, v)
		}
	}
	return out
}

// execute is executor.execute: WorkBuddy only streams, so its stream is
// gathered into the one answer the client asked for.
func execute(raw []byte) (any, error) {
	c, err := prepareCall(raw)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	res, err := c.fetch(ctx)
	if err != nil {
		return nil, err
	}
	defer res.Close()
	var body []byte
	if isJSONAnswer(res.Header) {
		body, _ = io.ReadAll(res.Stream)
	} else {
		var events []string
		if err := c.events(res, func(ev string) error { events = append(events, ev); return nil }); err != nil {
			return nil, fail(pluginabi.ErrorCodeStreamTruncated, c.site.Name+"'s answer broke off: "+err.Error(), http.StatusBadGateway)
		}
		if c.upstream == fmtClaude {
			body = wb.MessageOf(events)
		} else {
			body = wb.ChatCompletionOf(events, c.model)
		}
	}
	if c.upstream != c.out {
		var param any
		body = sdktranslator.TranslateNonStream(ctx, c.upstream, c.out, c.model, c.req.OriginalRequestOr(), c.body, body, &param)
	}
	return pluginapi.ExecutorResponse{Payload: body, Headers: answerHeaders(res.Header, false)}, nil
}

// executeStream is executor.execute_stream: the request is sent (and any
// refusal answered as an error, so CPA can try another credential) before
// it returns; the stream is then emitted through host.stream.emit.
func executeStream(raw []byte) (any, error) {
	c, err := prepareCall(raw)
	if err != nil {
		return nil, err
	}
	streamID := c.req.StreamID
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	res, err := c.fetch(ctx)
	if err != nil {
		cancel()
		return nil, err
	}
	if streamID == "" {
		// a host that keeps no stream open takes the chunks in the reply
		defer cancel()
		defer res.Close()
		type chunk struct {
			Payload []byte `json:"Payload"`
		}
		var chunks []chunk
		r := &relay{c: c}
		_ = c.events(res, func(ev string) error {
			for _, f := range r.frames(ev) {
				chunks = append(chunks, chunk{Payload: f})
			}
			return nil
		})
		for _, f := range r.tail() {
			chunks = append(chunks, chunk{Payload: f})
		}
		return map[string]any{"headers": answerHeaders(res.Header, true), "chunks": chunks}, nil
	}
	go func() {
		defer cancel()
		defer res.Close()
		r := &relay{c: c}
		if isJSONAnswer(res.Header) {
			// an answer read whole: handed on as one event
			b, _ := io.ReadAll(res.Stream)
			for _, f := range r.frames("data: " + string(bytes.TrimSpace(b))) {
				if emit(streamID, f) != nil {
					return
				}
			}
			for _, f := range r.tail() {
				_ = emit(streamID, f)
			}
			closeStream(streamID, nil)
			return
		}
		err := c.events(res, func(ev string) error {
			for _, f := range r.frames(ev) {
				if e := emit(streamID, f); e != nil {
					return e
				}
			}
			return nil
		})
		if err != nil {
			closeStream(streamID, &pluginabi.Error{Code: pluginabi.ErrorCodeStreamTruncated, Message: c.site.Name + "'s stream broke off: " + err.Error(), HTTPStatus: http.StatusBadGateway})
			return
		}
		for _, f := range r.tail() {
			if emit(streamID, f) != nil {
				return
			}
		}
		closeStream(streamID, nil)
	}()
	return map[string]any{"headers": answerHeaders(res.Header, true)}, nil
}

// countTokens is executor.count_tokens. WorkBuddy has no counting route a
// desktop client calls, so this is an estimate (about four characters a
// token) that keeps Claude Code's preflight working.
func countTokens(raw []byte) (any, error) {
	var req executorRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	n := int64(len([]rune(string(req.Payload))) / 4)
	if n < 1 {
		n = 1
	}
	var payload []byte
	if formatOf(req.Format) == fmtClaude {
		payload, _ = json.Marshal(map[string]any{"input_tokens": n})
	} else {
		payload, _ = json.Marshal(map[string]any{"object": "response.input_tokens", "input_tokens": n, "usage": map[string]any{"prompt_tokens": n, "total_tokens": n}})
	}
	return pluginapi.ExecutorResponse{Payload: payload, Headers: http.Header{"Content-Type": {"application/json"}}}, nil
}
