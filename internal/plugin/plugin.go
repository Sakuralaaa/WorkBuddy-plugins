package plugin

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"github.com/Sakuralaaa/WorkBuddy-plugins/internal/wb"
)

type envelope struct {
	OK     bool             `json:"ok"`
	Result json.RawMessage  `json:"result,omitempty"`
	Error  *pluginabi.Error `json:"error,omitempty"`
}

func okEnvelope(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errEnvelope(e *pluginabi.Error) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: e})
	return raw
}

// Failure is an error to answer CPA with as a structured plugin error.
type Failure struct{ E *pluginabi.Error }

func (f *Failure) Error() string { return f.E.Message }

func fail(code, msg string, status int) error {
	return &Failure{E: &pluginabi.Error{Code: code, Message: msg, HTTPStatus: status}}
}

type capabilities struct {
	ModelProvider         bool     `json:"model_provider"`
	AuthProvider          bool     `json:"auth_provider"`
	Executor              bool     `json:"executor"`
	ExecutorModelScope    string   `json:"executor_model_scope"`
	ExecutorInputFormats  []string `json:"executor_input_formats,omitempty"`
	ExecutorOutputFormats []string `json:"executor_output_formats,omitempty"`
	ManagementAPI         bool     `json:"management_api"`
	QuotaProvider         bool     `json:"quota_provider"`
}

type registration struct {
	SchemaVersion uint32             `json:"schema_version"`
	Metadata      pluginapi.Metadata `json:"metadata"`
	Capabilities  capabilities       `json:"capabilities"`
}

// Formats the executor takes and gives. Chat completions is WorkBuddy's
// own; Anthropic Messages is the China build's for its Claude models, and
// the plugin translates between the two itself (with CPA's own
// translators, built in) so each model goes on the route magpie sends it
// on whatever the client spoke. OpenAI Responses clients (Codex) reach it
// through CPA's responses→chat translation.
var (
	inputFormats  = []string{"chat-completions", "claude"}
	outputFormats = []string{"chat-completions", "claude"}
)

func configFields() []pluginapi.ConfigField {
	b := pluginapi.ConfigFieldTypeBoolean
	s := pluginapi.ConfigFieldTypeString
	o := pluginapi.ConfigFieldTypeObject
	return []pluginapi.ConfigField{
		{Name: "free_only", Type: b, Default: false, Description: "Free models only (both builds): list only models served at no cost and turn away any other request before it is sent. free_only_cn / free_only_ai set one build."},
		{Name: "free_only_cn", Type: b, Description: "Free models only, WorkBuddy (China) alone."},
		{Name: "free_only_ai", Type: b, Description: "Free models only, WorkBuddy AI alone."},
		{Name: "disabled_models_cn", Type: pluginapi.ConfigFieldTypeArray, Description: "Model ids switched off for WorkBuddy (China): not listed, and refused if asked for."},
		{Name: "disabled_models_ai", Type: pluginapi.ConfigFieldTypeArray, Description: "Model ids switched off for WorkBuddy AI."},
		{Name: "prompt_cache_key", Type: b, Default: true, Description: "Give each completion a prompt_cache_key of its own account and conversation, so WorkBuddy reads the conversation's prefix from its cache."},
		{Name: "tool_markup_repair", Type: b, Default: true, Description: "Turn DeepSeek's own tool-call markup, when it leaks into the answer's text, back into tool_calls."},
		{Name: "credits_refresh_minutes", Type: pluginapi.ConfigFieldTypeInteger, Default: 10, Description: "Read every account's credits in the background this often, in minutes (at least 5; 0 turns it off)."},
		{Name: "hide_dsh", Type: b, Default: true, Description: "Take DeepSeek Harness's name out of what is sent (its opening line, checkout and Web GUI lines)."},
		{Name: "checkin", Type: b, Default: false, Description: "China daily check-in (Buddy 加油站签到, 100 credits) once a Beijing day per account, as magpie's switch."},
		{Name: "tasks", Type: o, Description: "Background benefit jobs, each off unless true: growth, campaigns, miniprogram, cat_travel, rewards, night, activity_report, intl_daily, intl_daily_web; claim (default true) claims what they earn. Easiest set on the plugin's WorkBuddy page."},
		{Name: "schedule", Type: o, Description: "When jobs run (Beijing time): checkin_at (HH:MM), travel_hours, growth_hours, night_hours, rewards_hours, activity_hours (whole hours), intl_daily_times (HH:MM list)."},
		{Name: "scheduler", Type: b, Default: true, Description: "false stops the background loop (check-in and tasks) altogether."},
		{Name: "cn_app_version", Type: s, Description: "WorkBuddy (China) desktop version the requests say (default 5.9.3)."},
		{Name: "cn_cli_version", Type: s, Description: "Its CLI version (default 2.165.0)."},
		{Name: "ai_app_version", Type: s, Description: "WorkBuddy AI desktop version (default 5.9.4)."},
		{Name: "ai_cli_version", Type: s, Description: "Its CLI version (default 2.165.0)."},
		{Name: "sdk_os", Type: s, Description: "X-Stainless-OS sent (default: this host's, as magpie)."},
		{Name: "sdk_arch", Type: s, Description: "X-Stainless-Arch sent (default: this host's)."},
	}
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             "WorkBuddy (magpie port)",
			Version:          Version,
			Author:           "magpie-community (MIT), CPA port",
			GitHubRepository: "https://github.com/Sakuralaaa/WorkBuddy-plugins",
			ConfigFields:     configFields(),
		},
		Capabilities: capabilities{
			ModelProvider:         true,
			AuthProvider:          true,
			Executor:              true,
			ExecutorModelScope:    string(pluginapi.ExecutorModelScopeOAuth),
			ExecutorInputFormats:  inputFormats,
			ExecutorOutputFormats: outputFormats,
			ManagementAPI:         true,
			QuotaProvider:         true,
		},
	}
}

type lifecycle struct {
	ConfigYAML []byte `json:"config_yaml"`
}

// Handle answers one RPC method.
func Handle(method string, req []byte) ([]byte, error) {
	out, err := handle(method, req)
	if err != nil {
		var f *Failure
		if errors.As(err, &f) {
			return errEnvelope(f.E), nil
		}
		return errEnvelope(&pluginabi.Error{Code: "plugin_error", Message: err.Error()}), nil
	}
	return okEnvelope(out)
}

func handle(method string, req []byte) (any, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		var l lifecycle
		if len(req) > 0 {
			if err := json.Unmarshal(req, &l); err != nil {
				return nil, err
			}
		}
		if err := Configure(l.ConfigYAML); err != nil {
			return nil, err
		}
		startScheduler()
		return pluginRegistration(), nil
	case pluginabi.MethodPluginQuiesce:
		return struct{}{}, nil
	case pluginabi.MethodPluginShutdown:
		stopScheduler()
		return struct{}{}, nil
	case pluginabi.MethodAuthIdentifier:
		return authIdentifier(), nil
	case pluginabi.MethodAuthParse:
		return authParse(req)
	case pluginabi.MethodAuthLoginStart:
		return loginStart(req)
	case pluginabi.MethodAuthLoginPoll:
		return loginPoll(req)
	case pluginabi.MethodAuthRefresh:
		return authRefresh(req)
	case pluginabi.MethodModelStatic:
		return staticModels(req)
	case pluginabi.MethodModelForAuth:
		return modelsForAuth(req)
	case pluginabi.MethodExecutorIdentifier:
		return map[string]string{"identifier": wb.ProviderCN}, nil
	case pluginabi.MethodExecutorExecute:
		return execute(req)
	case pluginabi.MethodExecutorExecuteStream:
		return executeStream(req)
	case pluginabi.MethodExecutorCountTokens:
		return countTokens(req)
	case pluginabi.MethodQuotaIdentifier:
		return map[string]string{"identifier": wb.ProviderCN}, nil
	case pluginabi.MethodQuotaDescribe:
		return pluginapi.QuotaDescribeResponse{SupportedProviders: []string{wb.ProviderCN, wb.ProviderAI}, DisplayName: "WorkBuddy credits"}, nil
	case pluginabi.MethodQuotaFetch:
		return quotaFetch(req)
	case pluginabi.MethodQuotaReset:
		return pluginapi.QuotaResetResponse{Success: false, Message: "WorkBuddy credits can't be reset"}, nil
	case pluginabi.MethodManagementRegister:
		return managementRoutes(), nil
	case pluginabi.MethodManagementHandle:
		return managementHandle(req)
	}
	return nil, fail("unknown_method", fmt.Sprintf("unknown method: %s", method), 0)
}
