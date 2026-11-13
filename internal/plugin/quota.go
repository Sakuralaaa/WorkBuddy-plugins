package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/Sakuralaaa/WorkBuddy-plugins/internal/wb"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const summaryPath = "/billing/meter/get-user-resource-summary"

// quotaFetch reads an account's credits as magpie's usage card does.
func quotaFetch(req []byte) (any, error) {
	var r pluginapi.QuotaFetchRequest
	if err := json.Unmarshal(req, &r); err != nil {
		return nil, err
	}
	site, a, err := accountFrom(r.Provider, r.AuthID, r.StorageJSON, r.Metadata)
	if err != nil {
		return nil, fail("bad_auth", err.Error(), http.StatusBadRequest)
	}
	out := pluginapi.QuotaFetchResponse{}
	if a.Source == "key" {
		out.Groups = []pluginapi.QuotaGroup{{DisplayName: "credits unknown with an API key"}}
		return out, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var sum wb.Summary
	if err := wb.Meter(ctx, HostDoer{}, site, a, summaryPath, nil, &sum); err != nil {
		return nil, fail("quota_failed", err.Error(), http.StatusBadGateway)
	}
	u := wb.UsageOf(&sum, a.Plan, site)
	if u.Plan != "" {
		out.Subscription = &pluginapi.QuotaSubscription{Plan: u.Plan, TierName: u.Plan}
	}
	g := pluginapi.QuotaGroup{DisplayName: "WorkBuddy credits"}
	for _, w := range u.Windows {
		if w == nil {
			continue
		}
		left := 1 - w.Used/100
		if left < 0 {
			left = 0
		}
		g.Buckets = append(g.Buckets, pluginapi.QuotaBucket{Window: w.Name, RemainingFraction: left, ResetTime: w.ResetsAt, Description: w.Display})
		if w.Limit > 0 {
			out.Summary = append(out.Summary, pluginapi.QuotaMetric{Key: w.Name, Label: w.Name, Value: w.Limit - w.Amount, Unit: w.Unit})
		}
	}
	out.Groups = append(out.Groups, g)
	// The free-token window is counted apart from credits, so it is its
	// own group (magpie's usage card shows it beside the credits).
	if w := freeWindowFor(r.AuthID, site); w != nil {
		left := 1 - w.Used/100
		if left < 0 {
			left = 0
		}
		desc := w.Display
		if w.Refused {
			desc += " — refused until reset"
		} else if w.Aside {
			desc += " (estimated)"
		}
		out.Groups = append(out.Groups, pluginapi.QuotaGroup{DisplayName: w.Name, Buckets: []pluginapi.QuotaBucket{{Window: w.Name, RemainingFraction: left, ResetTime: w.ResetsAt, Description: desc}}})
		if w.Limit > 0 {
			out.Summary = append(out.Summary, pluginapi.QuotaMetric{Key: w.Name, Label: w.Name, Value: w.Limit - w.Amount, Unit: w.Unit})
		}
	}
	if runs := lastRuns(r.AuthID); len(runs) > 0 {
		jg := pluginapi.QuotaGroup{DisplayName: "Jobs"}
		for _, j := range runs {
			jg.Buckets = append(jg.Buckets, pluginapi.QuotaBucket{Window: lastKey(j), RemainingFraction: 1, ResetTime: j.At.Format(time.RFC3339), Description: j.Outcome + " " + j.Message})
		}
		out.Groups = append(out.Groups, jg)
	}
	return out, nil
}
