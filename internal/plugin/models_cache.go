package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Sakuralaaa/WorkBuddy-plugins/internal/wb"
)

// The last live model list of each build, kept beside the state file so the
// models page still shows prices and promotions when no account can read the
// list right now.
const modelsFileName = ".workbuddy-magpie-models"

type modelsEntry struct {
	At          int64      `json:"at"`
	AccountName string     `json:"account_name,omitempty"`
	Live        []wb.Model `json:"live"`
}

var modelsCacheMu sync.Mutex

func modelsPath() string { return filepath.Join(stateDir(), modelsFileName) }

func readModelsFile() map[string]modelsEntry {
	m := map[string]modelsEntry{}
	if raw, err := os.ReadFile(modelsPath()); err == nil {
		_ = json.Unmarshal(raw, &m)
	}
	if m == nil {
		m = map[string]modelsEntry{}
	}
	return m
}

func cachedModels(build string) (modelsEntry, bool) {
	modelsCacheMu.Lock()
	defer modelsCacheMu.Unlock()
	e, ok := readModelsFile()[build]
	return e, ok && len(e.Live) > 0
}

func saveModels(build string, live []wb.Model, accountName string) {
	if len(live) == 0 {
		return
	}
	modelsCacheMu.Lock()
	defer modelsCacheMu.Unlock()
	m := readModelsFile()
	m[build] = modelsEntry{At: time.Now().UnixMilli(), AccountName: accountName, Live: live}
	raw, err := json.Marshal(m)
	if err != nil {
		return
	}
	tmp := modelsPath() + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, modelsPath())
	}
}
