package client

import (
	"context"
	"net/http"
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
	// ActorId — идентификатор ЧЕЛОВЕКА (НЕ displayName). Для type=1 (one-to-one)
	// содержит собеседника, а не текущего пользователя (спека §6, §12).
	ActorId string `json:"actorId"`
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
	if err := c.doOCS(ctx, http.MethodGet, pathRooms, nil, false, &rooms); err != nil {
		return nil, err
	}
	return filterRooms(rooms, opts), nil
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
