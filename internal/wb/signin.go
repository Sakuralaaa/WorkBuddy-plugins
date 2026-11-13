package wb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

func randHex() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Login is a browser sign-in under way.
type Login struct {
	Site     *Site
	State    string
	URL      string
	Deadline time.Time
	token    *Token
	acct     *Account
	done     *Account
	name     string
	err      error
	mu       sync.Mutex
}

// StartLogin asks WorkBuddy for a sign-in page.
func StartLogin(ctx context.Context, d Doer, site *Site) (*Login, error) {
	var st struct {
		State   string `json:"state"`
		AuthURL string `json:"authUrl"`
	}
	err := Call(ctx, d, site, http.MethodPost, "/v2/plugin/auth/state?platform="+url.QueryEscape(site.Platform), map[string]string{
		"X-No-Authorization":    "true",
		"X-No-User-Id":          "true",
		"X-No-Enterprise-Id":    "true",
		"X-No-Department-Info":  "true",
	}, map[string]any{}, &st)
	if err != nil {
		return nil, err
	}
	if st.State == "" || st.AuthURL == "" {
		return nil, fmt.Errorf("%s gave no sign-in page", site.Name)
	}
	u, err := url.Parse(st.AuthURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("%s's sign-in page isn't https", site.Name)
	}
	q := u.Query()
	q.Set("version", site.App)
	q.Set("loginSessionId", randHex())
	u.RawQuery = q.Encode()
	return &Login{Site: site, State: st.State, URL: u.String(), Deadline: time.Now().Add(SignInFor)}, nil
}

// errPending is a sign-in WorkBuddy hasn't finished yet.
var errPending = errors.New("pending")

func pollOnce(ctx context.Context, d Doer, site *Site, path string, headers map[string]string, retry int, out any) error {
	var raw json.RawMessage
	err := Call(ctx, d, site, http.MethodGet, path, headers, nil, &raw)
	if err != nil {
		var we *WBError
		if errors.As(err, &we) && (we.Code == retry || we.Status == 408 || we.Status == 429) {
			return errPending
		}
		return err
	}
	var m map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &m) != nil || len(m) == 0 {
		return errPending
	}
	return json.Unmarshal(raw, out)
}

// Poll asks once how the sign-in stands: done gives the account, pending
// says to ask again.
func (l *Login) Poll(ctx context.Context, d Doer) (done *Account, name string, pending bool, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.done != nil {
		return l.done, l.name, false, nil
	}
	if l.err != nil {
		return nil, "", false, l.err
	}
	if time.Now().After(l.Deadline) {
		l.err = fmt.Errorf("%s sign-in timed out", l.Site.Name)
		return nil, "", false, l.err
	}
	site := l.Site
	q := url.QueryEscape(l.State)
	if l.token == nil {
		var t Token
		e := pollOnce(ctx, d, site, "/v2/plugin/auth/token?state="+q, nil, 11217, &t)
		if e == errPending {
			return nil, "", true, nil
		}
		if e != nil {
			l.err = e
			return nil, "", false, e
		}
		if t.AccessToken == "" {
			l.err = fmt.Errorf("%s sent back no token", site.Name)
			return nil, "", false, l.err
		}
		l.token = &t
		l.acct = Merge(&Account{}, &t)
	}
	a := l.acct
	var who Who
	e := pollOnce(ctx, d, site, "/v2/plugin/login/account?state="+q, map[string]string{
		"Authorization":      "Bearer " + a.Access,
		"X-Domain":           DomainOf(site, a),
		"X-No-User-Id":       "true",
		"X-No-Enterprise-Id": "true",
	}, 12151, &who)
	if e == errPending {
		return nil, "", true, nil
	}
	if e != nil {
		l.err = e
		return nil, "", false, e
	}
	a.UID = who.UID
	ent := EnterpriseOf(ctx, d, site, a, &who)
	a.EnterpriseID = ent.ID
	l.name = AccountName(&who, ent)
	if l.name == "" {
		l.name = site.Name
	}
	a.AccountID = l.name
	l.done = a
	return a, l.name, false, nil
}

// ---- accounts from a file ------------------------------------------------------

// Found is a sign-in a file holds.
type Found struct {
	A      *Account
	Who    *Who
	Sealed bool
	Error  string
}

func strOf(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func sealed(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	x, has := m["$wbEncrypted"]
	return has && truthyAny(x)
}

func truthyAny(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case float64:
		return t != 0
	case string:
		return t != ""
	}
	return true
}

func numAny(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err == nil {
			return f
		}
	}
	return 0
}

func atMS(at, in any, now int64) int64 {
	if n := numAny(at); n > 0 {
		return int64(n)
	}
	if n := numAny(in); n > 0 {
		return now + int64(n*1000)
	}
	return 0
}

func plain(acct map[string]any) *Who {
	if acct == nil {
		return nil
	}
	return &Who{UID: strOf(acct["uid"]), Nickname: strOf(acct["nickname"]), PhoneNumber: strOf(acct["phoneNumber"]),
		EnterpriseID: strOf(acct["enterpriseId"]), EnterpriseName: strOf(acct["enterpriseName"]), Type: strOf(acct["type"]), LastLogin: acct["lastLogin"]}
}

func first(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			return v
		}
	}
	return nil
}

// SignInsIn are the sign-ins a file's text holds: WorkBuddy desktop's .info
// ({auth, account}), a sign-in's JSON ({access, refresh, …}, alone, in a map
// or a list), or tokens named as OAuth names them. Nothing is asked of
// WorkBuddy here.
func SignInsIn(text []byte) []Found {
	var v any
	if err := json.Unmarshal(text, &v); err != nil {
		return []Found{{Error: "not JSON"}}
	}
	var out []Found
	now := nowMS()
	var walk func(o any, depth int)
	walk = func(o any, depth int) {
		if depth > 4 {
			return
		}
		switch t := o.(type) {
		case []any:
			for _, x := range t {
				walk(x, depth+1)
			}
			return
		case map[string]any:
			if auth, ok := t["auth"].(map[string]any); ok {
				_, hasA := auth["accessToken"]
				_, hasR := auth["refreshToken"]
				if hasA || hasR {
					if sealed(auth["accessToken"]) || sealed(auth["refreshToken"]) {
						out = append(out, Found{Sealed: true})
						return
					}
					acct, _ := t["account"].(map[string]any)
					a := &Account{
						Access: strOf(auth["accessToken"]), Refresh: strOf(auth["refreshToken"]),
						Expires: atMS(auth["expiresAt"], auth["expiresIn"], now), RefreshExpiresAt: atMS(auth["refreshExpiresAt"], auth["refreshExpiresIn"], now),
						Domain: strOf(auth["domain"]), TokenType: strOf(auth["tokenType"]),
					}
					if acct != nil {
						a.EnterpriseID = strOf(acct["enterpriseId"])
					}
					f := Found{A: a}
					if acct != nil && strOf(acct["uid"]) != "" {
						f.Who = plain(acct)
						if sealed(acct["nickname"]) {
							f.Who.Nickname = ""
						}
					}
					out = append(out, f)
					return
				}
			}
			access := first(t, "access", "accessToken", "access_token")
			refresh := first(t, "refresh", "refreshToken", "refresh_token")
			if sealed(access) || sealed(refresh) {
				out = append(out, Found{Sealed: true})
				return
			}
			if strOf(access) != "" || strOf(refresh) != "" {
				uid := strOf(t["uid"])
				a := &Account{
					Access: strOf(access), Refresh: strOf(refresh),
					Expires:          atMS(first(t, "expires", "expiresAt"), first(t, "expiresIn", "expires_in"), now),
					RefreshExpiresAt: atMS(t["refreshExpiresAt"], first(t, "refreshExpiresIn", "refresh_expires_in"), now),
					Domain:           strOf(t["domain"]), TokenType: strOf(first(t, "tokenType", "token_type")),
					EnterpriseID: strOf(t["enterpriseId"]),
				}
				f := Found{A: a}
				if uid != "" {
					nick := strOf(t["nickname"])
					if nick == "" {
						nick = strOf(t["accountId"])
					}
					f.Who = &Who{UID: uid, Nickname: nick}
				}
				out = append(out, f)
				return
			}
			for _, x := range t {
				walk(x, depth+1)
			}
		}
	}
	walk(v, 0)
	if len(out) == 0 {
		return []Found{{Error: "no WorkBuddy sign-in in it"}}
	}
	return out
}

var (
	cnDomain = regexp.MustCompile(`(^|\.)(codebuddy\.cn|tencent\.com|qq\.com)$`)
	aiDomain = regexp.MustCompile(`(^|\.)workbuddy\.ai$`)
)

// OtherSite says the build a sign-in's domain belongs to when it isn't site.
func OtherSite(site *Site, a *Account) string {
	d := strings.ToLower(strings.TrimSpace(a.Domain))
	if d == "" {
		return ""
	}
	if site.AI && cnDomain.MatchString(d) {
		return SiteCN().Name
	}
	if !site.AI && aiDomain.MatchString(d) {
		return SiteAI().Name
	}
	return ""
}

// SiteOfDomain is the build a sign-in's domain says, nil when it says none.
func SiteOfDomain(a *Account) *Site {
	d := strings.ToLower(strings.TrimSpace(a.Domain))
	switch {
	case d == "":
		return nil
	case aiDomain.MatchString(d):
		return SiteAI()
	case cnDomain.MatchString(d), CloudHostedDomain(d):
		return SiteCN()
	}
	return nil
}

// CloudHostedDomain says d is a cloud-hosted enterprise's host.
func CloudHostedDomain(d string) bool {
	for _, r := range CloudHosted {
		if r.MatchString(d) {
			return true
		}
	}
	return IOA[d]
}

// Imported is one sign-in a file brought in, checked with WorkBuddy.
type Imported struct {
	OK      bool     `json:"ok"`
	Account *Account `json:"-"`
	Name    string   `json:"name,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// SealedText says why a 5.6+ desktop file can't be read.
func SealedText(site *Site) string {
	return fmt.Sprintf(`%s desktop keeps this sign-in encrypted (WorkBuddy 5.6 and later), which this plugin can't read; sign in with the browser sign-in instead`, site.Name)
}

// ImportSignIns checks each sign-in texts hold with WorkBuddy, as signing in
// does: renewed with its refresh token (an access token alone is kept while
// it lasts), and the account looked up.
func ImportSignIns(ctx context.Context, d Doer, site *Site, texts [][]byte) []Imported {
	var results []Imported
	for _, text := range texts {
		for _, got := range SignInsIn(text) {
			if got.Error != "" {
				results = append(results, Imported{Error: got.Error})
				continue
			}
			if got.Sealed {
				results = append(results, Imported{Error: SealedText(site)})
				continue
			}
			res, err := ImportOne(ctx, d, site, got)
			if err != nil {
				r := Imported{Error: err.Error()}
				if got.Who != nil {
					r.Name = AccountName(got.Who, Enterprise{})
				}
				results = append(results, r)
				continue
			}
			results = append(results, res)
		}
	}
	return results
}

// ImportOne checks one sign-in a file held.
func ImportOne(ctx context.Context, d Doer, site *Site, got Found) (Imported, error) {
	a := got.A.Clone()
	if other := OtherSite(site, a); other != "" {
		return Imported{}, fmt.Errorf("this is a %s sign-in; import it under %s", other, other)
	}
	if Refreshable(a) {
		next, err := Renew(ctx, d, site, a, nil)
		if err != nil {
			return Imported{}, err
		}
		a = next
	} else if a.Access == "" || Stale(a) {
		return Imported{}, fmt.Errorf("the sign-in has ended; sign in to %s again where it came from", site.Name)
	}
	who := got.Who
	if who == nil || who.UID == "" {
		var list struct {
			Accounts []Who `json:"accounts"`
		}
		hdr := map[string]string{"Authorization": "Bearer " + a.Access, "X-Domain": DomainOf(site, a)}
		for k, v := range EnterpriseHeaders(a) {
			hdr[k] = v
		}
		if err := Call(ctx, d, At(site, a), http.MethodGet, "/v2/plugin/accounts", hdr, nil, &list); err != nil {
			return Imported{}, err
		}
		var all, pick []Who
		for _, x := range list.Accounts {
			if x.UID != "" {
				all = append(all, x)
			}
		}
		for _, x := range all {
			if a.EnterpriseID != "" {
				if strings.TrimSpace(x.EnterpriseID) == a.EnterpriseID {
					pick = append(pick, x)
				}
			} else if x.lastLogin() {
				pick = append(pick, x)
			}
		}
		switch {
		case len(pick) == 1:
			who = &pick[0]
		case len(all) == 1:
			who = &all[0]
		default:
			return Imported{}, fmt.Errorf("%s didn't say whose sign-in this is", site.Name)
		}
	}
	a.UID = who.UID
	ent := EnterpriseOf(ctx, d, site, a, who)
	if ent.ID != "" {
		a.EnterpriseID = ent.ID
	}
	name := AccountName(who, ent)
	if name == "" {
		name = site.Name
	}
	a.AccountID = name
	a.Pending = false
	return Imported{OK: true, Account: a, Name: name}, nil
}

// CheckKey asks WorkBuddy whether an API key is taken, as WorkBuddy's CLI
// would first use it: its /v3/config (ck_/pt_ keys), else the account list.
func CheckKey(ctx context.Context, d Doer, site *Site, key string) error {
	a := KeyAccount(key)
	if a == nil {
		return errors.New("empty key")
	}
	if RemoteConfig(a.Key) {
		_, err := LiveModels(ctx, d, site, a)
		return err
	}
	return nil
}
