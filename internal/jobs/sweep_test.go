package jobs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// TestRunTaskSweepClaimsDoneTask: a task already at 5/5 but never claimed
// is accepted and claimed by the sweep alone, with no action run.
func TestRunTaskSweepClaimsDoneTask(t *testing.T) {
	f := &fake{current: 5}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	c := testClient(srv.URL)
	r := c.RunTaskSweep(context.Background(), true)
	if !r.OK || r.Outcome != "claimed" || r.Credits != 100 {
		t.Fatalf("sweep %+v", r)
	}
	if !f.claimed {
		t.Fatalf("sweep did not claim")
	}
	// A second sweep finds nothing to claim.
	r = c.RunTaskSweep(context.Background(), true)
	if r.Outcome != "done" {
		t.Fatalf("second sweep %+v", r)
	}
}

// lyingMP is a mini-program list whose accept answers "accepted" the first
// time but does not register the task: the re-read still says
// not_accepted. Only the second accept registers it.
type lyingMP struct {
	mu         sync.Mutex
	accepts    int
	registered bool
}

func (f *lyingMP) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		ok := func(data any) {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "OK", "data": data})
		}
		switch r.URL.Path {
		case "/v2/activity/growth/tasks":
			st := "not_accepted"
			if f.registered {
				st = "accepted"
			}
			ok(map[string]any{"tasks": []any{map[string]any{"task_code": "Sequential_Tasks_1", "title": "连环任务", "accept_status": st, "progress": map[string]any{"current": 0, "target": 1}}}})
		case "/v2/activity/growth/tasks/accept":
			f.accepts++
			if f.accepts >= 2 {
				f.registered = true
			}
			ok(map[string]any{"results": []any{map[string]any{"task_code": "Sequential_Tasks_1", "status": "accepted"}}})
		default:
			w.WriteHeader(404)
		}
	})
}

// TestAcceptMPVerifiesRegistration: an MP accept that the server answers
// but does not register is retried once after the re-read, and only then
// counts as accepted.
func TestAcceptMPVerifiesRegistration(t *testing.T) {
	if !MPTask("Sequential_Tasks_1") {
		t.Fatal("Sequential_Tasks_1 should be an MP task")
	}
	f := &lyingMP{}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	c := testClient(srv.URL)
	accepted, failed, why := c.Accept(context.Background(), []string{"Sequential_Tasks_1"}, true)
	if len(accepted) != 1 || len(failed) != 0 {
		t.Fatalf("accepted %v failed %v why %s", accepted, failed, why)
	}
	if f.accepts != 2 {
		t.Fatalf("accept called %d times, want 2 (verify + retry)", f.accepts)
	}
}

// TestAcceptMPUnregisteredStaysFailed: when the re-read still says
// not_accepted after the retry, the code is failed, not accepted.
func TestAcceptMPUnregisteredStaysFailed(t *testing.T) {
	f := &lyingMP{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		ok := func(data any) {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "OK", "data": data})
		}
		switch r.URL.Path {
		case "/v2/activity/growth/tasks":
			ok(map[string]any{"tasks": []any{map[string]any{"task_code": "Sequential_Tasks_1", "accept_status": "not_accepted", "progress": map[string]any{"current": 0, "target": 1}}}})
		case "/v2/activity/growth/tasks/accept":
			// Always answers accepted, never registers.
			ok(map[string]any{"results": []any{map[string]any{"task_code": "Sequential_Tasks_1", "status": "accepted"}}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := testClient(srv.URL)
	accepted, failed, why := c.Accept(context.Background(), []string{"Sequential_Tasks_1"}, true)
	if len(accepted) != 0 || len(failed) != 1 {
		t.Fatalf("accepted %v failed %v why %s", accepted, failed, why)
	}
	if !strings.Contains(why, "未登记") {
		t.Fatalf("why %q should say the task was not registered", why)
	}
}
