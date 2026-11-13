package plugin

import (
	"strings"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Sakuralaaa/WorkBuddy-plugins/internal/wb"
)

// ID is the plugin's id (its file name, workbuddy-magpie.so, and its key
// under plugins.configs).
const ID = "workbuddy-magpie"

// Version is this build's version.
var Version = "0.3.1"

// Tasks are the background benefit jobs ported from workbuddy2api-hub, each
// off until switched on.
type Tasks struct {
	// Growth runs the China growth centre's one-time tasks (成长任务).
	Growth bool `yaml:"growth" json:"growth"`
	// Campaigns runs the activity portal's campaign tasks (星巴克协作, 记者节).
	Campaigns bool `yaml:"campaigns" json:"campaigns"`
	// MiniProgram runs the WeChat mini-program's task chain.
	MiniProgram bool `yaml:"miniprogram" json:"miniprogram"`
	// CatTravel sends the Buddy cat travelling and claims its return
	// (猫猫旅行), opening Buddy boxes with spare energy.
	CatTravel bool `yaml:"cat_travel" json:"cat_travel"`
	// Rewards keeps the growth centre's streak: a make-up card for a missed
	// day, the gift and compensation packs, streak tiers and lottery draws.
	Rewards bool `yaml:"rewards" json:"rewards"`
	// Night reports the night-owl task's event between 23:00 and 08:00.
	Night bool `yaml:"night" json:"night"`
	// ActivityReport reports one chat_request_send a day on China accounts
	// (lights the growth centre's streak day).
	ActivityReport bool `yaml:"activity_report" json:"activity_report"`
	// IntlDaily is WorkBuddy AI's daily active session.
	IntlDaily bool `yaml:"intl_daily" json:"intl_daily"`
	// IntlDailyWeb adds its web-channel leg (a real cloud agent session).
	IntlDailyWeb bool `yaml:"intl_daily_web" json:"intl_daily_web"`
	// Claim claims a reward a task earned (workbuddy2api-hub's claim switch).
	Claim *bool `yaml:"claim" json:"claim"`
	// Sweep runs the claim sweep (补领清扫): every sweep_minutes, accept
	// and claim any task whose progress reached its target, whether or
	// not the growth job knows how to complete it.
	Sweep bool `yaml:"sweep" json:"sweep"`
}

// Schedule is when the jobs run, in Beijing hours (workbuddy2api-hub's
// defaults: check-in and travel at 09 and 21, night at 01).
type Schedule struct {
	CheckinAt     string   `yaml:"checkin_at" json:"checkin_at"`
	TravelHours   []int    `yaml:"travel_hours" json:"travel_hours"`
	GrowthHours   []int    `yaml:"growth_hours" json:"growth_hours"`
	NightHours    []int    `yaml:"night_hours" json:"night_hours"`
	DailyTimes    []string `yaml:"intl_daily_times" json:"intl_daily_times"`
	RewardsHours  []int    `yaml:"rewards_hours" json:"rewards_hours"`
	ActivityHours []int    `yaml:"activity_hours" json:"activity_hours"`
	// SweepMinutes is how often the claim sweep runs (15 unless set;
	// 0 turns the timed sweep off, manual runs still work).
	SweepMinutes *int `yaml:"sweep_minutes" json:"sweep_minutes"`
}

// Config is plugins.configs.workbuddy-magpie.
type Config struct {
	FreeOnly   bool  `yaml:"free_only" json:"free_only"`
	FreeOnlyCN *bool `yaml:"free_only_cn" json:"free_only_cn"`
	FreeOnlyAI *bool `yaml:"free_only_ai" json:"free_only_ai"`
	// HideDsh takes DeepSeek Harness's name out of what goes upstream; on
	// unless set false (magpie's hide_dsh).
	HideDsh *bool `yaml:"hide_dsh" json:"hide_dsh"`
	// DisabledCN / DisabledAI are model ids switched off per build: left out
	// of the model list CPA is given and refused before anything is sent.
	DisabledCN []string `yaml:"disabled_models_cn" json:"disabled_models_cn"`
	DisabledAI []string `yaml:"disabled_models_ai" json:"disabled_models_ai"`
	// CacheKey gives completions a per-account, per-conversation
	// prompt_cache_key; on unless set false.
	CacheKey *bool `yaml:"prompt_cache_key" json:"prompt_cache_key"`
	// ToolRepair turns DeepSeek's tool-call markup, when it leaks into the
	// text, back into tool_calls; on unless set false.
	ToolRepair *bool `yaml:"tool_markup_repair" json:"tool_markup_repair"`
	// CreditsRefresh is how often, in minutes, every account's credits are
	// read in the background (10 unless set; 0 turns it off).
	CreditsRefresh *int `yaml:"credits_refresh_minutes" json:"credits_refresh_minutes"`
	// Checkin is the China build's daily check-in (Buddy 加油站签到, the
	// billing meter's daily-checkin), once a Beijing day. Off unless on, as
	// magpie's switch.
	Checkin  bool     `yaml:"checkin" json:"checkin"`
	Tasks    Tasks    `yaml:"tasks" json:"tasks"`
	Schedule Schedule `yaml:"schedule" json:"schedule"`
	// Versions override what the requests say the desktop app is.
	CNApp string `yaml:"cn_app_version" json:"cn_app_version"`
	CNCLI string `yaml:"cn_cli_version" json:"cn_cli_version"`
	AIApp string `yaml:"ai_app_version" json:"ai_app_version"`
	AICLI string `yaml:"ai_cli_version" json:"ai_cli_version"`
	// SDKOS and SDKArch override X-Stainless-OS/Arch.
	SDKOS   string `yaml:"sdk_os" json:"sdk_os"`
	SDKArch string `yaml:"sdk_arch" json:"sdk_arch"`
	// DeviceToken is the global X-Device-Token (panel's config source):
	// the account's own device_token, when it has one, wins over this,
	// and DeviceTokenFile is the file fallback (see fingerprint.go).
	DeviceToken     string `yaml:"device_token" json:"device_token,omitempty"`
	DeviceTokenFile string `yaml:"device_token_file" json:"device_token_file,omitempty"`
	// TasksConcurrency is how many accounts the scheduler runs in
	// parallel (1–4, 2 unless set); each account still runs one job at
	// a time.
	TasksConcurrency *int `yaml:"tasks_concurrency" json:"tasks_concurrency,omitempty"`
	// Scheduler turns the background loop off altogether (false).
	Scheduler *bool `yaml:"scheduler" json:"scheduler"`
}

func (c *Config) hideDsh() bool { return c.HideDsh == nil || *c.HideDsh }

func (c *Config) cacheKey() bool { return c.CacheKey == nil || *c.CacheKey }

func (c *Config) toolRepair() bool { return c.ToolRepair == nil || *c.ToolRepair }

func (c *Config) creditsRefresh() time.Duration {
	if c.CreditsRefresh == nil {
		return 10 * time.Minute
	}
	if *c.CreditsRefresh <= 0 {
		return 0
	}
	if *c.CreditsRefresh < 5 {
		return 5 * time.Minute
	}
	return time.Duration(*c.CreditsRefresh) * time.Minute
}

func (c *Config) claim() bool { return c.Tasks.Claim == nil || *c.Tasks.Claim }

func (c *Config) schedulerOn() bool { return c.Scheduler == nil || *c.Scheduler }

// sweepMinutes is how often the claim sweep runs (15 unless set; 0 off).
func (c *Config) sweepMinutes() int {
	if c.Schedule.SweepMinutes == nil {
		return 15
	}
	if *c.Schedule.SweepMinutes < 0 {
		return 0
	}
	return *c.Schedule.SweepMinutes
}

// concurrency is how many accounts run in parallel (1–4, 2 unless set).
func (c *Config) concurrency() int {
	if c.TasksConcurrency == nil {
		return 2
	}
	if *c.TasksConcurrency < 1 {
		return 1
	}
	if *c.TasksConcurrency > 4 {
		return 4
	}
	return *c.TasksConcurrency
}

func (c *Config) freeOnly(site *wb.Site) bool {
	if site.AI && c.FreeOnlyAI != nil {
		return *c.FreeOnlyAI
	}
	if !site.AI && c.FreeOnlyCN != nil {
		return *c.FreeOnlyCN
	}
	return c.FreeOnly
}

// disabled is the set of model ids switched off for a build.
func (c *Config) disabled(site *wb.Site) map[string]bool {
	list := c.DisabledCN
	if site.AI {
		list = c.DisabledAI
	}
	if len(list) == 0 {
		return nil
	}
	out := make(map[string]bool, len(list))
	for _, id := range list {
		if id = strings.TrimSpace(id); id != "" {
			out[id] = true
		}
	}
	return out
}

func (c *Config) withDefaults() *Config {
	out := *c
	s := &out.Schedule
	if len(s.TravelHours) == 0 {
		s.TravelHours = []int{9, 21}
	}
	if len(s.GrowthHours) == 0 {
		s.GrowthHours = []int{10}
	}
	if len(s.NightHours) == 0 {
		s.NightHours = []int{1}
	}
	if len(s.RewardsHours) == 0 {
		s.RewardsHours = []int{9, 21}
	}
	if len(s.ActivityHours) == 0 {
		s.ActivityHours = []int{9, 21}
	}
	if len(s.DailyTimes) == 0 {
		s.DailyTimes = []string{"10:30", "20:30"}
	}
	return &out
}

var current atomic.Pointer[Config]

func cfg() *Config {
	if c := current.Load(); c != nil {
		return c
	}
	return (&Config{}).withDefaults()
}

// Configure reads the plugin's YAML subtree.
func Configure(raw []byte) error {
	c := &Config{}
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := yaml.Unmarshal(raw, c); err != nil {
			return err
		}
	}
	c = c.withDefaults()
	current.Store(c)
	wb.ResetVersions()
	wb.SetVersions(c.CNApp, c.CNCLI, c.AIApp, c.AICLI)
	if c.SDKOS != "" {
		wb.SDKOS = c.SDKOS
	}
	if c.SDKArch != "" {
		wb.SDKArch = c.SDKArch
	}
	return nil
}
