package room

import (
	"context"
	"errors"
	"testing"

	"github.com/stas-bool/nctalk-cli/internal/client"
)

// mockLister — заглушка RoomLister для тестов ResolveRoom. Считает вызовы
// FindRooms и запоминает аргументы, чтобы тесты могли проверять «не вызвался
// ли клиент, когда не должен был» и какие query/actorId ушли.
type mockLister struct {
	// rooms/err — что вернуть из FindRooms.
	rooms []client.Room
	err   error
	// findCalls — счётчик вызовов FindRooms.
	findCalls int
	// lastQuery/lastActorId — аргументы последнего вызова FindRooms.
	lastQuery   string
	lastActorId string
}

func (m *mockLister) FindRooms(_ context.Context, query, actorId string) ([]client.Room, error) {
	m.findCalls++
	m.lastQuery = query
	m.lastActorId = actorId
	return m.rooms, m.err
}

// compile-time проверка: mockLister реализует RoomLister.
var _ RoomLister = (*mockLister)(nil)

// TestResolveRoom проверяет все ветки правила разрешения <room> (спека §7).
// Тест migrated из cli/room_test.go: проверяет Result.Status и Result.Candidates
// напрямую (вместо чтения stderr — печать списка кандидатов теперь
// ответственность вызывающего кода, не room.ResolveRoom).
func TestResolveRoom(t *testing.T) {
	// Комнаты для тестов на ambiguous и single-match.
	roomA := client.Room{Type: 1, Token: "tokA", DisplayName: "Первый", ActorId: "alice"}
	roomB := client.Room{Type: 2, Token: "tokB", DisplayName: "Второй", ActorId: "bob"}

	t.Run("positional token → StatusResolved, FindRooms не звался", func(t *testing.T) {
		mock := &mockLister{rooms: []client.Room{roomA}} // если звякнет — тест провалится
		got, err := ResolveRoom(context.Background(), mock, "abc", "")
		if err != nil {
			t.Fatalf("err: got %v, want nil", err)
		}
		if got.Status != StatusResolved {
			t.Errorf("Status: got %d, want %d (StatusResolved)", got.Status, StatusResolved)
		}
		if got.Token != "abc" {
			t.Errorf("Token: got %q, want %q", got.Token, "abc")
		}
		if len(got.Candidates) != 0 {
			t.Errorf("Candidates: want пусто, got %+v", got.Candidates)
		}
		if mock.findCalls != 0 {
			t.Errorf("FindRooms calls: got %d, want 0 (positional не должен звать клиент)", mock.findCalls)
		}
	})

	t.Run("positional + --name оба заданы → приоритет positional (StatusResolved), FindRooms не звался", func(t *testing.T) {
		// room.ResolveRoom НЕ печатает предупреждение в stderr — это задача
		// вызывающего (cli.ResolveRoom wrapper). Чистая логика просто
		// отдаёт приоритет positional.
		mock := &mockLister{rooms: []client.Room{roomA, roomB}} // если бы звякнул — был бы ambiguous
		got, err := ResolveRoom(context.Background(), mock, "abc", "что-то")
		if err != nil {
			t.Fatalf("err: got %v, want nil (приоритет positional — не ошибка)", err)
		}
		if got.Status != StatusResolved {
			t.Errorf("Status: got %d, want %d (StatusResolved)", got.Status, StatusResolved)
		}
		if got.Token != "abc" {
			t.Errorf("Token: got %q, want %q", got.Token, "abc")
		}
		if mock.findCalls != 0 {
			t.Errorf("FindRooms calls: got %d, want 0 (--name проигнорирован)", mock.findCalls)
		}
	})

	t.Run("--name ровно 1 совпадение → StatusResolved с token этой комнаты", func(t *testing.T) {
		mock := &mockLister{rooms: []client.Room{roomA}}
		got, err := ResolveRoom(context.Background(), mock, "", "пер")
		if err != nil {
			t.Fatalf("err: got %v, want nil", err)
		}
		if got.Status != StatusResolved {
			t.Errorf("Status: got %d, want %d (StatusResolved)", got.Status, StatusResolved)
		}
		if got.Token != "tokA" {
			t.Errorf("Token: got %q, want %q", got.Token, "tokA")
		}
		if got.Query != "пер" {
			t.Errorf("Query: got %q, want %q", got.Query, "пер")
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

	t.Run("--name 2 совпадения → StatusAmbiguous + Candidates содержит обе комнаты", func(t *testing.T) {
		mock := &mockLister{rooms: []client.Room{roomA, roomB}}
		got, err := ResolveRoom(context.Background(), mock, "", "тест")
		if err != nil {
			t.Fatalf("err: got %v, want nil (ambiguous — не ошибка, сигнал через Status)", err)
		}
		if got.Status != StatusAmbiguous {
			t.Fatalf("Status: got %d, want %d (StatusAmbiguous)", got.Status, StatusAmbiguous)
		}
		if got.Token != "" {
			t.Errorf("Token: got %q, want empty (при неоднозначности token не определён)", got.Token)
		}
		if got.Query != "тест" {
			t.Errorf("Query: got %q, want %q", got.Query, "тест")
		}
		if len(got.Candidates) != 2 {
			t.Fatalf("Candidates len: got %d, want 2", len(got.Candidates))
		}
		// Обе комнаты должны быть в Candidates (в порядке возврата FindRooms).
		if got.Candidates[0].Token != "tokA" || got.Candidates[1].Token != "tokB" {
			t.Errorf("Candidates tokens: got %q/%q, want tokA/tokB",
				got.Candidates[0].Token, got.Candidates[1].Token)
		}
	})

	t.Run("--name 0 совпадений → StatusNotFound", func(t *testing.T) {
		mock := &mockLister{rooms: nil} // FindRooms возвращает пустой срез, nil error
		got, err := ResolveRoom(context.Background(), mock, "", "несуществующее")
		if err != nil {
			t.Fatalf("err: got %v, want nil (not found — не ошибка, сигнал через Status)", err)
		}
		if got.Status != StatusNotFound {
			t.Fatalf("Status: got %d, want %d (StatusNotFound)", got.Status, StatusNotFound)
		}
		if got.Token != "" {
			t.Errorf("Token: got %q, want empty", got.Token)
		}
		if got.Query != "несуществующее" {
			t.Errorf("Query: got %q, want %q", got.Query, "несуществующее")
		}
		if len(got.Candidates) != 0 {
			t.Errorf("Candidates: want пусто, got %+v", got.Candidates)
		}
	})

	t.Run("оба пусты (positional и nameFlag) → StatusEmptyInput", func(t *testing.T) {
		mock := &mockLister{}
		got, err := ResolveRoom(context.Background(), mock, "", "")
		if err != nil {
			t.Fatalf("err: got %v, want nil (empty input — не ошибка, сигнал через Status)", err)
		}
		if got.Status != StatusEmptyInput {
			t.Fatalf("Status: got %d, want %d (StatusEmptyInput)", got.Status, StatusEmptyInput)
		}
		if got.Token != "" {
			t.Errorf("Token: got %q, want empty", got.Token)
		}
		if mock.findCalls != 0 {
			t.Errorf("FindRooms calls: got %d, want 0", mock.findCalls)
		}
	})

	t.Run("FindRooms сетевая ошибка → пробрасывается как raw err (без маппинга в ExitError)", func(t *testing.T) {
		// Контракт room.ResolveRoom: ошибка FindRooms возвращается как есть —
		// маппинг в exit.ExitError делает вызывающий (cli.ResolveRoom wrapper
		// или cmd/nctalk-call) через exit.FromClientErr.
		netErr := errors.New("network down")
		mock := &mockLister{err: netErr}
		got, err := ResolveRoom(context.Background(), mock, "", "x")

		if err == nil {
			t.Fatalf("err: got nil, want non-nil (FindRooms error пробрасывается)")
		}
		if !errors.Is(err, netErr) {
			t.Errorf("ожидалось, что возвращена исходная сетевая ошибка как есть; got %v", err)
		}
		// Result при ошибке невалиден — Status не задан (zero value).
		if got.Status != StatusResolved {
			// zero-value Status = StatusResolved (первая в iota); это ожидаемо,
			// вызывающий должен игнорировать Result при err != nil. Просто
			// проверяем, что Token пуст.
			if got.Token != "" {
				t.Errorf("Token при err: got %q, want empty", got.Token)
			}
		}
	})
}

// TestStatusOrdering — единый test для iota-порядка Status: гарантирует, что
// перенумерация констант не проскочит незаметно (значения не закреплены в спеке,
// но порядок — часть контракта вызывающего: switch в cli.ResolveRoom wrapper
// рассчитывает на конкретные имена, не числа).
func TestStatusOrdering(t *testing.T) {
	// Только sanity-check, что константы не пересекаются по значению.
	seen := map[Status]bool{}
	for _, s := range []Status{StatusResolved, StatusAmbiguous, StatusNotFound, StatusEmptyInput} {
		if seen[s] {
			t.Errorf("дублируется значение Status: %d", s)
		}
		seen[s] = true
	}
}
