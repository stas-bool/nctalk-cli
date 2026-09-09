package cli

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stas-bool/nctalk-cli/internal/client"
)

// roomMockClient — заглушка TalkClient для тестов обёртки cli.ResolveRoom.
// Нужен только FindRooms (через него проходит разрешение --name); остальные
// методы паникуют — если ResolveRoom вдруг начнёт их звать, тест упадёт громко.
//
// Большая часть assertion-логики (Status/Candidates) мигрировала в
// internal/room/room_test.go — там тестируется чистая логика разрешения без
// печати/exit-кодов. Здесь оставлен минимальный smoke-test обёртки: печать
// кандидатов в stderr через render.Candidates и маппинг Status → exit-код.
type roomMockClient struct {
	// rooms/err — что вернуть из FindRooms.
	rooms []client.Room
	err   error
	// findCalls — счётчик вызовов FindRooms.
	findCalls int
	// lastQuery/lastActorId — аргументы последнего вызова FindRooms.
	lastQuery   string
	lastActorId string
}

func (m *roomMockClient) FindRooms(_ context.Context, query, actorId string) ([]client.Room, error) {
	m.findCalls++
	m.lastQuery = query
	m.lastActorId = actorId
	return m.rooms, m.err
}

func (m *roomMockClient) ListRooms(_ context.Context, _ client.ListRoomsOpts) ([]client.Room, error) {
	panic("ListRooms: не должен вызываться из ResolveRoom")
}
func (m *roomMockClient) SearchRooms(_ context.Context, _ string, _ int) ([]client.ConversationResult, error) {
	panic("SearchRooms: не должен вызываться из ResolveRoom")
}
func (m *roomMockClient) GetChat(_ context.Context, _ string, _ client.GetChatOpts) ([]client.Message, error) {
	panic("GetChat: не должен вызываться из ResolveRoom")
}
func (m *roomMockClient) SendMessage(_ context.Context, _ string, _ client.SendMessageOpts) (int, error) {
	panic("SendMessage: не должен вызываться из ResolveRoom")
}
func (m *roomMockClient) EditMessage(_ context.Context, _ string, _ int, _ client.EditMessageOpts) (int, error) {
	return 0, errMock
}
func (m *roomMockClient) GetReactions(_ context.Context, _ string, _ int) (map[string][]client.ReactionActor, error) {
	panic("GetReactions: не должен вызываться из ResolveRoom")
}
func (m *roomMockClient) SearchMessages(_ context.Context, _ string, _ client.SearchMessagesOpts) ([]client.MessageResult, error) {
	panic("SearchMessages: не должен вызываться из ResolveRoom")
}

// compile-time проверка: roomMockClient реализует TalkClient.
var _ TalkClient = (*roomMockClient)(nil)

// exitCode извлекает Code из ошибки, если это ExitError; иначе возвращает -1
// (сигнал «не ExitError»).
func exitCode(err error) int {
	var ee ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	return -1
}

// TestResolveRoomWrapper_AmbiguousPrintsCandidates — основной regression-тест
// обёртки: при StatusAmbiguous (из room.ResolveRoom) обёртка должна напечатать
// список кандидатов в stderr через render.Candidates (ТИП\tимя\ttoken) и
// вернуть ExitError{ExitAmbiguous}. Этот текстовый контракт используют
// handler-ы (handlers_chat.go, handlers_reactions.go) — он должен сохраняться.
func TestResolveRoomWrapper_AmbiguousPrintsCandidates(t *testing.T) {
	roomA := client.Room{Type: 1, Token: "tokA", DisplayName: "Первый", ActorId: "alice"}
	roomB := client.Room{Type: 2, Token: "tokB", DisplayName: "Второй", ActorId: "bob"}
	mock := &roomMockClient{rooms: []client.Room{roomA, roomB}}
	stderr := &bytes.Buffer{}

	got, err := ResolveRoom(context.Background(), mock, "", "тест", stderr)

	if got != "" {
		t.Errorf("token: got %q, want empty (неоднозначность)", got)
	}
	if code := exitCode(err); code != ExitAmbiguous {
		t.Fatalf("exit code: got %d, want %d (ExitAmbiguous); err=%v", code, ExitAmbiguous, err)
	}
	if msg := err.Error(); msg != "неоднозначное имя комнаты: тест" {
		t.Errorf("текст ошибки: got %q, want %q", msg, "неоднозначное имя комнаты: тест")
	}

	// render.Candidates пишет строки вида "<ТИП>\t<имя>\t<token>". Проверяем
	// подстроки: обе комнаты должны попасть в вывод.
	out := stderr.String()
	for _, want := range []string{"Первый", "tokA", "Второй", "tokB"} {
		if !bytes.Contains(stderr.Bytes(), []byte(want)) {
			t.Errorf("stderr не содержит %q; полный вывод:\n%s", want, out)
		}
	}
}

// TestResolveRoomWrapper_PositionalAndNameConflictWarning — regression-тест
// предупреждения о конфликте: при заданных И positional, И --name обёртка
// пишет в stderr строку про приоритет token, --name игнорируется. Чистая
// room.ResolveRoom этого не делает.
func TestResolveRoomWrapper_PositionalAndNameConflictWarning(t *testing.T) {
	roomA := client.Room{Type: 1, Token: "tokA", DisplayName: "Первый"}
	mock := &roomMockClient{rooms: []client.Room{roomA, roomA}} // если бы звякнул — был бы ambiguous
	stderr := &bytes.Buffer{}

	got, err := ResolveRoom(context.Background(), mock, "abc", "что-то", stderr)
	if err != nil {
		t.Fatalf("err: got %v, want nil (приоритет positional — не ошибка)", err)
	}
	if got != "abc" {
		t.Errorf("token: got %q, want %q", got, "abc")
	}
	if mock.findCalls != 0 {
		t.Errorf("FindRooms calls: got %d, want 0 (--name проигнорирован)", mock.findCalls)
	}
	// Предупреждение должно упомянуть оба значения.
	out := stderr.String()
	if !bytes.Contains(stderr.Bytes(), []byte("abc")) || !bytes.Contains(stderr.Bytes(), []byte("что-то")) {
		t.Errorf("stderr должен содержать предупреждение про token и --name; got %q", out)
	}
}
