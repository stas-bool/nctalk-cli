# HPB signaling (`hpbesignaling`) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Звонки `nctalk-call`/`nctalk-talk` работают на серверах с внешним signaling-сервером (HPB, nextcloud-spreed-signaling): клиент сам выбирает транспорт (WebSocket вместо OCS-polling) по `signalingMode` из signaling-settings, UX не меняется.

**Architecture:** Дельта к спеке `docs/superpowers/specs/2026-09-23-nctalk-hpb-signaling-design.md` (читать вместе с базовой `2026-07-19-nctalk-call-design.md`; все 9 замечаний ревью `2026-09-23-nctalk-hpb-signaling-design.review.md` уже внесены в спеку, §2.1 дополнительно синхронизирован по ревью плана #7). Новый пакет `internal/call/hpbsignaling` реализует все 4 метода интерфейса `agent.sigClient` (JoinCall — сначала устанавливает WS-комнату с бюджетом, ЗАТЕМ делегирует OCS-клиенту `call/signaling` — canonical flow §2, ревью плана #4; LeaveCall — делегирование OCS; PollLoop/Send — WebSocket). `call/capability` расширяется: тот же единственный запрос signaling-settings отдаёт наружу `signalingMode`/`server`/`ticket`. Собственный sessionId в external-режиме приходит асинхронно из WS hello-response — новый `signaling.EvOwnSession`. Всё внутри границы удаления звонков (`internal/call` + `cmd/nctalk-call`/`cmd/nctalk-talk`); фундамент (transport/room/exit) и `cmd/nctalk` не затрагиваются.

**Tech Stack:** Go 1.21+ (stdlib + pion/webrtc/v4 + `github.com/coder/websocket v1.8.13` — единственная новая зависимость). Выбор WS-библиотеки (критерии спеки: чистый Go, CGO_ENABLED=0, пермиссивная OSI-лицензия): `coder/websocket` — ISC, context-first API (`Dial`/`Reader`/`Writer`/`Ping` принимают ctx — ложится на ctx-шатдаун агента, `Ping(ctx)` даёт детекцию half-open). Версия именно **v1.8.13**: последняя с `go 1.19` в go.mod (v1.8.14+ требуют go 1.23 — не соберётся на Go 1.21). Альтернатива gorilla/websocket (BSD-2, допустима) отклонена: ручные deadline, нет ctx-API.

## Протокол HPB (канон, подтверждён исходниками + спайком Task 1)

Источники: `strukturag/nextcloud-spreed-signaling` v1.3.2 (`api_signaling.go`, `hub.go`, `room.go`, `client.go`), `nextcloud/spreed` `src/utils/signaling.js` (клиент Standalone), `SignalingController.php` (`getSettings`). Сверено 2026-09-23; фактические поля на живом HPB фиксирует спайк (Task 1) — при расхождении правится протокольный слой (Task 3) и фикстуры, НЕ остальная логика.

1. **Ticket — из signaling-settings, отдельного эндпоинта НЕТ.** `GET /ocs/v2.php/apps/spreed/api/v3/signaling/settings` (Basic-auth, weblogin не нужен) в external-режиме дополнительно к stunservers/turnservers возвращает `signalingMode:"external"`, `server` (URL signaling-сервера), `ticket` и `helloAuthParams` (`"1.0": {userid, ticket}`). Кандидат из раннего драфта спеки `/api/v1/signaling/backend` — НЕ клиентский эндпоинт (это HPB→NC с HMAC-секретом); спайк это проверяет и фиксирует находку в fixtures/README. Ticket короткоживущий, НЕ логируется, в URL не попадает (только в WS-фрейме hello).
2. **WS + hello.** Соединение к `server` (https→wss нормализация). Первое сообщение: `{"id":"h","type":"hello","hello":{"version":"1.0","auth":{"type":"ticket","params":{"userid":"<login>","ticket":"<ticket>"}}}}`. Отдельный кадр `welcome` (server info/features) приходит сразу после коннекта — поглощаем. Ответ: `{"id":"h","type":"hello","hello":{"version":"1.0","sessionid":"...","resumeid":"...","userid":"...","server":{...}}}` → наш sessionId = `hello.sessionid`. Это ДРУГОЕ id-пространство, чем OCS-sessionId из JoinRoom.
3. **Room.** `{"id":"r","type":"room","room":{"roomid":"<token>","sessionid":"<OCS-sessionId из JoinRoom>"}}` — OCS-sessionId сервер прокидывает в Nextcloud для проверки прав (JoinRoom остаётся обязательным). Ack: `{"id":"r","type":"room","room":{"roomid":"<token>"}}`. Затем `event {target:"room", type:"join", join:[{sessionid, userid, roomsessionid?}, ...]}` — новый участник получает полный список соседей + свой собственный entry (self опознаём по sessionid == hello-sessionid). **Порядок canonical: WS room-ack ДО OCS-JoinCall** (join-event флагов inCall НЕ несёт; participants-update, порождённый JoinCall, должен находить нашу сессию уже в комнате — иначе фильтр WITH_AUDIO никого не увидит, симптом §1). Реализовано в `hpbesignaling.JoinCall` (Task 4, ревью плана #4).
4. **События.** `event` кадры: `target:"room"` → `join`/`leave` (incremental, БЕЗ inCall-флагов); `target:"participants"` → `update` c полным снапшотом `update.users` (camelCase-поля NC: `sessionId`/`userId`/`actorId`/`actorType`/`inCall`-битмаска; sessionId — HPB public id, сервер матчит через `GetSessionByPublicId`). Снапшот `usersInRoom`-аналога аккумулирует `roomState` (адаптер внутри hpbesignaling).
5. **P2P-сообщения.** Входящий: `{"type":"message","message":{"sender":{"type":"session","sessionid":"...","userid":"..."},"recipient":{...},"data":{...}}}` — `data` ОБЪЕКТ (не JSON-строка, как в OCS!) той же внутренней формы `{type, to, roomType, payload}`; From берём из `message.sender.sessionid` (браузер делает так же: `data.from = sender.sessionid`). Исходящий: `{"type":"message","message":{"recipient":{"type":"session","sessionid":"<to>"},"data":{type, to, roomType:"video", payload}}}` — ЛЮБОЙ `Type` (offer/answer/candidate/`unmute {name:"audio"}`/будущие), НЕ whitelist (root-cause spike-gate 2026-07-20: без unmute Spreed держит audio-sink замьюченным).
6. **Keepalive.** Сервер пингует каждые ~54с (pongWait 60с); coder/websocket отвечает на ping автоматически. Мы шлём свой `conn.Ping(ctx)` каждые 30с — детекция half-open (Ping блокирует до pong) + NAT-keepalive.
7. **Ошибки протокола.** Кадр `{"id":"<наш>","type":"error","error":{code, message}}` — ответ на hello/room. Коды: `no_such_room` → exit 2; `invalid_ticket`/`token_expired` → переполучить ticket и ретрай (счётчик исчерпывается бюджетом подключения); прочие — transient-ретрай.

## Global Constraints

- **`CGO_ENABLED=0` обязательно** для всех `go build`/`go test`/`go vet` (macOS + Go 1.21.4: без него dyld-падение `missing LC_UUID`).
- **Кириллические комментарии в существующем коде сохранять**; новые комментарии — на русском в стиле окружающего файла.
- **Коммиты: Conventional Commits обычным текстом, БЕЗ emoji** (`feat(call): ...`, не `✨ feat: ...`) и **БЕЗ какой-либо AI-атрибуции** — никакого `Co-Authored-By: Claude` (юридическое требование владельца репозитория, перекрывает дефолты ассистента).
- Секреты/личные URL (home.softmus.ru, signal.softmus.ru, логины, ticket) в код, тесты, фикстуры и коммиты НЕ допускать (публичный репозиторий `stas-bool/nctalk-cli`); фикстуры — обезличенные (`signaling.example.org`, `REDACTED-…`), реального формата эндпоинта (инвариант CLAUDE.md).
- **Ticket не логируется и не попадает в URL** — только в WS-фрейме hello; все ошибки через `transport.SanitizeErr` (дельта §4).
- Граница изоляции: импорт `coder/websocket` — ТОЛЬКО из `internal/call/hpbsignaling` (+ его тесты). `cmd/nctalk`, `internal/transport`/`room`/`exit`/`client` её не получают; `rm -rf internal/call cmd/nctalk-call cmd/nctalk-talk && go mod tidy` убирает зависимость (как pion).
- Exit-контракт базовой спеки §10 без изменений: `0` штатно · `1` сеть/общая · `2` not found. Исчерпание бюджета первичного подключения → `EvError{exit 1}` ДО срабатывания ICE-таймера (иначе молчаливый exit 0 «я один в звонке» — симптом, от которого дельта лечит).
- Режимы транспортировки выбираются в `cmd/*` по `st.SignalingMode`: `"internal"` ИЛИ пусто → текущий polling-путь байт-в-байт; `"external"` → hpbesignaling (weblogin пропускается — PHP-session нужен только OCS-pull).
- Каждый таск завершать: `CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./...` — зелёные.

## Риски и альтернативы

**Риски (дельта §6):**

1. **Протокольный дрейф HPB (главный).** Всё, что помечено «спайк уточнит», закрывается Task 1 (гейт на живом HPB до основной логики — правило базовой спеки: не compile-only, там это вскрыло 5 несоответствий). Ожидаемые точки дрейфа: ключ `sessionId` vs `sessionid` в `update.users` (декодируем оба), `inCall` int vs bool (толерантный UnmarshalJSON), форма `helloAuthParams`, схема `server` (https vs wss). При дрейфе правится `protocol.go` + фикстуры `testdata/hpb/`.
2. **Смешение id-пространств OCS/HPB.** OCS-sessionId (JoinRoom) идёт ТОЛЬКО в room-join-фрейм; OwnSessionId агента в external — из `EvOwnSession` (hello-response), OCS-sessionId в фильтр НЕ подмешивается. Все sessionId в `Event.From`/`Event.Users` — из HPB-пространства. Регресс-тест Task 6.
3. **Whitelist типов Send.** Если реализовать Send только под offer/answer/candidate — `unmute` потеряется, звук на живом HPB пропадёт, unit-тесты не заметят. Тест Task 5 шлёт именно `unmute`.
4. **Молчаливый ретрай = exit 0 «я один».** Бюджет первичного подключения (половина `NCTALK_ICE_TIMEOUT`, дефолт 15с) обязан фаталиться `EvError{exit 1}` ДО ICE-таймера (30с) — включая зависшую попытку: per-attempt `context.WithDeadline` обрывает молчащий dial/hello в бюджет, а не ждёт OS-таймаута TCP (ревью плана #5). Тесты Task 4 (отказ + зависший сервер).
5. **`data: null` / пустые `users`** в participants-update → nil-снапшот: guard `len(users)>0`, пустой апдейт не эмитит событие (не сбрасывает peers).
6. **Зависимость +1 в общий go.mod.** Импорт только из hpbesignaling; guard в `isolation_test.go` (Task 9) проверяет, что `cmd/nctalk` не зависит от `coder/websocket` (как от pion).

**Альтернативы (отклонены, не реализовывать):**

- Resume по `resumeid` вместо полного re-hello при переподключении — YAGNI: после re-room-join сервер сам присылает полный join-список, reconcile самовосстанавливается.
- Разделить `agent.sigClient` на два интерфейса (call-API + транспорт) — отклонено (вариант (б) ревью-блокера #1, зафиксированный спекой): hpbesignaling реализует все 4 метода, композиция не нужна.
- Высокоуровневый готовый HPB-Go-клиент — не найден (проверено при дизайне).
- MCU/SFU-режим HPB — out of scope (mesh сохраняется, HPB для нас только signaling-транспорт).

---

### Task 1: Спайк на живом HPB — зависимость + гейт-тест + фикстуры (ПЕРВЫЙ шаг реализации)

**Files:**
- Modify: `go.mod`, `go.sum` (go get)
- Create: `internal/call/hpbsignaling/doc.go`
- Create: `internal/call/hpbsignaling/integration_test.go` (build-tag `integration`)
- Create: `testdata/hpb/welcome.json`, `hello.json`, `room.json`, `event_join.json`, `event_leave.json`, `participants_update.json`, `message_offer.json`, `message_candidate.json` — обезличенные кадры из живого дампа
- Create: `testdata/hpb/README.md` — откуда кадры, что обезличено
- Modify: `docs/integration-run.md` — env `NCTALK_INTEGRATION_HPB` + команда запуска

**Interfaces:**
- Consumes: `transport.DoOCS(ctx, doer, auth, method, p, query, body, mutate, out) (http.Header, error)`, `transport.Auth`, `config.Load()`; JoinRoom-путь `POST /ocs/v2.php/apps/spreed/api/v4/room/{token}/participants/active`.
- Produces (для Task 3): фикстуры `testdata/hpb/*.json` — эталон wire-формата; подтверждённые спайком факты (ticket из settings, `helloAuthParams["1.0"]`, схема `server`, ключи sessionId/inCall в update.users).

- [ ] **Step 1: Добавить зависимость (последняя версия под Go 1.21)**

```sh
CGO_ENABLED=0 go get github.com/coder/websocket@v1.8.13
CGO_ENABLED=0 go mod tidy
```

Проверка: `grep coder/websocket go.mod` → `github.com/coder/websocket v1.8.13`; `CGO_ENABLED=0 go build ./...` — зелёный.

- [ ] **Step 2: Каркас пакета `doc.go`**

```go
// Package hpbesignaling — WebSocket-транспорт signaling для серверов
// Nextcloud Talk с настроенным High Performance Backend (внешний
// nextcloud-spreed-signaling). Дельта-спека 2026-09-23 (к базовой
// 2026-07-19 §7): при signalingMode=="external" OCS-polling не раздаёт
// участников и сообщения — клиент «слеп» в звонке; этот пакет — замена
// транспорта, peer/media-слои не меняются.
//
// Реализует все 4 метода agent.sigClient: PollLoop/Send — WebSocket (hello c
// ticket → room → события/исходящие), LeaveCall — делегирование OCS-клиенту
// call/signaling (Call API v4 работает при любом signalingMode), JoinCall —
// подключение WS-комнаты с бюджетом + делегирование OCS (canonical flow).
//
// Инварианты (дельта §2/§3/§4):
//   - ticket живёт только в памяти и в WS-фрейме hello: не в URL, не в логах;
//   - собственный sessionId — из hello-response (ДРУГОЕ id-пространство, чем
//     OCS-sessionId из JoinRoom) — доставляется агенту событием EvOwnSession;
//   - JoinCall устанавливает WS-комнату ДО делегирования OCS (canonical flow
//     §2: participants-update с inCall-флагами должен находить нашу сессию
//     уже в комнате — join-event флагов не несёт);
//   - первичное подключение — ограниченный бюджет (половина NCTALK_ICE_TIMEOUT,
//     дефолт 15с), per-attempt deadline: исчерпание/зависание → EvError{exit 1}
//     ДО ICE-таймера; переподключение
//     после установления — бесконечный backoff 1с→30с, новый ticket на каждое;
//   - Send пропускает signaling.Message с ЛЮБЫМ Type (unmute — критичен);
//   - импорт coder/websocket живёт только в этом пакете (граница удаления
//     звонков: rm -rf internal/call cmd/nctalk-call cmd/nctalk-talk && go mod tidy).
package hpbesignaling
```

- [ ] **Step 3: Гейт-тест спайка `integration_test.go`**

```go
//go:build integration

// Спайк на живом HPB (дельта §5.2 — ПЕРВЫЙ шаг реализации, до основной
// логики; правило базовой спеки §12: не compile-only). Доказывает
// канонический flow external-режима и фиксирует фактические поля кадров:
//
//	settings (Basic-auth, weblogin НЕ нужен) → ticket/helloAuthParams →
//	WS hello(auth: ticket) → hello-response(sessionid) →
//	room {roomid, sessionid: OCS из JoinRoom} → room-ack + event join.
//
// Все кадры дампов в t.Logf при NCTALK_DEBUG=1 (ticket маскируется — дельта §4).
// Захваченные обезличенные кадры ложатся в testdata/hpb/*.json.
//
// Мутационный (JoinRoom меняет состояние): env NCTALK_INTEGRATION_ROOM +
// NCTALK_INTEGRATION_HPB=1 (opt-in). Креды — NEXTCLOUD_URL/LOGIN/PASS.
// Сервер с HPB (боевой, TEST-комната); Docker internal-сервер этот тест
// пропустит (signalingMode != external).
package hpbesignaling

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stas-bool/nctalk-cli/internal/config"
	"github.com/stas-bool/nctalk-cli/internal/transport"
)

const (
	spikeTimeout    = 15 * time.Second // общий потолок спайка
	spikeEventsWait = 10 * time.Second // окно чтения событий после room-ack
)

type spikeSettings struct {
	SignalingMode string `json:"signalingMode"`
	Server        string `json:"server"`
	Ticket        string `json:"ticket"`
	UserId        string `json:"userId"`
	HelloAuthParams struct {
		V1 struct {
			Userid string `json:"userid"`
			Ticket string `json:"ticket"`
		} `json:"1.0"`
	} `json:"helloAuthParams"`
}

func TestHPBSpike_TicketHelloRoom(t *testing.T) {
	token := os.Getenv("NCTALK_INTEGRATION_ROOM")
	if token == "" {
		t.Skip("NCTALK_INTEGRATION_ROOM не задан — пропуск HPB-спайка")
	}
	if os.Getenv("NCTALK_INTEGRATION_HPB") != "1" {
		t.Skip("NCTALK_INTEGRATION_HPB != 1 — пропуск HPB-спайка (мутация)")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Skipf("пропуск HPB-спайка: %v", err)
	}
	baseURL, err := url.Parse(cfg.BaseURL)
	if err != nil {
		t.Fatalf("парсинг BaseURL %q: %v", cfg.BaseURL, err)
	}
	auth := transport.Auth{BaseURL: baseURL, Login: cfg.Login, Password: cfg.Password}
	// Basic-auth достаточно; PHP-session (weblogin) нужна только OCS-pull,
	// который в external не используется (дельта §2). Timeout>0 — запросы
	// разовые; WS-диал пойдёт через отдельный клиент без Timeout.
	httpClient := &http.Client{
		Timeout:       cfg.Timeout,
		CheckRedirect: transport.SameHostRedirectPolicy,
	}
	ctx, cancel := context.WithTimeout(context.Background(), spikeTimeout)
	defer cancel()

	// 1. signaling-settings → режим + ticket (отдельного ticket-эндпоинта нет).
	var st spikeSettings
	p := "/ocs/v2.php/apps/spreed/api/v3/signaling/settings"
	if _, err := transport.DoOCS(ctx, httpClient, auth, http.MethodGet, p, nil, nil, false, &st); err != nil {
		t.Fatalf("signaling-settings: %v — без settings транспорт не выбрать (дельта §3)", err)
	}
	t.Logf("settings: signalingMode=%q server=%q userId=%q ticketPresent=%v",
		st.SignalingMode, st.Server, st.UserId, st.Ticket != "")
	if st.SignalingMode != "external" {
		t.Fatalf("signalingMode=%q — сервер без HPB, спайк не применим (это Docker/internal?)", st.SignalingMode)
	}
	userid, ticket := st.UserId, st.Ticket
	if v := st.HelloAuthParams.V1; v.Ticket != "" {
		userid, ticket = v.Userid, v.Ticket // приоритет helloAuthParams["1.0"]
	}
	if ticket == "" || userid == "" || st.Server == "" {
		t.Fatalf("settings без ticket/userid/server — уточнить формат по дампа выше")
	}

	// 2. JoinRoom — OCS-sessionId нужен room-join'у (проверка прав в NC).
	var roomData struct {
		SessionId string `json:"sessionId"`
	}
	jp := fmt.Sprintf("/ocs/v2.php/apps/spreed/api/v4/room/%s/participants/active", token)
	if _, err := transport.DoOCS(ctx, httpClient, auth, http.MethodPost, jp, nil, nil, true, &roomData); err != nil {
		t.Fatalf("JoinRoom(%s): %v", token, err)
	}
	t.Logf("JoinRoom OK: OCS sessionId len=%d", len(roomData.SessionId))

	// 3. WS + hello(auth: ticket).
	wsURL := strings.Replace(strings.Replace(st.Server, "https://", "wss://", 1), "http://", "ws://", 1)
	dialer := &http.Client{Transport: http.DefaultTransport} // БЕЗ Timeout — длительное соединение
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: dialer})
	if err != nil {
		t.Fatalf("WS dial %s: %v", wsURL, transport.SanitizeErr(err))
	}
	defer conn.Close(websocket.StatusNormalClosure, "spike done")

	hello := map[string]any{
		"id": "h", "type": "hello",
		"hello": map[string]any{
			"version": "1.0",
			"auth": map[string]any{
				"type":   "ticket",
				"params": map[string]any{"userid": userid, "ticket": ticket},
			},
		},
	}
	writeSpike(t, ctx, conn, hello)

	// 4. Читаем до hello-response (welcome поглощаем), затем room-join.
	var ownSid string
	deadline := time.Now().Add(spikeEventsWait)
	gotRoomAck, gotJoin := false, false
	writeSpike(t, ctx, conn, map[string]any{
		"id": "r", "type": "room",
		"room": map[string]any{"roomid": token, "sessionid": roomData.SessionId},
	})
	for time.Now().Before(deadline) && !(gotRoomAck && gotJoin) {
		var raw json.RawMessage
		cctx, ccancel := context.WithTimeout(ctx, time.Until(deadline))
		_, r, err := conn.Reader(cctx)
		if err != nil {
			ccancel()
			break
		}
		if err := json.NewDecoder(r).Decode(&raw); err != nil {
			ccancel()
			t.Fatalf("decode кадра: %v", err)
		}
		ccancel()
		dumpSpikeFrame(t, raw, ticket)

		var f struct {
			ID    string `json:"id"`
			Type  string `json:"type"`
			Hello *struct {
				SessionId string `json:"sessionid"`
			} `json:"hello"`
			Room *struct {
				RoomId string `json:"roomid"`
			} `json:"room"`
			Event *struct {
				Target string `json:"target"`
				Type   string `json:"type"`
				Join   []struct {
					SessionId string `json:"sessionid"`
				} `json:"join"`
			} `json:"event"`
		}
		if err := json.Unmarshal(raw, &f); err != nil {
			continue // неканонический кадр — уже задамплен выше
		}
		switch {
		case f.Type == "hello" && f.Hello != nil:
			ownSid = f.Hello.SessionId
		case f.Type == "room" && f.Room != nil && f.Room.RoomId == token:
			gotRoomAck = true
		case f.Type == "event" && f.Event != nil && f.Event.Target == "room" && f.Event.Type == "join":
			for _, j := range f.Event.Join {
				if j.SessionId == ownSid {
					gotJoin = true // себя видели в join-списке
				}
			}
		}
	}

	// ГЕЙТ спайка (дельта §5.2): welcome+hello получены, room-ack + свой entry.
	if ownSid == "" {
		t.Fatal("ГЕЙТ ПРОВАЛЕН: hello-response без sessionid — приветствие не получено")
	}
	if !gotRoomAck {
		t.Fatal("ГЕЙТ ПРОВАЛЕН: room-ack не получен (проверить sessionid из JoinRoom)")
	}
	if !gotJoin {
		t.Fatal("ГЕЙТ ПРОВАЛЕН: event join со своим sessionid не получен")
	}
	t.Logf("ГЕЙТ ПРОЙДЕН: ownSid=%s…, room-ack OK, self-join OK", ownSid[:min(8, len(ownSid))])
}

// writeSpike отправляет JSON-кадр (тестовый helper).
func writeSpike(t *testing.T, ctx context.Context, conn *websocket.Conn, frame any) {
	t.Helper()
	w, err := conn.Writer(ctx)
	if err != nil {
		t.Fatalf("ws writer: %v", err)
	}
	if err := json.NewEncoder(w).Encode(frame); err != nil {
		t.Fatalf("ws encode: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("ws flush: %v", err)
	}
}

// dumpSpikeFrame печатает кадр в t.Logf при NCTALK_DEBUG=1, маскируя ticket.
func dumpSpikeFrame(t *testing.T, raw json.RawMessage, ticket string) {
	t.Helper()
	if os.Getenv("NCTALK_DEBUG") == "" {
		return
	}
	s := string(raw)
	if ticket != "" {
		s = strings.ReplaceAll(s, ticket, "<TICKET-REDACTED>")
	}
	t.Logf("WS<< %s", s)
}
```

Проверка компиляции: `CGO_ENABLED=0 go vet -tags integration ./internal/call/hpbsignaling/`.

- [ ] **Step 4: Прогнать спайк на живом HPB (TEST-комната) и зафиксировать форматы**

```sh
NCTALK_DEBUG=1 \
NEXTCLOUD_URL=<боевой> NEXTCLOUD_LOGIN=<login> NEXTCLOUD_PASS=<app-password> \
NCTALK_INTEGRATION_ROOM=<TEST-token> NCTALK_INTEGRATION_HPB=1 \
CGO_ENABLED=0 go test -tags integration ./internal/call/hpbsignaling/ -run TestHPBSpike -v
```

Ожидание: `ГЕЙТ ПРОЙДЕН`. По дампу `WS<<` сверить и при расхождении поправить ожидания этого теста. Затем перенести обезличенные реальные кадры в фикстуры `testdata/hpb/` (формат ниже — канон из исходников; ЗНАЧЕНИЯ заменить реальными из дампа, хосты → `signaling.example.org`, ticket → `REDACTED-TICKET`, userId → `alice`):

`testdata/hpb/welcome.json`:
```json
{
  "_comment": "Живой кадр HPB (спайк Task 1, 2026-09-23), обезличен. Приходит сразу после WS-connect, ДО hello-response.",
  "id": "",
  "type": "welcome",
  "welcome": {"version": "1.0", "features": ["hello-v2", "server-in-room"]}
}
```

`testdata/hpb/hello.json` (ответ на hello):
```json
{
  "_comment": "Живой кадр HPB (спайк Task 1), обезличен. sessionid — HPB-пространство (НЕ OCS).",
  "id": "h",
  "type": "hello",
  "hello": {
    "version": "1.0",
    "sessionid": "hpb-sess-own-REDACTED",
    "resumeid": "REDACTED-RESUME",
    "userid": "alice",
    "server": {"version": "1.3.2", "country": "XX", "features": ["hello-v2"]}
  }
}
```

`testdata/hpb/room.json` (ack):
```json
{"_comment": "Ack room-join (спайк Task 1).", "id": "r", "type": "room", "room": {"roomid": "tok-test"}}
```

`testdata/hpb/event_join.json`:
```json
{
  "_comment": "event join после room-join: полный список сессий комнаты, включая себя (спайк Task 1).",
  "type": "event",
  "event": {
    "target": "room",
    "type": "join",
    "join": [
      {"sessionid": "hpb-sess-own-REDACTED", "userid": "alice", "roomsessionid": "ocs-sess-own-REDACTED"},
      {"sessionid": "hpb-sess-peer-REDACTED", "userid": "bob"}
    ]
  }
}
```

`testdata/hpb/event_leave.json`:
```json
{"_comment": "event leave (спайк Task 1).", "type": "event", "event": {"target": "room", "type": "leave", "leave": ["hpb-sess-peer-REDACTED"]}}
```

`testdata/hpb/participants_update.json`:
```json
{
  "_comment": "participants update — usersInRoom-аналог с inCall-флагами; полный снапшот (спайк Task 1). Ключ sessionId — camelCase (fallback sessionid); inCall — int-битмаска.",
  "type": "event",
  "event": {
    "target": "participants",
    "type": "update",
    "update": {
      "roomid": "tok-test",
      "users": [
        {"sessionId": "hpb-sess-own-REDACTED", "userId": "alice", "actorId": "alice", "actorType": "users", "inCall": 3},
        {"sessionId": "hpb-sess-peer-REDACTED", "userId": "bob", "actorId": "bob", "actorType": "users", "inCall": 3}
      ]
    }
  }
}
```

`testdata/hpb/message_offer.json`:
```json
{
  "_comment": "P2P-сообщение: data — ОБЪЕКТ (в OCS — JSON-строка!); From = message.sender.sessionid (спайк Task 1).",
  "type": "message",
  "message": {
    "sender": {"type": "session", "sessionid": "hpb-sess-peer-REDACTED", "userid": "bob"},
    "recipient": {"type": "session", "sessionid": "hpb-sess-own-REDACTED"},
    "data": {"type": "offer", "to": "hpb-sess-own-REDACTED", "roomType": "video", "payload": {"type": "offer", "sdp": "v=0\r\n..."}}
  }
}
```

`testdata/hpb/message_candidate.json`:
```json
{
  "_comment": "candidate: payload.candidate — ВЛОЖЕННЫЙ ОБЪЕКТ (инвариант бага #6 базовой спеки; спайк Task 1).",
  "type": "message",
  "message": {
    "sender": {"type": "session", "sessionid": "hpb-sess-peer-REDACTED", "userid": "bob"},
    "recipient": {"type": "session", "sessionid": "hpb-sess-own-REDACTED"},
    "data": {"type": "candidate", "to": "hpb-sess-own-REDACTED", "roomType": "video", "payload": {"candidate": {"candidate": "candidate:1 1 UDP 2130706431 192.168.1.20 54321 typ host", "sdpMLineIndex": 0, "sdpMid": "0"}}}
  }
}
```

`testdata/hpb/README.md` — 3 строки: источник (спайк Task 1, живой HPB, обезличено: хост/ticket/sessionid/userId), «формат обязателен к сверке при правках hpbesignaling» (инвариант фикстур CLAUDE.md), ссылка на дельта-спеку §2.

- [ ] **Step 5: `docs/integration-run.md`** — в таблицы env обеих секций добавить строку и пример запуска:

```markdown
| `NCTALK_INTEGRATION_HPB` | `1` — явный opt-in HPB-спайка/звонка против сервера с внешним signaling (мутация: JoinRoom/WS). |
```

```sh
NCTALK_INTEGRATION_HPB=1 NCTALK_INTEGRATION_ROOM=<token> \
CGO_ENABLED=0 go test -tags integration ./internal/call/hpbsignaling/ -run TestHPBSpike -v
```

- [ ] **Step 6: Проверить обычный набор (спайк за тегом его не видит) и закоммитить**

```sh
CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./...
git add go.mod go.sum internal/call/hpbsignaling/ testdata/hpb/ docs/integration-run.md
git commit -m "feat(call): hpbesignaling — спайк-гейт HPB (ticket→WS hello→room), зависимость coder/websocket, фикстуры"
```

---

### Task 2: capability — Settings-структура с signalingMode/server/ticket (единый стартовый запрос)

**Files:**
- Modify: `internal/call/capability/capability.go`
- Modify: `internal/call/capability/capability_test.go` (11 call-sites `Settings`)
- Modify: `internal/call/capability/integration_test.go` (если зовёт `Settings`)
- Create: `testdata/signaling/capability_external.json`
- Modify: `cmd/nctalk-call/main.go:178-183`, `cmd/nctalk-talk/main.go:125-130` — только call-site (выбор транспорта в Task 7/8)

**Interfaces:**
- Consumes: `webrtc.ICEServer`, `transport.DoOCS` (без изменений).
- Produces (Task 7/8 завязаны на эти имена): `capability.Settings` — структура с полями `ICEServers []webrtc.ICEServer`, `SignalingMode string` (`""`/`"internal"` → internal), `Server string`, `Ticket string`, `Userid string`; метод `func (c *Client) Settings(ctx context.Context) (Settings, error)` (сигнатура меняется с `([]webrtc.ICEServer, error)`).

- [ ] **Step 1: Фикстура external-режима `testdata/signaling/capability_external.json`**

```json
{
  "_comment": "SYNTHETIC from live capture — собран из дампа спайка Task 1 (2026-09-23, живой HPB), обезличен: server → signaling.example.org, ticket → REDACTED-TICKET, userId → alice. Реальный формат GET /api/v3/signaling/settings в external-режиме: ticket + helloAuthParams выдаются ТОЛЬКО при signalingMode != internal (SignalingController::getSettings).",
  "ocs": {
    "meta": {"status": "ok", "statuscode": 200, "message": "OK"},
    "data": {
      "signalingMode": "external",
      "userId": "alice",
      "hideWarning": false,
      "server": "https://signaling.example.org/standalone-signaling/",
      "federation": null,
      "ticket": "REDACTED-TICKET",
      "helloAuthParams": {
        "1.0": {"userid": "alice", "ticket": "REDACTED-TICKET"}
      },
      "stunservers": [{"urls": ["stun:stun.example.org:3478"]}],
      "turnservers": [],
      "sipDialinInfo": ""
    }
  }
}
```

- [ ] **Step 2: Падающий тест (новое поведение) — в `capability_test.go`**

```go
// TestSettings_ExternalMode_DecodesTransportFields — external-фикстура: settings
// отдаёт не только ICE, но и поля выбора транспорта (дельта §3: один запрос).
// Хелперы — ФАКТИЧЕСКИЕ из capability_test.go (внешний тестовый пакет
// capability_test: loadFixture/newClient — ревью плана #3; голый New(...) тут
// не компилируется).
func TestSettings_ExternalMode_DecodesTransportFields(t *testing.T) {
	body := loadFixture(t, "capability_external.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	c := newClient(srv.URL, "secret")
	st, err := c.Settings(context.Background())
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	if st.SignalingMode != "external" {
		t.Errorf("SignalingMode = %q, want external", st.SignalingMode)
	}
	if st.Server == "" || !strings.HasPrefix(st.Server, "https://") {
		t.Errorf("Server = %q, want signaling-сервер URL", st.Server)
	}
	if st.Ticket == "" || st.Userid == "" {
		t.Errorf("Ticket/Userid пустые: %q/%q — external обязан выдать ticket", st.Ticket, st.Userid)
	}
	if len(st.ICEServers) != 1 {
		t.Errorf("ICEServers = %d, want 1 (stun)", len(st.ICEServers))
	}
}
```

- [ ] **Step 3: Запустить — упасть**

```sh
CGO_ENABLED=0 go test ./internal/call/capability/ -run TestSettings_ExternalMode -v
```
Ожидание: FAIL — компиляция: `Settings` возвращает `[]webrtc.ICEServer` (нет полей `.SignalingMode` и т.п.).

- [ ] **Step 4: Реализация — сменить сигнатуру `Settings` и расширить decode**

В `capability.go`: заменить метод `Settings` и `settingsData` (док-комментарий сохранить, дописав абзац):

```go
// Settings запрашивает signaling-settings и возвращает ICE-конфигурацию +
// поля выбора транспорта (дельта 2026-09-23 §3). ОДИН HTTP-запрос на старт —
// второй запрос settings не появляется; ticket из этого же ответа уходит в
// hpbesignaling (первичное подключение), переподключения пере-запрашивают
// settings сами (свежий ticket).
//
// Контракт:
//   - GET /ocs/v2.php/apps/spreed/api/v3/signaling/settings (БЕЗ token);
//   - stunservers/turnservers → []webrtc.ICEServer (как раньше, без изменений
//     маппинга; пустые → nil, это НЕ ошибка);
//   - SignalingMode: "" и "internal" → internal (polling-путь cmd); "external" →
//     HPB. Прочие значения не ожидаются — трактуются как internal;
//   - Server/Ticket/Userid заполняются только в external (в internal пустые);
//     TURN-credentials и ticket НЕ логируются (спека §5, дельта §4).
func (c *Client) Settings(ctx context.Context) (Settings, error) {
	var data settingsData
	if _, err := transport.DoOCS(ctx, c.doer, c.auth, http.MethodGet, pathSignalingSettings, nil, nil, false, &data); err != nil {
		return Settings{}, err
	}

	out := Settings{SignalingMode: data.SignalingMode, Server: data.Server}
	if out.SignalingMode != "external" {
		out.SignalingMode = "internal" // "" и неизвестные → internal (консервативно)
	}
	out.Userid = data.UserId
	if v := data.HelloAuthParams.V1; v.Ticket != "" {
		out.Ticket, out.Userid = v.Ticket, v.Userid // приоритет helloAuthParams["1.0"]
	} else {
		out.Ticket = data.Ticket
	}

	servers := make([]webrtc.ICEServer, 0, len(data.Stunservers)+len(data.Turnservers))
	for _, s := range data.Stunservers {
		servers = append(servers, webrtc.ICEServer{URLs: s.URLs})
	}
	for _, s := range data.Turnservers {
		servers = append(servers, webrtc.ICEServer{
			URLs:           s.URLs,
			Username:       s.Username,
			Credential:     s.Credential,
			CredentialType: webrtc.ICECredentialTypePassword,
		})
	}
	if len(servers) > 0 {
		out.ICEServers = servers
	}
	return out, nil
}

// Settings — результат единственного стартового запроса signaling-settings.
// Потребители: cmd/nctalk-call, cmd/nctalk-talk (выбор транспорта + ICE).
type Settings struct {
	ICEServers    []webrtc.ICEServer // STUN+TURN (любой режим)
	SignalingMode string             // "internal" (default) | "external"
	Server        string             // URL signaling-сервера (только external)
	Ticket        string             // ticket первого подключения (только external)
	Userid        string             // userid для hello-params (обычно == NEXTCLOUD_LOGIN)
}

// settingsData — фрагмент ocs.data ответа signaling-settings. Расширен полями
// выбора транспорта (дельта 2026-09-23): signalingMode/server/ticket/
// helloAuthParams. Federation/sipDialinInfo по-прежнему не декодируются.
type settingsData struct {
	SignalingMode string `json:"signalingMode"`
	Server        string `json:"server"`
	Ticket        string `json:"ticket"`
	UserId        string `json:"userId"`
	HelloAuthParams struct {
		V1 struct {
			Userid string `json:"userid"`
			Ticket string `json:"ticket"`
		} `json:"1.0"`
	} `json:"helloAuthParams"`
	Stunservers []struct {
		URLs []string `json:"urls"`
	} `json:"stunservers"`
	Turnservers []struct {
		URLs       []string `json:"urls"`
		Username   string   `json:"username"`
		Credential string   `json:"credential"`
	} `json:"turnservers"`
}
```

- [ ] **Step 5: Починить call-sites (компиляция репо)**

`cmd/nctalk-call/main.go` (шаг 5 `run`) и `cmd/nctalk-talk/main.go`:

```go
	st, err := capClient.Settings(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "nctalk-call: capability: %v (продолжаем без STUN/TURN)\n", err)
		st.ICEServers = nil // временно до Task 7/8: best-effort сохранён
	}
```
и дальше по коду `iceServers` → `st.ICEServers` (в `agent.Config{ICEServers: st.ICEServers}`). В `capability_test.go` 11 мест `servers, err := c.Settings(...)` (частично `_, err :=` / `_, _ :=`) → `st, err := c.Settings(...)` + `st.ICEServers`; в `capability/integration_test.go` — 1 место (`servers, err :=`, строка ~61) аналогично.

- [ ] **Step 6: Прогнать и закоммитить**

```sh
CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./...
git add internal/call/capability/ testdata/signaling/capability_external.json cmd/nctalk-call/main.go cmd/nctalk-talk/main.go
git commit -m "feat(call): capability.Settings — signalingMode/server/ticket из signaling-settings (единый запрос)"
```

---

### Task 3: `hpbesignaling/protocol.go` — wire-типы и чистый маппинг (без WS)

**Files:**
- Modify: `internal/call/signaling/types.go` (+`EvOwnSession` в конец iota-блока)
- Modify: `internal/call/signaling/integration_test.go` (`kindName` + case)
- Create: `internal/call/hpbsignaling/protocol.go`
- Create: `internal/call/hpbsignaling/protocol_test.go`

**Interfaces:**
- Consumes: `signaling.Event{Kind, Users, From, SDP, Candidate, Err}`, `signaling.User`, `signaling.ICECandidate`, `signaling.Message{Type, To, Payload}`, `signaling.EvOwnSession` (создаётся здесь); фикстуры `testdata/hpb/*.json` (Task 1).
- Produces (Task 4/5 завязаны на эти имена): `newHelloFrame(userid, ticket, id string) clientFrame`; `newRoomFrame(token, roomSessionId, id string) clientFrame`; `newMessageFrame(msg signaling.Message) clientFrame`; `type roomState struct` + `newRoomState() *roomState` + `(st *roomState) applyFrame(f *serverFrame) []signaling.Event`; `type serverFrame struct` (JSON-теги как в фикстурах); `type errFrameError struct{ Code, Message string }` + `(e *errFrameError) Error() string` + `func frameErrAction(code string) frameErrAction` (`frameErrFatal2` для `no_such_room`, `frameErrRefetchTicket` для `invalid_ticket`/`token_expired`, прочие — `frameErrRetry`); `nextBackoff(prev, base, max time.Duration) time.Duration`; `normalizeWSURL(server string) string`.

- [ ] **Step 1: Падающий тест маппинга `protocol_test.go`**

```go
package hpbesignaling

// protocol_test.go — чистый маппинг кадров HPB в signaling.Event (Task 3,
// дельта §2/§3). Фикстуры — живые кадры спайка Task 1 (testdata/hpb/).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stas-bool/nctalk-cli/internal/call/signaling"
)

// fixturePath — путь к testdata/hpb/ (cwd теста = internal/call/hpbsignaling).
func fixturePath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join("..", "..", "..", "testdata", "hpb", name)
}

func loadFrame(t *testing.T, name string) *serverFrame {
	t.Helper()
	b, err := os.ReadFile(fixturePath(t, name))
	if err != nil {
		t.Fatalf("фикстура %s: %v", name, err)
	}
	var f serverFrame
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("декод фикстуры %s: %v", name, err)
	}
	return &f
}

func TestApplyFrame_Hello_EmitsOwnSession(t *testing.T) {
	st := newRoomState()
	evs := st.applyFrame(loadFrame(t, "hello.json"))
	if len(evs) != 1 || evs[0].Kind != signaling.EvOwnSession || evs[0].From == "" {
		t.Fatalf("evs = %+v, want один EvOwnSession с sessionid", evs)
	}
}

func TestApplyFrame_JoinLeave_Participants(t *testing.T) {
	st := newRoomState()
	st.applyFrame(loadFrame(t, "hello.json"))

	// join: снапшот с 2 участниками (self опознаётся только по ownSid —
	// фильтрует агент, адаптер доставляет всех).
	evs := st.applyFrame(loadFrame(t, "event_join.json"))
	if len(evs) != 1 || evs[0].Kind != signaling.EvUsersUpdated || len(evs[0].Users) != 2 {
		t.Fatalf("join: evs = %+v", evs)
	}
	// participants update: те же сессии, но с inCall=3 (мерж, не дубли).
	evs = st.applyFrame(loadFrame(t, "participants_update.json"))
	if len(evs) != 1 || len(evs[0].Users) != 2 {
		t.Fatalf("participants: evs = %+v", evs)
	}
	for _, u := range evs[0].Users {
		if u.InCall != 3 {
			t.Errorf("user %s InCall = %d, want 3 (мерж флагов из update)", u.SessionId, u.InCall)
		}
	}
	// leave: снапшот из 1.
	evs = st.applyFrame(loadFrame(t, "event_leave.json"))
	if len(evs) != 1 || len(evs[0].Users) != 1 || evs[0].Users[0].SessionId != "hpb-sess-own-REDACTED" {
		t.Fatalf("leave: evs = %+v", evs)
	}
}

func TestApplyFrame_Messages(t *testing.T) {
	st := newRoomState()
	evs := st.applyFrame(loadFrame(t, "message_offer.json"))
	if len(evs) != 1 || evs[0].Kind != signaling.EvOffer || evs[0].From == "" || evs[0].SDP == "" {
		t.Fatalf("offer: evs = %+v", evs)
	}
	evs = st.applyFrame(loadFrame(t, "message_candidate.json"))
	if len(evs) != 1 || evs[0].Kind != signaling.EvCandidate {
		t.Fatalf("candidate: evs = %+v", evs)
	}
	if evs[0].Candidate.Candidate == "" || evs[0].Candidate.SDPMLineIndex == nil {
		t.Fatalf("candidate-поля: %+v", evs[0].Candidate)
	}
}

func TestApplyFrame_NoiseSkipped(t *testing.T) {
	st := newRoomState()
	for _, raw := range []string{
		`{"type":"welcome","welcome":{"version":"1.0"}}`,
		`{"id":"r","type":"room","room":{"roomid":"tok-test"}}`,
		`{"type":"event","event":{"target":"roomlist","type":"update"}}`,
		`{"type":"event","event":{"target":"room","type":"join","join":[]}}`,
		`{"type":"event","event":{"target":"participants","type":"update","update":{"roomid":"x","users":[]}}}`,
		`{"type":"message","message":{"sender":{"sessionid":"p"},"data":{"type":"control","payload":{}}}}`,
		`{"type":"dialout","dialout":{}}`,
		`{"type":"message","message":{"sender":{"sessionid":"p"},"data":{"type":"offer","payload":"not-an-object"}}}`,
	} {
		var f serverFrame
		if err := json.Unmarshal([]byte(raw), &f); err != nil {
			t.Fatalf("декод %s: %v", raw, err)
		}
		if evs := st.applyFrame(&f); len(evs) != 0 {
			t.Errorf("%s → %+v, want 0 событий (шум/пустое скипается)", raw, evs)
		}
	}
}

func TestInCallFlags_Tolerant(t *testing.T) {
	var u eventUser
	if err := json.Unmarshal([]byte(`{"sessionId":"s","inCall":3}`), &u); err != nil || int(u.InCall) != 3 {
		t.Fatalf("int inCall: %+v err=%v", u, err)
	}
	if err := json.Unmarshal([]byte(`{"sessionId":"s","inCall":true}`), &u); err != nil || int(u.InCall) != 1 {
		t.Fatalf("bool inCall: %+v err=%v (старые деплои шлют bool)", u, err)
	}
}

func TestNewMessageFrame_AnyType(t *testing.T) {
	payload, _ := json.Marshal(map[string]string{"name": "audio"})
	f := newMessageFrame(signaling.Message{Type: "unmute", To: "peer-1", Payload: payload})
	b, _ := json.Marshal(f)
	want := `{"type":"message","message":{"recipient":{"type":"session","sessionid":"peer-1"},` +
		`"data":{"type":"unmute","to":"peer-1","roomType":"video","payload":{"name":"audio"}}}}`
	if string(b) != want {
		t.Fatalf("кадр = %s, want %s", b, want)
	}
}

func TestFrameErrAction(t *testing.T) {
	if frameErrAction("no_such_room") != frameErrFatal2 {
		t.Error("no_such_room → exit 2")
	}
	for _, c := range []string{"invalid_ticket", "token_expired"} {
		if frameErrAction(c) != frameErrRefetchTicket {
			t.Errorf("%s → refetch ticket", c)
		}
	}
	if frameErrAction("processing_failed") != frameErrRetry {
		t.Error("прочее → retry")
	}
}

func TestNextBackoffAndWSURL(t *testing.T) {
	if got := nextBackoff(0, time.Second, 30*time.Second); got != time.Second {
		t.Errorf("nextBackoff(0) = %v", got)
	}
	if got := nextBackoff(16*time.Second, time.Second, 30*time.Second); got != 30*time.Second {
		t.Errorf("nextBackoff cap = %v", got)
	}
	for in, want := range map[string]string{
		"https://h/sig/": "wss://h/sig/",
		"http://h/sig/":  "ws://h/sig/",
		"wss://h/sig/":   "wss://h/sig/",
	} {
		if got := normalizeWSURL(in); got != want {
			t.Errorf("normalizeWSURL(%q) = %q", in, got)
		}
	}
}
```

- [ ] **Step 2: Запустить — упасть**

```sh
CGO_ENABLED=0 go test ./internal/call/hpbsignaling/ -run TestApplyFrame -v
```
Ожидание: FAIL — `undefined: serverFrame/newRoomState/...`.

- [ ] **Step 3: `signaling.EvOwnSession` (types.go, в конец const-блока)**

```go
	// EvError — фатальная ошибка (401/403 → exit 1; 404 → exit 2), loop завершён.
	// Err несёт *exit.ExitError{Code, Err}. Получив EvError, потребитель должен
	// завершиться с указанным кодом (после штатного leave).
	EvError
	// EvOwnSession — собственный sessionId доставлен транспортом асинхронно
	// (external/HPB-режим: id приходит в WS hello-response ВНУТРИ PollLoop,
	// до первого EvUsersUpdated; отдельное id-пространство от OCS-sessionId
	// из JoinRoom — дельта 2026-09-23 §2.3). From несёт sessionId; consumer
	// устанавливает его как own ДО фильтрации EvUsersUpdated. Internal-транспорт
	// (OCS-polling) это событие НЕ эмитит — там own известен статически.
	EvOwnSession
```

В `signaling/integration_test.go` → `kindName`: добавить `case EvOwnSession: return "EvOwnSession"`. Убедиться, что switch в `peer.HandleEvent` (peer.go:315) не требует правки: агент не маршрутизирует EvOwnSession в пиры (см. Task 6 — только Offer/Answer/Candidate).

- [ ] **Step 4: Реализация `protocol.go`**

```go
// protocol.go — wire-форматы HPB (nextcloud-spreed-signaling, протокол "1.0")
// и чистый маппинг в signaling.Event / из signaling.Message. Дельта §2.
//
// Структурные источники: api_signaling.go / hub.go / room.go
// strukturag/nextcloud-spreed-signaling (v1.3.2), клиентский эталон —
// src/utils/signaling.js nextcloud/spreed (Standalone). Фактические поля
// подтверждены спайком на живом HPB (Task 1) — фикстуры testdata/hpb/.
//
// Отличия от OCS-polling (call/signaling), изолированные ЗДЕСЬ:
//   - кадр — одиночный JSON-объект в WS-текстовом фрейме (не массив конвертов);
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
	helloVersion     = "1.0"     // протокол hello v1 (v2 helloAuthParams — не используем, YAGNI)
	recipientSession = "session" // MessageClientMessageRecipient.Type
	roomTypeVideo    = "video"   // Spreed всегда "video", даже для audio-only (см. signaling.Send)
	idHello          = "h"       // id hello-запроса (ответ матчится по нему)
	idRoom           = "r"       // id room-запроса
)

// ---- Исходящие кадры (клиент → сервер) ----

type clientFrame struct {
	ID      string        `json:"id,omitempty"`
	Type    string        `json:"type"` // "hello" | "room" | "message"
	Hello   *helloFrame   `json:"hello,omitempty"`
	Room    *roomFrame    `json:"room,omitempty"`
	Message *messageFrame `json:"message,omitempty"`
}

type helloAuth struct {
	Type   string     `json:"type"` // "ticket"
	Params authParams `json:"params"`
}

type authParams struct {
	Userid string `json:"userid"`
	Ticket string `json:"ticket"` // НЕ логируется нигде кроме этого кадра (дельта §4)
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
	Data      innerPayload    `json:"data"`
}

// innerPayload — внутренняя форма P2P-сообщения. СОВПАДАЕТ с OCS-формой
// (signaling.Send строит ту же), но передаётся объектом, не строкой.
type innerPayload struct {
	Type     string          `json:"type"`              // offer/answer/candidate/unmute/... — ЛЮБОЙ
	To       string          `json:"to,omitempty"`      // sessionId получателя
	RoomType string          `json:"roomType"`          // "video"
	Payload  json.RawMessage `json:"payload,omitempty"` // SDP / ICE / {"name":"audio"} / ...
}

// newHelloFrame — приветствие с ticket-аутентификацией.
func newHelloFrame(userid, ticket, id string) clientFrame {
	return clientFrame{ID: id, Type: "hello", Hello: &helloFrame{
		Version: helloVersion,
		Auth:    helloAuth{Type: "ticket", Params: authParams{Userid: userid, Ticket: ticket}},
	}}
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
	Hello   *helloResponse `json:"hello,omitempty"`
	Room    *roomAck       `json:"room,omitempty"`
	Event   *eventFrame    `json:"event,omitempty"`
	Message *serverMessage `json:"message,omitempty"`
	Error   *frameError    `json:"error,omitempty"`
}

type helloResponse struct {
	Version   string `json:"version"`
	SessionId string `json:"sessionid"` // наш sessionId (HPB-пространство)
	ResumeId  string `json:"resumeid"`  // resume не используем (re-hello самовосстанавливается)
	UserId    string `json:"userid"`
}

type roomAck struct {
	RoomId string `json:"roomid"`
}

type frameError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type eventFrame struct {
	Target string `json:"target"` // room | participants | roomlist
	Type   string `json:"type"`   // join | leave | update | ...
	Join   []sessionEntry `json:"join,omitempty"`
	Leave  []string       `json:"leave,omitempty"`
	Update *participantsUpdate `json:"update,omitempty"`
}

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
	Data   innerPayload  `json:"data"`
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

type frameErrAction int

const (
	frameErrRetry        frameErrAction = iota // transient — ретрай с backoff
	frameErrRefetchTicket                      // invalid_ticket/token_expired — новый ticket + ретрай
	frameErrFatal2                             // no_such_room — exit 2 без ретрая
)

func frameErrAction(code string) frameErrAction {
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
// клиент делает тот же replace на wss/ws).
func normalizeWSURL(server string) string {
	if strings.HasPrefix(server, "https://") {
		return "wss://" + strings.TrimPrefix(server, "https://")
	}
	if strings.HasPrefix(server, "http://") {
		return "ws://" + strings.TrimPrefix(server, "http://")
	}
	return server
}
```

- [ ] **Step 5: Прогнать тесты протокола + signaling (регресс internal-типов)**

```sh
CGO_ENABLED=0 go test ./internal/call/hpbsignaling/ ./internal/call/signaling/ -v
```
Ожидание: PASS (все TestApplyFrame/TestNewMessageFrame/…; signaling-тесты не задеты — новый Kind в конец iota).

- [ ] **Step 6: Коммит**

```sh
CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./...
git add internal/call/hpbsignaling/protocol.go internal/call/hpbsignaling/protocol_test.go internal/call/signaling/types.go internal/call/signaling/integration_test.go
git commit -m "feat(call): hpbesignaling protocol — wire-типы HPB, roomState-адаптер, EvOwnSession"
```

---

### Task 4: `hpbesignaling/client.go` — WS-сессия: connect с бюджетом, PollLoop, переподключение

**Files:**
- Create: `internal/call/hpbsignaling/client.go`
- Create: `internal/call/hpbsignaling/client_test.go` (мок WS-сервер + сценарии)

**Interfaces:**
- Consumes: `protocol.go` (Task 3); `signaling.New(auth, doer) *signaling.Client` (делегирование JoinCall/LeaveCall); `transport.DoOCS/Doer/Auth/SanitizeErr`; `exit.Exit/ExitError`; `coder/websocket` (`Dial`, `Reader`, `Writer`, `Ping`, `Close`).
- Produces (Task 5/7/8): `type Config struct` (поля ниже); `func New(cfg Config) *Client`; методы `func (c *Client) JoinCall(ctx context.Context, token string, flags int) error` (сначала `ensureConnected` — WS hello/room с бюджетом, ЗАТЕМ делегирование OCS: canonical flow §2, ревью плана #4), `func (c *Client) LeaveCall(ctx context.Context, token string) error`, `func (c *Client) PollLoop(ctx context.Context, token string, ch chan<- signaling.Event) error` (переиспользует соединение, установленное JoinCall; если PollLoop запущен без JoinCall — сам подключается с бюджетом), `func (c *Client) Send(ctx context.Context, token string, msg signaling.Message) error` (Send реализуется в Task 5, но сигнатура/структура — здесь).

- [ ] **Step 1: Мок HPB-сервера — хелпер в начале `client_test.go`**

```go
package hpbesignaling

// client_test.go — транспортные сценарии против мок WS-сервера
// (httptest + websocket.Accept), без сети и без pion.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stas-bool/nctalk-cli/internal/call/signaling"
	"github.com/stas-bool/nctalk-cli/internal/exit"
	"github.com/stas-bool/nctalk-cli/internal/transport"
)

// fakeConn — серверная сторона одного WS-соединения мока.
type fakeConn struct {
	t    *testing.T
	ws   *websocket.Conn
	auth []authParams // все hello-params (для assert ticket)
}

func (fc *fakeConn) readFrame() (clientFrame, error) {
	var f clientFrame
	_, r, err := fc.ws.Read(context.Background())
	if err != nil {
		return f, err
	}
	return f, json.NewDecoder(r).Decode(&f)
}

// awaitHello читает кадры до hello, возвращает userid+ticket.
func (fc *fakeConn) awaitHello() (string, string) {
	fc.t.Helper()
	for {
		f, err := fc.readFrame()
		if err != nil {
			fc.t.Fatalf("awaitHello: %v", err)
		}
		if f.Type == "hello" && f.Hello != nil {
			fc.auth = append(fc.auth, f.Hello.Auth.Params)
			return f.Hello.Auth.Params.Userid, f.Hello.Auth.Params.Ticket
		}
	}
}

func (fc *fakeConn) send(v any) {
	fc.t.Helper()
	w, err := fc.ws.Writer(context.Background())
	if err != nil {
		fc.t.Fatalf("мок writer: %v", err)
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		fc.t.Fatalf("мок encode: %v", err)
	}
	_ = w.Close()
}

func (fc *fakeConn) sendJSON(raw string) {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		fc.t.Fatalf("мок sendJSON: %v", err)
	}
	fc.send(v)
}

func (fc *fakeConn) replyHello(sessionid string) {
	fc.send(map[string]any{"id": idHello, "type": "hello", "hello": map[string]any{
		"version": helloVersion, "sessionid": sessionid, "resumeid": "res-" + sessionid, "userid": "alice",
	}})
}

func (fc *fakeConn) awaitRoom() string {
	fc.t.Helper()
	for {
		f, err := fc.readFrame()
		if err != nil {
			fc.t.Fatalf("awaitRoom: %v", err)
		}
		if f.Type == "room" && f.Room != nil {
			return f.Room.RoomId
		}
	}
}

func (fc *fakeConn) replyRoomAck(token string) {
	fc.send(map[string]any{"id": idRoom, "type": "room", "room": map[string]any{"roomid": token}})
}

func (fc *fakeConn) drop() {
	_ = fc.ws.Close(websocket.StatusInternalError, "мок: обрыв")
}

// fakeHPB — мок signaling-сервера: каждое принятое соединение обслуживается
// session(n) (n — 0-based счётчик коннектов). Все hello-params записываются
// в authLog для assert-ов ticket.
type fakeHPB struct {
	t       *testing.T
	srv     *httptest.Server
	session func(fc *fakeConn, n int)

	mu       sync.Mutex
	connects int
	authLog  []authParams
}

func newFakeHPB(t *testing.T, session func(fc *fakeConn, n int)) *fakeHPB {
	f := &fakeHPB{t: t, session: session}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close(websocket.StatusNormalClosure, "done")
		f.mu.Lock()
		n := f.connects
		f.connects++
		f.mu.Unlock()
		fc := &fakeConn{t: t, ws: ws}
		session(fc, n)
		f.mu.Lock()
		f.authLog = append(f.authLog, fc.auth...)
		f.mu.Unlock()
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeHPB) url() string { return "ws" + strings.TrimPrefix(f.srv.URL, "http") }

func (f *fakeHPB) connectCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connects
}

func (f *fakeHPB) lastAuth() authParams {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.authLog) == 0 {
		return authParams{}
	}
	return f.authLog[len(f.authLog)-1]
}

// fakeOCS — мок Nextcloud для ticket-refetch и JoinCall-делегирования.
type fakeOCS struct {
	srv *httptest.Server
	mu  sync.Mutex
	// tickets раздаются по порядку запросов settings.
	tickets []string
	i       int
	// joinCallPaths фиксирует пути POST /call/{token}.
	joinCallPaths []string
	// onCall — опциональный колбэк в момент POST /call/{token} (тест порядка
	// canonical flow: ws-room ДО ocs-call, ревью плана #4).
	onCall func()
}

func newFakeOCS(t *testing.T, tickets ...string) *fakeOCS {
	o := &fakeOCS{tickets: tickets}
	o.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/signaling/settings"):
			o.mu.Lock()
			tk := ""
			if o.i < len(o.tickets) {
				tk = o.tickets[o.i]
			}
			o.i++
			o.mu.Unlock()
			fmt.Fprintf(w, `{"ocs":{"meta":{"status":"ok","statuscode":200},"data":{`+
				`"signalingMode":"external","userId":"alice","ticket":%q,`+
				`"helloAuthParams":{"1.0":{"userid":"alice","ticket":%q}},`+
				`"stunservers":[],"turnservers":[]}}}}`, tk, tk)
		case strings.Contains(r.URL.Path, "/call/"):
			o.mu.Lock()
			o.joinCallPaths = append(o.joinCallPaths, r.URL.Path)
			cb := o.onCall
			o.mu.Unlock()
			if cb != nil {
				cb()
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ocs":{"meta":{"status":"ok","statuscode":200},"data":[]}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(o.srv.Close)
	return o
}

// setOnCall — потокобезопасная установка колбэка POST /call/{token}.
func (o *fakeOCS) setOnCall(fn func()) {
	o.mu.Lock()
	o.onCall = fn
	o.mu.Unlock()
}

// newTestClient собирает Client против моков.
func newTestClient(f *fakeHPB, o *fakeOCS, mutate func(*Config)) *Client {
	base, _ := url.Parse(o.srv.URL)
	cfg := Config{
		Auth:          transport.Auth{BaseURL: base, Login: "alice", Password: "pw"},
		Doer:          o.srv.Client(),
		Server:        f.url(),
		Ticket:        "ticket-1",
		Userid:        "alice",
		RoomSessionId: "ocs-sess-1",
		ConnectBudget: 3 * time.Second,
		BackoffBase:   20 * time.Millisecond,
		BackoffMax:    100 * time.Millisecond,
		PingPeriod:    time.Hour, // ping отключён (Task 5 включает)
		Stderr:        io.Discard,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	return New(cfg)
}
```

(в импорты добавить `"io"`).

- [ ] **Step 2: Падающие тесты сценариев — туда же в `client_test.go`**

```go
// collectEvents читает ch до таймаута, возвращая события и флаг loop-выхода.
func collectEvents(t *testing.T, ch <-chan signaling.Event, d time.Duration) ([]signaling.Event, bool) {
	t.Helper()
	var got []signaling.Event
	timer := time.After(d)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return got, true
			}
			got = append(got, ev)
		case <-timer:
			return got, false
		}
	}
}

func TestPollLoop_EventsFlow(t *testing.T) {
	f := newFakeHPB(t, func(fc *fakeConn, n int) {
		if _, tk := fc.awaitHello(); tk != "ticket-1" {
			t.Errorf("hello ticket = %q, want ticket-1 (стартовый из settings)", tk)
		}
		fc.replyHello("own-hpb-sid")
		if tok := fc.awaitRoom(); tok != "tok-test" {
			t.Errorf("room = %q", tok)
		}
		fc.replyRoomAck("tok-test")
		fc.sendJSON(`{"type":"event","event":{"target":"room","type":"join","join":[` +
			`{"sessionid":"own-hpb-sid","userid":"alice"},{"sessionid":"peer-1","userid":"bob"}]}}`)
		fc.sendJSON(`{"type":"event","event":{"target":"participants","type":"update","update":{"roomid":"tok-test",` +
			`"users":[{"sessionId":"own-hpb-sid","userId":"alice","inCall":3},{"sessionId":"peer-1","userId":"bob","inCall":3}]}}}`)
		fc.sendJSON(`{"type":"message","message":{"sender":{"type":"session","sessionid":"peer-1","userid":"bob"},` +
			`"recipient":{"type":"session","sessionid":"own-hpb-sid"},` +
			`"data":{"type":"offer","roomType":"video","payload":{"type":"offer","sdp":"v=0"}}}}`)
		time.Sleep(2 * time.Second) // держим соединение
	})
	o := newFakeOCS(t)
	c := newTestClient(f, o, nil)

	ch := make(chan signaling.Event, 16)
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { done <- c.PollLoop(ctx, "tok-test", ch) }()

	evs, _ := collectEvents(t, ch, 2*time.Second)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("PollLoop err = %v, want nil (ctx-выход)", err)
	}

	var sawOwn, sawUsers, sawOffer bool
	for _, ev := range evs {
		switch ev.Kind {
		case signaling.EvOwnSession:
			sawOwn = ev.From == "own-hpb-sid"
		case signaling.EvUsersUpdated:
			sawUsers = len(ev.Users) == 2 && ev.Users[1].InCall == 3
		case signaling.EvOffer:
			sawOffer = ev.From == "peer-1" && ev.SDP == "v=0"
		}
	}
	if !sawOwn || !sawUsers || !sawOffer {
		t.Fatalf("события неполные: own=%v users=%v offer=%v (все=%d)", sawOwn, sawUsers, sawOffer, len(evs))
	}
}

func TestPollLoop_ConnectBudgetExhausted_EvErrorExit1(t *testing.T) {
	// Сервер, отклоняющий upgrade → WS-dial падает; бюджет мал.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	o := newFakeOCS(t)
	base, _ := url.Parse(o.srv.URL)
	c := New(Config{
		Auth: transport.Auth{BaseURL: base, Login: "a", Password: "p"}, Doer: o.srv.Client(),
		Server: "ws" + strings.TrimPrefix(srv.URL, "http"), Ticket: "t", Userid: "a",
		ConnectBudget: 200 * time.Millisecond, BackoffBase: 20 * time.Millisecond, BackoffMax: 50 * time.Millisecond,
		PingPeriod: time.Hour, Stderr: io.Discard,
	})

	ch := make(chan signaling.Event, 4)
	done := make(chan error, 1)
	go func() { done <- c.PollLoop(context.Background(), "tok", ch) }()

	select {
	case ev := <-ch:
		if ev.Kind != signaling.EvError {
			t.Fatalf("первое событие = %v, want EvError", ev.Kind)
		}
		var ee exit.ExitError // VALUE-target, не указатель (инвариант CLAUDE.md)
		if !errors.As(ev.Err, &ee) || ee.Code != exit.ExitGeneric {
			t.Fatalf("EvError.Err = %v, want ExitError{1}", ev.Err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("бюджет не исчерпался — молчаливый ретрай даст exit 0 «я один» (симптом дельты §1)")
	}
	if err := <-done; err == nil {
		t.Fatal("PollLoop вернул nil — должен вернуть ошибку подключения")
	}
}

func TestPollLoop_NoSuchRoom_EvErrorExit2(t *testing.T) {
	f := newFakeHPB(t, func(fc *fakeConn, n int) {
		fc.awaitHello()
		fc.replyHello("own-hpb-sid")
		fc.awaitRoom()
		fc.send(map[string]any{"id": idRoom, "type": "error",
			"error": map[string]string{"code": "no_such_room", "message": "нет такой комнаты"}})
		time.Sleep(time.Second)
	})
	o := newFakeOCS(t)
	c := newTestClient(f, o, nil)

	ch := make(chan signaling.Event, 4)
	done := make(chan error, 1)
	go func() { done <- c.PollLoop(context.Background(), "tok-test", ch) }()

	select {
	case ev := <-ch:
		var ee exit.ExitError // VALUE-target (инвариант CLAUDE.md)
		if ev.Kind != signaling.EvError || !errors.As(ev.Err, &ee) || ee.Code != exit.ExitNotFound {
			t.Fatalf("ev = %+v, want EvError{exit 2}", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no_such_room не фаталится exit 2")
	}
	<-done
}

func TestPollLoop_ReconnectAfterDrop_NewTicket(t *testing.T) {
	f := newFakeHPB(t, func(fc *fakeConn, n int) {
		fc.awaitHello()
		fc.replyHello(fmt.Sprintf("own-hpb-sid-%d", n+1))
		fc.awaitRoom()
		fc.replyRoomAck("tok-test")
		if n == 0 {
			fc.drop() // обрыв первого соединения
			return
		}
		fc.sendJSON(`{"type":"event","event":{"target":"room","type":"join","join":[{"sessionid":"peer-1"}]}}`)
		time.Sleep(2 * time.Second)
	})
	o := newFakeOCS(t, "ticket-2") // один refetch: переподключение №2
	var buf syncBuffer
	c := newTestClient(f, o, func(cfg *Config) { cfg.Stderr = &buf })

	ch := make(chan signaling.Event, 16)
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { done <- c.PollLoop(ctx, "tok-test", ch) }()

	evs, _ := collectEvents(t, ch, 2*time.Second)
	cancel()
	<-done

	if f.connectCount() < 2 {
		t.Fatalf("connects = %d, want >= 2 (переподключение после обрыва)", f.connectCount())
	}
	if f.lastAuth().Ticket != "ticket-2" {
		t.Errorf("hello после переподключения несёт %q, want свежий ticket-2 (refetch из settings)", f.lastAuth().Ticket)
	}
	var sawOwn2 bool
	for _, ev := range evs {
		if ev.Kind == signaling.EvOwnSession && ev.From == "own-hpb-sid-2" {
			sawOwn2 = true // sessionId МЕНЯЕТСЯ между сессиями — агент обязан обновить own
		}
	}
	if !sawOwn2 {
		t.Error("после переподключения не доставлен EvOwnSession с новым sessionid")
	}
	if !strings.Contains(buf.String(), "reconnect") {
		t.Errorf("stderr без диагностики reconnect: %q", buf.String())
	}
}

// syncBuffer — потокобезопасный bytes.Buffer для Stderr-assert'ов.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestJoinCall_ConnectsWSRoomBeforeOCS — canonical flow (дельта §2, ревью
// плана #4): JoinCall обязан установить WS-комнату (hello → room-ack) ДО
// делегирования POST /call/{token} — иначе participants-update с inCall-флагами
// уйдёт комнате без нашей сессии, а join-event флагов не несёт (фильтр
// WITH_AUDIO даст 0 пиров → exit 0 «я один»).
func TestJoinCall_ConnectsWSRoomBeforeOCS(t *testing.T) {
	var mu sync.Mutex
	var order []string
	record := func(s string) { mu.Lock(); order = append(order, s); mu.Unlock() }

	f := newFakeHPB(t, func(fc *fakeConn, n int) {
		fc.awaitHello()
		fc.replyHello("own-hpb-sid")
		if tok := fc.awaitRoom(); tok != "tok-test" {
			t.Errorf("room = %q", tok)
		}
		fc.replyRoomAck("tok-test")
		record("ws-room")
		time.Sleep(time.Second) // держим соединение до конца теста
	})
	o := newFakeOCS(t)
	o.setOnCall(func() { record("ocs-call") })
	c := newTestClient(f, o, nil)

	if err := c.JoinCall(context.Background(), "tok-test", 3); err != nil {
		t.Fatalf("JoinCall: %v", err)
	}
	o.mu.Lock()
	paths := append([]string(nil), o.joinCallPaths...)
	o.mu.Unlock()
	if len(paths) != 1 || !strings.Contains(paths[0], "/call/tok-test") {
		t.Fatalf("JoinCall не дошёл до OCS: %v", paths)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "ws-room" || order[1] != "ocs-call" {
		t.Fatalf("порядок = %v, want [ws-room ocs-call] (canonical flow дельты §2)", order)
	}
}

// TestPollLoop_HungServer_BudgetFiresEvError — сервер принял TCP, но не
// отвечает (аналог SYN-blackhole/молчащего файрвола): per-attempt deadline
// обязан оборвать зависший dial в бюджет, а не ждать OS-таймаута TCP
// (ревью плана #5) — иначе EvError{exit 1} придёт после ICE-таймера (exit 0
// «я один»).
func TestPollLoop_HungServer_BudgetFiresEvError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() { // принимаем соединения и молчим: ни HTTP-ответа, ни upgrade
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c
		}
	}()
	o := newFakeOCS(t)
	base, _ := url.Parse(o.srv.URL)
	c := New(Config{
		Auth:          transport.Auth{BaseURL: base, Login: "a", Password: "p"},
		Doer:          o.srv.Client(),
		Server:        "ws://" + ln.Addr().String(),
		Ticket:        "t", Userid: "a",
		ConnectBudget: 200 * time.Millisecond, BackoffBase: 20 * time.Millisecond, BackoffMax: 50 * time.Millisecond,
		PingPeriod:    time.Hour, Stderr: io.Discard,
	})

	ch := make(chan signaling.Event, 4)
	done := make(chan error, 1)
	go func() { done <- c.PollLoop(context.Background(), "tok", ch) }()

	select {
	case ev := <-ch:
		var ee exit.ExitError // VALUE-target (инвариант CLAUDE.md)
		if ev.Kind != signaling.EvError || !errors.As(ev.Err, &ee) || ee.Code != exit.ExitGeneric {
			t.Fatalf("ev = %+v, want EvError{exit 1} (бюджет оборвал зависший dial)", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("зависший dial не оборван бюджетом — EvError после ICE-таймера дал бы exit 0 «я один»")
	}
	if err := <-done; err == nil {
		t.Fatal("PollLoop вернул nil — должен вернуть ошибку подключения")
	}
}
```

(в импорты теста добавить `"io"`, `"errors"` и `"net"`; `errors.As` с value-target `var ee exit.ExitError` — инвариант CLAUDE.md: `exit.ExitError` — VALUE-тип, НЕ указатель).

- [ ] **Step 3: Запустить — упасть**

```sh
CGO_ENABLED=0 go test ./internal/call/hpbsignaling/ -run 'TestPollLoop|TestJoinCall' -v
```
Ожидание: FAIL — `undefined: New/Config/Client`.

- [ ] **Step 4: Реализация `client.go`**

```go
// client.go — WS-транспорт signaling для HPB (дельта 2026-09-23 §2/§3).
// Реализует agent.sigClient ЦЕЛИКОМ: PollLoop/Send — WebSocket, LeaveCall —
// делегирование OCS-клиенту call/signaling (Call API v4 работает при любом
// signalingMode), JoinCall — ensureConnected (WS hello/room с бюджетом) +
// делегирование OCS.
//
// Модель подключений (дельта §2/§3):
//   - canonical flow: JoinRoom (cmd) → WS hello/room → JoinCall (OCS) →
//     события. WS-комнату устанавливает JoinCall ДО делегирования (ревью
//     плана #4): participants-update с inCall-флагами, порождённый JoinCall,
//     обязан расходиться по комнате, в которой наша сессия УЖЕ есть —
//     join-event флагов не несёт, «опоздавший» их не увидит (фильтр
//     WITH_AUDIO даст 0 пиров → exit 0 «я один», симптом §1);
//   - первичное (ticket → WS → hello → room-ack) — ОГРАНИЧЕННЫЙ бюджет
//     (ConnectBudget, дефолт 15с ≈ половина NCTALK_ICE_TIMEOUT), причём
//     per-attempt context.WithDeadline (ревью плана #5): зависший dial или
//     молчащий сервер обрываются в бюджет, а не ждут OS-таймаута TCP;
//     исчерпание → ошибка JoinCall (агент выйдет до старта ICE-таймера)
//     или EvError{exit 1} в канал ДО ICE-таймера — иначе молчаливый ретрай
//     даст exit 0 «я один в звонке» (исходный симптом дельты §1);
//   - переподключение ПОСЛЕ установления — бесконечный backoff 1с→30с БЕЗ
//     дедлайна бюджета, новый ticket на каждое (fetchTicket), peers стоят,
//     звонок живёт;
//   - reconnect = ПОЛНЫЙ re-hello + re-room-join (без resumeid): сервер сам
//     присылает полный join-список, reconcile агента самовосстанавливается.
package hpbesignaling

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/stas-bool/nctalk-cli/internal/call/signaling"
	"github.com/stas-bool/nctalk-cli/internal/exit"
	"github.com/stas-bool/nctalk-cli/internal/transport"
)

const (
	// pathCallFmt дублирует signaling.pathCallFmt (там unexported): JoinCall/
	// LeaveCall делегируются вложенному signaling.Client, а этот путь нужен
	// только его док-контексту — оставлен для симметрии/будущих правок.
	pathCallFmt = "/ocs/v2.php/apps/spreed/api/v4/call/%s"
	// pathSignalingSettings — источник ticket при переподключениях (дельта §2:
	// отдельного ticket-эндпоинта нет, ticket живёт в settings-ответе).
	pathSignalingSettings = "/ocs/v2.php/apps/spreed/api/v3/signaling/settings"

	defaultConnectBudget = 15 * time.Second // ≈ NCTALK_ICE_TIMEOUT(30с)/2 (дельта §3)
	defaultBackoffBase   = 1 * time.Second
	defaultBackoffMax    = 30 * time.Second
	defaultPingPeriod    = 30 * time.Second // сервер пингует ~54с; наш ping — NAT + half-open
	writeTimeout         = 5 * time.Second  // на один исходящий кадр
)

// Config — параметры WS-сессии. Ticket/Userid — из стартового settings-запроса
// (capability.Settings, ОДИН запрос на старт — дельта §3); переподключения
// пере-запрашивают settings сами (свежий ticket).
type Config struct {
	Auth          transport.Auth // Basic-auth для OCS (ticket/JoinCall/LeaveCall)
	Doer          transport.Doer // OCS-транспорт (обычно *http.Client с Timeout)
	Server        string         // settings.server (https/wss — нормализуется)
	Ticket        string         // ticket первого подключения
	Userid        string         // userid для hello-params
	RoomSessionId string         // OCS-sessionId из JoinRoom (room-join; НЕ own-фильтр!)
	ConnectBudget time.Duration  // 0 → defaultConnectBudget
	BackoffBase   time.Duration  // 0 → 1с (тесты сжимают)
	BackoffMax    time.Duration  // 0 → 30с
	PingPeriod    time.Duration  // 0 → 30с
	Stderr        io.Writer      // диагностика ( reconnect / budget ); nil → io.Discard
}

// Client — HPB-signaling клиент. Потокобезопасен: Send/Ping сериализуются
// мьютексом на conn; PollLoop — единственный владелец read-стороны.
type Client struct {
	cfg Config
	ocs *signaling.Client // делегирование JoinCall/LeaveCall (Call API — OCS)

	mu      sync.Mutex
	conn    *websocket.Conn   // nil между (пере)подключениями
	state   *roomState        // снапшот текущей WS-сессии (nil между подключениями)
	pending []signaling.Event // события connect-фазы при JoinCall-подключении (раздаёт PollLoop)
}

// New — конструктор. doer используется и для OCS-делегирования, и для
// ticket-refetch; WS-диал идёт через собственный http.Client БЕЗ Timeout
// (Timeout на http.Client убивает длительное WS-соединение).
func New(cfg Config) *Client {
	if cfg.BackoffBase == 0 {
		cfg.BackoffBase = defaultBackoffBase
	}
	if cfg.BackoffMax == 0 {
		cfg.BackoffMax = defaultBackoffMax
	}
	if cfg.PingPeriod == 0 {
		cfg.PingPeriod = defaultPingPeriod
	}
	if cfg.Stderr == nil {
		cfg.Stderr = io.Discard
	}
	return &Client{cfg: cfg, ocs: signaling.New(cfg.Auth, cfg.Doer)}
}

// JoinCall — Call API (POST /api/v4/call/{token}) ПОСЛЕ установления
// WS-комнаты: canonical flow дельты §2 (JoinRoom → ticket → WS hello/room →
// JoinCall; ревью плана #4 — иначе participants-update с inCall-флагами
// уйдёт комнате без нашей сессии). Ошибка подключения — ошибка JoinCall:
// агент выйдет по ней до старта ICE-таймера.
func (c *Client) JoinCall(ctx context.Context, token string, flags int) error {
	if err := c.ensureConnected(ctx, token); err != nil {
		return err
	}
	return c.ocs.JoinCall(ctx, token, flags)
}

// LeaveCall — делегирование Call API (DELETE /api/v4/call/{token}).
func (c *Client) LeaveCall(ctx context.Context, token string) error {
	return c.ocs.LeaveCall(ctx, token)
}

// ensureConnected — первичное подключение с бюджетом (идемпотентно: при живом
// соединении no-op). События connect-фазы буферизуются в pending: у JoinCall
// нет канала событий, их раздаст PollLoop. Per-attempt deadline — зависший
// dial/молчащий hello не переживают бюджет (ревью плана #5).
func (c *Client) ensureConnected(ctx context.Context, token string) error {
	c.mu.Lock()
	connected := c.conn != nil
	c.mu.Unlock()
	if connected {
		return nil
	}
	budget := c.cfg.ConnectBudget
	if budget == 0 {
		budget = defaultConnectBudget
	}
	actx, cancel := context.WithDeadline(ctx, time.Now().Add(budget))
	defer cancel()
	conn, st, pending, err := c.connect(actx, token, false)
	if err != nil {
		return fmt.Errorf("hpbesignaling: подключение к signaling-серверу не удалось за %s (бюджет первичного подключения): %w", budget, err)
	}
	c.mu.Lock()
	c.state = st
	c.pending = append(c.pending, pending...)
	c.mu.Unlock()
	c.setConn(conn)
	return nil
}

// PollLoop — главный цикл транспорта. Семантика выхода — как у OCS-polling
// (signaling.Client.PollLoop): ctx.Done → nil; фатал (бюджет исчерпан /
// no_such_room) → EvError в ch ДО return и возврат ошибки. Если JoinCall уже
// установил соединение (canonical flow) — переиспользует его и раздаёт
// буферизованные события connect-фазы (включая EvOwnSession). Если PollLoop
// запущен без JoinCall — сам подключается с бюджетом (тесты/защита).
func (c *Client) PollLoop(ctx context.Context, token string, ch chan<- signaling.Event) error {
	budget := c.cfg.ConnectBudget
	if budget == 0 {
		budget = defaultConnectBudget
	}
	deadline := time.Now().Add(budget)
	established := false
	firstAttempt := true // первый коннект тратит стартовый ticket (один settings-запрос)
	var backoff time.Duration

	for {
		if err := ctx.Err(); err != nil {
			return nil // штатный выход (вызывающий отменит ctx; LeaveCall — отдельно)
		}
		c.mu.Lock()
		conn, st := c.conn, c.state
		c.mu.Unlock()
		if conn == nil {
			// per-attempt deadline — ТОЛЬКО пока соединение не установлено
			// (reconnect-попытки после обрыва живут без дедлайна бюджета):
			// зависший dial/молчащий hello обрываются в бюджет (ревью плана #5).
			callCtx := ctx
			var cancel context.CancelFunc
			if !established {
				callCtx, cancel = context.WithDeadline(ctx, deadline)
			}
			var pending []signaling.Event
			var cerr error
			conn, st, pending, cerr = c.connect(callCtx, token, !firstAttempt)
			if cancel != nil {
				cancel()
			}
			if cerr != nil {
				var fe *errFrameError
				switch {
				case errors.As(cerr, &fe) && frameErrAction(fe.Code) == frameErrFatal2:
					return c.fatal(ch, ctx, exit.ExitNotFound, cerr)
				case !established && !time.Now().Before(deadline):
					return c.fatal(ch, ctx, exit.ExitGeneric,
						fmt.Errorf("подключение к signaling-серверу не удалось за %s (бюджет первичного подключения): %w", budget, cerr))
				}
				backoff = nextBackoff(backoff, c.cfg.BackoffBase, c.cfg.BackoffMax)
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(backoff):
					firstAttempt = false // ретрай тоже пере-запрашивает ticket (invalid_ticket лечится сам)
					continue
				}
			}
			c.setConn(conn)
			for _, ev := range pending {
				if !sendEvent(ctx, ch, ev) {
					return nil
				}
			}
		} else {
			// Соединение установлено JoinCall: забираем буфер connect-фазы.
			c.mu.Lock()
			pending := c.pending
			c.pending = nil
			c.mu.Unlock()
			for _, ev := range pending {
				if !sendEvent(ctx, ch, ev) {
					return nil
				}
			}
		}
		established = true
		firstAttempt = false
		backoff = 0

		_ = c.readLoop(ctx, conn, st, ch)
		_ = conn.Close(websocket.StatusNormalClosure, "loop exit")
		c.setConn(nil)
		if ctx.Err() != nil {
			return nil
		}
		fmt.Fprintln(c.cfg.Stderr, "signaling: reconnect") // диагностика обрыва (дельта §4)
	}
}

// fatal — EvError в канал (уважая ctx) + возврат ошибки.
func (c *Client) fatal(ch chan<- signaling.Event, ctx context.Context, code int, err error) error {
	sendEvent(ctx, ch, signaling.Event{Kind: signaling.EvError, Err: exit.Exit(code, err)})
	return err
}

func sendEvent(ctx context.Context, ch chan<- signaling.Event, ev signaling.Event) bool {
	select {
	case ch <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

// connect — первичное подключение: (ticket) → dial → hello → hello-response →
// room-join → room-ack. События, пришедшие ДО ack (join-список), буферизуются
// и возвращаются (включая EvOwnSession из hello-response).
func (c *Client) connect(ctx context.Context, token string, refetchTicket bool) (*websocket.Conn, *roomState, []signaling.Event, error) {
	userid, ticket := c.cfg.Userid, c.cfg.Ticket
	if refetchTicket || ticket == "" {
		u, tk, err := c.fetchTicket(ctx)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("hpbsignaling: ticket: %w", err)
		}
		userid, ticket = u, tk
	}

	conn, _, err := websocket.Dial(ctx, normalizeWSURL(c.cfg.Server), &websocket.DialOptions{
		HTTPClient: &http.Client{Transport: http.DefaultTransport}, // без Timeout — длительное WS
	})
	if err != nil {
		return nil, nil, nil, transport.SanitizeErr(fmt.Errorf("hpbsignaling: ws dial: %w", err))
	}

	st := newRoomState()
	var pending []signaling.Event
	// hello → ждём hello-response (welcome поглощаем applyFrame'ом).
	if err := writeFrame(ctx, conn, newHelloFrame(userid, ticket, idHello)); err != nil {
		_ = conn.Close(websocket.StatusNormalClosure, "")
		return nil, nil, nil, fmt.Errorf("hpbsignaling: hello: %w", err)
	}
	if err := waitReply(ctx, conn, st, idHello, func(f *serverFrame) error {
		if f.Hello == nil || f.Hello.SessionId == "" {
			return fmt.Errorf("hello-response без sessionid (формат дрейфанул — см. спайк)")
		}
		return nil
	}, &pending); err != nil {
		_ = conn.Close(websocket.StatusNormalClosure, "")
		return nil, nil, nil, err
	}
	// room-join → ждём ack (OCS-sessionId нужен серверу для проверки прав).
	if err := writeFrame(ctx, conn, newRoomFrame(token, c.cfg.RoomSessionId, idRoom)); err != nil {
		_ = conn.Close(websocket.StatusNormalClosure, "")
		return nil, nil, nil, fmt.Errorf("hpbsignaling: room: %w", err)
	}
	if err := waitReply(ctx, conn, st, idRoom, nil, &pending); err != nil {
		_ = conn.Close(websocket.StatusNormalClosure, "")
		return nil, nil, nil, err
	}
	c.setConn(conn)
	return conn, st, pending, nil
}

// waitReply читает кадры до ответа с id==want; попутные кадры идут через
// roomState (буфер событий) — join-список приходит сразу после ack.
func waitReply(ctx context.Context, conn *websocket.Conn, st *roomState, want string, check func(*serverFrame) error, pending *[]signaling.Event) error {
	for {
		f, err := readFrame(ctx, conn)
		if err != nil {
			return fmt.Errorf("hpbsignaling: чтение ответа %s: %w", want, err)
		}
		if f.Type == "error" && f.Error != nil {
			return &errFrameError{Code: f.Error.Code, Message: f.Error.Message}
		}
		*pending = append(*pending, st.applyFrame(f)...)
		if f.ID == want {
			if check != nil {
				if err := check(f); err != nil {
					return err
				}
			}
			return nil
		}
	}
}

// readLoop — чтение до обрыва/ctx; каждый кадр через roomState → события в ch.
func (c *Client) readLoop(ctx context.Context, conn *websocket.Conn, st *roomState, ch chan<- signaling.Event) error {
	for {
		f, err := readFrame(ctx, conn)
		if err != nil {
			return err // обрыв → PollLoop переподключится
		}
		if f.Type == "error" && f.Error != nil && f.ID == "" {
			// асинхронная ошибка: token_expired/invalid_ticket чинятся reconnect'ом
			// (fetchTicket), прочие — лог и продолжаем (не роняем звонок).
			fmt.Fprintf(c.cfg.Stderr, "signaling: error %s: %s\n", f.Error.Code, f.Error.Message)
			if frameErrAction(f.Error.Code) == frameErrRefetchTicket {
				return &errFrameError{Code: f.Error.Code, Message: f.Error.Message}
			}
			continue
		}
		for _, ev := range st.applyFrame(f) {
			if !sendEvent(ctx, ch, ev) {
				return nil
			}
		}
	}
}

// fetchTicket — свежий ticket из signaling-settings (Basic-auth; weblogin не
// нужен — дельта §2). Ticket не логируется.
func (c *Client) fetchTicket(ctx context.Context) (userid, ticket string, err error) {
	var data struct {
		Ticket string `json:"ticket"`
		UserId string `json:"userId"`
		HelloAuthParams struct {
			V1 struct {
				Userid string `json:"userid"`
				Ticket string `json:"ticket"`
			} `json:"1.0"`
		} `json:"helloAuthParams"`
	}
	if _, err := transport.DoOCS(ctx, c.cfg.Doer, c.cfg.Auth, http.MethodGet,
		pathSignalingSettings, nil, nil, false, &data); err != nil {
		return "", "", err
	}
	if v := data.HelloAuthParams.V1; v.Ticket != "" {
		return v.Userid, v.Ticket, nil
	}
	return data.UserId, data.Ticket, nil
}

// setConn — потокобезопасная замена активного соединения (Send/ping видят
// его). nil заодно обнуляет state — снапшот живёт ровно одну WS-сессию.
func (c *Client) setConn(conn *websocket.Conn) {
	c.mu.Lock()
	c.conn = conn
	if conn == nil {
		c.state = nil
	}
	c.mu.Unlock()
}

// ---- кодирование кадров ----

// writeFrame — один исходящий JSON-кадр (единственный Writer в моменте —
// контракт coder/websocket).
func writeFrame(ctx context.Context, conn *websocket.Conn, f clientFrame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return transport.SanitizeErr(err)
	}
	w, err := conn.Writer(ctx)
	if err != nil {
		return transport.SanitizeErr(err)
	}
	if _, err := w.Write(b); err != nil {
		_ = w.Close()
		return transport.SanitizeErr(err)
	}
	return w.Close()
}

// readFrame — один входящий кадр + debug-дамп без ticket (дельта §4).
func readFrame(ctx context.Context, conn *websocket.Conn) (*serverFrame, error) {
	_, r, err := conn.Reader(ctx)
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	hpbDebug("WS<< %s", redactTicket(string(b)))
	var f serverFrame
	if err := json.Unmarshal(b, &f); err != nil {
		// Поломанный кадр не роняет loop (как parseEnvelopes в call/signaling).
		hpbDebug("decode fail: %v", err)
		return &serverFrame{}, nil
	}
	return &f, nil
}

// redactTicket маскирует значение ticket в debug-дампе: hello-кадр содержит
// его в params.ticket (ответ settings сюда не попадает — это не WS-кадр).
func redactTicket(s string) string {
	i := strings.Index(s, `"ticket":"`)
	if i < 0 {
		return s
	}
	rest := s[i+len(`"ticket":"`):]
	if j := strings.Index(rest, `"`); j >= 0 {
		rest = rest[j:]
	}
	return s[:i] + `"ticket":"<REDACTED>"` + rest
}

// hpbDebug — отладка протокола (NCTALK_DEBUG=1), как signaling.signalingDebug.
func hpbDebug(format string, a ...any) {
	if os.Getenv("NCTALK_DEBUG") == "" {
		return
	}
	fmt.Fprintf(os.Stderr, "DEBUG hpbsignaling: "+format+"\n", a...)
}
```

(в импорты client.go добавить `"encoding/json"` и `"strings"`).

- [ ] **Step 5: Прогнать сценарии**

```sh
CGO_ENABLED=0 go test ./internal/call/hpbsignaling/ -v
```
Ожидание: PASS — все транспортные тесты; protocol-тесты Task 3 не регрессируют.

- [ ] **Step 6: Коммит**

```sh
CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./...
git add internal/call/hpbsignaling/client.go internal/call/hpbsignaling/client_test.go
git commit -m "feat(call): hpbesignaling client — WS-сессия с бюджетом подключения и переподключением"
```

---

### Task 5: `Send` (любой Type) + keepalive-ping + compile-guard интерфейса

**Files:**
- Modify: `internal/call/hpbsignaling/client.go`
- Modify: `internal/call/hpbsignaling/client_test.go`
- Create: `internal/call/agent/hpb_compat_test.go`

**Interfaces:**
- Consumes: `newMessageFrame` (Task 3), `c.setConn` (Task 4), `agent.sigClient` (unexported — guard в пакете agent).
- Produces: полный метод `func (c *Client) Send(ctx context.Context, token string, msg signaling.Message) error`; ping-горутина в `readLoop`.

- [ ] **Step 1: Падающие тесты**

```go
// TestSend_WrapsAnyType — критично: unmute обязан проходить (whitelist ломает
// звук на живом HPB — root cause spike-gate 2026-07-20; unit-мок бы не заметил).
func TestSend_WrapsAnyType(t *testing.T) {
	var got []clientFrame
	var mu sync.Mutex
	f := newFakeHPB(t, func(fc *fakeConn, n int) {
		fc.awaitHello()
		fc.replyHello("own-hpb-sid")
		fc.awaitRoom()
		fc.replyRoomAck("tok-test")
		for {
			fr, err := fc.readFrame()
			if err != nil {
				return // соединение закрыто тестом
			}
			mu.Lock()
			got = append(got, fr)
			mu.Unlock()
		}
	})
	o := newFakeOCS(t)
	c := newTestClient(f, o, nil)

	ch := make(chan signaling.Event, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.PollLoop(ctx, "tok-test", ch) }()

	// Ждём установления (EvOwnSession) — Send до подключения обязан ошибаться.
	if !waitForEvent(t, ch, signaling.EvOwnSession, 2*time.Second) {
		t.Fatal("соединение не установилось")
	}
	payload, _ := json.Marshal(map[string]string{"name": "audio"})
	if err := c.Send(ctx, "tok-test", signaling.Message{Type: "unmute", To: "peer-1", Payload: payload}); err != nil {
		t.Fatalf("Send(unmute): %v", err)
	}
	if err := c.Send(ctx, "tok-test", signaling.Message{Type: "candidate", To: "peer-1",
		Payload: json.RawMessage(`{"candidate":{"candidate":"cand-1","sdpMLineIndex":0,"sdpMid":"0"}}`)}); err != nil {
		t.Fatalf("Send(candidate): %v", err)
	}

	// Даём мок-серверу время прочитать оба кадра (Read в горутине сессии).
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 2 || !time.Now().Before(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) < 2 {
		t.Fatalf("сервер получил %d кадров, want >= 2", len(got))
	}
	u := got[0].Message
	if u == nil || u.Data.Type != "unmute" || u.Recipient.SessionId != "peer-1" ||
		string(u.Data.Payload) != `{"name":"audio"}` {
		t.Errorf("unmute-кадр = %+v", u)
	}
	cd := got[1].Message
	if cd == nil || cd.Data.Type != "candidate" || cd.Recipient.Type != "session" {
		t.Errorf("candidate-кадр = %+v", cd)
	}
}

// waitForEvent ждёт событие заданного Kind (true) до таймаута (false).
func waitForEvent(t *testing.T, ch <-chan signaling.Event, kind signaling.EventKind, d time.Duration) bool {
	t.Helper()
	timer := time.After(d)
	for {
		select {
		case ev := <-ch:
			if ev.Kind == kind {
				return true
			}
		case <-timer:
			return false
		}
	}
}

// TestSend_NoConnection_Error — Send между (пере)подключениями — явная ошибка.
func TestSend_NoConnection_Error(t *testing.T) {
	o := newFakeOCS(t)
	base, _ := url.Parse(o.srv.URL)
	c := New(Config{
		Auth: transport.Auth{BaseURL: base, Login: "alice", Password: "pw"},
		Doer: o.srv.Client(), Server: "ws://127.0.0.1:1",
		Ticket: "t", Userid: "a", ConnectBudget: 50 * time.Millisecond,
		BackoffBase: 10 * time.Millisecond, BackoffMax: 20 * time.Millisecond,
		PingPeriod: time.Hour, Stderr: io.Discard,
	})
	if err := c.Send(context.Background(), "tok", signaling.Message{Type: "offer"}); err == nil {
		t.Fatal("Send без соединения = nil, want ошибка")
	}
}

// TestPing_KeepsConnectionAlive — ping не убивает живое соединение; события
// приходят и спустя несколько ping-периодов (мок отвечает pong автоматически —
// coder/websocket; сервер читает кадры, чтобы control-фреймы обрабатывались).
func TestPing_KeepsConnectionAlive(t *testing.T) {
	f := newFakeHPB(t, func(fc *fakeConn, n int) {
		fc.awaitHello()
		fc.replyHello("own-hpb-sid")
		fc.awaitRoom()
		fc.replyRoomAck("tok-test")
		// Фоновое чтение: coder/websocket обрабатывает control-фреймы (наши
		// ping → auto-pong) только при активном Read — без него клиент
		// справедливо разорвёт «молчащую» связь.
		go func() {
			for {
				if _, err := fc.readFrame(); err != nil {
					return
				}
			}
		}()
		time.Sleep(500 * time.Millisecond) // > 4 ping-тиков при PingPeriod=100ms
		fc.sendJSON(`{"type":"event","event":{"target":"room","type":"join","join":[{"sessionid":"peer-1"}]}}`)
		time.Sleep(300 * time.Millisecond)
	})
	o := newFakeOCS(t)
	c := newTestClient(f, o, func(cfg *Config) { cfg.PingPeriod = 100 * time.Millisecond })

	ch := make(chan signaling.Event, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.PollLoop(ctx, "tok-test", ch) }()

	evs, _ := collectEvents(t, ch, 1200*time.Millisecond)
	if f.connectCount() != 1 {
		t.Fatalf("connects = %d, want 1 (ping не должен рвать живое соединение)", f.connectCount())
	}
	sawUsers := false
	for _, ev := range evs {
		if ev.Kind == signaling.EvUsersUpdated && len(ev.Users) == 1 {
			sawUsers = true
		}
	}
	if !sawUsers {
		t.Fatal("событие после ping-периодов не доставлено — соединение умерло")
	}
}
```

(все три теста используют helpers Task 4: `newFakeHPB`/`newFakeOCS`/`newTestClient`/`collectEvents` + новый `waitForEvent`; в `TestSend_NoConnection_Error` auth собирается inline — `base, _ := url.Parse(o.srv.URL)`, как в `newTestClient`.)

- [ ] **Step 2: Запустить — упасть**

```sh
CGO_ENABLED=0 go test ./internal/call/hpbsignaling/ -run 'TestSend|TestPing' -v
```
Ожидание: FAIL — `c.Send undefined` (и ping не реализован).

- [ ] **Step 3: Реализация — Send + ping-горутина в readLoop**

В `client.go` добавить (после `LeaveCall`):

```go
// Send отправляет исходящее signaling-сообщение через WS-кадр message
// (дельта §2.5). token ИГНОРИРУЕТСЯ: WS-сессия уже привязана к комнате
// room-join'ом (сигнатура — контракт agent.sigClient). Type — ЛЮБОЙ:
// offer/answer/candidate/unmute/будущие control-типы; меняется только
// транспортировка (WS вместо form-encoded POST), НЕ состав сообщений.
func (c *Client) Send(ctx context.Context, token string, msg signaling.Message) error {
	_ = token
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return errors.New("hpbsignaling: нет активного WS-соединения (переподключение)")
	}
	wctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return writeFrame(wctx, conn, newMessageFrame(msg))
}
```

Ping-горутина — обернуть тело `readLoop` (первой строкой — запуск, defer — стоп):

```go
func (c *Client) readLoop(ctx context.Context, conn *websocket.Conn, st *roomState, ch chan<- signaling.Event) error {
	// Keepalive: conn.Ping блокирует до pong — детекция half-open + NAT.
	// Ошибка ping → Close разблокирует Reader в основном цикле.
	pingStop := make(chan struct{})
	pingDone := make(chan struct{})
	go func() {
		defer close(pingDone)
		t := time.NewTicker(c.cfg.PingPeriod)
		defer t.Stop()
		for {
			select {
			case <-pingStop:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				pctx, pcancel := context.WithTimeout(ctx, c.cfg.PingPeriod)
				if err := conn.Ping(pctx); err != nil {
					hpbDebug("ping: %v — рвём соединение", err)
					_ = conn.Close(websocket.StatusGoingAway, "ping timeout")
					pcancel()
					return
				}
				pcancel()
			}
		}
	}()
	defer func() { close(pingStop); <-pingDone }()
	// ... прежнее тело цикла чтения без изменений ...
```

- [ ] **Step 4: Compile-guard интерфейса — `internal/call/agent/hpb_compat_test.go`**

```go
package agent

// hpb_compat_test.go — compile-time гарантия (дельта §3, аналог var _
// sigClient = (*signaling.Client)(nil) в agent.go): hpbesignaling.Client
// реализует agent.sigClient ЦЕЛИКОМ (4 метода). Рассинхрон сигнатур роняет
// сборку тестов agent'а ещё до wiring'а в cmd.
import "github.com/stas-bool/nctalk-cli/internal/call/hpbsignaling"

var _ sigClient = (*hpbesignaling.Client)(nil)
```

- [ ] **Step 5: Прогнать всё + коммит**

```sh
CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./...
git add internal/call/hpbsignaling/ internal/call/agent/hpb_compat_test.go
git commit -m "feat(call): hpbesignaling Send (любой Type) + keepalive-ping + sigClient-guard"
```

---

### Task 6: agent — обработка `EvOwnSession` (own из hello-response, асинхронно)

**Files:**
- Modify: `internal/call/agent/agent.go` (main-loop case + `setOwnSessionId`)
- Modify: `internal/call/agent/agent_test.go` (тест self-фильтра)

**Interfaces:**
- Consumes: `signaling.EvOwnSession` (Task 3), `agentState.ownSessionIdMu/ownSessionId/ownUserIdChecked` (существуют).
- Produces: `func (a *agentState) setOwnSessionId(sid string)`; ветка `case signaling.EvOwnSession` в main-loop `Run`.

- [ ] **Step 1: Падающий тест (в `agent_test.go`, рядом с reconcile-тестами)**

```go
// TestRun_OwnSessionFromEvent_SelfFiltered — external-режим: OwnSessionId НЕ
// задан статически (OCS-sessionId — другое id-пространство, дельта §2.3),
// own приходит EvOwnSession (WS hello-response) ВНУТРИ PollLoop. Self-фильтр
// reconcile обязан сработать по нему: пир создаётся только на «чужого».
func TestRun_OwnSessionFromEvent_SelfFiltered(t *testing.T) {
	fs := &fakeSignaling{
		pollLoop: pollSendThenBlock([]signaling.Event{
			{Kind: signaling.EvOwnSession, From: "hpb-own"},
			{Kind: signaling.EvUsersUpdated, Users: []signaling.User{
				{SessionId: "hpb-own", InCall: 3},  // сам себе — фильтруется
				{SessionId: "hpb-peer", InCall: 3}, // собеседник — пир
			}},
		}),
	}
	counters := newTestCounters()
	cfg := counters.buildConfig(fs, 1, io.Discard)
	cfg.OwnSessionId = "" // external: статического own НЕТ

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- Run(ctx, cfg) }()

	waitRecvTimeout(t, counters.peerCh, 500*time.Millisecond) // ровно один пир

	// Без self-фильтра (баг) было бы ДВА пира: hpb-own + hpb-peer.
	if n := atomic.LoadInt32(&counters.peerCreated); n != 1 {
		t.Fatalf("создано пиров %d, want 1 — EvOwnSession не установлен/не отфильтрован", n)
	}
	cancel()
	if err := <-runDone; err != nil {
		t.Fatalf("Run err = %v, want nil", err)
	}
}
```

- [ ] **Step 2: Запустить — упасть**

```sh
CGO_ENABLED=0 go test ./internal/call/agent/ -run TestRun_OwnSessionFromEvent -v
```
Ожидание: FAIL — создано 2 пира (EvOwnSession игнорируется, self не отфильтрован).

- [ ] **Step 3: Реализация**

В `Run` main-loop (agent.go, switch по `ev.Kind`, первая ветка перед `EvUsersUpdated`):

```go
			switch ev.Kind {
			case signaling.EvOwnSession:
				// external/HPB: own-sessionId из WS hello-response — приходит
				// асинхронно (внутри PollLoop), ДО первого EvUsersUpdated, и
				// МЕНЯЕТСЯ при каждом переподключении (дельта §2.3).
				a.setOwnSessionId(ev.From)
			case signaling.EvUsersUpdated:
				a.reconcile(ev.Users)
```

Метод в `agentState` (рядом с `resolveOwnSessionId`):

```go
// setOwnSessionId — own-sessionId, доставленный транспортом асинхронно
// (external/HPB: EvOwnSession из hello-response; эмитится при каждом
// (пере)подключении — sessionId между WS-сессиями меняется). Перетирает
// текущее значение и отключает userId-fallback: транспорт уже идентифицировал
// нас точно. В internal-режиме событие не приходит — статический
// cfg.OwnSessionId (OCS из JoinRoom) не затрагивается.
func (a *agentState) setOwnSessionId(sid string) {
	if sid == "" {
		return
	}
	a.ownSessionIdMu.Lock()
	prev := a.ownSessionId
	a.ownSessionId = sid
	a.ownUserIdChecked = true
	a.ownSessionIdMu.Unlock()
	if prev != sid {
		fmt.Fprintf(a.cfg.Stderr, "nctalk: ownSessionId получен из signaling (hello-response)\n")
	}
}
```

- [ ] **Step 4: Прогнать весь agent + коммит**

```sh
CGO_ENABLED=0 go test ./internal/call/agent/ -v
CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./...
git add internal/call/agent/agent.go internal/call/agent/agent_test.go
git commit -m "feat(call): agent — EvOwnSession: own-sessionId из hello-response (external), self-фильтр"
```

---

### Task 7: `cmd/nctalk-call` — выбор транспорта по signalingMode

**Files:**
- Modify: `cmd/nctalk-call/main.go` (шаги 4a/5/5a/8/8a/10: порядок capability→ветвление, weblogin только internal и ДО JoinRoom, hpbesignaling wiring, OwnSessionId только internal, iceTimeout парсится до ветвления)
- Modify: `cmd/nctalk-call/main_test.go` (+1 e2e-тест)

**Interfaces:**
- Consumes: `capability.Settings{ICEServers, SignalingMode, Server, Ticket, Userid}` (Task 2); `hpbesignaling.New(Config{Auth, Doer, Server, Ticket, Userid, RoomSessionId, ConnectBudget, Stderr})` (Task 4); `signaling.New/JoinRoom/SetSessionId`; `agent.Run(agent.Config{Signaling: ...})` — поле типа `sigClient` (обе реализации присваиваются).
- Produces: `run()` ветвится по режиму; хелпер `connectBudget(iceTimeout time.Duration) time.Duration`.

- [ ] **Step 1: Падающий e2e-тест (в `main_test.go`)**

```go
// TestRun_SettingsError_Exit1 — ошибка signaling-settings теперь ФАТАЛЬНА
// (дельта §3: без settings не выбрать транспорт; прежний best-effort
// воспроизводил «слепой» звонок на HPB). Позиционный token разрешается БЕЗ
// сети (room.ResolveRoom) — моку достаточно settings-ветки (ревью плана #10:
// rooms-ветка была мёртвой, реальный эндпоинт к тому же /api/v4/room).
func TestRun_SettingsError_Exit1(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "signaling/settings"):
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"ocs":{"meta":{"status":"failure","statuscode":500,"message":"boom"}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	setEnv(t, srv.URL)

	var out, errBuf bytes.Buffer
	code := run([]string{"tok-team-a"}, &out, &errBuf, strings.NewReader(""))
	if code != 1 {
		t.Fatalf("code = %d, want 1 (ошибка settings фатальна)", code)
	}
	if !strings.Contains(errBuf.String(), "signaling-settings") {
		t.Errorf("stderr = %q, want диагностика signaling-settings", errBuf.String())
	}
}
```

- [ ] **Step 2: Запустить — упасть**

```sh
CGO_ENABLED=0 go test ./cmd/nctalk-call/ -run TestRun_SettingsError -v
```
Ожидание: FAIL — но НЕ по коду (ревью плана #10): в текущем коде weblogin идёт РАНЬШЕ capability и падает о 404 мока → code уже 1. Red-фаза ловится только stderr-ассертом: диагностики «signaling-settings» нет.

- [ ] **Step 3: Реализация — перестроить шаги 4a→10 в `run`**

Заменить блоки 4a (weblogin), 5 (capability) и 8/8a (signaling+joinRoom) на:

```go
	// 4a. NCTALK_ICE_TIMEOUT парсим ДО ветвления транспорта: бюджет
	//     подключения hpbesignaling = iceTimeout/2 (дельта §3).
	iceTimeout, err := parseDurationEnv("NCTALK_ICE_TIMEOUT")
	if err != nil {
		fmt.Fprintln(stderr, "nctalk-call: "+err.Error())
		return 1
	}

	// 5. Capability: signaling-settings — режим транспорта + STUN/TURN.
	//    ОДИН запрос на старт; ошибка ФАТАЛЬНА (дельта §3): без settings
	//    транспорт не выбрать, а молчаливый fallback на internal воспроизводил
	//    бы исходный баг «слепого» звонка на HPB (§1). Практический риск мал —
	//    settings ходит на тот же сервер, что JoinRoom/JoinCall следом.
	st, err := capClient.Settings(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "nctalk-call: signaling-settings: "+err.Error())
		mapped := exit.FromClientErr(err)
		var cee exit.ExitError
		if errors.As(mapped, &cee) {
			return cee.Code
		}
		return 1
	}
```

(блок weblogin из старого шага 4a УДАЛЯЕТСЯ — переезжает в 5a ниже, ДО JoinRoom: порядок internal-пути «weblogin → JoinRoom» сохранён — ревью плана #6; `capClient` создаётся там же, где был.)

Далее — weblogin только для internal (ДО JoinRoom), затем JoinRoom обоим режимам:

```go
	// 5a. internal: web-login → PHP-session в shared jar (баг #5 базовой
	//     спеки: без session signaling pull → 404). Идёт ДО JoinRoom —
	//     порядок внутреннего пути сохранён «байт-в-байт», как обещает
	//     Global Constraints (ревью плана #6). external НЕ зовёт weblogin:
	//     PHP-session нужна только OCS-pull, который там не используется
	//     (дельта §2).
	if st.SignalingMode != "external" {
		if err := weblogin.Login(ctx, loginClient, auth); err != nil {
			fmt.Fprintln(stderr, "nctalk-call: "+err.Error())
			return 1
		}
	}

	// 6. Signaling-клиент + JoinRoom (canonical flow, дельта §2): participant-
	//    session нужна Call API (JoinCall) и серверному списку участников.
	//    ОБЕИМ режимам; разница — в использовании sessionId (ниже).
	ocsSig := signaling.New(auth, httpClient)
	sessionId, err := ocsSig.JoinRoom(ctx, result.Token)
	if err != nil {
		fmt.Fprintln(stderr, "nctalk-call: "+err.Error())
		mapped := exit.FromClientErr(err)
		var jee exit.ExitError
		if errors.As(mapped, &jee) {
			return jee.Code
		}
		return 1
	}

	agentCfg := agent.Config{
		Token:      result.Token,
		InFlags:    inFlags,
		ICEServers: st.ICEServers,
		Stdin:      pcmIn,
		Stdout:     pcmOut,
		Stderr:     stderr,
		OwnUserId:  cfg.Login,
		ICETimeout: iceTimeout,
	}

	if st.SignalingMode == "external" {
		// external (HPB, дельта §2/§3): weblogin НЕ нужен (PHP-session живёт
		// только в OCS-pull, который не используется); OCS-sessionId идёт
		// ТОЛЬКО в room-join WS (проверка прав в NC), в own-фильтр НЕ
		// подмешивается (другое id-пространство) — own придёт EvOwnSession.
		// WS-комнату клиент установит сам в JoinCall — ДО делегирования OCS
		// (canonical flow, ревью плана #4).
		agentCfg.Signaling = hpbesignaling.New(hpbsignaling.Config{
			Auth:          auth,
			Doer:          httpClient,
			Server:        st.Server,
			Ticket:        st.Ticket,
			Userid:        st.Userid,
			RoomSessionId: sessionId,
			ConnectBudget: connectBudget(iceTimeout),
			Stderr:        stderr,
		})
	} else {
		// internal: прежний путь (дельта §3): SetSessionId + own из JoinRoom.
		ocsSig.SetSessionId(sessionId) // исходящий POST signaling требует own sessionId
		agentCfg.Signaling = ocsSig
		agentCfg.OwnSessionId = sessionId // own-фильтр (приоритетный источник)
	}
```

Шаг 10 (`agent.Run`) — без изменений по сути, только источник конфига:

```go
	agentErr := agent.Run(sigCtx, agentCfg)
```

Хелпер в конец файла (рядом с `parseDurationEnv`):

```go
// connectBudget — бюджет первичного подключения hpbesignaling: половина
// ICE-таймаута (дельта §3: заведомо меньше, чтобы EvError успел ДО
// ICE-таймера). iceTimeout==0 → 0 → hpbesignaling подставит дефолт 15с.
func connectBudget(iceTimeout time.Duration) time.Duration {
	return iceTimeout / 2
}
```

Импорты main.go: добавить `hpbesignaling`; `signaling` уже есть. Удалить дублирующийся парс `NCTALK_ICE_TIMEOUT` из старого шага 8b.

- [ ] **Step 4: Прогнать e2e + коммит**

```sh
CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./...
git add cmd/nctalk-call/main.go cmd/nctalk-call/main_test.go
git commit -m "feat(call): nctalk-call — выбор транспорта по signalingMode (external → hpbesignaling)"
```

---

### Task 8: `interactive` + `cmd/nctalk-talk` — интерфейс Signaling и выбор транспорта

**Files:**
- Modify: `internal/call/interactive/interactive.go:27` (тип поля `Config.Signaling`)
- Modify: `cmd/nctalk-talk/main.go` (тот же wiring, что Task 7)
- Modify: `internal/call/interactive/interactive_test.go` (только если тесты зовут `Run` с `Signaling` — по факту не зовут, runner-инъекция)

**Interfaces:**
- Consumes: контракт 4 методов `agent.sigClient` (тип unexported — из другого пакета ИМЕНОВАТЬ НЕЛЬЗЯ, «cannot refer to unexported name»; в interactive объявляем локальный `SignalingClient` с теми же методами — ревью плана #1), `hpbesignaling.New`, `capability.Settings`.
- Produces: `interactive.Config.Signaling SignalingClient` (локальный интерфейс) — принимает `*signaling.Client` и `*hpbesignaling.Client` (структурное удовлетворение); значение присваиваемо полю `agent.Config.Signaling` (метод-сеты идентичны).

- [ ] **Step 1: Смена типа поля (компиляция — тест TDD здесь вырожден, тип-чек через guard Task 5)**

```go
// SignalingClient — транспорт signaling для interactive: ЛОКАЛЬНАЯ копия
// контракта agent.sigClient (4 метода). Копия обязательна: agent.sigClient
// unexported, а тип поля в ДРУГОМ пакете именовать селектором нельзя
// («cannot refer to unexported name agent.sigClient» — ревью плана #1).
// Идентичный метод-сет делает значение SignalingClient присваиваемым полю
// agent.Config.Signaling (interface-to-interface, структурно); обе реализации
// (*signaling.Client, *hpbesignaling.Client) удовлетворяют автовыженно
// (guard второй — agent/hpb_compat_test.go, Task 5).
type SignalingClient interface {
	JoinCall(ctx context.Context, token string, flags int) error
	LeaveCall(ctx context.Context, token string) error
	PollLoop(ctx context.Context, token string, ch chan<- signaling.Event) error
	Send(ctx context.Context, token string, msg signaling.Message) error
}

// Config для interactive.Run. Большинство полей пробрасывается в agent.Config.
type Config struct {
	Cfg   config.Config
	Token string
	// Signaling — транспорт signaling: internal (*signaling.Client) ИЛИ
	// external (*hpbesignaling.Client) — выбирает cmd/nctalk-talk по
	// signalingMode (дельта §3).
	Signaling    SignalingClient
	ICEServers   []webrtc.ICEServer
	OwnUserId    string
	OwnSessionId string
	ICETimeout   time.Duration
	DeviceIn     string // NCTALK_AUDIO_DEVICE_IN — avfoundation-строка (default ":0")
	DeviceOut    string // NCTALK_AUDIO_DEVICE_OUT — audiotoolbox int-idx или "default"
	LogFile      io.Writer
	View         View // nil → NewAnsiView(os.Stdin, os.Stdout)

	// internal: для тестов. nil → agent.Run.
	runner agentRunner
}

// Compile-time: *signaling.Client удовлетворяет локальному контракту
// (рассинхрон сигнатур роняет сборку interactive ещё до wiring'а в cmd).
var _ SignalingClient = (*signaling.Client)(nil)
```

Импорт `signaling` из interactive.go СОХРАНЯЕТСЯ (сигнатуры интерфейса используют `signaling.Event`/`signaling.Message`); проброс `Signaling: cfg.Signaling` в `agent.Config` внутри `Run` компилируется без правок — метод-сеты идентичны.

- [ ] **Step 2: `cmd/nctalk-talk/main.go` — зеркально Task 7**

Тот же паттерн, что Task 7, включая порядок (ревью плана #6): `iceTimeout` парсится до ветвления; `st, err := capClient.Settings(ctx)` — ошибка фатальна (тот же маппинг `exit.FromClientErr`); weblogin — только internal и ДО JoinRoom; `ocsSig := signaling.New(auth, httpClient)`; `sessionId, err := ocsSig.JoinRoom(ctx, result.Token)`; далее:

```go
	// internal: web-login ДО JoinRoom (порядок пути сохранён — ревью плана #6);
	// external не зовёт (PHP-session нужна только OCS-pull, дельта §2).
	if st.SignalingMode != "external" {
		if err := weblogin.Login(ctx, loginClient, auth); err != nil {
			fmt.Fprintln(stderr, "nctalk-talk: "+err.Error())
			return 1
		}
	}

	ocsSig := signaling.New(auth, httpClient)
	sessionId, err := ocsSig.JoinRoom(ctx, result.Token)
	if err != nil {
		fmt.Fprintln(stderr, "nctalk-talk: "+err.Error())
		mapped := exit.FromClientErr(err)
		var jee exit.ExitError
		if errors.As(mapped, &jee) {
			return jee.Code
		}
		return 1
	}

	iCfg := interactive.Config{
		Cfg:          cfg,
		Token:        result.Token,
		ICEServers:   st.ICEServers,
		OwnUserId:    cfg.Login,
		ICETimeout:   iceTimeout,
		DeviceIn:     deviceIn,
		DeviceOut:    deviceOut,
		LogFile:      logFile,
	}

	if st.SignalingMode == "external" {
		// external (HPB): weblogin пропущен выше, own — из EvOwnSession.
		// WS-комнату клиент установит сам в JoinCall (canonical flow, ревью #4).
		iCfg.Signaling = hpbesignaling.New(hpbsignaling.Config{
			Auth:          auth,
			Doer:          httpClient,
			Server:        st.Server,
			Ticket:        st.Ticket,
			Userid:        st.Userid,
			RoomSessionId: sessionId,
			ConnectBudget: connectBudget(iceTimeout),
			Stderr:        logFile, // диагностика reconnect — в call.log (TUI-терминал занят отрисовкой)
		})
	} else {
		// internal: own статически из JoinRoom.
		ocsSig.SetSessionId(sessionId)
		iCfg.Signaling = ocsSig
		iCfg.OwnSessionId = sessionId
	}

	err = interactive.Run(sigCtx, iCfg)
```

Присвоение `iCfg.Signaling = ...` из cmd легально при локальном типе поля: поле экспортировано, интерфейс удовлетворяется структурно — тип в cmd именовать не нужно.

Старые блоки weblogin/capability/SetSessionId до `interactive.Run` удаляются; `connectBudget` — копия хелпера из Task 7 в этом файле (cmd-пакеты не экспортируют друг другу); после блока — прежний маппинг ошибки `interactive.Run` без изменений.

- [ ] **Step 3: Сборка + тесты + коммит**

```sh
CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./...
git add internal/call/interactive/interactive.go cmd/nctalk-talk/main.go
git commit -m "feat(call): nctalk-talk/interactive — интерфейс Signaling, выбор транспорта по signalingMode"
```

---

### Task 9: Граница изоляции + финальная сборка + документация

**Files:**
- Modify: `internal/call/isolation_test.go`
- Modify: `CLAUDE.md` (секция «Инварианты звонков» — 3 строки)
- Modify: `docs/integration-run.md` (секция HPB-звонка — финализация после Task 1)

**Interfaces:**
- Consumes: готовые пакеты Tasks 1–8.
- Produces: guard на `coder/websocket` в изоляции; инварианты в CLAUDE.md.

- [ ] **Step 1: Расширить изоляционный guard**

В `TestCmdNctalkDoesNotDependOnPion` цикл проверки заменить на (имя теста сохранить — семантика прежняя + новая зависимость):

```go
	// pion ИЛИ coder/websocket (новая зависимость hpbesignaling, дельта §3:
	// импорт разрешён только из internal/call/hpbsignaling).
	for _, banned := range []string{"github.com/pion/webrtc/v4", "github.com/coder/websocket"} {
		for _, line := range strings.Split(out.String(), "\n") {
			if strings.HasPrefix(line, banned+" ") || line == banned {
				t.Fatalf("cmd/nctalk косвенно зависит от %s — нарушен инвариант изоляции (спека §3)", line)
			}
		}
	}
```

- [ ] **Step 2: CLAUDE.md — в «Инварианты звонков» добавить**

```markdown
- **HPB/external signaling (`internal/call/hpbsignaling`, дельта 2026-09-23):** транспорт выбирается в cmd по `signalingMode` из signaling-settings (capability.Settings — ЕДИНСТВЕННЫЙ стартовый запрос; ошибка фатальна). В external: weblogin пропускается, JoinRoom остаётся (его sessionId — ТОЛЬКО в WS room-join), own-sessionId — из `signaling.EvOwnSession` (WS hello-response, HPB-пространство), OCS-sessionId в own-фильтр НЕ подмешивается. Ticket — из settings-ответа (не `/backend`), не логируется. `Send` пропускает ЛЮБОЙ Type (unmute критичен). JoinCall устанавливает WS-комнату ДО делегирования OCS (canonical flow — иначе missed participants-update). Бюджет первичного подключения = NCTALK_ICE_TIMEOUT/2 (per-attempt deadline) → ошибка JoinCall/EvError{exit 1} ДО ICE-таймера; переподключение — бесконечный backoff, новый ticket. `coder/websocket` импортируется только из hpbesignaling (граница удаления `rm -rf internal/call …`).
```

- [ ] **Step 3: Полный прогон + проверка границы удаления (сухая проверка, НЕ выполнять rm)**

```sh
CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 go build -o /tmp/nctalk-check ./cmd/nctalk   # базовый бинарник собирается
```

Граница удаления (проверить рассуждением, не исполнением): `go list -deps ./internal/call/hpbsignaling | grep coder/websocket` — зависимость есть; `go list -deps ./cmd/nctalk | grep -c 'coder/websocket\|pion'` — 0.

- [ ] **Step 4: Коммит**

```sh
git add internal/call/isolation_test.go CLAUDE.md docs/integration-run.md
git commit -m "docs(call): инварианты HPB-signaling, guard coder/websocket в изоляции"
```

---

### Task 10: Integration на боевом HPB + финальный gate

**Files:**
- Modify: `cmd/nctalk-call/integration_test.go` (+HPB-сценарий) ИЛИ запуск по runbook из `docs/integration-run.md` (если e2e-spawn в тесте уже есть — расширить; иначе — ручной прогон по командам ниже, результат фиксируется в тикете/сообщении)

**Interfaces:**
- Consumes: собранный `nctalk-call` (Tasks 1–9), TEST-комната на боевом HPB-сервере.
- Produces: подтверждённый двусторонний signaling→ICE→audio на HPB; регресс internal не сломан.

- [ ] **Step 1: Integration-прогон signaling-слоя (гейт Task 1 уже зелёный — прогон повторно после финального кода)**

```sh
NCTALK_INTEGRATION_HPB=1 NCTALK_INTEGRATION_ROOM=<TEST-token> \
NEXTCLOUD_URL=<боевой> NEXTCLOUD_LOGIN=<login> NEXTCLOUD_PASS=<app-password> \
CGO_ENABLED=0 go test -tags integration ./internal/call/hpbsignaling/ -run TestHPBSpike -v
```

Ожидание: `ГЕЙТ ПРОЙДЕН` на финальном коде (валидация, что protocol.go не разошёлся со спайком).

- [ ] **Step 2: Полный агент-звонок на HPB (recvonly, без человека)**

```sh
CGO_ENABLED=0 go build -o nctalk-call ./cmd/nctalk-call
NEXTCLOUD_URL=<боевой> NEXTCLOUD_LOGIN=<login> NEXTCLOUD_PASS=<app-password> \
NCTALK_ICE_TIMEOUT=45s ./nctalk-call <TEST-token> --recvonly --out /tmp/hpb-rec.pcm ; echo "exit=$?"
```

Критерии (по stderr-диагностике): `joined` → участники видны (reconcile по EvUsersUpdated из HPB) → при наличии второго участника — `peer … ` создаётся, PCM пишется (`ls -la /tmp/hpb-rec.pcm` растёт); в одиночном звонке — чистый `exit=0` после ICE-таймаута («я один»), НЕ зависание и НЕ «слепой» exit 0 без событий. При недоступности HPB (проверить отдельно, подставив недоступный `settings.server` через /etc/hosts или остановив сеть) — `exit=1` с диагностикой бюджета подключения (НЕ «я один»).

- [ ] **Step 3: Финальный gate — живой звонок с человеком (по договорённости, минимально: одна сессия)**

Человек в TEST-комнате (браузер/десктоп Talk) + `./nctalk-talk <TEST-token>` с той же TEST-комнаты; человек заходит в звонок ПЕРВЫМ, агент стартует вторым — это целенаправленно проверяет сценарий «агент приходит в идущий звонок» (регресс ревью плана #4: participants-update с inCall-флагами должен быть принят ПОСЛЕ room-join). Критерий — собеседник слышит аудио агента и агент отдаёт PCM собеседника в динамик (TUI показывает участников и уровень). Спека §5.4: живые проверки с участием человека — по договорённости.

- [ ] **Step 4: Регресс internal-пути (Docker, без HPB)**

```sh
# существующий Docker-звонок (docs/integration-run.md) — без изменений и правок:
NCTALK_INTEGRATION_CALL=1 NCTALK_INTEGRATION_ROOM=<docker-token> \
CGO_ENABLED=0 go test -tags integration ./internal/call/signaling/ ./cmd/nctalk-call/ -v
CGO_ENABLED=0 go test ./...   # весь unit-набор
```

Ожидание: зелёные — polling-путь не менялся (дельта §1 out of scope: отказ от internal запрещён).

- [ ] **Step 5: Финальный коммит (если попадали правки по итогам живых прогонов)**

```sh
git add -A
git commit -m "test(call): HPB integration — signaling-гейт, recvonly-звонок, регресс internal"
```

---

## Самопроверка плана (выполнена при написании)

- **Покрытие спеки:** §2 протокол (Task 1 гейт + Task 3 адаптер); §3 выбор транспорта/capability/hpbsignaling-структура/бюджет/переподключение (Tasks 2, 4, 7, 8); §4 безопасность/диагностика (ticket-redact Task 3/4, NCTALK_DEBUG Task 1/4, reconnect-лог Task 4); §5 тестирование — юнит с мок-WS (Tasks 3–6), спайк первым шагом (Task 1), integration (Task 10), регресс internal (Task 10); §6 риски — выше. Out of scope (§1) в плане не реализуется.
- **Замечания ревью:** #1 композиция 4 методов (Tasks 4/5 + guard), #2 бюджет vs бесконечный backoff (Task 4: established-флаг), #3 фатальность settings (Task 7 тест), #4 декодирование mode/server (Task 2), #5 unmute не-whitelist (Tasks 3/5 тесты), #6 canonical flow с JoinRoom/weblogin (Tasks 1/7), #7 id-пространства (Tasks 3/6/7), #8 один go.mod (Task 1 go get), #9 лицензии/версия (header Tech Stack, v1.8.13).
- **Типовая консистентность:** `capability.Settings` поля — Task 2 создаёт, Tasks 7/8 потребляют; `hpbesignaling.Config` поля — Task 4 создаёт, Tasks 7/8 потребляют; `EvOwnSession` — Task 3 создаёт, Tasks 4 (эмит), 6 (консум); `newMessageFrame`/`roomState`/`errFrameError` — Task 3 → Task 4/5.
- **Плейсхолдеры:** отсутствуют; все шаги содержат код/команды с ожидаемым результатом.
- **Ревью плана** (`2026-09-23-nctalk-hpb-signaling-design.plan.review.md`, 11 замечаний — все 11 валидны, false-positive нет; каждое проверено против кода репо): #1 в interactive — локальный `SignalingClient` (unexported `agent.sigClient` типом поля именовать нельзя); #2 из protocol_test убран неиспользуемый `reflect`; #3 тест Task 2 переписан на фактические хелперы `capability_test` (loadFixture/newClient, внешний тестовый пакет; call-sites — 11, не 5); #4 `hpbesignaling.JoinCall` устанавливает WS-комнату ДО делегирования OCS (canonical flow §2; иначе missed participants-update → фильтр WITH_AUDIO даёт 0 пиров); #5 per-attempt `WithDeadline` бюджета — зависший dial обрывается (тест HungServer); #6 internal: weblogin возвращён ДО JoinRoom («байт-в-байт»); #7 спека §2.1 синхронизирована с планом (ticket из signaling-settings; `/backend` — внутренний HPB→NC с HMAC); #8 «welcome» → «hello-response» (Task 6 + спека §2.2/§2.3/§5.2); #9 опечатка «полувация» → «половина»; #10 из e2e-мока Task 7 убрана мёртвая rooms-ветка (позиционный token разрешается без сети; эндпоинт — `/api/v4/room`), red-фаза переописана (weblogin падает раньше settings); #11 финальные шаги всех тасков — полный `build ./... && vet ./... && test ./...`.




