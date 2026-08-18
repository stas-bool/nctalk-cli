package cli

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

// reactionsSpyClient — заглушка TalkClient для тестов `reactions get`.
// GetReactions записывает (gotToken, gotMessageId) и возвращает предзаданную
// map/ошибку; FindRooms возвращает foundRooms/findErr (нужен для ветки --name,
// которая идёт через ResolveRoom → client.FindRooms). Остальные методы TalkClient
// возвращают errMock (reactions-тесты их не трогают).
//
// Имя `reactionsSpyClient` (не `mockTalkClient`) — чтобы не конфликтовать с общей
// заглушкой в cli_test.go и с параллельными тестами в handlers_search_test.go /
// handlers_rooms_test.go.
type reactionsSpyClient struct {
	// GetReactions spy + stub.
	gotToken     string
	gotMessageId int
	reactions    map[string][]client.ReactionActor
	reactionsErr error
	// FindRooms stub (для разрешения --name в ResolveRoom).
	foundRooms []client.Room
	findErr    error
}

func (m *reactionsSpyClient) ListRooms(_ context.Context, _ client.ListRoomsOpts) ([]client.Room, error) {
	return nil, errMock
}
func (m *reactionsSpyClient) FindRooms(_ context.Context, _ string, _ string) ([]client.Room, error) {
	return m.foundRooms, m.findErr
}
func (m *reactionsSpyClient) SearchRooms(_ context.Context, _ string, _ int) ([]client.ConversationResult, error) {
	return nil, errMock
}
func (m *reactionsSpyClient) GetChat(_ context.Context, _ string, _ client.GetChatOpts) ([]client.Message, error) {
	return nil, errMock
}
func (m *reactionsSpyClient) SendMessage(_ context.Context, _ string, _ client.SendMessageOpts) (int, error) {
	return 0, errMock
}
func (m *reactionsSpyClient) GetReactions(_ context.Context, token string, messageId int) (map[string][]client.ReactionActor, error) {
	m.gotToken = token
	m.gotMessageId = messageId
	return m.reactions, m.reactionsErr
}
func (m *reactionsSpyClient) SearchMessages(_ context.Context, _ string, _ client.SearchMessagesOpts) ([]client.MessageResult, error) {
	return nil, errMock
}

// compile-time гарантия: reactionsSpyClient реализует TalkClient.
var _ TalkClient = (*reactionsSpyClient)(nil)

// newReactionsDeps — Deps с reactions-шпионом и буферизованным Stdout/Stderr.
func newReactionsDeps(spy *reactionsSpyClient) Deps {
	return Deps{
		Client: spy,
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
		Now:    func() time.Time { return time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC) },
	}
}

// TestReactionsGetHandlerText — `reactions get <token> <messageId>` в текстовом
// режиме: exit 0, spy получил именно введённые token/messageId, stdout содержит
// строку реакции с именами авторов (формат render.ReactionsText).
func TestReactionsGetHandlerText(t *testing.T) {
	spy := &reactionsSpyClient{reactions: map[string][]client.ReactionActor{
		"👍": {
			{ActorType: "users", ActorId: "u-alice", ActorDisplayName: "Alice", Timestamp: 1752750000},
			{ActorType: "users", ActorId: "u-bob", ActorDisplayName: "Bob", Timestamp: 1752750001},
		},
	}}
	deps := newReactionsDeps(spy)

	ee := reactionsGetHandler(context.Background(), deps, []string{"tok-abc", "42"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	// Аргументы дошли до клиента как есть.
	if spy.gotToken != "tok-abc" {
		t.Errorf("gotToken: got %q, want %q", spy.gotToken, "tok-abc")
	}
	if spy.gotMessageId != 42 {
		t.Errorf("gotMessageId: got %d, want 42", spy.gotMessageId)
	}
	// Текстовый вывод содержит эмодзи и обоих авторов (render.ReactionsText).
	out := deps.Stdout.(*bytes.Buffer).String()
	for _, want := range []string{"👍", "Alice", "Bob"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout: должен содержать %q; got %q", want, out)
		}
	}
}

// TestReactionsGetHandlerJSON — `reactions get ... --json`: exit 0, stdout —
// валидный JSON, обратный json.Unmarshal восстанавливает структуру.
func TestReactionsGetHandlerJSON(t *testing.T) {
	spy := &reactionsSpyClient{reactions: map[string][]client.ReactionActor{
		"🎉": {
			{ActorType: "users", ActorId: "u-alice", ActorDisplayName: "Alice", Timestamp: 1752750000},
		},
	}}
	deps := newReactionsDeps(spy)

	ee := reactionsGetHandler(context.Background(), deps, []string{"tok-xyz", "7"}, true)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	out := deps.Stdout.(*bytes.Buffer).String()

	// Обратный парсинг: stdout должен быть валидным JSON-объектом «эмодзи → []actor».
	var got map[string][]client.ReactionActor
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout не парсится как JSON: %v; raw=%q", err, out)
	}
	actors, ok := got["🎉"]
	if !ok {
		t.Fatalf("ожидался ключ %q в JSON; raw=%q", "🎉", out)
	}
	if len(actors) != 1 {
		t.Fatalf("len(actors): got %d, want 1", len(actors))
	}
	// json-теги ReactionActor — lowercase (actorId/actorDisplayName/...).
	if actors[0].ActorId != "u-alice" {
		t.Errorf("ActorId: got %q, want %q", actors[0].ActorId, "u-alice")
	}
	if actors[0].ActorDisplayName != "Alice" {
		t.Errorf("ActorDisplayName: got %q, want %q", actors[0].ActorDisplayName, "Alice")
	}
	if actors[0].Timestamp != 1752750000 {
		t.Errorf("Timestamp: got %d, want 1752750000", actors[0].Timestamp)
	}
}

// TestReactionsGetHandlerEmptyText — пустая map реакций в текстовом режиме:
// render.ReactionsText печатает «реакций нет», exit 0 (спека §6).
func TestReactionsGetHandlerEmptyText(t *testing.T) {
	spy := &reactionsSpyClient{reactions: map[string][]client.ReactionActor{}}
	deps := newReactionsDeps(spy)

	ee := reactionsGetHandler(context.Background(), deps, []string{"tok", "1"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	out := deps.Stdout.(*bytes.Buffer).String()
	const want = "реакций нет\n"
	if out != want {
		t.Errorf("stdout: got %q, want %q", out, want)
	}
}

// TestReactionsGetHandlerEmptyJSON — пустая map + --json: exit 0, render.ReactionsJSON
// маршалит пустую map как `{}` (НЕ null), спека §6.
func TestReactionsGetHandlerEmptyJSON(t *testing.T) {
	spy := &reactionsSpyClient{reactions: map[string][]client.ReactionActor{}}
	deps := newReactionsDeps(spy)

	ee := reactionsGetHandler(context.Background(), deps, []string{"tok", "1"}, true)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	out := deps.Stdout.(*bytes.Buffer).String()
	// json.NewEncoder для пустой non-nil map → `{}`. Обратный парсинг должен дать
	// пустую map (не nil), иначе контракт нарушен.
	var got map[string][]client.ReactionActor
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout не парсится как JSON: %v; raw=%q", err, out)
	}
	if len(got) != 0 {
		t.Errorf("ожидалась пустая map после Unmarshal; got %d ключей", len(got))
	}
}

// TestReactionsGetHandlerNameResolved — `reactions get --name Foo 42` с одним
// совпадением FindRooms: ResolveRoom возвращает token найденной комнаты, и именно
// этот token доходит до GetReactions (вместо строки "Foo").
func TestReactionsGetHandlerNameResolved(t *testing.T) {
	spy := &reactionsSpyClient{
		foundRooms: []client.Room{
			{Type: 2, Token: "real-token", DisplayName: "Foo Room"},
		},
		reactions: map[string][]client.ReactionActor{
			"👍": {{ActorType: "users", ActorId: "u-alice", ActorDisplayName: "Alice"}},
		},
	}
	deps := newReactionsDeps(spy)

	ee := reactionsGetHandler(context.Background(), deps, []string{"--name", "Foo", "42"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	if spy.gotToken != "real-token" {
		t.Errorf("gotToken: got %q, want %q (должен быть token разрешённой комнаты)", spy.gotToken, "real-token")
	}
	if spy.gotMessageId != 42 {
		t.Errorf("gotMessageId: got %d, want 42", spy.gotMessageId)
	}
}

// TestReactionsGetHandlerNameEqualsForm — форма `--name=Foo` (без пробела)
// разбирается так же, как `--name Foo`.
func TestReactionsGetHandlerNameEqualsForm(t *testing.T) {
	spy := &reactionsSpyClient{
		foundRooms: []client.Room{
			{Type: 2, Token: "real-token", DisplayName: "Foo Room"},
		},
		reactions: map[string][]client.ReactionActor{},
	}
	deps := newReactionsDeps(spy)

	ee := reactionsGetHandler(context.Background(), deps, []string{"--name=Foo", "42"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	if spy.gotToken != "real-token" {
		t.Errorf("gotToken: got %q, want %q", spy.gotToken, "real-token")
	}
}

// TestReactionsGetHandlerAmbiguousName — `--name Foo` с >1 совпадением:
// ResolveRoom печатает кандидаты в stderr и возвращает ExitAmbiguous → handler
// пробрасывает код 3 (спека §7).
func TestReactionsGetHandlerAmbiguousName(t *testing.T) {
	spy := &reactionsSpyClient{
		foundRooms: []client.Room{
			{Type: 2, Token: "tok-1", DisplayName: "Foo One"},
			{Type: 2, Token: "tok-2", DisplayName: "Foo Two"},
		},
		reactions: map[string][]client.ReactionActor{},
	}
	deps := newReactionsDeps(spy)

	ee := reactionsGetHandler(context.Background(), deps, []string{"--name", "Foo", "42"}, false)
	if ee.Code != ExitAmbiguous {
		t.Fatalf("code: got %d, want %d (ExitAmbiguous)", ee.Code, ExitAmbiguous)
	}
	// ResolveRoom уже напечатал список кандидатов в stderr (контракт exit 3).
	if stderr := deps.Stderr.(*bytes.Buffer).String(); stderr == "" {
		t.Errorf("ожидался непустой stderr со списком кандидатов")
	}
	// Клиент не должен был вызваться (разрешение комнаты не прошло).
	if spy.gotToken != "" {
		t.Errorf("GetReactions не должен был вызваться; got token=%q", spy.gotToken)
	}
}

// TestReactionsGetHandlerNotFoundName — `--name Missing` без совпадений:
// ResolveRoom возвращает ExitNotFound → handler пробрасывает код 2 (спека §7).
func TestReactionsGetHandlerNotFoundName(t *testing.T) {
	spy := &reactionsSpyClient{
		foundRooms: nil, // FindRooms вернёт (nil, nil) → len(rooms)==0 → NotFound
		reactions:  map[string][]client.ReactionActor{},
	}
	deps := newReactionsDeps(spy)

	ee := reactionsGetHandler(context.Background(), deps, []string{"--name", "Missing", "42"}, false)
	if ee.Code != ExitNotFound {
		t.Fatalf("code: got %d, want %d (ExitNotFound)", ee.Code, ExitNotFound)
	}
	if spy.gotToken != "" {
		t.Errorf("GetReactions не должен был вызваться; got token=%q", spy.gotToken)
	}
}

// TestReactionsGetHandlerInvalidMessageId — `reactions get tok abc`: strconv.Atoi
// обламывается → exit 1 (ExitGeneric) без клиентского вызова.
func TestReactionsGetHandlerInvalidMessageId(t *testing.T) {
	spy := &reactionsSpyClient{reactions: map[string][]client.ReactionActor{}}
	deps := newReactionsDeps(spy)

	ee := reactionsGetHandler(context.Background(), deps, []string{"tok", "abc"}, false)
	if ee.Code != ExitGeneric {
		t.Fatalf("code: got %d, want %d (ExitGeneric)", ee.Code, ExitGeneric)
	}
	if ee.Err == nil {
		t.Fatalf("ожидалась ошибка, got nil")
	}
	if spy.gotToken != "" {
		t.Errorf("GetReactions не должен был вызваться; got token=%q", spy.gotToken)
	}
}

// TestReactionsGetHandlerMissingArgs — недостаточно позиционных аргументов:
// `reactions get tok` без messageId → exit 1 (ExitGeneric).
func TestReactionsGetHandlerMissingArgs(t *testing.T) {
	spy := &reactionsSpyClient{reactions: map[string][]client.ReactionActor{}}
	deps := newReactionsDeps(spy)

	ee := reactionsGetHandler(context.Background(), deps, []string{"tok"}, false)
	if ee.Code != ExitGeneric {
		t.Fatalf("code: got %d, want %d (ExitGeneric)", ee.Code, ExitGeneric)
	}
	if spy.gotToken != "" {
		t.Errorf("GetReactions не должен был вызваться; got token=%q", spy.gotToken)
	}
}

// TestReactionsGetHandlerNonPositiveMessageId — `reactions get tok 0` и
// отрицательные: неположительный id лишён смысла, отсекаем ДО сетевого вызова
// (guard по замечанию review; сервер ответил бы 4xx).
func TestReactionsGetHandlerNonPositiveMessageId(t *testing.T) {
	for _, raw := range []string{"0", "-1"} {
		spy := &reactionsSpyClient{reactions: map[string][]client.ReactionActor{}}
		deps := newReactionsDeps(spy)

		ee := reactionsGetHandler(context.Background(), deps, []string{"tok", raw}, false)
		if ee.Code != ExitGeneric {
			t.Fatalf("messageId %s: code got %d, want %d", raw, ee.Code, ExitGeneric)
		}
		if ee.Err == nil {
			t.Fatalf("messageId %s: err nil, want non-nil", raw)
		}
		if spy.gotToken != "" {
			t.Errorf("messageId %s: GetReactions не должен был вызваться; got token=%q", raw, spy.gotToken)
		}
	}
}

// TestReactionsGetHandlerClientError — GetReactions возвращает ошибку → handler
// пробрасывает её как ExitGeneric (код 1), stdout пуст.
func TestReactionsGetHandlerClientError(t *testing.T) {
	sentinel := errors.New("boom: network down")
	spy := &reactionsSpyClient{reactionsErr: sentinel}
	deps := newReactionsDeps(spy)

	ee := reactionsGetHandler(context.Background(), deps, []string{"tok", "42"}, false)
	if ee.Code != ExitGeneric {
		t.Fatalf("code: got %d, want %d (ExitGeneric)", ee.Code, ExitGeneric)
	}
	if !errors.Is(ee.Err, sentinel) {
		t.Errorf("err: got %v, want wraps %v", ee.Err, sentinel)
	}
	if out := deps.Stdout.(*bytes.Buffer).String(); out != "" {
		t.Errorf("stdout: got %q, want empty при ошибке клиента", out)
	}
}

// TestReactionsGetHandlerOCSNotFound — GetReactions вернул *client.OCSError{404}
// (комната или messageId не найдены на сервере) → exit 2 (NotFound), а не 1.
// Это ключевой кейс e2e-баги: reactions get <room> <несуществующий_messageId>.
// Спека §7/§9: OCS 404 = exit 2.
func TestReactionsGetHandlerOCSNotFound(t *testing.T) {
	spy := &reactionsSpyClient{
		reactionsErr: &client.OCSError{Code: 404, Message: "message not found"},
	}
	deps := newReactionsDeps(spy)

	ee := reactionsGetHandler(context.Background(), deps, []string{"85z9h55k", "999999999"}, false)
	if ee.Code != ExitNotFound {
		t.Fatalf("code: got %d, want %d (ExitNotFound для OCS 404; err=%v)", ee.Code, ExitNotFound, ee.Err)
	}
	var oe *client.OCSError
	if !errors.As(ee.Err, &oe) || oe.Code != 404 {
		t.Errorf("OCSError{Code:404}: не извлечён из ee.Err=%v", ee.Err)
	}
	if out := deps.Stdout.(*bytes.Buffer).String(); out != "" {
		t.Errorf("stdout: got %q, want empty при ошибке клиента", out)
	}
}

// TestReactionsGetHandlerOCSAuth — GetReactions вернул *client.OCSError{401}
// → exit 1 (Generic), а не 2. Регресс: различие 404 vs 401.
func TestReactionsGetHandlerOCSAuth(t *testing.T) {
	spy := &reactionsSpyClient{
		reactionsErr: &client.OCSError{Code: 401, Message: "bad credentials"},
	}
	deps := newReactionsDeps(spy)

	ee := reactionsGetHandler(context.Background(), deps, []string{"tok", "42"}, false)
	if ee.Code != ExitGeneric {
		t.Fatalf("code: got %d, want %d (ExitGeneric для OCS 401; err=%v)", ee.Code, ExitGeneric, ee.Err)
	}
}
