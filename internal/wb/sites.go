// Package wb is a port of magpie-community/plugins' WorkBuddy channel
// (packages/workbuddy/index.mjs, MIT) to Go, for CLIProxyAPI. Names and
// comments follow the original where they map one to one, so a change there
// can be found here; what CPA does differently is said where it is done.
package wb

import (
	"regexp"
	"sync"
	"time"
)

const (
	PollEvery     = time.Second
	SignInFor     = 5 * time.Minute
	EarlyLead     = 60 * time.Second // a token this close to its end is refreshed before a request
	RefreshLead   = 5 * time.Minute  // and this close, CPA's auth.refresh renews it ahead of time
	KeepRenewedMS = 60 * time.Second // how long a refresh's result answers for the old refresh token
)

// BuiltinModel is [id, name, context, efforts, output] of magpie's tables.
type BuiltinModel struct {
	ID      string
	Name    string
	Context int64
	Efforts []string
	Output  int64
}

// CNModels is the list used before an account's /v3/config is read.
var CNModels = []BuiltinModel{
	{"auto", "Auto", 168000, nil, 0},
	{"hy4-preview-f", "Hy4 preview", 1000000, []string{"high"}, 0},
	{"hy3", "Hy3", 192000, []string{"low", "high"}, 0},
	{"hy3-x", "Hy3-X", 192000, []string{"low", "high"}, 0},
	{"deepseek-v4.1-flash", "Deepseek-V4.1-Flash", 1000000, []string{"none", "low", "high", "max"}, 128000},
	{"glm-5.3", "GLM-5.3", 1000000, []string{"low", "high", "max"}, 0},
	{"glm-5.3-flash", "GLM-5.3-Flash", 1000000, []string{"low", "high", "max"}, 131072},
	{"glm-5.2", "GLM-5.2", 1000000, []string{"high", "xhigh"}, 0},
	{"glm-5.1", "GLM-5.1", 200000, nil, 0},
	{"glm-5v-turbo", "GLM-5v-Turbo", 200000, nil, 0},
	{"minimax-m3", "MiniMax-M3", 512000, nil, 0},
	{"kimi-k3-1", "Kimi-K3", 1000000, []string{"low", "high", "xhigh"}, 0},
	{"kimi-k2.7", "Kimi-K2.7-Code", 256000, nil, 0},
	{"kimi-k2.6", "Kimi-K2.6", 256000, nil, 0},
	{"deepseek-v4-pro", "Deepseek-V4-Pro", 1000000, []string{"none", "high", "xhigh"}, 128000},
}

// AIModels is WorkBuddy AI's list used before an account's config is read.
var AIModels = []BuiltinModel{
	{"default-model", "Default", 176000, nil, 24000},
	{"fast-model", "Fast", 200000, nil, 32000},
	{"balanced-model", "Balanced", 256000, nil, 0},
	{"primary-model", "Primary", 272000, nil, 72000},
	{"deep-model", "Deep", 176000, nil, 0},
	{"gpt-5.5", "GPT-5.5", 1000000, nil, 0},
	{"gpt-5.4", "GPT-5.4", 272000, nil, 0},
	{"gpt-5.3-codex", "GPT-5.3-Codex", 272000, nil, 0},
	{"gemini-3.1-pro", "Gemini-3.1-Pro", 400000, nil, 0},
	{"gemini-3.5-flash", "Gemini-3.5-Flash", 1000000, nil, 65536},
	{"glm-5.3", "GLM-5.3", 1000000, []string{"low", "high", "max"}, 0},
	{"glm-5.2", "GLM-5.2", 1000000, []string{"high", "xhigh"}, 48000},
	{"hy3", "Hy3", 192000, []string{"low", "high"}, 64000},
	{"kimi-k3", "Kimi-K3", 1000000, nil, 32000},
	{"kimi-k2.6", "Kimi-K2.6", 256000, nil, 0},
	{"minimax-m3", "MiniMax-M3", 512000, nil, 0},
}

// Site is one of WorkBuddy's two builds.
type Site struct {
	// ID is magpie's provider id ("workbuddy", "workbuddy-ai"); Provider is
	// the CPA provider key this port registers it under.
	ID       string
	Provider string
	Name     string
	Endpoint string
	AuthID   string
	Platform string
	AI       bool
	// the desktop app the requests are WorkBuddy's own client's of
	App     string
	CLI     string
	AppName string
	Runtime string
	Keys    string
	Models  []BuiltinModel
	// Messages: it serves the Claude models on Anthropic's Messages API too
	Messages bool
	NoImages *regexp.Regexp
}

// Provider keys. The store's codebuddy-intl-cpa registers "workbuddy" for
// both builds; these differ from it so the two plugins can be installed side
// by side, each with its own credential files.
const (
	ProviderCN = "workbuddy-magpie"
	ProviderAI = "workbuddy-magpie-ai"
)

var siteCN = &Site{
	ID: "workbuddy", Provider: ProviderCN, Name: "WorkBuddy",
	Endpoint: "https://copilot.tencent.com", AuthID: "workbuddy-desktop", Platform: "workbuddy",
	// Read from WorkBuddy 5.9.3.42398134, the update feed's current build on
	// 2026-10-28 (magpie). workbuddy2api-hub still sends 5.8.3 / CLI 2.161.0
	// on this build and says it could not check a newer one, so magpie's is
	// the closer match to the official client here.
	App: "5.9.3", CLI: "2.165.0", AppName: "WorkBuddy", Runtime: "local-agent",
	Keys: "https://www.workbuddy.cn/profile/keys", Models: CNModels, Messages: true,
}

var siteAI = &Site{
	ID: "workbuddy-ai", Provider: ProviderAI, Name: "WorkBuddy AI",
	Endpoint: "https://www.workbuddy.ai", AuthID: "workbuddy-desktop-ai", Platform: "workbuddy-ai", AI: true,
	// magpie read 5.9.3 off the update feed on 2026-10-29; workbuddy2api-hub
	// captured the desktop's own requests at 5.9.4 (User-Agent
	// "WorkBuddy/5.9.4 workbuddy-ai/5.9.4 CLI/2.165.0"), and magpie's #691
	// capture is 5.9.4 too, so this build sends 5.9.4.
	App: "5.9.4", CLI: "2.165.0", AppName: "workbuddy-ai", Runtime: "local-agent",
	Keys: "https://www.workbuddy.ai/profile/keys", Models: AIModels,
	NoImages: regexp.MustCompile(`^glm-`),
}

var sitesMu sync.RWMutex

// Sites are both builds, the China one first.
func Sites() []*Site { return []*Site{SiteCN(), SiteAI()} }

// SiteCN is the China build.
func SiteCN() *Site {
	sitesMu.RLock()
	defer sitesMu.RUnlock()
	return siteCN
}

// SiteAI is the international build.
func SiteAI() *Site {
	sitesMu.RLock()
	defer sitesMu.RUnlock()
	return siteAI
}

// SiteFor is the site a CPA provider key (or magpie id) names; nil for none.
func SiteFor(provider string) *Site {
	switch provider {
	case ProviderCN, "workbuddy":
		return SiteCN()
	case ProviderAI, "workbuddy-ai":
		return SiteAI()
	}
	return nil
}

// SetVersions overrides the app and CLI version a build's requests say
// (plugin config); empty keeps the default.
func SetVersions(cnApp, cnCLI, aiApp, aiCLI string) {
	sitesMu.Lock()
	defer sitesMu.Unlock()
	cn := *siteCN
	ai := *siteAI
	if cnApp != "" {
		cn.App = cnApp
	}
	if cnCLI != "" {
		cn.CLI = cnCLI
	}
	if aiApp != "" {
		ai.App = aiApp
	}
	if aiCLI != "" {
		ai.CLI = aiCLI
	}
	siteCN, siteAI = &cn, &ai
}

// ResetVersions puts the default versions back.
func ResetVersions() { SetVersions("5.9.3", "2.165.0", "5.9.4", "2.165.0") }

// TextOnly says whether site drops the images sent to model, whatever its
// config says (WorkBuddy AI's GLM models).
func TextOnly(site *Site, model string) bool {
	return site != nil && site.NoImages != nil && site.NoImages.MatchString(model)
}

var claudeID = regexp.MustCompile(`(?i)^claude-`)

// NativeOf is whether site serves model on Messages rather than chat.
func NativeOf(site *Site, model string) bool {
	return site != nil && site.Messages && claudeID.MatchString(model)
}

// API is the chat API base.
func (s *Site) API() string { return s.Endpoint + "/v2" }

// APIMessages is the Messages API base.
func (s *Site) APIMessages() string { return s.Endpoint + "/v1" }

// OffTaken are models that turn thinking off for reasoning_effort "none"
// though the config says onlyReasoning.
var OffTaken = map[string]bool{"deepseek-v4.1-flash": true, "deepseek-v4.1-flash-sg": true}

// ModelName is the table's name for id, else id.
func (s *Site) ModelName(id string) string {
	for _, m := range s.Models {
		if m.ID == id {
			return m.Name
		}
	}
	return id
}
