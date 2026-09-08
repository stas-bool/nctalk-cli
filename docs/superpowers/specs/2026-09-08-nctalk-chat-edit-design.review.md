# Ревью спеки: 2026-09-08-nctalk-chat-edit-design.md

- **Дата ревью:** 2026-09-08
- **Проходы:** (1) документ сам по себе; (2) заземление в коде `/Users/stas/Projects/My/NCCliClient`
  + официальная документация Talk (nextcloud-talk.readthedocs.io, chat API «Editing a Chat Message»)
  и исходник контроллера `nextcloud/spreed` `lib/Controller/ChatController.php` (`editMessage`).
- **Итог:** 🔴 0 · 🟡 3 · 🟢 3.

Заземлено и подтверждено (без замечаний, для протокола проверки): `pathChat`
(`internal/client/chat.go:22`), `SendMessage`-паттерн (`mutate=true`, `url.PathEscape`,
`json.Marshal`), guard пустого сообщения до сети, трим одного trailing `\n`/`\r\n`,
`render.NewMessageID{,JSON}` (`internal/render/render.go:174`, `render_json.go:59`),
`ResolveRoom` (предупреждение при positional+`--name`, кандидаты при >1, коды 2/3 —
`internal/cli/room.go`), распределение позиционных `<room> <messageId>`
(`internal/cli/handlers_reactions.go:72-83`), exit-маппинг 404→2 / прочее→1
(`internal/exit/exit.go`, `internal/cli/exit.go`), транспорт пропускает HTTP 202 как
успех и не проверяет HTTP-статус вне OCS-конверта (`internal/transport/transport.go:169-219`),
подсказка про verb (`internal/cli/cli.go:119`), мутационные флаги интеграционных тестов
(`internal/client/integration_test.go:254-259`), эндпоинт/тело/ответ PUT-правки и коды
400/403/404/405/412, capability `edit-messages`, лимит 24ч, только `comment`,
`ocs.data` = объект системного сообщения с `parent` (подтверждено исходником контроллера:
`$data['parent'] = $parseMessage->toArray(...)`; статус 202 при боте/bridge), секция
`chat send` в README (`README.md:109`), «7 команд» в CLAUDE.md (`CLAUDE.md:5`).

## 🟡 Стоит уточнить

### 1. «Anti-drift тесты подхватывают декларацию автоматически» — неверно для двух из них

- **Цитата (§5):** «general/resource/detailed help и anti-drift тесты (`TestCmdSpecs_...`,
  `TestAntiDrift_...`) подхватывают декларацию автоматически»; **(§6):** «help
  (`internal/cli/help_test.go`): автоматически анти-drift'ом после добавления в `cmdSpecs`».
- **Что не так:** два теста содержат hardcoded-каноны по текущим 7 командам и при
  добавлении восьмой **падают**, а не «подхватывают»:
  - `TestCmdSpecs_OrderMatchesHelpOrder` — `internal/cli/help_test.go:39-44`:
    `want := []string{... "chat show", "chat send", ...}` + проверка
    `len(cmdSpecs) != len(want)` (строка 45) → `got 8, want 7`; требует ручной правки,
    заодно фиксирует позицию `chat edit` в списке — а §5 не говорит, куда вставлять
    (после `chat send`?);
  - `TestCmdSpecs_FlagsExactly` — `internal/cli/help_test.go:59-67`: map `want`
    без ключа `"chat edit"` → `t.Fatalf("неизвестный путь %q (нет в want)")` (строка 72).
  Автоматически работают только `TestCmdSpecs_CoverAllRoutes`, `TestHandleHelp_DetailedContent`,
  `TestAntiDrift_HelpMatchesDeclaration`, `TestAntiDrift_HandlerMatchesDeclaration`
  (через `lookupHandlerByPath` → `routes`, help_test.go:554-564).
- **Что сделать:** дополнить §6 явным шагом «обновить каноны `TestCmdSpecs_OrderMatchesHelpOrder`
  и `TestCmdSpecs_FlagsExactly` (вставка `chat edit` после `chat send`)». Для прогона через
  /spec-to-code это обязательный пункт плана, а не автоматика.

### 2. `TestAntiDrift_HandlerMatchesDeclaration`: спец-кейс stdin и `buildPositionalArgs` не покрывают `chat edit`

- **Цитата (§5):** «chatEditHandler ... по образцу `chatSendHandler` (чтение тела)»;
  **(§6):** анти-drift для help «автоматически».
- **Что не так:** в `TestAntiDrift_HandlerMatchesDeclaration` подача непустого Stdin
  захардкожена под `joinPath(cs.Path) == "chat send"` (`internal/cli/help_test.go:496`
  и `:538`) — ровно потому, что send читает тело ДО ResolveRoom и при nil-Stdin
  fallback-ает на `os.Stdin` (риск зависания на TTY при интерактивном `go test`).
  `chat edit` ведёт себя так же, но под условие не попадает. Аналогично
  `buildPositionalArgs` (`internal/cli/help_test.go:569-584`) не имеет кейса
  `"chat edit"` → возвращает `nil`; тест формально остаётся зелёным (флаги парсятся
  раньше позиционных), но handler перестаёт получать валидные позиционные — смысл
  prong-ов ослабляется. Если же кейс добавить по аналогии с `reactions get`
  (`["tok123", "1"]`), без расширения stdin-условия prong 1 для `--name` дойдёт до
  чтения тела с nil-Stdin → то самое зависание, ради которого кейс вводили.
- **Что сделать:** включить в §6 правку теста: case `"chat edit"` в `buildPositionalArgs`
  + расширить условие спец-кейса Stdin с `== "chat send"` на `chat send`/`chat edit`.

### 3. Сопроводительные правки документации не покрывают `docs/integration-run.md`

- **Цитата (§5):** «Сопроводительные правки документации (входят в задачу): README ...;
  CLAUDE.md: «7 команд» → «8 команд»» — других пунктов нет; **(§6):** интеграционный
  тест под существующими флагами `NCTALK_INTEGRATION_SEND=1` + `NCTALK_INTEGRATION_ROOM`.
- **Что не так:** `docs/integration-run.md` — регламент интеграционных прогонов (на него
  ссылается CLAUDE.md) — описывает `NCTALK_INTEGRATION_SEND` как флаг исключительно
  `TestIntegration_SendMessage`: строки 7, 26-27 (таблица env: «включить мутационный
  TestIntegration_SendMessage»), 61-66 (команда запуска `-run TestIntegration_SendMessage`),
  83-84 («Мутация (`SendMessage`) ... только при `NCTALK_INTEGRATION_SEND=1`»), 97
  (таблица тестов). Новый edit-мутационный тест под тем же флагом делает эти описания
  и таблицу тестов неточными.
- **Что сделать:** добавить `docs/integration-run.md` в список сопроводительных правок
  (семантика флага «мутационные SendMessage/EditMessage», строка в таблице тестов,
  пример запуска).

## 🟢 По красоте

### 4. Перечень серверных отказов правки неполон: есть ещё `413`

- **Цитата (§2):** «Серверные отказа правки (все — OCS-error ...): `400` ..., `403` ...,
  `405` ..., `412` (lobby)».
- **Что не так:** контроллер Talk возвращает также `413 Request Entity Too Large`
  (слишком длинный текст правки) — `lib/Controller/ChatController.php`, `editMessage`,
  `STATUS_REQUEST_ENTITY_TOO_LARGE` в @return. На клиент поведение не влияет (все
  не-404 → exit 1 с текстом сервера), но §2 подаёт перечень как исчерпывающий.
- **Что сделать:** дописать `413` (или пометить перечень «основные, не исчерпывающе»).

### 5. Счётчик «7 команд» живёт не только в README/CLAUDE.md, но и в комментариях кода

- **Цитата (§5):** правки документации — только README и CLAUDE.md.
- **Что не так:** «7 команд/7 методов» также в комментариях: `internal/cli/help.go:37`
  («единый источник правды: 7 команд»), `internal/cli/cli.go:16` («7 методов»),
  `internal/cli/cli_test.go:58`, `internal/cli/help_test.go:12` и `:321`. Комментарии,
  не поведение — но это тот же drift, который проект ловит анти-drift-тестами.
- **Что сделать:** включить правку комментариев в план (или осознанно перечислить, что
  не правим).

### 6. Комбинация «0 позиционных С `--name`» не перечислена в §2

- **Цитата (§2):** случаи распределения перечислены вплоть до «0 позиционных **без**
  `--name` → ошибка exit 1»; комбинация «0 позиционных, `--name` задан»
  (`nctalk chat edit --name "Комната"` — messageId отсутствует) не названа.
- **Что не так:** поведение определено шаблоном `reactionsGetHandler`
  (`internal/cli/handlers_reactions.go:81-82`, default-ветка) — тот же exit 1 с
  подсказкой «...или --name <имя> <messageId>», но из §2 это напрямую не следует.
- **Что сделать:** добавить одну строку в перечень случаев §2.
