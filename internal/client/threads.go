package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// threads.go — треды комнаты (спека-дельта 2026-09-29 §3–4):
// GET /ocs/v2.php/apps/spreed/api/v1/chat/{token}/threads/recent.
// Своя группа эндпоинта — по аналогии с reactions.go/participants.go.
// Чтение треда (GET chat?threadId=) и отправка (threadId/threadTitle в теле
// POST chat) — те же эндпоинты, что GetChat/SendMessage, живут в chat.go.

// Thread — каноническое представление треда (спека-дельта §3.1). Id треда =
// id корневого сообщения: им же читают (--thread), на него же отвечают.
type Thread struct {
	Id            int    `json:"id"` // = id корневого сообщения
	RoomToken     string `json:"roomToken"`
	LastMessageId int    `json:"lastMessageId"`
	NumReplies    int    `json:"numReplies"`
	LastActivity  int64  `json:"lastActivity"` // СЕКУНДЫ Unix
	Title         string `json:"title"`        // может быть пустым → fallback в render
}

// ThreadAttendee — подписка запросившего на тред (§3.1). В текстовый вывод не
// идёт (notify-level — out of scope), в --json — канонически.
type ThreadAttendee struct {
	NotificationLevel int `json:"notificationLevel"` // 0 default, 1 always, 2 mention, 3 never
}

// ThreadInfo — элемент выдачи threads/recent; first/last nullable (корень
// истёк/удалён). first/last — сообщения chat-API: у них messageParameters —
// объект при непустых параметрах, пустые — [] (тип MsgParams принимает оба).
type ThreadInfo struct {
	Thread   Thread         `json:"thread"`
	Attendee ThreadAttendee `json:"attendee"`
	First    *Message       `json:"first"`
	Last     *Message       `json:"last"`
}

// ListThreads возвращает список недавних тредов комнаты token (спека-дельта
// §3.1), порядок сервера — по lastActivity убыванию. limit — [1,50], клэмп
// выполняет cli-слой; метод не валидирует — тонкий клиент.
//
// Запрос: GET pathChat + "/" + token + "/threads/recent" — token кладётся в
// путь КАК ЕСТЬ, без предварительного url.PathEscape: эскейп path-сегмента
// выполняет транспорт при сборке URL (инвариант GetParticipants,
// participants.go: предварительный PathEscape даёт двойной эскейп, пробел →
// %2520, регресс-паттерн TestGetParticipants_PathEscape; легаси-PathEscape
// из chat.go/reactions.go удалён — пакет унифицирован по этому инварианту).
//
// Пустой ответ (data:[] ИЛИ data:null) → пустой non-nil слайс, nil error
// (guard-прецедент GetParticipants: «null» проходит len-guard doOCS и
// обнуляет слайс — переинициализируем). OCS-ошибка — стандартная обработка
// doOCS → *OCSError (404 → exit 2 в cli).
func (c *TalkClient) ListThreads(ctx context.Context, token string, limit int) ([]ThreadInfo, error) {
	p := pathChat + "/" + token + "/threads/recent"
	q := url.Values{}
	q.Set("limit", strconv.Itoa(limit))

	var out []ThreadInfo
	if _, err := c.doOCS(ctx, http.MethodGet, p, q, nil, false, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []ThreadInfo{}
	}
	return out, nil
}
