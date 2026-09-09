package cli

// handlers_rooms_test.go — table-driven тесты для roomsListHandler /
// roomsFindHandler / roomsSearchHandler (Task 4.5a).
//
// Spy-клиент (roomsSpyClient) записывает аргументы вызовов ListRooms /
// FindRooms / SearchRooms и возвращает преднастроенные результат/ошибку.
// Остальные методы TalkClient возвращают errMock (они rooms-тестам не нужны).
//
// Соглашение об empty-результате: пустой срез/nil → exit 0 с пустым stdout
// (поисковая семантика, спека §6).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stas-bool/nctalk-cli/internal/client"
)

// roomsSpyClient — заглушка TalkClient, фиксирующая аргументы вызовов
// ListRooms/FindRooms/SearchRooms для проверок в тестах.
type roomsSpyClient struct {
	listCalls   []roomsListCall
	findCalls   []roomsFindCall
	searchCalls []roomsSearchCall

	listResult   []client.Room
	listErr      error
	findResult   []client.Room
	findErr      error
	searchResult []client.ConversationResult
	searchErr    error
}

type roomsListCall struct {
	opts client.ListRoomsOpts
}

type roomsFindCall struct {
	query, actorId string
}

type roomsSearchCall struct {
	term  string
	limit int
}

func (m *roomsSpyClient) ListRooms(_ context.Context, opts client.ListRoomsOpts) ([]client.Room, error) {
	m.listCalls = append(m.listCalls, roomsListCall{opts: opts})
	return m.listResult, m.listErr
}

func (m *roomsSpyClient) FindRooms(_ context.Context, query, actorId string) ([]client.Room, error) {
	m.findCalls = append(m.findCalls, roomsFindCall{query: query, actorId: actorId})
	return m.findResult, m.findErr
}

func (m *roomsSpyClient) SearchRooms(_ context.Context, term string, limit int) ([]client.ConversationResult, error) {
	m.searchCalls = append(m.searchCalls, roomsSearchCall{term: term, limit: limit})
	return m.searchResult, m.searchErr
}

// Неиспользуемые в rooms-тестах методы — возвращают errMock (маркер).
func (m *roomsSpyClient) GetChat(_ context.Context, _ string, _ client.GetChatOpts) ([]client.Message, error) {
	return nil, errMock
}
func (m *roomsSpyClient) SendMessage(_ context.Context, _ string, _ client.SendMessageOpts) (int, error) {
	return 0, errMock
}
func (m *roomsSpyClient) EditMessage(_ context.Context, _ string, _ int, _ client.EditMessageOpts) (int, error) {
	return 0, errMock
}
func (m *roomsSpyClient) GetReactions(_ context.Context, _ string, _ int) (map[string][]client.ReactionActor, error) {
	return nil, errMock
}
func (m *roomsSpyClient) SearchMessages(_ context.Context, _ string, _ client.SearchMessagesOpts) ([]client.MessageResult, error) {
	return nil, errMock
}

// compile-time гарантия: roomsSpyClient реализует TalkClient.
var _ TalkClient = (*roomsSpyClient)(nil)

// newRoomsDeps собирает Deps с переданным клиентом и буферами для Stdout/Stderr.
// Now зафиксирован — тесты не должны зависеть от реального времени.
func newRoomsDeps(c TalkClient) Deps {
	return Deps{
		Client: c,
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
		Now:    func() time.Time { return time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC) },
	}
}

// roomTypePtr — хелпер для построения *RoomType в ожидаемых opts тестов.
func roomTypePtr(n int) *client.RoomType {
	t := client.RoomType(n)
	return &t
}

// equalListOpts сравнивает ListRoomsOpts с учётом *RoomType (nil-безопасно).
func equalListOpts(a, b client.ListRoomsOpts) bool {
	if a.UnreadOnly != b.UnreadOnly || a.IncludeFormer != b.IncludeFormer {
		return false
	}
	if (a.Type == nil) != (b.Type == nil) {
		return false
	}
	if a.Type != nil && *a.Type != *b.Type {
		return false
	}
	return true
}

// -----------------------------------------------------------------------------
// rooms list
// -----------------------------------------------------------------------------

// TestRoomsListHandlerFlags — флаги корректно парсятся и доходят до ListRooms
// в виде ListRoomsOpts.
func TestRoomsListHandlerFlags(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantOpts client.ListRoomsOpts
	}{
		{
			"нет флагов — дефолтные opts",
			[]string{},
			client.ListRoomsOpts{Type: nil, UnreadOnly: false, IncludeFormer: false},
		},
		{
			"--type 2 → *RoomType(2)",
			[]string{"--type", "2"},
			client.ListRoomsOpts{Type: roomTypePtr(2)},
		},
		{
			"--type=3 → *RoomType(3) (форма со знаком равенства)",
			[]string{"--type=3"},
			client.ListRoomsOpts{Type: roomTypePtr(3)},
		},
		{
			"--unread → UnreadOnly=true",
			[]string{"--unread"},
			client.ListRoomsOpts{UnreadOnly: true},
		},
		{
			"--include-former → IncludeFormer=true",
			[]string{"--include-former"},
			client.ListRoomsOpts{IncludeFormer: true},
		},
		{
			"комбинация --type 1 --unread --include-former",
			[]string{"--type", "1", "--unread", "--include-former"},
			client.ListRoomsOpts{Type: roomTypePtr(1), UnreadOnly: true, IncludeFormer: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := &roomsSpyClient{}
			deps := newRoomsDeps(spy)

			ee := roomsListHandler(context.Background(), deps, tc.args, false)
			if ee.Code != ExitOK {
				t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
			}
			if len(spy.listCalls) != 1 {
				t.Fatalf("ListRooms вызовов: got %d, want 1", len(spy.listCalls))
			}
			got := spy.listCalls[0].opts
			if !equalListOpts(got, tc.wantOpts) {
				t.Fatalf("ListRooms opts:\n got  %+v\n want %+v", got, tc.wantOpts)
			}
		})
	}
}

// TestRoomsListHandlerInvalidType — нечисловой --type → ExitGeneric без вызова клиента.
func TestRoomsListHandlerInvalidType(t *testing.T) {
	spy := &roomsSpyClient{}
	deps := newRoomsDeps(spy)

	ee := roomsListHandler(context.Background(), deps, []string{"--type", "abc"}, false)
	if ee.Code != ExitGeneric {
		t.Fatalf("code: got %d, want %d", ee.Code, ExitGeneric)
	}
	if len(spy.listCalls) != 0 {
		t.Fatalf("ListRooms не должен вызываться при ошибке парсинга --type; got %d calls", len(spy.listCalls))
	}
}

// TestRoomsListHandlerTypeNoValue — --type без значения (последний arg) → ExitGeneric.
func TestRoomsListHandlerTypeNoValue(t *testing.T) {
	spy := &roomsSpyClient{}
	deps := newRoomsDeps(spy)

	ee := roomsListHandler(context.Background(), deps, []string{"--type"}, false)
	if ee.Code != ExitGeneric {
		t.Fatalf("code: got %d, want %d", ee.Code, ExitGeneric)
	}
	if len(spy.listCalls) != 0 {
		t.Fatalf("ListRooms не должен вызываться; got %d calls", len(spy.listCalls))
	}
}

// TestRoomsListHandlerUnknownFlag — неизвестный --flag → ExitGeneric.
func TestRoomsListHandlerUnknownFlag(t *testing.T) {
	spy := &roomsSpyClient{}
	deps := newRoomsDeps(spy)

	ee := roomsListHandler(context.Background(), deps, []string{"--no-such"}, false)
	if ee.Code != ExitGeneric {
		t.Fatalf("code: got %d, want %d", ee.Code, ExitGeneric)
	}
	if len(spy.listCalls) != 0 {
		t.Fatalf("ListRooms не должен вызываться; got %d calls", len(spy.listCalls))
	}
}

// TestRoomsListHandlerEmptyResult — пустой список → exit 0 с пустым stdout
// (поисковая семантика; заголовок таблицы НЕ печатается).
func TestRoomsListHandlerEmptyResult(t *testing.T) {
	spy := &roomsSpyClient{listResult: []client.Room{}}
	deps := newRoomsDeps(spy)

	ee := roomsListHandler(context.Background(), deps, nil, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (пустой результат → exit 0)", ee.Code, ExitOK)
	}
	if buf := deps.Stdout.(*bytes.Buffer); buf.Len() != 0 {
		t.Fatalf("stdout: got %q, want пусто (поисковая семантика)", buf.String())
	}
}

// TestRoomsListHandlerNilResult — nil-список тоже трактуется как «пусто».
func TestRoomsListHandlerNilResult(t *testing.T) {
	spy := &roomsSpyClient{listResult: nil}
	deps := newRoomsDeps(spy)

	ee := roomsListHandler(context.Background(), deps, nil, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d", ee.Code, ExitOK)
	}
	if buf := deps.Stdout.(*bytes.Buffer); buf.Len() != 0 {
		t.Fatalf("stdout: got %q, want пусто", buf.String())
	}
}

// TestRoomsListHandlerClientError — ошибка от клиента → ExitGeneric, ошибка
// обёрнута как есть (errors.Is проходит по Unwrap).
func TestRoomsListHandlerClientError(t *testing.T) {
	sentinel := errors.New("list network boom")
	spy := &roomsSpyClient{listErr: sentinel}
	deps := newRoomsDeps(spy)

	ee := roomsListHandler(context.Background(), deps, nil, false)
	if ee.Code != ExitGeneric {
		t.Fatalf("code: got %d, want %d", ee.Code, ExitGeneric)
	}
	if !errors.Is(ee.Err, sentinel) {
		t.Fatalf("err: got %v, want wraps %v", ee.Err, sentinel)
	}
}

// TestRoomsListHandlerTable — непустой результат без --json → табличный вывод
// с заголовком и строками.
func TestRoomsListHandlerTable(t *testing.T) {
	spy := &roomsSpyClient{
		listResult: []client.Room{
			{Type: 2, Token: "TOK1", DisplayName: "Чат 1", UnreadMessages: 3, ActorId: "u-alice"},
		},
	}
	deps := newRoomsDeps(spy)

	ee := roomsListHandler(context.Background(), deps, nil, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	out := deps.Stdout.(*bytes.Buffer).String()
	if !strings.Contains(out, "TOKEN") {
		t.Fatalf("stdout: ожидается заголовок таблицы (contains 'TOKEN'); got %q", out)
	}
	if !strings.Contains(out, "TOK1") {
		t.Fatalf("stdout: ожидается строка с 'TOK1'; got %q", out)
	}
}

// TestRoomsListHandlerJSON — jsonOut=true → stdout валидный JSON, обратный
// парсинг json.Unmarshal даёт исходные данные.
func TestRoomsListHandlerJSON(t *testing.T) {
	spy := &roomsSpyClient{
		listResult: []client.Room{
			{Type: 2, Token: "TOK1", DisplayName: "Чат 1", UnreadMessages: 3, ActorId: "u-alice"},
		},
	}
	deps := newRoomsDeps(spy)

	ee := roomsListHandler(context.Background(), deps, nil, true)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	out := deps.Stdout.(*bytes.Buffer).Bytes()
	var got []client.Room
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("stdout не валидный JSON: %v; raw=%s", err, out)
	}
	if len(got) != 1 || got[0].Token != "TOK1" {
		t.Fatalf("JSON распарсился, но данные не совпадают: %+v", got)
	}
}

// -----------------------------------------------------------------------------
// rooms find
// -----------------------------------------------------------------------------

// TestRoomsFindHandlerArgs — query (первый позиционный) и --user корректно
// разбираются и передаются в FindRooms.
func TestRoomsFindHandlerArgs(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantQuery   string
		wantActorId string
	}{
		{"только query", []string{"alice"}, "alice", ""},
		{"query + --user", []string{"alice", "--user", "u-alice"}, "alice", "u-alice"},
		{"--user перед query", []string{"--user", "u-bob", "bob"}, "bob", "u-bob"},
		{"--user=value (со знаком равенства)", []string{"alice", "--user=u-alice"}, "alice", "u-alice"},
		{"только --user, без query → ошибка (см. отдельный тест)", nil, "", ""}, // вырожденный маркер; реальное поведение проверяется в TestRoomsFindHandlerNoQuery
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.args == nil {
				t.Skip()
			}
			spy := &roomsSpyClient{}
			deps := newRoomsDeps(spy)

			ee := roomsFindHandler(context.Background(), deps, tc.args, false)
			if ee.Code != ExitOK {
				t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
			}
			if len(spy.findCalls) != 1 {
				t.Fatalf("FindRooms вызовов: got %d, want 1", len(spy.findCalls))
			}
			got := spy.findCalls[0]
			if got.query != tc.wantQuery || got.actorId != tc.wantActorId {
				t.Fatalf("FindRooms args: got (query=%q actorId=%q), want (query=%q actorId=%q)",
					got.query, got.actorId, tc.wantQuery, tc.wantActorId)
			}
		})
	}
}

// TestRoomsFindHandlerNoQuery — нет позиционного запроса → ExitGeneric без вызова.
func TestRoomsFindHandlerNoQuery(t *testing.T) {
	spy := &roomsSpyClient{}
	deps := newRoomsDeps(spy)

	ee := roomsFindHandler(context.Background(), deps, nil, false)
	if ee.Code != ExitGeneric {
		t.Fatalf("code: got %d, want %d", ee.Code, ExitGeneric)
	}
	if len(spy.findCalls) != 0 {
		t.Fatalf("FindRooms не должен вызываться без query; got %d calls", len(spy.findCalls))
	}
}

// TestRoomsFindHandlerUserNoValue — --user без значения → ExitGeneric.
func TestRoomsFindHandlerUserNoValue(t *testing.T) {
	spy := &roomsSpyClient{}
	deps := newRoomsDeps(spy)

	ee := roomsFindHandler(context.Background(), deps, []string{"alice", "--user"}, false)
	if ee.Code != ExitGeneric {
		t.Fatalf("code: got %d, want %d", ee.Code, ExitGeneric)
	}
	if len(spy.findCalls) != 0 {
		t.Fatalf("FindRooms не должен вызываться при ошибке парсинга --user; got %d calls", len(spy.findCalls))
	}
}

// TestRoomsFindHandlerEmptyResult — пустой результат → exit 0 с пустым stdout.
func TestRoomsFindHandlerEmptyResult(t *testing.T) {
	spy := &roomsSpyClient{findResult: []client.Room{}}
	deps := newRoomsDeps(spy)

	ee := roomsFindHandler(context.Background(), deps, []string{"nothing"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (пустой → exit 0)", ee.Code, ExitOK)
	}
	if buf := deps.Stdout.(*bytes.Buffer); buf.Len() != 0 {
		t.Fatalf("stdout: got %q, want пусто", buf.String())
	}
}

// TestRoomsFindHandlerClientError — ошибка клиента → ExitGeneric.
func TestRoomsFindHandlerClientError(t *testing.T) {
	sentinel := errors.New("find boom")
	spy := &roomsSpyClient{findErr: sentinel}
	deps := newRoomsDeps(spy)

	ee := roomsFindHandler(context.Background(), deps, []string{"x"}, false)
	if ee.Code != ExitGeneric {
		t.Fatalf("code: got %d, want %d", ee.Code, ExitGeneric)
	}
	if !errors.Is(ee.Err, sentinel) {
		t.Fatalf("err: got %v, want wraps %v", ee.Err, sentinel)
	}
}

// TestRoomsFindHandlerJSON — jsonOut=true → валидный JSON, обратный парсинг.
func TestRoomsFindHandlerJSON(t *testing.T) {
	spy := &roomsSpyClient{
		findResult: []client.Room{
			{Type: 1, Token: "T1", DisplayName: "Alice", ActorId: "u-alice"},
		},
	}
	deps := newRoomsDeps(spy)

	ee := roomsFindHandler(context.Background(), deps, []string{"alice"}, true)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	out := deps.Stdout.(*bytes.Buffer).Bytes()
	var got []client.Room
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("stdout не валидный JSON: %v; raw=%s", err, out)
	}
	if len(got) != 1 || got[0].Token != "T1" {
		t.Fatalf("JSON данные не совпадают: %+v", got)
	}
}

// -----------------------------------------------------------------------------
// rooms search
// -----------------------------------------------------------------------------

// TestRoomsSearchHandlerTerm — term (первый позиционный) и limit=0 доходят до
// SearchRooms.
func TestRoomsSearchHandlerTerm(t *testing.T) {
	spy := &roomsSpyClient{}
	deps := newRoomsDeps(spy)

	ee := roomsSearchHandler(context.Background(), deps, []string{"xy"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	if len(spy.searchCalls) != 1 {
		t.Fatalf("SearchRooms вызовов: got %d, want 1", len(spy.searchCalls))
	}
	got := spy.searchCalls[0]
	if got.term != "xy" {
		t.Fatalf("SearchRooms term: got %q, want %q", got.term, "xy")
	}
	if got.limit != 0 {
		t.Fatalf("SearchRooms limit: got %d, want 0 (серверный дефолт)", got.limit)
	}
}

// TestRoomsSearchHandlerNoTerm — нет term → ExitGeneric без вызова.
func TestRoomsSearchHandlerNoTerm(t *testing.T) {
	spy := &roomsSpyClient{}
	deps := newRoomsDeps(spy)

	ee := roomsSearchHandler(context.Background(), deps, nil, false)
	if ee.Code != ExitGeneric {
		t.Fatalf("code: got %d, want %d", ee.Code, ExitGeneric)
	}
	if len(spy.searchCalls) != 0 {
		t.Fatalf("SearchRooms не должен вызываться без term; got %d calls", len(spy.searchCalls))
	}
}

// TestRoomsSearchHandlerUnknownFlag — rooms search не поддерживает флагов;
// любой --flag → ExitGeneric.
func TestRoomsSearchHandlerUnknownFlag(t *testing.T) {
	spy := &roomsSpyClient{}
	deps := newRoomsDeps(spy)

	ee := roomsSearchHandler(context.Background(), deps, []string{"xy", "--limit"}, false)
	if ee.Code != ExitGeneric {
		t.Fatalf("code: got %d, want %d", ee.Code, ExitGeneric)
	}
	if len(spy.searchCalls) != 0 {
		t.Fatalf("SearchRooms не должен вызываться; got %d calls", len(spy.searchCalls))
	}
}

// TestRoomsSearchHandlerEmptyResult — пустой результат → exit 0 с пустым stdout.
func TestRoomsSearchHandlerEmptyResult(t *testing.T) {
	spy := &roomsSpyClient{searchResult: []client.ConversationResult{}}
	deps := newRoomsDeps(spy)

	ee := roomsSearchHandler(context.Background(), deps, []string{"xy"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (пустой → exit 0)", ee.Code, ExitOK)
	}
	if buf := deps.Stdout.(*bytes.Buffer); buf.Len() != 0 {
		t.Fatalf("stdout: got %q, want пусто", buf.String())
	}
}

// TestRoomsSearchHandlerClientError — ошибка клиента → ExitGeneric.
func TestRoomsSearchHandlerClientError(t *testing.T) {
	sentinel := errors.New("search boom")
	spy := &roomsSpyClient{searchErr: sentinel}
	deps := newRoomsDeps(spy)

	ee := roomsSearchHandler(context.Background(), deps, []string{"x"}, false)
	if ee.Code != ExitGeneric {
		t.Fatalf("code: got %d, want %d", ee.Code, ExitGeneric)
	}
	if !errors.Is(ee.Err, sentinel) {
		t.Fatalf("err: got %v, want wraps %v", ee.Err, sentinel)
	}
}

// TestRoomsSearchHandlerJSON — jsonOut=true → валидный JSON []ConversationResult.
// У типа нет json-тегов, поэтому поля маршалятся как PascalCase и корректно
// читаются обратно.
func TestRoomsSearchHandlerJSON(t *testing.T) {
	spy := &roomsSpyClient{
		searchResult: []client.ConversationResult{
			{Title: "General", Token: "TOK-GEN"},
		},
	}
	deps := newRoomsDeps(spy)

	ee := roomsSearchHandler(context.Background(), deps, []string{"gen"}, true)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	out := deps.Stdout.(*bytes.Buffer).Bytes()
	var got []client.ConversationResult
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("stdout не валидный JSON: %v; raw=%s", err, out)
	}
	if len(got) != 1 || got[0].Token != "TOK-GEN" || got[0].Title != "General" {
		t.Fatalf("JSON данные не совпадают: %+v", got)
	}
}

// -----------------------------------------------------------------------------
// Интеграция через Run (end-to-end: парсинг args → handler → client)
// -----------------------------------------------------------------------------

// TestRoomsListViaRun — `rooms list --type 2` проходит роутинг, доходит до
// roomsListHandler и вызывает ListRooms с ожидаемыми opts. Глобальный --json
// обрабатывается в Run.
func TestRoomsListViaRun(t *testing.T) {
	spy := &roomsSpyClient{
		listResult: []client.Room{{Type: 2, Token: "TOK", DisplayName: "X", ActorId: "u-x"}},
	}
	deps := newRoomsDeps(spy)

	code := Run([]string{"rooms", "list", "--type", "2"}, deps)
	if code != ExitOK {
		t.Fatalf("Run code: got %d, want %d", code, ExitOK)
	}
	if len(spy.listCalls) != 1 {
		t.Fatalf("ListRooms вызовов: got %d, want 1", len(spy.listCalls))
	}
	opts := spy.listCalls[0].opts
	if opts.Type == nil || *opts.Type != 2 {
		t.Fatalf("ListRooms opts.Type: got %+v, want *2", opts.Type)
	}
	if deps.Stdout.(*bytes.Buffer).Len() == 0 {
		t.Fatalf("stdout: ожидался непустой вывод таблицы")
	}
}

// TestRoomsListViaRunJSON — `rooms list --json` через Run: --json извлечён до
// роутинга, jsonOut=true, stdout валидный JSON.
func TestRoomsListViaRunJSON(t *testing.T) {
	spy := &roomsSpyClient{
		listResult: []client.Room{{Type: 1, Token: "J1", DisplayName: "Json Room", ActorId: "u-j"}},
	}
	deps := newRoomsDeps(spy)

	code := Run([]string{"rooms", "list", "--json"}, deps)
	if code != ExitOK {
		t.Fatalf("Run code: got %d, want %d", code, ExitOK)
	}
	out := deps.Stdout.(*bytes.Buffer).Bytes()
	var got []client.Room
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("stdout не JSON: %v; raw=%s", err, out)
	}
	if len(got) != 1 || got[0].Token != "J1" {
		t.Fatalf("JSON данные не совпадают: %+v", got)
	}
}

// TestRoomsFindViaRun — `rooms find alice --user u-alice` через Run.
func TestRoomsFindViaRun(t *testing.T) {
	spy := &roomsSpyClient{}
	deps := newRoomsDeps(spy)

	code := Run([]string{"rooms", "find", "alice", "--user", "u-alice"}, deps)
	if code != ExitOK {
		t.Fatalf("Run code: got %d, want %d", code, ExitOK)
	}
	if len(spy.findCalls) != 1 {
		t.Fatalf("FindRooms вызовов: got %d, want 1", len(spy.findCalls))
	}
	got := spy.findCalls[0]
	if got.query != "alice" || got.actorId != "u-alice" {
		t.Fatalf("FindRooms args: got (query=%q actorId=%q), want (alice, u-alice)", got.query, got.actorId)
	}
}

// TestRoomsSearchViaRun — `rooms search xy` через Run.
func TestRoomsSearchViaRun(t *testing.T) {
	spy := &roomsSpyClient{}
	deps := newRoomsDeps(spy)

	code := Run([]string{"rooms", "search", "xy"}, deps)
	if code != ExitOK {
		t.Fatalf("Run code: got %d, want %d", code, ExitOK)
	}
	if len(spy.searchCalls) != 1 {
		t.Fatalf("SearchRooms вызовов: got %d, want 1", len(spy.searchCalls))
	}
	if got := spy.searchCalls[0]; got.term != "xy" || got.limit != 0 {
		t.Fatalf("SearchRooms args: got (term=%q limit=%d), want (xy, 0)", got.term, got.limit)
	}
}
