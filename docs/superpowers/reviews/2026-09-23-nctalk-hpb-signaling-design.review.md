# Code-review: реализация HPB signaling (a3a156b..HEAD)

Ревьюер: opencode/glm-5.3 (named-сессия s2c-code), 2026-09-24. Диапазон: `git diff a3a156b..ef7b748` — internal/call/hpbsignaling, capability, agent, cmd/nctalk-call, cmd/nctalk-talk, internal/call/interactive.

`go vet` и все тесты зелёные. `-race` на этой машине невозможен (dyld/LC_UUID при CGO=1) — гонки разобраны чтением.

## Вердикт

Архитектура добротная: isolation-guard расширен на `coder/websocket`, canonical flow (WS-комната до OCS JoinCall) реализован и покрыт тестом, exit-контракт соблюдён, ticket редактируется. Найден 1 реальный баг паритета, 2 Medium корректности, несколько minor.

## Баги корректности

### 1. [Bug] `nctalk-talk`: флаги после `<room>` молча игнорируются — паритет с ef7b748 нарушен
`cmd/nctalk-talk/main.go:49` использует голый `fs.Parse`, тогда как `nctalk-call` в этом же диапазоне получил `parseArgs` (`cmd/nctalk-call/main.go:354`) именно из-за синтаксиса «флаги после позиционного». `nctalk-talk myroom --debug` молча теряет `--debug`. Тот же класс бага, что чинился коммитом ef7b748, — фикс не тиражирован на соседний бинарник. Переиспользовать `parseArgs`.

### 2. [Medium] TOCTOU в `ensureConnected`/`PollLoop` — двойное подключение с утечкой WS и битым own-session
`internal/call/hpbsignaling/client.go:168-187` и `client.go:206-246`: оба пути проверяют `conn == nil` под мьютексом, но сам `connect` идёт без мьютекса. При конкурентном вызове оба подключатся: второй `setConn` перезапишет первый, чей коннект никогда не закроется (утечка + ghost-сессия в комнате сервера), а его pending (включая `EvOwnSession` мёртвой сессии) может доехать до агента. Сегодня недостижимо (`agent.Run` зовёт JoinCall строго до горутины PollLoop), но контракт `sigClient` этого не гарантирует. Лечится флагом `connecting` под `c.mu` (второй вызов ждёт/использует результат первого).

### 3. [Medium] `--out`-файл трункируется до сетевых шагов, которые могут упасть
`cmd/nctalk-call/main.go:250-259`: `os.Create` (truncate!) на шаге 7, а `JoinRoom` — на шаге 8a. Упавший JoinRoom стирает существующий файл записи ещё до входа в звонок. Открытие `--in`/`--out` перенести после JoinRoom.

### 4. [Medium] Пустой `userid` в `helloAuthParams["1.0"]` перетирает корневой `userId` → вечный invalid_ticket-цикл
`internal/call/hpbsignaling/client.go:485-488` и зеркально `internal/call/capability/capability.go:90-94`: приоритет v1-блока безусловен — если сервер шлёт `helloAuthParams["1.0"].ticket` с пустым `userid`, а юзер — в корневом `userId`, уйдёт v1-hello с пустым userid → `invalid_ticket` → reconnect refetch → снова пустой userid → бесконечный backoff без фатала (после `established` бюджета нет) — звонок висит молча. Лечится `firstNonEmpty(v.Userid, data.UserId)`.

## Minor / hardening

5. `websocket.Dial` без redirect-политики (`client.go:318`): `http.Client` следует редиректам на любой хост, включая wss→ws downgrade — противоречит инварианту `SameHostRedirectPolicy` остального транспорта. Кредов в URL нет, severity низкий, но консистентность стоит восстановить.
6. Async error-кадры с непустым ID в readLoop теряются молча (`applyFrame` → default → nil). Хоть бы в debug.
7. `sortBySessionId` — O(n²) insertion sort (`protocol.go:460`): n мал, но `sort.Slice` короче.
8. Stale `c.pending` при обрыве до старта PollLoop не сбрасывается при reconnect — сбрасывать в `setConn(nil)` вместе со `state`.
9. `pathCallFmt` (`client.go:57`) — мёртвый дубликат «для симметрии». Удалить.
10. `normalizeWSURL` — `strings.Replace(u, "https://", "wss://", 1)` матчит подстроку в любом месте, а не схему. На реальных settings.server безопасно; параноик парсил бы через `url.Parse`.

## Проверено и чисто

- Гонки WS-клиента: `conn`/`state`/`pending` — всегда под `c.mu`; read-сторона — единственный владелец; `Send`+ping+Close конкурентно — в контракте coder/websocket; ping-горутина корректно джойнится.
- Порядок EvOwnSession до первого EvUsersUpdated гарантирован протокольно; `setOwnSessionId` корректно отключает userId-fallback и переживает reconnect.
- id-пространства: OCS-sessionId — только в room-join WS, в own-фильтр не подмешивается (дельта §2.3).
- Бюджет vs ICE-таймер: JoinCall с per-attempt `WithDeadline` блокирует до ICE-таймера; `no_such_room`→2, бюджет→1, budget=ICE/2 соблюдён.
- Exit-контракт: `ExitError` value-type везде, 0/1/2/3 в обоих cmd корректен.
- Безопасность: ticket/token редактируются в debug-дампе (`redactTicket`), креды только в Authorization, ошибки через `SanitizeErr`. Утечек не найдено.
- Isolation: guard `TestCmdNctalkDoesNotDependOnPion` расширен на `coder/websocket` + compile-time `sigClient`-guards.

Приоритет фиксов: #1 (паритет talk/call), #4 (firstNonEmpty), #3 (порядок открытия файлов), #2 (guard подключения).
