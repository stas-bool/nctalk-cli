package client

import (
	"bytes"
	"encoding/json"
)

// Message — каноническое представление сообщения Nextcloud Talk (спека §12).
//
// ЕДИНОЕ определение этого типа в пакете client: используется и в
// Room.LastMessage (Task 2.2), и в GetChat (Task 2.5). Переопределять в других
// задачах НЕЛЬЗЯ — иначе compile error «type redeclared».
type Message struct {
	Id                  int           `json:"id"`
	ActorType           string        `json:"actorType"`
	ActorId             string        `json:"actorId"`
	ActorDisplayName    string        `json:"actorDisplayName"`
	MessageType         string        `json:"messageType"`       // "comment" / "system" / ...
	SystemMessage       string        `json:"systemMessage"`
	Message             string        `json:"message"`           // с плейсхолдерами {file}/{actor}/{mention-*}
	MessageParameters   MsgParams     `json:"messageParameters"` // ключ = имя плейсхолдера; см. MsgParams про два формата
	Reactions           map[string]int `json:"reactions"`        // emoji → count
	ReferenceId         string        `json:"referenceId"`
	Timestamp           int64         `json:"timestamp"`         // СЕКУНДЫ Unix
	IsReplyable         bool          `json:"isReplyable"`
	Markdown            bool          `json:"markdown"`
	ThreadId            int           `json:"threadId"`
	ExpirationTimestamp int           `json:"expirationTimestamp"`
	Token               string        `json:"token"`
}

// MsgParam — параметр сообщения (спека §12). Ключ в Message.MessageParameters —
// имя плейсхолдера (например "file", "actor", "mention-user1").
type MsgParam struct {
	Type string `json:"type"` // "file"/"user"/...
	Id   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path,omitempty"`
	Link string `json:"link,omitempty"`
}

// MsgParams — карта плейсхолдеров {name → параметр}. Обёрнута в именованный тип
// ради кастомного UnmarshalJSON: поле messageParameters в JSON Nextcloud Talk
// имеет РАЗНЫЙ формат в разных эндпоинтах (спека §12):
//
//   - chat-API /ocs/v2.php/apps/spreed/api/v1/chat/{token} — ВСЕГДА объект
//     {actor: {...}, file: {...}, mention-userN: {...}, ...} (и для comment,
//     и для system). Это канонический формат для подстановки плейсхолдеров.
//   - /ocs/v2.php/apps/spreed/api/v4/room → lastMessage.messageParameters:
//     для messageType=comment — МАССИВ (как правило пустой []); для
//     messageType=system — объект. Для comment сообщение уже человекочитаемое
//     (плейсхолдеры подставлены сервером), параметров нет.
//
// UnmarshalJSON трактует массив как «параметров нет» (nil-карта); объект
// парсит в map[string]MsgParam как раньше. Логика подстановки плейсхолдеров
// (render.SubstituteParams) для объектного случая остаётся без изменений.
type MsgParams map[string]MsgParam

// UnmarshalJSON разбирает оба формата messageParameters:
//   - массив (в т.ч. пустой []) → nil-карта (параметров нет);
//   - null → nil-карта;
//   - объект {key: {...}, ...} → map[string]MsgParam;
//   - прочее (невалидный JSON) → ошибка декодирования.
//
// Элементы массива нам не нужны: реальный массив от v4/room для comment либо
// пуст, либо (в редких случаях) содержит уже-подставленные значения — для
// вывода это всё равно не материал для подстановки плейсхолдеров.
func (p *MsgParams) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		// null или пустой ввод → параметров нет. (Пустой ввод через json.Unmarshal
		// в реальности не доходит сюда — пакет json отвергает его на верхнем
		// уровне; ветка оставлена для прямой защиты и читаемости.)
		*p = nil
		return nil
	}
	if trimmed[0] == '[' {
		// Массив (формат v4/room для comment) — параметров нет. Элементы массива
		// намеренно отбрасываем: для comment message уже человекочитаемый текст,
		// материалом для подстановки плейсхолдеров массив не является.
		*p = nil
		return nil
	}
	// Объект — канонический формат (chat-API всегда; v4/room для system).
	var m map[string]MsgParam
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	*p = MsgParams(m)
	return nil
}
