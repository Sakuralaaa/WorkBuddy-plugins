package plugin

import (
	"net/http"
	"path/filepath"
	"testing"
)

func TestSessionOfBodyFallback(t *testing.T) {
	h := http.Header{}
	if got := sessionOf(h, nil, []byte(`{"model":"x","messages":[{"role":"user","content":"hi"}]}`)); got != "" {
		t.Errorf("no session anywhere: got %q", got)
	}
	if got := sessionOf(h, nil, []byte(`{"conversation_id":"conv-1","messages":[]}`)); got != "conv-1" {
		t.Errorf("conversation_id: got %q", got)
	}
	if got := sessionOf(h, nil, []byte(`{"metadata":{"session_id":"sess-2"}}`)); got != "sess-2" {
		t.Errorf("metadata.session_id: got %q", got)
	}
	// Headers win over the body.
	h.Set("X-Session-Id", "hdr-3")
	if got := sessionOf(h, nil, []byte(`{"conversation_id":"conv-1"}`)); got != "hdr-3" {
		t.Errorf("header wins: got %q", got)
	}
}

func TestStickyBindHop(t *testing.T) {
	// stickyBind writes the state file; point the loader at a temp
	// file so the test never touches the real state.
	state.Lock()
	state.loaded = true
	state.path = filepath.Join(t.TempDir(), "state")
	state.p = persisted{}
	state.p.init()
	state.ring.Runs = nil
	state.Unlock()
	t.Cleanup(func() {
		state.Lock()
		state.loaded = false
		state.path = ""
		state.Unlock()
	})
	if hopped, _ := stickyBind("sess-a", "auth-1"); hopped {
		t.Fatal("first bind is not a hop")
	}
	if hopped, hops := stickyBind("sess-a", "auth-1"); hopped || hops != 0 {
		t.Fatalf("same auth: hopped %v hops %d", hopped, hops)
	}
	if hopped, hops := stickyBind("sess-a", "auth-2"); !hopped || hops != 1 {
		t.Fatalf("different auth: hopped %v hops %d", hopped, hops)
	}
	// The binding still names the first account.
	if got := stickyBindings(); len(got) != 1 || got[0].AuthID != "auth-1" || got[0].Hops != 1 {
		t.Fatalf("bindings %+v", got)
	}
}
