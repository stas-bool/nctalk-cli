package main

// main_test.go — e2e-тесты точки входа cmd/nctalk-talk (Этап 4.9).
// Зеркало cmd/nctalk-call/main_test.go: 4 теста exit-codes (EmptyInput/ConfigError/
// AmbiguousRoom/NotFound). Сетевой слой (capability, signaling, interactive.Run)
// НЕ тестируется на e2e — для этого нужен integration_test.go (Task 4.10).

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const twoRoomsJSON = `{
  "ocs": {
    "meta": {"status": "ok", "statuscode": 200, "message": "OK"},
    "data": [
      {"type": 2, "token": "tok-team-a", "displayName": "Team Alpha", "unreadMessages": 0, "actorType": "users", "actorId": "alice"},
      {"type": 2, "token": "tok-team-b", "displayName": "Team Beta",  "unreadMessages": 0, "actorType": "users", "actorId": "bob"}
    ]
  }
}`

const emptyRoomsJSON = `{
  "ocs": {
    "meta": {"status": "ok", "statuscode": 200, "message": "OK"},
    "data": []
  }
}`

func setEnv(t *testing.T, url string) {
	t.Helper()
	t.Setenv("NEXTCLOUD_URL", url)
	t.Setenv("NEXTCLOUD_LOGIN", "user")
	t.Setenv("NEXTCLOUD_PASS", "pass")
	t.Setenv("NEXTCLOUD_TIMEOUT", "5s")
}

func TestRun_Usage_NoArgs(t *testing.T) {
	setEnv(t, "http://localhost:1")

	var out, errOut bytes.Buffer
	code := run(nil, &out, &errOut, nil)
	if code != 1 {
		t.Fatalf("exit: got %d, want 1 (EmptyInput; stderr=%q)", code, errOut.String())
	}
	if out.Len() != 0 {
		t.Errorf("stdout: got %q, want empty", out.String())
	}
	stderr := errOut.String()
	if !strings.HasPrefix(stderr, "nctalk-talk:") {
		t.Errorf("stderr должен начинаться с 'nctalk-talk:': %q", stderr)
	}
	if !strings.Contains(stderr, "укажите") {
		t.Errorf("stderr должен содержать usage-hint: %q", stderr)
	}
}

func TestRun_ConfigError(t *testing.T) {
	t.Setenv("NEXTCLOUD_URL", "")
	t.Setenv("NEXTCLOUD_LOGIN", "")
	t.Setenv("NEXTCLOUD_PASS", "")
	t.Setenv("NEXTCLOUD_TIMEOUT", "")

	var out, errOut bytes.Buffer
	code := run(nil, &out, &errOut, nil)
	if code != 1 {
		t.Fatalf("exit: got %d, want 1 (config error)", code)
	}
	stderr := errOut.String()
	if !strings.HasPrefix(stderr, "nctalk-talk:") {
		t.Errorf("stderr должен начинаться с 'nctalk-talk:': %q", stderr)
	}
	if !strings.Contains(stderr, "обязателен") {
		t.Errorf("stderr должен упоминать обязательную переменную: %q", stderr)
	}
}

func TestRun_AmbiguousRoom(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/apps/spreed/api/v4/room") {
			t.Errorf("ambig: неожиданный путь: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, twoRoomsJSON)
	}))
	defer srv.Close()
	setEnv(t, srv.URL)

	var out, errOut bytes.Buffer
	code := run([]string{"--name", "team"}, &out, &errOut, nil)
	if code != 3 {
		t.Fatalf("exit: got %d, want 3 (ambiguous; stderr=%q)", code, errOut.String())
	}
	stderr := errOut.String()
	if !strings.Contains(stderr, "найдено 2 комнат") {
		t.Errorf("stderr должен сообщать о 2 комнатах: %q", stderr)
	}
	if !strings.Contains(stderr, "tok-team-a") || !strings.Contains(stderr, "tok-team-b") {
		t.Errorf("stderr должен содержать оба token'а: %q", stderr)
	}
}

func TestRun_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, emptyRoomsJSON)
	}))
	defer srv.Close()
	setEnv(t, srv.URL)

	var out, errOut bytes.Buffer
	code := run([]string{"--name", "missing"}, &out, &errOut, nil)
	if code != 2 {
		t.Fatalf("exit: got %d, want 2 (not found; stderr=%q)", code, errOut.String())
	}
}
