package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// searchTestServer поднимает httptest-сервер, отдающий содержимое
// testdata/search_conversations.json на любой запрос. Фикстура лежит в корневом
// testdata/ (рядом с go.mod), поэтому из директории пакета путь —
// ../../testdata/search_conversations.json.
func searchTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "search_conversations.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
}

// searchEmptyEntriesBody — OCS-конверт с пустым entries (для сценария «ничего не
// найдено»). Литерал вместо файла: пустой результат генерируется программно.
const searchEmptyEntriesBody = `{"ocs":{"meta":{"status":"ok","statuscode":200,"message":"OK"},"data":{"isPaginated":true,"entries":[]}}}`

// TestSearchRooms_ParsesEntries проверяет ключевой контракт разбора (спека §8):
// из каждого entry извлекается title → ConversationResult.Title, а из
// attributes.conversation — токен → ConversationResult.Token. Несколько entries
// должны дать срез соответствующей длины.
func TestSearchRooms_ParsesEntries(t *testing.T) {
	ts := searchTestServer(t)
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	res, err := c.SearchRooms(context.Background(), "room", 0)
	if err != nil {
		t.Fatalf("SearchRooms: %v", err)
	}
	// В фикстуре 3 entries.
	if got, want := len(res), 3; got != want {
		t.Fatalf("len(res) = %d, want %d (несколько entries → срез той же длины)", got, want)
	}

	// Ожидаемый маппинг token → title (проверка извлечения token именно из
	// attributes.conversation, а не из другого поля).
	want := map[string]string{
		"tok-team":   "Team Chat",
		"tok-public": "Public Room",
		"tok-projx":  "Project X",
	}
	for _, r := range res {
		wantTitle, ok := want[r.Token]
		if !ok {
			t.Errorf("неожидаемый token %q (нет в ожидаемом наборе %v)", r.Token, want)
			continue
		}
		if r.Title != wantTitle {
			t.Errorf("token %q: Title = %q, want %q", r.Token, r.Title, wantTitle)
		}
	}
}

// TestSearchRooms_EmptyTermNoCall проверяет клиентский guard (спека §8: мин
// длина term = 1): пустой term → ошибка "term не может быть пустым" ДО любого
// HTTP-вызова. Счётчик запросов на тест-сервере обязан остаться 0.
func TestSearchRooms_EmptyTermNoCall(t *testing.T) {
	var calls int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(searchEmptyEntriesBody))
	}))
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	res, err := c.SearchRooms(context.Background(), "", 5)
	if err == nil {
		t.Fatal(`SearchRooms(""): got nil error, want "term не может быть пустым"`)
	}
	if got := err.Error(); got != "term не может быть пустым" {
		t.Errorf("error: got %q, want %q", got, "term не может быть пустым")
	}
	if res != nil {
		t.Errorf("res: got %#v, want nil при клиентской ошибке", res)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("HTTP calls = %d, want 0 (пустой term отсекается на клиенте без сетевого вызова)", got)
	}
}

// TestSearchRooms_EmptyEntries проверяет контракт «пустые entries → пустой срез
// (НЕ nil), nil error».
func TestSearchRooms_EmptyEntries(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(searchEmptyEntriesBody))
	}))
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	res, err := c.SearchRooms(context.Background(), "zzz-not-found", 0)
	if err != nil {
		t.Fatalf("SearchRooms: %v", err)
	}
	if res == nil {
		t.Fatal("res = nil, want non-nil empty slice")
	}
	if len(res) != 0 {
		t.Errorf("len(res) = %d, want 0 (0 entries → пустой срез)", len(res))
	}
}

// TestSearchRooms_QueryParamsSent проверяет, что term и limit реально доходят до
// сервера как query-параметры. Это страховка от регрессии: общий doOCS
// (client.go) не поддерживает RawQuery (path.Join кодирует '?' в %3F), поэтому
// SearchRooms использует локальный doSearchOCSGet, который выставляет RawQuery
// отдельно. Если бы запрос уходил через doOCS — сервер не получил бы term и
// кейс бы провалился.
func TestSearchRooms_QueryParamsSent(t *testing.T) {
	var gotPath, gotTerm, gotLimit string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotTerm = r.URL.Query().Get("term")
		gotLimit = r.URL.Query().Get("limit")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(searchEmptyEntriesBody))
	}))
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	if _, err := c.SearchRooms(context.Background(), "bob", 7); err != nil {
		t.Fatalf("SearchRooms: %v", err)
	}
	if gotTerm != "bob" {
		t.Errorf("server got term=%q, want %q (query не доходит — проверь doSearchOCSGet)", gotTerm, "bob")
	}
	if gotLimit != "7" {
		t.Errorf("server got limit=%q, want %q", gotLimit, "7")
	}
	// Путь НЕ должен содержать '?' или %3F — это признак бага, когда query
	// «схлопнут» в path через path.Join.
	if strings.Contains(gotPath, "?") || strings.Contains(gotPath, "%3F") {
		t.Errorf("server got path=%q (содержит '?' или %%3F — query ушли в path, не в RawQuery)", gotPath)
	}
}

// TestSearchRooms_LimitOmittedWhenNonPositive проверяет, что при limit<=0
// параметр limit НЕ передаётся (серверный дефолт), а term передаётся.
func TestSearchRooms_LimitOmittedWhenNonPositive(t *testing.T) {
	for _, limit := range []int{0, -1} {
		var sawLimit bool
		var gotTerm string
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, sawLimit = r.URL.Query()["limit"]
			gotTerm = r.URL.Query().Get("term")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(searchEmptyEntriesBody))
		}))
		c := NewTalkClient(testCfg(ts.URL))
		if _, err := c.SearchRooms(context.Background(), "bob", limit); err != nil {
			ts.Close()
			t.Fatalf("SearchRooms limit=%d: %v", limit, err)
		}
		ts.Close()
		if sawLimit {
			t.Errorf("limit=%d: сервер получил параметр limit, не должен был", limit)
		}
		if gotTerm != "bob" {
			t.Errorf("limit=%d: term got %q, want %q", limit, gotTerm, "bob")
		}
	}
}
