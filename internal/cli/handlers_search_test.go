package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stas-bool/nctalk-cli/internal/client"
)

// searchSpyClient — заглушка TalkClient для тестов search-хендлера. Все методы
// кроме SearchMessages возвращают errMock (search-тесты их не трогают).
// SearchMessages записывает полученные (term, opts) в gotTerm/gotOpts и
// возвращает заранее настроенные rs/err — это и есть «mock-клиент реализует
// cli.TalkClient; SearchMessages записывает аргументы» из плана Task 4.5c.
//
// Имя `searchSpyClient` (не `mockTalkClient`) — чтобы НЕ конфликтовать с общей
// заглушкой в cli_test.go и с параллельной работой Task 4.5a.
type searchSpyClient struct {
	gotTerm string
	gotOpts client.SearchMessagesOpts
	rs      []client.MessageResult
	err     error
}

func (m *searchSpyClient) ListRooms(_ context.Context, _ client.ListRoomsOpts) ([]client.Room, error) {
	return nil, errMock
}
func (m *searchSpyClient) FindRooms(_ context.Context, _, _ string) ([]client.Room, error) {
	return nil, errMock
}
func (m *searchSpyClient) SearchRooms(_ context.Context, _ string, _ int) ([]client.ConversationResult, error) {
	return nil, errMock
}
func (m *searchSpyClient) GetChat(_ context.Context, _ string, _ client.GetChatOpts) ([]client.Message, error) {
	return nil, errMock
}
func (m *searchSpyClient) SendMessage(_ context.Context, _ string, _ client.SendMessageOpts) (int, error) {
	return 0, errMock
}
func (m *searchSpyClient) EditMessage(_ context.Context, _ string, _ int, _ client.EditMessageOpts) (int, error) {
	return 0, errMock
}
func (m *searchSpyClient) GetReactions(_ context.Context, _ string, _ int) (map[string][]client.ReactionActor, error) {
	return nil, errMock
}
func (m *searchSpyClient) GetParticipants(_ context.Context, _ string) ([]client.Participant, error) {
	return nil, errMock
}
func (m *searchSpyClient) SearchMessages(_ context.Context, term string, opts client.SearchMessagesOpts) ([]client.MessageResult, error) {
	m.gotTerm = term
	m.gotOpts = opts
	return m.rs, m.err
}

// compile-time гарантия: searchSpyClient реализует TalkClient.
var _ TalkClient = (*searchSpyClient)(nil)

// newSearchDeps — Deps с search-шпионом и буферизованным Stdout/Stderr.
func newSearchDeps(spy *searchSpyClient) Deps {
	return Deps{
		Client: spy,
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
		Now:    func() time.Time { return time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC) },
	}
}

// TestSearchHandlerParsesArgs — term и флаги корректно разбираются и доходят до
// SearchMessages в виде (term, SearchMessagesOpts). --limit по умолчанию = 0
// (клиент сам подставит дефолт 10, спека §6).
func TestSearchHandlerParsesArgs(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantTerm string
		wantOpts client.SearchMessagesOpts
	}{
		{
			name:     "только term — limit=0 (клиент подставит дефолт 10)",
			args:     []string{"foo"},
			wantTerm: "foo",
			wantOpts: client.SearchMessagesOpts{Limit: 0, From: "", All: false},
		},
		{
			name:     "term + --from + --limit + --all — все три параметра дошли",
			args:     []string{"foo", "--from", "u-alice", "--limit", "25", "--all"},
			wantTerm: "foo",
			wantOpts: client.SearchMessagesOpts{From: "u-alice", Limit: 25, All: true},
		},
		{
			name:     "флаги ДО term — term всё равно первый не-флаг аргумент",
			args:     []string{"--from", "u-bob", "bar"},
			wantTerm: "bar",
			wantOpts: client.SearchMessagesOpts{From: "u-bob", Limit: 0, All: false},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := &searchSpyClient{rs: []client.MessageResult{}}
			deps := newSearchDeps(spy)
			ee := searchHandler(context.Background(), deps, tc.args, false)
			if ee.Code != ExitOK {
				t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
			}
			if spy.gotTerm != tc.wantTerm {
				t.Errorf("term: got %q, want %q", spy.gotTerm, tc.wantTerm)
			}
			if spy.gotOpts != tc.wantOpts {
				t.Errorf("opts: got %+v, want %+v", spy.gotOpts, tc.wantOpts)
			}
		})
	}
}

// TestSearchHandlerEmptyResult — пустой результат: exit 0 и пустой stdout
// (поисковая семантика, спека §7).
func TestSearchHandlerEmptyResult(t *testing.T) {
	spy := &searchSpyClient{rs: []client.MessageResult{}}
	deps := newSearchDeps(spy)
	ee := searchHandler(context.Background(), deps, []string{"nothing-matches"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	if out := deps.Stdout.(*bytes.Buffer).String(); out != "" {
		t.Errorf("stdout: got %q, want empty (поисковая семантика — пустой вывод)", out)
	}
}

// TestSearchHandlerEmptyTerm — пустой term отсекается на CLI-уровне: exit 1
// (ExitGeneric) с сообщением "term не может быть пустым", без сетевого вызова
// (дублирующий клиентский guard, спека §8).
func TestSearchHandlerEmptyTerm(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"search без аргумента", []string{}},
		{"search с пустой строкой", []string{""}},
		{"только флаги, без term", []string{"--from", "u-x", "--all"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := &searchSpyClient{rs: []client.MessageResult{}}
			deps := newSearchDeps(spy)
			ee := searchHandler(context.Background(), deps, tc.args, false)
			if ee.Code != ExitGeneric {
				t.Fatalf("code: got %d, want %d", ee.Code, ExitGeneric)
			}
			if ee.Err == nil {
				t.Fatalf("ожидалась ошибка, got nil")
			}
			const want = "term не может быть пустым"
			if ee.Err.Error() != want {
				t.Errorf("текст ошибки: got %q, want %q", ee.Err.Error(), want)
			}
			// Клиент НЕ должен был вызваться (guard срабатывает до вызова).
			if spy.gotTerm != "" {
				t.Errorf("SearchMessages не должен был вызваться; got term=%q", spy.gotTerm)
			}
		})
	}
}

// TestSearchHandlerJSON — с jsonOut=true результат пишется как валидный JSON
// (через render.MessageResultsJSON). Проверяем обратным json.Unmarshal.
func TestSearchHandlerJSON(t *testing.T) {
	var rs []client.MessageResult
	mr := client.MessageResult{
		Title:       "Alice",
		Subline:     "hello world",
		ResourceUrl: "https://nc.example/call/tok1#message_42",
	}
	mr.Attributes.Conversation = "tok1"
	mr.Attributes.MessageId = 42
	mr.Attributes.ActorType = "users"
	mr.Attributes.ActorId = "u-alice"
	mr.Attributes.Timestamp = 1752750000
	rs = append(rs, mr)

	spy := &searchSpyClient{rs: rs}
	deps := newSearchDeps(spy)
	ee := searchHandler(context.Background(), deps, []string{"hello"}, true)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	out := deps.Stdout.(*bytes.Buffer).String()

	// Обратный парсинг: stdout должен быть валидным JSON-массивом.
	var got []client.MessageResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout не парсится как JSON: %v; raw=%q", err, out)
	}
	if len(got) != 1 {
		t.Fatalf("len(result): got %d, want 1", len(got))
	}
	// MessageResult не имеет json-тегов → имена полей PascalCase в JSON.
	if got[0].Title != "Alice" {
		t.Errorf("Title: got %q, want %q", got[0].Title, "Alice")
	}
	if got[0].Attributes.MessageId != 42 {
		t.Errorf("MessageId: got %d, want 42", got[0].Attributes.MessageId)
	}
	if got[0].Subline != "hello world" {
		t.Errorf("Subline: got %q, want %q", got[0].Subline, "hello world")
	}
}

// TestSearchHandlerClientError — ошибка клиента пробрасывается как
// ExitError{ExitGeneric, ...}; stdout остаётся пустым.
func TestSearchHandlerClientError(t *testing.T) {
	sentinel := errors.New("boom: network down")
	spy := &searchSpyClient{err: sentinel}
	deps := newSearchDeps(spy)
	ee := searchHandler(context.Background(), deps, []string{"foo"}, false)
	if ee.Code != ExitGeneric {
		t.Fatalf("code: got %d, want %d", ee.Code, ExitGeneric)
	}
	if !errors.Is(ee.Err, sentinel) {
		t.Errorf("err: got %v, want wraps %v", ee.Err, sentinel)
	}
	if out := deps.Stdout.(*bytes.Buffer).String(); out != "" {
		t.Errorf("stdout: got %q, want empty при ошибке клиента", out)
	}
}
