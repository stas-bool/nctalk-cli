# Review: 2026-09-09-nctalk-rooms-participants-design.plan.md

Ревью по скиллу `review-plan`: три прохода — заземление в коде, контракт
`superpowers:writing-plans`, покрытие спеки `2026-09-09-nctalk-rooms-participants-design.md`.
Только замечания; план не правился.

## 🟡 Стоит уточнить

### 1. Task 3, Step 2 — «Expected» красной фазы не соответствует фактическому поведению (3 из 4 утверждений неверны, реальный исход — паника)

- **Цитата:** «Expected: FAIL — `cmdSpecs len: got 9, want 10` (Order), неизвестный путь в
  FlagsExactly, spy не вызвался в Run-канонах (роутинга нет)».
- **Что не так** (пруфы по коду):
  - Числа длины неверны: в `cmdSpecs` сейчас **8** записей (`internal/cli/help.go:44–150`),
    want-список после правки — 9. Реальное сообщение:
    `cmdSpecs len: got 8, want 9` (`internal/cli/help_test.go:45–47`), не «got 9, want 10».
  - `TestCmdSpecs_FlagsExactly` в красной фазе **пройдёт**: цикл идёт по `cmdSpecs`
    (`internal/cli/help_test.go:69`), лишний ключ в want-map просто не используется.
    Ошибка «неизвестный путь» (`help_test.go:73`) возникает только в обратную сторону —
    запись есть в `cmdSpecs`, а в want нет.
  - Оба Run-канона тоже **пройдут**. `TestRunRoutesAllStubs`: без роутинга `Run` отдаёт
    «неизвестный verb» → `ExitGeneric` + непустой stderr (`internal/cli/cli.go:132–136`) —
    ровно то, что asserts тест. `TestRunRoutesToCorrectHandler`: `installSpy` НЕ проверяет
    существование verb (`internal/cli/cli_test.go:122–128`, `orig := verbMap[verb];
    verbMap[verb] = spy`) и сам динамически вставит spy в `routes` — кейс станет зелёным.
  - Реально упадут только `TestCmdSpecs_OrderMatchesHelpOrder` и
    `TestRoomsParticipantsViaRun`, причём последний — **через панику**: restore у
    `installSpy` делает `routes[resource][verb] = orig` (присваивание nil, не delete) и
    оставляет в карте `routes["rooms"]["participants"] = nil`; тесты файла
    `cli_test.go` выполняются раньше `handlers_rooms_test.go`, и `TestRoomsParticipantsViaRun`
    вызовет nil-handler (`fn, ok := verbMap[verb]` даёт `ok=true`, `fn=nil`,
    `internal/cli/cli.go:132` → `invokeHandler` → вызов nil-функции). Паника роняет весь
    тест-бинарник — `TestCmdSpecs_*` до запуска не дойдут вовсе.
- **Что сделать:** переписать Expected к факту: красная фаза = FAIL всего прогона из-за
  паники в `TestRoomsParticipantsViaRun` (nil-остаток `installSpy` при отсутствующем verb);
  из перечисленных тестов самостоятельно падает только `OrderMatchesHelpOrder` с
  «got 8, want 9». Иначе исполнитель будет гоняться за фантомными падениями FlagsExactly /
  Run-канонов или «чинить» тесты, которые на самом деле зелёные.

### 2. Task 2, Step 2 — «Expected» называет compile-ошибки, которых не будет

- **Цитата:** «Expected: FAIL на компиляции — `undefined: roomsParticipantsHandler`,
  `m.participantsCalls undefined` (метода нет у `roomsSpyClient`), `client.Participant
  undefined` в интерфейсе».
- **Что не так:** Step 1 этого же таска уже добавил поля и метод `GetParticipants` в
  `roomsSpyClient` (блоки «в struct roomsSpyClient добавить поля» и «метод roomsSpyClient»),
  а `client.Participant` существует с Task 1 (Step 5). Единственная реальная ошибка
  компиляции — `undefined: roomsParticipantsHandler`.
- **Что сделать:** сузить Expected до фактической ошибки (`undefined:
  roomsParticipantsHandler`), убрать две лишние.

## 🟢 По красоте

### 3. Task 1, Step 1 — «все 13 наблюдаемых полей» — на самом деле их 14

- **Цитата:** «все 13 наблюдаемых полей присутствуют, клиент декодирует только 7».
- **Что не так:** и в перечне спеки §3, и в самой JSON-фикстуре плана 14 полей:
  `roomToken, inCall, lastPing, sessionIds, participantType, attendeeId, actorType, actorId,
  displayName, permissions, attendeePermissions, attendeePin, phoneNumber, callId`.
  Фикстура корректна — ошибочно только число в прозе (а на него опирается тезис о
  «реальном формате»).
- **Что сделать:** заменить «13» на «14».

### 4. Task 1, Interfaces (Consumes) — `ocsBody` упомянут, но не используется

- **Цитата:** «хелперы тестов `testCfg`/`wantAuth`/`ocsBody`/`newOCSServer`».
- **Что не так:** новый `participants_test.go` использует `testCfg`, `wantAuth`,
  `newOCSServer` (`internal/client/client_test.go:23,52`, `ocs_test.go:95`), но не
  `ocsBody` (`client_test.go:37`) — фикстуры читаются из файлов, тела инлайн.
- **Что сделать:** убрать `ocsBody` из Consumes.

### 5. Спека §6 «stub → errMock во все шесть» vs план — roomsSpyClient получает spy, а не errMock-стаб

- **Цитата:** план, Task 2 Step 1: «метод `roomsSpyClient` — вместо простого стаба errMock,
  чтобы новые тесты видели вызовы»; Step 3: «Стаб `errMock` — в пять остальных моков».
- **Что не так:** буква спеки §6 требует errMock-стаб во **все шесть** реализаций, план
  осознанно даёт `roomsSpyClient` полноценный spy (поля + метод). Функционально это
  необходимо (тестам нужны `participantsResult`/`participantsErr`/`participantsCalls` —
  тот же паттерн, что у list/find/search в `internal/cli/handlers_rooms_test.go:27–66`),
  и план документирует отклонение инлайн, но формулировка спеки остаётся рассинхронной.
- **Что сделать:** при случае поправить формулировку в спеке («во все шесть, кроме
  roomsSpyClient — ему spy») либо явно сослаться в спеке на план как на уточнение.
