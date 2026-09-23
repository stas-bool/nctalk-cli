// protocol.go — wire-форматы HPB (nextcloud-spreed-signaling, протокол hello
// "1.0"/"2.0") и чистый маппинг в signaling.Event / из signaling.Message.
// Дельта §2.
//
// Структурные источники: api_signaling.go / hub.go / room.go
// strukturag/nextcloud-spreed-signaling (v2.1.1 — версия живого сервера
// спайка), клиентский эталон — src/utils/signaling.js nextcloud/spreed
// (Standalone). Фактические поля подтверждены спайком на живом HPB
// (Task 1, 2026-09-23) — фикстуры testdata/hpb/ ЭТАЛОН wire-формата;
// доспайковый шаблон брифа разошёлся с реальностью в hello/welcome/пути WS —
// расхождение исправлено ЗДЕСЬ (протокольный слой), не в остальной логике.
//
// Отличия от OCS-polling (call/signaling), изолированные ЗДЕСЬ:
//   - кадр — одиночный JSON-объект в WS-текстовом фрейме (не массив конвертов);
//   - WS-путь standalone-сервера — /spreed, корень отдаёт 404 (normalizeWSURL);
//   - hello: auth = {url: OCS signaling-backend Nextcloud, params:
//     helloAuthParams[version]} — форма auth.type:"ticket" живым сервером
//     ОТВЕРГНУТА (invalid_format); версия "2.0" (params {token}) выбирается
//     по welcome-feature hello-v2, иначе "1.0" (params {userid, ticket});
//   - welcome-кадр (сразу после connect, до hello): id-поля нет, version —
//     ВЕРСИЯ СЕРВЕРА (не протокола), features — список возможностей — по нему
//     выбирается версия hello (wantHelloV2);
//   - у message-кадров data — ОБЪЕКТ (в OCS — JSON-СТРОКА, двойной unmarshal);
//   - From входящих P2P — из message.sender.sessionid (НЕ из data.from —
//     браузер делает так же);
//   - участники — инкрементальные события (join/leave/participants-update),
//     полный снапшот usersInRoom-аналога аккумулирует roomState;
//   - собственный sessionId — из hello-response (EvOwnSession), HPB-пространство.

package hpbesignaling

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/stas-bool/nctalk-cli/internal/call/signaling"
)

// ---- Константы протокола ----

const (
	helloV1          = "1.0"      // протокол hello v1: params {userid, ticket}
	helloV2          = "2.0"      // протокол hello v2: params {token} (feature hello-v2)
	featureHelloV2   = "hello-v2" // welcome-фича, открывающая hello v2
	recipientSession = "session"  // MessageClientMessageRecipient.Type
	roomTypeVideo    = "video"    // Spreed всегда "video", даже для audio-only (см. signaling.Send)
	idHello          = "h"        // id hello-запроса (ответ матчится по нему)
	idRoom           = "r"        // id room-запроса
	wsPath           = "/spreed"  // WS-путь standalone-сервера (корень — 404, спайк Task 1)

	// backendPath — OCS signaling-backend Nextcloud для hello.auth.url. Это НЕ
	// клиентский эндпоинт (туда ходит сам HPB с HMAC-секретом) — клиент лишь
	// указывает полный URL, и сервер его ВАЛИДИРУЕТ (не содержит
	// ocs/v2.php/apps/spreed/ → invalid_format; путь принят живым HPB Task 1).
	// Полный URL = транспорт.Auth.BaseURL + backendPath; сборка — Task 4.
	backendPath = "/ocs/v2.php/apps/spreed/api/v3/signaling/backend"
)

// ---- Исходящие кадры (клиент → сервер) ----

type clientFrame struct {
	ID      string        `json:"id,omitempty"`
	Type    string        `json:"type"` // "hello" | "room" | "message"
	Hello   *helloFrame   `json:"hello,omitempty"`
	Room    *roomFrame    `json:"room,omitempty"`
	Message *messageFrame `json:"message,omitempty"`
}

// helloAuth — аутентификация hello. Реальная форма (живой HPB Task 1, та же
// в JS-клиенте spreed): БЕЗ поля type — auth.type:"ticket" отвергается кодом
// invalid_format (Type там — ClientType со значением "client", не билет).
type helloAuth struct {
	URL    string      `json:"url"`    // OCS signaling-backend (BaseURL + backendPath)
	Params helloParams `json:"params"` // helloAuthParams[version] из signaling-settings
}

// helloParams — параметры hello-аутентификации; форма зависит от версии
// (канон JS-клиента spreed): "1.0" → {userid, ticket}; "2.0" → {token}.
// Пустые поля маршаллер опускает — форму задаёт конструктор кадра
// (newHelloFrame / newHelloFrameV2). Ticket/token НЕ логируются нигде
// кроме этого кадра (дельта §4).
type helloParams struct {
	Userid string `json:"userid,omitempty"`
	Ticket string `json:"ticket,omitempty"`
	Token  string `json:"token,omitempty"`
}

type helloFrame struct {
	Version string    `json:"version"`
	Auth    helloAuth `json:"auth"`
}

type roomFrame struct {
	RoomId    string `json:"roomid"`
	SessionId string `json:"sessionid,omitempty"` // OCS-sessionId из JoinRoom: проверка прав в NC
}

type messageRecipient struct {
	Type      string `json:"type"` // "session" (unicast)
	SessionId string `json:"sessionid,omitempty"`
}

type messageFrame struct {
	Recipient messageRecipient `json:"recipient"`
	Data      innerPayload     `json:"data"`
}

// innerPayload — внутренняя форма P2P-сообщения. СОВПАДАЕТ с OCS-формой
// (signaling.Send строит ту же), но передаётся объектом, не строкой.
type innerPayload struct {
	Type     string          `json:"type"`              // offer/answer/candidate/unmute/... — ЛЮБОЙ
	To       string          `json:"to,omitempty"`      // sessionId получателя
	RoomType string          `json:"roomType"`          // "video"
	Payload  json.RawMessage `json:"payload,omitempty"` // SDP / ICE / {"name":"audio"} / ...
}

// newHelloFrame — приветствие протокола v1 (params {userid, ticket}).
// backendURL = транспорт.Auth.BaseURL + backendPath (валидируется HPB).
// id кадра — константа idHello: ответ матчится по ней (Task 4).
func newHelloFrame(userid, ticket, backendURL string) clientFrame {
	return clientFrame{ID: idHello, Type: "hello", Hello: &helloFrame{
		Version: helloV1,
		Auth:    helloAuth{URL: backendURL, Params: helloParams{Userid: userid, Ticket: ticket}},
	}}
}

// newHelloFrameV2 — приветствие протокола v2 (params {token} из
// helloAuthParams["2.0"] signaling-settings). Использовать ТОЛЬКО при
// welcome-feature hello-v2 (wantHelloV2) — путь, принятый живым HPB Task 1.
func newHelloFrameV2(token, backendURL string) clientFrame {
	return clientFrame{ID: idHello, Type: "hello", Hello: &helloFrame{
		Version: helloV2,
		Auth:    helloAuth{URL: backendURL, Params: helloParams{Token: token}},
	}}
}

// wantHelloV2 — выбор версии hello по welcome-features и доступным v2-params
// (как JS-клиент spreed; подтверждено живым HPB Task 1): v2 только если сервер
// объявил feature hello-v2 И helloAuthParams["2.0"] непусты, иначе v1
// (userid + ticket). Согласованность версии и формы params — обязанность
// вызывающего: wantHelloV2 → newHelloFrameV2, иначе newHelloFrame.
func wantHelloV2(features []string, v2Token string) bool {
	if v2Token == "" {
		return false
	}
	for _, f := range features {
		if f == featureHelloV2 {
			return true
		}
	}
	return false
}

// newRoomFrame — вход в комнату; roomSessionId — OCS-sessionId из JoinRoom.
func newRoomFrame(token, roomSessionId, id string) clientFrame {
	return clientFrame{ID: id, Type: "room", Room: &roomFrame{RoomId: token, SessionId: roomSessionId}}
}

// newMessageFrame — обёртка исходящего signaling.Message ЛЮБОГО Type (дельта
// §2.5: НЕ whitelist — unmute {name:"audio"} критичен, spike-gate 2026-07-20).
func newMessageFrame(msg signaling.Message) clientFrame {
	return clientFrame{Type: "message", Message: &messageFrame{
		Recipient: messageRecipient{Type: recipientSession, SessionId: msg.To},
		Data: innerPayload{
			Type:     msg.Type,
			To:       msg.To,
			RoomType: roomTypeVideo,
			Payload:  msg.Payload,
		},
	}}
}

// ---- Входящие кадры (сервер → клиент) ----

type serverFrame struct {
	ID      string         `json:"id,omitempty"`
	Type    string         `json:"type"` // welcome|hello|room|event|message|error|...
	Welcome *welcomeFrame  `json:"welcome,omitempty"`
	Hello   *helloResponse `json:"hello,omitempty"`
	Room    *roomAck       `json:"room,omitempty"`
	Event   *eventFrame    `json:"event,omitempty"`
	Message *serverMessage `json:"message,omitempty"`
	Error   *frameError    `json:"error,omitempty"`
}

// welcomeFrame — первый кадр после WS-connect, ДО hello. Дрифт против
// доспайкового шаблона (живой HPB Task 1, фикстура welcome.json): id-поля
// НЕТ; version — ВЕРСИЯ СЕРВЕРА ("2.1.1", не протокола); features — реальный
// список возможностей (hello-v2/mcu/incall-all/...; server-in-room НЕ приходит).
type welcomeFrame struct {
	Version  string   `json:"version"`
	Features []string `json:"features"`
}

// helloResponse — ответ на hello. Дублированный блок server (копия welcome,
// в коде сервера помечен к удалению) сознательно НЕ декодируем — не полагаемся.
type helloResponse struct {
	Version   string `json:"version"`
	SessionId string `json:"sessionid"` // наш sessionId (HPB-пространство)
	ResumeId  string `json:"resumeid"`  // resume не используем (re-hello самовосстанавливается)
	UserId    string `json:"userid"`
}

// roomAck — подтверждение room-join. Живой кадр несёт ещё properties комнаты
// и bandwidth-лимиты (фикстура room.json) — декодируем только roomid: состав
// участников всё равно приходит event-join'ом, лишние поля JSON игнорирует.
type roomAck struct {
	RoomId string `json:"roomid"`
}

type frameError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type eventFrame struct {
	Target string              `json:"target"` // room | participants | roomlist
	Type   string              `json:"type"`   // join | leave | update | ...
	Join   []sessionEntry      `json:"join,omitempty"`
	Leave  []string            `json:"leave,omitempty"`
	Update *participantsUpdate `json:"update,omitempty"`
}

// sessionEntry — запись join-списка. Вложенный объект user {displayname} живой
// кадр несёт, но signaling.User display-name не имеет — не декодируем (минимум;
// менять signaling.User под адаптер нельзя).
type sessionEntry struct {
	SessionId     string `json:"sessionid"`
	UserId        string `json:"userid"`
	RoomSessionId string `json:"roomsessionid,omitempty"` // OCS-sessionId (маппинг пространств)
}

// participantsUpdate — usersInRoom-аналог с inCall-флагами. Users — ПОЛНЫЙ
// снапшот комнаты (getParticipantsUpdateMessage(r.users) в server.go).
type participantsUpdate struct {
	RoomId string      `json:"roomid"`
	Users  []eventUser `json:"users"`
}

// eventUser — запись из update.users. Поля camelCase (формат NC room-ping);
// sessionId — HPB public id (сервер матчит GetSessionByPublicId). Альтернативные
// ключи (sessionid/userid) сервер допускает сам — декодируем оба.
type eventUser struct {
	SessionId    string      `json:"sessionId"`
	SessionIdAlt string      `json:"sessionid"`
	UserId       string      `json:"userId"`
	UserIdAlt    string      `json:"userid"`
	ActorId      string      `json:"actorId"`
	ActorType    string      `json:"actorType"`
	InCall       inCallFlags `json:"inCall"`
}

func (u eventUser) sid() string {
	if u.SessionId != "" {
		return u.SessionId
	}
	return u.SessionIdAlt
}

func (u eventUser) uid() string {
	if u.UserId != "" {
		return u.UserId
	}
	return u.UserIdAlt
}

// inCallFlags — толерантный декод inCall: современные Spreed шлют int-битмаску
// (1=IN_CALL, 2=WITH_AUDIO), отдельные деплои — bool (сервер сам терпит оба:
// «TODO: Change InCall to int when #914» в RoomEventServerMessage).
type inCallFlags int

func (f *inCallFlags) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' { // редкая строковая форма
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		b = []byte(s)
	}
	var n int
	if err := json.Unmarshal(b, &n); err == nil {
		*f = inCallFlags(n)
		return nil
	}
	var bl bool
	if err := json.Unmarshal(b, &bl); err != nil {
		return fmt.Errorf("inCall: ни число, ни bool: %s", b)
	}
	if bl {
		*f = 1
	}
	return nil
}

type serverMessage struct {
	Sender *messageSender `json:"sender"`
	Data   innerPayload   `json:"data"`
}

type messageSender struct {
	Type      string `json:"type"`
	SessionId string `json:"sessionid"` // From всех входящих P2P-событий
	UserId    string `json:"userid"`
}

// ---- roomState: аккумулятор снапшота (адаптер формата, дельта §3) ----

// roomState накапливает полный снапшот участников комнаты из инкрементальных
// HPB-событий. Живёт ОДНУ WS-сессию: переподключение создаёт новый state
// (server после re-room-join сам присылает полный join-список).
type roomState struct {
	sessions map[string]signaling.User
}

func newRoomState() *roomState {
	return &roomState{sessions: make(map[string]signaling.User)}
}

// applyFrame прогоняет серверный кадр через состояние; возвращает события для
// агента (может быть пусто). Чистая функция состояния — тестируется отдельно
// от WS. Поведение при повреждённых данных: скип без шума (как parseEnvelopes
// в call/signaling — поломанный кадр не роняет loop).
func (st *roomState) applyFrame(f *serverFrame) []signaling.Event {
	if f == nil {
		return nil
	}
	switch f.Type {
	case "hello":
		if f.Hello == nil || f.Hello.SessionId == "" {
			return nil
		}
		// ДО первого EvUsersUpdated — потребитель установит own (дельта §2.3).
		return []signaling.Event{{Kind: signaling.EvOwnSession, From: f.Hello.SessionId}}
	case "event":
		return st.applyEvent(f.Event)
	case "message":
		return applyMessage(f.Message)
	default:
		// welcome / room-ack / error / неизвестные — не события (error-кадры
		// обрабатывает connect/readLoop по коду, дельта §2.7).
		return nil
	}
}

func (st *roomState) applyEvent(e *eventFrame) []signaling.Event {
	if e == nil {
		return nil
	}
	switch e.Target {
	case "room":
		switch e.Type {
		case "join":
			changed := false
			for _, j := range e.Join {
				if j.SessionId == "" {
					continue
				}
				if _, ok := st.sessions[j.SessionId]; !ok {
					st.sessions[j.SessionId] = signaling.User{SessionId: j.SessionId, UserId: j.UserId}
					changed = true
				}
			}
			if changed {
				return st.snapshotEvent()
			}
		case "leave":
			changed := false
			for _, sid := range e.Leave {
				if _, ok := st.sessions[sid]; ok {
					delete(st.sessions, sid)
					changed = true
				}
			}
			if changed {
				return st.snapshotEvent()
			}
		}
	case "participants":
		// update — полный снапшот с флагами: замещает состав, мержит actor-поля
		// и inCall. Пустой users (или data:null → nil) — НЕ событие: не сбрасываем
		// пиры на пустом/битом апдейте.
		if e.Type == "update" && e.Update != nil && len(e.Update.Users) > 0 {
			fresh := make(map[string]signaling.User, len(e.Update.Users))
			for _, u := range e.Update.Users {
				sid := u.sid()
				if sid == "" {
					continue
				}
				prev := st.sessions[sid] // сохраняем userid из join, если update пуст
				fresh[sid] = signaling.User{
					SessionId: sid,
					UserId:    firstNonEmpty(u.uid(), prev.UserId),
					ActorId:   u.ActorId,
					ActorType: u.ActorType,
					InCall:    int(u.InCall),
				}
			}
			if len(fresh) > 0 {
				st.sessions = fresh
				return st.snapshotEvent()
			}
		}
	}
	return nil
}

// applyMessage — P2P-кадр → EvOffer/EvAnswer/EvCandidate. From — из sender.
func applyMessage(m *serverMessage) []signaling.Event {
	if m == nil || m.Sender == nil || m.Sender.SessionId == "" {
		return nil
	}
	from := m.Sender.SessionId
	switch m.Data.Type {
	case "offer", "answer":
		var p sdpPayload
		if err := json.Unmarshal(m.Data.Payload, &p); err != nil || p.SDP == "" {
			return nil
		}
		kind := signaling.EvOffer
		if m.Data.Type == "answer" {
			kind = signaling.EvAnswer
		}
		return []signaling.Event{{Kind: kind, From: from, SDP: p.SDP}}
	case "candidate":
		var p icePayload
		if err := json.Unmarshal(m.Data.Payload, &p); err != nil || p.Candidate.Candidate == "" {
			return nil
		}
		return []signaling.Event{{Kind: signaling.EvCandidate, From: from, Candidate: signaling.ICECandidate{
			Candidate:     p.Candidate.Candidate,
			SDPMLineIndex: p.Candidate.SDPMLineIndex,
			SDPMid:        p.Candidate.SDPMid,
		}}}
	}
	return nil // control и будущие типы — скип (получатель нас не звал)
}

// snapshot — детерминированная (отсортированная) копия для EvUsersUpdated:
// без сортировки итерация map даёт случайный порядок — TUI/логи прыгают.
func (st *roomState) snapshot() []signaling.User {
	out := make([]signaling.User, 0, len(st.sessions))
	for _, u := range st.sessions {
		out = append(out, u)
	}
	sortBySessionId(out)
	return out
}

func (st *roomState) snapshotEvent() []signaling.Event {
	return []signaling.Event{{Kind: signaling.EvUsersUpdated, Users: st.snapshot()}}
}

func sortBySessionId(users []signaling.User) {
	for i := 1; i < len(users); i++ {
		for j := i; j > 0 && users[j-1].SessionId > users[j].SessionId; j-- {
			users[j-1], users[j] = users[j], users[j-1]
		}
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// ---- локальные payload-типы (дубликат signaling/types.go — там unexported) ----
//
// Форма payload СОВПАДАЕТ с OCS-путём (инвариант бага #6 базовой спеки:
// candidate — ВЛОЖЕННЫЙ объект {candidate,sdpMLineIndex,sdpMid}, не строка).
// Дублирование осознанное: экспорт внутренних типов signaling раздул бы его
// API ради одного потребителя.

type sdpPayload struct {
	Type string `json:"type"`
	SDP  string `json:"sdp"`
}

type icePayload struct {
	Candidate iceCandidateBody `json:"candidate"`
}

type iceCandidateBody struct {
	Candidate     string  `json:"candidate"`
	SDPMLineIndex *int    `json:"sdpMLineIndex"`
	SDPMid        *string `json:"sdpMid"`
}

// ---- ошибки протокола и backoff ----

// errFrameError — ошибка из кадра {"type":"error"} (ответ на hello/room или
// асинхронная). Классифицируется frameErrAction.
type errFrameError struct {
	Code    string
	Message string
}

func (e *errFrameError) Error() string {
	return fmt.Sprintf("hpbsignaling: %s: %s", e.Code, e.Message)
}

// errAction — классификация error-кадра. Тип назван иначе, чем функция-
// классификатор frameErrAction (Go не даёт одному имени быть и типом, и
// функцией в одном блоке; контракт Task 4/5 фиксирует имя ФУНКЦИИ).
type errAction int

const (
	frameErrRetry         errAction = iota // transient — ретрай с backoff
	frameErrRefetchTicket                  // invalid_ticket/token_expired — новый ticket + ретрай
	frameErrFatal2                         // no_such_room — exit 2 без ретрая
)

func frameErrAction(code string) errAction {
	switch code {
	case "no_such_room":
		return frameErrFatal2
	case "invalid_ticket", "token_expired":
		return frameErrRefetchTicket
	}
	return frameErrRetry
}

// nextBackoff — экспоненциальный рост с потолком (идентичен signaling.
// nextBackoff; дублирован — там unexported).
func nextBackoff(prev, base, max time.Duration) time.Duration {
	if prev == 0 {
		return base
	}
	next := prev * 2
	if next > max {
		return max
	}
	return next
}

// normalizeWSURL — settings.server приходит с https/http схемой (браузерный
// клиент делает тот же replace на wss/ws), а standalone-сервер слушает путь
// /spreed — корень отдаёт 404 (живой HPB, спайк Task 1). Гарантирует РОВНО
// ОДИН /spreed: уже суффиксированный URL не трогаем, хвостовой / срезаем
// (иначе got бы .../spreed/spreed).
func normalizeWSURL(server string) string {
	u := strings.TrimSuffix(server, "/")
	u = strings.Replace(u, "https://", "wss://", 1)
	u = strings.Replace(u, "http://", "ws://", 1)
	if !strings.HasSuffix(u, wsPath) {
		u += wsPath
	}
	return u
}
