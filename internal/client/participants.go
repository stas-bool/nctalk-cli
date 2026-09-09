package client

import (
	"context"
	"net/http"
)

// participants.go — участники комнаты (спека-дельта 2026-09-09 §3–4):
// GET /ocs/v2.php/apps/spreed/api/v4/room/{token}/participants.
// Своя группа эндпоинта — по аналогии с reactions.go; в rooms.go НЕ добавлять.

// Participant — каноническое представление участника комнаты (спека-дельта
// §4). Из ответа декодируются только эти семь полей; остальные наблюдаемые
// поля эндпоинта (roomToken, attendeeId, permissions, attendeePermissions,
// attendeePin, phoneNumber, callId) игнорируются — тонкий клиент отдаёт
// каноническую модель (как Room), сырой ответ НЕ проксируется.
type Participant struct {
	ActorType       string   `json:"actorType"`       // "users" / "guests" / "emails" / ...
	ActorId         string   `json:"actorId"`         // для guests — "guest::<anon-id>" (assumption, спека §4)
	DisplayName     string   `json:"displayName"`     // может быть пустым (гость без имени)
	ParticipantType int      `json:"participantType"` // 1–6, см. спека §3; неизвестное — само число в выводе
	SessionIds      []string `json:"sessionIds"`      // непустой = есть живая сессия (онлайн)
	InCall          int      `json:"inCall"`          // 0 = не в звонке
	LastPing        int64    `json:"lastPing"`        // СЕКУНДЫ Unix, 0 = никогда
}

// GetParticipants возвращает список участников комнаты token (спека-дельта
// §3–4). Read-only GET без guard-ов до сети — как GetReactions (непустоту
// token гарантирует cli.ResolveRoom).
//
// Запрос: GET pathRooms + "/" + token + "/participants" — token кладётся в
// путь КАК ЕСТЬ: эскейп path-сегмента выполняет сериализация URL в
// transport.DoOCS (RawPath сброшен, String() экранирует Path). Предварительный
// url.PathEscape дал бы двойной эскейп (пробел → %2520: PathEscape ставит %20,
// транспорт экранирует % повторно) — сервер декодировал бы литеральный
// "tok%20team" вместо "tok team". Регресс-тест TestGetParticipants_PathEscape.
// Разбор: doOCS → []Participant.
//
// Нормализация после decode (контракт детерминированного --json):
//   - ocs.data:null — json.Unmarshal обнуляет слайс в nil («null» проходит
//     guard len(data)>0, т.к. RawMessage("null") имеет len 4) — прецедент
//     nil-map у GetReactions; переинициализируем в пустой non-nil слайс
//     (data:[] уже декодируется в non-nil, guard идемпотентен);
//   - SessionIds == null у отдельного участника → пустой слайс, не nil
//     (иначе --json напечатал бы "sessionIds": null).
//
// Пустой ответ сервера (data:[] ИЛИ data:null) → пустой (non-nil) слайс,
// nil error. OCS-ошибка — стандартная обработка doOCS → *OCSError
// (404 → exit 2 в cli).
func (c *TalkClient) GetParticipants(ctx context.Context, token string) ([]Participant, error) {
	// Token в пути без предварительного PathEscape — эскейп делает
	// transport.DoOCS при сборке URL (объяснение в doc-комментарии выше).
	p := pathRooms + "/" + token + "/participants"

	var out []Participant
	if _, err := c.doOCS(ctx, http.MethodGet, p, nil, nil, false, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []Participant{}
	}
	for i := range out {
		if out[i].SessionIds == nil {
			out[i].SessionIds = []string{}
		}
	}
	return out, nil
}
