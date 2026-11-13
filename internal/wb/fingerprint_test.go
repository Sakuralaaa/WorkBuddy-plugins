package wb

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestDeviceTokenOfPrecedence(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "device_token")
	if err := os.WriteFile(file, []byte("  file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := DeviceTokenOf("acct-token", "global-token", file); got != "acct-token" {
		t.Errorf("account wins: got %q", got)
	}
	if got := DeviceTokenOf("", "global-token", file); got != "global-token" {
		t.Errorf("global wins over file: got %q", got)
	}
	if got := DeviceTokenOf("", "", file); got != "file-token" {
		t.Errorf("file fallback: got %q", got)
	}
	if got := DeviceTokenOf("", "", ""); got != "" {
		t.Errorf("none: got %q", got)
	}
}

func TestDeviceTokenFileTooLarge(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "device_token")
	big := make([]byte, deviceTokenFileMaxLen+1)
	for i := range big {
		big[i] = 'x'
	}
	if err := os.WriteFile(file, big, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readDeviceTokenFile(file); got != "" {
		t.Errorf("oversize file must be ignored, got %q", got)
	}
}

func TestSignSetsDeviceToken(t *testing.T) {
	h := http.Header{}
	Sign(SiteCN(), &Account{UID: "u1", Access: "a", DeviceToken: "dt-1"}, h)
	if got := h.Get(deviceTokenHeader); got != "dt-1" {
		t.Errorf("Sign: got %q", got)
	}
	h2 := http.Header{}
	Sign(SiteCN(), &Account{UID: "u1", Access: "a"}, h2)
	if got := h2.Get(deviceTokenHeader); got != "" {
		t.Errorf("no token, no header: got %q", got)
	}
}

func TestInjectDeviceTokenKeepsExisting(t *testing.T) {
	h := http.Header{}
	h.Set(deviceTokenHeader, "keep-me")
	InjectDeviceToken(h, "acct", "global", "")
	if got := h.Get(deviceTokenHeader); got != "keep-me" {
		t.Errorf("caller header wins: got %q", got)
	}
}
