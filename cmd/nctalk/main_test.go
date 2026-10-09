package main

// main_test.go — e2e-тесты связки config → client → cli через httptest.Server
// (план Task 5.1, спека §5/§9). Тестируется именно интеграция слоёв: реальный
// config.Load (читает env, выставленный через t.Setenv), реальный
// client.NewTalkClient (ходит на httptest-сервер) и реальный cli.Run (роутинг
// + handler-ы + render).
//
// Контракт безопасности: отдельный тест проверяет, что пароль не утекает в
// stdout/stderr при cross-host redirect (клиент его блокирует, ошибка
// пробрасывается через sanitizeErr — без userinfo/query и без Authorization).

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// roomsListJSON — OCS-конверт с двумя комнатами для e2e-теста rooms list.
// Подобрано так, чтобы actorId/displayName были детерминированно проверяемы
// в stdout (рендер RoomsTable: ТИП ЧАТ TOKEN НЕПРОЧИТАНО АКТЁР ПОСЛЕДНЕЕ).
const roomsListJSON = `{
  "ocs": {
    "meta": {"status": "ok", "statuscode": 200, "message": "OK"},
    "data": [
      {"type": 1, "token": "tok-bob", "displayName": "Bob Bobson", "unreadMessages": 3, "actorType": "users", "actorId": "bob"},
      {"type": 2, "token": "tok-team", "displayName": "Team Chat", "unreadMessages": 0, "actorType": "users", "actorId": "alice"}
    ]
  }
}`

// chatShowJSON — OCS-конверт с двумя сообщениями для e2e-теста chat show.
// MessageType=comment (чтобы пройти дефолтный фильтр «скрывать system»).
// Меньше chatPageSize (200), поэтому клиент не уходит в пагинацию.
const chatShowJSON = `{
  "ocs": {
    "meta": {"status": "ok", "statuscode": 200, "message": "OK"},
    "data": [
      {"id": 100, "actorType": "users", "actorId": "bob",   "actorDisplayName": "Bob",   "messageType": "comment", "message": "hello world", "timestamp": 1718000000, "token": "tok-bob"},
      {"id": 99,  "actorType": "users", "actorId": "alice", "actorDisplayName": "Alice", "messageType": "comment", "message": "hi there",   "timestamp": 1718000100, "token": "tok-bob"}
    ]
  }
}`

// setEnv выставляет валидные креды/URL/timeout через t.Setenv (с авто-восстанов-
// лом после теста). URL указывает на httptest-сервер.
func setEnv(t *testing.T, url string) {
	t.Helper()
	t.Setenv("NEXTCLOUD_URL", url)
	t.Setenv("NEXTCLOUD_LOGIN", "user")
	t.Setenv("NEXTCLOUD_PASS", "pass")
	t.Setenv("NEXTCLOUD_TIMEOUT", "5s")
}

// TestRun_RoomsList_E2E — полный цикл config → client → cli для `rooms list`.
// httptest-сервер отдаёт фикс-ответ на /ocs/v2.php/apps/spreed/api/v4/room;
// проверяем что stdout содержит заголовок таблицы, имена комнат и actorId
// (план Task 5.1 Step 1, спека §6).
func TestRun_RoomsList_E2E(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/apps/spreed/api/v4/room") {
			t.Errorf("rooms list: неожиданный путь запроса: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, roomsListJSON)
	}))
	defer srv.Close()

	setEnv(t, srv.URL)
	var out, errOut bytes.Buffer
	code := run([]string{"rooms", "list"}, &out, &errOut, nil)
	if code != 0 {
		t.Fatalf("rooms list: exit=%d, stderr=%s", code, errOut.String())
	}

	stdout := out.String()
	for _, want := range []string{"Bob Bobson", "Team Chat", "bob", "alice", "НЕПРОЧИТАНО"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("rooms list: stdout не содержит %q; вывод=\n%s", want, stdout)
		}
	}
}

// TestRun_ChatShow_E2E — полный цикл config → client → cli для `chat show <token>`.
// Token передаётся позиционно, поэтому ResolveRoom НЕ делает дополнительных
// сетевых запросов — единственный вызов на /api/v1/chat/<token>. Проверяем
// что stdout содержит id, авторов и текст сообщений (формат MessagesTable).
func TestRun_ChatShow_E2E(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/apps/spreed/api/v1/chat/tok-bob") {
			t.Errorf("chat show: неожиданный путь запроса: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, chatShowJSON)
	}))
	defer srv.Close()

	setEnv(t, srv.URL)
	var out, errOut bytes.Buffer
	code := run([]string{"chat", "show", "tok-bob"}, &out, &errOut, nil)
	if code != 0 {
		t.Fatalf("chat show: exit=%d, stderr=%s", code, errOut.String())
	}

	stdout := out.String()
	for _, want := range []string{"Bob", "Alice", "hello world", "hi there", "[100]", "[99]"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("chat show: stdout не содержит %q; вывод=\n%s", want, stdout)
		}
	}
}

// TestRun_NoEnv_Returns1 — спека §5 (после правки help): пустые NEXTCLOUD_*
// при обычной команде (НЕ help-запрос) → config.Load возвращает ошибку, run
// пишет её в stderr с префиксом "nctalk:" и возвращает 1. stdout остаётся пустым.
//
// ВАЖНО: раньше этот тест подавал nil args и проверял config-ошибку; но после
// перехвата help в run() пустые args → общий help в stderr + exit 1 (не config).
// Поэтому теперь тест подаёт реальную команду, которая минует HandleHelp и
// доходит до config.Load.
func TestRun_NoEnv_Returns1(t *testing.T) {
	// Явно опустошаем все четыре env-переменные (t.Setenv восстанавливает
	// значения после теста).
	t.Setenv("NEXTCLOUD_URL", "")
	t.Setenv("NEXTCLOUD_LOGIN", "")
	t.Setenv("NEXTCLOUD_PASS", "")
	t.Setenv("NEXTCLOUD_TIMEOUT", "")

	var out, errOut bytes.Buffer
	// Реальная команда, минует HandleHelp (там нет --help/help) → доходит до config.Load.
	code := run([]string{"rooms", "list"}, &out, &errOut, nil)
	if code != 1 {
		t.Fatalf("ожидался exit 1, получен %d (stderr=%q)", code, errOut.String())
	}
	if out.Len() != 0 {
		t.Errorf("stdout должен быть пуст, получено: %q", out.String())
	}
	stderr := errOut.String()
	if !strings.HasPrefix(stderr, "nctalk:") {
		t.Errorf("stderr должен начинаться с 'nctalk:', получено: %q", stderr)
	}
	if !strings.Contains(stderr, "обязателен") {
		t.Errorf("stderr должен упоминать обязательную переменную: %q", stderr)
	}
}

// TestRun_PasswordDoesNotLeak_CrossHostRedirect — критичный для безопасности
// тест на утечку пароля (план Task 5.1 Step 6, спека §5/§9).
//
// Сценарий: httptest-сервер присылает cross-host 302 на evil.example.com.
// client.sameHostRedirectPolicy блокирует редирект (Authorization не уходит
// на чужой хост); http.Client оборачивает ошибку в *url.Error; sanitizeErr
// отрезает userinfo/query из URL. cli.Run печатает текст ошибки в stderr.
//
// Утверждение: SECRET_MARKER (значение NEXTCLOUD_PASS) не встречается ни в
// stdout, ни в stderr. Если это когда-нибудь сломается — это регрессия
// контракта безопасности, релиз нельзя выпускать.
func TestRun_PasswordDoesNotLeak_CrossHostRedirect(t *testing.T) {
	const secret = "SECRET_MARKER"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Любой запрос → редирект на чужой хост; клиент обязан его заблокировать.
		http.Redirect(w, r, "https://evil.example.com/leak", http.StatusFound)
	}))
	defer srv.Close()

	t.Setenv("NEXTCLOUD_URL", srv.URL)
	t.Setenv("NEXTCLOUD_LOGIN", "user")
	t.Setenv("NEXTCLOUD_PASS", secret)
	t.Setenv("NEXTCLOUD_TIMEOUT", "5s")

	var out, errOut bytes.Buffer
	code := run([]string{"rooms", "list"}, &out, &errOut, nil)
	if code == 0 {
		t.Fatalf("ожидался ненулевой exit при cross-host redirect, получен 0 (stdout=%q)", out.String())
	}
	if strings.Contains(out.String(), secret) {
		t.Errorf("УТЕЧКА: пароль найден в stdout: %q", out.String())
	}
	if strings.Contains(errOut.String(), secret) {
		t.Errorf("УТЕЧКА: пароль найден в stderr: %q", errOut.String())
	}
}

// chatShowOCS404JSON — OCS-конверт с meta.statusCode=404 (room not found):
// именно так Nextcloud Talk отвечает на запрос чата по несуществующему token.
// HTTP-статус при этом 200 (OCS вкладывает бизнес-код в meta), поэтому doOCS
// обязан разбирать конверт и возвращать *client.OCSError{Code:404} — а
// cli.exitFromClientErr маппит его в exit 2 (контракт §7/§9).
const chatShowOCS404JSON = `{
  "ocs": {
    "meta": {"status": "failure", "statuscode": 404, "message": "Room not found"},
    "data": []
  }
}`

// reactionsOCS404JSON — то же для reactions get: несуществующий messageId
// (или token) → meta.statusCode=404.
const reactionsOCS404JSON = `{
  "ocs": {
    "meta": {"status": "failure", "statuscode": 404, "message": "Message not found"},
    "data": []
  }
}`

// TestRun_ChatShow_OCS404_Exit2 — e2e проверка контракта exit-кодов (спека
// §7/§9): `chat show <несуществующий_token>` → серверный OCS 404 → процесс
// возвращает код 2 (NotFound), НЕ 1. Раньше любой OCS-error сваливался в
// exit 1; маппинг теперь различает 404 → 2 и прочие → 1.
//
// Полный цикл: httptest-сервер отдаёт OCS 404 на /api/v1/chat/<token>;
// client.doOCS распознаёт meta.statusCode>=400 и возвращает *client.OCSError
// с Code=404; cli.exitFromClientErr маппит в ExitNotFound; run() возвращает 2.
func TestRun_ChatShow_OCS404_Exit2(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, chatShowOCS404JSON)
	}))
	defer srv.Close()

	setEnv(t, srv.URL)
	var out, errOut bytes.Buffer
	code := run([]string{"chat", "show", "НЕСУЩЕСТВУЮЩИЙ_zzz", "--last", "3"}, &out, &errOut, nil)
	if code != 2 {
		t.Fatalf("exit: got %d, want 2 (OCS 404 → NotFound; stderr=%q)", code, errOut.String())
	}
	// stdout пуст (ошибка — не результат); stderr содержит сообщение сервера.
	if out.String() != "" {
		t.Errorf("stdout: got %q, want empty при ошибке", out.String())
	}
	if !strings.Contains(errOut.String(), "Room not found") {
		t.Errorf("stderr должен содержать сообщение OCS: %q", errOut.String())
	}
}

// TestRun_ReactionsGet_OCS404_Exit2 — аналогично для `reactions get <room>
// <несуществующий_messageId>`: сервер OCS 404 → exit 2.
func TestRun_ReactionsGet_OCS404_Exit2(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, reactionsOCS404JSON)
	}))
	defer srv.Close()

	setEnv(t, srv.URL)
	var out, errOut bytes.Buffer
	code := run([]string{"reactions", "get", "85z9h55k", "999999999"}, &out, &errOut, nil)
	if code != 2 {
		t.Fatalf("exit: got %d, want 2 (OCS 404 → NotFound; stderr=%q)", code, errOut.String())
	}
	if out.String() != "" {
		t.Errorf("stdout: got %q, want empty при ошибке", out.String())
	}
}

// TestRun_ChatShow_OCS401_Exit1 — регресс: OCS statusCode 401 (неверные креды)
// маппится в exit 1 (Generic), а НЕ 2. Защита от гипотетической ошибки «все
// OCS >= 400 → 2».
func TestRun_ChatShow_OCS401_Exit1(t *testing.T) {
	const body401 = `{"ocs":{"meta":{"status":"failure","statuscode":401,"message":"Bad credentials"},"data":[]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body401)
	}))
	defer srv.Close()

	setEnv(t, srv.URL)
	var out, errOut bytes.Buffer
	code := run([]string{"chat", "show", "TOK"}, &out, &errOut, nil)
	if code != 1 {
		t.Fatalf("exit: got %d, want 1 (OCS 401 → Generic)", code)
	}
}

// TestRun_RoomsFind_Empty_Exit0 — регресс: пустой результат поиска
// (`rooms find <нет_совпадений>`) должен оставаться exit 0 (поисковая
// семантика, спека §7), а не превращаться в 2 из-за нового маппинга. Сервер
// здесь отвечает 200 с пустым data — клиент возвращает пустой []Room без
// ошибки, и handler выходит по ветке ExitOK.
func TestRun_RoomsFind_Empty_Exit0(t *testing.T) {
	const emptyRooms = `{"ocs":{"meta":{"status":"ok","statuscode":200,"message":"OK"},"data":[]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, emptyRooms)
	}))
	defer srv.Close()

	setEnv(t, srv.URL)
	var out, errOut bytes.Buffer
	code := run([]string{"rooms", "find", "несуществующее_zzz"}, &out, &errOut, nil)
	if code != 0 {
		t.Fatalf("exit: got %d, want 0 (пустой поиск = успех, не not-found; stderr=%q)", code, errOut.String())
	}
	if out.String() != "" {
		t.Errorf("stdout: got %q, want empty", out.String())
	}
}

// TestRun_HelpWithoutEnv_WorksWithoutCreds — спека §5: help работает БЕЗ
// NEXTCLOUD_*. HandleHelp вызывается строго до config.Load, поэтому пустые env
// не мешают; клиент не создаётся; код 0 (или 1 для no-args); в stderr/stdout — help.
func TestRun_HelpWithoutEnv_WorksWithoutCreds(t *testing.T) {
	// Явно опустошаем env (как TestRun_NoEnv_Returns1).
	t.Setenv("NEXTCLOUD_URL", "")
	t.Setenv("NEXTCLOUD_LOGIN", "")
	t.Setenv("NEXTCLOUD_PASS", "")
	t.Setenv("NEXTCLOUD_TIMEOUT", "")

	cases := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string // "stdout" | "stderr"
	}{
		{"help flag", []string{"--help"}, 0, "stdout"},
		{"h flag", []string{"-h"}, 0, "stdout"},
		{"help word", []string{"help"}, 0, "stdout"},
		{"help rooms list", []string{"help", "rooms", "list"}, 0, "stdout"},
		{"rooms list --help", []string{"rooms", "list", "--help"}, 0, "stdout"},
		{"search --help", []string{"search", "--help"}, 0, "stdout"},
		{"no args", nil, 1, "stderr"},
		{"help unknown", []string{"help", "nosuch"}, 1, "stderr"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := run(tc.args, &out, &errOut, nil)
			if code != tc.wantCode {
				t.Fatalf("exit: got %d, want %d (stdout=%q stderr=%q)", code, tc.wantCode, out.String(), errOut.String())
			}
			switch tc.wantOut {
			case "stdout":
				if out.String() == "" {
					t.Errorf("want non-empty stdout, got empty (stderr=%q)", errOut.String())
				}
			case "stderr":
				if errOut.String() == "" {
					t.Errorf("want non-empty stderr, got empty (stdout=%q)", out.String())
				}
			}
		})
	}
}

// attachE2EServer — httptest-сервер шагов SendFile для e2e `chat send
// --attach` (спека-дельта 2026-10-09 §7): cloud/user, capabilities, HEAD 404,
// PUT 201, шаринг, страница chat — живые фикстуры Task 1 из testdata/.
// leakTo непусто → PUT отвечает 302 на этот URL (проверка утечки пароля).
// Возвращает сервер и счётчик запросов шаринга.
func attachE2EServer(t *testing.T, leakTo string) (*httptest.Server, *int32) {
	t.Helper()
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join("..", "..", "testdata", name))
		if err != nil {
			t.Fatalf("read fixture %s: %v", name, err)
		}
		return b
	}
	user := read("cloud_user.json")
	caps := read("capabilities_attachments.json")
	share := read("share_create.json")
	chat := read("chat_file_messages.json")
	var shares int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		writeJSON := func(b []byte) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(b)
		}
		switch {
		case r.Method == http.MethodHead && strings.HasPrefix(p, "/remote.php/dav/files/"):
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPut && strings.HasPrefix(p, "/remote.php/dav/files/"):
			_, _ = io.Copy(io.Discard, r.Body)
			if leakTo != "" {
				http.Redirect(w, r, leakTo, http.StatusFound)
				return
			}
			w.WriteHeader(http.StatusCreated)
		case p == "/ocs/v2.php/cloud/user":
			writeJSON(user)
		case p == "/ocs/v2.php/cloud/capabilities":
			writeJSON(caps)
		case r.Method == http.MethodPost && p == "/ocs/v2.php/apps/files_sharing/api/v1/shares":
			atomic.AddInt32(&shares, 1)
			writeJSON(share)
		case r.Method == http.MethodGet && p == "/ocs/v2.php/apps/spreed/api/v1/chat/tok-team":
			writeJSON(chat)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &shares
}

// TestRun_ChatSendAttach_E2E — полный цикл config → client → cli для
// `chat send <token> --attach <файл> --json`: загрузка, шаринг и поиск id по
// referenceId (фикстура: сообщение 1500 с referenceId 0…01).
func TestRun_ChatSendAttach_E2E(t *testing.T) {
	srv, shares := attachE2EServer(t, "")
	setEnv(t, srv.URL)
	file := filepath.Join(t.TempDir(), "report.pdf")
	if err := os.WriteFile(file, []byte("e2e-содержимое"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	ref := strings.Repeat("0", 63) + "1"

	var out, errOut bytes.Buffer
	code := run([]string{"chat", "send", "tok-team", "--attach", file, "--reference-id", ref, "--json"},
		&out, &errOut, strings.NewReader("подпись e2e"))
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%s", code, errOut.String())
	}
	var got struct {
		Id int `json:"id"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.Id != 1500 {
		t.Errorf("stdout %q: id=%d err=%v, want 1500", out.String(), got.Id, err)
	}
	if n := atomic.LoadInt32(shares); n != 1 {
		t.Errorf("запросов шаринга: %d, want 1", n)
	}
}

// TestRun_PasswordDoesNotLeak_AttachPutRedirect — регресс безопасности
// (спека-дельта 2026-10-09 §4.1) по образцу
// TestRun_PasswordDoesNotLeak_CrossHostRedirect: PUT загрузки получает 302 на
// чужой хост → upload-клиент блокирует редирект, exit 1, SECRET_MARKER нет ни
// в stdout, ни в stderr, шаринга нет.
func TestRun_PasswordDoesNotLeak_AttachPutRedirect(t *testing.T) {
	const secret = "SECRET_MARKER"
	srv, shares := attachE2EServer(t, "https://evil.example.com/leak")
	t.Setenv("NEXTCLOUD_URL", srv.URL)
	t.Setenv("NEXTCLOUD_LOGIN", "user")
	t.Setenv("NEXTCLOUD_PASS", secret)
	t.Setenv("NEXTCLOUD_TIMEOUT", "5s")
	file := filepath.Join(t.TempDir(), "report.pdf")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	var out, errOut bytes.Buffer
	code := run([]string{"chat", "send", "tok-team", "--attach", file}, &out, &errOut, strings.NewReader(""))
	if code != 1 {
		t.Fatalf("exit=%d, want 1 (stderr=%q)", code, errOut.String())
	}
	if strings.Contains(out.String(), secret) || strings.Contains(errOut.String(), secret) {
		t.Errorf("УТЕЧКА: пароль в выводе: stdout=%q stderr=%q", out.String(), errOut.String())
	}
	if !strings.Contains(errOut.String(), "cross-host redirect blocked") {
		t.Errorf("stderr %q, want «cross-host redirect blocked»", errOut.String())
	}
	if n := atomic.LoadInt32(shares); n != 0 {
		t.Errorf("шаринг после заблокированного PUT: %d, want 0", n)
	}
}
