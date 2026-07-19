package signaling

import (
	"encoding/json"

	"github.com/stas/nctalk/internal/transport"
)

// Auth — псевдоним для transport.Auth, чтобы потребители signaling (peer-слой,
// cmd/nctalk-call) не импортировали transport только ради типа кред.
type Auth = transport.Auth

// EventKind — тип события, доставленного подписчику (peer-слою) из PollLoop.
type EventKind int

// События signaling-loop (спека 2026-07-19 §7).
const (
	// EvUsersUpdated — обновился список участников (usersInRoom).
	// Поле Users несёт актуальный снапшот; в нём присутствует и собственная
	// сессия (по ней peer-слой фильтрует «себя»).
	EvUsersUpdated EventKind = iota
	// EvOffer / EvAnswer — входящий SDP от удалённого пира; From — sessionId
	// отправителя, SDP — offer/answer SDP строка.
	EvOffer
	EvAnswer
	// EvCandidate — входящий ICE candidate (trickle). From — sessionId
	// отправителя, Candidate — сам кандидат. End-of-candidates marker
	// (Spreed шлёт payload.candidate === "") тоже доставляется как EvCandidate
	// с пустым Candidate.Candidate — peer-слой трактует его как сигнал конца
	// trickle-последовательности.
	EvCandidate
	// EvError — фатальная ошибка (401/403 → exit 1; 404 → exit 2), loop завершён.
	// Err несёт *exit.ExitError{Code, Err}. Получив EvError, потребитель должен
	// завершиться с указанным кодом (после штатного leave).
	EvError
)

// Event — сигнал из signaling-loop. Только одно из полей (Users/SDP/Candidate/Err)
// имеет смысл для конкретного Kind; остальные остаются zero-value.
type Event struct {
	Kind      EventKind
	Users     []User       // для EvUsersUpdated
	From      string       // sessionId отправителя (для EvOffer/EvAnswer/EvCandidate)
	SDP       string       // для EvOffer/EvAnswer
	Candidate ICECandidate // для EvCandidate
	Err       error        // для EvError
}

// User — участник signaling-комнаты. Соответствует форме записи usersInRoom
// из Task 2.1 (фикстура usersInRoom.json, источник SignalingController::
// getUsersInRoom). Полей больше, чем здесь — roomId/lastPing/
// participantPermissions — но они не нужны peer-слою и сознательно опущены
// (selective декодирование через rawUser в signaling.go).
//
// UserId несёт own-identification (review замечание 3): ownSessionId
// извлекается из usersInRoom по совпадению UserId == cfg.Login (NEXTCLOUD_LOGIN)
// — это documented Spreed-поведение (signaling.js делает так же).
type User struct {
	SessionId string
	UserId    string // NEXTCLOUD_LOGIN (для self-identification)
	ActorId   string
	ActorType string
	InCall    int // битмаск: 1=IN_CALL, 2=WITH_AUDIO, 4=WITH_VIDEO (Spreed-константы)
}

// ICECandidate — trickle ICE кандидат из входящего signaling-сообщения
// или для исходящего. Соответствует форме {candidate, sdpMLineIndex, sdpMid,
// usernameFragment} (fixtures: candidate.json). SDPMLineIndex/SDPMid — указатели,
// т.к. в реальном трафике могут отсутствовать (null); zero-value через указатель
// отличим от «не задано».
type ICECandidate struct {
	Candidate     string
	SDPMLineIndex *int
	SDPMid        *string
}

// Message — исходящее signaling-сообщение (offer/answer/candidate), отправляемое
// через Send. Payload — raw JSON: для offer/answer это {"type":"offer","sdp":"..."},
// для candidate — {"candidate":"...","sdpMLineIndex":0,"sdpMid":"0"}; сигналинг
// не вдаётся в структуру, упаковывает Payload как есть.
type Message struct {
	Type    string          `json:"type"`              // "offer"/"answer"/"candidate"/...
	To      string          `json:"to,omitempty"`      // sessionId получателя (для P2P unicast)
	Payload json.RawMessage `json:"payload,omitempty"` // SDP / ICE / ...
}

// Именованные канонические пути эндпоинтов signaling/call Nextcloud Talk
// (спека 2026-07-19 §7, по аналогии с internal/client/paths.go).
//
// РАСХОЖДЕНИЕ СО СПЕКОЙ (зафиксировано в testdata/signaling/README.md):
// PHP-бэкенд Spreed hard-restrictит signaling-эндпоинты к apiVersion v3, тогда
// как Call API — действительно v4. Спека требует v4 для signaling; реализуем
// по спеке (пользователь сказал «реализуй по спеке, противоречие отметь»).
// Путь — единая константа: при сверке с боевого сервера в Task 2.3 правка
// в одном месте переведёт signaling на v3, если v4 действительно не работает.
// Структура тела ответа/запроса от версии пути НЕ зависит (fixtures корректны
// независимо).
const (
	pathCallFmt      = "/ocs/v2.php/apps/spreed/api/v4/call/%s"
	pathSignalingFmt = "/ocs/v2.php/apps/spreed/api/v4/signaling/%s"
)

// ---- Внутренние типы для декодирования OCS-конверта signaling ----
//
// Не экспортируются: потребитель (peer-слой) работает только с Event/User/
// ICECandidate/Message. Форматы JSON приведены по фиксурам Task 2.1 и их
// цитированным источникам (SignalingController.php / signaling.js).
//
// ВАЖНО (fixtures/README.md п.2): у message-сообщений Spreed поле data — это
// JSON-СТРОКА (результат серверного json_encode), а не структурированный
// объект. Парсер делает двойной unmarshal: сначала в string, потом саму
// строку — в innerMessage. Это критический инвариант протокола.

// signalingEnvelope — элемент массива ocs.data ответа GET /signaling/{token}.
// Type определяет форму Data: "message" → Data это JSON-строка (string),
// "usersInRoom" → Data это массив участников. Data оставлен как RawMessage
// для отложенного декодирования в parseEnvelopes.
type signalingEnvelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// innerMessage — распарсенное содержимое Data для Type=="message" (после
// разворачивания внешней JSON-строки). Форма (fixtures/README.md п.4):
// {type, from, to, roomType, payload}. Поле from добавляется сервером при
// приёме POST (SignalingController::sendMessages подставляет sessionId
// отправителя), поэтому в ИСХОДЯЩЕМ POST его нет (см. Send).
type innerMessage struct {
	Type     string          `json:"type"`
	From     string          `json:"from"`
	To       string          `json:"to"`
	RoomType string          `json:"roomType"`
	Payload  json.RawMessage `json:"payload"`
}

// sdpPayload — payload для offer/answer (fixtures: offer.json/answer.json).
type sdpPayload struct {
	Type string `json:"type"` // "offer" или "answer" (дублирует внешнее innerMessage.Type)
	SDP  string `json:"sdp"`
}

// icePayload — payload для candidate (fixtures: candidate.json).
// SDPMLineIndex/SDPMid — указатели: в реальном трафике могут быть null
// (или отсутствовать), отличаем от zero-value.
type icePayload struct {
	Candidate        string  `json:"candidate"`
	SDPMLineIndex    *int    `json:"sdpMLineIndex"`
	SDPMid           *string `json:"sdpMid"`
	UsernameFragment string  `json:"usernameFragment"`
}

// rawUser — участник в usersInRoom (fixtures: usersInRoom.json). Соответствует
// SignalingController::getUsersInRoom: userId/roomId/lastPing/sessionId/
// inCall/participantPermissions/actorType/actorId. В User экспортируем только
// нужные peer-слою поля; остальные парсим для сохранения инварианта «фикстуры
// отражают реальный формат» (см. CLAUDE.md про messageParameters и e2e-баги).
type rawUser struct {
	UserId      string `json:"userId"`
	RoomId      int    `json:"roomId"`
	LastPing    int64  `json:"lastPing"`
	SessionId   string `json:"sessionId"`
	InCall      int    `json:"inCall"`
	Perms       int    `json:"participantPermissions"`
	ActorType   string `json:"actorType"`
	ActorId     string `json:"actorId"`
}
