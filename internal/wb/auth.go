package wb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Account is a stored sign-in: magpie's OpenCode `oauth` auth with its
// fields (access, refresh, expires, uid, domain, enterpriseId …), or an API
// key (source "key"). Times are Unix milliseconds, as magpie keeps them.
type Account struct {
	Type             string `json:"type,omitempty"` // the CPA provider key
	Source           string `json:"source,omitempty"`
	Access           string `json:"access,omitempty"`
	Refresh          string `json:"refresh,omitempty"`
	Expires          int64  `json:"expires,omitempty"`
	RefreshExpiresAt int64  `json:"refreshExpiresAt,omitempty"`
	Domain           string `json:"domain,omitempty"`
	TokenType        string `json:"tokenType,omitempty"`
	UID              string `json:"uid,omitempty"`
	EnterpriseID     string `json:"enterpriseId,omitempty"`
	AccountID        string `json:"accountId,omitempty"` // the name magpie lists it as
	Key              string `json:"key,omitempty"`
	Plan             string `json:"plan,omitempty"`
	// Pending is set on a sign-in a file brought in that CPA parsed before
	// it could be renewed (auth.parse has no network): it is renewed and
	// looked up on its first use, as magpie's import does up front.
	Pending bool `json:"pendingImport,omitempty"`
}

func nowMS() int64 { return time.Now().UnixMilli() }

// Clone is a copy of a.
func (a *Account) Clone() *Account {
	if a == nil {
		return nil
	}
	c := *a
	return &c
}

// IsKey says a is an API key's account.
func (a *Account) IsKey() bool { return a != nil && a.Source == "key" }

// KeyAccount is the account an API key is sent as, the way WorkBuddy's own
// CLI sends one: access token = the key, uid "anonymous_" + its last eight
// characters, personal, no domain, no enterprise. Nothing renews a key.
func KeyAccount(key string) *Account {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	tail := key
	if len(tail) > 8 {
		tail = tail[len(tail)-8:]
	}
	return &Account{Key: key, Access: key, UID: "anonymous_" + tail, Source: "key"}
}

var remoteConfigRe = regexp.MustCompile(`^(ck_|pt_)`)

// RemoteConfig says whether WorkBuddy's own CLI asks /v3/config for the
// account's models with this key: only for keys that start ck_ or pt_.
func RemoteConfig(key string) bool { return remoteConfigRe.MatchString(key) }

// DomainOf is a's domain, else the API's host.
func DomainOf(site *Site, a *Account) string {
	if a != nil && a.Domain != "" {
		return a.Domain
	}
	return strings.TrimPrefix(site.Endpoint, "https://")
}

// CloudHosted are the hosts of WorkBuddy's cloud-hosted enterprises.
var CloudHosted = []*regexp.Regexp{
	regexp.MustCompile(`^[a-z0-9-]+\.sso\.copilot\.tencent\.com$`),
	regexp.MustCompile(`^[a-z0-9-]+\.copilot\.qq\.com$`),
	regexp.MustCompile(`^[a-z0-9-]+\.sso\.codebuddy\.cn$`),
}

// IOA is Tencent's own iOA hosts, served at copilot.tencent.com.
var IOA = map[string]bool{"tencent.sso.copilot.tencent.com": true, "tencent.sso.codebuddy.cn": true}

// EndpointOf is where a's requests go: its enterprise's own host for a
// cloud-hosted enterprise's account, else the site's endpoint.
func EndpointOf(site *Site, a *Account) string {
	if a == nil {
		return site.Endpoint
	}
	d := strings.ToLower(strings.TrimSpace(a.Domain))
	if d == "" || IOA[d] || a.Source == "key" {
		return site.Endpoint
	}
	for _, r := range CloudHosted {
		if r.MatchString(d) {
			return "https://" + d
		}
	}
	return site.Endpoint
}

// At is site with a's endpoint.
func At(site *Site, a *Account) *Site {
	end := EndpointOf(site, a)
	if end == site.Endpoint {
		return site
	}
	s := *site
	s.Endpoint = end
	return &s
}

// Moved is url sent to a's endpoint instead of the site's, when they differ.
func Moved(site *Site, a *Account, url string) string {
	end := EndpointOf(site, a)
	if end == site.Endpoint || !strings.HasPrefix(url, site.Endpoint+"/") {
		return url
	}
	return end + url[len(site.Endpoint):]
}

// Token is what WorkBuddy gives on a sign-in or refresh.
type Token struct {
	AccessToken      string  `json:"accessToken"`
	RefreshToken     string  `json:"refreshToken"`
	Domain           string  `json:"domain"`
	TokenType        string  `json:"tokenType"`
	ExpiresAt        float64 `json:"expiresAt"`
	ExpiresIn        float64 `json:"expiresIn"`
	RefreshExpiresAt float64 `json:"refreshExpiresAt"`
	RefreshExpiresIn float64 `json:"refreshExpiresIn"`
}

// Merge takes a token WorkBuddy gave into a stored sign-in.
func Merge(a *Account, got *Token) *Account {
	now := nowMS()
	out := a.Clone()
	if out == nil {
		out = &Account{}
	}
	out.Access = got.AccessToken
	if got.RefreshToken != "" {
		out.Refresh = got.RefreshToken
	}
	if got.Domain != "" {
		out.Domain = got.Domain
	}
	if got.TokenType != "" {
		out.TokenType = got.TokenType
	}
	if got.ExpiresAt > 0 {
		out.Expires = int64(got.ExpiresAt)
	} else if got.ExpiresIn > 0 {
		out.Expires = now + int64(got.ExpiresIn*1000)
	}
	if got.RefreshExpiresAt > 0 {
		out.RefreshExpiresAt = int64(got.RefreshExpiresAt)
	} else if got.RefreshExpiresIn > 0 {
		out.RefreshExpiresAt = now + int64(got.RefreshExpiresIn*1000)
	}
	return out
}

// Stale says a's access token is within EarlyLead of its end.
func Stale(a *Account) bool {
	return a.Expires != 0 && nowMS() >= a.Expires-EarlyLead.Milliseconds()
}

// Refreshable says a has a refresh token that hasn't ended.
func Refreshable(a *Account) bool {
	return a.Refresh != "" && !(a.RefreshExpiresAt != 0 && nowMS() >= a.RefreshExpiresAt)
}

// renewing is each refresh under way, by site and the refresh token it
// spends; renewed is what each gave, kept a minute so one that read the old
// refresh token after it was spent takes that rather than spending it again.
type renewal struct {
	done chan struct{}
	next *Account
	err  error
}

var (
	renewMu  sync.Mutex
	renewing = map[string]*renewal{}
	renewed  = map[string]struct {
		next *Account
		at   time.Time
	}{}
)

// Renew spends a's refresh token, once: those who come while it runs, or
// within KeepRenewedMS of it, get what it gave. save keeps the result.
func Renew(ctx context.Context, d Doer, site *Site, a *Account, save func(*Account)) (*Account, error) {
	key := site.ID + "\n" + a.Refresh
	renewMu.Lock()
	if done, ok := renewed[key]; ok && time.Since(done.at) < KeepRenewedMS {
		renewMu.Unlock()
		return MergedTokens(a, done.next), nil
	}
	r, ok := renewing[key]
	if !ok {
		r = &renewal{done: make(chan struct{})}
		renewing[key] = r
		renewMu.Unlock()
		func() {
			defer func() {
				renewMu.Lock()
				delete(renewing, key)
				renewMu.Unlock()
				close(r.done)
			}()
			var got Token
			hdr := map[string]string{
				"X-Refresh-Token":       a.Refresh,
				"X-Auth-Refresh-Source": "plugin",
				"X-Domain":              DomainOf(site, a),
			}
			for k, v := range EnterpriseHeaders(a) {
				hdr[k] = v
			}
			if err := Call(context.WithoutCancel(ctx), d, At(site, a), http.MethodPost, "/v2/plugin/auth/token/refresh", hdr, map[string]any{}, &got); err != nil {
				r.err = err
				return
			}
			if got.AccessToken == "" {
				r.err = &WBError{Message: "WorkBuddy gave no refreshed token", Bare: true}
				return
			}
			next := Merge(a, &got)
			renewMu.Lock()
			renewed[key] = struct {
				next *Account
				at   time.Time
			}{next, time.Now()}
			renewMu.Unlock()
			if save != nil {
				save(next)
			}
			r.next = next
		}()
	} else {
		renewMu.Unlock()
	}
	select {
	case <-r.done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if r.err != nil {
		return nil, r.err
	}
	return MergedTokens(a, r.next), nil
}

// MergedTokens is a with the tokens another's refresh gave over it.
func MergedTokens(a, next *Account) *Account {
	out := a.Clone()
	out.Access = next.Access
	out.Refresh = next.Refresh
	out.Expires = next.Expires
	out.RefreshExpiresAt = next.RefreshExpiresAt
	out.Domain = next.Domain
	out.TokenType = next.TokenType
	return out
}

// ErrSignedOut is a sign-in with nothing left to send.
var ErrSignedOut = errors.New("this WorkBuddy account is signed out; sign in again")

// Fresh is the sign-in with an access token that isn't about to end:
// refreshed (and saved) when it is. A refresh that fails, or a refresh
// token that has ended, leaves the access token there is.
func Fresh(ctx context.Context, d Doer, site *Site, a *Account, save func(*Account)) (*Account, error) {
	if !Stale(a) {
		return a, nil
	}
	if !Refreshable(a) {
		if a.Access != "" {
			return a, nil
		}
		return nil, ErrSignedOut
	}
	next, err := Renew(ctx, d, site, a, save)
	if err != nil {
		if a.Access != "" {
			return a, nil
		}
		var we *WBError
		if errors.As(err, &we) && we.Bare {
			return nil, err
		}
		return nil, fmt.Errorf("WorkBuddy token refresh: %s", saidOf(err))
	}
	return next, nil
}

func saidOf(err error) string {
	var we *WBError
	if errors.As(err, &we) && we.Said != "" {
		return we.Said
	}
	return err.Error()
}

// Refreshed is what CPA's auth.refresh gives for a sign-in: the account
// renewed, nil when there's nothing to renew (a key). A refresh token past
// its end is said plainly.
func Refreshed(ctx context.Context, d Doer, site *Site, a *Account) (*Account, error) {
	if a == nil || a.Source == "key" || a.Access == "" || a.Refresh == "" {
		return nil, nil
	}
	if !Refreshable(a) {
		return nil, ErrSignedOut
	}
	next, err := Renew(ctx, d, site, a, nil)
	if err != nil {
		var we *WBError
		if errors.As(err, &we) && we.Bare {
			return nil, err
		}
		return nil, fmt.Errorf("WorkBuddy token refresh: %s", saidOf(err))
	}
	return next, nil
}

// EnterpriseHeaders names the enterprise an account signed in as.
func EnterpriseHeaders(a *Account) map[string]string {
	if a == nil {
		return nil
	}
	id := strings.TrimSpace(a.EnterpriseID)
	if id == "" {
		return nil
	}
	return map[string]string{"X-Enterprise-Id": id, "X-Tenant-Id": id}
}

// Who is the account /login/account or the account list names.
type Who struct {
	UID            string `json:"uid"`
	Nickname       string `json:"nickname"`
	PhoneNumber    string `json:"phoneNumber"`
	EnterpriseID   string `json:"enterpriseId"`
	EnterpriseName string `json:"enterpriseName"`
	Type           string `json:"type"`
	LastLogin      any    `json:"lastLogin"`
}

func (w *Who) lastLogin() bool {
	switch t := w.LastLogin.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case float64:
		return t != 0
	}
	return true
}

// Enterprise is { id, name }; a personal account has id "".
type Enterprise struct{ ID, Name string }

// EnterpriseOf is the enterprise the account a sign-in gave belongs to.
func EnterpriseOf(ctx context.Context, d Doer, site *Site, a *Account, who *Who) Enterprise {
	own := strings.TrimSpace(who.EnterpriseID)
	typ := strings.ToLower(strings.TrimSpace(who.Type))
	if own == "" && (typ == "" || typ == "personal") {
		return Enterprise{}
	}
	if own != "" && strings.TrimSpace(who.EnterpriseName) != "" {
		return Enterprise{ID: own, Name: strings.TrimSpace(who.EnterpriseName)}
	}
	var list struct {
		Accounts []Who `json:"accounts"`
	}
	err := Call(ctx, d, At(site, a), http.MethodGet, "/v2/plugin/accounts", map[string]string{
		"Authorization": "Bearer " + a.Access,
		"X-Domain":      DomainOf(site, a),
	}, nil, &list)
	if err != nil {
		return Enterprise{ID: own}
	}
	var mine []Who
	for _, x := range list.Accounts {
		if x.UID == who.UID && strings.TrimSpace(x.EnterpriseID) != "" {
			mine = append(mine, x)
		}
	}
	if own != "" {
		for _, x := range mine {
			if strings.TrimSpace(x.EnterpriseID) == own {
				return Enterprise{ID: own, Name: strings.TrimSpace(x.EnterpriseName)}
			}
		}
		return Enterprise{ID: own}
	}
	var same, last []Who
	for _, x := range mine {
		if strings.ToLower(strings.TrimSpace(x.Type)) == typ {
			same = append(same, x)
		}
		if x.lastLogin() {
			last = append(last, x)
		}
	}
	var pick *Who
	switch {
	case len(same) == 1:
		pick = &same[0]
	case len(last) == 1:
		pick = &last[0]
	case len(mine) == 1:
		pick = &mine[0]
	}
	if pick == nil {
		return Enterprise{}
	}
	return Enterprise{ID: strings.TrimSpace(pick.EnterpriseID), Name: strings.TrimSpace(pick.EnterpriseName)}
}

// AccountName is what an account a sign-in gave is listed as: the person's
// name, and for an enterprise account the enterprise's too.
func AccountName(who *Who, ent Enterprise) string {
	person := ""
	if who != nil {
		person = who.Nickname
		if person == "" {
			person = who.PhoneNumber
		}
		if person == "" {
			person = who.UID
		}
	}
	if ent.ID == "" {
		return person
	}
	team := ent.Name
	if team == "" {
		id := ent.ID
		if len(id) > 6 {
			id = id[len(id)-6:]
		}
		team = "enterprise " + id
	}
	if person != "" {
		return person + " · " + team
	}
	return team
}

// CLIAgent is the User-Agent WorkBuddy's desktop CLI sends its API calls with.
func CLIAgent(site *Site) string {
	return fmt.Sprintf("WorkBuddy/%s %s/%s CLI/%s", site.App, site.AppName, site.App, site.CLI)
}

// Sign sets on headers what WorkBuddy's desktop app sends with a chat: the
// account's own, the client's, and the attribution WorkBuddy's usage list
// shows as the client that asked (使用端).
func Sign(site *Site, a *Account, h http.Header) {
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
	h.Set("X-IDE-Type", "WorkBuddy")
	h.Set("User-Agent", CLIAgent(site))
	h.Set("X-Requested-With", "XMLHttpRequest")
	h.Set("X-Agent-Intent", "craft")
	h.Set("X-Agent-Purpose", "conversation")
	h.Set("X-IDE-Name", "WorkBuddy")
	h.Set("X-IDE-Version", site.App)
	for k, v := range EnterpriseHeaders(a) {
		h.Set(k, v)
	}
}

// OpenAISDK and Node are what the CLI's chats go out through.
const (
	OpenAISDK = "6.25.0"
	NodeVer   = "v22.21.1"
)

// SDKOS and SDKArch are the X-Stainless-OS/Arch the OpenAI SDK sends: the
// platform the client runs on, as both magpie (process.platform/arch) and
// workbuddy2api-hub (the gateway host's) send them. The plugin config can set
// them to a desktop's ("MacOS"/"arm64", "Windows"/"x64").
var (
	SDKOS   = sdkOS(runtime.GOOS)
	SDKArch = sdkArch(runtime.GOARCH)
)

func sdkOS(goos string) string {
	switch goos {
	case "darwin":
		return "MacOS"
	case "windows":
		return "Windows"
	case "linux":
		return "Linux"
	case "freebsd":
		return "FreeBSD"
	case "openbsd":
		return "OpenBSD"
	case "android":
		return "Android"
	}
	return "Other:" + goos
}

func sdkArch(goarch string) string {
	switch goarch {
	case "amd64":
		return "x64"
	case "arm64":
		return "arm64"
	case "arm":
		return "arm"
	case "386":
		return "x32"
	}
	return "other:" + goarch
}

// ChatHeaders sets on a chat what WorkBuddy's CLI adds to its model requests
// alone.
func ChatHeaders(site *Site, h http.Header) {
	h.Set("X-Agent-Type", "main")
	if site.Runtime != "" {
		h.Set("X-Agent-Runtime", site.Runtime)
	} else {
		h.Del("X-Agent-Runtime")
	}
	h.Set("Accept", "application/json")
	h.Set("X-Stainless-Retry-Count", "0")
	h.Set("X-Stainless-Timeout", "1200")
	h.Set("X-Stainless-Lang", "js")
	h.Set("X-Stainless-Package-Version", OpenAISDK)
	h.Set("X-Stainless-OS", SDKOS)
	h.Set("X-Stainless-Arch", SDKArch)
	h.Set("X-Stainless-Runtime", "node")
	h.Set("X-Stainless-Runtime-Version", NodeVer)
}

// ParseAccount reads a stored sign-in.
func ParseAccount(raw []byte) (*Account, error) {
	var a Account
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	return &a, nil
}
