// Package signaling — OCS-polling signaling-клиент для P2P-звонков Nextcloud
// Talk (без HPB). Спека 2026-07-19 §7.
//
// Две основные операции:
//   - PollLoop: long-poll GET /signaling/{token}, разбор входящих сообщений
//     (usersInRoom / offer / answer / candidate) и доставка их подписчику
//     через канал Event. Стратегия retry/backoff — §7 «Polling retry/backoff».
//   - Send: POST form-encoded исходящих сообщений (offer/answer/candidate)
//     на тот же эндпоинт.
//
// Plus JoinCall/LeaveCall — управление состоянием участника в Call API (POST /
// DELETE /call/{token}). Call и signaling эндпоинты разные, но используются
// совместно: JoinCall включает флаги inCall (recvonly/sendrecv), signaling
// доносит их другим участникам через usersInRoom.
//
// Контракт безопасности (спека §5, §9): все ошибки пропускаются через
// transport.SanitizeErr; заголовок Authorization собирается на каждый запрос
// заново (не кэшируется); в URL никогда не попадают креды (только в заголовке
// Authorization). Same-host redirect-политика проставляется вызывающим
// (http.Client.CheckRedirect = transport.SameHostRedirectPolicy) — сам signaling
// только использует doer, как и internal/client.
package signaling

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/stas-bool/nctalk-cli/internal/exit"
	"github.com/stas-bool/nctalk-cli/internal/transport"
)

// signalingDebug — отладочные логи signaling-парсера. Молчат по умолчанию;
// включаются env NCTALK_DEBUG=1 (или CLI-флагом --debug, который выставляет env).
// Пишет через стандартный log (os.Stderr) — намеренно: debug нужен в терминале
// при ручной отладке, а не в call.log, и только если человек явно попросил.
func signalingDebug(format string, a ...any) {
	if os.Getenv("NCTALK_DEBUG") == "" {
		return
	}
	log.Printf("DEBUG signaling: " + format, a...)
}

// Client — signaling-клиент. Не знает про WebRTC-пир и аудио (микширование,
// кодеки) — только OCS-транспорт и протокол signaling. Создаётся один раз на
// звонок; методы реентранабельны кроме PollLoop (он блокирует до ctx.Done или
// фатальной ошибки).
//
// Поля unexported: doer (HTTP-транспорт; *http.Client в production, мок в
// тестах), auth (BaseURL + Basic-auth креды), sessionId (собственный sessionId
// в комнате — нужен для формирования исходящего POST), backoffBase/backoffMax
// (настраиваемые границы exp-backoff, в тестах сжимаются до ms).
type Client struct {
	doer        transport.Doer
	auth        transport.Auth
	sessionId   string
	backoffBase time.Duration
	backoffMax  time.Duration
}

// New — production-style конструктор: doer обычно это *http.Client с
// настроенным Timeout и CheckRedirect = transport.SameHostRedirectPolicy
// (см. internal/client.NewTalkClient). backoff по умолчанию: база 1с, потолок
// 30с (спека §7).
func New(auth Auth, doer transport.Doer) *Client {
	return &Client{
		doer:        doer,
		auth:        auth,
		backoffBase: 1 * time.Second,
		backoffMax:  30 * time.Second,
	}
}

// SetSessionId устанавливает собственный sessionId (полученный от сервера при
// входе в звонок / из signaling-settings). Должен быть установлен до первого
// Send: сервер проверяет sessionId записи против активной сессии в комнате
// (SignalingController::sendMessages). JoinCall/LeaveCall/PollLoop sessionId
// не требуют.
func (c *Client) SetSessionId(id string) { c.sessionId = id }

// JoinCall выполняет POST /call/{token} с указанными flags (спека §6/§7).
// flags — битмаск: 1=IN_CALL (recvonly), 3=IN_CALL|WITH_AUDIO (sendrecv).
// Если участник уже в звонке — сервер атомарно обновит его flags (это и есть
// способ переключить mute/unmute на стороне сервера).
//
// Тело ответа (список пиров в звонке) здесь не разбирается — это задача
// peer-слоя (для seed-списка перед первым PollLoop); на уровне signaling
// интересен только факт успеха/ошибки.
func (c *Client) JoinCall(ctx context.Context, token string, flags int) error {
	p := fmt.Sprintf(pathCallFmt, token)
	body, err := json.Marshal(struct {
		Flags int `json:"flags"`
	}{Flags: flags})
	if err != nil {
		return transport.SanitizeErr(err)
	}
	var out json.RawMessage
	_, err = transport.DoOCS(ctx, c.doer, c.auth, http.MethodPost, p, nil, bytes.NewReader(body), true, &out)
	return err
}

// LeaveCall выполняет DELETE /call/{token} (спека §7). Выход из звонка:
// сервер убирает участника из usersInRoom, остальные пири получат update и
// закроют свои PeerConnection'ы. all=1 (завершить звонок для всех) НЕ
// передаём — обычный участник может выйти только сам за себя.
func (c *Client) LeaveCall(ctx context.Context, token string) error {
	p := fmt.Sprintf(pathCallFmt, token)
	var out json.RawMessage
	_, err := transport.DoOCS(ctx, c.doer, c.auth, http.MethodDelete, p, nil, nil, false, &out)
	return err
}

// JoinRoom выполняет POST /api/v4/room/{token}/participants/active (joinRoom,
// баг #3): создаёт participant session на сервере — без неё signaling pull даёт
// 404 (CallController требует session). Возвращает собственный sessionId из
// ocs.data.sessionId — он нужен для SetSessionId (исходящий POST signaling) и для
// ownSessionId-фильтра в agent (не звонить самому себе). Вызывать ПЕРЕД JoinCall/
// PollLoop. Canonical flow: web-login → JoinRoom → pull → JoinCall (спека §7,
// подтверждено spike-gate 2026-07-20; порядок pull/JoinCall некритичен — оба 200).
func (c *Client) JoinRoom(ctx context.Context, token string) (string, error) {
	p := fmt.Sprintf(pathJoinRoomFmt, token)
	var data struct {
		SessionId string `json:"sessionId"`
	}
	if _, err := transport.DoOCS(ctx, c.doer, c.auth, http.MethodPost, p, nil, nil, true, &data); err != nil {
		return "", err
	}
	return data.SessionId, nil
}

// PollLoop крутит long-poll GET /signaling/{token} (спека §7). Каждое событие
// из ответа (usersInRoom/offer/answer/candidate) отправляется в ch. Выходит:
//   - при ctx.Done() — штатный leave (вызывающий делает LeaveCall отдельно);
//     возвращает nil;
//   - при 401/403 — EvError{Err: exit.Exit(1, err)}, loop выходит без retry;
//     возвращает исходную ошибку;
//   - при 404 — EvError{Err: exit.Exit(2, err)}, loop выходит без retry.
//
// На 5xx/timeout/сетевой сбой — экспоненциальный backoff (база 1с, потолок 30с)
// и немедленный повтор. 2xx/304 — обработать тело (если есть), немедленный
// повторный poll без задержки.
//
// Канал ch должен буферизоваться потребителем или читаться оперативно: send
// блокирует, но уважает ctx.Done() (select). Это даёт backpressure на медленного
// потребителя и гарантирует, что LeaveCall через ctx.Cancel не зависнет на
// записи в ch.
func (c *Client) PollLoop(ctx context.Context, token string, ch chan<- Event) error {
	p := fmt.Sprintf(pathSignalingFmt, token)
	var backoff time.Duration
	for {
		if err := ctx.Err(); err != nil {
			return nil // штатный выход (вызывающий отменил контекст)
		}

		var envelopes []signalingEnvelope
		_, err := transport.DoOCS(ctx, c.doer, c.auth, http.MethodGet, p, nil, nil, false, &envelopes)
		if err != nil {
			action := classifyPollErr(err)
			if action == actionFatalExit1 || action == actionFatalExit2 {
				code := exit.ExitGeneric
				if action == actionFatalExit2 {
					code = exit.ExitNotFound
				}
				select {
				case ch <- Event{Kind: EvError, Err: exit.Exit(code, err)}:
				case <-ctx.Done():
				}
				return err
			}
			// actionBackoff: transient-сбой — засыпаем, повторяем.
			backoff = nextBackoff(backoff, c.backoffBase, c.backoffMax)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
				continue
			}
		}

		// Успех (включая 304/204 — тогда envelopes остаётся nil). Сброс backoff
		// и немедленный повтор после обработки событий.
		backoff = 0
		for _, ev := range parseEnvelopes(envelopes) {
			select {
			case ch <- ev:
			case <-ctx.Done():
				return nil
			}
		}
	}
}

// Send POST'ит исходящее signaling-сообщение на /signaling/{token} (спека §7).
//
// WIRE-FORMAT (fixtures/README.md §«Исходящий POST»): тело запроса — НЕ JSON,
// а application/x-www-form-urlencoded с единственным полем messages, значение
// которого — JSON-строка массива записей вида:
//
//	[{"ev":"message","fn":"<stringified inner payload>","sessionId":"<own>"}]
//
// где inner payload — {type, to, roomType:"video", payload:{...}} БЕЗ поля from
// (сервер подставит его сам из sessionId записи). Форма взята из JS-клиента
// Spreed (src/utils/signaling.js: sendCallMessage → _sendMessages), который
// кодирует через jQuery $.ajax (form-encode по умолчанию).
//
// Поскольку transport.DoOCS жестко задаёт Content-Type: application/json при
// mutate=true (а Spreed требует form-encode), здесь request строится вручную
// через Doer.Do с последующим разворотом OCS-конверта. Дублирование логики
// DoOCS (URL-склейка, auth-заголовки, decode конверта) намеренное —
// альтернатива расширять сигнатуру transport.DoOCS параметром headers, что
// выходит за рамки Task 2.2.
func (c *Client) Send(ctx context.Context, token string, msg Message) error {
	// 1) Внутренний payload — type/to/roomType/payload. Без 'from' (см. выше).
	inner := struct {
		Type     string          `json:"type"`
		To       string          `json:"to,omitempty"`
		RoomType string          `json:"roomType"`
		Payload  json.RawMessage `json:"payload,omitempty"`
	}{
		Type:     msg.Type,
		To:       msg.To,
		RoomType: "video", // Spreed всегда использует "video" — даже для audio-only (direction в SDP m-line)
		Payload:  msg.Payload,
	}
	innerJSON, err := json.Marshal(inner)
	if err != nil {
		return transport.SanitizeErr(err)
	}

	// 2) Внешняя запись: {ev, fn (JSON-строка!), sessionId}.
	record := struct {
		Ev        string `json:"ev"`
		Fn        string `json:"fn"`
		SessionId string `json:"sessionId"`
	}{
		Ev:        "message",
		Fn:        string(innerJSON), // именно СТРОКА, не вложенный JSON
		SessionId: c.sessionId,
	}
	records := []any{record}
	recordsJSON, err := json.Marshal(records)
	if err != nil {
		return transport.SanitizeErr(err)
	}

	// 3) Form-encode: messages=<recordsJSON>. url.Values.Encode() сортирует
	// ключи и эскейпит по RFC 3986 — серверу OCS это нормируется на приёме.
	form := url.Values{}
	form.Set("messages", string(recordsJSON))

	p := fmt.Sprintf(pathSignalingFmt, token)
	fullURL := buildURL(c.auth.BaseURL, p)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fullURL, strings.NewReader(form.Encode()))
	if err != nil {
		return transport.SanitizeErr(err)
	}
	applyAuthHeaders(req, c.auth)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.doer.Do(req)
	if err != nil {
		return transport.SanitizeErr(err)
	}
	defer resp.Body.Close()

	// 4) Разворот OCS-конверта (логика идентична transport.DoOCS для >=400 и 404).
	var env transport.OCSEnvelope[json.RawMessage]
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		if resp.StatusCode == http.StatusNotFound {
			return &transport.OCSError{
				Code:    http.StatusNotFound,
				Message: fmt.Sprintf("HTTP 404: %s", err),
			}
		}
		return transport.SanitizeErr(fmt.Errorf("signaling: не удалось декодировать OCS-ответ (HTTP %d): %w", resp.StatusCode, err))
	}
	if env.OCS.Meta.StatusCode >= 400 {
		code := env.OCS.Meta.StatusCode
		if resp.StatusCode == http.StatusNotFound {
			code = http.StatusNotFound
		}
		return &transport.OCSError{Code: code, Message: env.OCS.Meta.Message}
	}
	return nil
}

// ---- parser: разворот входящих сообщений signaling в Event'ы ----
//
// Чистая функция — тестируется отдельно от PollLoop (фикстуры из Task 2.1).
// Концепция: ocs.data — массив signalingEnvelope; для каждого определяем форму
// Data по Type и разбираем соответственно. Поведение при повреждённых данных:
// скип записи без шума (поломанная запись не должна ронять весь loop —
// transient-повреждение на сети не редкость; peer-слой не должен получать
// EvError для некритичных parse-ошибок).

// parseEnvelopes разворачивает массив {type, data} записей из ocs.data
// GET /signaling/{token} в поток Event. Обрабатывает:
//   - type:"usersInRoom" → EvUsersUpdated (даже при пустом data:[] — это
//     нормальный сигнал «список не изменился», все равно доставляем пустой
//     снапшот; потребитель может его игнорировать);
//   - type:"message" с внутренним type:"offer"/"answer" → EvOffer/EvAnswer
//     (From = sender sessionId, SDP из payload.sdp);
//   - type:"message" с внутренним type:"candidate" → EvCandidate (включая
//     end-of-candidates marker — payload.candidate === "").
//
// Прочие Type (например, "control") и неизвестные внутренние type — скипаются.
func parseEnvelopes(envelopes []signalingEnvelope) []Event {
	signalingDebug("parseEnvelopes count=%d", len(envelopes))
	events := make([]Event, 0, len(envelopes))
	for _, env := range envelopes {
		signalingDebug("env.Type=%s", env.Type)
		switch env.Type {
		case "usersInRoom":
			users, ok := decodeUsers(env.Data)
			if !ok {
				continue
			}
			events = append(events, Event{Kind: EvUsersUpdated, Users: users})
		case "message":
			inner, ok := decodeInnerMessage(env.Data)
			if ok {
				signalingDebug("inner.type=%s from=%.12s", inner.Type, inner.From)
			} else {
				signalingDebug("inner decode FAILED")
			}
			if !ok {
				continue
			}
			switch inner.Type {
			case "offer", "answer":
				if ev, ok := decodeSDPEvent(inner); ok {
					events = append(events, ev)
				}
			case "candidate":
				if ev, ok := decodeCandidateEvent(inner); ok {
					events = append(events, ev)
				}
			}
			// неизвестные inner.Type (control, ...) — скип
		}
	}
	return events
}

// decodeUsers разворачивает data-массив usersInRoom в []User.
func decodeUsers(data json.RawMessage) ([]User, bool) {
	var raw []rawUser
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, false
	}
	users := make([]User, 0, len(raw))
	for _, r := range raw {
		users = append(users, User{
			SessionId: r.SessionId,
			UserId:    r.UserId,
			ActorId:   r.ActorId,
			ActorType: r.ActorType,
			InCall:    r.InCall,
		})
	}
	return users, true
}

// decodeInnerMessage — двойной unmarshal: data это JSON-строка, внутри —
// объект signaling-сообщения {type, from, to, roomType, payload}.
// КРИТИЧНО: Spreed кодирует data через json_encode (PHP), поэтому на проводе
// идёт строка, а НЕ объект (fixtures/README.md п.2). Однократный unmarshal
// в innerMessage напрямую из data работает только если data — объект; на
// реальном трафике это даст ошибку и события будут потеряны.
func decodeInnerMessage(data json.RawMessage) (innerMessage, bool) {
	var dataStr string
	if err := json.Unmarshal(data, &dataStr); err != nil {
		return innerMessage{}, false
	}
	var inner innerMessage
	if err := json.Unmarshal([]byte(dataStr), &inner); err != nil {
		return innerMessage{}, false
	}
	return inner, true
}

// decodeSDPEvent — build Event для offer/answer.
func decodeSDPEvent(inner innerMessage) (Event, bool) {
	var p sdpPayload
	if err := json.Unmarshal(inner.Payload, &p); err != nil {
		return Event{}, false
	}
	kind := EvOffer
	if inner.Type == "answer" {
		kind = EvAnswer
	}
	return Event{Kind: kind, From: inner.From, SDP: p.SDP}, true
}

// decodeCandidateEvent — build Event для candidate (включая end-of-candidates).
func decodeCandidateEvent(inner innerMessage) (Event, bool) {
	var p icePayload
	if err := json.Unmarshal(inner.Payload, &p); err != nil {
		signalingDebug("decodeCandidateEvent FAIL err=%v payload=%.200s", err, string(inner.Payload))
		return Event{}, false
	}
	signalingDebug("decodeCandidateEvent OK cand=%.120s", p.Candidate.Candidate)
	return Event{
		Kind: EvCandidate,
		From: inner.From,
		Candidate: ICECandidate{
			Candidate:     p.Candidate.Candidate,
			SDPMLineIndex: p.Candidate.SDPMLineIndex,
			SDPMid:        p.Candidate.SDPMid,
		},
	}, true
}

// WrapCandidatePayload оборачивает плоский pion ICECandidateInit JSON во
// вложенный Spreed wire-format {"candidate":{candidate,sdpMLineIndex,sdpMid}}
// для ИСХОДЯЩЕГО candidate (баг #6, spike-gate 2026-07-20). Симметрично
// входящему icePayload: удалённый Spreed-клиент в talk-main.js вызывает
// pc.addIceCandidate(a.payload.candidate), ожидая ОБЪЕКТ RTCIceCandidateInit,
// а НЕ строку — плоский {candidate:"<str>"} даёт TypeError ×N, ломает peer-
// pipeline → нет audio-sink → нет звука.
//
// initJSON — уже смаршаленный ICECandidateInit от pion c.ToJSON()
// ({candidate,sdpMLineIndex,sdpMid}). signaling НЕ зависит от webrtc, поэтому
// принимает json.RawMessage, а не webrtc.ICECandidateInit — caller (peer.go)
// маршалит сам.
func WrapCandidatePayload(initJSON json.RawMessage) (json.RawMessage, error) {
	return json.Marshal(struct {
		Candidate json.RawMessage `json:"candidate"`
	}{Candidate: initJSON})
}

// ---- retry/backoff (спека §7 «Polling retry/backoff») ----

// pollAction классифицирует действия PollLoop при ошибке.
type pollAction int

const (
	actionBackoff     pollAction = iota // transient — retry с backoff
	actionFatalExit1                    // 401/403 — auth/permissions, без retry
	actionFatalExit2                    // 404 — комната исчезла, без retry
)

// classifyPollErr разделяет ошибки на transient (retry) и fatal (exit).
// Контракт DoOCS: *transport.OCSError возвращается при HTTP 4xx/5xx с
// OCS-телом; при сетевой ошибке / не-OCS теле — обычная error (через
// SanitizeErr). Классификация:
//   - 401/403 → fatal exit 1 (повторять бессмысленно — креды/права не изменятся);
//   - 404 → fatal exit 2 (комната/signaling исчезли);
//   - 5xx (OCS или HTTP) → transient (backoff);
//   - network/decode error → transient (backoff).
//
// Прочие OCS-коды (400, 409, …) — консервативно трактуем как transient: сигналинг
// идемпотентен, лишний retry дешевле ложного exit-1.
func classifyPollErr(err error) pollAction {
	var ocsErr *transport.OCSError
	if !errors.As(err, &ocsErr) {
		return actionBackoff // network / decode / не-OCS
	}
	switch ocsErr.Code {
	case http.StatusUnauthorized, http.StatusForbidden:
		return actionFatalExit1
	case http.StatusNotFound:
		return actionFatalExit2
	}
	return actionBackoff
}

// nextBackoff — экспоненциальный рост: prev==0 → base, иначе prev*2 с потолком max.
// Не рандомизированный jitter (упрощение; спека §7 упоминает только exp-growth).
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

// ---- helpers для Send: дублируют часть transport.DoOCS ----

// buildURL собирает абсолютный URL из BaseURL и относительного пути. Семантика
// идентична transport.DoOCS: path.Join нормирует слэши, лидирующий '/' всегда
// восстанавливается, RawPath сбрасывается (иначе url.String() может отдать
// «грязный» путь после ручной правки Path).
func buildURL(base *url.URL, p string) string {
	full := *base
	joined := path.Join(base.Path, p)
	if !strings.HasPrefix(joined, "/") {
		joined = "/" + joined
	}
	full.Path = joined
	full.RawPath = ""
	return full.String()
}

// applyAuthHeaders проставляет Authorization (Basic, собирается на каждый
// запрос заново — не кэшируется), OCS-APIRequest и Accept — идентично
// transport.DoOCS. Content-Type выставляет вызывающий (Send использует
// application/x-www-form-urlencoded вместо application/json).
func applyAuthHeaders(req *http.Request, auth Auth) {
	creds := auth.Login + ":" + auth.Password
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(creds)))
	req.Header.Set("OCS-APIRequest", "true")
	req.Header.Set("Accept", "application/json")
}
