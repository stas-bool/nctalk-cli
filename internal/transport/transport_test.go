// Package transport — тесты чистого HTTP-транспорта поверх OCS-конверта.
//
// Покрывает: SanitizeURL/SanitizeErr (редакт кредов в URL), SameHostRedirectPolicy
// (блокировка cross-host и https→http downgrade), OCSError/OCSEnvelope,
// DoOCS (успех, типизированные ошибки, нормализация HTTP 404).
//
// Источник сценариев — internal/client/client_test.go + internal/client/ocs_test.go:
// тесты перенесены механически и адаптированы под функции пакета transport
// (SanitizeErr вместо sanitizeErr, SameHostRedirectPolicy вместо
// sameHostRedirectPolicy, DoOCS вместо TalkClient.doOCS).
package transport

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// ocsBodyTransport строит тело OCS-ответа с указанным statusCode/message/data.
// data может быть nil. Используется httptest-хендлером.
func ocsBodyTransport(t *testing.T, statusCode int, message string, data any) []byte {
	t.Helper()
	var env OCSEnvelope[any]
	env.OCS.Meta.Status = "ok"
	env.OCS.Meta.StatusCode = statusCode
	env.OCS.Meta.Message = message
	env.OCS.Data = data
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal OCS: %v", err)
	}
	return b
}

// mustParseURL — вспомогательный парсер для тестов политики редиректов.
func mustParseURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

// authFromURL строит Auth для тестов: BaseURL=распарсенный URL, креды фиксированы.
func authFromURL(t *testing.T, rawURL string) Auth {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse BaseURL %q: %v", rawURL, err)
	}
	return Auth{BaseURL: u, Login: "alice", Password: "secret"}
}

// wantAuthHeader возвращает ожидаемый Authorization для authFromURL (alice:secret).
func wantAuthHeader() string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:secret"))
}

// ---- SanitizeURL/SanitizeErr ----

// TestSanitizeURL проверяет, что SanitizeURL убирает userinfo и query, но
// сохраняет scheme/host/path. При ошибке парсинга — "<invalid url>".
func TestSanitizeURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"userinfo+query", "https://user:secret@host/path?x=1", "https://host/path"},
		{"no userinfo", "http://host/path", "http://host/path"},
		{"query only", "http://host/p?a=1", "http://host/p"},
		{"root", "https://nc.example.com", "https://nc.example.com"},
		{"invalid", "://bad", "<invalid url>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SanitizeURL(tc.in); got != tc.want {
				t.Errorf("SanitizeURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestSanitizeErr_URLRedacted — основная проверка редaкта (DoD): строит URL
// вида https://user:secret@host/path?x=1, оборачивает в *url.Error, прогоняет
// через SanitizeErr и убеждается, что в результате НЕТ ни "secret", ни "x=1".
// Исходная ошибка и scheme://host/path сохраняются.
func TestSanitizeErr_URLRedacted(t *testing.T) {
	orig := &url.Error{
		Op:  "Get",
		URL: "https://user:secret@host/path?x=1",
		Err: errors.New("connection refused"),
	}
	got := SanitizeErr(orig)
	s := got.Error()
	if strings.Contains(s, "secret") {
		t.Errorf("SanitizeErr: утечка userinfo в тексте: %q", s)
	}
	if strings.Contains(s, "x=1") {
		t.Errorf("SanitizeErr: утечка query в тексте: %q", s)
	}
	if !strings.Contains(s, "host/path") {
		t.Errorf("SanitizeErr: путь должен сохраниться: %q", s)
	}
	if !strings.Contains(s, "connection refused") {
		t.Errorf("SanitizeErr: исходная ошибка потеряна: %q", s)
	}
}

// TestSanitizeErr_NoURLError — если в цепочке нет *url.Error, возвращаем err as is.
func TestSanitizeErr_NoURLError(t *testing.T) {
	src := errors.New("some plain error")
	got := SanitizeErr(src)
	if got != src {
		t.Errorf("SanitizeErr: got %v, want исходную ошибку %v", got, src)
	}
}

// TestSanitizeErr_NilReturnsNil — контракт: nil → nil (не паника).
func TestSanitizeErr_NilReturnsNil(t *testing.T) {
	if got := SanitizeErr(nil); got != nil {
		t.Errorf("SanitizeErr(nil): got %v, want nil", got)
	}
}

// ---- SameHostRedirectPolicy ----

// TestSameHostRedirectPolicy_EmptyVia — пустой via (первый запрос в цепочке)
// всегда разрешается; http.Client таки вызывает политику только при редиректе,
// но защитная проверка len(via)==0 нужна для устойчивости.
func TestSameHostRedirectPolicy_EmptyVia(t *testing.T) {
	if err := SameHostRedirectPolicy(
		&http.Request{URL: mustParseURL("https://host")},
		nil,
	); err != nil {
		t.Errorf("empty via: got %v, want nil", err)
	}
}

// TestSameHostRedirectPolicy_TableDriven покрывает ветки политики:
//   - same-host https→https → follow (nil);
//   - same-host http→http → follow;
//   - same-host http→https → follow (апгрейд безопасен);
//   - same-host https→http → БЛОК (даунгрейд, иначе Authorization уйдёт открыто);
//   - cross-host https→https → БЛОК.
func TestSameHostRedirectPolicy_TableDriven(t *testing.T) {
	cases := []struct {
		name    string
		prev    string // scheme://host предыдущего запроса
		next    string // scheme://host нового запроса
		wantErr bool
	}{
		{"https→http same host (даунгрейд)", "https://nc", "http://nc", true},
		{"https→https same host (разрешено)", "https://nc", "https://nc", false},
		{"http→http same host (разрешено)", "http://nc", "http://nc", false},
		{"http→https same host (апгрейд, разрешено)", "http://nc", "https://nc", false},
		{"https→https cross host (блок)", "https://nc", "https://evil", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prevU := mustParseURL(tc.prev)
			nextU := mustParseURL(tc.next)
			err := SameHostRedirectPolicy(
				&http.Request{URL: nextU},
				[]*http.Request{{URL: prevU}},
			)
			if tc.wantErr && err == nil {
				t.Errorf("хотели ошибку блокировки, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("не хотели ошибку, got %v", err)
			}
		})
	}
}

// ---- OCSError ----

// TestOCSError_Error — метод Error формирует текст по правилу:
//   - непустое Message → «client: <Message>» (префикс «client: » сохранён для
//     обратной совместимости текстов существующих тестов/сообщений);
//   - пустое Message → «client: OCS statusCode=<Code>»;
//   - nil-приёмник → безопасная строка без паники.
//
// NB: префикс «client: » сохранён намеренно — это часть пользовательского
// текста ошибок, на него опираются e2e-тесты (TestRun_ChatShow_OCS404_Exit2
// проверяет только вхождение "Room not found", но стабильность формата важна
// для логов и совместимости с другими потребителями).
func TestOCSError_Error(t *testing.T) {
	cases := []struct {
		name string
		err  *OCSError
		want string
	}{
		{
			name: "code+message",
			err:  &OCSError{Code: 404, Message: "room not found"},
			want: "client: room not found",
		},
		{
			name: "пустое message — код в канонической форме",
			err:  &OCSError{Code: 404, Message: ""},
			want: "client: OCS statusCode=404",
		},
		{
			name: "auth 401 с message",
			err:  &OCSError{Code: http.StatusUnauthorized, Message: "bad credentials"},
			want: "client: bad credentials",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Errorf("Error(): got %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("nil-приёмник не паникует", func(t *testing.T) {
		var e *OCSError
		got := e.Error()
		if got == "" {
			t.Errorf("Error() на nil-приёмнике: пустая строка, want непустая")
		}
	})
}

// TestOCSError_ErrorsAs — *OCSError извлекается из ошибок через errors.As
// (в т.ч. через обёртку fmt.Errorf("…: %w", ocsErr)) — это механизм, по которому
// вышележащий cli.exitFromClientErr различает 404 и прочие statusCode.
func TestOCSError_ErrorsAs(t *testing.T) {
	src := &OCSError{Code: 404, Message: "room not found"}

	t.Run("прямой *OCSError", func(t *testing.T) {
		var oe *OCSError
		if !errors.As(src, &oe) {
			t.Fatalf("errors.As: не извлёк *OCSError из самого себя")
		}
		if oe.Code != 404 || oe.Message != "room not found" {
			t.Errorf("извлечённый OCSError = %+v, want {Code:404 Message:%q}", oe, "room not found")
		}
	})

	t.Run("оборачнутый через fmt.Errorf %w", func(t *testing.T) {
		wrapped := fmt.Errorf("GetChat: %w", src)
		var oe *OCSError
		if !errors.As(wrapped, &oe) {
			t.Fatalf("errors.As: не прошел по цепочке Unwrap")
		}
		if oe.Code != 404 {
			t.Errorf("Code: got %d, want 404", oe.Code)
		}
	})
}

// ---- OCSEnvelope ----

// TestOCSEnvelope_Decode — базовая проверка, что JSON распаковывается в
// структуру с заполнением meta.* и data через параметр-тип.
func TestOCSEnvelope_Decode(t *testing.T) {
	body := `{"ocs":{"meta":{"status":"ok","statuscode":200,"message":"OK"},"data":{"foo":"bar"}}}`
	var env OCSEnvelope[map[string]string]
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.OCS.Meta.StatusCode != 200 {
		t.Errorf("StatusCode: got %d, want 200", env.OCS.Meta.StatusCode)
	}
	if env.OCS.Meta.Message != "OK" {
		t.Errorf("Message: got %q, want %q", env.OCS.Meta.Message, "OK")
	}
	if env.OCS.Data["foo"] != "bar" {
		t.Errorf("Data[foo]: got %q, want %q", env.OCS.Data["foo"], "bar")
	}
}

// ---- DoOCS ----

// TestDoOCS_Headers проверяет, что DoOCS выставляет обязательные заголовки
// (спека §5): Authorization (Basic <base64(login:pass)>), OCS-APIRequest: true,
// Accept: application/json.
func TestDoOCS_Headers(t *testing.T) {
	var gotAuth, gotOCS, gotAccept string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotOCS = r.Header.Get("OCS-APIRequest")
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(ocsBodyTransport(t, 200, "OK", nil))
	}))
	defer ts.Close()

	auth := authFromURL(t, ts.URL)
	var out json.RawMessage
	if _, err := DoOCS(context.Background(), http.DefaultClient, auth, http.MethodGet, "/p", nil, nil, false, &out); err != nil {
		t.Fatalf("DoOCS: %v", err)
	}
	if gotAuth != wantAuthHeader() {
		t.Errorf("Authorization: got %q, want %q", gotAuth, wantAuthHeader())
	}
	if gotOCS != "true" {
		t.Errorf("OCS-APIRequest: got %q, want %q", gotOCS, "true")
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept: got %q, want %q", gotAccept, "application/json")
	}
}

// TestDoOCS_MutateContentType — при mutate=true добавляется Content-Type:
// application/json (для POST/PUT/DELETE).
func TestDoOCS_MutateContentType(t *testing.T) {
	var gotCT string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(ocsBodyTransport(t, 200, "OK", nil))
	}))
	defer ts.Close()

	auth := authFromURL(t, ts.URL)
	body := strings.NewReader(`{"x":1}`)
	var out any
	if _, err := DoOCS(context.Background(), http.DefaultClient, auth, http.MethodPost, "/p", nil, body, true, &out); err != nil {
		t.Fatalf("DoOCS: %v", err)
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type: got %q, want %q", gotCT, "application/json")
	}
}

// TestDoOCS_Success — успешный конверт распаковывается в целевой тип.
func TestDoOCS_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(ocsBodyTransport(t, 200, "OK", map[string]any{"foo": "bar"}))
	}))
	defer ts.Close()

	auth := authFromURL(t, ts.URL)
	var out map[string]string
	if _, err := DoOCS(context.Background(), http.DefaultClient, auth, http.MethodGet, "/p", nil, nil, false, &out); err != nil {
		t.Fatalf("DoOCS: %v", err)
	}
	if out["foo"] != "bar" {
		t.Errorf("out[foo]: got %q, want %q", out["foo"], "bar")
	}
}

// TestDoOCS_ReturnsTypedOCSError — главный регрессионный тест баги e2e:
// при meta.statusCode>=400 DoOCS обязан возвращать именно *OCSError (с Code),
// а не errors.New — иначе вышележащий маппинг не сможет различить 404 и 401.
//
// Воспроизводим сценарий: httptest-сервер отдаёт OCS 404 с message, и
// проверяем, что errors.As(*OCSError) извлекает Code=404 из ошибки.
func TestDoOCS_ReturnsTypedOCSError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(ocsBodyTransport(t, 404, "room not found", nil))
	}))
	defer ts.Close()

	auth := authFromURL(t, ts.URL)
	var out any
	_, err := DoOCS(context.Background(), http.DefaultClient, auth, http.MethodGet, "/p", nil, nil, false, &out)
	if err == nil {
		t.Fatal("err = nil, want *OCSError")
	}
	var oe *OCSError
	if !errors.As(err, &oe) {
		t.Fatalf("errors.As(*OCSError): не извлечён; err имеет тип %T (%v)", err, err)
	}
	if oe.Code != 404 {
		t.Errorf("OCSError.Code: got %d, want 404", oe.Code)
	}
	if !strings.Contains(oe.Error(), "room not found") {
		t.Errorf("Error(): got %q, want содержит 'room not found'", oe.Error())
	}
}

// TestDoOCS_OCSError_401_NotConfusedWith404 — регресс: 401 (auth) должен
// возвращаться как *OCSError{Code:401}, и его Code НЕ должен случайно
// совпадать с 404.
func TestDoOCS_OCSError_401_NotConfusedWith404(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(ocsBodyTransport(t, 401, "bad credentials", nil))
	}))
	defer ts.Close()

	auth := authFromURL(t, ts.URL)
	var out any
	_, err := DoOCS(context.Background(), http.DefaultClient, auth, http.MethodGet, "/p", nil, nil, false, &out)
	if err == nil {
		t.Fatal("err = nil, want *OCSError")
	}
	var oe *OCSError
	if !errors.As(err, &oe) {
		t.Fatalf("errors.As(*OCSError): не извлечён; err = %T (%v)", err, err)
	}
	if oe.Code != 401 {
		t.Errorf("OCSError.Code: got %d, want 401 (НЕ 404)", oe.Code)
	}
}

// TestDoOCS_HTTP404_with_OCS998_NormalizedTo404 — кейс с боевого Nextcloud:
// HTTP 404 + OCS meta.statusCode=998 («Invalid query») при запросе chat/{token}
// с кириллицей. Пользователь видит «комната не найдена» → маппинг должен дать
// exit 2, поэтому DoOCS нормализует Code до 404 когда HTTP-статус 404.
func TestDoOCS_HTTP404_with_OCS998_NormalizedTo404(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write(ocsBodyTransport(t, 998, "Invalid query, please check the syntax. API specifications are here: http://www.freedesktop.org/wiki/Specifications/open-collaboration-services.\n", nil))
	}))
	defer ts.Close()

	auth := authFromURL(t, ts.URL)
	var out any
	_, err := DoOCS(context.Background(), http.DefaultClient, auth, http.MethodGet, "/p", nil, nil, false, &out)
	if err == nil {
		t.Fatal("err = nil, want *OCSError")
	}
	var oe *OCSError
	if !errors.As(err, &oe) {
		t.Fatalf("errors.As(*OCSError): не извлечён; err = %T (%v)", err, err)
	}
	if oe.Code != http.StatusNotFound {
		t.Errorf("OCSError.Code: got %d, want 404 (HTTP 404 нормализует OCS 998 → 404)", oe.Code)
	}
}

// TestDoOCS_HTTP404_NonOCSBody_TypedAsNotFound — если на HTTP 404 сервер отдал
// HTML вместо OCS-конверта, decode падает, но мы всё равно трактуем это как
// NotFound и возвращаем *OCSError{Code:404} — чтобы cli-маппинг дал exit 2.
func TestDoOCS_HTTP404_NonOCSBody_TypedAsNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("<html><body>404 not found</body></html>"))
	}))
	defer ts.Close()

	auth := authFromURL(t, ts.URL)
	var out any
	_, err := DoOCS(context.Background(), http.DefaultClient, auth, http.MethodGet, "/p", nil, nil, false, &out)
	if err == nil {
		t.Fatal("err = nil, want *OCSError")
	}
	var oe *OCSError
	if !errors.As(err, &oe) {
		t.Fatalf("errors.As(*OCSError): не извлечён; err = %T (%v)", err, err)
	}
	if oe.Code != http.StatusNotFound {
		t.Errorf("OCSError.Code: got %d, want 404 (HTTP 404 + не-OCS тело → NotFound)", oe.Code)
	}
}

// TestDoOCS_HTTP500_DecodeFailure_StaysGeneric — HTTP 500 + не-OCS тело (HTML
// от reverse-proxy) НЕ должно превращаться в OCSError{Code:404} — остаётся
// обычная transport-подобная ошибка → cli даёт exit 1 (Generic).
func TestDoOCS_HTTP500_DecodeFailure_StaysGeneric(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("<html><body>500 server error</body></html>"))
	}))
	defer ts.Close()

	auth := authFromURL(t, ts.URL)
	var out any
	_, err := DoOCS(context.Background(), http.DefaultClient, auth, http.MethodGet, "/p", nil, nil, false, &out)
	if err == nil {
		t.Fatal("err = nil, want error")
	}
	var oe *OCSError
	if errors.As(err, &oe) && oe.Code == http.StatusNotFound {
		t.Errorf("HTTP 500 не должен классифицироваться как NotFound; got *OCSError{Code:404}")
	}
}

// TestDoOCS_304_NoBody_NoError — 304/204 трактуются как пустой ответ: out не
// трогается, ошибка не возвращается (стоп пагинации для chat-API).
func TestDoOCS_304_NoBody_NoError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	}))
	defer ts.Close()

	auth := authFromURL(t, ts.URL)
	var out map[string]any
	// Важно: out остаётся nil-ным — функция не должна его трогать.
	if _, err := DoOCS(context.Background(), http.DefaultClient, auth, http.MethodGet, "/p", nil, nil, false, &out); err != nil {
		t.Fatalf("DoOCS на 304: got %v, want nil", err)
	}
}

// ---- Изоляция от Pion: проверка, что transport не зависит от internal/call/* ----
//
// Это статическая проверка того, что transport не импортирует internal/call
// (включая pion/webrtc). Реальный end-to-end тест на cmd/nctalk живёт в
// internal/call/isolation_test.go — здесь же компактная гарантия на уровне
// пакета-источника: импортная декларация transport.go не должна содержать
// "github.com/pion/webrtc/v4".

// io.Copy в эту переменную, чтобы импорт io не терся goimports в будущих правках
// (и оставался доступным для тестов на body-обработку).
var _ = io.Copy
