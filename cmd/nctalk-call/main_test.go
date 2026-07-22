package main

// main_test.go — e2e-тесты точки входа cmd/nctalk-call (Task 2.9 brief §«ТЕСТЫ»).
//
// Покрытие (4 теста, compile-only — запуск DEFERRED до ручного шага из-за
// macOS firewall, см. CLAUDE.md / brief):
//  1. TestRun_Usage_NoArgs — нет <room>/--name → EmptyInput → exit 1.
//  2. TestRun_ConfigError — env не задан → config.Load error → exit 1.
//  3. TestRun_AmbiguousRoom — httptest отдаёт 2 комнаты → exit 3 + candidates.
//  4. TestRun_NotFound — httptest отдаёт пустой список → exit 2.
//
// Сетевой слой (capability, signaling, agent.Run) НЕ тестируется на e2e — для
// этого нужен мок pion/ffmpeg, выходящий за рамки compile-only режима.
// Соответствующий сценарий вынесен в integration_test.go (build-tag integration).

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// twoRoomsJSON — OCS-конверт с двумя комнатами для теста StatusAmbiguous.
// Оба DisplayName содержат подстроку "team" — общий поисковый запрос --name.
const twoRoomsJSON = `{
  "ocs": {
    "meta": {"status": "ok", "statuscode": 200, "message": "OK"},
    "data": [
      {"type": 2, "token": "tok-team-a", "displayName": "Team Alpha", "unreadMessages": 0, "actorType": "users", "actorId": "alice"},
      {"type": 2, "token": "tok-team-b", "displayName": "Team Beta",  "unreadMessages": 0, "actorType": "users", "actorId": "bob"}
    ]
  }
}`

// emptyRoomsJSON — пустой список → FindRooms возвращает [] → StatusNotFound.
const emptyRoomsJSON = `{
  "ocs": {
    "meta": {"status": "ok", "statuscode": 200, "message": "OK"},
    "data": []
  }
}`

// setEnv выставляет валидные креды/URL/timeout через t.Setenv с авто-восстановлением.
func setEnv(t *testing.T, url string) {
	t.Helper()
	t.Setenv("NEXTCLOUD_URL", url)
	t.Setenv("NEXTCLOUD_LOGIN", "user")
	t.Setenv("NEXTCLOUD_PASS", "pass")
	t.Setenv("NEXTCLOUD_TIMEOUT", "5s")
}

// TestRun_Usage_NoArgs — позиционный и --name оба пусты → room.ResolveRoom
// возвращает StatusEmptyInput → run пишет usage в stderr и возвращает 1.
// config.Load должен пройти (env задан), иначе ветка ResolveRoom недостижима.
func TestRun_Usage_NoArgs(t *testing.T) {
	// URL непринципиален — до сетевого запроса дело не доходит. Но config.Load
	// требует валидный URL, иначе exit 1 на этапе конфигурации.
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
	if !strings.HasPrefix(stderr, "nctalk-call:") {
		t.Errorf("stderr должен начинаться с 'nctalk-call:': %q", stderr)
	}
	if !strings.Contains(stderr, "укажите") {
		t.Errorf("stderr должен содержать usage-hint: %q", stderr)
	}
}

// TestRun_ConfigError — NEXTCLOUD_URL пустой → config.Load возвращает ошибку →
// exit 1, stderr начинается с "nctalk-call:" и упоминает обязательную env-переменную.
func TestRun_ConfigError(t *testing.T) {
	// Явно опустошаем все env (t.Setenv восстанавливает после теста).
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
	if !strings.HasPrefix(stderr, "nctalk-call:") {
		t.Errorf("stderr должен начинаться с 'nctalk-call:': %q", stderr)
	}
	if !strings.Contains(stderr, "обязателен") {
		t.Errorf("stderr должен упоминать обязательную переменную: %q", stderr)
	}
}

// TestRun_AmbiguousRoom — httptest-сервер отдаёт 2 комнаты на FindRooms по
// --name "team" → StatusAmbiguous → exit 3 + candidates построчно в stderr.
//
// Полный цикл: config.Load → NewTalkClient → room.ResolveRoom → FindRooms →
// GET /ocs/v2.php/apps/spreed/api/v4/room → фильтр по подстроке "team" →
// 2 совпадения → StatusAmbiguous → печать двух строк и exit 3.
//
// Заметка: capability/signaling/agent НЕ вызываются — выход из run происходит
// раньше (после StatusAmbiguous return exit.ExitAmbiguous).
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
		t.Fatalf("exit: got %d, want 3 (Ambiguous; stderr=%q)", code, errOut.String())
	}
	stderr := errOut.String()
	// Оба candidates должны быть в stderr.
	for _, want := range []string{"tok-team-a", "Team Alpha", "tok-team-b", "Team Beta"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr должен содержать %q: %s", want, stderr)
		}
	}
}

// TestRun_NotFound — httptest отдаёт пустой список → FindRooms возвращает []
// → StatusNotFound → exit 2.
func TestRun_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, emptyRoomsJSON)
	}))
	defer srv.Close()

	setEnv(t, srv.URL)
	var out, errOut bytes.Buffer
	code := run([]string{"--name", "несуществующее_zzz"}, &out, &errOut, nil)
	if code != 2 {
		t.Fatalf("exit: got %d, want 2 (NotFound; stderr=%q)", code, errOut.String())
	}
	stderr := errOut.String()
	if !strings.HasPrefix(stderr, "nctalk-call:") {
		t.Errorf("stderr должен начинаться с 'nctalk-call:': %q", stderr)
	}
}
