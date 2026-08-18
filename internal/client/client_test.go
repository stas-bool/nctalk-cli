package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stas-bool/nctalk-cli/internal/config"
	"github.com/stas-bool/nctalk-cli/internal/transport"
)

// testCfg собирает Config с указанным baseURL и фиксированными тестовыми
// кредами (login=alice, password=secret). Timeout=5s — чтобы тест не завис
// при сетевой ошибке.
func testCfg(baseURL string) config.Config {
	return config.Config{
		BaseURL:  baseURL,
		Login:    "alice",
		Password: "secret",
		Timeout:  5 * time.Second,
	}
}

// ocsBody строит тело OCS-ответа с указанным statusCode/message/data.
// data может быть nil. Используется httptest-хендлером.
//
// NB: используется transport.OCSEnvelope напрямую (а не client-alias), т.к.
// Go 1.21 не поддерживает generic type aliases (см. client/aliases.go).
func ocsBody(t *testing.T, statusCode int, message string, data any) []byte {
	t.Helper()
	var env transport.OCSEnvelope[any]
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

// wantAuth возвращает ожидаемое значение заголовка Authorization для testCfg.
func wantAuth() string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:secret"))
}

// TestDoOCS_Headers проверяет, что doOCS выставляет обязательные заголовки
// (спека §5): Authorization (Basic <base64(login:pass)>), OCS-APIRequest: true,
// Accept: application/json.
func TestDoOCS_Headers(t *testing.T) {
	var gotAuth, gotOCS, gotAccept string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotOCS = r.Header.Get("OCS-APIRequest")
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(ocsBody(t, 200, "OK", nil))
	}))
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	var out []Message
	if _, err := c.doOCS(context.Background(), http.MethodGet, pathRooms, nil, nil, false, &out); err != nil {
		t.Fatalf("doOCS: %v", err)
	}
	if gotAuth != wantAuth() {
		t.Errorf("Authorization: got %q, want %q", gotAuth, wantAuth())
	}
	if gotOCS != "true" {
		t.Errorf("OCS-APIRequest: got %q, want %q", gotOCS, "true")
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept: got %q, want %q", gotAccept, "application/json")
	}
}

// TestDoOCS_Success проверяет, что успешный OCS-конверт распаковывается в
// целевой тип ([]Message), включая вложенные MsgParam и map[string]int
// реакций.
func TestDoOCS_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		data := []map[string]any{{
			"id":               42,
			"actorType":        "users",
			"actorId":          "alice",
			"actorDisplayName": "Alice",
			"messageType":      "comment",
			"message":          "hi {file}",
			"messageParameters": map[string]any{
				"file": map[string]any{
					"type": "file",
					"id":   "100",
					"name": "doc.pdf",
					"path": "/files/doc.pdf",
					"link": "https://nc.example.com/f/100",
				},
			},
			"reactions":   map[string]any{"👍": 2},
			"timestamp":   1700000000,
			"token":       "abc123",
			"isReplyable": true,
			"markdown":    true,
		}}
		_, _ = w.Write(ocsBody(t, 200, "OK", data))
	}))
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	var out []Message
	if _, err := c.doOCS(context.Background(), http.MethodGet, pathRooms, nil, nil, false, &out); err != nil {
		t.Fatalf("doOCS: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("len(out) = %d, want 1", len(out))
	}
	m := out[0]
	if m.Id != 42 {
		t.Errorf("Id: got %d, want 42", m.Id)
	}
	if m.Message != "hi {file}" {
		t.Errorf("Message: got %q, want %q", m.Message, "hi {file}")
	}
	if mp := m.MessageParameters["file"]; mp.Name != "doc.pdf" || mp.Type != "file" {
		t.Errorf("MsgParam.file: got %+v, want {Type:file Name:doc.pdf}", mp)
	}
	if m.Reactions["👍"] != 2 {
		t.Errorf("Reactions[👍]: got %d, want 2", m.Reactions["👍"])
	}
	if m.Timestamp != 1700000000 {
		t.Errorf("Timestamp: got %d, want 1700000000", m.Timestamp)
	}
	if !m.IsReplyable || !m.Markdown {
		t.Errorf("IsReplyable/Markdown: got %v/%v, want true/true", m.IsReplyable, m.Markdown)
	}
}

// TestDoOCS_OCSError проверяет, что при meta.statusCode>=400 возвращается
// ошибка, содержащая meta.message.
func TestDoOCS_OCSError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(ocsBody(t, 404, "room not found", nil))
	}))
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	var out []Message
	_, err := c.doOCS(context.Background(), http.MethodGet, pathRooms, nil, nil, false, &out)
	if err == nil {
		t.Fatal("err = nil, want error containing 'room not found'")
	}
	if !strings.Contains(err.Error(), "room not found") {
		t.Errorf("err: got %q, want contains 'room not found'", err.Error())
	}
}

// TestDoOCS_MutateContentType проверяет, что при mutate=true добавляется
// заголовок Content-Type: application/json (для mutation-запросов).
func TestDoOCS_MutateContentType(t *testing.T) {
	var gotCT string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(ocsBody(t, 200, "OK", nil))
	}))
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	var out any
	body := strings.NewReader(`{"x":1}`)
	if _, err := c.doOCS(context.Background(), http.MethodPost, pathRooms, nil, body, true, &out); err != nil {
		t.Fatalf("doOCS: %v", err)
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type: got %q, want %q", gotCT, "application/json")
	}
}

// TestSanitizeErr_URLRedacted — основная проверка редaкта (DoD): строит URL
// вида https://user:secret@host/path?x=1, оборачивает в *url.Error, прогоняет
// через sanitizeErr и убеждается, что в результате НЕТ ни "secret", ни "x=1".
// Исходная ошибка и scheme://host/path сохраняются.
func TestSanitizeErr_URLRedacted(t *testing.T) {
	orig := &url.Error{
		Op:  "Get",
		URL: "https://user:secret@host/path?x=1",
		Err: errors.New("connection refused"),
	}
	got := sanitizeErr(orig)
	s := got.Error()
	if strings.Contains(s, "secret") {
		t.Errorf("sanitizeErr: утечка userinfo в тексте: %q", s)
	}
	if strings.Contains(s, "x=1") {
		t.Errorf("sanitizeErr: утечка query в тексте: %q", s)
	}
	if !strings.Contains(s, "host/path") {
		t.Errorf("sanitizeErr: путь должен сохраниться: %q", s)
	}
	if !strings.Contains(s, "connection refused") {
		t.Errorf("sanitizeErr: исходная ошибка потеряна: %q", s)
	}
}

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

// TestRedirect_CrossHostBlocked проверяет политику редиректов (спека §5):
// сервер A возвращает 302 на сервер B (другой хост) → клиент блокирует редирект,
// на сервер B запрос НЕ идёт (счётчик = 0), doOCS возвращает ошибку с
// "cross-host redirect blocked". В тексте ошибки нет кредов.
func TestRedirect_CrossHostBlocked(t *testing.T) {
	var bCount int32
	tsB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&bCount, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(ocsBody(t, 200, "OK", nil))
	}))
	defer tsB.Close()

	tsA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Редирект на ДРУГОЙ хост (tsB). Location — абсолютный URL.
		http.Redirect(w, r, tsB.URL+pathRooms, http.StatusFound)
	}))
	defer tsA.Close()

	c := NewTalkClient(testCfg(tsA.URL))
	var out []Message
	_, err := c.doOCS(context.Background(), http.MethodGet, pathRooms, nil, nil, false, &out)
	if err == nil {
		t.Fatal("err = nil, want cross-host redirect error")
	}
	if !strings.Contains(err.Error(), "cross-host redirect blocked") {
		t.Errorf("err: got %q, want contains 'cross-host redirect blocked'", err.Error())
	}
	// Структурная гарантия: Authorization никогда не попадает в URL/текст
	// ошибки. Проверяем отсутствие логина/пароля.
	if strings.Contains(err.Error(), "alice") || strings.Contains(err.Error(), "secret") {
		t.Errorf("err: утечка кредов в тексте: %q", err.Error())
	}
	if got := atomic.LoadInt32(&bCount); got != 0 {
		t.Errorf("server B hit count: got %d, want 0 (редирект не должен следовать)", got)
	}
}

// TestRedirect_SameHostFollowed проверяет, что same-host редирект разрешается:
// сервер возвращает 302 на тот же хост (другой путь) → клиент следует и
// получает финальный 200 с корректно распакованным data.
func TestRedirect_SameHostFollowed(t *testing.T) {
	var finalHits int32
	// ts объявляем заранее — замыкание хендлера ссылается на ts.URL.
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case pathRooms:
			// 302 на тот же хост, путь /final. Location — абсолютный URL.
			http.Redirect(w, r, ts.URL+"/final", http.StatusFound)
			return
		case "/final":
			atomic.AddInt32(&finalHits, 1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(ocsBody(t, 200, "OK", []map[string]any{{
				"id":      7,
				"message": "redirected ok",
				"token":   "tok",
			}}))
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	var out []Message
	if _, err := c.doOCS(context.Background(), http.MethodGet, pathRooms, nil, nil, false, &out); err != nil {
		t.Fatalf("doOCS: %v", err)
	}
	if got := atomic.LoadInt32(&finalHits); got != 1 {
		t.Errorf("final endpoint hit count: got %d, want 1", got)
	}
	if len(out) != 1 || out[0].Id != 7 {
		t.Errorf("out: got %+v, want single message id=7", out)
	}
}

// TestRedirect_BlocksHttpsToHttpDowngrade — same-host редирект с https на http
// должен блокироваться: иначе Authorization (с паролем) уйдёт в открытом виде.
// Политика проверяется напрямую (без TLS-сервера) — синтетическими запросами.
func TestRedirect_BlocksHttpsToHttpDowngrade(t *testing.T) {
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
			err := sameHostRedirectPolicy(
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

// mustParseURL — вспомогательный парсер для тестов политики редиректов.
func mustParseURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}
