package client

import (
	"context"
	"net/http"
	"strings"
)

// RoomType — тип комнаты Nextcloud Talk (спека §12):
//   - 1 — one-to-one (диалог на двоих);
//   - 2 — group (групповой чат);
//   - 3 — public (публичная);
//   - 4, 5, 6 — former (покинутые/архивированные варианты 1/2/3 соответственно).
//
// По умолчанию former (4/5/6) скрыты в ListRooms — см. ListRoomsOpts.IncludeFormer.
type RoomType int

// Room — каноническое представление комнаты Nextcloud Talk (спека §12).
// Имена полей соответствуют JSON-ответу /ocs/v2.php/apps/spreed/api/v4/room.
type Room struct {
	Type           RoomType `json:"type"`
	Token          string   `json:"token"`
	DisplayName    string   `json:"displayName"`
	UnreadMessages int      `json:"unreadMessages"`
	ActorType      string   `json:"actorType"`
	// ActorId — actorId ОТВЕТА /v4/room: у всех комнат это ТЕКУЩИЙ
	// пользователь (владелец сессии), НЕ собеседник (живой сервер 2026-09-09).
	ActorId string `json:"actorId"`
	// Name — поле name ответа. Для type=1/4 (one-to-one и её former) содержит
	// actorId СОБЕСЕДНИКА — это то, что ищет rooms find --user. Для групповых/
	// публичных пусто (живой сервер 2026-09-09).
	Name string `json:"name"`
	// LastMessage — последнее сообщение в комнате; nil, если сообщений ещё не
	// было. Тип Message определён в types.go и здесь НЕ дублируется.
	LastMessage *Message `json:"lastMessage,omitempty"`
}

// ListRoomsOpts — параметры ListRooms. Все фильтры применяются на клиенте
// после получения полного списка комнат (спека §6 `rooms list`).
type ListRoomsOpts struct {
	// Type, если не nil, оставляет только комнаты этого типа. На former-комнаты
	// (4/5/6) дополнительно действует IncludeFormer.
	Type *RoomType
	// UnreadOnly — если true, оставить только комнаты с UnreadMessages > 0.
	UnreadOnly bool
	// IncludeFormer — если false (по умолчанию), former-комнаты (type 4/5/6)
	// скрыты; true — показывать.
	IncludeFormer bool
}

// ListRooms возвращает список комнат пользователя (спека §6, §12).
//
// Запрос: GET pathRooms (константа = "/ocs/v2.php/apps/spreed/api/v4/room") —
// это единственный канонический путь ListRooms; вариант v1/room НЕ
// использовать. Заголовки OCS и OCS-конверт обрабатываются в doOCS.
//
// Фильтры opts применяются на клиенте к полученному списку.
func (c *TalkClient) ListRooms(ctx context.Context, opts ListRoomsOpts) ([]Room, error) {
	var rooms []Room
	if _, err := c.doOCS(ctx, http.MethodGet, pathRooms, nil, nil, false, &rooms); err != nil {
		return nil, err
	}
	return filterRooms(rooms, opts), nil
}

// FindRooms возвращает ВСЕ совпадения (одно или несколько). Empty → пустой срез,
// не ошибка (спека §6 `rooms find`).
//
// query — case-insensitive подстрока по DisplayName:
// strings.Contains(strings.ToLower(r.DisplayName), strings.ToLower(query)).
// Пустой query математически совпадает с любой строкой, поэтому сам по себе
// фильтра не делает — удобно для поиска «только по actorId».
//
// actorId — необязательный фильтр собеседника личного чата; при непустом
// дополнительно требуется точное совпадение r.Name == actorId (спека §8:
// --user принимает actorId собеседника; собеседник one-to-one лежит в поле
// name ответа, actorId ответа — всегда текущий пользователь, живой сервер
// 2026-09-09).
//
// Поиск идёт по ВСЕМ комнатам, включая former (типы 4/5/6): список
// запрашивается с IncludeFormer=true. Правило «exit 3 / неоднозначно» — слой
// cli (RoomCommand), здесь НЕ применяется: метод отдаёт срез как есть.
func (c *TalkClient) FindRooms(ctx context.Context, query, actorId string) ([]Room, error) {
	rooms, err := c.ListRooms(ctx, ListRoomsOpts{IncludeFormer: true})
	if err != nil {
		return nil, err
	}

	q := strings.ToLower(query)
	out := make([]Room, 0, len(rooms))
	for _, r := range rooms {
		// Подстрока по DisplayName, case-insensitive.
		if !strings.Contains(strings.ToLower(r.DisplayName), q) {
			continue
		}
		// Точный фильтр по собеседнику 1:1 (поле name) — только когда actorId
		// задан явно.
		if actorId != "" && r.Name != actorId {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// filterRooms применяет клиентские фильтры к списку комнат. Порядок проверок
// фиксирован: former (IncludeFormer) → UnreadOnly → Type. Так former скрыты
// всегда, если не запрошены явно, а прочие фильтры работают по остаточному
// множеству (например, UnreadOnly не «оживит» бывшую комнату без IncludeFormer).
func filterRooms(rooms []Room, opts ListRoomsOpts) []Room {
	out := make([]Room, 0, len(rooms))
	for _, r := range rooms {
		// Бывшие комнаты (4/5/6) по умолчанию скрыты (спека §6 `rooms list`).
		if isFormerRoom(r.Type) && !opts.IncludeFormer {
			continue
		}
		if opts.UnreadOnly && r.UnreadMessages <= 0 {
			continue
		}
		if opts.Type != nil && r.Type != *opts.Type {
			continue
		}
		out = append(out, r)
	}
	return out
}

// isFormerRoom возвращает true для «бывших» типов комнаты (4/5/6 — спека §12).
func isFormerRoom(t RoomType) bool {
	switch t {
	case 4, 5, 6:
		return true
	}
	return false
}
