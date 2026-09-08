# Review: `2026-09-08-nctalk-chat-edit-design.plan.md`

Дата: 2026-09-08. Три прохода: заземление в коде репозитория / контракт writing-plans / покрытие спеки `2026-09-08-nctalk-chat-edit-design.md`.

## 🔴 Блокер

### 1. Task 2 Step 1 — assertion `findCalls` в `TestChatEdit` ломает два собственных кейса таблицы

1. **Цитата** (тело цикла `TestChatEdit`):
   ```go
   if tc.wantEditCalls == 0 && spy.findCalls != 0 {
       t.Errorf("FindRooms не должен вызываться (findCalls=%d)", spy.findCalls)
   }
   ```
2. **Что не так.** Для кейсов `--name 0 совпадений → exit 2` и `--name 2 совпадения → exit 3` сам контракт требует вызова `FindRooms` — это единственный источник кодов 2/3 (`internal/room/room.go`: при `positional=="" && nameFlag!=""` всегда `client.FindRooms`; `internal/cli/room.go` → `ExitNotFound`/`ExitAmbiguous`). Шпион `chatSpyClient.FindRooms` инкрементирует `findCalls` безусловно (`internal/cli/handlers_chat_test.go:73-76`). В обоих кейсах `wantEditCalls: 0`, но `findCalls` будет 1 → `t.Errorf` срабатывает ложно, оба под-теста красные на корректной реализации. Expected Task 2 Step 5 «PASS — новые TestChatEdit* зелёные» недостижим без правки теста.
3. **Что сделать.** Добавить в таблицу поле `wantFindCalls int` (0 по умолчанию; `1` для кейсов с `--name`, где `FindRooms` обязан зваться) и сравнивать `spy.findCalls` с ним; либо ограничить текущую проверку кейсами без `--name` (например, флаг `noFind bool`).

## 🟡 Стоит уточнить

### 2. Task 3 Step 2 — Expected не соответствует фактическому поведению двух из трёх названных тестов

1. **Цитата**: «Expected: FAIL — `TestCmdSpecs_OrderMatchesHelpOrder` (len 7 != 8), `TestCmdSpecs_FlagsExactly` (…после Step 1 — падение из-за отсутствия записи в cmdSpecs), `TestCmdSpecs_CoverAllRoutes`/`TestRunRoutesToCorrectHandler` — ожидаемо красные до Step 3.»
2. **Что не так.**
   - `TestCmdSpecs_FlagsExactly` после Step 1 НЕ упадёт: сверка односторонняя — цикл идёт по `cmdSpecs` и ищет путь в `want` (`internal/cli/help_test.go:68-72`); лишний ключ в `want` не детектируется. Этот канон краснеет только в обратной ситуации (запись в `cmdSpecs` есть, ключ в `want` забыли).
   - `TestRunRoutesToCorrectHandler` с новым кейсом `chat/edit` пройдёт и ДО Step 3: `installSpy` пишет шпиона в `routes["chat"]["edit"]` обычным map-присваиванием, ключа в таблице для этого не нужно (`internal/cli/cli_test.go:118-124`), Run найдёт `ok=true` и spy вернёт ExitOK.
   - `TestCmdSpecs_CoverAllRoutes` действительно упадёт в этом прогоне, но по неописанной причине: restore вернёт `nil` в созданный ключ (`routes[resource][verb] = orig`, cli_test.go:124), и выполнившийся раньше (cli_test.go < help_test.go по алфавиту) `TestRunRoutesToCorrectHandler` оставит в `routes` лишний путь `chat edit` → множества разойдутся.
   Красным по заявленной планом причине остаётся только `OrderMatchesHelpOrder` (len 7 != 8).
3. **Что сделать.** Переформулировать Expected честно: красный — `OrderMatchesHelpOrder` (и `CoverAllRoutes` как артефакт nil-restore от нового кейса `chat/edit`); `FlagsExactly` и `RoutesToCorrectHandler` на этом шаге зелёные. TDD-цикл задачи в целом не рушится (красный есть), но исполняющий агент, увидев «зелёный вместо красного», потратит время на ложный дебаг.

### 3. Task 3 Step 1.7 — комментарий `TestRunRoutesAllStubs` станет «8 команд» при 7 кейсах в таблице

1. **Цитата**: «7. Комментарий `TestRunRoutesAllStubs` — «все 7 команд» → «все 8 команд».»
2. **Что не так.** В таблицу теста кейс `chat edit` план не добавляет — останется 7 кейсов (`internal/cli/cli_test.go:61-73`: rooms list/find/search, chat show/send, reactions get, search). Комментарий «все 8 команд доходят до своих stub-handler'ов» начнёт врать — это ровно тот комментарий-drift, который план сам же вычищает в других файлах (grep Task 5 Step 4).
3. **Что сделать.** Либо добавить кейс `{"chat edit", []string{"chat", "edit", "tok", "1"}}` (маска: handler вернёт ExitGeneric от mock-клиента/пустого stdin — как у соседнего `chat send` с заведомо невалидным `--text`), либо не трогать числительное в комментарии (переформулировать без счётчика). Первый вариант предпочтителен — он же закрывает дыру, найденную в пункте 2 (restore от кейса `chat/edit`).

### 4. Task 4 Step 2 — команда проверки skip-пути запущена без `-tags=integration`, заявленный SKIP не воспроизведётся

1. **Цитата**: «Run: `CGO_ENABLED=0 go vet -tags=integration ./internal/client/ && CGO_ENABLED=0 go test ./internal/client/ -run TestIntegration_EditMessage -v` / Expected: …тест SKIP («NCTALK_INTEGRATION_SEND != 1» …)».
2. **Что не так.** Вторая команда без `-tags=integration`: файл исключён build-констрайнтом (`internal/client/integration_test.go:1`), теста в бинарнике нет — go выведет `testing: warning: no tests to run` и ok, а не skip-сообщение gate-а.
3. **Что сделать.** Добавить тег: `CGO_ENABLED=0 go test -tags=integration ./internal/client/ -run TestIntegration_EditMessage -v` — тогда без env действительно будет `SKIP`.

## 🟢 По красоте

### 5. Спека §4 «`url.PathEscape` обоих сегментов» vs план — `strconv.Itoa` для messageId

1. **Цитата** (Task 1 Step 4): «token и messageId — отдельные path-сегменты: PathEscape токена, messageId форматируем через strconv.Itoa (целое — безопасно без эскейпа)».
2. **Что не так.** Спека §4 требует «`url.PathEscape` обоих сегментов»; план делает `Itoa` для второго. Поведение идентично (Itoa даёт только цифры/знак, PathEscape их не меняет), но расхождение с буквой спеки нигде не зафиксировано как отклонение — в отличие от честно оформленного отклонения по пути фикстуры.
3. **Что сделать.** Либо привести код к букве спеки (`url.PathEscape(strconv.Itoa(messageId))`), либо добавить одну строку в блок «Отклонение от буквы спеки».

### 6. Успех `202 Accepted` заявлен, но не зафиксирован ни одним тестом

1. **Цитата** (Global Constraints): «`0` успех (в т.ч. `202 Accepted`)» (таблица exit-кодов спеки §2 — то же).
2. **Что не так.** Единственный успех-тест (`TestEditMessage_Success`) покрывает только 200-фикстуру; на 202 поведение обеспечивает лишь общий транспорт (`internal/transport/transport.go` DoOCS: ошибкой считается только `meta.statusCode >= 400`). Спека §6 отдельного теста на 202 не требует — это заявленное-но-непроверенное, а не дыра контракта.
3. **Что сделать.** По желанию автора: подкрепить строку тестом (фикстура/`ocsBody` с `statusCode: 202` и `parent.id`) — либо осознанно оставить на интеграционном цикле Task 4.
