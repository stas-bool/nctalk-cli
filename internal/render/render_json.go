package render

// JSON-рендереры структур client.* по спеке §6 (--json).
// Текстовые рендереры живут в render.go; FormatTime — в format_time.go.
//
// Все функции маршалят значения напрямую через json.NewEncoder(w).SetIndent,
// поэтому json-теги канонических структур client определяют имена полей:
//   - Room/Message/ReactionActor имеют теги → поля lowercase (actorId и т.д.);
//   - ConversationResult/MessageResult тегов не имеют → имена полей как в Go
//     (PascalCase). Спека не требует переименования для этих типов; marshaling
//     «как есть» — канонический для MVP.

import (
	"encoding/json"
	"io"

	"github.com/stas-bool/nctalk-cli/internal/client"
)

// writeJSON маршалит value в w с отступом 2 пробела (спека §6 --json).
// json.Encoder.Encode добавляет финальный перевод строки — это допустимо.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// RoomsJSON выводит []Room как JSON-массив (спека §6, --json).
// Поле actorId присутствует всегда (Room.ActorId с json-тегом).
func RoomsJSON(w io.Writer, rooms []client.Room) error {
	return writeJSON(w, rooms)
}

// MessagesJSON выводит []Message как JSON-массив (спека §6 chat show, --json).
func MessagesJSON(w io.Writer, msgs []client.Message) error {
	return writeJSON(w, msgs)
}

// ConversationResultsJSON выводит []ConversationResult как JSON-массив
// (спека §6 rooms search, --json).
func ConversationResultsJSON(w io.Writer, rs []client.ConversationResult) error {
	return writeJSON(w, rs)
}

// MessageResultsJSON выводит []MessageResult как JSON-массив
// (спека §6 search, --json).
func MessageResultsJSON(w io.Writer, rs []client.MessageResult) error {
	return writeJSON(w, rs)
}

// ReactionsJSON сериализует map «эмодзи → актёры» как есть (спека §6 reactions
// get, --json): {"👍":[{"actorId":...}...], ...}.
func ReactionsJSON(w io.Writer, rs map[string][]client.ReactionActor) error {
	return writeJSON(w, rs)
}

// NewMessageIDJSON выводит id отправленного сообщения как {"id": <int>}
// (спека §6 chat send, --json-ветка).
func NewMessageIDJSON(w io.Writer, id int) error {
	return writeJSON(w, struct {
		Id int `json:"id"`
	}{Id: id})
}

// ParticipantsJSON выводит []Participant как JSON-массив (спека-дельта
// 2026-09-09 §2, --json). Сырой ответ API не проксируется — каноническая
// модель, json-теги структур client определяют имена полей.
func ParticipantsJSON(w io.Writer, ps []client.Participant) error {
	return writeJSON(w, ps)
}
