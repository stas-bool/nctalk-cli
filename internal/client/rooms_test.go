package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// roomsTestServer поднимает httptest-сервер, отдавая содержимое
// testdata/rooms_list.json на любой запрос. Возвращает сервер и функцию
// очистки. Фикстура лежит в корневом testdata/ (рядом с go.mod), поэтому из
// директории пакета путь — ../../testdata/rooms_list.json.
func roomsTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "rooms_list.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
}

// TestListRooms_AllFieldsMapped проверяет, что без фильтров возвращаются все
// «активные» комнаты (типы 1/2/3, но НЕ 4/5/6 — они скрыты по умолчанию) и все
// ключевые поля смаппились: Type, Token, DisplayName, UnreadMessages, ActorType,
// ActorId (для type=1 — собеседник, а не текущий пользователь), LastMessage
// (включая вложенный Message.MessageParameters).
func TestListRooms_AllFieldsMapped(t *testing.T) {
	ts := roomsTestServer(t)
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	rooms, err := c.ListRooms(context.Background(), ListRoomsOpts{})
	if err != nil {
		t.Fatalf("ListRooms: %v", err)
	}

	// В фикстуре 5 комнат, но type=4 (former) по умолчанию скрыт → ожидаем 4.
	if got, want := len(rooms), 4; got != want {
		t.Fatalf("len(rooms) = %d, want %d (former type=4 скрыт по умолчанию)", got, want)
	}

	// Ищем type=1 (one-to-one) — для неё actorId = собеседник ("bob"), а не
	// текущий пользователь ("alice").
	var one *Room
	for i := range rooms {
		if rooms[i].Type == RoomType(1) {
			one = &rooms[i]
			break
		}
	}
	if one == nil {
		t.Fatal("room type=1 не найдена в результате")
	}
	if one.Token != "tok-1to1-bob" {
		t.Errorf("type=1 Token: got %q, want %q", one.Token, "tok-1to1-bob")
	}
	if one.DisplayName != "Bob Bobson" {
		t.Errorf("type=1 DisplayName: got %q, want %q", one.DisplayName, "Bob Bobson")
	}
	if one.UnreadMessages != 3 {
		t.Errorf("type=1 UnreadMessages: got %d, want 3", one.UnreadMessages)
	}
	if one.ActorType != "users" {
		t.Errorf("type=1 ActorType: got %q, want %q", one.ActorType, "users")
	}
	// Ключевая проверка (спека §12): для one-to-one actorId — собеседник.
	if one.ActorId != "bob" {
		t.Errorf("type=1 ActorId: got %q, want %q (должен быть собеседник)", one.ActorId, "bob")
	}
	// LastMessage должен смаппиться как *Message с вложенными параметрами.
	if one.LastMessage == nil {
		t.Fatal("type=1 LastMessage = nil, want *Message")
	}
	if one.LastMessage.Id != 9001 {
		t.Errorf("type=1 LastMessage.Id: got %d, want 9001", one.LastMessage.Id)
	}
	if one.LastMessage.Message != "привет, скинул report.pdf" {
		t.Errorf("type=1 LastMessage.Message: got %q, want %q", one.LastMessage.Message, "привет, скинул report.pdf")
	}
	// Реальный формат v4/room для messageType=comment — МАССИВ (здесь []),
	// поэтому после Unmarshal MsgParams должен быть пустым (параметров нет).
	if got := len(one.LastMessage.MessageParameters); got != 0 {
		t.Errorf("type=1 LastMessage.MessageParameters: got %d элементов, want 0 (comment → массив → пустая карта)",
			got)
	}
}

// TestListRooms_SystemMessageObjectParams проверяет, что для system-сообщений
// в v4/room messageParameters остаётся объектом — это канонический формат для
// подстановки плейсхолдеров. В фикстуре такая комната — "Team Chat" (type=2),
// чьё lastMessage имеет messageType="system" с плейсхолдером {actor}.
func TestListRooms_SystemMessageObjectParams(t *testing.T) {
	ts := roomsTestServer(t)
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	rooms, err := c.ListRooms(context.Background(), ListRoomsOpts{})
	if err != nil {
		t.Fatalf("ListRooms: %v", err)
	}
	var team *Room
	for i := range rooms {
		if rooms[i].Token == "tok-team" {
			team = &rooms[i]
			break
		}
	}
	if team == nil {
		t.Fatal("комната tok-team не найдена в фикстуре")
	}
	if team.LastMessage == nil {
		t.Fatal("tok-team LastMessage = nil")
	}
	if team.LastMessage.MessageType != "system" {
		t.Fatalf("tok-team LastMessage.MessageType: got %q, want %q",
			team.LastMessage.MessageType, "system")
	}
	mp := team.LastMessage.MessageParameters["actor"]
	if mp.Type != "user" || mp.Name != "Alice" {
		t.Errorf("tok-team system message actor param: got %+v, want {Type:user Name:Alice}", mp)
	}
}

// TestListRooms_FilterType проверяет, что opts.Type фильтрует результат
// на клиенте: Type=2 (group) → только групповые комнаты.
func TestListRooms_FilterType(t *testing.T) {
	ts := roomsTestServer(t)
	defer ts.Close()

	group := RoomType(2)
	c := NewTalkClient(testCfg(ts.URL))
	rooms, err := c.ListRooms(context.Background(), ListRoomsOpts{Type: &group})
	if err != nil {
		t.Fatalf("ListRooms: %v", err)
	}
	if len(rooms) != 2 {
		t.Fatalf("len(rooms) with Type=2: got %d, want 2 (Team Chat + Project X)", len(rooms))
	}
	for _, r := range rooms {
		if r.Type != group {
			t.Errorf("Type: got %d, want %d (фильтр по Type=2 нарушен)", r.Type, group)
		}
	}
}

// TestListRooms_FilterUnreadOnly проверяет, что opts.UnreadOnly оставляет
// только комнаты с UnreadMessages > 0 (former-комнаты скрыты, даже если у них
// есть непрочитанные — но в фикстуре у former unread=0, поэтому проверка
// однозначная).
func TestListRooms_FilterUnreadOnly(t *testing.T) {
	ts := roomsTestServer(t)
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	rooms, err := c.ListRooms(context.Background(), ListRoomsOpts{UnreadOnly: true})
	if err != nil {
		t.Fatalf("ListRooms: %v", err)
	}
	// В фикстуре непрочитанные: type=1 (unread=3), type=3 (unread=5),
	// type=2 "Project X" (unread=2). Комната type=4 скрыта former-фильтром.
	if got, want := len(rooms), 3; got != want {
		t.Fatalf("len(rooms) with UnreadOnly=true: got %d, want %d", got, want)
	}
	for _, r := range rooms {
		if r.UnreadMessages <= 0 {
			t.Errorf("UnreadMessages: got %d, want >0 (UnreadOnly нарушен)", r.UnreadMessages)
		}
		// Двойная проверка: former в результате не появилось.
		if r.Type == RoomType(4) || r.Type == RoomType(5) || r.Type == RoomType(6) {
			t.Errorf("former room %d проходит через UnreadOnly без IncludeFormer", r.Type)
		}
	}
}

// TestListRooms_FormerHiddenByDefault подтверждает DoD: типы 4/5/6 скрыты
// по умолчанию. В фикстуре есть type=4 — без IncludeFormer её быть не должно.
func TestListRooms_FormerHiddenByDefault(t *testing.T) {
	ts := roomsTestServer(t)
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	rooms, err := c.ListRooms(context.Background(), ListRoomsOpts{})
	if err != nil {
		t.Fatalf("ListRooms: %v", err)
	}
	for _, r := range rooms {
		if r.Type == RoomType(4) || r.Type == RoomType(5) || r.Type == RoomType(6) {
			t.Errorf("former room прошла без IncludeFormer: type=%d token=%q", r.Type, r.Token)
		}
	}
}

// TestListRooms_FormerIncluded проверяет, что при opts.IncludeFormer=true
// бывшие комнаты (type 4/5/6) попадают в результат.
func TestListRooms_FormerIncluded(t *testing.T) {
	ts := roomsTestServer(t)
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	rooms, err := c.ListRooms(context.Background(), ListRoomsOpts{IncludeFormer: true})
	if err != nil {
		t.Fatalf("ListRooms: %v", err)
	}
	// Все 5 комнат фикстуры.
	if got, want := len(rooms), 5; got != want {
		t.Fatalf("len(rooms) with IncludeFormer=true: got %d, want %d", got, want)
	}
	var sawFormer bool
	for _, r := range rooms {
		if r.Type == RoomType(4) {
			sawFormer = true
			if r.Token != "tok-former-1to1" {
				t.Errorf("former Token: got %q, want %q", r.Token, "tok-former-1to1")
			}
			if r.ActorId != "carol" {
				t.Errorf("former ActorId: got %q, want %q", r.ActorId, "carol")
			}
		}
	}
	if !sawFormer {
		t.Error("former room type=4 не найдена в результате с IncludeFormer=true")
	}
}

// TestFindRooms проверяет клиентскую фильтрацию FindRooms: подстрока по
// DisplayName (case-insensitive) + необязательный точный фильтр по ActorId.
// Список комнат берётся из той же фикстуры testdata/rooms_list.json, что и в
// TestListRooms* (5 комнат, с IncludeFormer=true видны все).
//
// FindRooms возвращает ВСЕ совпадения (слой «exit 3/неоднозначно» — это cli, не
// клиент); пустой результат — пустой срез и nil error.
func TestFindRooms(t *testing.T) {
	ts := roomsTestServer(t)
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))

	tests := []struct {
		name       string
		query      string
		actorId    string
		wantLen    int
		wantTokens []string // ожидаемый набор токенов (порядок не важен)
	}{
		{
			// "bob" (lower) содержится только в "Bob Bobson".
			name:       "substring matches exactly one room",
			query:      "bob",
			actorId:    "",
			wantLen:    1,
			wantTokens: []string{"tok-1to1-bob"},
		},
		{
			// "p" (lower) содержится в "public room" и "project x", но НЕ в
			// "bob bobson" / "team chat" / "former 1:1" → ровно 2 совпадения.
			// Не ошибка — клиент отдаёт срез как есть.
			name:       "substring matches two rooms is not an error",
			query:      "p",
			actorId:    "",
			wantLen:    2,
			wantTokens: []string{"tok-public", "tok-projx"},
		},
		{
			// Пустой query → проходят все по подстроке; фильтр по actorId="bob"
			// оставляет только 1:1 с Бобом (спека §8: --user принимает actorId).
			name:       "actorId filter keeps only bob rooms",
			query:      "",
			actorId:    "bob",
			wantLen:    1,
			wantTokens: []string{"tok-1to1-bob"},
		},
		{
			// Ничего не найдено → пустой срез (не nil), nil error.
			name:    "no match returns empty slice and nil error",
			query:   "zzz-not-found",
			actorId: "",
			wantLen: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rooms, err := c.FindRooms(context.Background(), tc.query, tc.actorId)
			if err != nil {
				t.Fatalf("FindRooms(%q, %q): got error %v, want nil", tc.query, tc.actorId, err)
			}
			if got := len(rooms); got != tc.wantLen {
				t.Fatalf("len(rooms) = %d, want %d (query=%q actorId=%q)", got, tc.wantLen, tc.query, tc.actorId)
			}
			// Контракт «Empty → пустой срез, не ошибка»: пустой результат должен
			// быть именно ненулевым пустым срезом, не nil.
			if tc.wantLen == 0 && rooms == nil {
				t.Errorf("rooms = nil, want non-nil empty slice")
			}
			// Проверка набора токенов (порядок не важен).
			if tc.wantTokens != nil {
				got := make(map[string]bool, len(rooms))
				for _, r := range rooms {
					got[r.Token] = true
				}
				for _, tok := range tc.wantTokens {
					if !got[tok] {
						t.Errorf("ожидался token %q в результате; фактические комнаты = %#v", tok, rooms)
					}
				}
			}
			// При заданном actorId ВСЕ возвращённые комнаты обязаны иметь его.
			if tc.actorId != "" {
				for _, r := range rooms {
					if r.ActorId != tc.actorId {
						t.Errorf("ActorId: got %q, want %q (фильтр по actorId нарушен)", r.ActorId, tc.actorId)
					}
				}
			}
		})
	}
}

// TestListRooms_FormerIncludedAndUnreadOnly проверяет комбинированный сценарий:
// IncludeFormer=true + UnreadOnly=true. В фикстуре у former (type=4)
// unread=0, поэтому она не должна появиться. Это страховка от регрессии:
// former не должна «проскакивать» только из-за IncludeFormer.
func TestListRooms_FormerIncludedAndUnreadOnly(t *testing.T) {
	ts := roomsTestServer(t)
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	rooms, err := c.ListRooms(context.Background(), ListRoomsOpts{IncludeFormer: true, UnreadOnly: true})
	if err != nil {
		t.Fatalf("ListRooms: %v", err)
	}
	// type=4 имеет unread=0 → не проходит Unread-фильтр; остальные former
	// (5/6) в фикстуре отсутствуют → ожидаем те же 3 комнаты, что и без
	// IncludeFormer.
	if got, want := len(rooms), 3; got != want {
		t.Fatalf("len(rooms) with IncludeFormer+UnreadOnly: got %d, want %d", got, want)
	}
}
