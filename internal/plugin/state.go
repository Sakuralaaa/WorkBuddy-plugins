package plugin

import (
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"github.com/Sakuralaaa/WorkBuddy-plugins/internal/wb"
)

// held is each auth's sign-in as renewed here, by CPA auth id: CPA hands
// the executor the credential as it was stored, and a renewal made by a
// request (before CPA's own refresh) is kept here so the next request, and
// the next refresh, take it rather than spending a spent refresh token.
var (
	heldMu sync.Mutex
	held   = map[string]*wb.Account{}
	// files is each auth's file name, for writing a renewal back.
	files = map[string]string{}
)

func holdAccount(authID string, a *wb.Account) {
	if authID == "" || a == nil {
		return
	}
	heldMu.Lock()
	held[authID] = a.Clone()
	heldMu.Unlock()
}

func heldAccount(authID string) *wb.Account {
	heldMu.Lock()
	defer heldMu.Unlock()
	if a := held[authID]; a != nil {
		return a.Clone()
	}
	return nil
}

// newest is the fresher of a stored sign-in and the one held here.
func newest(authID string, stored *wb.Account) *wb.Account {
	h := heldAccount(authID)
	if h == nil || stored == nil {
		if stored == nil {
			return h
		}
		return stored
	}
	if stored.Source == "key" {
		return stored
	}
	if h.Expires > stored.Expires || (h.Access != stored.Access && h.Refresh != stored.Refresh && stored.Expires <= h.Expires) {
		return h
	}
	return stored
}

var channels = struct {
	once sync.Once
	cn   *wb.Channel
	ai   *wb.Channel
}{}

func channelFor(site *wb.Site) *wb.Channel {
	channels.once.Do(func() {
		mk := func(get func() *wb.Site) *wb.Channel {
			ch := wb.NewChannel(get, func() wb.Options {
				c := cfg()
				return wb.Options{FreeOnly: c.freeOnly(get()), HideDsh: c.hideDsh(), Disabled: c.disabled(get())}
			})
			ch.Stored = heldAccount
			ch.Save = func(authID string, a *wb.Account) {
				holdAccount(authID, a)
				persist(authID, a)
			}
			return ch
		}
		channels.cn = mk(wb.SiteCN)
		channels.ai = mk(wb.SiteAI)
	})
	if site.AI {
		return channels.ai
	}
	return channels.cn
}

// persist writes a renewed sign-in back to its credential file, best effort:
// CPA's own refresh writes it too, on its schedule.
func persist(authID string, a *wb.Account) {
	heldMu.Lock()
	name := files[authID]
	heldMu.Unlock()
	if name == "" || a == nil {
		return
	}
	go func() {
		if err := authSave(name, storageOf(a)); err != nil {
			hostLog("warn", "could not write the renewed sign-in back: "+err.Error(), map[string]any{"auth": authID})
		}
	}()
}

func noteFile(authID string, attrs map[string]string, meta map[string]any) {
	path := ""
	if attrs != nil {
		path = attrs["path"]
		if path == "" {
			path = attrs["source"]
		}
	}
	if path == "" {
		return
	}
	name := path[strings.LastIndexAny(path, `/\`)+1:]
	if !strings.HasSuffix(strings.ToLower(name), ".json") {
		return
	}
	heldMu.Lock()
	files[authID] = name
	heldMu.Unlock()
}

// storageOf is a sign-in as its credential file holds it.
func storageOf(a *wb.Account) map[string]any {
	raw, _ := json.Marshal(a)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
}

// accountFrom reads the sign-in a CPA credential carries.
func accountFrom(provider, authID string, storage []byte, meta map[string]any) (*wb.Site, *wb.Account, error) {
	site := wb.SiteFor(provider)
	raw := storage
	if len(raw) == 0 && meta != nil {
		raw, _ = json.Marshal(meta)
	}
	a, err := wb.ParseAccount(raw)
	if err != nil {
		return site, nil, err
	}
	a = normKey(a)
	if site == nil {
		site = wb.SiteFor(a.Type)
	}
	if site == nil {
		site = wb.SiteCN()
	}
	return site, newest(authID, a), nil
}

// authDataOf is a sign-in as CPA's AuthData.
func authDataOf(site *wb.Site, a *wb.Account, fileName string) pluginapi.AuthData {
	a = a.Clone()
	a.Type = site.Provider
	storage, _ := json.Marshal(a)
	label := a.AccountID
	if label == "" {
		label = site.Name
	}
	meta := map[string]any{"type": site.Provider}
	if a.Source == "key" {
		meta["source"] = "key"
	}
	if fileName == "" {
		fileName = fileNameOf(site, a)
	}
	d := pluginapi.AuthData{
		Provider:    site.Provider,
		FileName:    fileName,
		Label:       label,
		StorageJSON: storage,
		Metadata:    meta,
		Attributes:  map[string]string{"site": site.ID},
	}
	if a.Source != "key" && a.Expires > 0 {
		d.NextRefreshAfter = time.UnixMilli(a.Expires).Add(-wb.RefreshLead)
	}
	return d
}

// fileNameOf names a sign-in's credential file: one per account, its
// enterprise apart.
func fileNameOf(site *wb.Site, a *wb.Account) string {
	who := a.UID
	if a.Source == "key" {
		who = "key-" + strings.TrimPrefix(a.UID, "anonymous_")
	}
	if who == "" {
		who = "unknown"
	}
	name := site.Provider + "-" + sanitize(who)
	if a.EnterpriseID != "" {
		name += "-" + sanitize(a.EnterpriseID)
	}
	return name + ".json"
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// normKey completes a key credential written with its key alone
// ({"type": ..., "source": "key", "key": ...}) as WorkBuddy's CLI sends one;
// any other account is returned as it is.
func normKey(a *wb.Account) *wb.Account {
	if a == nil || a.Source != "key" || a.Key == "" || (a.Access != "" && a.UID != "") {
		return a
	}
	k := wb.KeyAccount(a.Key)
	if k == nil {
		return a
	}
	k.Type = a.Type
	return k
}
