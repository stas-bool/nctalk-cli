// Пакет capability_test — внешние тесты STUN/TURN-клиента (package capability_test,
// а не capability): тестируем только public API (Client.Settings), без доступа
// к внутренним типам. Подход — fixture + httptest.Server (как в
// cmd/nctalk/main_test.go): сервер управляет ответами, клиент идёт реальным
// *http.Client с SameHostRedirectPolicy — это end-to-end проверка всего
// transport.DoOCS-пути (URL-склейка, Basic-auth, OCS-заголовки, разворот конверта).
package capability_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pion/webrtc/v4"
	"github.com/stas/nctalk/internal/call/capability"
	"github.com/stas/nctalk/internal/transport"
)

// ---- helpers ----

// fixturePath возвращает путь к фикстуре в testdata/signaling/. cwd теста
// (internal/call/capability) — на 3 уровня ниже репо-рута, отсюда относительный
// путь '../../../testdata/signaling/<name>'. Работает в CI и локально.
func fixturePath(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join("..", "..", "..", "testdata", "signaling", name)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("fixture %s недоступен: %v", name, err)
	}
	return p
}

// loadFixture загружает JSON-фикстуру целиком как строку (для передачи в mock-
// server как тело ответа).
func loadFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(fixturePath(t, name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(b)
}

// mustParseURL — тестовый хелпер для Auth.BaseURL. Падает (panic) при невалидном
// URL — в тестах это всегда программистская ошибка, не рантайм.
func mustParseURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

// newClient собирает capability.Client на указанный baseURL. doer — реальный
// *http.Client с SameHostRedirectPolicy (production-конфигурация транспорта).
// password передаётся отдельно, чтобы redact-тест мог подсунуть SECRET_MARKER.
func newClient(baseURL, password string) *capability.Client {
	return capability.New(
		capability.Auth{BaseURL: mustParseURL(baseURL), Login: "alice", Password: password},
		&http.Client{CheckRedirect: transport.SameHostRedirectPolicy},
	)
}

// ocsOK строит OCS-конверт с meta.statusCode=200 и указанным data (строкой JSON
// или значением). Сервер Nextcloud всегда отвечает таким конвертом — даже если
// data пустой (тогда `data: null`).
func ocsOK(data string) string {
	return `{"ocs":{"meta":{"status":"ok","statuscode":200,"message":"OK"},"data":` + data + `}}`
}

// ocsErrEnvelope — OCS-конверт с meta.statusCode=code (для симуляции OCS-ошибок:
// сервер возвращает HTTP 200, но бизнес-ошибку в meta). Используется в TestSettings_
// OCSError401: именно так Nextcloud сигнализирует об ошибке auth в OCS-API.
func ocsErrEnvelope(code int, msg string) string {
	type meta struct {
		Status     string `json:"status"`
		StatusCode int    `json:"statuscode"`
		Message    string `json:"message"`
	}
	type env struct {
		OCS struct {
			Meta meta `json:"meta"`
			Data any  `json:"data"`
		} `json:"ocs"`
	}
	e := env{}
	e.OCS.Meta.Status = "failure"
	e.OCS.Meta.StatusCode = code
	e.OCS.Meta.Message = msg
	b, _ := json.Marshal(e)
	return string(b)
}

// ---- Test cases ----

// TestSettings_FullResponse_Fixture — основная фикстура capability.json
// (Task 2.1): 1 STUN + 1 TURN. TURN несёт Username/Credential, CredentialType
// равно ICECredentialTypePassword. Также проверяем, что путь запроса —
// v3 signaling-settings (НЕ v4, НЕ cloud/capabilities).
func TestSettings_FullResponse_Fixture(t *testing.T) {
	body := loadFixture(t, "capability.json")
	var capturedPath, capturedMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedMethod = r.Method
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	c := newClient(srv.URL, "secret")
	servers, err := c.Settings(context.Background())
	if err != nil {
		t.Fatalf("Settings: got err=%v, want nil", err)
	}

	// Путь — v3 signaling-settings БЕЗ token (Spreed route Signaling#getSettings
	// без {token}; curl с token → 404, без token → 200 — баг #2). Token в path
	// НЕ подставляется — настройки signaling глобальны, не per-room.
	if want := "/ocs/v2.php/apps/spreed/api/v3/signaling/settings"; capturedPath != want {
		t.Errorf("URL.Path: got %q, want %q", capturedPath, want)
	}
	if capturedMethod != http.MethodGet {
		t.Errorf("Method: got %q, want GET", capturedMethod)
	}

	if got, want := len(servers), 2; got != want {
		t.Fatalf("len(servers) = %d, want %d (1 stun + 1 turn)", got, want)
	}

	// [0] STUN: только URLs, без кредов.
	stun := servers[0]
	if got, want := len(stun.URLs), 1; got != want {
		t.Errorf("stun.URLs len: got %d, want %d", got, want)
	}
	if got, want := stun.URLs[0], "stun:stun.example.org:3478"; got != want {
		t.Errorf("stun.URLs[0]: got %q, want %q", got, want)
	}
	if stun.Username != "" {
		t.Errorf("stun.Username: got %q, want empty (STUN не требует кредов)", stun.Username)
	}
	if stun.Credential != nil {
		t.Errorf("stun.Credential: got %v, want nil", stun.Credential)
	}

	// [1] TURN: URLs + Username + Credential + CredentialType=Password.
	turn := servers[1]
	if got, want := len(turn.URLs), 3; got != want {
		t.Fatalf("turn.URLs len: got %d, want %d (udp/tcp/tls)", got, want)
	}
	if got, want := turn.URLs[0], "turn:turn.example.org:3478?transport=udp"; got != want {
		t.Errorf("turn.URLs[0]: got %q, want %q", got, want)
	}
	if got, want := turn.URLs[1], "turn:turn.example.org:3478?transport=tcp"; got != want {
		t.Errorf("turn.URLs[1]: got %q, want %q", got, want)
	}
	if got, want := turn.URLs[2], "turns:turn.example.org:443?transport=tcp"; got != want {
		t.Errorf("turn.URLs[2]: got %q, want %q", got, want)
	}
	if got, want := turn.Username, "1714060800:alice"; got != want {
		t.Errorf("turn.Username: got %q, want %q", got, want)
	}
	if turn.CredentialType != webrtc.ICECredentialTypePassword {
		t.Errorf("turn.CredentialType: got %v, want ICECredentialTypePassword", turn.CredentialType)
	}
	// Credential — interface{}, в нашем маппинге это string. Spreed getTurnSettings
	// возвращает 'password'-style credential (не OAuth).
	credStr, ok := turn.Credential.(string)
	if !ok {
		t.Fatalf("turn.Credential: got %T, want string", turn.Credential)
	}
	if got, want := credStr, "REDACTED-TURN-AUTH"; got != want {
		t.Errorf("turn.Credential: got %q, want %q", got, want)
	}
}

// TestSettings_OnlySTUN_EmptyTurn — turnservers=[] (явный пустой массив).
// Спека: клиент работает с одним STUN, если TURN не сконфигурирован.
func TestSettings_OnlySTUN_EmptyTurn(t *testing.T) {
	body := ocsOK(`{
		"signalingMode": "internal",
		"userId": "alice",
		"server": "",
		"stunservers": [{"urls":["stun:stun.example.org:3478"]}],
		"turnservers": []
	}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	c := newClient(srv.URL, "secret")
	servers, err := c.Settings(context.Background())
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	if got, want := len(servers), 1; got != want {
		t.Fatalf("len(servers) = %d, want %d", got, want)
	}
	if got, want := servers[0].URLs[0], "stun:stun.example.org:3478"; got != want {
		t.Errorf("servers[0].URLs[0]: got %q, want %q", got, want)
	}
	if servers[0].Username != "" {
		t.Errorf("servers[0].Username: got %q, want empty", servers[0].Username)
	}
}

// TestSettings_OnlySTUN_FieldMissing — turnservers отсутствует вовсе
// (json.Unmarshal оставляет slice nil — это валидный случай).
func TestSettings_OnlySTUN_FieldMissing(t *testing.T) {
	body := ocsOK(`{
		"signalingMode": "internal",
		"userId": "alice",
		"server": "",
		"stunservers": [{"urls":["stun:stun.example.org:3478"]}]
	}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	c := newClient(srv.URL, "secret")
	servers, err := c.Settings(context.Background())
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	if got, want := len(servers), 1; got != want {
		t.Fatalf("len(servers) = %d, want %d", got, want)
	}
}

// TestSettings_Empty_BothEmpty — обе секции пусты (явные пустые массивы).
// Спека: (nil, nil) без ошибки. pion работает с пустым списком ICEServers
// (хост в той же сети — STUN не нужен).
func TestSettings_Empty_BothEmpty(t *testing.T) {
	body := ocsOK(`{
		"signalingMode": "internal",
		"userId": "alice",
		"server": "",
		"stunservers": [],
		"turnservers": []
	}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	c := newClient(srv.URL, "secret")
	servers, err := c.Settings(context.Background())
	if err != nil {
		t.Fatalf("Settings: got err=%v, want nil (пустой STUN/TURN — НЕ ошибка)", err)
	}
	if servers != nil {
		t.Errorf("servers: got %v, want nil (пустой список → nil для удобства потребителя)", servers)
	}
}

// TestSettings_Empty_FieldsMissing — обе секции отсутствуют (signalingMode !=
// internal / external HPB). Должно тоже дать (nil, nil).
func TestSettings_Empty_FieldsMissing(t *testing.T) {
	body := ocsOK(`{
		"signalingMode": "external",
		"userId": "alice",
		"server": "https://hpb.example.org"
	}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	c := newClient(srv.URL, "secret")
	servers, err := c.Settings(context.Background())
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	if servers != nil {
		t.Errorf("servers: got %v, want nil при отсутствии STUN/TURN-полей", servers)
	}
}

// TestSettings_OCSError401 — HTTP 200, но OCS meta.statusCode=401 (так Nextcloud
// сигнализирует об ошибке auth). DoOCS разворачивает конверт и возвращает
// *transport.OCSError{Code:401}.
func TestSettings_OCSError401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, ocsErrEnvelope(http.StatusUnauthorized, "Unauthorized"))
	}))
	defer srv.Close()

	c := newClient(srv.URL, "secret")
	_, err := c.Settings(context.Background())
	if err == nil {
		t.Fatal("Settings: got nil, want error on OCS 401")
	}
	var oe *transport.OCSError
	if !errors.As(err, &oe) {
		t.Fatalf("errors.As(*OCSError): got %T (%v)", err, err)
	}
	if oe.Code != http.StatusUnauthorized {
		t.Errorf("OCSError.Code: got %d, want 401", oe.Code)
	}
}

// TestSettings_HTTP404_NonOCSBody — HTTP 404 с HTML-телом от reverse-proxy
// (не OCS-конверт). transport.DoOCS нормализует это в *OCSError{Code:404}
// (см. комментарий в transport.DoOCS про reverse-proxy 404).
func TestSettings_HTTP404_NonOCSBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, "<html><body>404 Not Found</body></html>")
	}))
	defer srv.Close()

	c := newClient(srv.URL, "secret")
	_, err := c.Settings(context.Background())
	if err == nil {
		t.Fatal("Settings: got nil, want error on HTTP 404")
	}
	var oe *transport.OCSError
	if !errors.As(err, &oe) {
		t.Fatalf("errors.As(*OCSError): got %T (%v)", err, err)
	}
	if oe.Code != http.StatusNotFound {
		t.Errorf("OCSError.Code: got %d, want 404", oe.Code)
	}
}

// TestSettings_HTTP404_OCSBody — OCS-конверт с meta.statusCode=404 (так
// Spreed отвечает на запрос signaling-settings для несуществующего token'а).
func TestSettings_HTTP404_OCSBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, ocsErrEnvelope(http.StatusNotFound, "Room not found"))
	}))
	defer srv.Close()

	c := newClient(srv.URL, "secret")
	_, err := c.Settings(context.Background())
	if err == nil {
		t.Fatal("Settings: got nil, want error on OCS 404")
	}
	var oe *transport.OCSError
	if !errors.As(err, &oe) {
		t.Fatalf("errors.As(*OCSError): got %T (%v)", err, err)
	}
	if oe.Code != http.StatusNotFound {
		t.Errorf("OCSError.Code: got %d, want 404", oe.Code)
	}
}

// TestSettings_RedactPasswordInNetworkError — критичный для безопасности тест
// (по образцу cmd/nctalk/main_test.go:TestRun_PasswordDoesNotLeak_CrossHostRedirect).
//
// Сценарий: httptest.Server закрыли ДО запроса; следующий *http.Client.Do
// падает с network error (dial: connection refused), http.Client оборачивает
// её в *url.Error. transport.SanitizeErr (внутри DoOCS) отрезает userinfo/query
// из URL. Auth.Password = SECRET_MARKER.
//
// Утверждение: текст ошибки НЕ содержит SECRET_MARKER. Basic-auth живёт в
// заголовке Authorization (не в URL), но тест защищает от регрессий — например,
// случайного fmt.Errorf("%v", auth) или логирования full-request в будущем.
func TestSettings_RedactPasswordInNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, ocsOK(`{}`))
	}))
	srv.Close() // закрываем ДО запроса — следующий Do упадёт с network error

	c := newClient(srv.URL, "SECRET_MARKER")
	_, err := c.Settings(context.Background())
	if err == nil {
		t.Fatal("Settings: got nil, want network error от закрытого сервера")
	}
	if strings.Contains(err.Error(), "SECRET_MARKER") {
		t.Errorf("УТЕЧКА: пароль найден в тексте ошибки: %q", err.Error())
	}
}

// TestSettings_RequestHeaders — контракт transport.DoOCS: запрос несёт
// OCS-APIRequest:true, Accept:application/json и Authorization:Basic.
// Без OCS-APIRequest сервер отвечает 403 (CSRF-защита Nextcloud).
func TestSettings_RequestHeaders(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, ocsOK(`{"stunservers":[],"turnservers":[]}`))
	}))
	defer srv.Close()

	c := newClient(srv.URL, "secret")
	_, _ = c.Settings(context.Background())

	if got.Get("OCS-APIRequest") != "true" {
		t.Errorf("OCS-APIRequest header: got %q, want \"true\"", got.Get("OCS-APIRequest"))
	}
	if got.Get("Accept") != "application/json" {
		t.Errorf("Accept header: got %q, want \"application/json\"", got.Get("Accept"))
	}
	if auth := got.Get("Authorization"); !strings.HasPrefix(auth, "Basic ") {
		t.Errorf("Authorization header: got %q, want \"Basic ...\"", auth)
	}
}

// TestSettings_PathHasNoToken — signaling-settings БЕЗ token (баг #2): Spreed
// route Signaling#getSettings не принимает {token}, settings глобальны. Path
// заканчивается на /settings, без подставленного token; query тоже пуст.
func TestSettings_PathHasNoToken(t *testing.T) {
	var capturedPath, capturedRawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedRawQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, ocsOK(`{"stunservers":[],"turnservers":[]}`))
	}))
	defer srv.Close()

	c := newClient(srv.URL, "secret")
	if _, err := c.Settings(context.Background()); err != nil {
		t.Fatalf("Settings: %v", err)
	}
	if want := "/ocs/v2.php/apps/spreed/api/v3/signaling/settings"; capturedPath != want {
		t.Errorf("URL.Path: got %q, want %q (БЕЗ token)", capturedPath, want)
	}
	if capturedRawQuery != "" {
		t.Errorf("URL.RawQuery: got %q, want empty", capturedRawQuery)
	}
}
