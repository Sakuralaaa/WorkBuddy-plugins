package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"github.com/Sakuralaaa/WorkBuddy-plugins/internal/wb"
)

// modelInfo is a model of a list as CPA's ModelInfo.
func modelInfo(site *wb.Site, m wb.Model) pluginapi.ModelInfo {
	info := pluginapi.ModelInfo{
		ID:                  m.ID,
		Object:              "model",
		OwnedBy:             site.Name,
		Type:                site.Provider,
		DisplayName:         m.Name,
		Name:                m.ID,
		ContextLength:       m.Context,
		InputTokenLimit:     m.Context,
		MaxCompletionTokens: m.Output,
		OutputTokenLimit:    m.Output,
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
		SupportedParameters:       []string{"tools", "tool_choice", "temperature", "max_tokens", "stream"},
	}
	if m.Images {
		info.SupportedInputModalities = []string{"text", "image"}
	}
	var levels []string
	zero := false
	for _, e := range m.Efforts {
		if e == "none" {
			zero = true
			continue
		}
		levels = append(levels, e)
	}
	if len(levels) > 0 {
		info.Thinking = &pluginapi.ThinkingSupport{Levels: levels, ZeroAllowed: zero}
		info.SupportedParameters = append(info.SupportedParameters, "reasoning_effort")
	}
	desc := ""
	if m.Free {
		desc = "free (x0.00)"
	} else if m.Rate != nil {
		desc = fmt.Sprintf("x%s credits", wb.Compact(*m.Rate))
	}
	if m.RateWas > 0 {
		desc += fmt.Sprintf(" (was x%s)", wb.Compact(m.RateWas))
	}
	if wb.NativeOf(site, m.ID) {
		if desc != "" {
			desc += "; "
		}
		desc += "served on Anthropic Messages"
	}
	info.Description = desc
	return info
}

func listInfos(site *wb.Site, list []wb.Model) []pluginapi.ModelInfo {
	out := make([]pluginapi.ModelInfo, 0, len(list))
	for _, m := range list {
		out = append(out, modelInfo(site, m))
	}
	return out
}

// onlyFree is a list with the free-only switch on: its free models, or an
// error when there are none to tell.
func onlyFree(site *wb.Site, list []wb.Model) ([]wb.Model, error) {
	if !cfg().freeOnly(site) {
		return list, nil
	}
	var free []wb.Model
	for _, m := range list {
		if m.Free {
			free = append(free, m)
		}
	}
	if len(free) == 0 {
		return nil, fmt.Errorf(`%s lists no free model, and "Free models only" is on`, site.Name)
	}
	return free, nil
}

// staticModels are each build's own table, before an account's list is read.
func staticModels(_ []byte) (any, error) {
	var regs []pluginapi.ProviderModels
	for _, site := range wb.Sites() {
		regs = append(regs, pluginapi.ProviderModels{Provider: site.Provider, Models: listInfos(site, wb.BuiltinList(site))})
	}
	return pluginapi.ModelResponse{Provider: wb.ProviderCN, Models: regs[0].Models, Registrations: regs}, nil
}

type authModelRequest struct {
	pluginapi.AuthModelRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

// modelsForAuth is the account's own list (/v3/config's cli agent), else the
// build's table; a key whose CLI reads no config gets the table.
func modelsForAuth(raw []byte) (any, error) {
	var req authModelRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	noteFile(req.AuthID, req.Attributes, req.Metadata)
	site, a, err := accountFrom(req.AuthProvider, req.AuthID, req.StorageJSON, req.Metadata)
	if err != nil {
		return nil, err
	}
	d := HostDoer{CallbackID: req.HostCallbackID}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	resp := pluginapi.ModelResponse{Provider: site.Provider}
	if a.Pending {
		if next, err := completeImport(ctx, d, site, a); err == nil {
			holdAccount(req.AuthID, next)
			a = next
			upd := authDataOf(site, next, "")
			upd.FileName = ""
			resp.AuthUpdate = upd
		} else {
			hostLog("warn", "imported sign-in could not be renewed: "+err.Error(), map[string]any{"auth": req.AuthID})
		}
	}
	fallback := func(e error) (any, error) {
		if cfg().freeOnly(site) {
			if e == nil {
				e = fmt.Errorf(`%s lists no prices for this key, and "Free models only" is on`, site.Name)
			}
			return nil, e
		}
		resp.Models = listInfos(site, wb.BuiltinList(site))
		return resp, nil
	}
	if a.Source == "key" && !wb.RemoteConfig(a.Key) {
		return fallback(nil)
	}
	if a.Source != "key" {
		fresh, err := wb.Fresh(ctx, d, site, a, func(n *wb.Account) {
			holdAccount(req.AuthID, n)
			upd := authDataOf(site, n, "")
			upd.FileName = ""
			resp.AuthUpdate = upd
		})
		if err != nil {
			return fallback(err)
		}
		a = fresh
	}
	ch := channelFor(site)
	list, err := ch.Listed(ctx, d, a, true)
	if err != nil {
		return fallback(err)
	}
	if len(list) == 0 && !cfg().freeOnly(site) {
		return fallback(nil)
	}
	list, err = onlyFree(site, list)
	if err != nil {
		return nil, err
	}
	resp.Models = listInfos(site, list)
	return resp, nil
}
