# Ревью плана: 2026-09-23-nctalk-hpb-signaling-design.plan.md

Дата: 2026-09-23. Ревью по скиллу review-plan: три прохода — заземление в коде репо,
контракт writing-plans, покрытие спеки `docs/superpowers/specs/2026-09-23-nctalk-hpb-signaling-design.md`
(+ базовая `2026-07-19-nctalk-call-design.md`). Только замечания.

## 🔴 Блокер

### 1. Task 8 Step 1: `Signaling agent.sigClient` в package interactive не компилируется

1. Цитата (Task 8, Interfaces + Step 1): «Produces: `interactive.Config.Signaling agent.sigClient`» и код
   поля `Signaling agent.sigClient`; там же утверждение «`agent.sigClient` (unexported — ссылка из
   interactive легальна, агент уже импортируется)».
2. Что не так: непробитированное имя другого пакета нельзя именовать селектором — `sigClient`
   объявлен unexported в пакете agent (`internal/call/agent/agent.go:88`), а поле объявляется в
   `internal/call/interactive/interactive.go` (package interactive, строка 4; текущее поле —
   interactive.go:27). Компилятор: «cannot refer to unexported name agent.sigClient». Присвоение
   значению такого поля из cmd действительно легально (план прав в этой половине), но объявление
   типа поля — нет. Спека §3 предписывает ту же невозможную форму («становится интерфейсом
   `agent.sigClient`») — план обязан был разрешить это корректно.
3. Что сделать: объявить в interactive локальный интерфейс с теми же 4 методами
   (JoinCall/LeaveCall/PollLoop/Send) — обе реализации присваиваются полю автовыженно; либо
   экспортировать интерфейс из agent (`SigClient`) с компенсацией guard'ов. Заодно поправить фразу
   «ссылка из interactive легальна».

## 🟡 Стоит уточнить

### 2. Task 3 Step 1: в protocol_test.go неиспользуемый импорт `reflect`

1. Цитата: import-блок теста содержит `"reflect"`.
2. Что не так: `reflect` в приведённом коде файла нигде не используется → файл не соберётся,
   упадут ВСЕ тесты задачи (включая ожидаемый PASS на Step 5). Заметка в конце шага про
   `import "time"` есть, про удаление `reflect` — нет.
3. Что сделать: убрать `reflect` из импортов (и вписать `time` в сам блок, а не постскриптум).

### 3. Task 2 Step 2: тест опирается на несуществующие хелперы и неквалифицированный `New`

1. Цитата: `srv := newOCSServer(t, fixture(t, "capability_external.json"))`;
   `c := New(authFor(srv.URL), srv.Client())`; оговорка «Хелперы `newOCSServer`/`fixture`/`authFor` —
   существующие в capability_test.go».
2. Что не так: ни одного из трёх имён в файле нет: фактические хелперы — `fixturePath`
   (capability_test.go:32), `loadFixture` (:43), `newClient(baseURL, password)` (:65), `ocsOK` (:75).
   Кроме того, файл — внешний тестовый пакет `package capability_test` (строка 7), поэтому и
   голое `New(...)` не скомпилируется (нужно `capability.New` или `newClient`). Код шага не готов к
   вставке, несмотря на hedge «при других именах — по факту файла».
3. Что сделать: переписать тест под фактические хелперы (httptest.NewServer + loadFixture +
   newClient), как в существующих TestSettings_*.

### 4. Порядок «JoinCall до WS-room-join» отклоняется от канонического flow дельты §2

1. Цитата (дельта §2): «canonical flow external целиком: JoinRoom → ticket → WS hello/room →
   JoinCall → события»; план Task 7: WS-сессия стартует внутри PollLoop, который agent.Run
   запускает ПОСЛЕ JoinCall.
2. Что не так: в agent.Run JoinCall — шаг 1 (`internal/call/agent/agent.go:266`), PollLoop-горутина —
   шаг 4 (:331) → наш JoinCall случится до room-join. Participants-update с inCall-флагами,
   порождённый нашим JoinCall, уйдёт комнате, в которой нас ещё нет, а join-event флагов не несёт
   (план сам фиксирует: «join/leave — БЕЗ inCall-флагов»). Если HPB не рассылает update в ответ на
   room-join, при статичных соседях фильтр WITH_AUDIO никого не увидит → exit 0 «я один» — исходный
   симптом §1. Спайк Task 1 это не проверяет (не делает JoinCall вообще).
3. Что сделать: либо устанавливать WS-комнату ДО OCS-JoinCall (например, в
   hpbesignaling.JoinCall: сначала connect, потом делегирование), либо явно закрыть вопрос
   спайком/Task 10 (добавить проверку «participants-update получен после room-join при уже
   идущем звонке») и зафиксировать вывод в fixtures/README.

### 5. Бюджет первичного подключения не действует на зависшую попытку

1. Цитата (Task 4, PollLoop): `case !established && !time.Now().Before(deadline)` — дедлайн
   проверяется только ПОСЛЕ ошибки connect; dial/hello/room ждут внешнего ctx (HTTPClient для WS —
   без Timeout, сознательно).
2. Что не так: при SYN-blackhole (HPB-хост за молчаливым файрволом) dial висит минуты — дольше
   ICE-таймера; budget между попытками не срабатывает, EvError не эмитится, агент выходит по
   iceC с maxParticipants==0 → exit 0 «я один» — ровно то, от чего дельта лечит (§3: «EvError …
   ДО срабатывания ICE-таймера»). Тест Task 4 (TestPollLoop_ConnectBudgetExhausted) покрывает
   только мгновенный отказ (403).
3. Что сделать: per-attempt deadline — `context.WithDeadline(ctx, deadline)` на весь connect
   (dial + waitReply hello/room), плюс тест на «сервер принял TCP, но не отвечает»
   (мок: принять соединение и молчать).

### 6. Internal-ветка меняет порядок weblogin/JoinRoom вопреки «байт-в-байт»

1. Цитата (Global Constraints): «`"internal"` ИЛИ пусто → текущий polling-путь байт-в-байт»;
   Task 7 Step 3: JoinRoom (шаг 6) выполняется ДО weblogin, который перенесён внутрь internal-ветки.
2. Что не так: текущий код — weblogin (cmd/nctalk-call/main.go:170) → JoinRoom (:246); канон
   базовой спеки/CLAUDE.md: weblogin → joinRoom → pull → joinCall. Перестановка не закрыта тестом
   (существующие e2e в main_test.go обоих cmd заканчиваются на ResolveRoom) и ловится только
   Docker-регрессом в Task 10 Step 4. Если на каких-то деплоях JoinRoom требует PHP-session —
   тихий регресс internal-пути.
3. Что сделать: в internal-ветке сохранить порядок weblogin → JoinRoom (ветвление по режиму после
   settings; external просто не зовёт weblogin), либо явно оговорить перестановку в плане и
   добавить e2e-тест internal-пути, проходящий JoinRoom.

### 7. Расхождение плана со спекой §2.1: источник ticket

1. Цитата (спека §2.1): «запрос к signaling-backend-эндпоинту Spreed (см. спайк; кандидат —
   `/ocs/v2.php/apps/spreed/api/v1/signaling/backend`) → `data.ticket`»; план (протокол-канон §1):
   «Ticket — из signaling-settings, отдельного эндпоинта НЕТ», backend — «НЕ клиентский эндпоинт».
2. Что не так: план и спека дают разные каноны одного и того же шага. Заявление плана «все 9
   замечаний ревью уже внесены в спеку» тут ни при чём: ни одно из 9 замечаний
   (`...design.review.md`, ### 1–9) не касалось источника ticket — §2.1 остался со старым
   кандидатом. Спайк закроет вопрос по факту, но до него канон в документах расходится.
3. Что сделать: синхронизировать спеку §2.1 с планом (или пометить в плане, что он суперседит
   формулировку спеки до подтверждения спайком), чтобы исполнитель не выбирал между двумя канонами.

## 🟢 По красоте

### 8. Task 6: «welcome» вместо «hello-response» в диагностике

1. Цитата: `fmt.Fprintf(a.cfg.Stderr, "nctalk: ownSessionId получен из signaling (welcome)\n")`;
   заголовок задачи «own из welcome».
2. Что не так: по канону самого плана sessionId приходит в hello-RESPONSE, а `welcome` — отдельный
   кадр server-info без sessionId. Сообщение в логах собьёт с толку при отладке на живом HPB.
3. Что сделать: заменить «(welcome)» на «(hello-response)» в сообщении и заголовке.

### 9. Опечатка «полувация» (→ «половина»)

1. Цитата: «полувация `NCTALK_ICE_TIMEOUT`» — Risks п.4, doc.go Task 1 Step 2, комментарий
   `defaultConnectBudget` в client.go Task 4, хелпер connectBudget Task 7.
2. Что не так: опечатка в 4 местах, три из них попадут в код-комментарии.
3. Что сделать: исправить на «половина» во всех вхождениях.

### 10. Task 7 Step 1/2: мёртвая «rooms»-ветка хендлера и неточное ожидание red-фазы

1. Цитата: `case strings.Contains(r.URL.Path, "rooms"): ... oneRoomJSON`; Step 2: «Ожидание: FAIL —
   code 0/другой (сейчас settings best-effort; дальше weblogin падает по-другому)».
2. Что не так: (а) positional-token разрешается БЕЗ сети (`internal/room/room.go:90-92`) — «rooms»-
   ветка и `oneRoomJSON` не нужны (реальный путь эндпоинта вдобавок `/api/v4/room` без «s»);
   (б) в текущем коде weblogin идёт раньше capability и падает о 404 мока → code УЖЕ 1, red-фаза
   гарантируется только ассершном stderr на «signaling-settings», а не «code 0/другой».
3. Что сделать: убрать «rooms»-ветку и `oneRoomJSON`; уточнить ожидание Step 2 («FAIL по
   stderr-ассершну: код 1 от weblogin, без „signaling-settings“»).

### 11. Частичные прогоны в Task 2/Task 6 против собственного Global Constraints

1. Цитата (Global Constraints): «Каждый таск завершать: `CGO_ENABLED=0 go build ./... && … vet
   ./... && … test ./...` — зелёные».
2. Что не так: Task 2 Step 6 гоняет только `./internal/call/capability/ ./cmd/...`, Task 6 Step 4 —
   только `./internal/call/agent/`.
3. Что сделать: в обоих шагах добавить полный `build ./... && vet ./... && test ./...`.
