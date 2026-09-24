# Ревью спеки: 2026-09-23-nctalk-hpb-signaling-design.md

Дата: 2026-09-23. Два прохода по skill review-spec: документ сам по себе + заземление
утверждений о существующей системе в коде репо (`main` @ `c2c0621`). Прочитано вместе с
базовой спекой `2026-07-19-nctalk-call-design.md`, на которую ссылается дельта.

## 🔴 Блокеры

### 1. `agent.sigClient` — четыре метода, а не два; подмена транспорта как написано не собирается

1. Цитата (§3): «Реализует интерфейс `agent.sigClient` (`PollLoop(ctx, token, ch chan<- signaling.Event)` + `Send(ctx, token, signaling.Message)`) — тот же контракт, что у polling-клиента»; и §2: «JoinCall/LeaveCall (`/api/v4/call/{token}`) — OCS … остаются в `call/signaling` как есть».
2. Что не так: интерфейс `agent.sigClient` требует `JoinCall`, `LeaveCall`, `PollLoop`, `Send` (`internal/call/agent/agent.go:88-93`), и `agent.Run` вызывает их все: `JoinCall` — `agent.go:266`, `LeaveCall` (через `leaveBestEffort`) — `agent.go:472` и `agent.go:1252-1256`. Если JoinCall/LeaveCall остаются на polling-клиенте, а hpbesignaling реализует только PollLoop/Send — hpbesignaling **не** удовлетворяет `agent.sigClient` и не может быть подставлен в `agent.Config.Signaling` (compile-time guard `agent.go:108` здесь не поможет — он проверяет polling-клиента). Дополнительно для `nctalk-talk`: `interactive.Config.Signaling` — конкретный тип `*signaling.Client`, не интерфейс (`internal/call/interactive/interactive.go:27`), т.е. подмена транспорта в TUI-режиме требует смены типа поля — в спеке эта правка не упомянута.
3. Что сделать: зафиксировать композицию. Варианты: (а) разделить `sigClient` на два интерфейса — call-API (`JoinCall`/`LeaveCall`) и signaling-транспорт (`PollLoop`/`Send`) — и передавать в agent обоих; (б) hpbesignaling реализует все 4 метода, делегируя JoinCall/LeaveCall OCS-клиенту; (в) композитный sigClient, собираемый в cmd. Любой вариант + явная правка типа `interactive.Config.Signaling` (и `cmd/nctalk-talk/main.go:132,172`).

### 2. Противоречие по ретраям: бесконечный backoff «как polling» против «исчерпания ретраев»; обещанный exit 1 недостижим без дополнительных механизмов

1. Цитата §3: «Переподключение: backoff 1с→30с (как polling §7 «Polling retry/backoff»), новый ticket на каждое переподключение»; и §4: «HPB недоступен (TLS/conn refused) после исчерпания ретраев → exit 1 с диагностикой (сеть), не «я один в звонке» (важное отличие от сегодняшнего молчаливого симптома)».
2. Что не так: у polling-пути ретраи **бесконечны** — `PollLoop` выходит только по ctx.Done / 401 / 403 / 404, на transient — backoff без счётчика попыток (`internal/call/signaling/signaling.go:157-201`; base 1с / потолок 30с — `signaling.go:75-82`). «Как polling» ⇒ ретраи не исчерпываются никогда ⇒ «исчерпание ретраев» не определено (нет ни числа попыток, ни дедлайна). Следствие в существующем коде: пока WS-клиент молча ретраится, agent не получает событий, и через `defaultIceTimeout = 30s` (`agent.go:131-133`) при `iceMaxParticipants() == 0` срабатывает ветка exit 0 «я один в звонке» (`agent.go:432-438`) — ровно тот молчаливый симптом, от которого дельта обещает избавиться (§1, §4).
3. Что сделать: определить бюджет первичного подключения (число попыток ИЛИ дедлайн, заведомо меньше `NCTALK_ICE_TIMEOUT`) и семантику отказа: фатальная ошибка WS → `Event{Kind: EvError, Err: exit.Exit(1, …)}` в канал ДО срабатывания ICE-таймера (agent уже умеет: `EvError` → `pollErr` → exit, `agent.go:410-412`). Отдельно указать, различаются ли правила для первого подключения и переподключений в середине звонка.

## 🟡 Стоит уточнить

### 3. «Ошибка запроса settings → ошибка звонка» молча меняет сегодняшнее best-effort-поведение

1. Цитата §3: «Ошибка запроса settings → обычная ошибка звонка (exit-контракт базовой спеки §10)» — при том же §3: «`signalingMode == "internal"` … Поведение байт-в-байт как сегодня».
2. Что не так: сегодня `capability.Settings` — best-effort: при ошибке лог в stderr и продолжение без STUN/TURN (`cmd/nctalk-call/main.go:178-183`, `cmd/nctalk-talk/main.go:125-130`). Спека делает settings фатальными (без них не выбрать транспорт) — но это распространяется и на internal-серверы с временно недоступным settings: звонок, который сегодня работает на host-candidates, начнёт падать с exit 1. Это противоречит «байт-в-байт как сегодня» для internal-ветки.
3. Что сделать: выбрать и зафиксировать одно из: (а) ошибка settings всегда фатальна — задокументировать смену поведения; (б) fallback «settings недоступен → считаем internal» с диагностикой в stderr. Во втором случае уточнить, как диагностируется отказ именно HPB.

### 4. Не определено, откуда берутся `signalingMode` и `server`: capability их сейчас отбрасывает

1. Цитата §1: «клиент сам выбирает транспорт по `signalingMode` из signaling-settings (запрос уже выполняется на старте — `call/capability`)».
2. Что не так: запрос обоими бинарниками действительно выполняется, но `capability.Client.Settings` возвращает только `[]webrtc.ICEServer`; поля `signalingMode`/`server` сознательно не декодируются (`internal/call/capability/capability.go:67-72` — «прочие поля — signalingMode / userId / server / … — игнорируются»; `capability.go:121-130`). Для выбора транспорта и WS-URL (`settings.server`, §2.2) нужна доработка capability (новый метод или расширение возвращаемой структуры), которой в спеке нет. Имена полей подтверждены фикстурой `testdata/signaling/capability.json:14,17` (`"signalingMode"`, `"server"`).
3. Что сделать: зафиксировать расширение capability: кто декодирует `signalingMode` + `server` и возвращает их наружу, при одном HTTP-запросе на старт (не второй запрос поверх существующего).

### 5. Исходящие — не только offer/answer/candidate: agent шлёт через тот же `Send` критичный `unmute`

1. Цитата §2.5: «`message {recipient, data}` — обёртка над теми же `signaling.Message` (offer/answer/candidate), которые сегодня шлёт `Send`».
2. Что не так: существующий `Send`-путь несёт ещё и `Message{Type: "unmute", Payload: {"name":"audio"}}` — agent шлёт его на каждый `OnConnected` в sendrecv-режиме (`internal/call/agent/agent.go:930-949`). Это критично: без unmute Spreed держит audio-sink удалённого участника замьюченным — root cause spike-gate 2026-07-20, задокументирован в комментарии `agent.go:920-929`. Если реализатор возьмёт перечень «offer/answer/candidate» как whitelist типов, звук пропадёт на живом HPB, а unit-тесты на моке этого не заметят.
3. Что сделать: явно написать: hpbesignaling.`Send` пропускает `signaling.Message` с **любым** `Type` (включая `unmute` и будущие control-сообщения), меняется только транспортировка (WS `message` вместо form-encoded POST).

### 6. Канонический flow external-режима не говорит про JoinRoom и weblogin

1. Цитата §2: flow из шести шагов (ticket → WS → room → события → исходящие → keepalive); weblogin упомянут один раз — «Backend-ticket через OCS с Basic-auth (weblogin НЕ нужен)». JoinRoom (`POST /api/v4/room/{token}/participants/active`) не упомянут вовсе.
2. Что не так: сегодняшний canonical flow — web-login → JoinRoom → pull → JoinCall (`internal/call/signaling/signaling.go:123-139`); оба cmd делают weblogin безусловно (`cmd/nctalk-call/main.go:170-173`, `cmd/nctalk-talk/main.go:120-123`) и JoinRoom (`cmd/nctalk-call/main.go:246-256`). Для external-режима из спеки не следует: остаётся ли JoinRoom обязательным (Call API и серверный usersInRoom опираются на participant-session) и что с weblogin (нужен ли он чему-то, кроме OCS-pull, который в external не используется).
3. Что сделать: выписать в §2 канонический flow external-режима целиком — какие OCS-шаги остаются до/после WS (JoinRoom? JoinCall и LeaveCall указаны) — и решение по weblogin (скорее всего пропускается — зафиксировать), с пометкой «спайк уточнит».

### 7. Собственный sessionId: смешение id-пространств OCS и HPB не разобрано

1. Цитата §2.3: «SessionId из welcome ставится в peer-слой (аналог `SetSessionId` OCS-пути)».
2. Что не так: сегодня `agent.Config.OwnSessionId` задаётся статически из OCS-ответа JoinRoom до старта agent (`cmd/nctalk-call/main.go:284`), и peer-события приходят в том же id-пространстве (OCS-сессии). В external-режиме welcome-sessionId (HPB) появится только асинхронно, внутри PollLoop, и это **другое** id-пространство. Если cmd продолжит передавать OCS-sessionId в `OwnSessionId`, self-фильтр в `reconcile` (`agent.go:754-767`, `u.SessionId == ownSid`) никогда не совпадёт → агент попытается набрать самого себя (класс бага «review замечание 3», `cmd/nctalk-call/main.go:233-238`). Ленивый fallback `resolveOwnSessionId` по `UserId == login` (`agent.go:1000-1021`) спасёт, только если HPB-аналог usersInRoom несёт userId — что само по себе предмет спайка.
3. Что сделать: описать механизм: в external-режиме OwnSessionId приходит из welcome (как Event/колбэк до первого EvUsersUpdated), OCS-sessionId из JoinRoom в self-фильтр не подмешивается; зафиксировать требование к адаптеру — все sessionId в `Event.From` и `Users` из одного (HPB) пространства.

## 🟢 По красоте

### 8. «Модуль звонков» как получатель зависимости — в репо один go.mod

1. Цитата §3: «Одна новая зависимость в go.mod (модуль звонков уже зависит от pion)».
2. Что не так: отдельного модуля звонков нет — один `go.mod` на весь репозиторий (`go.mod`: `module github.com/stas-bool/nctalk-cli`; базовая спека §3: «Один `go.mod` (не отдельные go-модули)»). Зависимость добавится в общий go.mod; изоляция держится только импортом из `internal/call/hpbsignaling` (граница удаления при этом работает: `rm -rf internal/call … && go mod tidy` уберёт её, как у pion).
3. Что сделать: переформулировать: «в единственный go.mod репозитория; импортируется только из `internal/call/hpbsignaling`, вне границы удаления не появляется».

### 9. Оценка gorilla/websocket устарела; лицензионный критерий сужает список кандидатов

1. Цитата §3: «альтернатива gorilla/websocket — в обслуживательском режиме … критерии: чистый Go, CGO_ENABLED=0, MIT/Apache».
2. Что не так: Gorilla Toolkit разморожен и активно поддерживается с 2023 — посылка сравнения устарела (финальный выбор всё равно отдан плану, поэтому 🟢). Лицензия gorilla/websocket — BSD-2-Clause: критерий «MIT/Apache» отсекает её априори, независимо от статуса поддержки; лицензию выбранного кандидата `coder/websocket` тоже стоит сверить с критерием до его фиксации.
3. Что сделать: на этапе плана перепроверить статус и лицензии кандидатов и либо расширить критерий до «permissive OSI-лицензия», либо оставить единственного кандидата без сравнения.
