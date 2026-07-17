package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// pathReaction — базовый путь эндпоинта реакций (спека §6 `reactions get`, §12).
// Локальная константа (НЕ в paths.go), чтобы не конфликтовать с параллельной
// работой над search.go/chat.go. Полная форма URL собирается как
// pathReaction + "/" + url.PathEscape(token) + "/" + strconv.Itoa(messageId).
const pathReaction = "/ocs/v2.php/apps/spreed/api/v1/reaction"

// ReactionActor — актёр, поставивший реакцию на сообщение (спека §12).
// Имена полей соответствуют JSON-ответу эндпоинта reactions.
type ReactionActor struct {
	ActorType        string `json:"actorType"`        // "users" / "guests" / ...
	ActorId          string `json:"actorId"`          // идентификатор актёра (для guests — "guest::<anon-id>")
	ActorDisplayName string `json:"actorDisplayName"` // отображаемое имя
	Timestamp        int64  `json:"timestamp"`        // СЕКУНДЫ Unix
}

// GetReactions возвращает мапу «эмодзи → список актёров» для сообщения
// messageId в комнате token (спека §6 `reactions get`, §12).
//
// Запрос: GET pathReaction/{token}/{messageId}.
// Разбор: ocs.data → map[string][]ReactionActor.
//
// Контракт пустого результата: data={} → пустая (non-nil) map, nil error
// (спека §6: exit 0, «реакций нет»). Это достигается пред. инициализацией out
// через make — даже если ocs.data пуст, вызывающий получает валидную пустую map.
func (c *TalkClient) GetReactions(ctx context.Context, token string, messageId int) (map[string][]ReactionActor, error) {
	// Собираем путь: базовая константа + url.PathEscape(token) + messageId.
	// PathEscape на token — защита от специальных символов в path-сегменте;
	// messageId форматируем через strconv.Itoa (целое — безопасно без эскейпа).
	p := pathReaction + "/" + url.PathEscape(token) + "/" + strconv.Itoa(messageId)

	// make гарантирует non-nil map даже при пустом data={}: json.Unmarshal
	// переиспользует существующую map, оставляя её пустой.
	out := make(map[string][]ReactionActor)
	if err := c.doOCS(ctx, http.MethodGet, p, nil, false, &out); err != nil {
		return nil, err
	}
	return out, nil
}
