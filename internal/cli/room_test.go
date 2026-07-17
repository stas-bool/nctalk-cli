package cli

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stas/nctalk/internal/client"
)

// roomMockClient — заглушка TalkClient для тестов ResolveRoom. Нужен только
// FindRooms: он считает вызовы и запоминает аргументы, чтобы тесты могли
// проверять «не вызвался ли клиент, когда не должен был» и какие query/actorId
// ушли. Остальные методы паникуют — если ResolveRoom вдруг начнёт их звать,
// тест упадёт громко.
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

// TestResolveRoom проверяет все ветки правила разрешения <room> (спека §7).
func TestResolveRoom(t *testing.T) {
	// Комнаты для тестов на ambiguous и single-match.
	roomA := client.Room{Type: 1, Token: "tokA", DisplayName: "Первый", ActorId: "alice"}
	roomB := client.Room{Type: 2, Token: "tokB", DisplayName: "Второй", ActorId: "bob"}

	t.Run("positional token возвращается как есть, FindRooms не звался", func(t *testing.T) {
		mock := &roomMockClient{rooms: []client.Room{roomA}} // если звякнет — тест провалится
		stderr := &bytes.Buffer{}
		got, err := ResolveRoom(context.Background(), mock, "abc", "", stderr)
		if err != nil {
			t.Fatalf("err: got %v, want nil", err)
		}
		if got != "abc" {
			t.Errorf("token: got %q, want %q", got, "abc")
		}
		if mock.findCalls != 0 {
			t.Errorf("FindRooms calls: got %d, want 0 (positional не должен звать клиент)", mock.findCalls)
		}
		if stderr.Len() != 0 {
			t.Errorf("stderr: хотим пусто, got %q", stderr.String())
		}
	})

	t.Run("--name ровно 1 совпадение → token этой комнаты", func(t *testing.T) {
		mock := &roomMockClient{rooms: []client.Room{roomA}}
		stderr := &bytes.Buffer{}
		got, err := ResolveRoom(context.Background(), mock, "", "пер", stderr)
		if err != nil {
			t.Fatalf("err: got %v, want nil", err)
		}
		if got != "tokA" {
			t.Errorf("token: got %q, want %q", got, "tokA")
		}
		if mock.findCalls != 1 {
			t.Errorf("FindRooms calls: got %d, want 1", mock.findCalls)
		}
		if mock.lastQuery != "пер" {
			t.Errorf("FindRooms query: got %q, want %q", mock.lastQuery, "пер")
		}
		if mock.lastActorId != "" {
			t.Errorf("FindRooms actorId: got %q, want empty", mock.lastActorId)
		}
	})

	t.Run("--name 2 совпадения → ExitAmbiguous + кандидаты в stderr", func(t *testing.T) {
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
	})

	t.Run("--name 0 совпадений → ExitNotFound", func(t *testing.T) {
		mock := &roomMockClient{rooms: nil} // FindRooms возвращает пустой срез, nil error
		stderr := &bytes.Buffer{}
		got, err := ResolveRoom(context.Background(), mock, "", "несуществующее", stderr)

		if got != "" {
			t.Errorf("token: got %q, want empty", got)
		}
		if code := exitCode(err); code != ExitNotFound {
			t.Fatalf("exit code: got %d, want %d (ExitNotFound); err=%v", code, ExitNotFound, err)
		}
		if msg := err.Error(); msg != "комната не найдена: несуществующее" {
			t.Errorf("текст ошибки: got %q, want %q", msg, "комната не найдена: несуществующее")
		}
		// При not found кандидатов нет — stderr пуст.
		if stderr.Len() != 0 {
			t.Errorf("stderr: хотим пусто, got %q", stderr.String())
		}
	})

	t.Run("оба пусты → ExitGeneric", func(t *testing.T) {
		mock := &roomMockClient{}
		stderr := &bytes.Buffer{}
		got, err := ResolveRoom(context.Background(), mock, "", "", stderr)

		if got != "" {
			t.Errorf("token: got %q, want empty", got)
		}
		if code := exitCode(err); code != ExitGeneric {
			t.Fatalf("exit code: got %d, want %d (ExitGeneric); err=%v", code, ExitGeneric, err)
		}
		if msg := err.Error(); msg != "укажите token позиционно или --name" {
			t.Errorf("текст ошибки: got %q, want %q", msg, "укажите token позиционно или --name")
		}
		if mock.findCalls != 0 {
			t.Errorf("FindRooms calls: got %d, want 0", mock.findCalls)
		}
	})

	t.Run("positional + --name оба заданы → приоритет positional, --name игнорируется, не ошибка", func(t *testing.T) {
		mock := &roomMockClient{rooms: []client.Room{roomA, roomB}} // если бы звякнул — был бы ambiguous
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
	})

	t.Run("FindRooms сетевая ошибка → ExitGeneric", func(t *testing.T) {
		netErr := errors.New("network down")
		mock := &roomMockClient{err: netErr}
		stderr := &bytes.Buffer{}
		got, err := ResolveRoom(context.Background(), mock, "", "x", stderr)

		if got != "" {
			t.Errorf("token: got %q, want empty", got)
		}
		if code := exitCode(err); code != ExitGeneric {
			t.Fatalf("exit code: got %d, want %d (ExitGeneric для сетевой ошибки); err=%v", code, ExitGeneric, err)
		}
		// Ошибка из client-слоя пробрасывается как есть (sanitized).
		if !errors.Is(err, netErr) {
			t.Errorf("ожидалось, что ExitError оборачивает исходную сетевую ошибку; got %v", err)
		}
	})
}
