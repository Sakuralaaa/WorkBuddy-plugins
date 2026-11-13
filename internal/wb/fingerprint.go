package wb

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Device token: the X-Device-Token header WorkBuddy's desktop client sends.
// panel (internal/upstream/headers.go + device_token.go) resolves it from
// three sources, in this order:
//
//  1. the account's own credential (device_token key in its auth file),
//  2. a global token in config,
//  3. a file the host drops the desktop client's token into.
//
// The plugin had none of the three: wb.Sign never set the header, so every
// chat and every jobs request went out without the device fingerprint the
// desktop client always carries. This file adds the resolution; the call
// sites (Sign for the account's own token, Channel.Fetch and the jobs
// client for the global/file ones) inject it.

const deviceTokenHeader = "X-Device-Token"

var errDeviceTokenTooLarge = errors.New("device token file too large")

// deviceTokenFileTTL is how long a file read is cached (panel: 5 minutes).
const deviceTokenFileTTL = 5 * time.Minute

// deviceTokenFileMaxLen is the largest file accepted as a token (panel:
// 1 KiB; a real token is a few hundred bytes).
const deviceTokenFileMaxLen = 1024

var dtFileCache = struct {
	sync.Mutex
	path   string
	token  string
	readAt time.Time
}{}

// readDeviceTokenFile reads and caches a device token file; "" means no
// usable token (file not configured, unreadable, too large, blank).
func readDeviceTokenFile(path string) string {
	if path == "" {
		return ""
	}
	dtFileCache.Lock()
	defer dtFileCache.Unlock()
	if dtFileCache.path == path && time.Since(dtFileCache.readAt) < deviceTokenFileTTL {
		return dtFileCache.token
	}
	dtFileCache.path = path
	tok := ""
	if info, err := os.Stat(path); err == nil && info.Size() <= deviceTokenFileMaxLen {
		if raw, err := os.ReadFile(path); err == nil {
			tok = strings.TrimSpace(string(raw))
		}
	}
	dtFileCache.token = tok
	dtFileCache.readAt = time.Now()
	return tok
}

// DeviceTokenOf resolves the device token for a request: the account's own
// first, then the configured global token, then the token file. "" when
// none is available — callers simply send no header then, as before.
func DeviceTokenOf(accountToken, globalToken, tokenFile string) string {
	if s := strings.TrimSpace(accountToken); s != "" {
		return s
	}
	if s := strings.TrimSpace(globalToken); s != "" {
		return s
	}
	return readDeviceTokenFile(tokenFile)
}

// InjectDeviceToken sets X-Device-Token on h when a token resolves and the
// header is not already set (a caller-set header wins).
func InjectDeviceToken(h http.Header, accountToken, globalToken, tokenFile string) {
	if h == nil || h.Get(deviceTokenHeader) != "" {
		return
	}
	if tok := DeviceTokenOf(accountToken, globalToken, tokenFile); tok != "" {
		h.Set(deviceTokenHeader, tok)
	}
}
