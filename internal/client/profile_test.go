package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

// profileFixture читает фикстуру из корневого testdata/ (конвенция репо).
func profileFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return body
}

// profileServer — httptest-сервер, отвечающий status + body на любой запрос
// и считающий запросы (для проверок «без сети»).
func profileServer(t *testing.T, status int, body []byte) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(ts.Close)
	return ts, &hits
}

// TestGetProfile — фикстура реального формата (обезличенный ответ живого
// сервера 2026-10-22): метод/путь/заголовки, маппинг полей, null → "",
// фильтр talk/talk-call, срез mailto:/tel:, порядок контактов — как у сервера.
func TestGetProfile(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotOCS, gotAccept string
	body := profileFixture(t, "profile.json")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotOCS = r.Header.Get("OCS-APIRequest")
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	// Аргумент — логин в другом регистре: сервер отвечает каноническим uid,
	// клиент НЕ сравнивает его с аргументом (спека-дельта §3).
	p, err := c.GetProfile(context.Background(), "Ivan.P")
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("method: got %q, want GET", gotMethod)
	}
	if want := "/ocs/v2.php/profile/Ivan.P"; gotPath != want {
		t.Errorf("path: got %q, want %q", gotPath, want)
	}
	if gotAuth != wantAuth() {
		t.Errorf("Authorization: got %q, want %q", gotAuth, wantAuth())
	}
	if gotOCS != "true" {
		t.Errorf("OCS-APIRequest: got %q, want \"true\"", gotOCS)
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept: got %q, want \"application/json\"", gotAccept)
	}

	want := &Profile{
		UserId:         "ivan.p",
		DisplayName:    "Иван Петров",
		Role:           "Генеральный директор",
		Headline:       "",
		Organisation:   "Example LLC",
		Address:        "Калининград",
		Pronouns:       "",
		Biography:      "",
		Timezone:       "Europe/Kaliningrad",
		TimezoneOffset: 7200,
		Contacts: []ProfileContact{
			{Type: "email", Value: "ivan.p@example.org"},
			{Type: "phone", Value: "+70000000000"},
			{Type: "website", Value: "https://example.org"},
			{Type: "twitter", Value: "https://x.com/example"},
		},
	}
	if !reflect.DeepEqual(p, want) {
		t.Errorf("profile:\n got %+v\nwant %+v", p, want)
	}
}

// TestGetProfile_AllStringFields — непустые headline/pronouns/biography
// (в живой фикстуре null) маппятся в свои поля; биография — как есть.
func TestGetProfile_AllStringFields(t *testing.T) {
	const body = `{"ocs":{"meta":{"status":"ok","statuscode":200,"message":"OK"},"data":{` +
		`"userId":"ivan.p","address":"A","isUserAvatarVisible":true,"biography":"Строка 1\n\tСтрока 2",` +
		`"displayname":"D","headline":"H","organisation":"O","role":"R","pronouns":"он/его",` +
		`"actions":[],"timezone":"UTC","timezoneOffset":0}}}`
	ts, _ := profileServer(t, http.StatusOK, []byte(body))

	p, err := NewTalkClient(testCfg(ts.URL)).GetProfile(context.Background(), "ivan.p")
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if p.Headline != "H" || p.Pronouns != "он/его" || p.Biography != "Строка 1\n\tСтрока 2" ||
		p.Organisation != "O" || p.Role != "R" || p.Address != "A" || p.DisplayName != "D" {
		t.Errorf("profile: got %+v", p)
	}
}

// TestGetProfile_AllNull — все строковые поля null, actions [] → пустые
// строки и non-nil пустой Contacts (детерминированный --json: [] не null).
func TestGetProfile_AllNull(t *testing.T) {
	const body = `{"ocs":{"meta":{"status":"ok","statuscode":200,"message":"OK"},"data":{` +
		`"userId":"ivan.p","address":null,"isUserAvatarVisible":false,"biography":null,` +
		`"displayname":null,"headline":null,"organisation":null,"role":null,"pronouns":null,` +
		`"actions":[],"timezone":"UTC","timezoneOffset":0}}}`
	ts, _ := profileServer(t, http.StatusOK, []byte(body))

	p, err := NewTalkClient(testCfg(ts.URL)).GetProfile(context.Background(), "ivan.p")
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	want := &Profile{UserId: "ivan.p", Timezone: "UTC", Contacts: []ProfileContact{}}
	if !reflect.DeepEqual(p, want) {
		t.Errorf("profile:\n got %+v\nwant %+v", p, want)
	}
	if p.Contacts == nil {
		t.Error("Contacts: got nil, want non-nil пустой слайс")
	}
}

// TestGetProfile_ActionsEdgeCases — пустой target и голый «mailto:»
// пропускаются; неизвестный id выводится тем же правилом (format-drift-safe);
// target без известной схемы — как есть.
func TestGetProfile_ActionsEdgeCases(t *testing.T) {
	const body = `{"ocs":{"meta":{"status":"ok","statuscode":200,"message":"OK"},"data":{` +
		`"userId":"ivan.p","actions":[` +
		`{"id":"email","icon":"","title":"","target":""},` +
		`{"id":"email","icon":"","title":"","target":"mailto:"},` +
		`{"id":"talk","icon":"","title":"","target":"https://cloud.example.org/apps/spreed/"},` +
		`{"id":"fediverse","icon":"","title":"","target":"https://social.example.org/@ivan"},` +
		`{"id":"matrix","icon":"","title":"","target":"tel:+70000000001"}` +
		`],"timezone":"UTC","timezoneOffset":0}}}`
	ts, _ := profileServer(t, http.StatusOK, []byte(body))

	p, err := NewTalkClient(testCfg(ts.URL)).GetProfile(context.Background(), "ivan.p")
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	want := []ProfileContact{
		{Type: "fediverse", Value: "https://social.example.org/@ivan"},
		{Type: "matrix", Value: "+70000000001"},
	}
	if !reflect.DeepEqual(p.Contacts, want) {
		t.Errorf("contacts:\n got %+v\nwant %+v", p.Contacts, want)
	}
}

// TestGetProfile_NotFound — живой формат 404 (HTTP 404, OCS 404, message "",
// data null) → *OCSError{Code:404, Message:""} без обёртки.
func TestGetProfile_NotFound(t *testing.T) {
	ts, _ := profileServer(t, http.StatusNotFound, profileFixture(t, "profile_not_found.json"))

	_, err := NewTalkClient(testCfg(ts.URL)).GetProfile(context.Background(), "nobody")
	var oe *OCSError
	if !errors.As(err, &oe) || oe.Code != http.StatusNotFound || oe.Message != "" {
		t.Fatalf("err: got %#v, want *OCSError{Code:404, Message:\"\"}", err)
	}
}

// TestGetProfile_RouteMissing — сервер старше Nextcloud 31: HTTP 404 + OCS 998
// «Invalid query» → транспорт нормализует в *OCSError{Code:404} с НЕпустым
// Message (по нему cli отличает «нет profile-API» от «нет пользователя»).
func TestGetProfile_RouteMissing(t *testing.T) {
	const body = `{"ocs":{"meta":{"status":"failure","statuscode":998,"message":"Invalid query, please check the syntax. API specifications are here: http:\/\/www.freedesktop.org\/wiki\/Specifications\/open-collaboration-services."},"data":[]}}`
	ts, _ := profileServer(t, http.StatusNotFound, []byte(body))

	_, err := NewTalkClient(testCfg(ts.URL)).GetProfile(context.Background(), "ivan.p")
	var oe *OCSError
	if !errors.As(err, &oe) || oe.Code != http.StatusNotFound || oe.Message == "" {
		t.Fatalf("err: got %#v, want *OCSError{Code:404, Message: непустой}", err)
	}
}

// TestGetProfile_Disabled — профиль отключён: HTTP 400 + OCS 400, data null
// → *OCSError{Code:400} (формат по коду сервера, живьём не воспроизводился).
func TestGetProfile_Disabled(t *testing.T) {
	const body = `{"ocs":{"meta":{"status":"failure","statuscode":400,"message":""},"data":null}}`
	ts, _ := profileServer(t, http.StatusBadRequest, []byte(body))

	_, err := NewTalkClient(testCfg(ts.URL)).GetProfile(context.Background(), "ivan.p")
	var oe *OCSError
	if !errors.As(err, &oe) || oe.Code != http.StatusBadRequest {
		t.Fatalf("err: got %#v, want *OCSError{Code:400}", err)
	}
}

// TestGetProfile_RateLimited — HTTP 429 с пустым телом (живьём так) →
// ошибка декодирования с «HTTP 429» в тексте, НЕ *OCSError (не exit 2).
func TestGetProfile_RateLimited(t *testing.T) {
	ts, _ := profileServer(t, http.StatusTooManyRequests, nil)

	_, err := NewTalkClient(testCfg(ts.URL)).GetProfile(context.Background(), "ivan.p")
	if err == nil {
		t.Fatal("GetProfile: got nil err, want ошибку 429")
	}
	var oe *OCSError
	if errors.As(err, &oe) {
		t.Fatalf("err: got *OCSError %#v, want не-OCS ошибку", oe)
	}
	if !strings.Contains(err.Error(), "HTTP 429") {
		t.Errorf("err: got %q, want содержит «HTTP 429»", err)
	}
}

// TestGetProfile_FormatDrift — успех без данных (data:null, нет поля data,
// HTTP 204) и данные без userId → ошибка, а не пустой профиль с exit 0
// (спека-дельта §4, находка №4).
func TestGetProfile_FormatDrift(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"data null", http.StatusOK, `{"ocs":{"meta":{"status":"ok","statuscode":200,"message":"OK"},"data":null}}`},
		{"нет поля data", http.StatusOK, `{"ocs":{"meta":{"status":"ok","statuscode":200,"message":"OK"}}}`},
		{"HTTP 204", http.StatusNoContent, ``},
		{"пустой userId", http.StatusOK, `{"ocs":{"meta":{"status":"ok","statuscode":200,"message":"OK"},"data":{"userId":"","displayname":"X","actions":[]}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts, _ := profileServer(t, tc.status, []byte(tc.body))
			p, err := NewTalkClient(testCfg(ts.URL)).GetProfile(context.Background(), "ivan.p")
			if err == nil {
				t.Fatalf("GetProfile: got nil err (profile=%+v), want ошибку drift-а", p)
			}
			if !strings.Contains(err.Error(), "GetProfile") {
				t.Errorf("err: got %q, want содержит «GetProfile»", err)
			}
		})
	}
}

// TestGetProfile_InvalidUserId — userId, не являющийся одним сегментом пути,
// отвергается ДО сети (единственный guard, спека-дельта §2, §4).
func TestGetProfile_InvalidUserId(t *testing.T) {
	ts, hits := profileServer(t, http.StatusOK, profileFixture(t, "profile.json"))
	c := NewTalkClient(testCfg(ts.URL))
	for _, id := range []string{"", ".", "..", "a/b", "../x"} {
		_, err := c.GetProfile(context.Background(), id)
		if err == nil || !strings.Contains(err.Error(), "недопустимый userId") {
			t.Errorf("GetProfile(%q): got %v, want «недопустимый userId»", id, err)
		}
	}
	if n := atomic.LoadInt32(hits); n != 0 {
		t.Errorf("запросов к серверу: got %d, want 0", n)
	}
}

// TestGetProfile_PathEscape — спецсимволы и управляющие символы уходят ОДНИМ
// экранированным сегментом (эскейп делает транспорт; ручной PathEscape дал
// бы %2520), на чужой путь запрос не уходит.
func TestGetProfile_PathEscape(t *testing.T) {
	cases := []struct{ id, wantSuffix string }{
		{"ivan p@example.org", "/ocs/v2.php/profile/ivan%20p@example.org"},
		{"a\nb", "/ocs/v2.php/profile/a%0Ab"},
		{"50%", "/ocs/v2.php/profile/50%25"},
	}
	// Фикстура читается ДО серверов: profileFixture зовёт t.Fatalf, а его нельзя
	// вызывать из горутины обработчика httptest (только из горутины теста).
	notFound := profileFixture(t, "profile_not_found.json")
	for _, tc := range cases {
		var gotEscaped string
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotEscaped = r.URL.EscapedPath()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write(notFound)
		}))
		_, err := NewTalkClient(testCfg(ts.URL)).GetProfile(context.Background(), tc.id)
		ts.Close()
		var oe *OCSError
		if !errors.As(err, &oe) || oe.Code != http.StatusNotFound {
			t.Errorf("GetProfile(%q): got %v, want *OCSError{Code:404} (запрос дошёл до сервера)", tc.id, err)
		}
		if gotEscaped != tc.wantSuffix {
			t.Errorf("GetProfile(%q): escaped path got %q, want %q", tc.id, gotEscaped, tc.wantSuffix)
		}
	}
}
