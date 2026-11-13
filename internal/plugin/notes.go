package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"
)

// Account notes: a label the user gives an account on the page, kept beside
// the state file. Keyed by the account's id (its auth file).
const notesFileName = ".workbuddy-magpie-notes"

const noteMaxRunes = 40

var notes = struct {
	sync.Mutex
	loaded bool
	m      map[string]string
}{}

func notesPath() string { return filepath.Join(stateDir(), notesFileName) }

func loadNotes() {
	if notes.loaded {
		return
	}
	notes.loaded = true
	notes.m = map[string]string{}
	if raw, err := os.ReadFile(notesPath()); err == nil {
		_ = json.Unmarshal(raw, &notes.m)
		if notes.m == nil {
			notes.m = map[string]string{}
		}
	}
}

// noteOf returns the note of an account, or "".
func noteOf(id string) string {
	notes.Lock()
	defer notes.Unlock()
	loadNotes()
	return notes.m[id]
}

// cleanNote trims a note, drops control characters and caps its length.
func cleanNote(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if utf8.RuneCountInString(s) > noteMaxRunes {
		s = string([]rune(s)[:noteMaxRunes])
	}
	return s
}

// setNote stores (or, when empty, removes) an account's note.
func setNote(id, note string) (string, error) {
	note = cleanNote(note)
	notes.Lock()
	defer notes.Unlock()
	loadNotes()
	if note == "" {
		delete(notes.m, id)
	} else {
		notes.m[id] = note
	}
	raw, _ := json.MarshalIndent(notes.m, "", "  ")
	tmp := notesPath() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return "", err
	}
	return note, os.Rename(tmp, notesPath())
}

func handleSetNote(body []byte) (any, error) {
	var req struct {
		Account string `json:"account"`
		Note    string `json:"note"`
	}
	if err := json.Unmarshal(body, &req); err != nil || strings.TrimSpace(req.Account) == "" {
		return replyErr(400, "需要 account")
	}
	list, err := accounts()
	if err != nil {
		return replyErr(502, err.Error())
	}
	found := false
	for _, x := range list {
		if x.ID == req.Account {
			found = true
			break
		}
	}
	if !found {
		return replyErr(404, "没有这个账号")
	}
	n, err := setNote(req.Account, req.Note)
	if err != nil {
		return replyErr(500, "保存备注失败："+err.Error())
	}
	return reply(200, map[string]any{"ok": true, "account": req.Account, "note": n})
}
