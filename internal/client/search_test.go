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

// searchMessagesFixtureServer поднимает httptest-сервер, отдающий
// testdata/search_messages_page1.json при запросе БЕЗ cursor (или с неизвестным
// cursor) и testdata/search_messages_page2.json при cursor=2.
// Эмулирует серверную пагинацию talk-message provider'а (спека §6, §8);
// формат полей — как на живом сервере (cursor ЧИСЛОМ, 2026-09-09).
// Необязательный счётчик calls инкрементируется на каждый запрос.
func searchMessagesFixtureServer(t *testing.T, calls *int32) *httptest.Server {
	t.Helper()
	body1, err := os.ReadFile(filepath.Join("..", "..", "testdata", "search_messages_page1.json"))
	if err != nil {
		t.Fatalf("read page1 fixture: %v", err)
	}
	body2, err := os.ReadFile(filepath.Join("..", "..", "testdata", "search_messages_page2.json"))
	if err != nil {
		t.Fatalf("read page2 fixture: %v", err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls != nil {
			atomic.AddInt32(calls, 1)
		}
		w.Header().Set("Content-Type", "application/json")
		// cursor=2 → вторая страница (isPaginated=false).
		if r.URL.Query().Get("cursor") == "2" {
			_, _ = w.Write(body2)
			return
		}
		_, _ = w.Write(body1)
	}))
}

// TestSearchMessages_SinglePage проверяет контракт «по умолчанию ОДНА страница»
// (спека §6): даже если сервер сообщает isPaginated=true и возвращает cursor,
// при All=false клиент делает ровно один запрос. Заодно — базовый маппинг полей
// одной entry (title/subline/resourceUrl + все attributes).
func TestSearchMessages_SinglePage(t *testing.T) {
	var calls int32
	ts := searchMessagesFixtureServer(t, &calls)
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	res, err := c.SearchMessages(context.Background(), "hello", SearchMessagesOpts{})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("HTTP calls = %d, want 1 (All=false → одна страница, несмотря на isPaginated=true)", got)
	}
	// В page1 фикстуре 2 entries.
	if got, want := len(res), 2; got != want {
		t.Fatalf("len(res) = %d, want %d", got, want)
	}
	// Проверка маппинга первой entry.
	r := res[0]
	if r.Title != "Alice" {
		t.Errorf("Title: got %q, want %q", r.Title, "Alice")
	}
	if r.Subline != "hello world" {
		t.Errorf("Subline: got %q, want %q", r.Subline, "hello world")
	}
	if r.ResourceUrl != "https://nc.example.com/call/tok-team#message_100" {
		t.Errorf("ResourceUrl: got %q", r.ResourceUrl)
	}
	if r.Attributes.Conversation != "tok-team" {
		t.Errorf("Attributes.Conversation: got %q, want %q", r.Attributes.Conversation, "tok-team")
	}
	if r.Attributes.MessageId != 100 {
		t.Errorf("Attributes.MessageId: got %d, want 100", r.Attributes.MessageId)
	}
	if r.Attributes.ActorType != "users" {
		t.Errorf("Attributes.ActorType: got %q, want %q", r.Attributes.ActorType, "users")
	}
	if r.Attributes.ActorId != "alice" {
		t.Errorf("Attributes.ActorId: got %q, want %q", r.Attributes.ActorId, "alice")
	}
}

// TestSearchMessages_AllTwoPages проверяет пагинацию при All=true (спека §6):
// сервер отдаёт page1 (isPaginated=true, cursor=cursor-page2), затем page2
// (isPaginated=false). Клиент обязан собрать обе страницы — итого 2 запроса и
// 3 entry (2 из page1 + 1 из page2).
func TestSearchMessages_AllTwoPages(t *testing.T) {
	var calls int32
	ts := searchMessagesFixtureServer(t, &calls)
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	res, err := c.SearchMessages(context.Background(), "hello", SearchMessagesOpts{All: true})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("HTTP calls = %d, want 2 (page1 с cursor + page2 с isPaginated=false)", got)
	}
	// 2 entry из page1 + 1 entry из page2.
	if got, want := len(res), 3; got != want {
		t.Fatalf("len(res) = %d, want %d (2 из page1 + 1 из page2)", got, want)
	}
	// Последняя entry — из page2 (Carol, messageId=200).
	last := res[len(res)-1]
	if last.Title != "Carol" {
		t.Errorf("последняя entry Title: got %q, want %q (должна прийти из page2)", last.Title, "Carol")
	}
	if last.Attributes.MessageId != 200 {
		t.Errorf("последняя entry MessageId: got %d, want 200", last.Attributes.MessageId)
	}
}

// TestSearchMessages_AllCapFivePages проверяет cap 5 страниц (спека §6):
// сервер ВСЕГДА возвращает isPaginated=true и непустой cursor. Клиент обязан
// остановиться ровно на 5 запросах, не зацикливаясь.
func TestSearchMessages_AllCapFivePages(t *testing.T) {
	var calls int32
	// Программная page с isPaginated=true и непустым cursor — возвращается на
	// любой запрос независимо от cursor.
	entry := map[string]any{
		"title":       "Alice",
		"subline":     "msg",
		"resourceUrl": "https://nc.example.com/call/tok#message_1",
		"attributes": map[string]any{
			"conversation": "tok",
			"messageId":    "1", // живой сервер отдаёт messageId строкой (2026-09-09)
			"actorType":    "users",
			"actorId":      "alice",
			"timestamp":    "1752710400",
		},
	}
	body := ocsBody(t, 200, "OK", map[string]any{
		"isPaginated": true,
		"cursor":      5, // живой сервер отдаёт cursor ЧИСЛОМ (2026-09-09)
		"entries":     []map[string]any{entry},
	})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	res, err := c.SearchMessages(context.Background(), "hello", SearchMessagesOpts{All: true})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 5 {
		t.Errorf("HTTP calls = %d, want 5 (cap maxSearchMessagesPages=5, сервер не сообщает конец)", got)
	}
	if got, want := len(res), 5; got != want {
		t.Errorf("len(res) = %d, want %d (по одной entry с каждой из 5 страниц)", got, want)
	}
}

// TestSearchMessages_EmptyTermNoCall проверяет клиентский guard (спека §8):
// пустой term → ошибка "term не может быть пустым" ДО любого HTTP-вызова.
// Счётчик запросов обязан остаться 0.
func TestSearchMessages_EmptyTermNoCall(t *testing.T) {
	var calls int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(ocsBody(t, 200, "OK", map[string]any{"entries": []any{}}))
	}))
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	res, err := c.SearchMessages(context.Background(), "", SearchMessagesOpts{Limit: 5})
	if err == nil {
		t.Fatal(`SearchMessages(""): got nil error, want "term не может быть пустым"`)
	}
	if got := err.Error(); got != "term не может быть пустым" {
		t.Errorf("error: got %q, want %q", got, "term не может быть пустым")
	}
	if res != nil {
		t.Errorf("res: got %#v, want nil при клиентской ошибке", res)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("HTTP calls = %d, want 0 (пустой term отсекается на клиенте)", got)
	}
}

// TestSearchMessages_TimestampFromString проверяет нормализацию timestamp
// (спека §12) ОТДЕЛЬНЫМ кейсом: сервер присылает attributes.timestamp СТРОКОЙ
// "1752710400", а MessageResult.Attributes.Timestamp должен быть int64(1752710400).
func TestSearchMessages_TimestampFromString(t *testing.T) {
	var calls int32
	ts := searchMessagesFixtureServer(t, &calls)
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	res, err := c.SearchMessages(context.Background(), "hello", SearchMessagesOpts{})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(res) == 0 {
		t.Fatal("res пуст, хотим хотя бы одну entry для проверки timestamp")
	}
	// Первая entry в page1 имеет timestamp="1752710400" (строка в JSON).
	const want int64 = 1752710400
	if got := res[0].Attributes.Timestamp; got != want {
		t.Errorf("Timestamp: got %d (type %T), want %d — строка ответа должна нормализоваться в int64", got, got, want)
	}
}

// TestSearchMessages_LimitClampedAndPerson проверяет нормализацию limit
// (спека §6 search): при Limit=99 (>25) серверу уходит limit=25. Заодно — что
// From (actorId) маппится в query-параметр person, а на первом запросе cursor
// НЕ передаётся.
func TestSearchMessages_LimitClampedAndPerson(t *testing.T) {
	var gotLimit, gotPerson, gotTerm, gotCursor string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		gotLimit = q.Get("limit")
		gotPerson = q.Get("person")
		gotTerm = q.Get("term")
		gotCursor = q.Get("cursor")
		w.Header().Set("Content-Type", "application/json")
		// Возвращаем одну страницу с isPaginated=false, чтобы All не ушёл в
		// лишнюю пагинацию (нам важен первый запрос).
		_, _ = w.Write(ocsBody(t, 200, "OK", map[string]any{
			"isPaginated": false,
			"entries":     []any{},
		}))
	}))
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	if _, err := c.SearchMessages(context.Background(), "hello", SearchMessagesOpts{
		Limit: 99,
		From:  "alice",
		All:   true,
	}); err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if gotLimit != "25" {
		t.Errorf("server got limit=%q, want %q (Limit=99 → clamp 25)", gotLimit, "25")
	}
	if gotPerson != "alice" {
		t.Errorf("server got person=%q, want %q (From → person)", gotPerson, "alice")
	}
	if gotTerm != "hello" {
		t.Errorf("server got term=%q, want %q", gotTerm, "hello")
	}
	if gotCursor != "" {
		t.Errorf("на первом запросе cursor не должен передаваться, got %q", gotCursor)
	}
}

// TestSearchMessages_FromClientSideFilter — живой сервер (2026-09-09)
// ИГНОРИРУЕТ query-параметр person: с person=alice приходят entry чужих
// авторов. From обязан дополнительно фильтроваться на клиенте по
// attributes.actorId (как клиентский --from у chat show, спека §6).
func TestSearchMessages_FromClientSideFilter(t *testing.T) {
	entry := func(actor string) map[string]any {
		return map[string]any{
			"title":       "Autor " + actor,
			"subline":     "msg",
			"resourceUrl": "https://nc.example.com/call/tok#message_1",
			"attributes": map[string]any{
				"conversation": "tok",
				"messageId":    "1",
				"actorType":    "users",
				"actorId":      actor,
				"timestamp":    "1752710400",
			},
		}
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(ocsBody(t, 200, "OK", map[string]any{
			"isPaginated": false,
			"entries":     []map[string]any{entry("alice"), entry("bob"), entry("alice"), entry("carol")},
		}))
	}))
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	res, err := c.SearchMessages(context.Background(), "hello", SearchMessagesOpts{From: "alice"})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if got, want := len(res), 2; got != want {
		t.Fatalf("len(res) = %d, want %d: сервер прислал 2 entry alice + 2 чужих, клиентский фильтр From должен оставить только alice", got, want)
	}
	for i, r := range res {
		if r.Attributes.ActorId != "alice" {
			t.Errorf("res[%d].ActorId = %q, want %q (From=alice)", i, r.Attributes.ActorId, "alice")
		}
	}

	// Без From фильтр не действует: возвращаются все 4 entry.
	resAll, err := c.SearchMessages(context.Background(), "hello", SearchMessagesOpts{})
	if err != nil {
		t.Fatalf("SearchMessages без From: %v", err)
	}
	if got, want := len(resAll), 4; got != want {
		t.Errorf("len(resAll) без From = %d, want %d", got, want)
	}
}
