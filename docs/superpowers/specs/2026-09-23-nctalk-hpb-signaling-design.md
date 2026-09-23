# Дизайн-дельта: HPB signaling — внешний signaling-сервер (nextcloud-spreed-signaling) для `nctalk-call` / `nctalk-talk`

Дата: 2026-09-23. Дельта к базовой спеке звонков `2026-07-19-nctalk-call-design.md`
(§7 «Общее ядро: signaling и peer»); наследует её контракты (exit-коды, безопасность,
WebRTC/peer/media-слои). Читается вместе с ней.

## 1. Цель и контекст

Агент-звонки (`nctalk-call`, `nctalk-talk`) реализуют **internal signaling** — OCS-polling
`/api/v3/signaling/{token}` (спека 2026-07-19 §7). На серверах с настроенным
High Performance Backend (HPB, [nextcloud-spreed-signaling](https://github.com/strukturag/nextcloud-spreed-signaling))
`/ocs/.../api/v3/signaling/settings` отдаёт `signalingMode: "external"`, OCS-polling не
раздаёт участников и сообщения — клиент «слеп» в звонке: 30с ICE-таймаута, exit 0
«я один в звонке», аудио не доставляется. Найдено на боевом сервере 2026-09-23
(home.softmus.ru, HPB `signal.softmus.ru`, Talk 20+; участники/чаты через OCS при этом
работают). Docker-спайк базовой спеки этого не ловил — там HPB нет.

Цель: звонки работают на **обоих** типах серверов без изменения UX: клиент сам выбирает
транспорт по `signalingMode` из signaling-settings (запрос уже выполняется на старте —
`call/capability`).

### Out of scope

- Видео,/screen-sharing — только аудио (наследуем базовую спеку).
- SMP-масштабирование HPB (конференции десятки+ участников) — mesh-модель базовой спеки
  сохраняется; HPB для нас только signaling-транспорт, не MCU/SFU.
- Отказ от internal-режима: polling-путь остаётся как есть (Docker, self-hosted без HPB).
- Федерация (`federation` в settings) — игнорируем.

## 2. Протокол HPB (канон для реализации)

Источники истины: исходники `strukturag/nextcloud-spreed-signaling` (server.go,
protocol.go) и клиент `spreed/src/services/signaling.js`. **Точные поля сообщений
подтверждаются спайком на живом HPB до написания основной логики** (правило базовой спеки:
не compile-only — спайк-гейт §12 там вскрыл 5 несоответствий; здесь ожидаем то же).

Канонический flow (ожидание, спайк уточнит):

1. **Ticket.** Backend-ticket через OCS с Basic-auth (weblogin НЕ нужен):
   запрос к signaling-backend-эндпоинту Spreed (см. спайк; кандидат —
   `/ocs/v2.php/apps/spreed/api/v1/signaling/backend`) → `data.ticket`. Ticket
   короткоживущий, выдаётся на звонок, в URL/логи не попадает.
2. **WebSocket.** Соединение к `settings.server` (`wss://...`). Первое сообщение —
   `hello` c `auth.type: ticket`; ответ `welcome` несёт собственный sessionId.
3. **Room.** `room {roomid: <token>}` → подтверждение + `roomJoined` со списком
   участников. SessionId из welcome ставится в peer-слой (аналог `SetSessionId`
   OCS-пути).
4. **События сервера.** `event`-сообщения: `update` (participants/usersInRoom-аналог),
   `message` (recipient + data: те же offer/answer/candidate, что в OCS-протоколе),
   `leave`. Маппинг → существующие `signaling.Event` (см. §3).
5. **Исходящие.** `message {recipient, data}` — обёртка над теми же
   `signaling.Message` (offer/answer/candidate), которые сегодня шлёт `Send`.
6. **Keepalive.** `ping`/`pong` по таймеру (интервал из welcome/spike; ориентир ~30с).

JoinCall/LeaveCall (`/api/v4/call/{token}`) — OCS, работают при любом signalingMode и
остаются в `call/signaling` как есть.

## 3. Архитектура

### Выбор транспорта

`cmd/nctalk-call` / `cmd/nctalk-talk` после signaling-settings:

- `signalingMode == "internal"` (или поле отсутствует) → текущий `signaling.New(...)`
  (OCS-polling). Поведение байт-в-байт как сегодня.
- `signalingMode == "external"` → новый `hpbesignaling`-клиент (§ ниже).
- Ошибка запроса settings → обычная ошибка звонка (exit-контракт базовой спеки §10).

### Новый пакет `internal/call/hpbsignaling`

Одна ответственность: WebSocket-транспорт signaling для HPB. Реализует интерфейс
`agent.sigClient` (`PollLoop(ctx, token, ch chan<- signaling.Event)` + `Send(ctx, token,
signaling.Message)`) — тот же контракт, что у polling-клиента (compile-time проверка
аналогично `agent.go:108`). Внутри:

- `TicketFetcher` (doer): OCS-запрос билета (переиспользует transport.DoOCS-конверт);
- WS-сессия: соединение, hello/room/message/ping, read-loop → `signaling.Event`,
  write-маппинг `signaling.Message` → `message`;
- Переподключение: backoff 1с→30с (как polling §7 «Polling retry/backoff»), новый ticket
  на каждое переподключение.

`signaling.Event`/`signaling.Message` — общие типы обоих транспортов (живут в
`call/signaling`, HPB их только потребляет/производит; при расхождении форматов HPB —
адаптация внутри hpbsignaling, типы не расширяем до необходимости, подтверждённой спайком).

### Зависимость WebSocket

Stdlib WebSocket не содержит. Одна новая зависимость в go.mod (модуль звонков уже зависит
от pion): `github.com/coder/websocket` (CGO-free, активно поддерживается; альтернатива
gorilla/websocket — в обслуживательском режиме). Финальный выбор — за планом реализации;
критерии: чистый Go, CGO_ENABLED=0, MIT/Apache. Готовый высокоуровневый HPB-Go-клиент
не найден (проверено при дизайне; если план найдёт — пересмотреть с ревью).

### Граница изоляции

Инвариант базовой спеки сохраняется: `cmd/nctalk` и фундамент (transport/room/exit) от
WebRTC-стека не зависят. Граница удаления звонков не меняется. Новый пакет входит в
удаляемую зону.

## 4. Ошибки, безопасность, диагностика

- Креды — только в `Authorization` на OCS-запросе ticket. Ticket — в WS-фрейме hello,
  никогда в URL и логах. Все ошибки через `transport.SanitizeErr`.
- WS-обрыв: переподключение с backoff; диагностика в stderr «signaling: reconnect».
- HPB недоступен (TLS/conn refused) после исчерпания ретраев → exit 1 с диагностикой
  (сеть), не «я один в звонке» (важное отличие от сегодняшнего молчаливого симптома).
- `NCTALK_DEBUG=1` — протокольный дамп WS-сообщений (в stderr, без ticket/кредов).

## 5. Тестирование

1. **Юнит** (без сети): мок WS-сервер (httptest + upgrade) — сценарии hello/room/message/
   ping, маппинг событий, переподключение, EOF/ошибки фреймов.
2. **Спайк на живом HPB** (первый шаг реализации, до основной логики): ticket → hello →
   room по комнате TEST; зафиксировать фактические поля. Gate: welcome+roomJoined получены.
3. **Integration** (build-tag `integration`, env как у звонков + `NCTALK_INTEGRATION_HPB=1`):
   полный агент-звонок на боевом, двустороннее аудио.
4. **Финальный gate**: живой звонок с человеком в TEST (критерий — собеседник слышит
   аудио). Живые проверки с участием человека — по договорённости, минимальное число
   сессий (спайк + финал).
5. Регресс internal-пути: существующие тесты `call/signaling` + integration Docker-звонок
   не меняются.

## 6. Риски

- **Протокольный дрейф HPB** (главный): закрывается спайком №5.2 и живым integration.
- Расхождение форматов событий HPB vs OCS (usersInRoom-аналог) — изолировано внутри
  hpbesignaling адаптером.
- STUN/TURN уже получаем из signaling-settings — при external режиме тот же источник
  (проверено на боевом: списки приходят), отдельной работы нет.
- Возрастание зависимостей: +1 чистая Go-библиотека в звонковый модуль.
