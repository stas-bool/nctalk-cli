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

1. **Ticket.** Из signaling-settings через OCS с Basic-auth (weblogin НЕ нужен):
   `GET /ocs/v2.php/apps/spreed/api/v3/signaling/settings` в external-режиме
   дополнительно к stunservers/turnservers возвращает `signalingMode:"external"`,
   `server`, `ticket` и `helloAuthParams` (`"1.0": {userid, ticket}`) — источник
   подтверждён по SignalingController.php (`getSettings`); спайк дополнительно
   фиксирует фактические поля. Отдельного
   клиентского ticket-эндпоинта НЕТ: `/ocs/.../api/v1/signaling/backend` —
   внутренний HPB→NC-эндпоинт с HMAC-секретом, клиентом не вызывается. Ticket
   короткоживущий, выдаётся на звонок, в URL/логи не попадает (только в WS-кадре
   hello).
2. **WebSocket.** Соединение к `settings.server` (`wss://...`). Отдельный кадр
   `welcome` (server-info/features, БЕЗ sessionId) приходит сразу после коннекта
   и поглощается. Первое сообщение клиента — `hello` c `auth.type: ticket`;
   ответ `hello` несёт собственный sessionId (`hello.sessionid`).
3. **Room.** `room {roomid: <token>}` → подтверждение + `roomJoined` со списком
   участников. SessionId из hello-response — ДРУГОЕ id-пространство, чем OCS-sessionId из
   JoinRoom: в external-режиме именно он — OwnSessionId агента (self-фильтр
   `u.SessionId == ownSid` в reconcile), OCS-sessionId из JoinRoom в фильтр НЕ
   подмешивается (иначе фильтр никогда не совпадёт — агент наберёт самого себя).
   SessionId из hello-response асинхронен (приходит внутри PollLoop) → доставить его до первого
   EvUsersUpdated (Event/колбэк), а не статическим `agent.Config.OwnSessionId` из cmd.
   Требование к адаптеру: все sessionId в `Event.From` и `Event.Users` — из одного
   (HPB) пространства. Fallback `resolveOwnSessionId` по `UserId == login` спасёт,
   только если HPB-аналог usersInRoom несёт userId — предмет спайка, не полагаться.
4. **События сервера.** `event`-сообщения: `update` (participants/usersInRoom-аналог),
   `message` (recipient + data: те же offer/answer/candidate, что в OCS-протоколе),
   `leave`. Маппинг → существующие `signaling.Event` (см. §3).
5. **Исходящие.** `message {recipient, data}` — обёртка над `signaling.Message` с ЛЮБЫМ
   `Type` (НЕ whitelist): offer/answer/candidate, контрольный `unmute {name:"audio"}`
   (agent шлёт его на каждый OnConnected в sendrecv — без него Spreed держит audio-sink
   удалённого участника замьюченным, root cause spike-gate 2026-07-20) и будущие типы.
   Меняется только транспортировка (WS `message` вместо form-encoded POST).
6. **Keepalive.** `ping`/`pong` по таймеру (интервал из welcome/spike; ориентир ~30с).

OCS-шаги вокруг WS (canonical flow external целиком: settings(+ticket) → JoinRoom →
WS hello/room → JoinCall → события/исходящие → LeaveCall):

- **JoinRoom** (`POST /api/v4/room/{token}/participants/active`) — сохраняется:
  participant-session нужен Call API (JoinCall) и серверному списку участников
  (usersInRoom-аналог в `event update`). Его sessionId дальше НЕ используется (§2.3).
- **weblogin** — пропускается: PHP-session нужен только OCS-pull `pullMessages`,
  который в external не используется; ticket/JoinRoom/JoinCall идут с Basic-auth
  (OCS-эндпоинты, в отличие от pull, Basic-auth принимают).
- JoinCall/LeaveCall (`/api/v4/call/{token}`) — OCS, работают при любом signalingMode
  и остаются в `call/signaling` как есть.

Как и основной flow — ожидание, спайк уточнит.

## 3. Архитектура

### Выбор транспорта

`cmd/nctalk-call` / `cmd/nctalk-talk` после signaling-settings:

- `signalingMode == "internal"` (или поле отсутствует) → текущий `signaling.New(...)`
  (OCS-polling); сам polling-путь не меняется.
- `signalingMode == "external"` → новый `hpbesignaling`-клиент (§ ниже).
- Ошибка запроса settings → обычная ошибка звонка (exit-контракт базовой спеки §10).
  Осознанное изменение сегодняшнего best-effort (сейчас оба cmd лишь логируют ошибку
  и продолжают без STUN/TURN): без settings транспорт не выбрать, а молчаливый
  fallback на internal воспроизводил бы исходный баг на HPB-серверах (§1). Практический
  риск мал — settings ходит на тот же сервер, что и JoinRoom/JoinCall следом.

### Расширение `call/capability`

Сегодня capability из signaling-settings декодирует только stunservers/turnservers;
`signalingMode` и `server` (WS-URL из §2.2) отбрасываются. Capability расширяется:
декодировать оба поля и возвращать наружу вместе с ICE-серверами — тем же единственным
HTTP-запросом на старт (второго запроса settings не появляется; форма — новый метод или
расширение возвращаемой структуры, за планом). Имена полей подтверждены фикстурой
`testdata/signaling/capability.json`.

### Новый пакет `internal/call/hpbsignaling`

Одна ответственность: WebSocket-транспорт signaling для HPB. Интерфейс `agent.sigClient`
— ЧЕТЫРЕ метода: `JoinCall`/`LeaveCall` (Call API) + `PollLoop`/`Send` (транспорт).
hpbesignaling реализует все четыре: PollLoop/Send — WebSocket, LeaveCall —
делегирование OCS (вложенный клиент `call/signaling` либо собственные
`transport.DoOCS`-вызовы `/api/v4/call/{token}`), JoinCall — установление WS-комнаты
(hello/room с бюджетом) ДО делегирования OCS (canonical flow §2). Compile-time
проверка аналогично `agent.go:108`. Для TUI поле `interactive.Config.Signaling`
(сегодня конкретный тип `*signaling.Client`, `interactive.go:27`) становится
интерфейсом с теми же ЧЕТЫРЬМЯ методами (unexported `agent.sigClient` из другого
пакета типом поля именовать нельзя — «cannot refer to unexported name»; в interactive
объявляется локальный интерфейс, обе реализации удовлетворяют его структурно) —
правка и в `cmd/nctalk-talk`. Внутри:

- `TicketFetcher` (doer): OCS-запрос билета (переиспользует transport.DoOCS-конверт);
- WS-сессия: соединение, hello/room/message/ping, read-loop → `signaling.Event`,
  write-маппинг `signaling.Message` → `message`;
- Первичное подключение (ticket → WS → hello → room) — ОГРАНИЧЕННЫЙ бюджет: ретраи с
  backoff 1с→30с, но суммарный дедлайн заведомо меньше ICE-таймаута (ориентир —
  половина `NCTALK_ICE_TIMEOUT`; точное значение за планом). Исчерпание → фатал:
  `Event{Kind: EvError, Err: exit.Exit(1, …)}` в канал ДО срабатывания ICE-таймера —
  agent выходит по EvError существующим путём; иначе молчаливый ретрай даст exit 0
  «я один в звонке» — тот симптом, от которого дельта лечит (§1).
- Переподключение ПОСЛЕ установленного звонка: бесконечный backoff 1с→30с, как polling
  §7 «Polling retry/backoff» (там ретраи принципиально не исчерпываются), новый ticket
  на каждое переподключение; peers стоят, звонок жив.

`signaling.Event`/`signaling.Message` — общие типы обоих транспортов (живут в
`call/signaling`, HPB их только потребляет/производит; при расхождении форматов HPB —
адаптация внутри hpbsignaling, типы не расширяем до необходимости, подтверждённой спайком).

### Зависимость WebSocket

Stdlib WebSocket не содержит. Отдельного go-модуля звонков нет — в репо один `go.mod`
(базовая спека §3), уже зависящий от pion; новая зависимость добавится в него.
Кандидат `github.com/coder/websocket` (CGO-free, активно поддерживается, лицензия ISC);
альтернатива gorilla/websocket тоже активно поддерживается (ранее считалась
«обслужательским режимом» — оценка устарела; лицензия BSD-2-Clause). Финальный выбор —
за планом реализации; критерии: чистый Go, CGO_ENABLED=0, пермиссивная OSI-лицензия
(MIT/Apache/BSD/ISC). Импорт — только из `internal/call/hpbsignaling`: вне границы
удаления зависимость не появляется (`rm -rf internal/call … && go mod tidy` уберёт её,
как pion). Готовый высокоуровневый HPB-Go-клиент не найден (проверено при дизайне;
если план найдёт — пересмотреть с ревью).

### Граница изоляции

Инвариант базовой спеки сохраняется: `cmd/nctalk` и фундамент (transport/room/exit) от
WebRTC-стека не зависят. Граница удаления звонков не меняется. Новый пакет входит в
удаляемую зону.

## 4. Ошибки, безопасность, диагностика

- Креды — только в `Authorization` на OCS-запросе ticket. Ticket — в WS-фрейме hello,
  никогда в URL и логах. Все ошибки через `transport.SanitizeErr`.
- WS-обрыв: переподключение с backoff; диагностика в stderr «signaling: reconnect».
- HPB недоступен (TLS/conn refused): при первичном подключении — исчерпание бюджета
  подключения (§3) → exit 1 с диагностикой (сеть), не «я один в звонке» (важное отличие
  от сегодняшнего молчаливого симптома); в середине звонка — бесконечный backoff.
- `NCTALK_DEBUG=1` — протокольный дамп WS-сообщений (в stderr, без ticket/кредов).

## 5. Тестирование

1. **Юнит** (без сети): мок WS-сервер (httptest + upgrade) — сценарии hello/room/message/
   ping, маппинг событий, переподключение, исчерпание бюджета первичного подключения
   (EvError → exit 1), EOF/ошибки фреймов.
2. **Спайк на живом HPB** (первый шаг реализации, до основной логики): settings →
   ticket → hello → room по комнате TEST; зафиксировать фактические поля. Gate:
   hello-response (sessionid) + room-ack и свой entry в join-списке получены.
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
- Возрастание зависимостей: +1 чистая Go-библиотека в единственный go.mod (импорт —
  только из `internal/call/hpbsignaling`, граница удаления §3).
