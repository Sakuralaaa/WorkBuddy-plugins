package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"github.com/Sakuralaaa/WorkBuddy-plugins/internal/wb"
)

type authEntry struct {
	Identifier string `json:"identifier"`
	Name       string `json:"name,omitempty"`
	Logo       string `json:"logo,omitempty"`
}

func authIdentifier() any {
	return map[string]any{
		"identifier":  wb.ProviderCN,
		"identifiers": []string{wb.ProviderCN, wb.ProviderAI},
		"entries": []authEntry{
			{Identifier: wb.ProviderCN, Name: "WorkBuddy (China, magpie port)"},
			{Identifier: wb.ProviderAI, Name: "WorkBuddy AI (international, magpie port)"},
		},
	}
}

func ours(provider string) bool {
	return provider == wb.ProviderCN || provider == wb.ProviderAI
}

// authParse reads a credential file: one this plugin wrote (type
// workbuddy-magpie[-ai]), or a WorkBuddy sign-in brought in as a file — the
// desktop app's .info, or a sign-in's token JSON — which is renewed and
// looked up on first use (CPA parses files without network).
func authParse(raw []byte) (any, error) {
	var req pluginapi.AuthParseRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	var head struct {
		Type string `json:"type"`
		Site string `json:"site"`
	}
	_ = json.Unmarshal(req.RawJSON, &head)
	if ours(head.Type) {
		site := wb.SiteFor(head.Type)
		a, err := wb.ParseAccount(req.RawJSON)
		if err != nil {
			return nil, err
		}
		d := authDataOf(site, a, req.FileName)
		d.StorageJSON = append([]byte(nil), req.RawJSON...)
		return pluginapi.AuthParseResponse{Handled: true, Auth: d}, nil
	}
	if head.Type != "" && !ours(req.Provider) {
		return pluginapi.AuthParseResponse{Handled: false}, nil
	}
	found := wb.SignInsIn(req.RawJSON)
	var datas []pluginapi.AuthData
	for _, f := range found {
		if f.Error != "" {
			continue
		}
		site := siteForImport(req.Provider, head.Site, req.FileName, f)
		if site == nil {
			continue
		}
		if f.Sealed {
			return nil, fmt.Errorf("%s", wb.SealedText(site))
		}
		a := f.A.Clone()
		a.Pending = true
		if f.Who != nil {
			a.UID = f.Who.UID
			a.AccountID = wb.AccountName(f.Who, wb.Enterprise{})
		}
		datas = append(datas, authDataOf(site, a, ""))
	}
	if len(datas) == 0 {
		return pluginapi.AuthParseResponse{Handled: false}, nil
	}
	return pluginapi.AuthParseResponse{Handled: true, Auth: datas[0], Auths: datas}, nil
}

// siteForImport is the build a file's sign-in is for: the provider CPA
// named, else a site hint, the desktop file's name, or the token's domain.
// A file that names none of them is no WorkBuddy file this plugin takes.
func siteForImport(provider, hint, fileName string, f wb.Found) *wb.Site {
	if s := wb.SiteFor(provider); s != nil && ours(provider) {
		return s
	}
	if s := wb.SiteFor(strings.ToLower(strings.TrimSpace(hint))); s != nil {
		return s
	}
	base := strings.ToLower(fileName)
	switch {
	case strings.Contains(base, "workbuddy-desktop-ai"):
		return wb.SiteAI()
	case strings.Contains(base, "workbuddy-desktop"):
		return wb.SiteCN()
	}
	if f.A != nil {
		return wb.SiteOfDomain(f.A)
	}
	return nil
}

// ---- browser sign-in ------------------------------------------------------------

var logins = struct {
	sync.Mutex
	by map[string]*wb.Login
}{by: map[string]*wb.Login{}}

type loginStartRequest struct {
	pluginapi.AuthLoginStartRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type loginPollRequest struct {
	pluginapi.AuthLoginPollRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

func loginStart(raw []byte) (any, error) {
	var req loginStartRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	site := wb.SiteFor(req.Provider)
	if site == nil {
		return nil, fmt.Errorf("unknown provider %q", req.Provider)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	l, err := wb.StartLogin(ctx, HostDoer{CallbackID: req.HostCallbackID}, site)
	if err != nil {
		return nil, err
	}
	logins.Lock()
	for k, x := range logins.by {
		if time.Now().After(x.Deadline.Add(time.Minute)) {
			delete(logins.by, k)
		}
	}
	logins.by[l.State] = l
	logins.Unlock()
	return pluginapi.AuthLoginStartResponse{
		Provider:  site.Provider,
		URL:       l.URL,
		State:     l.State,
		ExpiresAt: l.Deadline,
		Metadata:  map[string]any{"instructions": fmt.Sprintf("Sign in to %s in the browser; this finishes by itself.", site.Name)},
	}, nil
}

func loginPoll(raw []byte) (any, error) {
	var req loginPollRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	logins.Lock()
	l := logins.by[req.State]
	logins.Unlock()
	if l == nil {
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: "this sign-in is unknown or has ended; start it again"}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	a, _, pending, err := l.Poll(ctx, HostDoer{CallbackID: req.HostCallbackID})
	if pending {
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusPending}, nil
	}
	logins.Lock()
	delete(logins.by, req.State)
	logins.Unlock()
	if err != nil {
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: err.Error()}, nil
	}
	return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusSuccess, Auth: authDataOf(l.Site, a, "")}, nil
}

// ---- refresh ------------------------------------------------------------------------

type refreshRequest struct {
	pluginapi.AuthRefreshRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

// refreshMu makes refreshes run one at a time (magpie: "renewed once for
// the account"; the renewal itself is single-flight per refresh token too).
var refreshMu sync.Mutex

// authRefresh renews a sign-in RefreshLead before its access token ends. A
// failed refresh is an error, so CPA keeps the sign-in it has and asks
// again; the old token is never thrown away.
func authRefresh(raw []byte) (any, error) {
	var req refreshRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	noteFile(req.AuthID, req.Attributes, req.Metadata)
	site, a, err := accountFrom(req.AuthProvider, req.AuthID, req.StorageJSON, req.Metadata)
	if err != nil {
		return nil, err
	}
	d := HostDoer{CallbackID: req.HostCallbackID}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	refreshMu.Lock()
	defer refreshMu.Unlock()
	if a.Pending {
		next, err := completeImport(ctx, d, site, a)
		if err != nil {
			return nil, err
		}
		holdAccount(req.AuthID, next)
		data := authDataOf(site, next, "")
		data.FileName = ""
		return pluginapi.AuthRefreshResponse{Auth: data, NextRefreshAfter: data.NextRefreshAfter}, nil
	}
	next, err := wb.Refreshed(ctx, d, site, a)
	if err != nil {
		return nil, err
	}
	if next == nil {
		data := authDataOf(site, a, "")
		data.FileName = ""
		return pluginapi.AuthRefreshResponse{Auth: data}, nil
	}
	holdAccount(req.AuthID, next)
	data := authDataOf(site, next, "")
	data.FileName = ""
	return pluginapi.AuthRefreshResponse{Auth: data, NextRefreshAfter: data.NextRefreshAfter}, nil
}

// completeImport renews and looks up a sign-in a file brought in, as
// magpie's import does before it adds one.
func completeImport(ctx context.Context, d wb.Doer, site *wb.Site, a *wb.Account) (*wb.Account, error) {
	f := wb.Found{A: a}
	if a.UID != "" {
		f.Who = &wb.Who{UID: a.UID, Nickname: a.AccountID}
	}
	got, err := wb.ImportOne(ctx, d, site, f)
	if err != nil {
		return nil, err
	}
	return got.Account, nil
}
