# Code-review: `--help` для базового CLI `nctalk`

Дата: 2026-08-12
Ветка: `feat/nctalk-help`
Объём: 5 коммитов (`c739f10..44c021d`), 4 файла (+1042 строки): `internal/cli/help.go` (новый), `internal/cli/help_test.go` (новый), `cmd/nctalk/main.go`, `cmd/nctalk/main_test.go`.
Спека: [`2026-08-12-nctalk-help-design.md`](../specs/2026-08-12-nctalk-help-design.md). План: [`2026-08-12-nctalk-help-design.plan.md`](../plans/2026-08-12-nctalk-help-design.plan.md).

> **Исполнитель:** internal-субагент на glm-5.2 (фоллбэк). Штатно code-review идёт через acpx/glm-5.2 (изолированный read-only контекст), но opencode-сервис был недоступен (`Internal error: OpenCode service failure`). В `--auto` режиме пайплайн не останавливается — фоллбэк на субагента glm-5.2 (та же модель, изолированный контекст: читает только `git diff`). Цели acpx (изоляция контекста координатора, независимый read-only анализ) достигнуты; cross-model взгляд отсутствует (координатор и так glm-5.2).

## Предварительная верификация (всё зелёное)

- `CGO_ENABLED=0 go build ./...` — OK (новый код — только stdlib: `fmt`/`io`/`strings`/`testing`/`bytes`/`context`/`reflect`/`sort`; CGO не требуется).
- `CGO_ENABLED=0 go vet ./...` — чисто.
- `CGO_ENABLED=0 go test ./...` — 18 пакетов `ok`, 0 FAIL.
- `TestCmdNctalkDoesNotDependOnPion` — PASS (изоляция `cmd/nctalk` от pion сохранена).
- AI-атрибуция в коммитах — отсутствует (`Co-Authored-By`/AI-trailer нет ни в одном из 5 сообщений).

## Замечания

### HIGH

Нет.

### MEDIUM

**[M1] `internal/cli/help.go` (`computePath`) + (`HandleHelp`, switch по `len(path)`) — UX-баг: `nctalk search foo --help` и `nctalk help search foo` дают «неизвестная команда "search foo"» (exit 1) вместо help по `search`.**
Подтверждено через бинарник. `computePath` собирает первые 2 не-флаговых токена; для leaf-команды `search` (единственная leaf с positional-аргументом) её term заполняет 2-й слот пути → `findSpec(["search","foo"])` → nil → fallback в «невалидный путь». Для 2-уровневых команд этот случай невозможен: путь `["rooms","list"]` уже на `len=2`, «лишние» positionals игнорируются. **Фикс:** в `HandleHelp` перед `switch len(path)` нормализация — если `len(path) > 1` и `findSpec([]string{path[0]}) != nil` (т.е. `path[0]` — известная leaf-команда), усечь `path` до `[path[0]]`. Убирает асимметрию leaf-vs-resource, делает `search foo --help` эквивалентом `search --help`. (Видно конечному пользователю — приоритетный фикс.)

**[M2] `internal/cli/help_test.go` (док-комментарий `TestAntiDrift_HandlerMatchesDeclaration`) — док-комментарий обещает больше, чем проверяет тест.**
Текст обещает «handler отвергает **любой** незаявленный `--*-флаг`». По факту prong 2 кидает в handler ОДИН конкретный флаг `--zzz-undeclared-test-flag` и проверяет только его отвержение. Реальная дыра: если разработчик добавит в handler новый флаг (например, `--foo`), но забудет обновить `cmdSpecs.Flags` И canonical-map `want` в `TestCmdSpecs_FlagsExactly` — ни один тест не упадёт: handler молча примет незаявленный флаг, help его не покажет. Жёсткий канон `TestCmdSpecs_FlagsExactly` эту дыру НЕ закрывает (он сверяет `cs.Flags` с hardcoded `want`, не с фактическим поведением handler-а). **Суждение:** дыра реальная, но **приемлемая** для целевого класса дрейфа (исторические баги `rooms find --include-former` / `rooms search --limit` — это направление «заявлено в декларации, но handler не парсит», и prong 1/шаг 1 теста их ловит). Полная защита от обратного направления потребовала бы property-теста (перебор словаря имён), что дорого и хрупко. **Фикс (минимальный):** смягчить формулировку комментария на «отвергает незаявленный флаг `--zzz-undeclared-test-flag`». Дополнительно (желательно): второй тест, перебирающий известный список «опасных» имён (`--limit`, `--type`, `--name`, `--user`, …), проверяющий, что handler команды их НЕ принимает, если они не в `cs.Flags`.

### LOW

**[L1] `internal/cli/help.go` (`joinPath`) — пересобственный `strings.Join`.** Ручная конкатенация вместо одной строки. **Фикс:** `return strings.Join(p, " ")` (`strings` уже импортирован).

**[L2] `internal/cli/help_test.go` (`tokeniseFlags`) — хрупкий hand-rolled токенизатор.** Матчит только `--[a-z][a-z-]*`. Если однажды текст описания законно упомянет `--что-то` в прозе (например, «аналогично `--silent` у `chat send`»), тест упадёт по false-positive «незаявленный флаг». Сегодня описания чистые, но это мина. **Фикс:** парсить только строки под заголовком «Флаги:» (регулярная структура `  <name> <desc>`), либо вынести список флагов из render в структуру и сравнивать её.

**[L3] `internal/cli/help.go` (док-комментарий `cmdSpecs`) — неточная ссылка.** Написано «regression-защита — `TestCmdSpecs_FlagsExactly` и `TestAntiDrift_Handler` (Task 4)». Реальное имя теста — `TestAntiDrift_HandlerMatchesDeclaration`; ссылка на «Task 4» — нумерация плана, которая устареет. **Фикс:** фактическое имя теста; убрать «(Task 4)».

**[L4] `internal/cli/help_test.go` — отсутствует тест-кейс для `nctalk search foo --help` (баг из M1).** Тест либо задокументировал бы текущее поведение, либо вскрыл бы проблему. Также нет явного кейса `nctalk --json --help` (поведение корректно: path=[], общий help в stdout, exit 0, но не покрыто). **Фикс:** добавить кейсы в `TestHandleHelp_Routing` (после фикса M1 — ожидают exit 0, stdout).

**[L5] `internal/cli/help.go` — формат ошибки «неизвестная команда» использует `%q` от `strings.Join(path, " ")`, давая строку в кавычках (`"search foo"`).** Стилево согласовано с `cli.go` (`cli.Run` тоже использует `%q` от одного resource) — **не баг**. Кавычки вокруг составного пути читаются чуть страннее. Оставлено на усмотрение.

## Проверено, проблем нет (не-замечания)

- **Перехват help в `run()` до `config.Load`:** корректный. `HandleHelp` не трогает `Deps.Client`/`Stdin`/`Now` → `run()` создаёт `Deps` только с `Stdout`/`Stderr` для help-ветки, без инициализации клиента. `TestRun_HelpWithoutEnv_WorksWithoutCreds` покрывает.
- **Exit-контракт 0/1/2/3:** соблюдён. Help → 0; no-args → 1 (`ExitGeneric`); unknown-path → 1 (`ExitGeneric`). `ExitNotFound`/`ExitAmbiguous` в help-логике не участвуют.
- **Роутинг `search` (special-case):** сохранён. `isResourceName` корректно возвращает false для `search` (его нет в `routes`), `findSpec(["search"])` возвращает spec. `TestCmdSpecs_CoverAllRoutes` проверяет синхронизацию `cmdSpecs` ↔ `routes`+search.
- **Изоляция `cmd/nctalk` от pion/WebRTC:** ни `help.go`, ни `help_test.go`, ни `main.go` не добавили звонковых импортов. Guard PASS.
- **Redact кредов:** help-вывод ссылается только на имена env (`NEXTCLOUD_URL/LOGIN/PASS/TIMEOUT`), не на значения — соответствует контракту.
- **Парсинг флагов команд:** handler-ы не тронуты (diff только в new-файлах и `main.go`). `cli.Run`/`extractJSON` неизменны.
- **`--json` как не-help-маркер:** проверено (`TestIsHelpRequest_NotHelp`, `TestHandleHelp_NotHandled`) — корректно проваливается в обычный flow.
- **`--flag value` vs `--flag=value`:** `computePath` + `isValueFlag` корректно обрабатывают обе формы.
- **Unknown-флаг в `computePath`:** safe — обрабатывается как boolean, не съедает следующий positional.

## СВОДКА

issuesCount=7 (HIGH: 0, MEDIUM: 2, LOW: 5). Качество — **высокое**. Реализация следует спеке, тесты содержательны и покрывают основные классы дрейфа; связка `HandleHelp` до `config.Load` в `main.go` чистая; сборка/изоляция/exit-контракт не повреждены. Приоритет до мёрджа — фикс **M1** (UX-асимметрия для leaf-команд с positional-аргументами: `search foo --help` показывает ошибку вместо help — видно конечному пользователю); **M2** — желательно, не блокирующее. LOW — косметика/усиление тестов.
