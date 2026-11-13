package wb

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Model is a model of an account's live list (magpie's liveModels entry).
type Model struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Context int64    `json:"context,omitempty"`
	Output  int64    `json:"output,omitempty"`
	Images  bool     `json:"images,omitempty"`
	Efforts []string `json:"efforts,omitempty"`
	Effort  string   `json:"effort,omitempty"`
	Free    bool     `json:"free,omitempty"`
	Rate    *float64 `json:"rate,omitempty"`
	RateWas float64  `json:"rateWas,omitempty"`
	// Tools: the config says the model takes tool calls. AlwaysThink: it
	// reasons and thinking can't be turned off.
	Tools       bool    `json:"tools,omitempty"`
	AlwaysThink bool    `json:"alwaysThink,omitempty"`
	Badges      []Badge `json:"badges,omitempty"`
}

// Badge is a promotion running for a model, as WorkBuddy's client labels it
// ("限时免费", "错峰使用"). Active is false outside its daily hours.
type Badge struct {
	Label  string   `json:"label"`
	Note   string   `json:"note,omitempty"`
	Hours  []string `json:"hours,omitempty"` // "23:00-07:50", Asia/Shanghai
	Until  int64    `json:"until,omitempty"` // unix ms
	Active bool     `json:"active"`
	Rate   *float64 `json:"rate,omitempty"` // the price while it is active
}

// ConfigModel is a model of /v3/config.
type ConfigModel struct {
	ID                string          `json:"id"`
	Name              string          `json:"name"`
	Credits           json.RawMessage `json:"credits"`
	MaxInputTokens    int64           `json:"maxInputTokens"`
	MaxOutputTokens   int64           `json:"maxOutputTokens"`
	SupportsImages    *bool           `json:"supportsImages"`
	SupportsReasoning *bool           `json:"supportsReasoning"`
	SupportsToolCall  *bool           `json:"supportsToolCall"`
	OnlyReasoning     bool            `json:"onlyReasoning"`
	Reasoning         *struct {
		CanDisableThinking *bool    `json:"canDisableThinking"`
		DefaultEffort      *string  `json:"defaultEffort"`
		Effort             *string  `json:"effort"`
		SupportedEfforts   []string `json:"supportedEfforts"`
	} `json:"reasoning"`
}

// Promotion is a config's modelPromotions entry.
type Promotion struct {
	Kind     string `json:"kind"`
	Priority int    `json:"priority"`
	Badge    *struct {
		Label string `json:"label"`
	} `json:"badge"`
	Hover *struct {
		TextZh string `json:"textZh"`
	} `json:"hover"`
	ModelIDs []string `json:"modelIds"`
	Enabled  *bool    `json:"enabled"`
	Schedule *struct {
		ValidFrom  string            `json:"validFrom"`
		ValidUntil string            `json:"validUntil"`
		Daily      []json.RawMessage `json:"daily"`
	} `json:"schedule"`
	Discount *struct {
		DiscountedCredits json.RawMessage `json:"discountedCredits"`
		Factor            json.RawMessage `json:"factor"`
	} `json:"discount"`
}

// Config is /v3/config's data.
type Config struct {
	Agents []struct {
		Name   string   `json:"name"`
		Models []string `json:"models"`
	} `json:"agents"`
	Models          []ConfigModel `json:"models"`
	ModelPromotions []Promotion   `json:"modelPromotions"`
}

func rawString(raw json.RawMessage) (string, bool) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return "", false
	}
	if strings.HasPrefix(s, `"`) {
		var v string
		if json.Unmarshal(raw, &v) == nil {
			return v, true
		}
		return "", false
	}
	return s, true
}

var creditsSuffix = regexp.MustCompile(`\s+credits?$`)

// jsNumber is Number(s) for a trimmed string.
func jsNumber(s string) (float64, bool) {
	if s == "" {
		return 0, true
	}
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		n, err := strconv.ParseInt(s[2:], 16, 64)
		return float64(n), err == nil
	}
	if s == "Infinity" || s == "+Infinity" {
		return math.Inf(1), true
	}
	if s == "-Infinity" {
		return math.Inf(-1), true
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// CreditsOf reads a model's credits ("x0.03", "0", 0); ok false when it
// can't be read.
func CreditsOf(raw json.RawMessage) (float64, bool) {
	s, ok := rawString(raw)
	if !ok || s == "" {
		return 0, false
	}
	return creditsText(s)
}

func creditsText(s string) (float64, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	s = creditsSuffix.ReplaceAllString(s, "")
	s = strings.TrimPrefix(s, "x")
	if s == "" {
		return 0, false
	}
	n, ok := jsNumber(s)
	if !ok || math.IsInf(n, 0) || math.IsNaN(n) || n < 0 {
		return 0, false
	}
	return n, true
}

// FreeCredits says whether a model's credits are none.
func FreeCredits(raw json.RawMessage) bool {
	n, ok := CreditsOf(raw)
	return ok && n == 0
}

func discountOf(p Promotion, base float64, hasBase bool) (float64, bool) {
	if p.Discount == nil {
		return 0, false
	}
	s, _ := rawString(p.Discount.DiscountedCredits)
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(strings.TrimSuffix(s, "x"), "X")
	if n, ok := creditsText(s); ok {
		return n, true
	}
	fs, ok := rawString(p.Discount.Factor)
	if !ok {
		return 0, false
	}
	f, ok := jsNumber(strings.TrimSpace(fs))
	if ok && !math.IsInf(f, 0) && f >= 0 && hasBase {
		return base * f, true
	}
	return 0, false
}

func jsDateParse(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05Z07:00", "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UnixMilli(), true
		}
	}
	return 0, false
}

func running(p Promotion, now int64) bool {
	if p.Enabled != nil && !*p.Enabled {
		return false
	}
	var from, until int64
	var hasFrom, hasUntil bool
	if p.Schedule != nil {
		from, hasFrom = jsDateParse(p.Schedule.ValidFrom)
		until, hasUntil = jsDateParse(p.Schedule.ValidUntil)
	}
	return !(hasFrom && from > now) && !(hasUntil && until <= now)
}

// Price is a model's price as magpie keeps it.
type Price struct {
	Known   bool
	Free    bool
	Rate    float64
	HasRate bool
	RateWas float64
}

// PriceOf is a model's price from its credits and the config's promotions;
// a price that holds some hours of the day alone is its full price.
func PriceOf(d ConfigModel, promotions []Promotion, now int64) Price {
	base, hasBase := CreditsOf(d.Credits)
	var mine []Promotion
	for _, p := range promotions {
		if p.Kind != "discount" || !containsStr(p.ModelIDs, d.ID) || !running(p, now) {
			continue
		}
		mine = append(mine, p)
	}
	daily := func(p Promotion) bool { return p.Schedule != nil && len(p.Schedule.Daily) > 0 }
	for _, p := range mine {
		if daily(p) {
			nightFree := false
			for _, q := range mine {
				if q.Discount != nil && daily(q) {
					if r, ok := discountOf(q, base, hasBase); ok && r == 0 {
						nightFree = true
					}
				}
			}
			if !hasBase || (base == 0 && nightFree) {
				return Price{Known: true}
			}
			return Price{Known: true, Rate: base, HasRate: true}
		}
	}
	var off []float64
	for _, p := range mine {
		if p.Discount == nil {
			continue
		}
		if r, ok := discountOf(p, base, hasBase); ok {
			off = append(off, r)
		}
	}
	if len(off) > 0 {
		r := off[0]
		for _, x := range off[1:] {
			r = math.Min(r, x)
		}
		out := Price{Known: true, Free: r == 0, Rate: r, HasRate: true}
		if hasBase && base > r {
			out.RateWas = base
		}
		return out
	}
	if !hasBase {
		return Price{}
	}
	return Price{Known: true, Free: base == 0, Rate: base, HasRate: true}
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// EffortOrder is the order WorkBuddy's levels go in.
var EffortOrder = []string{"low", "medium", "high", "xhigh", "max"}

// EffortsOf are the levels a config model takes.
func EffortsOf(d ConfigModel, site *Site, id string) []string {
	if d.Reasoning != nil && len(d.Reasoning.SupportedEfforts) > 0 {
		said := d.Reasoning.SupportedEfforts
		if NativeOf(site, id) && !containsStr(said, "max") {
			return append(append([]string{}, said...), "max")
		}
		return said
	}
	if d.SupportsReasoning == nil || !*d.SupportsReasoning {
		return nil
	}
	set := map[string]bool{"low": true, "high": true, "max": true}
	if d.Reasoning != nil && d.Reasoning.Effort != nil && containsStr(EffortOrder, *d.Reasoning.Effort) {
		set[*d.Reasoning.Effort] = true
	}
	var out []string
	for _, e := range EffortOrder {
		if set[e] {
			out = append(out, e)
		}
	}
	return out
}

// DefaultEffortOf is the level WorkBuddy's client asks a model for when the
// user picked none.
func DefaultEffortOf(d ConfigModel, efforts []string) string {
	if d.Reasoning == nil {
		return ""
	}
	own := d.Reasoning.DefaultEffort
	if own == nil {
		own = d.Reasoning.Effort
	}
	if own != nil && containsStr(efforts, *own) {
		return *own
	}
	return ""
}

// ParseConfig reads /v3/config's models into the account's list.
func ParseConfig(site *Site, cfg *Config, now int64) []Model {
	var out []Model
	seen := map[string]bool{}
	find := func(id string) *ConfigModel {
		for i := range cfg.Models {
			if cfg.Models[i].ID == id {
				return &cfg.Models[i]
			}
		}
		return nil
	}
	add := func(id string, d *ConfigModel) {
		seen[id] = true
		m := Model{ID: id, Name: id}
		if d != nil && d.Name != "" {
			m.Name = d.Name
		}
		if d != nil {
			m.Context = d.MaxInputTokens
			m.Output = d.MaxOutputTokens
			m.Images = d.SupportsImages != nil && *d.SupportsImages && !TextOnly(site, id)
			p := PriceOf(*d, cfg.ModelPromotions, now)
			m.Free = p.Free
			if p.HasRate {
				r := p.Rate
				m.Rate = &r
			}
			m.RateWas = p.RateWas
			m.Tools = d.SupportsToolCall == nil || *d.SupportsToolCall
			m.Badges = BadgesOf(*d, cfg.ModelPromotions, now)
			if es := EffortsOf(*d, site, id); len(es) > 0 {
				canOffSaid := d.Reasoning != nil && d.Reasoning.CanDisableThinking != nil
				canOff := !canOffSaid || *d.Reasoning.CanDisableThinking
				off := (!d.OnlyReasoning && canOff) || OffTaken[id]
				if off {
					m.Efforts = append([]string{"none"}, es...)
				} else {
					m.Efforts = append([]string{}, es...)
				}
				m.Effort = DefaultEffortOf(*d, es)
				m.AlwaysThink = !off
			}
		}
		out = append(out, m)
	}
	cli := false
	for _, agent := range cfg.Agents {
		if agent.Name != "cli" {
			continue
		}
		cli = true
		for _, id := range agent.Models {
			add(id, find(id))
		}
	}
	if cli {
		for i := range cfg.Models {
			d := &cfg.Models[i]
			if strings.HasPrefix(d.ID, "custom:") && !seen[d.ID] {
				add(d.ID, d)
			}
		}
	}
	first := map[string]string{}
	for i := range out {
		m := &out[i]
		id, ok := first[m.Name]
		if !ok {
			first[m.Name] = m.ID
			continue
		}
		rest := ""
		if strings.HasPrefix(m.ID, id+"-") {
			rest = m.ID[len(id)+1:]
		}
		if rest != "" {
			m.Name = fmt.Sprintf("%s (%s)", m.Name, strings.ToUpper(rest))
		} else {
			m.Name = fmt.Sprintf("%s (%s)", m.Name, m.ID)
		}
	}
	return out
}

// LiveModels asks WorkBuddy's product config for the account's models.
func LiveModels(ctx context.Context, d Doer, site *Site, a *Account) ([]Model, error) {
	h := http.Header{}
	Sign(site, a, h)
	h.Set("User-Agent", CLIAgent(site))
	h.Set("X-Requested-With", "XMLHttpRequest")
	h.Set("Accept", "application/json")
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	res, err := d.Do(ctx, &Request{Method: http.MethodGet, URL: EndpointOf(site, a) + "/v3/config", Header: h})
	if err != nil {
		return nil, err
	}
	var env struct {
		Code json.RawMessage `json:"code"`
		Msg  string          `json:"msg"`
		Data *Config         `json:"data"`
	}
	_ = json.Unmarshal(res.Body, &env)
	if res.Status < 200 || res.Status >= 300 {
		return nil, fmt.Errorf("%s's config: HTTP %d", site.Name, res.Status)
	}
	if c := codeOf(env.Code); c != 0 {
		return nil, fmt.Errorf("%s's config: %s (%d)", site.Name, env.Msg, c)
	}
	cfg := env.Data
	if cfg == nil {
		cfg = &Config{}
	}
	return ParseConfig(site, cfg, time.Now().UnixMilli()), nil
}

// BuiltinList is the site's own table as a list, for before an account's is
// read.
func BuiltinList(site *Site) []Model {
	out := make([]Model, 0, len(site.Models))
	for _, b := range site.Models {
		m := Model{ID: b.ID, Name: b.Name, Context: b.Context, Output: b.Output, Images: !TextOnly(site, b.ID)}
		if len(b.Efforts) > 0 {
			m.Efforts = append([]string{}, b.Efforts...)
			m.AlwaysThink = !containsStr(b.Efforts, "none")
		}
		m.Tools = true
		out = append(out, m)
	}
	return out
}

var promoZone = time.FixedZone("CST", 8*3600)

func clockOf(raw json.RawMessage) (start, end string, ok bool) {
	var w struct {
		Start string `json:"start"`
		End   string `json:"end"`
	}
	if json.Unmarshal(raw, &w) != nil || w.Start == "" || w.End == "" {
		return "", "", false
	}
	return w.Start, w.End, true
}

func minutesOf(hhmm string) (int, bool) {
	var h, m int
	if _, err := fmt.Sscanf(strings.TrimSpace(hhmm), "%d:%d", &h, &m); err != nil || h < 0 || h > 24 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

func pad(hhmm string) string {
	if n, ok := minutesOf(hhmm); ok {
		return fmt.Sprintf("%02d:%02d", n/60, n%60)
	}
	return hhmm
}

// BadgesOf are the promotions running for a model today, the one WorkBuddy
// would show first leading: within its dates, and Active when now falls in
// one of its daily windows (or it has none).
func BadgesOf(d ConfigModel, promotions []Promotion, now int64) []Badge {
	base, hasBase := CreditsOf(d.Credits)
	cur := time.UnixMilli(now).In(promoZone)
	curMin := cur.Hour()*60 + cur.Minute()
	type item struct {
		b    Badge
		prio int
	}
	var list []item
	seen := map[string]int{}
	for _, p := range promotions {
		if !containsStr(p.ModelIDs, d.ID) || !running(p, now) {
			continue
		}
		b := Badge{Active: true}
		if p.Badge != nil {
			b.Label = strings.TrimSpace(p.Badge.Label)
		}
		if p.Hover != nil {
			b.Note = strings.TrimSpace(p.Hover.TextZh)
		}
		if p.Schedule != nil {
			if t, ok := jsDateParse(p.Schedule.ValidUntil); ok {
				b.Until = t
			}
			if len(p.Schedule.Daily) > 0 {
				b.Active = false
				for _, raw := range p.Schedule.Daily {
					st, ed, ok := clockOf(raw)
					if !ok {
						continue
					}
					b.Hours = append(b.Hours, pad(st)+"-"+pad(ed))
					a, ok1 := minutesOf(st)
					z, ok2 := minutesOf(ed)
					if ok1 && ok2 && ((a <= z && curMin >= a && curMin < z) || (a > z && (curMin >= a || curMin < z))) {
						b.Active = true
					}
				}
			}
		}
		if r, ok := discountOf(p, base, hasBase); ok {
			b.Rate = &r
		}
		if b.Label == "" {
			switch {
			case b.Rate != nil && *b.Rate == 0:
				b.Label = "限时免费"
			case len(b.Hours) > 0:
				b.Label = "错峰使用"
			case b.Rate != nil:
				b.Label = "限时折扣"
			default:
				continue
			}
		}
		key := b.Label + strings.Join(b.Hours, ",")
		if i, dup := seen[key]; dup {
			if p.Priority > list[i].prio {
				list[i] = item{b, p.Priority}
			}
			continue
		}
		seen[key] = len(list)
		list = append(list, item{b, p.Priority})
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].b.Active != list[j].b.Active {
			return list[i].b.Active
		}
		return list[i].prio > list[j].prio
	})
	out := make([]Badge, len(list))
	for i := range list {
		out[i] = list[i].b
	}
	return out
}
