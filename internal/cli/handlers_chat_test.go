package cli

// handlers_chat_test.go — table-driven тесты для chatShowHandler (Task 4.3).
//
// Шпион chatSpyClient реализует cli.TalkClient: GetChat записывает (token, opts)
// и возвращает преднастроенные msgs/err; FindRooms возвращает findRooms/err
// (нужно для тестов разрешения --name). Остальные методы возвращают errMock.
//
// Выбрано имя `handlers_chat_test.go` (а не `chat_test.go`) — единообразно с
// `handlers_rooms_test.go` / `handlers_search_test.go`. Task 4.4 (chat send)
// будет дописывать тесты в этот же файл.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stas-bool/nctalk-cli/internal/client"
)

// chatSpyClient — заглушка TalkClient для тестов chat show/send. GetChat
// фиксирует аргументы вызова в gotToken/gotOpts и возвращает chatMsgs/chatErr.
// SendMessage фиксирует аргументы в gotSendToken/gotSendOpts и возвращает
// sendId/sendErr. FindRooms возвращает findRooms/findErr — это нужно для тестов
// --name (неоднозначное разрешение комнаты через ResolveRoom).
type chatSpyClient struct {
	// GetChat-запись
	gotToken  string
	gotOpts   client.GetChatOpts
	chatCalls int

	// GetChat-результат
	chatMsgs []client.Message
	chatErr  error

	// SendMessage-запись (Task 4.4)
	gotSendToken string
	gotSendOpts  client.SendMessageOpts
	sendCalls    int

	// SendMessage-результат
	sendId  int
	sendErr error

	// EditMessage-запись (chat edit)
	gotEditToken string
	gotEditId    int
	gotEditOpts  client.EditMessageOpts
	editCalls    int

	// EditMessage-результат
	editId  int
	editErr error

	// FindRooms-результат (для --name)
	findRooms []client.Room
	findErr   error
	findCalls int
}

func (m *chatSpyClient) GetChat(_ context.Context, token string, opts client.GetChatOpts) ([]client.Message, error) {
	m.chatCalls++
	m.gotToken = token
	m.gotOpts = opts
	return m.chatMsgs, m.chatErr
}

func (m *chatSpyClient) SendMessage(_ context.Context, token string, opts client.SendMessageOpts) (int, error) {
	m.sendCalls++
	m.gotSendToken = token
	m.gotSendOpts = opts
	return m.sendId, m.sendErr
}

func (m *chatSpyClient) EditMessage(_ context.Context, token string, messageId int, opts client.EditMessageOpts) (int, error) {
	m.editCalls++
	m.gotEditToken = token
	m.gotEditId = messageId
	m.gotEditOpts = opts
	return m.editId, m.editErr
}

func (m *chatSpyClient) FindRooms(_ context.Context, _, _ string) ([]client.Room, error) {
	m.findCalls++
	return m.findRooms, m.findErr
}

// Неиспользуемые в chat-тестах методы — возвращают errMock (маркер).
func (m *chatSpyClient) ListRooms(_ context.Context, _ client.ListRoomsOpts) ([]client.Room, error) {
	return nil, errMock
}
func (m *chatSpyClient) SearchRooms(_ context.Context, _ string, _ int) ([]client.ConversationResult, error) {
	return nil, errMock
}
func (m *chatSpyClient) GetReactions(_ context.Context, _ string, _ int) (map[string][]client.ReactionActor, error) {
	return nil, errMock
}
func (m *chatSpyClient) GetParticipants(_ context.Context, _ string) ([]client.Participant, error) {
	return nil, errMock
}
func (m *chatSpyClient) SearchMessages(_ context.Context, _ string, _ client.SearchMessagesOpts) ([]client.MessageResult, error) {
	return nil, errMock
}

// compile-time гарантия: chatSpyClient реализует TalkClient.
var _ TalkClient = (*chatSpyClient)(nil)

// chatFixedNow — опорное время для тестов chat show (2026-07-17 12:00:00 UTC).
// Устанавливается в Deps.Now, чтобы парсинг относительных --since был
// детерминированным (handler использует deps.Now через parseSinceAt).
var chatFixedNow = time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)

// newChatDeps собирает Deps с шпионом и зафиксированным Now.
func newChatDeps(c *chatSpyClient) Deps {
	return Deps{
		Client: c,
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
		Now:    func() time.Time { return chatFixedNow },
	}
}

// makeChatMsg — конструктор client.Message с основными полями для тестов.
func makeChatMsg(id int, actorId, msgType, text string, ts int64) client.Message {
	return client.Message{
		Id:               id,
		ActorType:        "users",
		ActorId:          actorId,
		ActorDisplayName: actorId,
		MessageType:      msgType,
		Message:          text,
		Timestamp:        ts,
	}
}

// chatTsAt — helper: секунды Unix для 2026-07-17 HH:MM:00 UTC. Удобно задавать
// timestamps сообщений относительно chatFixedNow в тестах.
func chatTsAt(h, m int) int64 {
	return time.Date(2026, 7, 17, h, m, 0, 0, time.UTC).Unix()
}

// -----------------------------------------------------------------------------
// Выбор потолка выборки (limit) и парсинг флагов
// -----------------------------------------------------------------------------

// TestChatShowLastN — `--last 5` → GetChat вызван с Limit=5; выведены 5 строк.
func TestChatShowLastN(t *testing.T) {
	msgs := []client.Message{
		makeChatMsg(1, "alice", "comment", "one", chatFixedNow.Unix()),
		makeChatMsg(2, "alice", "comment", "two", chatFixedNow.Unix()),
		makeChatMsg(3, "alice", "comment", "three", chatFixedNow.Unix()),
		makeChatMsg(4, "alice", "comment", "four", chatFixedNow.Unix()),
		makeChatMsg(5, "alice", "comment", "five", chatFixedNow.Unix()),
	}
	spy := &chatSpyClient{chatMsgs: msgs}
	deps := newChatDeps(spy)

	ee := chatShowHandler(context.Background(), deps, []string{"TOK", "--last", "5"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	if spy.gotOpts.Limit != 5 {
		t.Errorf("Limit: got %d, want 5", spy.gotOpts.Limit)
	}
	if spy.gotToken != "TOK" {
		t.Errorf("token: got %q, want %q", spy.gotToken, "TOK")
	}
	out := deps.Stdout.(*bytes.Buffer).String()
	if got := strings.Count(out, "\n"); got != 5 {
		t.Errorf("stdout lines: got %d, want 5; raw=%q", got, out)
	}
}

// TestChatShowLastFromAlice — `--last 5 --from alice` → Limit=5 (НЕ 200):
// потолок остаётся равным N, фильтр применяется поверх.
func TestChatShowLastFromAlice(t *testing.T) {
	msgs := []client.Message{
		makeChatMsg(1, "alice", "comment", "a1", chatFixedNow.Unix()),
		makeChatMsg(2, "bob", "comment", "b1", chatFixedNow.Unix()),
		makeChatMsg(3, "alice", "comment", "a2", chatFixedNow.Unix()),
	}
	spy := &chatSpyClient{chatMsgs: msgs}
	deps := newChatDeps(spy)

	ee := chatShowHandler(context.Background(), deps, []string{"TOK", "--last", "5", "--from", "alice"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	if spy.gotOpts.Limit != 5 {
		t.Errorf("Limit: got %d, want 5 (--last приоритет над cap 200)", spy.gotOpts.Limit)
	}
	out := deps.Stdout.(*bytes.Buffer).String()
	if !strings.Contains(out, "a1") || !strings.Contains(out, "a2") {
		t.Errorf("ожидалось сообщение alice в выводе; raw=%q", out)
	}
	if strings.Contains(out, "b1") {
		t.Errorf("bob должен отфильтроваться; raw=%q", out)
	}
}

// TestChatShowFromWithoutLast — `--from alice` без --last → Limit=200 (cap),
// фильтр оставил только comment-сообщения от alice.
func TestChatShowFromWithoutLast(t *testing.T) {
	msgs := []client.Message{
		makeChatMsg(1, "alice", "comment", "alice-1", chatFixedNow.Unix()),
		makeChatMsg(2, "bob", "comment", "bob-1", chatFixedNow.Unix()),
		makeChatMsg(3, "alice", "comment", "alice-2", chatFixedNow.Unix()),
	}
	spy := &chatSpyClient{chatMsgs: msgs}
	deps := newChatDeps(spy)

	ee := chatShowHandler(context.Background(), deps, []string{"TOK", "--from", "alice"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	if spy.gotOpts.Limit != 200 {
		t.Errorf("Limit: got %d, want 200 (cap при --from без --last)", spy.gotOpts.Limit)
	}
	out := deps.Stdout.(*bytes.Buffer).String()
	if !strings.Contains(out, "alice-1") || !strings.Contains(out, "alice-2") {
		t.Errorf("ожидалось оба сообщения alice; raw=%q", out)
	}
	if strings.Contains(out, "bob-1") {
		t.Errorf("bob должен отфильтроваться по --from alice; raw=%q", out)
	}
}

// TestChatShowSinceWithoutLast — `--since 2h` без --last → Limit=200,
// StopBeforeTs задан (детерминированно через deps.Now), фильтр по ts.
func TestChatShowSinceWithoutLast(t *testing.T) {
	// chatFixedNow = 12:00 UTC; «2h» → sinceTs = 10:00 UTC.
	// Сообщение в 11:00 — новее, остаётся; в 09:00 — старее, режется.
	msgs := []client.Message{
		makeChatMsg(1, "alice", "comment", "newer", chatTsAt(11, 0)),
		makeChatMsg(2, "bob", "comment", "older", chatTsAt(9, 0)),
	}
	spy := &chatSpyClient{chatMsgs: msgs}
	deps := newChatDeps(spy)

	ee := chatShowHandler(context.Background(), deps, []string{"TOK", "--since", "2h"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	if spy.gotOpts.Limit != 200 {
		t.Errorf("Limit: got %d, want 200 (cap при --since без --last)", spy.gotOpts.Limit)
	}
	wantStop := chatFixedNow.Add(-2 * time.Hour).Unix()
	if spy.gotOpts.StopBeforeTs != wantStop {
		t.Errorf("StopBeforeTs: got %d, want %d (deps.Now - 2h)", spy.gotOpts.StopBeforeTs, wantStop)
	}
	out := deps.Stdout.(*bytes.Buffer).String()
	if !strings.Contains(out, "newer") {
		t.Errorf("ожидалось сообщение 'newer' (внутри окна); raw=%q", out)
	}
	if strings.Contains(out, "older") {
		t.Errorf("сообщение 'older' должно отфильтроваться по --since 2h; raw=%q", out)
	}
}

// TestChatShowDefaultLimit — без --last/--from/--since → Limit=20 (базовый
// дефолт показа, спека §6).
func TestChatShowDefaultLimit(t *testing.T) {
	spy := &chatSpyClient{chatMsgs: nil}
	deps := newChatDeps(spy)

	ee := chatShowHandler(context.Background(), deps, []string{"TOK"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	if spy.gotOpts.Limit != 20 {
		t.Errorf("Limit: got %d, want 20 (базовый дефолт показа)", spy.gotOpts.Limit)
	}
	if spy.gotOpts.StopBeforeTs != 0 {
		t.Errorf("StopBeforeTs: got %d, want 0 (без --since)", spy.gotOpts.StopBeforeTs)
	}
}

// -----------------------------------------------------------------------------
// Фильтр system-сообщений
// -----------------------------------------------------------------------------

// TestChatShowSystemFilter — по умолчанию system-сообщения скрыты; --system
// показывает их.
func TestChatShowSystemFilter(t *testing.T) {
	msgs := []client.Message{
		makeChatMsg(1, "alice", "comment", "regular-msg", chatFixedNow.Unix()),
		makeChatMsg(2, "system", "system", "user_added", chatFixedNow.Unix()),
	}
	msgs[1].SystemMessage = "user_added"

	t.Run("без --system → system скрыты", func(t *testing.T) {
		spy := &chatSpyClient{chatMsgs: msgs}
		deps := newChatDeps(spy)

		ee := chatShowHandler(context.Background(), deps, []string{"TOK"}, false)
		if ee.Code != ExitOK {
			t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
		}
		out := deps.Stdout.(*bytes.Buffer).String()
		if !strings.Contains(out, "regular-msg") {
			t.Errorf("ожидалось regular-сообщение; raw=%q", out)
		}
		if strings.Contains(out, "user_added") {
			t.Errorf("system-сообщение должно быть скрыто; raw=%q", out)
		}
	})

	t.Run("с --system → system видны", func(t *testing.T) {
		spy := &chatSpyClient{chatMsgs: msgs}
		deps := newChatDeps(spy)

		ee := chatShowHandler(context.Background(), deps, []string{"TOK", "--system"}, false)
		if ee.Code != ExitOK {
			t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
		}
		out := deps.Stdout.(*bytes.Buffer).String()
		if !strings.Contains(out, "regular-msg") {
			t.Errorf("ожидалось regular-сообщение; raw=%q", out)
		}
		if !strings.Contains(out, "user_added") {
			t.Errorf("при --system ожидалось system-сообщение; raw=%q", out)
		}
	})
}

// -----------------------------------------------------------------------------
// Предупреждение о cap 200
// -----------------------------------------------------------------------------

// TestChatShowCap200NoMatchesWarning — mock вернул 200 сообщений, фильтр
// оставил 0 → в stderr есть текст предупреждения.
func TestChatShowCap200NoMatchesWarning(t *testing.T) {
	msgs := make([]client.Message, 200)
	for i := range msgs {
		// Все сообщения от bob — фильтр --from alice вырежет всё.
		msgs[i] = makeChatMsg(i+1, "bob", "comment", "from-bob", chatFixedNow.Unix())
	}
	spy := &chatSpyClient{chatMsgs: msgs}
	deps := newChatDeps(spy)

	ee := chatShowHandler(context.Background(), deps, []string{"TOK", "--from", "alice"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (0 совпадений → exit 0)", ee.Code, ExitOK)
	}
	stderr := deps.Stderr.(*bytes.Buffer).String()
	const wantSubstr = "выборка ограничена 200 сообщениями"
	if !strings.Contains(stderr, wantSubstr) {
		t.Errorf("stderr должен содержать %q; got: %q", wantSubstr, stderr)
	}
	// stdout при этом пустой (результ нет).
	if out := deps.Stdout.(*bytes.Buffer).String(); out != "" {
		t.Errorf("stdout должен быть пустым при 0 совпадениях; got: %q", out)
	}
}

// TestChatShowCap200WithMatchesNoWarning — mock вернул 200 сообщений, фильтр
// оставил >0 → предупреждения НЕТ.
func TestChatShowCap200WithMatchesNoWarning(t *testing.T) {
	msgs := make([]client.Message, 200)
	for i := range msgs {
		msgs[i] = makeChatMsg(i+1, "alice", "comment", fmt.Sprintf("alice-msg-%d", i), chatFixedNow.Unix())
	}
	spy := &chatSpyClient{chatMsgs: msgs}
	deps := newChatDeps(spy)

	ee := chatShowHandler(context.Background(), deps, []string{"TOK", "--from", "alice"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	stderr := deps.Stderr.(*bytes.Buffer).String()
	if stderr != "" {
		t.Errorf("stderr должен быть пустым (есть совпадения); got: %q", stderr)
	}
}

// TestChatShowBelowCapNoWarning — выборка < 200 (например, 5) и 0 совпадений:
// предупреждения НЕТ (cap не достигнут).
func TestChatShowBelowCapNoWarning(t *testing.T) {
	msgs := []client.Message{
		makeChatMsg(1, "bob", "comment", "b1", chatFixedNow.Unix()),
		makeChatMsg(2, "bob", "comment", "b2", chatFixedNow.Unix()),
	}
	spy := &chatSpyClient{chatMsgs: msgs}
	deps := newChatDeps(spy)

	ee := chatShowHandler(context.Background(), deps, []string{"TOK", "--from", "alice"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d", ee.Code, ExitOK)
	}
	stderr := deps.Stderr.(*bytes.Buffer).String()
	if stderr != "" {
		t.Errorf("stderr должен быть пустым (cap не достигнут); got: %q", stderr)
	}
}

// TestChatShowLastExplicitNoWarning — `--last 200` (явный потолок) + фильтр
// вырезал всё: предупреждения о cap 200 НЕ должно быть — пользователь сам задал
// потолок и знает объём выборки. Без guard срабатывало бы ложноположительно.
func TestChatShowLastExplicitNoWarning(t *testing.T) {
	msgs := make([]client.Message, 200)
	for i := range msgs {
		msgs[i] = makeChatMsg(i+1, "bob", "comment", "from-bob", chatFixedNow.Unix())
	}
	spy := &chatSpyClient{chatMsgs: msgs}
	deps := newChatDeps(spy)

	ee := chatShowHandler(context.Background(), deps, []string{"TOK", "--last", "200", "--from", "alice"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (0 совпадений → exit 0)", ee.Code, ExitOK)
	}
	stderr := deps.Stderr.(*bytes.Buffer).String()
	if strings.Contains(stderr, "выборка ограничена 200") {
		t.Errorf("при явном --last 200 предупреждения быть не должно; stderr=%q", stderr)
	}
}

// -----------------------------------------------------------------------------
// Подстановка messageParameters
// -----------------------------------------------------------------------------

// TestChatShowSubstituteParams — плейсхолдер {file} в таблице заменяется на
// Name параметра (render.SubstituteParams).
func TestChatShowSubstituteParams(t *testing.T) {
	msg := makeChatMsg(1, "alice", "comment", "файл {file}", chatFixedNow.Unix())
	msg.MessageParameters = client.MsgParams{
		"file": {Type: "file", Name: "doc.pdf"},
	}
	spy := &chatSpyClient{chatMsgs: []client.Message{msg}}
	deps := newChatDeps(spy)

	ee := chatShowHandler(context.Background(), deps, []string{"TOK"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	out := deps.Stdout.(*bytes.Buffer).String()
	if !strings.Contains(out, "doc.pdf") {
		t.Errorf("в выводе ожидается подставленное имя 'doc.pdf'; raw=%q", out)
	}
	if strings.Contains(out, "{file}") {
		t.Errorf("плейсхолдер {file} должен быть заменён; raw=%q", out)
	}
}

// -----------------------------------------------------------------------------
// Разрешение --name через ResolveRoom
// -----------------------------------------------------------------------------

// TestChatShowNameAmbiguous — --name с 2 совпадениями → ExitAmbiguous (код 3),
// GetChat не вызывается.
func TestChatShowNameAmbiguous(t *testing.T) {
	spy := &chatSpyClient{findRooms: []client.Room{
		{Type: 1, Token: "T1", DisplayName: "Первый", ActorId: "u-a"},
		{Type: 2, Token: "T2", DisplayName: "Второй", ActorId: "u-b"},
	}}
	deps := newChatDeps(spy)

	ee := chatShowHandler(context.Background(), deps, []string{"--name", "тест"}, false)
	if ee.Code != ExitAmbiguous {
		t.Fatalf("code: got %d, want %d (ExitAmbiguous; err=%v)", ee.Code, ExitAmbiguous, ee.Err)
	}
	if spy.chatCalls != 0 {
		t.Errorf("GetChat не должен вызываться при неоднозначном --name; got %d calls", spy.chatCalls)
	}
	if spy.findCalls != 1 {
		t.Errorf("FindRooms должен вызваться 1 раз; got %d", spy.findCalls)
	}
}

// TestChatShowNameSingleMatch — --name с 1 совпадением → токен этой комнаты,
// GetChat вызван с этим token.
func TestChatShowNameSingleMatch(t *testing.T) {
	spy := &chatSpyClient{
		findRooms: []client.Room{{Type: 1, Token: "TOKRESOLVED", DisplayName: "Single", ActorId: "u-a"}},
		chatMsgs:  []client.Message{makeChatMsg(1, "alice", "comment", "hello", chatFixedNow.Unix())},
	}
	deps := newChatDeps(spy)

	ee := chatShowHandler(context.Background(), deps, []string{"--name", "single"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	if spy.gotToken != "TOKRESOLVED" {
		t.Errorf("token: got %q, want %q (from single --name match)", spy.gotToken, "TOKRESOLVED")
	}
}

// -----------------------------------------------------------------------------
// JSON-вывод
// -----------------------------------------------------------------------------

// TestChatShowJSON — jsonOut=true → в stdout валидный JSON-массив client.Message
// с оригинальными полями (MessageParameters не теряется).
func TestChatShowJSON(t *testing.T) {
	msg := makeChatMsg(1, "alice", "comment", "текст {file}", chatFixedNow.Unix())
	msg.MessageParameters = client.MsgParams{
		"file": {Type: "file", Name: "doc.pdf"},
	}
	spy := &chatSpyClient{chatMsgs: []client.Message{msg}}
	deps := newChatDeps(spy)

	ee := chatShowHandler(context.Background(), deps, []string{"TOK"}, true)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	out := deps.Stdout.(*bytes.Buffer).Bytes()
	var got []client.Message
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("stdout не валидный JSON: %v; raw=%s", err, out)
	}
	if len(got) != 1 {
		t.Fatalf("ожидалось 1 сообщение, got %d", len(got))
	}
	// JSON хранит оригинальное Message + параметры (подстановка не применяется).
	if got[0].Message != "текст {file}" {
		t.Errorf("JSON Message: got %q, want %q (оригинал без подстановки)", got[0].Message, "текст {file}")
	}
	if got[0].MessageParameters["file"].Name != "doc.pdf" {
		t.Errorf("JSON MessageParameters.file.Name: got %q, want %q",
			got[0].MessageParameters["file"].Name, "doc.pdf")
	}
}

// -----------------------------------------------------------------------------
// Ошибки парсинга флагов
// -----------------------------------------------------------------------------

// TestChatShowInvalidLast — нечисловой --last → ExitGeneric без вызова клиента.
func TestChatShowInvalidLast(t *testing.T) {
	spy := &chatSpyClient{}
	deps := newChatDeps(spy)

	ee := chatShowHandler(context.Background(), deps, []string{"TOK", "--last", "abc"}, false)
	if ee.Code != ExitGeneric {
		t.Fatalf("code: got %d, want %d", ee.Code, ExitGeneric)
	}
	if spy.chatCalls != 0 {
		t.Errorf("GetChat не должен вызываться при ошибке парсинга --last; got %d calls", spy.chatCalls)
	}
}

// TestChatShowUnknownFlag — неизвестный --flag → ExitGeneric без вызова клиента.
func TestChatShowUnknownFlag(t *testing.T) {
	spy := &chatSpyClient{}
	deps := newChatDeps(spy)

	ee := chatShowHandler(context.Background(), deps, []string{"TOK", "--no-such-flag"}, false)
	if ee.Code != ExitGeneric {
		t.Fatalf("code: got %d, want %d", ee.Code, ExitGeneric)
	}
	if spy.chatCalls != 0 {
		t.Errorf("GetChat не должен вызываться при неизвестном флаге; got %d calls", spy.chatCalls)
	}
}

// TestChatShowClientError — ошибка GetChat → ExitGeneric, обёрнутка сохранена.
func TestChatShowClientError(t *testing.T) {
	sentinel := fmt.Errorf("chat network boom")
	spy := &chatSpyClient{chatErr: sentinel}
	deps := newChatDeps(spy)

	ee := chatShowHandler(context.Background(), deps, []string{"TOK"}, false)
	if ee.Code != ExitGeneric {
		t.Fatalf("code: got %d, want %d", ee.Code, ExitGeneric)
	}
	if ee.Err != sentinel {
		t.Errorf("err: got %v, want %v", ee.Err, sentinel)
	}
}

// TestChatShowClientOCSNotFound — GetChat вернул *client.OCSError{Code:404}
// (комната с таким token не найдена на сервере) → exit 2 (NotFound), а не 1.
// Это ключевой кейс e2e-баги: позиционный token несуществующий → сервер OCS 404.
// Спека §7/§9: not found = exit 2, в т.ч. OCS 404.
func TestChatShowClientOCSNotFound(t *testing.T) {
	spy := &chatSpyClient{
		chatErr: &client.OCSError{Code: 404, Message: "room not found"},
	}
	deps := newChatDeps(spy)

	ee := chatShowHandler(context.Background(), deps, []string{"НЕСУЩЕСТВУЮЩИЙ_zzz"}, false)
	if ee.Code != ExitNotFound {
		t.Fatalf("code: got %d, want %d (ExitNotFound для OCS 404; err=%v)", ee.Code, ExitNotFound, ee.Err)
	}
	// Исходная типизированная ошибка сохранена в ExitError.Err — не переупакована.
	var oe *client.OCSError
	if !errors.As(ee.Err, &oe) || oe.Code != 404 {
		t.Errorf("OCSError{Code:404}: не извлечён из ee.Err=%v", ee.Err)
	}
}

// TestChatShowClientOCSAuth — GetChat вернул *client.OCSError{Code:401}
// (неверные креды) → exit 1 (Generic), а не 2. Регресс: различие 404 vs 401.
func TestChatShowClientOCSAuth(t *testing.T) {
	spy := &chatSpyClient{
		chatErr: &client.OCSError{Code: 401, Message: "bad credentials"},
	}
	deps := newChatDeps(spy)

	ee := chatShowHandler(context.Background(), deps, []string{"TOK"}, false)
	if ee.Code != ExitGeneric {
		t.Fatalf("code: got %d, want %d (ExitGeneric для OCS 401; err=%v)", ee.Code, ExitGeneric, ee.Err)
	}
}

// -----------------------------------------------------------------------------
// chat send (Task 4.4)
// -----------------------------------------------------------------------------
//
// newChatSendDeps собирает Deps со шпионом, зафиксированным Now и заданным
// stdin (Deps.Stdin). Для chat send тестов stdin обязателен — иначе handler
// fallback-нул бы на реальный os.Stdin и завис на чтении терминала.
func newChatSendDeps(c *chatSpyClient, stdin io.Reader) Deps {
	d := newChatDeps(c)
	d.Stdin = stdin
	return d
}

// TestChatSendStdinText — тело из stdin → SendMessage вызван с этим телом, в
// stdout выведен id (текстовый формат `<int>\n`).
func TestChatSendStdinText(t *testing.T) {
	spy := &chatSpyClient{sendId: 777}
	deps := newChatSendDeps(spy, strings.NewReader("hello talk"))

	ee := chatSendHandler(context.Background(), deps, []string{"TOK"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	if spy.sendCalls != 1 {
		t.Fatalf("SendMessage calls: got %d, want 1", spy.sendCalls)
	}
	if spy.gotSendToken != "TOK" {
		t.Errorf("token: got %q, want %q", spy.gotSendToken, "TOK")
	}
	if spy.gotSendOpts.Message != "hello talk" {
		t.Errorf("Message: got %q, want %q", spy.gotSendOpts.Message, "hello talk")
	}
	out := deps.Stdout.(*bytes.Buffer).String()
	wantOut := "777\n"
	if out != wantOut {
		t.Errorf("stdout: got %q, want %q", out, wantOut)
	}
}

// TestChatSendStdinJSON — jsonOut=true → в stdout валидный JSON `{"id": <int>}`
// (проверка через json.Unmarshal), id совпадает с sendId.
func TestChatSendStdinJSON(t *testing.T) {
	spy := &chatSpyClient{sendId: 42}
	deps := newChatSendDeps(spy, strings.NewReader("json body"))

	ee := chatSendHandler(context.Background(), deps, []string{"TOK"}, true)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	raw := deps.Stdout.(*bytes.Buffer).Bytes()
	var got struct {
		Id int `json:"id"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("stdout не валидный JSON: %v; raw=%s", err, raw)
	}
	if got.Id != 42 {
		t.Errorf("json.id: got %d, want 42", got.Id)
	}
}

// TestChatSendFile — тело из --file <path> (создаётся через os.CreateTemp),
// содержимое файла доходит до SendMessage как opts.Message.
func TestChatSendFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "msg.txt")
	if err := os.WriteFile(path, []byte("from-file-body"), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	// Stdin пустой, но при --file он не читается — поэтому пустой buffer
	// подходит (handler не дойдёт до чтения stdin).
	spy := &chatSpyClient{sendId: 9}
	deps := newChatSendDeps(spy, strings.NewReader(""))

	ee := chatSendHandler(context.Background(), deps, []string{"TOK", "--file", path}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	if spy.sendCalls != 1 {
		t.Fatalf("SendMessage calls: got %d, want 1", spy.sendCalls)
	}
	if spy.gotSendOpts.Message != "from-file-body" {
		t.Errorf("Message: got %q, want %q", spy.gotSendOpts.Message, "from-file-body")
	}
}

// TestChatSendReplyToSilent — --reply-to 42 --silent доходят до mock-клиента
// как opts.ReplyTo=42 и opts.Silent=true.
func TestChatSendReplyToSilent(t *testing.T) {
	spy := &chatSpyClient{sendId: 1}
	deps := newChatSendDeps(spy, strings.NewReader("payload"))

	ee := chatSendHandler(context.Background(), deps,
		[]string{"TOK", "--reply-to", "42", "--silent"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	if spy.gotSendOpts.ReplyTo != 42 {
		t.Errorf("ReplyTo: got %d, want 42", spy.gotSendOpts.ReplyTo)
	}
	if !spy.gotSendOpts.Silent {
		t.Errorf("Silent: got false, want true")
	}
}

// TestChatSendReferenceId — --reference-id <uuid> доходит до mock-клиента как
// opts.ReferenceId. Бонус-проверка флага, добавленного в Task 4.4.
func TestChatSendReferenceId(t *testing.T) {
	spy := &chatSpyClient{sendId: 1}
	deps := newChatSendDeps(spy, strings.NewReader("x"))

	const ref = "11111111-2222-3333-4444-555555555555"
	ee := chatSendHandler(context.Background(), deps,
		[]string{"TOK", "--reference-id", ref}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	if spy.gotSendOpts.ReferenceId != ref {
		t.Errorf("ReferenceId: got %q, want %q", spy.gotSendOpts.ReferenceId, ref)
	}
}

// TestChatSendEmptyStdin — пустой stdin → ExitGeneric БЕЗ вызова SendMessage
// (счётчик вызовов = 0) и БЕЗ FindRooms (если бы был --name). Тело отсекается
// до любого сетевого запроса — это явное требование DoD Task 4.4.
func TestChatSendEmptyStdin(t *testing.T) {
	spy := &chatSpyClient{sendId: 1}
	deps := newChatSendDeps(spy, strings.NewReader(""))

	ee := chatSendHandler(context.Background(), deps, []string{"TOK"}, false)
	if ee.Code != ExitGeneric {
		t.Fatalf("code: got %d, want %d (ExitGeneric)", ee.Code, ExitGeneric)
	}
	if ee.Err == nil {
		t.Fatalf("Err: got nil, want non-nil (тело сообщения пусто)")
	}
	if !strings.Contains(ee.Err.Error(), "пусто") {
		t.Errorf("Err text: got %q, want содержит 'пусто'", ee.Err.Error())
	}
	if spy.sendCalls != 0 {
		t.Errorf("SendMessage не должен вызываться при пустом теле; got %d calls", spy.sendCalls)
	}
	if spy.findCalls != 0 {
		t.Errorf("FindRooms не должен вызываться при пустом теле; got %d calls", spy.findCalls)
	}
}

// TestChatSendEmptyFile — пустой --file → ExitGeneric БЕЗ вызова SendMessage
// (аналог пустого stdin, но через файловый путь).
func TestChatSendEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	spy := &chatSpyClient{sendId: 1}
	deps := newChatSendDeps(spy, strings.NewReader(""))

	ee := chatSendHandler(context.Background(), deps, []string{"TOK", "--file", path}, false)
	if ee.Code != ExitGeneric {
		t.Fatalf("code: got %d, want %d (ExitGeneric)", ee.Code, ExitGeneric)
	}
	if spy.sendCalls != 0 {
		t.Errorf("SendMessage не должен вызываться при пустом --file; got %d calls", spy.sendCalls)
	}
}

// TestChatSendClientError — SendMessage вернул ошибку (имитация OCS-error от
// сервера, например невалидный replyTo) → ExitGeneric, обёрнутка сохранена.
func TestChatSendClientError(t *testing.T) {
	sentinel := fmt.Errorf("ocs: invalid replyTo")
	spy := &chatSpyClient{sendErr: sentinel}
	deps := newChatSendDeps(spy, strings.NewReader("payload"))

	ee := chatSendHandler(context.Background(), deps, []string{"TOK"}, false)
	if ee.Code != ExitGeneric {
		t.Fatalf("code: got %d, want %d", ee.Code, ExitGeneric)
	}
	if ee.Err != sentinel {
		t.Errorf("err: got %v, want %v", ee.Err, sentinel)
	}
}

// TestChatSendNameResolves — --name с 1 совпадением → SendMessage зовётся с
// токеном найденной комнаты (интеграция с ResolveRoom).
func TestChatSendNameResolves(t *testing.T) {
	spy := &chatSpyClient{
		findRooms: []client.Room{{Type: 1, Token: "NAMETOKEN", DisplayName: "Project", ActorId: "u-1"}},
		sendId:    5,
	}
	deps := newChatSendDeps(spy, strings.NewReader("hi"))

	ee := chatSendHandler(context.Background(), deps, []string{"--name", "proj"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	if spy.gotSendToken != "NAMETOKEN" {
		t.Errorf("token: got %q, want %q (from --name resolution)", spy.gotSendToken, "NAMETOKEN")
	}
}

// TestChatSendInvalidReplyTo — нечисловой --reply-to → ExitGeneric без вызова
// SendMessage (ошибка парсинга, никакого сетевого запроса).
func TestChatSendInvalidReplyTo(t *testing.T) {
	spy := &chatSpyClient{sendId: 1}
	deps := newChatSendDeps(spy, strings.NewReader("x"))

	ee := chatSendHandler(context.Background(), deps,
		[]string{"TOK", "--reply-to", "not-a-number"}, false)
	if ee.Code != ExitGeneric {
		t.Fatalf("code: got %d, want %d", ee.Code, ExitGeneric)
	}
	if spy.sendCalls != 0 {
		t.Errorf("SendMessage не должен вызываться при невалидном --reply-to; got %d calls", spy.sendCalls)
	}
}

// TestChatSendReplyToNonPositive — --reply-to 0 и отрицательные значения
// отсекаются ДО сетевого вызова: неположительный id сообщения лишён смысла,
// сервер ответил бы 4xx. Покрывает guard, добавленный по замечанию review.
func TestChatSendReplyToNonPositive(t *testing.T) {
	for _, raw := range []string{"0", "-5"} {
		spy := &chatSpyClient{sendId: 1}
		deps := newChatSendDeps(spy, strings.NewReader("payload"))

		ee := chatSendHandler(context.Background(), deps,
			[]string{"TOK", "--reply-to", raw}, false)
		if ee.Code != ExitGeneric {
			t.Fatalf("--reply-to %s: code got %d, want %d", raw, ee.Code, ExitGeneric)
		}
		if ee.Err == nil {
			t.Fatalf("--reply-to %s: err nil, want non-nil", raw)
		}
		if spy.sendCalls != 0 {
			t.Errorf("--reply-to %s: SendMessage не должен вызываться; got %d calls", raw, spy.sendCalls)
		}
	}
}

// TestChatSendStdinTrimsTrailingNewline — `echo "hi" | nctalk chat send`
// отправляет "hi\n"; один завершающий перевод строки срезается, до сервера
// доходит ровно "hi". Многострочные тела (с \n внутри) сохраняются.
func TestChatSendStdinTrimsTrailingNewline(t *testing.T) {
	// Unix trailing newline.
	spy := &chatSpyClient{sendId: 1}
	deps := newChatSendDeps(spy, strings.NewReader("hi\n"))

	ee := chatSendHandler(context.Background(), deps, []string{"TOK"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("unix newline: code got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	if spy.gotSendOpts.Message != "hi" {
		t.Errorf("unix newline: Message got %q, want %q (trailing \\n не срезан)", spy.gotSendOpts.Message, "hi")
	}

	// CRLF trailing newline.
	spy2 := &chatSpyClient{sendId: 1}
	deps2 := newChatSendDeps(spy2, strings.NewReader("hi\r\n"))
	ee = chatSendHandler(context.Background(), deps2, []string{"TOK"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("crlf newline: code got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	if spy2.gotSendOpts.Message != "hi" {
		t.Errorf("crlf newline: Message got %q, want %q", spy2.gotSendOpts.Message, "hi")
	}

	// Многострочное тело: внутренние переводы строк сохраняются, один trailing — срезается.
	spy3 := &chatSpyClient{sendId: 1}
	deps3 := newChatSendDeps(spy3, strings.NewReader("line1\nline2\n"))
	ee = chatSendHandler(context.Background(), deps3, []string{"TOK"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("multiline: code got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	if want := "line1\nline2"; spy3.gotSendOpts.Message != want {
		t.Errorf("multiline: Message got %q, want %q (внутренние \\n должны сохраниться)", spy3.gotSendOpts.Message, want)
	}
}

// -----------------------------------------------------------------------------
// chat edit (дизайн 2026-09-08)
// -----------------------------------------------------------------------------

// TestChatEdit — table-driven по контракту `chat edit` (дизайн §2/§6): вывод id
// (текстовый режим), пустое тело и невалидный messageId — без вызова клиента,
// неизвестный флаг, --name (1/0/2 совпадения → 0/2/3), OCS 404 → 2, OCS 403 → 1,
// лишние позиционные игнорируются, трим одного trailing newline, --file, =формы.
// JSON-ветка — отдельно в TestChatEdit_JSONOutput (writeJSON печатает с отступами,
// точное строковое сравнение в таблице хрупко).
func TestChatEdit(t *testing.T) {
	cases := []struct {
		name          string
		args          []string
		stdin         string
		editId        int
		editErr       error
		findRooms     []client.Room
		wantCode      int
		wantEditCalls int
		wantFindCalls int    // FindRooms: 1 для кейсов с --name (ResolveRoom зовёт поиск), иначе 0
		wantStdout    string // точное совпадение (пустая строка — проверить как пустое)
		checkStdout   bool
		wantErrSubstr string // подстрока в ee.Err ("" — не проверять)
	}{
		{
			name: "stdin тело → успех, stdout id", args: []string{"TOK", "2927"}, stdin: "исправлено",
			editId: 555, wantCode: ExitOK, wantEditCalls: 1, wantStdout: "555\n", checkStdout: true,
		},
		{
			name: "трейлинг \\n срезается", args: []string{"TOK", "2927"}, stdin: "hi\n",
			editId: 1, wantCode: ExitOK, wantEditCalls: 1,
		},
		{
			name: "многострочное тело сохраняется", args: []string{"TOK", "2927"}, stdin: "line1\nline2\n",
			editId: 1, wantCode: ExitOK, wantEditCalls: 1,
		},
		{
			name: "--name= форма + позиционный messageId", args: []string{"2927", "--name=proj"}, stdin: "x",
			findRooms: []client.Room{{Type: 1, Token: "NAMETOK", DisplayName: "Project", ActorId: "u-1"}},
			editId:    7, wantCode: ExitOK, wantEditCalls: 1, wantFindCalls: 1,
		},
		{
			name: "лишние позиционные игнорируются", args: []string{"TOK", "2927", "junk"}, stdin: "x",
			editId: 3, wantCode: ExitOK, wantEditCalls: 1,
		},
		{
			name: "пустое тело → exit 1 без сети", args: []string{"TOK", "2927"}, stdin: "",
			wantCode: ExitGeneric, wantEditCalls: 0, wantErrSubstr: "пусто",
		},
		{
			name: "тело только \\n → exit 1", args: []string{"TOK", "2927"}, stdin: "\n",
			wantCode: ExitGeneric, wantEditCalls: 0, wantErrSubstr: "пусто",
		},
		{
			name: "messageId не число → exit 1 до сети", args: []string{"TOK", "abc"}, stdin: "x",
			wantCode: ExitGeneric, wantEditCalls: 0, wantErrSubstr: "целое число",
		},
		{
			name: "messageId <= 0 → exit 1 до сети", args: []string{"TOK", "0"}, stdin: "x",
			wantCode: ExitGeneric, wantEditCalls: 0, wantErrSubstr: "положительное",
		},
		{
			name: "неизвестный флаг → exit 1", args: []string{"TOK", "2927", "--silent"}, stdin: "x",
			wantCode: ExitGeneric, wantEditCalls: 0, wantErrSubstr: "неизвестный флаг",
		},
		{
			name: "1 позиционный без --name → exit 1", args: []string{"2927"}, stdin: "x",
			wantCode: ExitGeneric, wantEditCalls: 0, wantErrSubstr: "ожидается <room> <messageId>",
		},
		{
			name: "0 позиционных без --name → exit 1", args: []string{}, stdin: "x",
			wantCode: ExitGeneric, wantEditCalls: 0, wantErrSubstr: "ожидается <room> <messageId> или --name",
		},
		{
			name: "0 позиционных с --name → exit 1 (нет messageId)", args: []string{"--name", "proj"}, stdin: "x",
			wantCode: ExitGeneric, wantEditCalls: 0, wantErrSubstr: "ожидается <room> <messageId> или --name",
		},
		{
			name: "--name 0 совпадений → exit 2", args: []string{"2927", "--name", "нету"}, stdin: "x",
			findRooms: []client.Room{}, wantCode: ExitNotFound, wantEditCalls: 0, wantFindCalls: 1,
		},
		{
			name: "--name 2 совпадения → exit 3", args: []string{"2927", "--name", "тест"}, stdin: "x",
			findRooms: []client.Room{
				{Type: 1, Token: "T1", DisplayName: "Первый", ActorId: "u-a"},
				{Type: 2, Token: "T2", DisplayName: "Второй", ActorId: "u-b"},
			},
			wantCode: ExitAmbiguous, wantEditCalls: 0, wantFindCalls: 1,
		},
		{
			name: "OCS 404 от EditMessage → exit 2", args: []string{"TOK", "2927"}, stdin: "x",
			editErr: &client.OCSError{Code: 404, Message: "message not found"},
			wantCode: ExitNotFound, wantEditCalls: 1,
		},
		{
			name: "OCS 403 от EditMessage → exit 1 с текстом сервера", args: []string{"TOK", "2927"}, stdin: "x",
			editErr: &client.OCSError{Code: 403, Message: "not allowed to edit"},
			wantCode: ExitGeneric, wantEditCalls: 1, wantErrSubstr: "not allowed to edit",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := &chatSpyClient{editId: tc.editId, editErr: tc.editErr, findRooms: tc.findRooms}
			deps := newChatSendDeps(spy, strings.NewReader(tc.stdin))

			ee := chatEditHandler(context.Background(), deps, tc.args, false)
			if ee.Code != tc.wantCode {
				t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, tc.wantCode, ee.Err)
			}
			if spy.editCalls != tc.wantEditCalls {
				t.Errorf("EditMessage calls: got %d, want %d", spy.editCalls, tc.wantEditCalls)
			}
			// wantFindCalls, а не «0 при wantEditCalls==0»: кейсы --name с 0/2
			// совпадениями требуют вызова FindRooms (это единственный источник
			// кодов 2/3) — безликая проверка «FindRooms не звался» ложно ругалась
			// бы на корректной реализации.
			if spy.findCalls != tc.wantFindCalls {
				t.Errorf("FindRooms calls: got %d, want %d", spy.findCalls, tc.wantFindCalls)
			}
			if tc.wantErrSubstr != "" && (ee.Err == nil || !strings.Contains(ee.Err.Error(), tc.wantErrSubstr)) {
				t.Errorf("err: got %v, want содержит %q", ee.Err, tc.wantErrSubstr)
			}
			if tc.checkStdout {
				if out := deps.Stdout.(*bytes.Buffer).String(); out != tc.wantStdout {
					t.Errorf("stdout: got %q, want %q", out, tc.wantStdout)
				}
			}
		})
	}
}

// TestChatEdit_PassesArguments — позиционные <room> <messageId> и тело доходят
// до EditMessage; id вывода берётся из ответа клиента (не эхо входного).
func TestChatEdit_PassesArguments(t *testing.T) {
	spy := &chatSpyClient{editId: 999}
	deps := newChatSendDeps(spy, strings.NewReader("исправлено"))

	ee := chatEditHandler(context.Background(), deps, []string{"TOK", "2927"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	if spy.gotEditToken != "TOK" {
		t.Errorf("token: got %q, want %q", spy.gotEditToken, "TOK")
	}
	if spy.gotEditId != 2927 {
		t.Errorf("messageId: got %d, want 2927", spy.gotEditId)
	}
	if spy.gotEditOpts.Message != "исправлено" {
		t.Errorf("Message: got %q, want %q", spy.gotEditOpts.Message, "исправлено")
	}
	if out := deps.Stdout.(*bytes.Buffer).String(); out != "999\n" {
		t.Errorf("stdout: got %q, want %q (id из ответа клиента)", out, "999\n")
	}
}

// TestChatEdit_JSONOutput — jsonOut=true → в stdout валидный JSON {"id": <int>}
// (render.NewMessageIDJSON, тот же вывод, что у chat send; проверка через
// json.Unmarshal — по образцу TestChatSendStdinJSON).
func TestChatEdit_JSONOutput(t *testing.T) {
	spy := &chatSpyClient{editId: 42}
	deps := newChatSendDeps(spy, strings.NewReader("json body"))

	ee := chatEditHandler(context.Background(), deps, []string{"TOK", "2927"}, true)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	raw := deps.Stdout.(*bytes.Buffer).Bytes()
	var got struct {
		Id int `json:"id"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("stdout не валидный JSON: %v; raw=%s", err, raw)
	}
	if got.Id != 42 {
		t.Errorf("json.id: got %d, want 42", got.Id)
	}
}

// TestChatEdit_TrimsOneTrailingNewline — срезается РОВНО ОДИН завершающий
// \n / \r\n; внутренние переводы строк сохраняются (идентично chat send).
func TestChatEdit_TrimsOneTrailingNewline(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stdin string
		want  string
	}{
		{"unix", "hi\n", "hi"},
		{"crlf", "hi\r\n", "hi"},
		{"multiline", "line1\nline2\n", "line1\nline2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := &chatSpyClient{editId: 1}
			deps := newChatSendDeps(spy, strings.NewReader(tc.stdin))

			ee := chatEditHandler(context.Background(), deps, []string{"TOK", "2927"}, false)
			if ee.Code != ExitOK {
				t.Fatalf("%s: code got %d, want %d (err=%v)", tc.name, ee.Code, ExitOK, ee.Err)
			}
			if spy.gotEditOpts.Message != tc.want {
				t.Errorf("%s: Message got %q, want %q", tc.name, spy.gotEditOpts.Message, tc.want)
			}
		})
	}
}

// TestChatEdit_File — тело из --file <path> (временный файл), stdin не читается.
func TestChatEdit_File(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")
	if err := os.WriteFile(path, []byte("new-body"), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	spy := &chatSpyClient{editId: 5}
	deps := newChatSendDeps(spy, strings.NewReader(""))

	ee := chatEditHandler(context.Background(), deps, []string{"TOK", "2927", "--file", path}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	if spy.editCalls != 1 || spy.gotEditOpts.Message != "new-body" {
		t.Errorf("EditMessage: calls=%d Message=%q, want 1 и %q", spy.editCalls, spy.gotEditOpts.Message, "new-body")
	}
}

// TestChatEdit_NamePositionalConflict — заданы и token, и --name → приоритет у
// token, --name игнорируется с предупреждением в stderr (поведение ResolveRoom).
func TestChatEdit_NamePositionalConflict(t *testing.T) {
	spy := &chatSpyClient{editId: 1}
	deps := newChatSendDeps(spy, strings.NewReader("x"))

	ee := chatEditHandler(context.Background(), deps, []string{"TOK", "2927", "--name", "ignored"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	if spy.gotEditToken != "TOK" {
		t.Errorf("token: got %q, want %q (positional выигрывает)", spy.gotEditToken, "TOK")
	}
	if stderr := deps.Stderr.(*bytes.Buffer).String(); !strings.Contains(stderr, "приоритет у token") {
		t.Errorf("stderr должен содержать предупреждение о конфликте; got %q", stderr)
	}
}
