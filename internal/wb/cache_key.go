package wb

import (
	"crypto/sha256"
	"encoding/hex"
)

// CacheKeyOf is a completion's prompt_cache_key: stable for one account and
// conversation, and never the same for two accounts, so no account is ever
// answered from another's cached prefix. WorkBuddy honours the field (as
// workbuddy2api-panel found: an 8k prefix read from cache costs a fraction of
// one sent fresh).
func CacheKeyOf(a *Account, conversation string) string {
	uid := ""
	if a != nil {
		uid = a.UID
		if uid == "" {
			uid = a.AccountID
		}
	}
	u8 := uid
	if uid == "" && a != nil && a.Key != "" {
		k := sha256.Sum256([]byte(a.Key))
		uid = "key:" + hex.EncodeToString(k[:])
		u8 = ""
	}
	if len(u8) > 8 {
		u8 = u8[:8]
	}
	if u8 == "" {
		u8 = "-"
	}
	sum := sha256.Sum256([]byte(uid + "|" + conversation))
	return "wbm-" + u8 + "-" + hex.EncodeToString(sum[:16])
}
