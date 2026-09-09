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
	"net/http"
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

	participantsCalls  []participantsCall
	participantsResult []client.Participant
	participantsErr    error
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

// Spy-поля для rooms participants (спека-дельта 2026-09-09 §6):
// GetParticipants записывает token каждого вызова и возвращает
// преднастроенный результат/ошибку.
type participantsCall struct{ token string }

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

func (m *roomsSpyClient) GetParticipants(_ context.Context, token string) ([]client.Participant, error) {
	m.participantsCalls = append(m.participantsCalls, participantsCall{token: token})
	return m.participantsResult, m.participantsErr
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

// -----------------------------------------------------------------------------
// rooms participants (спека-дельта 2026-09-09)
// -----------------------------------------------------------------------------

// participantsFixture — перемешанный серверный порядок: покрывает сортировку
// роль→имя, fallback имени (пустой displayName → actorId), все тексты ролей
// включая неизвестное число (9), онлайн да/нет, гостя с пустым именем.
func participantsFixture() []client.Participant {
	return []client.Participant{
		{ActorType: "users", ActorId: "vera.t", DisplayName: "Вера Тимофеева", ParticipantType: 3, SessionIds: []string{"sess-1"}, LastPing: 1757401200},
		{ActorType: "users", ActorId: "anna.s", DisplayName: "Анна Смирнова", ParticipantType: 1, SessionIds: []string{}, LastPing: 1757401100},
		{ActorType: "users", ActorId: "boris.k", DisplayName: "Борис Крылов", ParticipantType: 2, SessionIds: []string{"sess-2", "sess-3"}, LastPing: 1757401300},
		{ActorType: "guests", ActorId: "guest::anon-9", DisplayName: "", ParticipantType: 3, SessionIds: nil},
		{ActorType: "guests", ActorId: "guest::anon-1", DisplayName: "", ParticipantType: 4, SessionIds: nil},
		{ActorType: "users", ActorId: "eva.l", DisplayName: "Ева Ссылкина", ParticipantType: 6, SessionIds: nil},
		{ActorType: "users", ActorId: "zed.u", DisplayName: "Зиновий", ParticipantType: 9, SessionIds: nil},
	}
}

// TestRoomsParticipantsHandlerText — текстовый вывод: заголовок капсом,
// сортировка роль→имя (fallback имени ДО сортировки — латиница раньше
// кириллицы: guest::anon-9 перед Верой в одной роли), тексты ролей
// 1–6 и неизвестного числа, онлайн да/нет.
func TestRoomsParticipantsHandlerText(t *testing.T) {
	spy := &roomsSpyClient{participantsResult: participantsFixture()}
	deps := newRoomsDeps(spy)

	ee := roomsParticipantsHandler(context.Background(), deps, []string{"tok-abc"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	if len(spy.participantsCalls) != 1 || spy.participantsCalls[0].token != "tok-abc" {
		t.Fatalf("GetParticipants calls: got %+v, want 1 вызов с tok-abc", spy.participantsCalls)
	}

	out := deps.Stdout.(*bytes.Buffer).String()
	lines := strings.Split(out, "\n")
	if len(lines) != 9 { // заголовок + 7 строк + хвост после финального \n
		t.Fatalf("строк вывода: got %d, want 9\nвывод=\n%s", len(lines), out)
	}
	// Заголовок капсом, 4 колонки.
	for _, h := range []string{"ИМЯ", "РОЛЬ", "ОНЛАЙН", "ID"} {
		if !strings.Contains(lines[0], h) {
			t.Errorf("заголовок не содержит %q\nвывод=\n%s", h, out)
		}
	}
	// Ожидаемый порядок строк (роль asc → итоговое имя case-insensitive asc):
	// Анна(1) → Борис(2) → guest::anon-9(3, латиница) → Вера(3) →
	// guest::anon-1(4, гость без имени) → Ева(6) → Зиновий(9 → печатается «9»).
	wantRows := []struct{ name, role, online, id string }{
		{"Анна Смирнова", "владелец", "нет", "anna.s"},
		{"Борис Крылов", "модератор", "да", "boris.k"},
		{"guest::anon-9", "участник", "нет", "guest::anon-9"},
		{"Вера Тимофеева", "участник", "да", "vera.t"},
		{"guest::anon-1", "гость", "нет", "guest::anon-1"},
		{"Ева Ссылкина", "гость-модератор", "нет", "eva.l"},
		{"Зиновий", "9", "нет", "zed.u"},
	}
	for i, w := range wantRows {
		line := lines[i+1]
		for _, part := range []string{w.name, w.role, w.online, w.id} {
			if !strings.Contains(line, part) {
				t.Errorf("строка %d: не содержит %q\nстрока=%q\nвывод=\n%s", i+1, part, line, out)
			}
		}
	}
}

// TestRoomsParticipantsHandlerRole5 — значение participantType=5 из
// документации (спека §3): текст «по ссылке».
func TestRoomsParticipantsHandlerRole5(t *testing.T) {
	spy := &roomsSpyClient{participantsResult: []client.Participant{
		{ActorType: "users", ActorId: "link.u", DisplayName: "Линк", ParticipantType: 5, SessionIds: nil},
	}}
	deps := newRoomsDeps(spy)

	ee := roomsParticipantsHandler(context.Background(), deps, []string{"tok"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	if out := deps.Stdout.(*bytes.Buffer).String(); !strings.Contains(out, "по ссылке") {
		t.Fatalf("stdout: должен содержать роль \"по ссылке\"; got %q", out)
	}
}

// TestRoomsParticipantsHandlerJSON — --json: отсортированный массив
// канонических Participant; sessionIds печатается как [], не null.
func TestRoomsParticipantsHandlerJSON(t *testing.T) {
	spy := &roomsSpyClient{participantsResult: []client.Participant{
		{ActorType: "users", ActorId: "u-b", DisplayName: "Борис", ParticipantType: 2, SessionIds: []string{}},
		{ActorType: "users", ActorId: "u-a", DisplayName: "Анна", ParticipantType: 1, SessionIds: []string{"s1"}},
	}}
	deps := newRoomsDeps(spy)

	ee := roomsParticipantsHandler(context.Background(), deps, []string{"tok"}, true)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	raw := deps.Stdout.(*bytes.Buffer).String()
	var got []client.Participant
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("stdout не валидный JSON: %v; raw=%s", err, raw)
	}
	// Сортировка применена и к --json: владелец (1) первым.
	if len(got) != 2 || got[0].ActorId != "u-a" || got[0].ParticipantType != 1 {
		t.Fatalf("JSON порядок: got %+v, want u-a (PT=1) первым", got)
	}
	// sessionIds офлайн-участника — [] (не null).
	if !strings.Contains(raw, `"sessionIds": []`) {
		t.Errorf("JSON: ожидается \"sessionIds\": [] (не null); raw=%s", raw)
	}
	if strings.Contains(raw, `"sessionIds": null`) {
		t.Errorf("JSON: sessionIds не должен быть null; raw=%s", raw)
	}
}

// TestRoomsParticipantsHandlerEmpty — пустой список → пустой stdout, exit 0
// (поисковая семантика rooms list; одинаково для текста и --json).
func TestRoomsParticipantsHandlerEmpty(t *testing.T) {
	for _, jsonOut := range []bool{false, true} {
		spy := &roomsSpyClient{participantsResult: []client.Participant{}}
		deps := newRoomsDeps(spy)

		ee := roomsParticipantsHandler(context.Background(), deps, []string{"tok"}, jsonOut)
		if ee.Code != ExitOK {
			t.Fatalf("jsonOut=%v: code: got %d, want %d", jsonOut, ee.Code, ExitOK)
		}
		if buf := deps.Stdout.(*bytes.Buffer); buf.Len() != 0 {
			t.Fatalf("jsonOut=%v: stdout: got %q, want пусто", jsonOut, buf.String())
		}
	}
}

// TestRoomsParticipantsHandlerNameResolution — --name: 1 совпадение →
// exit 0 и GetParticipants с резолвнутым token; >1 → exit 3 (кандидаты в
// stderr, GetParticipants не звался); 0 → exit 2 (тоже без вызова).
func TestRoomsParticipantsHandlerNameResolution(t *testing.T) {
	cases := []struct {
		name       string
		findResult []client.Room
		wantExit   int
		wantToken  string // ожидаемый token GetParticipants ("" — вызова не было)
		wantCalls  int
	}{
		{
			name:       "--name 1 совпадение → 0",
			findResult: []client.Room{{Type: 2, Token: "tok-1", DisplayName: "Команда"}},
			wantExit:   ExitOK,
			wantToken:  "tok-1",
			wantCalls:  1,
		},
		{
			name: "--name >1 совпадений → 3, GetParticipants не звался",
			findResult: []client.Room{
				{Type: 2, Token: "tokA", DisplayName: "Команда X"},
				{Type: 2, Token: "tokB", DisplayName: "Команда Y"},
			},
			wantExit:  ExitAmbiguous,
			wantCalls: 0,
		},
		{
			name:       "--name 0 совпадений → 2, GetParticipants не звался",
			findResult: []client.Room{},
			wantExit:   ExitNotFound,
			wantCalls:  0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := &roomsSpyClient{findResult: tc.findResult, participantsResult: participantsFixture()}
			deps := newRoomsDeps(spy)

			ee := roomsParticipantsHandler(context.Background(), deps, []string{"--name", "Команда"}, false)
			if ee.Code != tc.wantExit {
				t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, tc.wantExit, ee.Err)
			}
			if len(spy.participantsCalls) != tc.wantCalls {
				t.Fatalf("GetParticipants вызовов: got %d, want %d", len(spy.participantsCalls), tc.wantCalls)
			}
			if tc.wantCalls == 1 && spy.participantsCalls[0].token != tc.wantToken {
				t.Fatalf("GetParticipants token: got %q, want %q", spy.participantsCalls[0].token, tc.wantToken)
			}
		})
	}
	// При неоднозначности кандидаты печатаются в stderr (render.Candidates).
	spy := &roomsSpyClient{findResult: []client.Room{
		{Type: 2, Token: "tokA", DisplayName: "Команда X"},
		{Type: 2, Token: "tokB", DisplayName: "Команда Y"},
	}}
	deps := newRoomsDeps(spy)
	roomsParticipantsHandler(context.Background(), deps, []string{"--name", "Команда"}, false)
	if stderr := deps.Stderr.(*bytes.Buffer).String(); !strings.Contains(stderr, "tokA") {
		t.Errorf("stderr при exit 3: должен содержать кандидата tokA; got %q", stderr)
	}
}

// TestRoomsParticipantsHandlerClientError — OCS 404 → exit 2; OCS 403 →
// exit 1 с текстом сервера (спека §2, базовый контракт §7).
func TestRoomsParticipantsHandlerClientError(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantExit int
		wantText string
	}{
		{"OCS 404 → 2", &client.OCSError{Code: http.StatusNotFound, Message: "room not found"}, ExitNotFound, "room not found"},
		{"OCS 403 → 1 с текстом сервера", &client.OCSError{Code: http.StatusForbidden, Message: "нет доступа к комнате"}, ExitGeneric, "нет доступа к комнате"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := &roomsSpyClient{participantsErr: tc.err}
			deps := newRoomsDeps(spy)

			ee := roomsParticipantsHandler(context.Background(), deps, []string{"tok"}, false)
			if ee.Code != tc.wantExit {
				t.Fatalf("code: got %d, want %d", ee.Code, tc.wantExit)
			}
			if ee.Err == nil || !strings.Contains(ee.Err.Error(), tc.wantText) {
				t.Fatalf("err: got %v, want содержит %q", ee.Err, tc.wantText)
			}
		})
	}
}

// TestRoomsParticipantsHandlerUnknownFlag — неизвестный --flag → exit 1,
// клиент не звался.
func TestRoomsParticipantsHandlerUnknownFlag(t *testing.T) {
	spy := &roomsSpyClient{participantsResult: participantsFixture()}
	deps := newRoomsDeps(spy)

	ee := roomsParticipantsHandler(context.Background(), deps, []string{"tok", "--no-such"}, false)
	if ee.Code != ExitGeneric {
		t.Fatalf("code: got %d, want %d", ee.Code, ExitGeneric)
	}
	if len(spy.participantsCalls) != 0 {
		t.Fatalf("GetParticipants не должен вызываться; got %d calls", len(spy.participantsCalls))
	}
}

// TestRoomsParticipantsHandlerConflict — positional + --name: приоритет у
// positional, --name игнорируется с предупреждением в stderr (поведение
// chat show через ResolveRoom), FindRooms не звался.
func TestRoomsParticipantsHandlerConflict(t *testing.T) {
	spy := &roomsSpyClient{participantsResult: participantsFixture()}
	deps := newRoomsDeps(spy)

	ee := roomsParticipantsHandler(context.Background(), deps, []string{"tok-pos", "--name", "Команда"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	if len(spy.participantsCalls) != 1 || spy.participantsCalls[0].token != "tok-pos" {
		t.Fatalf("GetParticipants: got %+v, want 1 вызов с tok-pos (приоритет positional)", spy.participantsCalls)
	}
	if len(spy.findCalls) != 0 {
		t.Fatalf("FindRooms не должен вызываться при positional; got %d calls", len(spy.findCalls))
	}
	if stderr := deps.Stderr.(*bytes.Buffer).String(); !strings.Contains(stderr, "приоритет у token") {
		t.Errorf("stderr: должен содержать предупреждение о конфликте; got %q", stderr)
	}
}

// TestRoomsParticipantsHandlerExtraPositionals — лишние позиционные (второй
// и далее) игнорируются — единообразно с остальными командами.
func TestRoomsParticipantsHandlerExtraPositionals(t *testing.T) {
	spy := &roomsSpyClient{participantsResult: participantsFixture()}
	deps := newRoomsDeps(spy)

	ee := roomsParticipantsHandler(context.Background(), deps, []string{"tok-main", "extra", "more"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	if len(spy.participantsCalls) != 1 || spy.participantsCalls[0].token != "tok-main" {
		t.Fatalf("GetParticipants: got %+v, want 1 вызов с tok-main", spy.participantsCalls)
	}
}

// TestRoomsParticipantsHandlerEmptyInput — нет ни <room>, ни --name →
// exit 1 «укажите token позиционно или --name» (ResolveRoom StatusEmptyInput),
// без единого вызова клиента.
func TestRoomsParticipantsHandlerEmptyInput(t *testing.T) {
	spy := &roomsSpyClient{}
	deps := newRoomsDeps(spy)

	ee := roomsParticipantsHandler(context.Background(), deps, nil, false)
	if ee.Code != ExitGeneric {
		t.Fatalf("code: got %d, want %d", ee.Code, ExitGeneric)
	}
	if ee.Err == nil || !strings.Contains(ee.Err.Error(), "укажите token позиционно или --name") {
		t.Fatalf("err: got %v, want «укажите token позиционно или --name»", ee.Err)
	}
	if len(spy.findCalls) != 0 || len(spy.participantsCalls) != 0 {
		t.Fatalf("клиент не должен зваться: find=%d participants=%d", len(spy.findCalls), len(spy.participantsCalls))
	}
}
