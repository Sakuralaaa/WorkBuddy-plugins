package wb

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Sakuralaaa/WorkBuddy-plugins/internal/jsonx"
)

// PriceTTL is how long a list read for the free-only check is used.
const PriceTTL = 60 * time.Second

// EdgeReads is how much of a request WorkBuddy's front door reads before it
// answers a sign-in it refuses; past it the connection is reset.
const EdgeReads = 2 * 1024 * 1024

// Options are a build's switches (magpie's provider options).
type Options struct {
	FreeOnly bool
	HideDsh  bool
	// Disabled model ids are refused before anything is sent.
	Disabled map[string]bool
}

// Channel is one build's state for a run (magpie's makePlugin closure).
type Channel struct {
	Site    func() *Site
	Options func() Options
	// Stored is the sign-in CPA holds for an auth now (renewed since, maybe).
	Stored func(authID string) *Account
	// Save keeps a sign-in renewed here for an auth.
	Save func(authID string, a *Account)

	mu             sync.Mutex
	endsAfterTools map[string]bool
	refusedSystems *boundedSet
	commaSystems   *boundedSet
	lists          map[string]listed
	unlisted       map[string]time.Time
	refusals       map[string]refusal
}

type listed struct {
	at   time.Time
	list []Model
}

type boundedSet struct {
	order []string
	set   map[string]bool
	cap   int
}

func newBoundedSet(n int) *boundedSet { return &boundedSet{set: map[string]bool{}, cap: n} }

func (b *boundedSet) has(k string) bool { return b.set[k] }

func (b *boundedSet) add(k string) {
	if b.set[k] {
		return
	}
	b.set[k] = true
	b.order = append(b.order, k)
	if len(b.order) > b.cap {
		delete(b.set, b.order[0])
		b.order = b.order[1:]
	}
}

// NewChannel is a build's channel.
func NewChannel(site func() *Site, opts func() Options) *Channel {
	return &Channel{
		Site: site, Options: opts,
		endsAfterTools: map[string]bool{},
		refusedSystems: newBoundedSet(256),
		commaSystems:   newBoundedSet(256),
		lists:          map[string]listed{},
		unlisted:       map[string]time.Time{},
	}
}

func listKey(a *Account) string {
	key := ""
	if a.Source == "key" {
		key = a.Key
	}
	return a.UID + "|" + a.EnterpriseID + "|" + key
}

// Listed is the account's live list, read again past PriceTTL.
func (c *Channel) Listed(ctx context.Context, d Doer, a *Account, fresh bool) ([]Model, error) {
	k := listKey(a)
	c.mu.Lock()
	was, ok := c.lists[k]
	c.mu.Unlock()
	if !fresh && ok && time.Since(was.at) < PriceTTL {
		return was.list, nil
	}
	list, err := LiveModels(ctx, d, c.Site(), a)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.lists[k] = listed{at: time.Now(), list: list}
	if len(c.lists) > 64 {
		for key := range c.lists {
			if key != k {
				delete(c.lists, key)
				break
			}
		}
	}
	c.mu.Unlock()
	return list, nil
}

func (c *Channel) effortList(ctx context.Context, d Doer, a *Account) []Model {
	if a.Source == "key" && !RemoteConfig(a.Key) {
		return nil
	}
	k := listKey(a)
	c.mu.Lock()
	at, bad := c.unlisted[k]
	c.mu.Unlock()
	if bad && time.Since(at) < PriceTTL {
		return nil
	}
	list, err := c.Listed(ctx, d, a, false)
	if err != nil {
		c.mu.Lock()
		c.unlisted[k] = time.Now()
		c.mu.Unlock()
		return nil
	}
	return list
}

// ChatRequest is one request of a client, as the loader's fetch is handed it.
type ChatRequest struct {
	AuthID  string
	Account *Account
	// URL is where it goes on the site's own endpoint: <site>/v2/chat/completions
	// or <site>/v1/messages.
	URL     string
	Body    []byte
	Native  bool
	Session string
	// Header is the client's headers to keep (ids it set, say).
	Header http.Header
}

// SignIn is magpie's X-Magpie-Sign-In: what came of the sign-in.
type SignIn string

const (
	SignInKept    SignIn = "kept"
	SignInRenewed SignIn = "renewed"
	SignInExpired SignIn = "expired"
)

// ChatResult is what WorkBuddy answered, read as magpie's loader reads it.
type ChatResult struct {
	Status  int
	Header  http.Header
	Body    []byte        // an error's body, or a body read whole
	Stream  io.ReadCloser // a success's body, as it comes
	Names   map[string]string
	SignIn  SignIn
	Account *Account
	// Explained says what kind of refusal Body is ("" for none).
	Explained string
	// NotFree is set on an answer the free-only switch gave.
	NotFree bool
}

// Close lets go of a result's stream.
func (r *ChatResult) Close() {
	if r != nil && r.Stream != nil {
		_ = r.Stream.Close()
	}
}

var completionPath = regexp.MustCompile(`/chat/completions$`)

func isCompletion(url string, chat *obj) bool {
	if i := strings.Index(url, "://"); i >= 0 {
		path := url[i+3:]
		if j := strings.IndexByte(path, '/'); j >= 0 {
			path = path[j:]
		} else {
			path = ""
		}
		if k := strings.IndexAny(path, "?#"); k >= 0 {
			path = path[:k]
		}
		if path != "" {
			return completionPath.MatchString(path)
		}
	}
	_, ok := chat.Arr("messages")
	return chat != nil && ok
}

// Gzipped is a chat's body compressed, as WorkBuddy's client sends its
// model requests.
func Gzipped(b []byte) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, _ = w.Write(b)
	_ = w.Close()
	return buf.Bytes()
}

func jsonResp(status int, v any, extra map[string]string) *ChatResult {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	for k, x := range extra {
		h.Set(k, x)
	}
	return &ChatResult{Status: status, Header: h, Body: jsonx.Marshal(jsonx.FromGo(v))}
}

func notFree(site *Site, model, why string) *ChatResult {
	if model == "" {
		model = "(none)"
	}
	var msg string
	status, code := 404, "model_not_found"
	if why != "" {
		msg = fmt.Sprintf(`%s's model list couldn't be read (%s), so whether model %s is free is unknown; with "Free models only" on it isn't sent`, site.Name, why, model)
		status, code = 503, "free_only_unknown"
	} else {
		msg = fmt.Sprintf(`model %s is not available while "Free models only" is on: %s charges for it. Turn the switch off in the plugin config (free_only) to use it`, model, site.Name)
	}
	r := jsonResp(status, map[string]any{"error": map[string]any{"message": msg, "type": "invalid_request_error", "code": code}}, nil)
	r.SignIn = SignInKept
	r.NotFree = true
	return r
}

func refusedText(site *Site, why, note string) string {
	s := fmt.Sprintf("%s refused this account's sign-in (401)", site.Name)
	if why != "" {
		s += "; " + why
	}
	s += ". "
	if note != "" {
		s += note + " "
	}
	return s + fmt.Sprintf("Sign in to %s again in CLIProxyAPI (its management page, or the plugin's import).", site.Name)
}

func authError(msg string) map[string]any {
	return map[string]any{"error": map[string]any{"message": msg, "type": "authentication_error", "code": 401}}
}

// invalidToken says a 401 is WorkBuddy refusing the access token itself.
func invalidToken(status int, h http.Header) bool {
	return status == 401 && strings.Contains(strings.ToLower(h.Get("WWW-Authenticate")), "invalid_token")
}

func refreshRefused(err error) bool {
	var we *WBError
	return errors.As(err, &we) && (we.Status == 401 || strings.Contains(strings.ToLower(we.Message), "invalid_grant"))
}

type reissue struct {
	a    *Account
	why  string
	kind string
}

func (c *Channel) reissued(ctx context.Context, d Doer, authID string, a *Account, began string) reissue {
	site := c.Site()
	newer := func() *Account {
		if c.Stored == nil {
			return nil
		}
		now := c.Stored(authID)
		if now != nil && now.Source != "key" && now.Access != "" && now.Access != began && now.Access != a.Access {
			return now
		}
		return nil
	}
	if s := newer(); s != nil {
		return reissue{a: s}
	}
	if !Refreshable(a) {
		why := "there is no refresh token to renew it with"
		if a.Refresh != "" {
			why = "its refresh token has ended"
		}
		return reissue{why: why, kind: "nothing to renew with"}
	}
	next, err := Renew(ctx, d, site, a, func(n *Account) {
		if c.Save != nil {
			c.Save(authID, n)
		}
	})
	if err != nil {
		if refreshRefused(err) {
			if s := newer(); s != nil {
				return reissue{a: s}
			}
			return reissue{why: fmt.Sprintf("it refused the refresh token too (%s)", saidOf(err)), kind: "refresh refused"}
		}
		return reissue{why: "renewing it failed: " + saidOf(err), kind: "unknown"}
	}
	if next.Access == a.Access {
		return reissue{why: "renewing it gave the same token", kind: "unknown"}
	}
	return reissue{a: next}
}

func gone(r reissue, tokenRefused bool) bool {
	return r.kind == "refresh refused" || (r.kind == "nothing to renew with" && tokenRefused)
}

// Fetch is magpie's loader fetch for one request: the body shaped, sent
// signed as WorkBuddy's desktop client, a 401 renewed once, a 400 for a
// refused system prompt or tool-result order sent again shaped, and the
// answer read back.
func (c *Channel) Fetch(ctx context.Context, d Doer, req *ChatRequest) (*ChatResult, error) {
	site := c.Site()
	opts := c.Options()
	a := req.Account
	if a == nil {
		return nil, fmt.Errorf("%s isn't signed in", site.Name)
	}
	if a.Source != "key" {
		var err error
		save := func(n *Account) {
			if c.Save != nil {
				c.Save(req.AuthID, n)
			}
		}
		a, err = Fresh(ctx, d, site, a, save)
		if err != nil {
			return nil, err
		}
	}
	began := a.Access
	headers := http.Header{}
	for k, v := range req.Header {
		headers[k] = append([]string(nil), v...)
	}
	headers.Del("Authorization")
	headers.Del("X-Api-Key")
	headers.Del("Content-Length")
	Sign(site, a, headers)

	native := req.Native
	p := Prepare(req.Body, native)
	body, chat, names, changed := p.Body, p.Chat, p.Names, p.Changed
	if native {
		names = map[string]string{}
	}
	if opts.HideDsh && HideDsh(chat) {
		changed = true
	}
	if len(opts.Disabled) > 0 && chat != nil {
		if model := chat.String("model"); opts.Disabled[model] {
			r := jsonResp(404, map[string]any{"error": map[string]any{"message": fmt.Sprintf("model %s is disabled in the plugin config (disabled_models_%s)", model, map[bool]string{true: "ai", false: "cn"}[site.AI]), "type": "invalid_request_error", "code": "model_disabled"}}, nil)
			r.SignIn = SignInKept
			return r, nil
		}
	}
	if opts.FreeOnly {
		model := ""
		if chat != nil {
			model = chat.String("model")
		}
		list, err := c.Listed(ctx, d, a, false)
		if err != nil {
			return notFree(site, model, err.Error()), nil
		}
		free := false
		for _, m := range list {
			if m.ID == model && m.Free {
				free = true
			}
		}
		if !free {
			return notFree(site, model, ""), nil
		}
	}
	completion := !native && isCompletion(req.URL, chat)
	if completion && chat != nil && !NamesEffort(chat) && WithDefaultEffort(chat, c.effortList(ctx, d, a)) {
		changed = true
	}
	ChatHeaders(site, headers)
	who := ScopeOf(a)
	ids := Attend(headers, chat, sessionLimit(req.Session), who)
	grownFor := ""
	hadVars := chat != nil && chat.Has("extra_vars")
	if completion && WithGrowth(site, chat, headers.Get("X-Conversation-ID"), "") {
		if hadVars {
			changed = true
		}
		grownFor = headers.Get("X-Conversation-ID")
	}

	post := func(b []byte) (*StreamResponse, []byte, error) {
		conv := headers.Get("X-Conversation-ID")
		if grownFor != "" && conv != "" && conv != grownFor {
			if rb := Regrown(site, b, grownFor, conv); rb != nil {
				b = rb
			}
		}
		wire := b
		if completion {
			wire = Gzipped(b)
			headers.Set("Content-Encoding", "gzip")
		} else {
			headers.Del("Content-Encoding")
		}
		headers.Set("Content-Type", "application/json")
		res, err := d.DoStream(ctx, &Request{Method: http.MethodPost, URL: Moved(site, a, req.URL), Header: headers.Clone(), Body: wire})
		if err != nil {
			if r := c.unanswered(ctx, d, a, wire, err); r != nil {
				return r, wire, nil
			}
			return nil, wire, err
		}
		return res, wire, nil
	}

	renewed := false
	type answer struct {
		res  *StreamResponse
		ours *ChatResult
	}
	out := func(why string, expired bool, note string) *ChatResult {
		r := jsonResp(401, authError(refusedText(site, why, note)), nil)
		if expired {
			r.SignIn = SignInExpired
		} else {
			r.SignIn = SignInKept
		}
		return r
	}
	send := func(b []byte) (answer, error) {
		res, _, err := post(b)
		if err != nil {
			return answer{}, err
		}
		if res.Status != 401 {
			return answer{res: res}, nil
		}
		note := res.Header.Get(edgeNoteHeader)
		res.Body.Close()
		if renewed {
			return answer{ours: out("it refused the token renewed for it too", invalidToken(res.Status, res.Header), note)}, nil
		}
		if a.Source == "key" {
			msg := fmt.Sprintf("%s refused this API key (401). ", site.Name)
			if note != "" {
				msg += note + " "
			}
			msg += fmt.Sprintf("Check the key at %s, then add it again.", site.Keys)
			r := jsonResp(401, authError(msg), nil)
			r.SignIn = SignInKept
			if invalidToken(res.Status, res.Header) {
				r.SignIn = SignInExpired
			}
			return answer{ours: r}, nil
		}
		ri := c.reissued(ctx, d, req.AuthID, a, began)
		if ri.a == nil {
			return answer{ours: out(ri.why, gone(ri, invalidToken(res.Status, res.Header)), note)}, nil
		}
		renewed = true
		a = ri.a
		Sign(site, a, headers)
		now := ScopeOf(a)
		ids = Reattend(headers, ids, who, now)
		who = now
		again, _, err := post(b)
		if err != nil {
			return answer{}, err
		}
		if again.Status != 401 {
			return answer{res: again}, nil
		}
		again.Body.Close()
		return answer{ours: out("it refused the token renewed for it too", invalidToken(again.Status, again.Header), again.Header.Get(edgeNoteHeader))}, nil
	}

	model := ""
	if chat != nil {
		model = chat.String("model")
	}
	sysKey := ""
	if !native {
		sysKey = SystemKeyOf(chat)
	}
	c.mu.Lock()
	moveNow := sysKey != "" && c.refusedSystems.has(sysKey)
	commaNow := sysKey != "" && c.commaSystems.has(sysKey)
	afterTools := c.endsAfterTools[model]
	c.mu.Unlock()
	if moveNow && SystemAsUserOn(chat) {
		changed = true
	}
	if commaNow && CommaPausedOn(chat, nil) {
		changed = true
	}
	shaped := func(b []byte) []byte {
		c.mu.Lock()
		at := c.endsAfterTools[model]
		c.mu.Unlock()
		if !native && at {
			if r := ReplyAfterTools(b); r != nil {
				return r
			}
		}
		return b
	}
	if !native && afterTools && ReplyAfterToolsOn(chat) {
		changed = true
	}
	if changed {
		body = jsonx.Marshal(chat)
	} else if grownFor != "" {
		body = WithVars(body, chat.Get("extra_vars"))
	}
	chat = nil

	ans, err := send(body)
	if err != nil {
		return nil, err
	}
	readText := func(res *StreamResponse) []byte {
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return b
	}
	var text []byte
	var textOf *StreamResponse // the response text was read off
	if ans.ours == nil && native && ans.res.Status >= 400 {
		text = readText(ans.res)
		textOf = ans.res
		if BadSignature.test(string(text)) {
			if again := WithoutSignedThinking(body); again != nil {
				ans, err = send(again)
				if err != nil {
					return nil, err
				}
				text, textOf = nil, nil
			}
		}
	}
	if ans.ours == nil && !native && ans.res.Status == 400 {
		text = readText(ans.res)
		textOf = ans.res
		roles := map[string]bool{}
		var paused []byte
		if Refused.test(string(text)) {
			paused = CommaPaused(body, roles)
		}
		if paused != nil {
			body = paused
			ans, err = send(shaped(body))
			if err != nil {
				return nil, err
			}
			text, textOf = nil, nil
			if ans.ours == nil && ans.res.Status == 400 {
				text = readText(ans.res)
				textOf = ans.res
			} else if sysKey != "" && roles["system"] {
				c.mu.Lock()
				c.commaSystems.add(sysKey)
				c.mu.Unlock()
			}
		}
		if ans.ours == nil && ans.res.Status == 400 && Refused.test(string(text)) && sysKey != "" {
			c.mu.Lock()
			known := c.refusedSystems.has(sysKey)
			c.mu.Unlock()
			if !known {
				if moved := SystemAsUser(body); moved != nil {
					c.mu.Lock()
					c.refusedSystems.add(sysKey)
					c.mu.Unlock()
					body = moved
					ans, err = send(shaped(body))
					if err != nil {
						return nil, err
					}
					text, textOf = nil, nil
					if ans.ours == nil && ans.res.Status == 400 {
						text = readText(ans.res)
						textOf = ans.res
					}
				}
			}
		}
		if ans.ours == nil && ans.res.Status == 400 && textOf == ans.res {
			c.mu.Lock()
			known := c.endsAfterTools[model]
			c.mu.Unlock()
			if !known && AfterToolsRefused.test(string(text)) {
				if again := ReplyAfterTools(body); again != nil {
					c.mu.Lock()
					c.endsAfterTools[model] = true
					c.mu.Unlock()
					ans, err = send(again)
					if err != nil {
						return nil, err
					}
					text, textOf = nil, nil
				}
			}
		}
	}
	if ans.ours != nil {
		ans.ours.Account = a
		return ans.ours, nil
	}
	res := ans.res
	signIn := SignInKept
	if renewed {
		signIn = SignInRenewed
	}
	if res.Status >= 400 {
		if textOf != res {
			text = readText(res)
		}
		ex := Explain(text)
		h := res.Header.Clone()
		h.Del("Content-Length")
		h.Del("Content-Encoding")
		if ex.Kind != "" {
			h.Set("Content-Type", "application/json")
		}
		return &ChatResult{Status: res.Status, Header: h, Body: ex.Body, SignIn: SignInKept, Account: a, Explained: ex.Kind, Names: names}, nil
	}
	h := res.Header.Clone()
	h.Del("Content-Length")
	h.Del("Content-Encoding")
	return &ChatResult{Status: res.Status, Header: h, Stream: res.Body, Names: names, SignIn: signIn, Account: a}, nil
}

const edgeNoteHeader = "X-Wb-Edge-Note"

// unanswered is what a chat whose send failed comes to: one past EdgeReads
// the connection was reset on is asked about at the billing API, whose 401
// is the chat's own answer.
func (c *Channel) unanswered(ctx context.Context, d Doer, a *Account, wire []byte, err error) *StreamResponse {
	if ctx.Err() != nil || len(wire) <= EdgeReads || a.Source == "key" {
		return nil
	}
	site := c.Site()
	res, e := MeterAsked(ctx, d, site, a, "/billing/meter/get-user-resource-summary", map[string]any{})
	if e != nil || res.Status != 401 {
		return nil
	}
	mb := float64(len(wire)) / (1024 * 1024)
	note := fmt.Sprintf("It closes the connection on a refused request over 2 MiB (this one was %.1f MiB) instead of answering, which is why it read as a reset connection.", mb)
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	if ch := res.Header.Get("WWW-Authenticate"); ch != "" {
		h.Set("WWW-Authenticate", ch)
	}
	h.Set(edgeNoteHeader, note)
	msg := fmt.Sprintf("%s refused this account's sign-in (401). %s Sign in to %s again.", site.Name, note, site.Name)
	return &StreamResponse{Status: 401, Header: h, Body: io.NopCloser(bytes.NewReader(jsonx.Marshal(jsonx.FromGo(authError(msg)))))}
}
