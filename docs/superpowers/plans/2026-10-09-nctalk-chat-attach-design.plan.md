# `nctalk` — отправка файлов (`chat send --attach`) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Научить `nctalk chat send` отправлять локальный файл в чат Talk флагом `--attach <путь>`. Текст из stdin/`--file` становится необязательной подписью, вывод — id сообщения с файлом. С файлом работают `--reply-to`, `--thread`, `--thread-title`, `--silent` и `--reference-id`.

**Architecture:** Дельта к базовому CLI по спеке `docs/superpowers/specs/2026-10-09-nctalk-chat-attach-design.md` (дизайн утверждён, правки по ревью спеки — коммит `cd7b9cd`). Используется классический механизм Talk: WebDAV PUT в папку вложений и OCS-шаринг `shareType=10`. Ответ шаринга id сообщения не содержит, поэтому id ищется по `referenceId`. Слои — как у прежних дельт:
- `internal/transport` — новый `dav.go` с функцией `DoDAV`;
- `internal/client` — новые `files_helpers.go` (чистые хелперы) и `files.go` (`SendFile`), а в `client.go` — отдельный upload-клиент без `Timeout`;
- `internal/render` — функция `NewMessageIDNullJSON`;
- `internal/cli` — ветка `--attach` в `chatSendHandler`, общий `readBody` с правилом терминала, 11-й метод интерфейса `TalkClient` с правкой шести моков, декларация help.

Фикстуры — только живые: их снимают curl-ом с боевого сервера ДО написания тестов (Task 1). Интеграционный мутационный `TestIntegration_SendFile` стоит за build-тегом `integration`.

**Tech Stack:** Go 1.21+, только stdlib. Новые импорты тоже из stdlib: `encoding/xml`, `crypto/rand`, `encoding/hex`, `regexp`, `math`, а также `syscall` в unix-тесте. Для Task 1 нужны `curl`, `jq` 1.6+ и `od`.

**Ветка:** реализация идёт в ветке `feat/chat-attach` от `main`. Исполнитель создаёт ветку ПОСЛЕ того, как координатор закоммитит в `main` этот план и его ревью; план и ревью коммитит координатор, в ветку они не входят. Команды — в шаге «Перед Task 1».

**Проверено при планировании:**
- Код задач 2–6 собран в копии репозитория и прогнан по фазам red → green теми же командами, что в шагах. Ожидаемые сообщения red-фаз — фактический вывод этих прогонов.
- Протокол Task 1 прогнан всухую: под zsh, против локальной имитации эндпоинтов, вплоть до зелёного `go test ./...` на полученных фикстурах. Живых запросов к серверу при планировании не было, поэтому реальный формат сверяется в самой Task 1.

## Global Constraints

- **`CGO_ENABLED=0` обязательно** для всех `go build` / `go vet` / `go test`: на macOS Tahoe 26.5 + Go 1.21.4 без него сборка падает с `dyld: missing LC_UUID`.
- Только stdlib, зависимостей вне stdlib не вводить.
- Кириллические комментарии в существующем коде сохранять. Новые комментарии писать на русском, в стиле окружающего файла.
- **Новые файлы должны быть gofmt-чистыми:** `gofmt -l <новые файлы>` печатает пусто.
- **Файлы с уже имеющимся gofmt-drift не форматировать** — иначе шум в diff (прецедент — заметки плана тредов). Это `internal/cli/cli_test.go`, `internal/cli/exit.go`, `internal/cli/exit_test.go`, `internal/cli/handlers_chat.go`, `internal/cli/handlers_chat_test.go`, `internal/cli/help_test.go`, `internal/client/search.go`, `internal/client/types.go`, `internal/render/parse_since.go`, `internal/room/room.go`. Правки в них делать в стиле окружения: например, выравнивание var-блока `chatSendHandler` — как у соседних строк. `gofmt -w` по пакетам не запускать.
- **Гейт каждой задачи:** `CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./...` зелёный, после него — отдельный коммит задачи.
- **Коммиты — Conventional Commits обычным текстом, БЕЗ emoji и БЕЗ какой-либо AI-атрибуции.** Никакого `Co-Authored-By: Claude` и подобных трейлеров: это юридическое требование владельца, оно перекрывает дефолты ассистента.
- **Стейджинг — только явными путями файлов задачи** (`git add <пути>`), не `git add -A`, `git add .` или `git commit -a`. `TODO.md` — чужая незакоммиченная правка: не трогать никогда (ни правкой, ни `git add`, ни `git checkout`/`git stash`).
- **Публичный репозиторий `stas-bool/nctalk-cli`.** В код, тесты, фикстуры, заметки и коммиты не попадают адрес сервера, логин, uid, имена, реальные token-ы и имена пользовательских папок.
- **Пароль `NEXTCLOUD_PASS` никогда не выводить** (не `echo`, не `set -x`). Креды в curl передаются только через stdin-конфиг: `printf 'user = "%s:%s"\n' "$NEXTCLOUD_LOGIN" "$NEXTCLOUD_PASS" | curl -K - …`. Не `-u`: с ним пароль попал бы в argv и в `ps`.
- **Token integration-комнаты** даёт координатор в брифе. В env его НЕТ, поэтому в командах он задаётся явно: `NCTALK_INTEGRATION_ROOM='<token integration-комнаты из брифа координатора>'`. Сам token не попадает в план, код, фикстуры, заметки и коммиты. Комната общая с другими агентами, поэтому в фикстуры берутся только свои сообщения.
- **Эталон контракта** — спека `2026-10-09-nctalk-chat-attach-design.md`. Exit-коды (§2.6, новых кодов нет):
  - `0` — успех, в том числе «файл отправлен, id не определён» (с предупреждением в stderr);
  - `1` — ошибки флагов (повтор или пустой `--attach`, конфликт, неположительный id, пустой `--thread-title`, неизвестный флаг); файл не найден, не обычный или не читается; ошибка чтения подписи; вложения запрещены на сервере; сообщение `--reply-to` не replyable; ошибки WebDAV; шаринг 400/403/5xx; сеть; 401;
  - `2` — OCS 404 на любом шаге (комната, «Conversation not found» шаринга, тред `--thread`, сообщение `--reply-to`) и 0 совпадений `--name`;
  - `3` — больше одного совпадения `--name`.
- **API (спека §3):**
  - `GET /ocs/v2.php/cloud/user` — `ocs.data.id` = uid, он ≠ логину;
  - `GET /ocs/v2.php/cloud/capabilities` — `spreed.config.attachments.{allowed,folder}`;
  - `GET …/apps/spreed/api/v1/chat/{token}/threads/{id}` и `GET …/chat/{token}/{id}/context?limit=0` — pre-check;
  - WebDAV `HEAD`/`PUT`/`MKCOL`/`DELETE` под `/remote.php/dav/files/{uid}`;
  - `POST /ocs/v2.php/apps/files_sharing/api/v1/shares` — JSON, `talkMetaData` — JSON-строка;
  - поиск id — существующий `GetChat{Limit: 50}`.
- **Безопасность (спека §4.1):**
  - `Authorization: Basic` собирается на каждый запрос;
  - все ошибки DAV проходят через `transport.SanitizeErr`; dump запросов и ответов запрещён;
  - редиректы подчиняются `SameHostRedirectPolicy`, у upload-клиента тоже;
  - регресс-тесты с паролем-маркером `SECRET_MARKER` остаются зелёными: существующий `TestRun_PasswordDoesNotLeak_CrossHostRedirect` и новый `TestRun_PasswordDoesNotLeak_AttachPutRedirect`.
- **Token кладётся в путь БЕЗ `url.PathEscape`** в новом коде: сегменты экранирует транспорт (инвариант `GetParticipants`/`ListThreads`). `PathEscape` в `chat.go`/`reactions.go` — легаси, его не копировать.
- **`messageParameters` в chat-API** — объект при непустых параметрах и `[]` при пустых (инвариант CLAUDE.md; тип `MsgParams` принимает оба формата). Фикстуры обязаны отражать реальный формат.
- **Живой API — истина.** Если формат расходится с ожидаемым (таблица в Task 1 Step 9), работа идёт в автономном режиме:
  - адаптировать код и тесты;
  - поправить пункт спеки с пометкой «(уточнено по живому API <дата>)»;
  - записать расхождение в заметки.

  Останавливаться — только если фича принципиально невыполнима (строки «СТОП» таблицы).
- **Заметки исполнения** ведутся в `docs/superpowers/plans/2026-10-09-nctalk-chat-attach-design.plan.notes.md`. Файл создаётся при первой записи, формат — как у заметок плана тредов: заголовок `# Заметки по реализации 2026-10-09-nctalk-chat-attach-design.plan.md`, записи `## [Task N] <суть>` со строками `Что:` и `Почему:`. Заметка коммитится вместе с задачей, к которой относится.

## Решения плана (уточнения спеки, принятые при планировании)

1. Token integration-комнаты в план не пишется (репозиторий публичный), только плейсхолдер `<token integration-комнаты из брифа координатора>`.
2. Имя файла «.» или «..» отвергается наравне с пустым и содержащим «/» (`isPlainSegment`). Спека §5 называет только пустое имя и «/», но `path.Join` в транспорте схлопнул бы «.»/«..», и PUT ушёл бы на саму папку вложений или её родителя. Та же проверка применяется к uid из `cloud/user`: недопустимый uid — drift формата и ошибка.
3. Дотфайл: `.bashrc` → `.bashrc (2)`. Веб-клиент дал бы ` (2).bashrc`, но совпадение «расширения» с позиции 0 расширением не считается. Правило спеки §1 («Модель», расширение по `\.[0-9a-z]+$`) дотфайл покрывало иначе и поправлено при планировании, с пометкой «(уточнено при планировании 2026-10-09)».
4. Числовой суффикс ` (N)`, который не влезает в `int`, суффиксом не считается и остаётся частью основы имени.
5. В cli файл проверяется так: `os.Stat` ДО `os.Open`, затем `Stat` открытого дескриптора. Спека §2.3/§6 (было «`os.Open` + `Stat`») поправлена под этот порядок при планировании, с пометкой «(уточнено при планировании 2026-10-09)»; Task 7 спеку не трогает. Причины:
   - `open(2)` FIFO без писателя блокируется навсегда, поэтому не-обычный файл отсекается до открытия;
   - повторный `Stat` закрывает подмену файла между проверкой и открытием.
6. `uploadTimeout`:
   - при `NEXTCLOUD_TIMEOUT <= 0` (у `http.Client` это «без таймаута») срок PUT не ставится. Пакет `config` принимает `0s` и отрицательные значения, а формула спеки §4.2 дала бы для них конечный или отрицательный срок. Поэтому спека поправлена при планировании, с пометкой «(уточнено при планировании 2026-10-09)»;
   - переполнение — потолок `math.MaxInt64`;
   - «1 с на каждые начатые 64 КиБ» — округление вверх.
7. `DELETE` при cleanup считается успешным при любом 2xx и при 404 (файла уже нет). Спека §3.4 называла только 204 и поправлена при планировании, с пометкой «(уточнено при планировании 2026-10-09)».
8. Тексты «не найдено» у pre-check: «тред N не найден (или комната недоступна)» и «сообщение N не найдено (или комната недоступна)». Ответ 404 этих эндпоинтов не отличает отсутствие треда или сообщения от отсутствия комнаты.
9. `referenceId` уходит в шаринг таким, каким его задал пользователь: нормализует сервер. Нормализованное значение нужно только для поиска. Генерация — если значение пусто ПОСЛЕ нормализации.
10. MKCOL выполняется не больше одного раза за вызов `SendFile`. Если после MKCOL повторный PUT снова даёт 409 — ошибка «папка вложений недоступна».
11. Если `attachments.allowed` отсутствует, вложения считаются запрещёнными (проверка `!= true`, как в тексте ошибки). Если нет `spreed` — ошибка «Talk недоступен на сервере».
12. PUT со статусом 200, 3xx и любым другим, кроме 201/204/409/412, — ошибка. На 301/302 Go превращает PUT в GET, а на 307/308 тело без `GetBody` не переотправляет; молчаливого успеха нет (спека §4.1).
13. Если `MessageId == 0` и `LookupErr == nil` (такое возможно при прямых вызовах), в stderr пишется «причина не указана».
14. `talkMetaData` и тело шаринга собираются через `encoding/json`. HTML-экранирование `<>&` в `\u003c`/`\u003e`/`\u0026` допустимо: сервер делает `json_decode`.
15. Фикстуры `cloud_user.json` и `capabilities_attachments.json` усечены до полей, нужных клиенту: `id`/`display-name` и `spreed.config.attachments`. Усечение capabilities требует спека §7, а остальные поля `cloud/user` (e-mail, телефон, квота) — персональные данные. Нестандартное имя папки вложений в фикстурах обезличивается в `/Talk`.
16. Случайные `referenceId` захвата нормализуются в 64-символьные константы `0…01`/`0…02`/`0…03`. id — в фиксированные значения: сообщения 1500–1503 и 1510, файлы 2001–2003, папка 2000, шар 3001.
17. К частым статусам DAV в тексте ошибки добавляется подсказка:
    - 401 — «проверьте NEXTCLOUD_LOGIN/NEXTCLOUD_PASS»;
    - 403 — «нет прав на запись»;
    - 413 — «файл больше лимита сервера или прокси»;
    - 423 — «файл заблокирован»;
    - 507 — «недостаточно места в хранилище».
18. С `--attach` флаг `--file` — источник подписи (спека §2.1/§2.2), без `--attach` — источник тела, как прежде.
19. В help `--attach` стоит последним флагом `chat send` (после `--file`), пример с подписью — третьим.
20. cli-тесты ветки `--attach` лежат в отдельном файле `handlers_chat_attach_test.go`; шпион `chatSpyClient` и хелперы `newChatSendDeps`/`newChatDeps` берутся из `handlers_chat_test.go`. FIFO-тест — в `handlers_chat_attach_unix_test.go` с `//go:build darwin || linux`, потому что `syscall.Mkfifo` нет на Windows. Спека §7 называла файлом cli-тестов `handlers_chat_test.go` и поправлена при планировании, с пометкой «(уточнено при планировании 2026-10-09)».
21. Правило терминала проверяется тестами и без управляющего терминала. Детектор попытки чтения — `/dev/null`, открытый только на запись: `read(2)` на нём даёт EBADF. Так ловятся оба регресса — «правило проверено до fallback `nil → os.Stdin`» и «правило снято». Подтест с настоящим `/dev/tty` оставлен и скипается, если терминала нет.
22. `TestIntegration_SendFile` делает около 40 запросов к боевому серверу, поэтому его общий срок — `3 × integrationTimeout`.
23. «errMock-стабы» спеки §5 на деле — пять spy-моков. Каждый из них получает только новый метод `SendFile`, возвращающий `errMock`; остальные методы не трогаются (ревью спеки, раздел «По красоте», п. 1).

## Риски и альтернативы

**Риски (снятие — в задачах):**

1. Формат API сверен по исходникам spreed/server, а не живым запросом. Это шаринг с `talkMetaData`, `referenceId` у сообщения-файла, `thread_created`, `context?limit=0`, статусы WebDAV. Снятие — Task 1: живые фикстуры ДО тестов, jq-сверка и таблица расхождений с действиями (Step 9).
2. PUT в несуществующую папку может ответить 404 вместо 409 (зависит от версии Sabre/Nextcloud), и тогда MKCOL не сработает. Task 1 Step 6 фиксирует факт; на случай расхождения в таблице есть строка.
3. У сообщения-файла в chat-API может не оказаться `referenceId`, и поиск id станет невозможен. Строка таблицы задаёт запасной вариант: поиск по `messageParameters.file.path`.
4. Расширение `cli.TalkClient` ломает компиляцию шести моков. Поэтому интерфейс и все моки правятся одним шагом (Task 5 Step 1), до red-тестов.
5. Комната общая, и «сосед» для `context` несуществующего id может оказаться чужим сообщением. Task 1 Step 7 постит своё сообщение и сверяет id; если сосед чужой — шаг повторяется.
6. У загрузки нет общего таймаута: зависший канал держит PUT до срока контекста (`NEXTCLOUD_TIMEOUT` + 1 с на 64 КиБ). Это осознанно (спека §4.2), срок покрыт тестом.
7. Окно поиска — 50 сообщений. В очень активной комнате id может не найтись: тогда exit 0, предупреждение и `{"id": null}`, файл при этом отправлен (компромисс спеки §3.6).
8. При массовых коллизиях подбор имени делает до 1000 пар HEAD (+PUT) round-trip-ов — это потолок спеки §3.4.
9. Если процесс упадёт между PUT и шарингом, файл останется в папке вложений: cleanup идёт только после ошибки шаринга в том же процессе. Это осознанно, такой файл удаляется вручную.
10. Шаринг может не принять JSON-тело (принимает form) — на этот случай есть строка таблицы.
11. В правимых файлах есть gofmt-drift, его не выправлять (см. Global Constraints).
12. Мутационные шаги Task 1 и Task 6 оставляют в integration-комнате сообщения «файл больше недоступен» — та же семантика допуска, что у прежних мутационных тестов.

**Альтернативы (отклонены, спека §1/§9 — не реализовать):**

- Conversation subfolders Talk 24 (`attachment/folder` + Draft): не задокументирован, работает только на Talk 24+, игнорирует `threadTitle`.
- Отдельный verb (`chat attach`): принята надстройка `--attach` над `chat send`.
- Несколько файлов за вызов, chunked upload, шаринг файла, уже лежащего в Nextcloud.
- Exit ≠ 0, когда id не найден: файл уже отправлен, и повтор вызова создал бы дубликат.
- Отказ от pre-check `--thread`/`--reply-to`: для файлов сервер молча игнорирует невалидные значения.
- `GetBody` у PUT для переотправки на 307/308: редирект загрузки трактуется как ошибка, молчаливого успеха нет.
- Один doer на все запросы: `http.Client.Timeout` оборвал бы загрузку большого файла.

## Карта файлов

| Файл | Ответственность | Задача |
|---|---|---|
| `testdata/{cloud_user,capabilities_attachments,share_create,chat_file_messages,chat_context_message,chat_context_neighbor,chat_context_system,thread_info,thread_not_found}.json`, `testdata/dav_error_{409,412}.xml` | живые обезличенные ответы API | 1 |
| `internal/transport/dav.go` + `dav_test.go` | `DoDAV`, `DAVResponse`, извлечение `s:message` | 2 |
| `internal/client/files_helpers.go` + `files_helpers_test.go` | имена-кандидаты, `referenceId`, `talkMetaData`, папка вложений, срок PUT | 3 |
| `internal/client/client.go` (+ `client_test.go`) | поле `uploadDoer`, upload-клиент без `Timeout` | 3 |
| `internal/client/files.go` + `files_test.go` | `SendFile` и его шаги | 4 |
| `internal/cli/cli.go`, `cli_test.go`, `handlers_reactions_test.go`, `handlers_search_test.go`, `room_test.go`, `handlers_rooms_test.go`, `handlers_chat_test.go` | 11-й метод интерфейса, стабы моков, spy | 5 |
| `internal/render/render_json.go` (+ `render_test.go`) | `NewMessageIDNullJSON` | 5 |
| `internal/cli/handlers_chat.go` | флаг `--attach`, `readBody`/`readCaption`/`isCharDevice`, `chatSendAttach` | 5 |
| `internal/cli/handlers_chat_attach_test.go`, `handlers_chat_attach_unix_test.go` | cli-тесты ветки `--attach` | 5 |
| `internal/cli/help.go` (+ `help_test.go`) | флаг, `Short`, пример; каноны | 5 |
| `cmd/nctalk/main_test.go` | e2e и регресс утечки пароля | 5 |
| `internal/client/integration_test.go` | `TestIntegration_SendFile` | 6 |
| `README.md`, `CLAUDE.md`, `docs/integration-run.md`, `~/.claude/skills/nctalk/SKILL.md` (вне git) | документация | 7 |

---

### Перед Task 1: ветка

- [ ] **Step 1: Убедиться, что план закоммичен в `main`, и создать ветку**

```sh
git log --oneline -1 main -- docs/superpowers/plans/2026-10-09-nctalk-chat-attach-design.plan.md
git switch -c feat/chat-attach main
git status --short
```

Expected:
- первая команда печатает коммит плана; если вывод пуст — план ещё не в `main`: не начинать и сообщить координатору;
- `Switched to a new branch 'feat/chat-attach'`;
- `git status --short` печатает только ` M TODO.md` — чужая правка, её не трогать.

Если исполнение идёт в git worktree (superpowers:using-git-worktrees), ветку `feat/chat-attach` создать от `main` при создании worktree. Правка `TODO.md` в worktree не переносится — так и должно быть.

---

### Task 1: Живые фикстуры с боевого сервера (curl, ДО тестов)

Спека §7 требует снять фикстуры с живого API ДО тестов. Урок CLAUDE.md: самопальные фикстуры однажды скрыли баг, который всплыл только e2e на боевом.

Что нужно знать до начала:
- **Креды и token.** `NEXTCLOUD_URL`, `NEXTCLOUD_LOGIN`, `NEXTCLOUD_PASS` есть в env. `NCTALK_INTEGRATION_ROOM` в env нет: его token берётся из брифа координатора и подставляется в Step 1 вместо плейсхолдера.
- **Состояние между шагами.** Каждый шаг — отдельный вызов shell, а состояние shell между вызовами не сохраняется. Поэтому путь к каталогу сырья лежит в файле-указателе `${TMPDIR:-/tmp}/nctalk-attach-raw.path`, а общие переменные и функции — в `$RAW/env.sh`. Каждый шаг начинается с их подключения.
- **Сырьё** хранится только в `mktemp -d` с правами 700, в репозиторий не попадает и удаляется в Step 14.
- **Где запускать.** Все команды — из корня рабочей копии. Step 12–13 сами переходят в корень через `git rev-parse --show-toplevel`, поэтому в git worktree фикстуры ложатся в worktree, а не в основную копию.
- **Пароль** не выводить: функции `ncurl`/`ocs` передают креды curl-у через stdin.

**Мутации на боевом:**
- 3 загрузки и 3 шаринга — это 3 сообщения-файла и одно `thread_created`;
- 1 текстовое сообщение-«сосед» (+ его повторы в Step 7);
- 3 `DELETE` — файлы уходят в корзину пользователя.

Остальные запросы читающие или заведомо отклоняемые: PUT с `If-None-Match: *` поверх существующего файла и в несуществующую папку, MKCOL существующей папки, шаринг в несуществующую комнату.

**Загруженные файлы удалить с сервера (Step 10) даже после СТОПа.**

**Files:**
- Create: `testdata/cloud_user.json`
- Create: `testdata/capabilities_attachments.json`
- Create: `testdata/share_create.json`
- Create: `testdata/chat_file_messages.json`
- Create: `testdata/chat_context_message.json`
- Create: `testdata/chat_context_neighbor.json`
- Create: `testdata/chat_context_system.json`
- Create: `testdata/thread_info.json`
- Create: `testdata/thread_not_found.json`
- Create: `testdata/dav_error_409.xml`
- Create: `testdata/dav_error_412.xml`
- Modify (только при расхождении, Step 9): `docs/superpowers/specs/2026-10-09-nctalk-chat-attach-design.md`
- Create (только при расхождении): `docs/superpowers/plans/2026-10-09-nctalk-chat-attach-design.plan.notes.md`

**Interfaces:**
- Consumes:
  - env `NEXTCLOUD_URL`, `NEXTCLOUD_LOGIN`, `NEXTCLOUD_PASS`;
  - token integration-комнаты из брифа;
  - спека §1 (модель), §3.3–§3.6, §7.
- Produces — нормализованные значения, на которые завязаны Task 2–5 (`TestFileFixtures_LiveFormat`, `fakeNC`, `TestDoDAV_SabreMessage`, e2e в `main_test.go`):
  - `cloud_user.json`: `ocs.data` = `{"id": "anna.s", "display-name": "Анна Смирнова"}`.
  - `capabilities_attachments.json`: `ocs.data` = `{"capabilities": {"spreed": {"config": {"attachments": {…}}}}}`, где `allowed: true` и `folder: "/Talk"`; прочие ключи `attachments` — как отдал сервер.
  - `share_create.json`: share-объект с полями:
    - `id` `"3001"`, `share_type` 10;
    - `item_source`/`file_source` 2001, `file_parent` 2000;
    - `share_with` `tok-team`, `share_with_displayname` «Команда»;
    - `path` `/Talk/nctalk-fixture-plain.txt`.
  - `chat_file_messages.json`: ровно 4 своих сообщения в серверном порядке:
    - 1500 — `message` `{file}`, `referenceId` `0…01` (63 нуля + `1`);
    - 1501 — подпись «nctalk attach fixture — подпись», ref `0…02`;
    - 1502 — подпись «nctalk attach fixture — тред», `threadTitle` «Фикстурный тред с файлом», `threadId` 1502, ref `0…03`;
    - 1503 — system `thread_created`, ref `0…03`, `parent.id` 1502.

    У файлов `messageParameters.file` = `{type: "file", id: "2001"…"2003", name: "nctalk-fixture-{plain,caption,thread}.txt", path: "Talk/…", link: "https://nc.example.org/f/200N"}`.
  - context-фикстуры — по одному сообщению: `chat_context_message.json` — 1501 (`isReplyable: true`), `chat_context_neighbor.json` — 1510, «nctalk attach fixture — сосед» (`true`), `chat_context_system.json` — 1503 (`false`).
  - `thread_info.json`: `thread.id` 1502. `thread_not_found.json`: тело HTTP 404 `{"error":"thread"}`.
  - `dav_error_409.xml`: Sabre `Conflict`, `s:message` «Files cannot be created in non-existent collections».
  - `dav_error_412.xml`: Sabre `PreconditionFailed`, `s:message` «An If-None-Match header was specified, but the ETag matched (or * was specified).» и `<s:header>If-None-Match</s:header>`. Тексты `s:message` тесты берут из фикстур, а не из кода.
  - Во всех фикстурах актёр — `anna.s`/«Анна Смирнова», token — `tok-team`, хост — `nc.example.org`.

- [ ] **Step 1: Каталог сырья и хелперы** — вместо плейсхолдера подставить token из брифа; в файлы репозитория token не записывать:

```sh
: "${NEXTCLOUD_URL:?}" "${NEXTCLOUD_LOGIN:?}" "${NEXTCLOUD_PASS:?}"   # проверка наличия, значения не печатаются
export NCTALK_INTEGRATION_ROOM='<token integration-комнаты из брифа координатора>'
RAW=$(mktemp -d) && chmod 700 "$RAW" && printf '%s\n' "$RAW" > "${TMPDIR:-/tmp}/nctalk-attach-raw.path"
printf '%s' "$NCTALK_INTEGRATION_ROOM" > "$RAW/token"
date +%s | tr -d '\n' > "$RAW/ts"
cat > "$RAW/env.sh" <<'ENV'
# env.sh — общие переменные и хелперы захвата Task 1 (подключается в каждом шаге).
BASE=${NEXTCLOUD_URL%/}
OCS="$BASE/ocs/v2.php"
API="$OCS/apps/spreed/api/v1"
TOKEN=$(cat "$RAW/token")
TS=$(cat "$RAW/ts")
# Креды — только через stdin-конфиг curl: пароль не попадает в argv/ps.
ncurl() { printf 'user = "%s:%s"\n' "$NEXTCLOUD_LOGIN" "$NEXTCLOUD_PASS" | curl -sS -K - "$@"; }
ocs() { ncurl -H 'OCS-APIRequest: true' -H 'Accept: application/json' "$@"; }
# davu <путь от корня пользователя> — URL WebDAV; uid и каждый сегмент percent-кодированы.
davu() {
  jq -rn --rawfile u "$RAW/uid" --arg p "$1" --arg b "$BASE" \
    '$b + "/remote.php/dav/files/" + ($u | rtrimstr("\n") | @uri) + ($p | split("/") | map(@uri) | join("/"))'
}
ENV
ls "$RAW"
```

Expected: `env.sh`, `token`, `ts` (листинг `$RAW`). Если какая-то `NEXTCLOUD_*` не задана, shell сообщает `NEXTCLOUD_…: parameter not set` (zsh) или `parameter null or not set` (bash) и шаг прерывается; значения не печатаются.

- [ ] **Step 2: uid и настройки вложений** (§3.1, §3.2)

```sh
RAW=$(cat "${TMPDIR:-/tmp}/nctalk-attach-raw.path"); . "$RAW/env.sh"
ocs -o "$RAW/user.json" -w 'cloud/user: %{http_code}\n' "$OCS/cloud/user"
jq -ej '.ocs.data.id | select(type == "string" and . != "")' "$RAW/user.json" > "$RAW/uid" && echo "uid сохранён (не печатаем)"
ocs -o "$RAW/caps.json" -w 'capabilities: %{http_code}\n' "$OCS/cloud/capabilities"
jq -c '.ocs.data.capabilities.spreed.config.attachments' "$RAW/caps.json"
# Папка как у клиента (normalizeAttachmentFolder): пусто → /Talk, без завершающего «/»; корень — пустая строка.
jq -j '.ocs.data.capabilities.spreed.config.attachments.folder | if . == null or . == "" then "/Talk" else sub("/+$"; "") end' \
  "$RAW/caps.json" > "$RAW/folder"
echo "папка вложений: «$(cat "$RAW/folder")»"
```

Expected:
```
cloud/user: 200
uid сохранён (не печатаем)
capabilities: 200
{"allowed":true,"folder":"/Talk",…}        ← прочие ключи attachments — как есть (например "conversation-subfolders":true)
папка вложений: «/Talk»
```
`"allowed":false` или `allowed` нет — СТОП (таблица Step 9, строка 1).

- [ ] **Step 3: Загрузить три файла** (`If-None-Match: *`, `Content-Length` — curl `-T` с обычного файла chunked не шлёт)

```sh
RAW=$(cat "${TMPDIR:-/tmp}/nctalk-attach-raw.path"); . "$RAW/env.sh"
FOLDER=$(cat "$RAW/folder")
for k in plain caption thread; do
  printf 'nctalk attach fixture %s\n' "$k" > "$RAW/f-$k.txt"
  ncurl -o "$RAW/put-$k.out" -w "PUT $k: %{http_code}\n" -T "$RAW/f-$k.txt" \
    -H 'If-None-Match: *' -H 'Content-Type: application/octet-stream' \
    "$(davu "$FOLDER/nctalk-fixture-$TS-$k.txt")"
done
```

Expected: `PUT plain: 201`, `PUT caption: 201`, `PUT thread: 201`.

Если `409` — папки вложений нет: классический путь её не создаёт (§1). Создать её и повторить Step 3:

```sh
RAW=$(cat "${TMPDIR:-/tmp}/nctalk-attach-raw.path"); . "$RAW/env.sh"
ncurl -X MKCOL -o /dev/null -w 'MKCOL: %{http_code}\n' "$(davu "$(cat "$RAW/folder")")"
```

Expected: `MKCOL: 201`. Это ожидаемая ситуация, а не расхождение: факт записать в заметки (Task 1).

- [ ] **Step 4: Три шаринга** (§3.5): без подписи, с подписью, с подписью + `threadTitle`; `referenceId` — 64 hex из `/dev/urandom`

```sh
RAW=$(cat "${TMPDIR:-/tmp}/nctalk-attach-raw.path"); . "$RAW/env.sh"
FOLDER=$(cat "$RAW/folder")
for k in plain caption thread; do od -An -tx1 -N32 /dev/urandom | tr -d ' \n' > "$RAW/ref-$k"; done
mk() {
  jq -n --arg tok "$TOKEN" --arg p "$FOLDER/nctalk-fixture-$TS-$1.txt" --rawfile r "$RAW/ref-$1" --argjson m "$2" \
    '{shareType: 10, shareWith: $tok, path: $p, referenceId: $r, talkMetaData: ($m | tojson)}' > "$RAW/share-req-$1.json"
}
mk plain '{}'
mk caption '{"caption": "nctalk attach fixture — подпись"}'
mk thread '{"caption": "nctalk attach fixture — тред", "threadTitle": "Фикстурный тред с файлом"}'
for k in plain caption thread; do
  ocs -X POST -H 'Content-Type: application/json' --data-binary "@$RAW/share-req-$k.json" \
    -o "$RAW/share-$k.json" -w "share $k: %{http_code}\n" "$OCS/apps/files_sharing/api/v1/shares"
done
jq -c '.ocs.meta | {statuscode, message}' "$RAW/share-plain.json" "$RAW/share-caption.json" "$RAW/share-thread.json"
```

Expected: `share plain: 200`, `share caption: 200`, `share thread: 200`, затем трижды `{"statuscode":200,"message":"OK"}`. Если шаринг отверг JSON-тело (400/415) — строка 7 таблицы Step 9.

- [ ] **Step 5: Страница chat и id своих сообщений** (§3.6: кандидаты — `referenceId` + `messageType != "system"`; `thread_created` — по `parent.id`)

```sh
RAW=$(cat "${TMPDIR:-/tmp}/nctalk-attach-raw.path"); . "$RAW/env.sh"
ocs -o "$RAW/chat.json" -w 'chat: %{http_code}\n' "$API/chat/$TOKEN?limit=50&lookIntoFuture=0&setReadMarker=0"
for k in plain caption thread; do
  jq --rawfile r "$RAW/ref-$k" '[.ocs.data[] | select(.referenceId == $r and .messageType != "system") | .id] | max' \
    "$RAW/chat.json" > "$RAW/id-$k"
done
jq --slurpfile m "$RAW/id-thread" \
  '[.ocs.data[] | select(.systemMessage == "thread_created" and .parent.id? == $m[0]) | .id] | max' \
  "$RAW/chat.json" > "$RAW/id-s3"
echo "M1=$(cat "$RAW/id-plain") M2=$(cat "$RAW/id-caption") M3=$(cat "$RAW/id-thread") S3=$(cat "$RAW/id-s3")"
```

Expected: `chat: 200` и строка `M1=<id> M2=<id> M3=<id> S3=<id>` — четыре числа, `M1 < M2 < M3 < S3`. Если вместо числа `null` — строки 9 и 10 таблицы Step 9.

- [ ] **Step 6: context, threads, статусы WebDAV, шаринг в несуществующую комнату** (§3.3, §3.4, §3.5)

```sh
RAW=$(cat "${TMPDIR:-/tmp}/nctalk-attach-raw.path"); . "$RAW/env.sh"
FOLDER=$(cat "$RAW/folder")
ocs -o "$RAW/ctx-msg.json" -w 'context M2: %{http_code}\n' "$API/chat/$TOKEN/$(cat "$RAW/id-caption")/context?limit=0"
ocs -o "$RAW/ctx-sys.json" -w 'context S3: %{http_code}\n' "$API/chat/$TOKEN/$(cat "$RAW/id-s3")/context?limit=0"
ocs -o "$RAW/thread-200.json" -w 'thread M3: %{http_code}\n' "$API/chat/$TOKEN/threads/$(cat "$RAW/id-thread")"
ocs -o "$RAW/thread-404.json" -w 'thread 2147483647: %{http_code}\n' "$API/chat/$TOKEN/threads/2147483647"
ncurl -o "$RAW/dav-409.xml" -w 'PUT в несуществующую папку: %{http_code}\n' -T "$RAW/f-plain.txt" \
  -H 'If-None-Match: *' -H 'Content-Type: application/octet-stream' "$(davu "/nctalk-fixture-missing-$TS/x.txt")"
ncurl -o "$RAW/dav-412.xml" -w 'PUT поверх существующего: %{http_code}\n' -T "$RAW/f-plain.txt" \
  -H 'If-None-Match: *' -H 'Content-Type: application/octet-stream' "$(davu "$FOLDER/nctalk-fixture-$TS-plain.txt")"
ncurl -I -o /dev/null -w 'HEAD существующего: %{http_code}\n' "$(davu "$FOLDER/nctalk-fixture-$TS-plain.txt")"
ncurl -I -o /dev/null -w 'HEAD отсутствующего: %{http_code}\n' "$(davu "$FOLDER/nctalk-fixture-$TS-absent.txt")"
ncurl -X MKCOL -o /dev/null -w 'MKCOL существующей папки: %{http_code}\n' "$(davu "$FOLDER")"
jq '.shareWith = "nctalk-no-such-room"' "$RAW/share-req-plain.json" > "$RAW/share-req-404.json"
ocs -X POST -H 'Content-Type: application/json' --data-binary "@$RAW/share-req-404.json" \
  -o "$RAW/share-404.json" -w 'share в несуществующую комнату: %{http_code}\n' "$OCS/apps/files_sharing/api/v1/shares"
jq -c '.ocs.meta | {statuscode, message}' "$RAW/share-404.json"
```

Expected:
```
context M2: 200
context S3: 200
thread M3: 200
thread 2147483647: 404
PUT в несуществующую папку: 409
PUT поверх существующего: 412
HEAD существующего: 200
HEAD отсутствующего: 404
MKCOL существующей папки: 405
share в несуществующую комнату: 404
{"statuscode":404,"message":"Conversation not found"}      ← текст может быть локализован
```
Любое другое значение — строки 3–6, 8 и 18 таблицы Step 9.

- [ ] **Step 7: «Сосед» — своё сообщение и context несуществующего id** (§3.3: при `limit=0` сервер отдаёт ближайшее сообщение с id ≤ запрошенного). Затем собрать `ids.json`.

```sh
RAW=$(cat "${TMPDIR:-/tmp}/nctalk-attach-raw.path"); . "$RAW/env.sh"
jq -n '{message: "nctalk attach fixture — сосед"}' > "$RAW/t-req.json"
ocs -X POST -H 'Content-Type: application/json' --data-binary "@$RAW/t-req.json" \
  -o "$RAW/t.json" -w 'POST T: %{http_code}\n' "$API/chat/$TOKEN"
jq '.ocs.data.id' "$RAW/t.json" > "$RAW/id-t"
ocs -o "$RAW/ctx-neighbor.json" -w 'context 2147483647: %{http_code}\n' "$API/chat/$TOKEN/2147483647/context?limit=0"
jq -c --slurpfile t "$RAW/id-t" '[.ocs.data[] | {id, isReplyable}] | {neighbor: ., ours: (map(.id) == $t)}' "$RAW/ctx-neighbor.json"
jq -n --slurpfile a "$RAW/id-plain" --slurpfile b "$RAW/id-caption" --slurpfile c "$RAW/id-thread" \
      --slurpfile d "$RAW/id-s3" --slurpfile e "$RAW/id-t" \
  '{plain: $a[0], caption: $b[0], thread: $c[0], s3: $d[0], t: $e[0]}' > "$RAW/ids.json"
jq -c '{ids_ok: (map(type == "number") | all)}' "$RAW/ids.json"
```

Expected:
```
POST T: 201
context 2147483647: 200
{"neighbor":[{"id":<T>,"isReplyable":true}],"ours":true}
{"ids_ok":true}
```
- `"ours":false` — после T в общую комнату кто-то написал: повторить Step 7. Каждый повтор оставляет в комнате ещё одно своё сообщение, это допустимо.
- `context 2147483647: 404` — строка 16 таблицы Step 9.

- [ ] **Step 8: Сверка формата со спекой** (§1 «Модель», §3.3, §3.4; урок CLAUDE.md про формат `messageParameters`)

```sh
RAW=$(cat "${TMPDIR:-/tmp}/nctalk-attach-raw.path"); . "$RAW/env.sh"
# 1) Сообщения-файлы и thread_created (§1 модель, §3.6): тип, message, формат messageParameters, referenceId.
jq -c --slurpfile ids "$RAW/ids.json" '$ids[0] as $i | .ocs.data[]
  | select(.id | IN($i.plain, $i.caption, $i.thread, $i.s3))
  | {id, messageType, systemMessage, message, params: (.messageParameters | type),
     file: (.messageParameters | if type == "object" and .file then .file | {type, id: (.id | type)} else null end),
     ref: .referenceId[0:8], threadId, threadTitle, parent: (.parent | if type == "object" then .id else null end)}' "$RAW/chat.json"
jq -c --slurpfile ids "$RAW/ids.json" --rawfile r1 "$RAW/ref-plain" --rawfile r2 "$RAW/ref-caption" --rawfile r3 "$RAW/ref-thread" '
  $ids[0] as $i | (.ocs.data | map({key: (.id | tostring), value: .}) | from_entries) as $m
  | $m[$i.plain | tostring] as $p | $m[$i.caption | tostring] as $c | $m[$i.thread | tostring] as $t | $m[$i.s3 | tostring] as $s
  | {plain: ($p.messageType == "comment" and $p.systemMessage == "" and $p.message == "{file}"
             and $p.referenceId == $r1 and $p.messageParameters.file.type == "file"),
     caption: ($c.message == "nctalk attach fixture — подпись" and $c.referenceId == $r2 and $c.messageParameters.file.type == "file"),
     thread: ($t.message == "nctalk attach fixture — тред" and $t.referenceId == $r3
              and $t.threadTitle == "Фикстурный тред с файлом" and $t.threadId == $i.thread),
     thread_created: ($s.messageType == "system" and $s.systemMessage == "thread_created"
                      and $s.referenceId == $r3 and $s.parent.id == $i.thread)}' "$RAW/chat.json"
# 2) context?limit=0 (§3.3): одно сообщение — M2 replyable, S3 не replyable, сосед T.
for f in ctx-msg ctx-sys ctx-neighbor; do
  jq -c --arg f "$f" '{($f): [.ocs.meta.statuscode, [.ocs.data[]? | {id, isReplyable}]]}' "$RAW/$f.json"
done
# 3) threads/{id} (§3.3): 200 с thread.id = M3; 404 {"error":"thread"}.
jq -c '[.ocs.meta.statuscode, .ocs.data.thread.id?, .ocs.data.error?]' "$RAW/thread-200.json" "$RAW/thread-404.json"
# 4) XML-ошибки Sabre (§3.4): s:message.
grep -ho '<s:message>[^<]*' "$RAW/dav-409.xml" "$RAW/dav-412.xml"
```

Expected (id — фактические, порядок строк — серверный, новые сверху; текст system-сообщения может быть локализован):
```
{"id":<S3>,"messageType":"system","systemMessage":"thread_created",…,"params":"object","file":null,"ref":"<8 hex M3>","threadId":<M3>,…,"parent":<M3>}
{"id":<M3>,"messageType":"comment","systemMessage":"","message":"nctalk attach fixture — тред","params":"object","file":{"type":"file","id":"string"},…,"threadId":<M3>,"threadTitle":"Фикстурный тред с файлом","parent":null}
{"id":<M2>,"messageType":"comment","systemMessage":"","message":"nctalk attach fixture — подпись","params":"object","file":{"type":"file","id":"string"},…}
{"id":<M1>,"messageType":"comment","systemMessage":"","message":"{file}","params":"object","file":{"type":"file","id":"string"},…}
{"plain":true,"caption":true,"thread":true,"thread_created":true}
{"ctx-msg":[200,[{"id":<M2>,"isReplyable":true}]]}
{"ctx-sys":[200,[{"id":<S3>,"isReplyable":false}]]}
{"ctx-neighbor":[200,[{"id":<T>,"isReplyable":true}]]}
[200,<M3>,null]
[404,null,"thread"]
<s:message>Files cannot be created in non-existent collections
<s:message>An If-None-Match header was specified, but the ETag matched (or * was specified).
```
Ключевая строка — `{"plain":true,"caption":true,"thread":true,"thread_created":true}`. Любой `false` и любое расхождение выше — к таблице Step 9.

- [ ] **Step 9: Если сверка Step 2–8 расходится с ожидаемым**

Живой API — истина. Для каждого расхождения:
- выполнить действие из таблицы;
- поправить пункт спеки с пометкой «(уточнено по живому API <дата>)»;
- записать в заметки `## [Task 1] …` (`Что:` — наблюдение, `Почему:` — действие).

Заметку и спеку коммитить вместе с фикстурами (Step 15). Код, который появится в задачах 2–6, адаптировать в той задаче, где он создаётся: столбец «Где». Код задачи до адаптации — verbatim из плана.

| № | Наблюдение (шаг) | Действие | Где |
|---|---|---|---|
| 1 | `allowed` ≠ `true` (Step 2) | **СТОП**: вложения выключены на сервере, фича невыполнима. Ничего не загружено — сразу Step 14, отчёт координатору | — |
| 2 | PUT в папку вложений = 409 (Step 3) | ожидаемо (§1): MKCOL из Step 3, повтор Step 3, факт — в заметки | — |
| 3 | PUT поверх существующего ≠ 412, файл перезаписан (Step 6) | **СТОП**: гарантия «существующие файлы не перезаписываются» (§2.3) невыполнима. Step 10, Step 14, отчёт | — |
| 4 | PUT в несуществующую папку = 404, не 409 (Step 6) | `dav_error_409.xml` — тело этого 404 (имя фикстуры то же). В `uploadFile` 404 обрабатывать как 409 (MKCOL + повтор) + кейс в `TestSendFile_MissingFolder`; спека §3.4 | Task 4 |
| 5 | PUT в несуществующую папку = 201: сервер сам создал папку (Step 6) | В Step 10 дополнительно удалить папку `/nctalk-fixture-missing-<TS>` (`ncurl -X DELETE "$(davu "/nctalk-fixture-missing-$TS")"`). XML 409 снять с `MKCOL` вложенного несуществующего пути `…/nctalk-fixture-missing2-<TS>/a/b`; спека §3.4 | Task 1 |
| 6 | HEAD существующего ≠ 200, отсутствующего ≠ 404 или MKCOL существующей ≠ 405 (Step 6) | статусы в `uploadFile`/`davMkcol` и кейсы `TestSendFile_UniqueName`/`TestSendFile_MissingFolder` — по факту; спека §3.4 | Task 4 |
| 7 | шаринг 400/415: тело не JSON (Step 4) | В `shareToRoom` слать `application/x-www-form-urlencoded` (`url.Values` с теми же ключами), в `TestSendFile_HappyPath`/`shareBody` разбирать форму; спека §3.5. Step 4 повторить с `--data-urlencode` | Task 4 |
| 8 | шаринг в несуществующую комнату ≠ 404 (Step 6) | код не менять (не-404 → exit 1); в спеке §2.6/§3.5 уточнить фактический код | Task 1 |
| 9 | `M1`/`M2`/`M3` = `null` (Step 5): у сообщения-файла нет `referenceId` | В `findFileMessage` искать по `messageParameters.file.path == strings.TrimPrefix(sharePath, "/")` (+ `messageType != "system"`, max id). id в Step 5 брать по имени файла; кейсы поиска в `files_test.go` — по пути; спека §3.6 | Task 4 |
| 10 | `S3` = `null`, но у `M3` есть `threadTitle`: `thread_created` без того же `referenceId` | Фильтр system оставить. `S3` взять по `parent.id`, как в Step 5, или из `threads/{M3}`; в `TestFileFixtures_LiveFormat` убрать проверку ref у 1503; спека §1 | Task 4 |
| 11 | тред из файла не создаётся: у `M3` нет `threadTitle`, `thread M3` = 404 | `--attach --thread-title` → exit 1 до сети (guard в cli и `validateSendFile`) + тесты; спека §2.4. System-сообщение для `chat_context_system.json` — своё: текстовый тред `POST chat/{token}` с `threadTitle`, его `thread_created` | Task 4/5 |
| 12 | `message`, `messageType` или подпись не такие (Step 8: `plain`/`caption`/`thread` = `false`) | `TestFileFixtures_LiveFormat`, ассерты `TestIntegration_SendFile` и спеку §1 — по факту | Task 4/6 |
| 13 | `params` = `"array"` у сообщения-файла (Step 8) | Непустой массив — формат `/v4/room`, `MsgParams` его принимает. Проверку `[{` в `TestFileFixtures_LiveFormat` — по факту; спека §1, CLAUDE.md (Task 7) | Task 4/7 |
| 14 | `file.id` — число, не строка (Step 8: `"id":"number"`) | `MsgParam.Id` (`types.go`) должен принимать и число, и строку: кастомный `UnmarshalJSON` + тест; спека §1 | Task 4 |
| 15 | `context?limit=0` вернул больше одного сообщения (Step 8) | в фикстурах — только свои; `TestFileFixtures_LiveFormat` — на фактическое число; сверка id в `checkReplyable` не меняется; спека §3.3 | Task 4 |
| 16 | context несуществующего id = 404, а не сосед (Step 7) | `chat_context_neighbor.json` — тело этого 404; `checkReplyable` уже возвращает `*OCSError{404}`; `TestSendFile_PrecheckReply` и `TestFileFixtures_LiveFormat` — по факту; спека §3.3 | Task 4 |
| 17 | `"ours":false` (Step 7) | сосед чужой: повторить Step 7 | — |
| 18 | `thread 2147483647` ≠ 404 (Step 6) | `checkThreadExists` — по факту; спека §3.3 | Task 4 |
| 19 | папка вложений ≠ `/Talk` (Step 2) | обрабатывается автоматически: пары подстрок в Step 11 и `folder` → `/Talk` в Step 12. Факт — в заметки | — |

Команды к строке 5 — XML 409 из MKCOL вложенного пути и удаление созданной сервером папки:

```sh
RAW=$(cat "${TMPDIR:-/tmp}/nctalk-attach-raw.path"); . "$RAW/env.sh"
ncurl -X MKCOL -o "$RAW/dav-409.xml" -w 'MKCOL вложенного: %{http_code}\n' "$(davu "/nctalk-fixture-missing2-$TS/a/b")"
ncurl -X DELETE -o /dev/null -w 'DELETE папки: %{http_code}\n' "$(davu "/nctalk-fixture-missing-$TS")"
```

Expected: `MKCOL вложенного: 409`, `DELETE папки: 204`.

Команды к строке 7 — повтор Step 4 form-телом с теми же `referenceId`:

```sh
RAW=$(cat "${TMPDIR:-/tmp}/nctalk-attach-raw.path"); . "$RAW/env.sh"
for k in plain caption thread; do
  ocs -X POST --data-urlencode "shareType=10" --data-urlencode "shareWith=$TOKEN" \
    --data-urlencode "path=$(jq -r .path "$RAW/share-req-$k.json")" \
    --data-urlencode "referenceId=$(jq -r .referenceId "$RAW/share-req-$k.json")" \
    --data-urlencode "talkMetaData=$(jq -r .talkMetaData "$RAW/share-req-$k.json")" \
    -o "$RAW/share-$k.json" -w "share $k: %{http_code}\n" "$OCS/apps/files_sharing/api/v1/shares"
done
```

Expected: `share plain: 200`, `share caption: 200`, `share thread: 200`.

Команда к строке 9 — id сообщений-файлов по пути файла вместо `referenceId` (заменяет первую половину Step 5; `S3` ищется, как в Step 5, по `parent.id`):

```sh
RAW=$(cat "${TMPDIR:-/tmp}/nctalk-attach-raw.path"); . "$RAW/env.sh"
FOLDER=$(cat "$RAW/folder")
for k in plain caption thread; do
  jq --arg p "${FOLDER#/}${FOLDER:+/}nctalk-fixture-$TS-$k.txt" '[.ocs.data[]
      | select(.messageType != "system" and ((.messageParameters | if type == "object" then .file.path? else null end) == $p))
      | .id] | max' "$RAW/chat.json" > "$RAW/id-$k"
done
echo "M1=$(cat "$RAW/id-plain") M2=$(cat "$RAW/id-caption") M3=$(cat "$RAW/id-thread")"
```

Expected: `M1=<id> M2=<id> M3=<id>` — три числа.

- [ ] **Step 10: Удалить загруженные файлы с сервера** (выполнять и после СТОПа, если Step 3 что-то загрузил)

```sh
RAW=$(cat "${TMPDIR:-/tmp}/nctalk-attach-raw.path"); . "$RAW/env.sh"
FOLDER=$(cat "$RAW/folder")
for k in plain caption thread; do
  ncurl -X DELETE -o /dev/null -w "DELETE $k: %{http_code}\n" "$(davu "$FOLDER/nctalk-fixture-$TS-$k.txt")"
done
ncurl -I -o /dev/null -w 'HEAD plain после DELETE: %{http_code}\n' "$(davu "$FOLDER/nctalk-fixture-$TS-plain.txt")"
```

Expected: `DELETE plain: 204`, `DELETE caption: 204`, `DELETE thread: 204`, `HEAD plain после DELETE: 404`. Файлы уходят в корзину пользователя; сообщения в комнате остаются с «файл больше недоступен».

- [ ] **Step 11: Карты обезличивания** (`norm.jq`, карта id `im.json`, пары подстрок `sm.json`; всё — в `$RAW`)

```sh
RAW=$(cat "${TMPDIR:-/tmp}/nctalk-attach-raw.path"); . "$RAW/env.sh"
cat > "$RAW/norm.jq" <<'JQ'
# norm.jq — обезличивание ответа API. $sm[0] — пары подстрок [сырое, обезличенное]
# (порядок важен), $im[0] — карта {"<сырой id>": <id фикстуры>} для числовых
# id-полей и строк, целиком равных сырому id. Порядок ключей сохраняется
# (walk из jq 1.6 сортирует ключи — свой обход на keys_unsorted).
def walk_ordered(f): . as $in
  | if type == "object" then reduce keys_unsorted[] as $k ({}; . + {($k): ($in[$k] | walk_ordered(f))}) | f
    elif type == "array" then map(walk_ordered(f)) | f
    else f end;
def subst: reduce $sm[0][] as $p (.; split($p[0]) | join($p[1]));
def idkey: IN("id", "threadId", "lastMessageId", "item_source", "file_source", "file_parent");
walk_ordered(
  if type == "object" then
    with_entries(
      if (.key | idkey) and (.value | type) == "number" and ($im[0][.value | tostring] != null)
      then .value = $im[0][.value | tostring] else . end)
  elif type == "string" then
    (if $im[0][.] != null then ($im[0][.] | tostring) else . end) | subst
  else . end)
JQ
# Карта id: сообщения → 1500…1503/1510, файлы → 2001…2003, папка → 2000, шар → 3001.
jq -n --slurpfile ids "$RAW/ids.json" --slurpfile chat "$RAW/chat.json" --slurpfile share "$RAW/share-plain.json" '
  $ids[0] as $i | $chat[0].ocs.data as $d
  | def fid($id): $d | map(select(.id == $id)) | .[0].messageParameters.file.id | tostring;
  {
    ($i.plain | tostring): 1500, ($i.caption | tostring): 1501, ($i.thread | tostring): 1502,
    ($i.s3 | tostring): 1503, ($i.t | tostring): 1510,
    (fid($i.plain)): 2001, (fid($i.caption)): 2002, (fid($i.thread)): 2003,
    ($share[0].ocs.data.file_parent | tostring): 2000,
    ($share[0].ocs.data.id | tostring): 3001
  }' > "$RAW/im.json"
jq -c '{im_keys: length}' "$RAW/im.json"
# Пары подстрок (порядок важен: длинные и специфичные — раньше; uid и token — последними).
jq -n --rawfile uid "$RAW/uid" --rawfile tok "$RAW/token" --rawfile ts "$RAW/ts" --rawfile fd "$RAW/folder" \
      --rawfile r1 "$RAW/ref-plain" --rawfile r2 "$RAW/ref-caption" --rawfile r3 "$RAW/ref-thread" \
      --slurpfile user "$RAW/user.json" --slurpfile share "$RAW/share-plain.json" \
      --slurpfile ids "$RAW/ids.json" --slurpfile im "$RAW/im.json" '
  ($uid | rtrimstr("\n")) as $u | ($tok | rtrimstr("\n")) as $t | ($ts | rtrimstr("\n")) as $s
  | ($ENV.NEXTCLOUD_URL | sub("/+$"; "")) as $base | ($base | sub("^https?://"; "")) as $host
  | ($im[0] | to_entries | map(select(.value >= 2001 and .value <= 2003)) | map(["/f/" + .key, "/f/" + (.value | tostring)])) as $links
  | [ [$base, "https://nc.example.org"], [$host, "nc.example.org"] ]
    + (if $ENV.NEXTCLOUD_LOGIN != $u then [[$ENV.NEXTCLOUD_LOGIN, "anna.s@example.org"]] else [] end)
    + [ [$user[0].ocs.data["display-name"], "Анна Смирнова"],
        [$share[0].ocs.data.share_with_displayname, "Команда"],
        [$r1, ("0" * 63 + "1")], [$r2, ("0" * 63 + "2")], [$r3, ("0" * 63 + "3")],
        ["thread/\($ids[0].thread)", "thread/1502"],
        ["-\($s)-", "-"], ["-\($s)", ""] ]
    + (if $fd != "/Talk" and $fd != "" then [[$fd, "/Talk"], [($fd | ltrimstr("/")), "Talk"]] else [] end)
    + $links
    + [ [$u, "anna.s"], [$t, "tok-team"] ]
  | map(select(.[0] != null and .[0] != ""))' > "$RAW/sm.json"
jq -c '{sm_pairs: length, targets: map(.[1])}' "$RAW/sm.json"
```

Expected:
- `{"im_keys":10}`. Меньше 10 — сырые id разных типов совпали (например, id шара = id файла). Тогда развести пары вручную в `$RAW/im.json` и повторить шаг с `jq -c '{sm_pairs…'`.
- Строка `{"sm_pairs":16,"targets":["https://nc.example.org","nc.example.org","anna.s@example.org","Анна Смирнова","Команда","000…001","000…002","000…003","thread/1502","-","","/f/2001","/f/2002","/f/2003","anna.s","tok-team"]}`:
  - пар 15, если логин совпадает с uid (пары логина нет);
  - пар 18, если папка не `/Talk` (добавлены `"/Talk"`, `"Talk"`).

Порядок пар важен: URL и хост — первыми, uid и token — последними. Если uid короче 4 символов, подстрочная замена опасна: после Step 12 просмотреть фикстуры глазами на искажения.

- [ ] **Step 12: Нормализация в `testdata/`**

```sh
cd "$(git rev-parse --show-toplevel)"
RAW=$(cat "${TMPDIR:-/tmp}/nctalk-attach-raw.path"); . "$RAW/env.sh"
N() { jq --slurpfile sm "$RAW/sm.json" --slurpfile im "$RAW/im.json" -f "$RAW/norm.jq" "$@"; }
# cloud/user и capabilities — усечённые: только нужные клиенту поля (решение плана №15).
jq '.ocs.data |= {id: .id, "display-name": .["display-name"]}' "$RAW/user.json" | N > testdata/cloud_user.json
# Нестандартное имя папки вложений обезличивается в /Talk (пустое/отсутствующее — как есть).
jq '.ocs.data |= {capabilities: {spreed: {config: {attachments: .capabilities.spreed.config.attachments}}}}
    | .ocs.data.capabilities.spreed.config.attachments.folder |= (if . == null or . == "" then . else "/Talk" end)' \
  "$RAW/caps.json" | N > testdata/capabilities_attachments.json
N "$RAW/share-plain.json" > testdata/share_create.json
# Страница chat — только свои четыре сообщения (комната общая с другими агентами).
jq --slurpfile ids "$RAW/ids.json" '$ids[0] as $i | .ocs.data |= map(select(.id | IN($i.plain, $i.caption, $i.thread, $i.s3)))' \
  "$RAW/chat.json" | N > testdata/chat_file_messages.json
N "$RAW/ctx-msg.json" > testdata/chat_context_message.json
N "$RAW/ctx-neighbor.json" > testdata/chat_context_neighbor.json
N "$RAW/ctx-sys.json" > testdata/chat_context_system.json
N "$RAW/thread-200.json" > testdata/thread_info.json
N "$RAW/thread-404.json" > testdata/thread_not_found.json
for c in 409 412; do
  jq -Rr --slurpfile sm "$RAW/sm.json" 'reduce $sm[0][] as $p (.; split($p[0]) | join($p[1]))' "$RAW/dav-$c.xml" \
    > "testdata/dav_error_$c.xml"
done
ls -l testdata/cloud_user.json testdata/capabilities_attachments.json testdata/share_create.json \
  testdata/chat_file_messages.json testdata/chat_context_message.json testdata/chat_context_neighbor.json \
  testdata/chat_context_system.json testdata/thread_info.json testdata/thread_not_found.json \
  testdata/dav_error_409.xml testdata/dav_error_412.xml | awk '{print $5, $9}'
```

Expected: 11 строк `<размер> testdata/<имя>` (порядок — как у `ls`); пустой файл (размер 0) — ошибка jq выше по выводу.

- [ ] **Step 13: Проверка фикстур — валидность, утечки, только свои данные**

```sh
cd "$(git rev-parse --show-toplevel)"
RAW=$(cat "${TMPDIR:-/tmp}/nctalk-attach-raw.path"); . "$RAW/env.sh"
J=(testdata/cloud_user.json testdata/capabilities_attachments.json testdata/share_create.json
   testdata/chat_file_messages.json testdata/chat_context_message.json testdata/chat_context_neighbor.json
   testdata/chat_context_system.json testdata/thread_info.json testdata/thread_not_found.json)
X=(testdata/dav_error_409.xml testdata/dav_error_412.xml)
jq empty "${J[@]}" && echo "json: OK"
# Утечки: хост, логин, uid, token, метка времени, имя пользователя, имя комнаты.
jq -rn --rawfile uid "$RAW/uid" --rawfile tok "$RAW/token" --rawfile ts "$RAW/ts" \
       --slurpfile user "$RAW/user.json" --slurpfile share "$RAW/share-plain.json" '
  [ ($ENV.NEXTCLOUD_URL | sub("^https?://"; "") | sub("[/:].*$"; "")), $ENV.NEXTCLOUD_LOGIN,
    ($uid | rtrimstr("\n")), ($tok | rtrimstr("\n")), ("-" + ($ts | rtrimstr("\n"))),
    $user[0].ocs.data["display-name"], $share[0].ocs.data.share_with_displayname ]
  | .[] | select(. != null and . != "")' > "$RAW/leak.txt"
grep -lFf "$RAW/leak.txt" "${J[@]}" "${X[@]}" || echo "leak: clean"
# Все URL — только заглушка и пространство имён Sabre.
grep -ohE 'https?://[^"/ <]+' "${J[@]}" "${X[@]}" | sort -u
jq -c '[.ocs.data[].id] | sort' testdata/chat_file_messages.json
jq -c '[.. | objects | .actorId? // empty] | unique' testdata/chat_file_messages.json testdata/chat_context_message.json \
  testdata/chat_context_neighbor.json testdata/chat_context_system.json testdata/thread_info.json
jq -c '[.ocs.data[] | .referenceId] | unique | map(.[60:])' testdata/chat_file_messages.json
```

Expected:
```
json: OK
leak: clean
http://sabredav.org
https://nc.example.org
[1500,1501,1502,1503]
["anna.s"]
["anna.s"]
["anna.s"]
["anna.s"]
["anna.s"]
["0001","0002","0003"]
```
Если вывод другой:
- вместо `leak: clean` — имена файлов: в них осталось сырое значение. Добавить пару в конец карты подстрок и повторить Step 12–13:
  `RAW=$(cat "${TMPDIR:-/tmp}/nctalk-attach-raw.path"); jq --arg raw '<сырое>' --arg stub '<заглушка>' '. + [[$raw, $stub]]' "$RAW/sm.json" > "$RAW/sm.tmp" && mv "$RAW/sm.tmp" "$RAW/sm.json"`
  (сырое значение остаётся только в команде и в `$RAW`);
- лишний URL — то же самое;
- чужой `actorId` — фильтр «только свои» не сработал: повторить Step 12.

Затем просмотреть каждую фикстуру глазами (`cat testdata/<имя>`): не должно остаться персональных данных, которые не покрыты картой, — имён других людей, e-mail, названий папок. Структуру и типы полей не трогать.

- [ ] **Step 14: Удалить сырьё**

```sh
rm -rf "$(cat "${TMPDIR:-/tmp}/nctalk-attach-raw.path")" "${TMPDIR:-/tmp}/nctalk-attach-raw.path" && echo "сырьё удалено"
```

Expected: `сырьё удалено`.

- [ ] **Step 15: Гейт и коммит** (код не менялся — гейт подтверждает, что фикстуры ничего не сломали)

```sh
CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./...
git status --short
```

Expected:
- гейт зелёный;
- в `git status` — ` M TODO.md` и 11 новых `?? testdata/…`, плюс спека и заметки, если были расхождения.

```sh
git add testdata/cloud_user.json testdata/capabilities_attachments.json testdata/share_create.json \
  testdata/chat_file_messages.json testdata/chat_context_message.json testdata/chat_context_neighbor.json testdata/chat_context_system.json \
  testdata/thread_info.json testdata/thread_not_found.json testdata/dav_error_409.xml testdata/dav_error_412.xml
# только при расхождениях (Step 9) — добавить:
#   docs/superpowers/specs/2026-10-09-nctalk-chat-attach-design.md docs/superpowers/plans/2026-10-09-nctalk-chat-attach-design.plan.notes.md
git commit -m "test(testdata): живые фикстуры отправки файлов (chat send --attach)

Сняты curl-ом с боевого сервера до тестов (спека-дельта 2026-10-09 §7):
cloud/user и capabilities (усечены до нужных полей), ответ шаринга,
страница chat с сообщениями-файлами (без подписи, с подписью, с
threadTitle) и thread_created того же referenceId, context?limit=0
(сообщение, сосед, system), threads/{id} 200/404, XML-ошибки Sabre
409/412. Обезличены: tok-team, anna.s, фиксированные id и referenceId."
```

---

### Task 2: transport — WebDAV-транспорт `DoDAV`

**Files:**
- Create: `internal/transport/dav.go`
- Test: `internal/transport/dav_test.go` (новый)

**Interfaces:**
- Consumes:
  - `transport.Doer`, `transport.Auth{BaseURL *url.URL; Login, Password string}`, `SanitizeErr(error) error`, `SameHostRedirectPolicy` — всё из `internal/transport/transport.go`;
  - фикстуры Task 1 `testdata/dav_error_409.xml` и `testdata/dav_error_412.xml`.
- Produces (Task 4 и Task 6 на него завязаны):

```go
// DAVResponse — итог WebDAV-запроса: статус интерпретирует вызывающий.
type DAVResponse struct {
	StatusCode int
	Header     http.Header
	Message    string // s:message из XML-ошибки Sabre (только при StatusCode >= 400)
}

func DoDAV(ctx context.Context, doer Doer, auth Auth, method, p string, header http.Header, body io.Reader, contentLength int64) (*DAVResponse, error)
```

  Контракт:
  - `error` возвращается только при транспортном сбое (сеть, отмена ctx, редирект на чужой хост) и всегда проходит `SanitizeErr`. Любой HTTP-статус — в `DAVResponse`.
  - Путь `p` передаётся НЕэкранированным, сегменты экранирует `url.URL`. Склейка через `path.Join` схлопывает «.»/«..», поэтому сегменты проверяет вызывающий.
  - Тело: `body == nil` — без тела; `contentLength == 0` — `http.NoBody` (`Content-Length: 0`); иначе `io.NopCloser(body)` и явный `req.ContentLength`. Тело `DoDAV` не закрывает, chunked не шлёт.
  - `header` копируется ДО `Authorization`, и `Authorization` перекрывает значение вызывающего.
  - Тело ответа читается не дальше `davBodyLimit` (64 КиБ).

- [ ] **Step 1: Написать падающие тесты** — новый файл `internal/transport/dav_test.go` целиком:

```go
package transport

// dav_test.go — тесты WebDAV-транспорта DoDAV (спека-дельта 2026-10-09 §4.1,
// §7): метод, путь и заголовки, экранирование сегментов, Content-Length без
// chunked, тело не закрывается (повтор PUT), s:message из живых XML-ошибок
// Sabre, лимит чтения тела, SanitizeErr и пароль-маркер.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// davFixture читает живую фикстуру из корневого testdata/ (Task 1).
func davFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// fixtureSabreMessage — текст <s:message> живой фикстуры, вырезанный строковым
// поиском: эталон для сверки парсера, независимый от encoding/xml.
func fixtureSabreMessage(t *testing.T, raw []byte) string {
	t.Helper()
	s := string(raw)
	i := strings.Index(s, "<s:message>")
	j := strings.Index(s, "</s:message>")
	if i < 0 || j < i {
		t.Fatalf("в фикстуре нет <s:message>: %s", s)
	}
	return strings.TrimSpace(s[i+len("<s:message>") : j])
}

// closeCountingReader — ReadSeeker с Close-счётчиком: тип, длину которого
// net/http не выводит, и io.Closer — Transport закрыл бы его, будь он передан
// телом запроса как есть.
type closeCountingReader struct {
	r      io.ReadSeeker
	closes int32
}

func (c *closeCountingReader) Read(p []byte) (int, error) { return c.r.Read(p) }

func (c *closeCountingReader) Seek(off int64, whence int) (int64, error) {
	return c.r.Seek(off, whence)
}

func (c *closeCountingReader) Close() error {
	atomic.AddInt32(&c.closes, 1)
	return nil
}

// TestDoDAV_RequestShape — метод, путь от BaseURL с префиксом, экранирование
// сегментов (пробел, кириллица, '#', '?', '%'), Authorization: Basic поверх
// значения вызывающего, его прочие заголовки; OCS-заголовков у WebDAV нет;
// '?' в имени не превратился в query. HTTP 404 — статус, не ошибка.
func TestDoDAV_RequestShape(t *testing.T) {
	var (
		gotMethod, gotEscaped, gotQuery string
		gotHeader                       http.Header
	)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotEscaped = r.URL.EscapedPath()
		gotQuery = r.URL.RawQuery
		gotHeader = r.Header.Clone()
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	h := http.Header{}
	h.Set("If-None-Match", "*")
	h.Set("Authorization", "Basic подмена")
	resp, err := DoDAV(context.Background(), http.DefaultClient, authFromURL(t, ts.URL+"/nc"),
		http.MethodHead, "/remote.php/dav/files/anna smith/Talk/отчёт #1?%.pdf", h, nil, 0)
	if err != nil {
		t.Fatalf("DoDAV: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("StatusCode: got %d, want 404 (статус отдаётся вызывающему)", resp.StatusCode)
	}
	if gotMethod != http.MethodHead {
		t.Errorf("method: got %q, want HEAD", gotMethod)
	}
	const wantEscaped = "/nc/remote.php/dav/files/anna%20smith/Talk/%D0%BE%D1%82%D1%87%D1%91%D1%82%20%231%3F%25.pdf"
	if gotEscaped != wantEscaped {
		t.Errorf("escaped path:\n got %q\nwant %q", gotEscaped, wantEscaped)
	}
	if gotQuery != "" {
		t.Errorf("RawQuery: got %q, want пусто ('?' — часть имени)", gotQuery)
	}
	if got := gotHeader.Get("Authorization"); got != wantAuthHeader() {
		t.Errorf("Authorization: got %q, want %q", got, wantAuthHeader())
	}
	if got := gotHeader.Get("If-None-Match"); got != "*" {
		t.Errorf("If-None-Match: got %q, want *", got)
	}
	if got := gotHeader.Get("OCS-APIRequest"); got != "" {
		t.Errorf("OCS-APIRequest: got %q, want пусто (у WebDAV нет OCS-заголовков)", got)
	}
}

// TestDoDAV_ContentLengthNoChunked — PUT уходит с Content-Length = размер и
// без chunked: и пустой файл (http.NoBody), и тело неизвестного net/http типа
// (спека §3.4: chunked-тело за php-fpm теряется). Тело не закрыто.
func TestDoDAV_ContentLengthNoChunked(t *testing.T) {
	for _, content := range []string{"", "hello", strings.Repeat("x", 70000)} {
		var (
			gotLen  int64
			gotTE   []string
			gotBody []byte
		)
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotLen = r.ContentLength
			gotTE = r.TransferEncoding
			gotBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusCreated)
		}))
		body := &closeCountingReader{r: strings.NewReader(content)}
		resp, err := DoDAV(context.Background(), http.DefaultClient, authFromURL(t, ts.URL),
			http.MethodPut, "/f.bin", nil, body, int64(len(content)))
		ts.Close()
		if err != nil {
			t.Fatalf("len=%d: DoDAV: %v", len(content), err)
		}
		if resp.StatusCode != http.StatusCreated {
			t.Errorf("len=%d: StatusCode %d, want 201", len(content), resp.StatusCode)
		}
		if gotLen != int64(len(content)) || len(gotTE) != 0 {
			t.Errorf("len=%d: сервер видит ContentLength=%d TransferEncoding=%v, want %d и без chunked",
				len(content), gotLen, gotTE, len(content))
		}
		if string(gotBody) != content {
			t.Errorf("len=%d: тело на сервере отличается (got %d байт)", len(content), len(gotBody))
		}
		if n := atomic.LoadInt32(&body.closes); n != 0 {
			t.Errorf("len=%d: тело закрыто %d раз, want 0", len(content), n)
		}
	}
}

// TestDoDAV_BodyReusableAfterSend — после PUT тело не закрыто: вызывающий
// перематывает его и шлёт тот же файл повторно (сценарий 409/412 → повтор).
// Реальный *os.File: без не-закрывающей обёртки Seek упал бы «file already closed».
func TestDoDAV_BodyReusableAfterSend(t *testing.T) {
	var bodies []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		w.WriteHeader(http.StatusPreconditionFailed)
	}))
	defer ts.Close()

	path := filepath.Join(t.TempDir(), "report.pdf")
	if err := os.WriteFile(path, []byte("file-content"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	auth := authFromURL(t, ts.URL)
	for i := 0; i < 2; i++ {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			t.Fatalf("попытка %d: Seek: %v (тело закрыто транспортом?)", i+1, err)
		}
		if _, err := DoDAV(context.Background(), http.DefaultClient, auth, http.MethodPut, "/f", nil, f, 12); err != nil {
			t.Fatalf("попытка %d: DoDAV: %v", i+1, err)
		}
	}
	if len(bodies) != 2 || bodies[0] != "file-content" || bodies[1] != "file-content" {
		t.Errorf("тела попыток: got %q, want дважды «file-content»", bodies)
	}
}

// TestDoDAV_SabreMessage — s:message извлекается из живых XML-ошибок Sabre
// (Task 1: 409 PUT без папки, 412 If-None-Match на существующий файл); не-XML
// тело (HTML прокси) → пусто; у 2xx Message не заполняется.
func TestDoDAV_SabreMessage(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   []byte
		want   string
	}{
		{"409 живой", http.StatusConflict, davFixture(t, "dav_error_409.xml"), fixtureSabreMessage(t, davFixture(t, "dav_error_409.xml"))},
		{"412 живой", http.StatusPreconditionFailed, davFixture(t, "dav_error_412.xml"), fixtureSabreMessage(t, davFixture(t, "dav_error_412.xml"))},
		{"413 HTML прокси", http.StatusRequestEntityTooLarge, []byte("<html><body><h1>413 Request Entity Too Large</h1></body></html>"), ""},
		{"201 с телом", http.StatusCreated, davFixture(t, "dav_error_412.xml"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/xml; charset=utf-8")
				w.WriteHeader(tc.status)
				_, _ = w.Write(tc.body)
			}))
			defer ts.Close()
			resp, err := DoDAV(context.Background(), http.DefaultClient, authFromURL(t, ts.URL), http.MethodPut, "/f", nil, strings.NewReader("x"), 1)
			if err != nil {
				t.Fatalf("DoDAV: %v", err)
			}
			if resp.StatusCode != tc.status {
				t.Errorf("StatusCode: got %d, want %d", resp.StatusCode, tc.status)
			}
			if resp.Message != tc.want {
				t.Errorf("Message: got %q, want %q", resp.Message, tc.want)
			}
		})
	}
	if fixtureSabreMessage(t, davFixture(t, "dav_error_412.xml")) == "" {
		t.Fatal("живая фикстура 412 без текста s:message — эталон пуст")
	}
}

// stubDoer — Doer с заранее заданным ответом (без сети).
type stubDoer struct{ resp *http.Response }

func (d *stubDoer) Do(*http.Request) (*http.Response, error) { return d.resp, nil }

// countingBody — тело ответа: считает прочитанные байты и факт Close.
type countingBody struct {
	r      io.Reader
	n      int64
	closed bool
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	b.n += int64(n)
	return n, err
}

func (b *countingBody) Close() error {
	b.closed = true
	return nil
}

// TestDoDAV_BodyReadLimit — тело ответа читается не дальше davBodyLimit
// (64 КиБ, спека §3.4) и закрывается: 1 МиБ «s:message» не тянется целиком.
func TestDoDAV_BodyReadLimit(t *testing.T) {
	huge := `<d:error xmlns:d="DAV:" xmlns:s="http://sabredav.org/ns"><s:message>` +
		strings.Repeat("x", 1<<20) + `</s:message></d:error>`
	body := &countingBody{r: strings.NewReader(huge)}
	d := &stubDoer{resp: &http.Response{StatusCode: http.StatusInsufficientStorage, Header: http.Header{}, Body: body}}
	resp, err := DoDAV(context.Background(), d, authFromURL(t, "https://nc.example.org"), http.MethodPut, "/f", nil, strings.NewReader("x"), 1)
	if err != nil {
		t.Fatalf("DoDAV: %v", err)
	}
	if body.n > davBodyLimit {
		t.Errorf("прочитано %d байт тела, want <= %d", body.n, davBodyLimit)
	}
	if !body.closed {
		t.Error("тело ответа не закрыто")
	}
	if resp.Message != "" {
		t.Errorf("Message: got %d символов, want пусто (обрезанный XML не разбирается)", len(resp.Message))
	}
}

// errDoer — Doer, возвращающий заданную транспортную ошибку.
type errDoer struct{ err error }

func (d *errDoer) Do(*http.Request) (*http.Response, error) { return nil, d.err }

// TestDoDAV_TransportErrorSanitized — транспортная ошибка проходит SanitizeErr:
// ни userinfo с паролем-маркером, ни query в тексте нет.
func TestDoDAV_TransportErrorSanitized(t *testing.T) {
	d := &errDoer{err: &url.Error{Op: "Put", URL: "https://user:SECRET_MARKER@nc.example.org/f?x=1", Err: errors.New("connection refused")}}
	_, err := DoDAV(context.Background(), d, authFromURL(t, "https://nc.example.org"), http.MethodPut, "/f", nil, bytes.NewReader([]byte("x")), 1)
	if err == nil {
		t.Fatal("err = nil, want транспортную ошибку")
	}
	if s := err.Error(); strings.Contains(s, "SECRET_MARKER") || strings.Contains(s, "x=1") {
		t.Errorf("утечка в тексте ошибки: %q", s)
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("исходная причина потеряна: %q", err.Error())
	}
}

// TestDoDAV_CrossHostRedirectBlocked — PUT с 302 на чужой хост: http.Client с
// SameHostRedirectPolicy блокирует (302 превращает PUT в GET, политика
// срабатывает до запроса), чужой хост не получает ни одного запроса, пароль-
// маркер в тексте ошибки отсутствует.
func TestDoDAV_CrossHostRedirectBlocked(t *testing.T) {
	var evilHits int32
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&evilHits, 1)
	}))
	defer evil.Close()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL+"/leak", http.StatusFound)
	}))
	defer ts.Close()

	hc := &http.Client{CheckRedirect: SameHostRedirectPolicy}
	auth := authFromURL(t, ts.URL)
	auth.Password = "SECRET_MARKER"
	_, err := DoDAV(context.Background(), hc, auth, http.MethodPut, "/f", nil, strings.NewReader("x"), 1)
	if err == nil {
		t.Fatal("err = nil, want cross-host redirect blocked")
	}
	if !strings.Contains(err.Error(), "cross-host redirect blocked") {
		t.Errorf("err: got %q, want «cross-host redirect blocked»", err.Error())
	}
	if strings.Contains(err.Error(), "SECRET_MARKER") {
		t.Errorf("УТЕЧКА пароля в тексте ошибки: %q", err.Error())
	}
	if n := atomic.LoadInt32(&evilHits); n != 0 {
		t.Errorf("чужой хост получил %d запросов, want 0", n)
	}
}
```

- [ ] **Step 2: Прогнать тесты, убедиться в падении**

Run: `CGO_ENABLED=0 go test ./internal/transport/ -run 'TestDoDAV' -v`

Expected: FAIL — `[build failed]`, строки `internal/transport/dav_test.go:86:15: undefined: DoDAV` и `undefined: davBodyLimit`.

- [ ] **Step 3: Реализовать `internal/transport/dav.go`** — новый файл целиком:

```go
package transport

// dav.go — WebDAV-транспорт (спека-дельта 2026-10-09 §4.1). Гарантии
// безопасности — те же, что у DoOCS (шапка transport.go): Authorization
// собирается на каждый запрос, все ошибки — через SanitizeErr, dump
// запросов/ответов запрещён. OCS-конверта и OCS-заголовков нет: HTTP-статус
// интерпретирует вызывающий (у HEAD 404 — норма, «имя свободно»). Транспорт
// пригодится будущим files ls/get/find (TODO.md).

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"io"
	"net/http"
	"path"
	"strings"
)

// davBodyLimit — потолок чтения тела ответа WebDAV (спека §3.4): XML-ошибка
// Sabre — сотни байт; лимит защищает от неожиданно большого тела (HTML-страница
// прокси на 413/507 и т.п.).
const davBodyLimit = 64 << 10

// DAVResponse — итог WebDAV-запроса: статус интерпретирует вызывающий.
type DAVResponse struct {
	StatusCode int
	Header     http.Header
	Message    string // s:message из XML-ошибки Sabre (только при StatusCode >= 400)
}

// DoDAV выполняет WebDAV-запрос (спека-дельта 2026-10-09 §4.1).
//
// Параметры:
//   - p — путь относительно auth.BaseURL (начинается с '/'), НЕэкранированный:
//     сегменты экранирует url.URL при сборке (пробел, кириллица, '#', '?', '%');
//     склейка — path.Join, как у DoOCS: он схлопывает '.'/'..', поэтому имена
//     сегментов проверяет вызывающий;
//   - header — дополнительные заголовки (If-None-Match, Content-Type), может
//     быть nil; Authorization из header перекрывается собранным здесь;
//   - body — тело запроса (nil для HEAD/MKCOL/DELETE). DoDAV НЕ закрывает body
//     и не даёт закрыть его Transport-у: net/http закрывает тело-io.Closer
//     (например *os.File) после отправки и даже при ошибке, а вызывающий
//     перематывает тот же файл для повтора PUT после 409/412;
//   - contentLength — точная длина body (>= 0). Уходит заголовком
//     Content-Length, без chunked transfer-encoding: за php-fpm chunked-тело в
//     части конфигураций приходит пустым.
//
// error — только транспортные сбои: сеть, отмена ctx, редирект на чужой хост
// (политика doer-а SameHostRedirectPolicy), чтение тела ответа; всегда через
// SanitizeErr. HTTP-статусы, включая 4xx/5xx, возвращаются в DAVResponse.
// Редиректы PUT: на 307/308 тело без GetBody Go не переотправляет (вернётся
// сам 307), а 301/302 превращает PUT в GET — в обоих случаях статус не совпадёт
// с ожидаемым вызывающим, молчаливого успеха нет.
//
// Тело ответа дочитывается не дальше davBodyLimit и закрывается.
func DoDAV(ctx context.Context, doer Doer, auth Auth, method, p string, header http.Header, body io.Reader, contentLength int64) (*DAVResponse, error) {
	// Сборка URL — как в DoOCS: path.Join без двойных слэшей, RawPath сброшен,
	// чтобы url.String() экранировал сегменты из Path. Query у WebDAV нет.
	fullURL := *auth.BaseURL
	joined := path.Join(auth.BaseURL.Path, p)
	if !strings.HasPrefix(joined, "/") {
		joined = "/" + joined
	}
	fullURL.Path = joined
	fullURL.RawPath = ""
	fullURL.RawQuery = ""

	var reqBody io.Reader
	explicitLength := false
	switch {
	case body == nil:
		// HEAD/MKCOL/DELETE — без тела.
	case contentLength == 0:
		// Пустой файл: http.NoBody → Content-Length: 0. Не-nil тело с нулевой
		// ContentLength Go считает «длина неизвестна» и шлёт chunked.
		reqBody = http.NoBody
	default:
		// Не-закрывающая обёртка: Transport закрыл бы *os.File, и Seek перед
		// повтором PUT упал бы «file already closed».
		reqBody = io.NopCloser(body)
		explicitLength = true
	}

	req, err := http.NewRequestWithContext(ctx, method, fullURL.String(), reqBody)
	if err != nil {
		return nil, SanitizeErr(err)
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if explicitLength {
		// Тело неизвестного net/http типа: длину задаём явно, иначе chunked.
		req.ContentLength = contentLength
	}
	// Authorization: Basic — на каждый запрос заново и ПОСЛЕ копирования
	// header (перекрывает значение вызывающего), как в DoOCS.
	creds := auth.Login + ":" + auth.Password
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(creds)))

	resp, err := doer.Do(req)
	if err != nil {
		// При redirect-ошибке http.Client может вернуть предыдущий Response —
		// закрываем идемпотентно (как в DoOCS).
		if resp != nil {
			_ = resp.Body.Close()
		}
		return nil, SanitizeErr(err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, davBodyLimit))
	if err != nil {
		return nil, SanitizeErr(err)
	}
	out := &DAVResponse{StatusCode: resp.StatusCode, Header: resp.Header}
	if resp.StatusCode >= 400 {
		out.Message = sabreMessage(raw)
	}
	return out, nil
}

// sabreError — XML-ошибка Sabre:
//
//	<d:error xmlns:d="DAV:" xmlns:s="http://sabredav.org/ns">
//	  <s:exception>…</s:exception>
//	  <s:message>…</s:message>
//	</d:error>
//
// Поле сопоставляется по локальному имени (пространство имён любое).
type sabreError struct {
	Message string `xml:"message"`
}

// sabreMessage извлекает s:message из тела ошибки WebDAV. Не-XML (HTML прокси),
// обрезанное лимитом или пустое тело → "".
func sabreMessage(raw []byte) string {
	var e sabreError
	if err := xml.Unmarshal(raw, &e); err != nil {
		return ""
	}
	return strings.TrimSpace(e.Message)
}
```

- [ ] **Step 4: Прогнать тесты — зелёный, gofmt новых файлов, гейт**

Run: `CGO_ENABLED=0 go test ./internal/transport/ -run 'TestDoDAV' -v`

Expected: PASS — 7 тестов (`TestDoDAV_RequestShape`, `TestDoDAV_ContentLengthNoChunked`, `TestDoDAV_BodyReusableAfterSend`, `TestDoDAV_SabreMessage`, `TestDoDAV_BodyReadLimit`, `TestDoDAV_TransportErrorSanitized`, `TestDoDAV_CrossHostRedirectBlocked`), `ok  github.com/stas-bool/nctalk-cli/internal/transport`.

```sh
gofmt -l internal/transport/dav.go internal/transport/dav_test.go
CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./...
```

Expected: `gofmt -l` печатает пусто; гейт зелёный.

- [ ] **Step 5: Commit**

```sh
git add internal/transport/dav.go internal/transport/dav_test.go
git commit -m "feat(transport): WebDAV-транспорт DoDAV

DoDAV возвращает HTTP-статус вызывающему (DAVResponse), error - только
транспортные сбои через SanitizeErr. Тело PUT - io.NopCloser с явным
Content-Length (без chunked, файл переживает повтор), пустой файл -
http.NoBody. s:message Sabre из XML-ошибки, лимит чтения тела 64 КиБ.
Спека-дельта 2026-10-09 §4.1."
```

---

### Task 3: client — чистые хелперы вложений и upload-клиент без таймаута

**Files:**
- Create: `internal/client/files_helpers.go`
- Test: `internal/client/files_helpers_test.go` (новый)
- Modify: `internal/client/client.go` (строки 30–40 — структура; строки 45–53 — `NewTalkClient`; строки 67–71 — литерал в `NewTalkClientWithDoer`)
- Test: `internal/client/client_test.go` (два теста в конец файла)

**Interfaces:**
- Consumes: `config.Config{Timeout}`, `transport.Doer`, `transport.SameHostRedirectPolicy`; существующие тестовые хелперы `testCfg(baseURL string) config.Config` (`Timeout` 5s) и `mustParseURL` из `client_test.go`.
- Produces (unexported, пакет `client`; на них завязана Task 4):

```go
const defaultAttachmentFolder = "/Talk"
const uploadChunk = 64 << 10
func isPlainSegment(s string) bool                                // непустой, не «.»/«..», без «/»
func splitFileName(name string) (base string, n int, ext string)  // правило веб-клиента + дотфайл
func nameCandidate(name string, i int) string                     // i=0 — исходное; i>=1 — base (n+i)ext
func normalizeReferenceId(ref string) string                      // первые 64 байта + PHP trim
func generateReferenceId() (string, error)                        // 64 hex из crypto/rand
type talkMetaData struct {                                        // JSON omitempty
	Caption     string `json:"caption,omitempty"`
	ReplyTo     int    `json:"replyTo,omitempty"`
	ThreadId    int    `json:"threadId,omitempty"`
	ThreadTitle string `json:"threadTitle,omitempty"`
	Silent      bool   `json:"silent,omitempty"`
}
func encodeTalkMetaData(m talkMetaData) (string, error)           // JSON-строка, пустой набор → "{}"
func normalizeAttachmentFolder(folder string) string              // "" → /Talk; path.Clean("/"+folder)
func uploadTimeout(base time.Duration, size int64) time.Duration  // base + 1 с на начатые 64 КиБ; base<=0 → 0

// TalkClient: новое поле uploadDoer transport.Doer — PUT загрузки.
// NewTalkClient: отдельный *http.Client{Transport: тот же, CheckRedirect: тот же, Timeout: 0}.
// NewTalkClientWithDoer: uploadDoer = doer (тесты подменяют один doer).
```

- [ ] **Step 1: Написать падающие тесты хелперов** — новый файл `internal/client/files_helpers_test.go` целиком:

```go
package client

// files_helpers_test.go — таблицы чистых хелперов отправки файла (спека-дельта
// 2026-10-09 §1, §2.4, §3.5, §3.6, §4.2): кандидаты имени, сегменты пути,
// referenceId, talkMetaData, папка вложений, срок загрузки.

import (
	"math"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestNameCandidate — правило веб-клиента (§1, модель) + решение плана для
// дотфайлов: расширение с позиции 0 расширением не считается.
func TestNameCandidate(t *testing.T) {
	cases := []struct {
		name string
		i    int
		want string
	}{
		{"report.pdf", 0, "report.pdf"},
		{"report.pdf", 1, "report (2).pdf"},
		{"report.pdf", 2, "report (3).pdf"},
		{"x (3).pdf", 1, "x (4).pdf"},
		{"x (0).pdf", 1, "x (1).pdf"},
		{"a.tar.gz", 1, "a.tar (2).gz"},
		{"Makefile", 1, "Makefile (2)"},
		{"Makefile (7)", 2, "Makefile (9)"},
		{"REPORT.PDF", 1, "REPORT (2).PDF"},
		{"отчёт.pdf", 1, "отчёт (2).pdf"},
		{"файл.тхт", 1, "файл.тхт (2)"}, // кириллица в «расширении» — не [0-9a-z]
		{".bashrc", 1, ".bashrc (2)"},
		{".bashrc (2)", 1, ".bashrc (3)"},
		// суффикс — только « (N)» с пробелом; не влезающий в int — часть base
		{"x(2).pdf", 1, "x(2) (2).pdf"},
		{"n (99999999999999999999).txt", 1, "n (99999999999999999999) (2).txt"},
	}
	for _, tc := range cases {
		if got := nameCandidate(tc.name, tc.i); got != tc.want {
			t.Errorf("nameCandidate(%q, %d): got %q, want %q", tc.name, tc.i, got, tc.want)
		}
	}
}

// TestIsPlainSegment — один сегмент пути: пустое, «.», «..» и имена с «/»
// отвергаются (path.Join в транспорте схлопнул бы «.»/«..»).
func TestIsPlainSegment(t *testing.T) {
	cases := []struct {
		s    string
		want bool
	}{
		{"report.pdf", true},
		{"отчёт #1?%.pdf", true},
		{"...", true},
		{".bashrc", true},
		{"", false},
		{".", false},
		{"..", false},
		{"a/b", false},
		{"/", false},
	}
	for _, tc := range cases {
		if got := isPlainSegment(tc.s); got != tc.want {
			t.Errorf("isPlainSegment(%q): got %v, want %v", tc.s, got, tc.want)
		}
	}
}

// TestNormalizeReferenceId — как у сервера: первые 64 байта, затем trim по
// набору PHP trim().
func TestNormalizeReferenceId(t *testing.T) {
	long := strings.Repeat("ab", 40) // 80 байт
	cases := []struct {
		in, want string
	}{
		{"abc", "abc"},
		{"  abc \t\n", "abc"},
		{"\x00\x0Babc\r", "abc"},
		{long, long[:64]},
		{"  " + long, ("  " + long)[2:64]}, // сначала срез 64 байт, потом trim
		{strings.Repeat(" ", 70), ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := normalizeReferenceId(tc.in); got != tc.want {
			t.Errorf("normalizeReferenceId(%q): got %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestGenerateReferenceId — 64 hex-символа (32 байта crypto/rand), два вызова
// различаются.
func TestGenerateReferenceId(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{64}$`)
	a, err := generateReferenceId()
	if err != nil {
		t.Fatalf("generateReferenceId: %v", err)
	}
	b, err := generateReferenceId()
	if err != nil {
		t.Fatalf("generateReferenceId: %v", err)
	}
	if !re.MatchString(a) || !re.MatchString(b) {
		t.Errorf("формат: got %q / %q, want 64 hex", a, b)
	}
	if a == b {
		t.Errorf("два вызова дали одинаковый id %q", a)
	}
	if normalizeReferenceId(a) != a {
		t.Errorf("сгенерированный id меняется нормализацией: %q", a)
	}
}

// TestEncodeTalkMetaData — JSON-строка только с заданными полями; пустой
// набор → "{}" (§3.5).
func TestEncodeTalkMetaData(t *testing.T) {
	cases := []struct {
		in   talkMetaData
		want string
	}{
		{talkMetaData{}, `{}`},
		{talkMetaData{Caption: "Отчёт"}, `{"caption":"Отчёт"}`},
		{talkMetaData{ReplyTo: 7}, `{"replyTo":7}`},
		{talkMetaData{ThreadId: 1234, Silent: true}, `{"threadId":1234,"silent":true}`},
		{talkMetaData{Caption: "a\"b", ThreadTitle: "Тема"}, `{"caption":"a\"b","threadTitle":"Тема"}`},
	}
	for _, tc := range cases {
		got, err := encodeTalkMetaData(tc.in)
		if err != nil {
			t.Fatalf("encodeTalkMetaData(%+v): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("encodeTalkMetaData(%+v): got %s, want %s", tc.in, got, tc.want)
		}
	}
}

// TestNormalizeAttachmentFolder — пусто → /Talk; абсолютный путь без
// завершающего «/» и двойных слэшей; корень — «/».
func TestNormalizeAttachmentFolder(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "/Talk"},
		{"/Talk", "/Talk"},
		{"Talk", "/Talk"},
		{"/Docs/Chat/", "/Docs/Chat"},
		{"//a//b", "/a/b"},
		{"/", "/"},
	}
	for _, tc := range cases {
		if got := normalizeAttachmentFolder(tc.in); got != tc.want {
			t.Errorf("normalizeAttachmentFolder(%q): got %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestUploadTimeout — base + 1 с на каждые начатые 64 КиБ (§4.2); base <= 0 —
// без срока; переполнение — потолок.
func TestUploadTimeout(t *testing.T) {
	const s = time.Second
	cases := []struct {
		base time.Duration
		size int64
		want time.Duration
	}{
		{0, 1 << 20, 0},
		{-1, 1 << 20, 0},
		{30 * s, 0, 30 * s},
		{30 * s, 1, 31 * s},
		{30 * s, 64 << 10, 31 * s},
		{30 * s, 64<<10 + 1, 32 * s},
		{30 * s, 52428800, 830 * s}, // 50 МиБ = 800 кусков
		{30 * s, math.MaxInt64, time.Duration(math.MaxInt64)},
	}
	for _, tc := range cases {
		if got := uploadTimeout(tc.base, tc.size); got != tc.want {
			t.Errorf("uploadTimeout(%v, %d): got %v, want %v", tc.base, tc.size, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: Дописать падающие тесты upload-клиента** — в конец `internal/client/client_test.go` (после `mustParseURL`, через пустую строку; импорты `net/http`, `strings`, `time`, `transport` в файле уже есть):

```go
// TestNewTalkClient_UploadDoer — production-конструктор: PUT загрузки идёт
// через отдельный *http.Client без Timeout (срок задаёт контекст, спека-дельта
// 2026-10-09 §4.2) с тем же Transport и той же redirect-политикой; обычный
// doer сохраняет таймаут cfg.
func TestNewTalkClient_UploadDoer(t *testing.T) {
	c := NewTalkClient(testCfg("https://nc.example.org"))
	hc, ok := c.doer.(*http.Client)
	if !ok {
		t.Fatalf("doer: got %T, want *http.Client", c.doer)
	}
	uc, ok := c.uploadDoer.(*http.Client)
	if !ok {
		t.Fatalf("uploadDoer: got %T, want *http.Client", c.uploadDoer)
	}
	if uc == hc {
		t.Fatal("uploadDoer совпадает с doer — нужен отдельный клиент без Timeout")
	}
	if hc.Timeout != 5*time.Second {
		t.Errorf("doer.Timeout: got %v, want 5s (cfg.Timeout)", hc.Timeout)
	}
	if uc.Timeout != 0 {
		t.Errorf("uploadDoer.Timeout: got %v, want 0 (срок — из контекста)", uc.Timeout)
	}
	if uc.Transport != hc.Transport {
		t.Error("uploadDoer.Transport отличается от doer.Transport (нужен общий пул соединений)")
	}
	if uc.CheckRedirect == nil {
		t.Fatal("uploadDoer.CheckRedirect = nil — cross-host редирект унёс бы пароль")
	}
	err := uc.CheckRedirect(&http.Request{URL: mustParseURL("https://evil.example.com/x")},
		[]*http.Request{{URL: mustParseURL("https://nc.example.org/x")}})
	if err == nil || !strings.Contains(err.Error(), "cross-host redirect blocked") {
		t.Errorf("uploadDoer.CheckRedirect(cross-host): got %v, want cross-host redirect blocked", err)
	}
}

// TestNewTalkClientWithDoer_UploadDoerIsDoer — тестовый конструктор: загрузка
// идёт через тот же подменённый doer (спека-дельта 2026-10-09 §4.2).
func TestNewTalkClientWithDoer_UploadDoerIsDoer(t *testing.T) {
	d := &http.Client{}
	c := NewTalkClientWithDoer(testCfg("https://nc.example.org"), d)
	if c.uploadDoer != transport.Doer(d) {
		t.Errorf("uploadDoer: got %v, want переданный doer", c.uploadDoer)
	}
}
```

- [ ] **Step 3: Прогнать тесты, убедиться в падении**

Run: `CGO_ENABLED=0 go test ./internal/client/ -run 'TestNameCandidate|TestIsPlainSegment|TestNormalizeReferenceId|TestGenerateReferenceId|TestEncodeTalkMetaData|TestNormalizeAttachmentFolder|TestUploadTimeout|TestNewTalkClient' -v`

Expected: FAIL — `[build failed]`, строки `c.uploadDoer undefined (type *TalkClient has no field or method uploadDoer)`, `undefined: nameCandidate`, `undefined: isPlainSegment`, `undefined: normalizeReferenceId`, `undefined: generateReferenceId`.

- [ ] **Step 4: Реализовать `internal/client/files_helpers.go`** — новый файл целиком:

```go
package client

// files_helpers.go — чистые хелперы отправки файла (спека-дельта 2026-10-09
// `chat send --attach`, §1, §3.4–§3.6, §4.2): имена-кандидаты по правилу
// веб-клиента Talk, referenceId, talkMetaData, папка вложений, срок загрузки.
// Без сети — тестируются таблицами (files_helpers_test.go); сам SendFile — в
// files.go.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// defaultAttachmentFolder — папка вложений Talk по умолчанию (§3.2): ею
// заменяется пустой или отсутствующий config.attachments.folder.
const defaultAttachmentFolder = "/Talk"

// uploadChunk — шаг надбавки к сроку загрузки (§4.2): 1 с на каждые начатые
// 64 КиБ, т.е. минимальная скорость канала около 512 кбит/с.
const uploadChunk = 64 << 10

// refTrimSet — символы PHP trim() по умолчанию: сервер нормализует
// referenceId как trim(substr(ref, 0, 64)) (spreed ChatManager::addSystemMessage).
const refTrimSet = " \t\n\r\x00\x0B"

var (
	// fileExtRe — расширение по правилу веб-клиента (src/utils/fileUpload.ts,
	// extensionRegex): точка и [0-9a-z]+ в конце имени, без учёта регистра.
	fileExtRe = regexp.MustCompile(`(?i)\.[0-9a-z]+$`)
	// fileSuffixRe — числовой суффикс « (N)» в конце имени без расширения
	// (suffixRegex веб-клиента).
	fileSuffixRe = regexp.MustCompile(` \((\d+)\)$`)
)

// isPlainSegment — s годится как ОДИН сегмент пути WebDAV: непустой, без «/»,
// не «.» и не «..». path.Join в транспорте схлопнул бы «.»/«..», и PUT ушёл бы
// на саму папку вложений или её родителя (защитное расширение §5, решение
// плана). Проверяются имя файла и uid.
func isPlainSegment(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.Contains(s, "/")
}

// splitFileName разбирает имя по правилу веб-клиента (findUniquePath, §1):
// base, числовой суффикс n (1, если суффикса нет) и расширение ext.
//
// Отличие от веб-клиента (решение плана): совпадение расширения с позиции 0 —
// дотфайл («.bashrc») — расширением не считается: иначе base пуст и кандидат
// выглядел бы « (2).bashrc»; у нас — «.bashrc (2)». Суффикс, не влезающий в
// int, суффиксом не считается (остаётся частью base).
func splitFileName(name string) (base string, n int, ext string) {
	ext = fileExtRe.FindString(name)
	if len(ext) == len(name) {
		ext = ""
	}
	base = name[:len(name)-len(ext)]
	n = 1
	if m := fileSuffixRe.FindStringSubmatch(base); m != nil {
		if v, err := strconv.Atoi(m[1]); err == nil {
			n = v
			base = base[:len(base)-len(m[0])]
		}
	}
	return base, n, ext
}

// nameCandidate — i-й кандидат имени файла на сервере (§1, модель): i=0 —
// исходное имя; i>=1 — base + « (n+i)» + ext, где n — суффикс исходного имени.
// report.pdf → report (2).pdf → report (3).pdf; x (3).pdf → x (4).pdf;
// a.tar.gz → a.tar (2).gz; Makefile → Makefile (2).
func nameCandidate(name string, i int) string {
	if i == 0 {
		return name
	}
	base, n, ext := splitFileName(name)
	return base + " (" + strconv.Itoa(n+i) + ")" + ext
}

// normalizeReferenceId повторяет серверную нормализацию referenceId (§1, §3.6):
// первые 64 БАЙТА, затем trim по refTrimSet. Поиск id сообщения сравнивает с
// этим значением.
func normalizeReferenceId(ref string) string {
	if len(ref) > 64 {
		ref = ref[:64]
	}
	return strings.Trim(ref, refTrimSet)
}

// generateReferenceId — случайный referenceId: 64 hex-символа из crypto/rand
// (docs Talk: «should be a random sha256», §2.4).
func generateReferenceId() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("client: SendFile: не удалось сгенерировать referenceId: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// talkMetaData — поля JSON-строки talkMetaData шаринга (§3.5): в строку
// попадают только заданные (omitempty). messageType не используется
// (голосовые сообщения — out of scope).
type talkMetaData struct {
	Caption     string `json:"caption,omitempty"`
	ReplyTo     int    `json:"replyTo,omitempty"`
	ThreadId    int    `json:"threadId,omitempty"`
	ThreadTitle string `json:"threadTitle,omitempty"`
	Silent      bool   `json:"silent,omitempty"`
}

// encodeTalkMetaData — talkMetaData как JSON-СТРОКА (её кладут строковым полем
// в тело шаринга, listener Talk делает json_decode): caption непуст,
// replyTo/threadId > 0, threadTitle непуст, silent = true; пустой набор →
// "{}" (как у веб-клиента).
func encodeTalkMetaData(m talkMetaData) (string, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("client: SendFile: не удалось собрать talkMetaData: %w", err)
	}
	return string(b), nil
}

// normalizeAttachmentFolder — папка вложений из config.attachments.folder
// (§3.2): пусто → /Talk; иначе абсолютный путь без завершающего «/» и без
// «.»/«..» (path.Clean). Корень пользователя — «/».
func normalizeAttachmentFolder(folder string) string {
	if folder == "" {
		return defaultAttachmentFolder
	}
	return path.Clean("/" + folder)
}

// uploadTimeout — срок PUT по размеру файла (§4.2): base (NEXTCLOUD_TIMEOUT) +
// 1 с на каждые начатые 64 КиБ. base <= 0 — «без таймаута» (семантика
// http.Client.Timeout = 0) → 0: срок не ставится (решение плана).
// Переполнение (файлы больше ~600 ТБ) — потолок math.MaxInt64.
func uploadTimeout(base time.Duration, size int64) time.Duration {
	if base <= 0 {
		return 0
	}
	if size <= 0 {
		return base
	}
	chunks := (size-1)/uploadChunk + 1 // округление вверх без переполнения size+chunk
	if chunks > (math.MaxInt64-int64(base))/int64(time.Second) {
		return time.Duration(math.MaxInt64)
	}
	return base + time.Duration(chunks)*time.Second
}
```

- [ ] **Step 5: Upload-клиент в `internal/client/client.go`** — три правки.

1. `internal/client/client.go`, структура `TalkClient` (строки 30–40 исходного файла) — найти:

```go
// TalkClient — HTTP-клиент Nextcloud Talk.
//
// Поля unexported: cfg (креды/таймаут — источник правды о конфигурации),
// doer (транспорт, подменяется в тестах), auth (распарсенный BaseURL + креды
// в формате, готовом для transport.DoOCS — без timeout, он выставлен на
// http.Client в NewTalkClient).
type TalkClient struct {
	cfg  config.Config
	doer transport.Doer
	auth transport.Auth
}
```

заменить на:

```go
// TalkClient — HTTP-клиент Nextcloud Talk.
//
// Поля unexported: cfg (креды/таймаут — источник правды о конфигурации),
// doer (транспорт, подменяется в тестах), uploadDoer (транспорт PUT загрузки
// файла — без общего таймаута, срок задаёт контекст; спека-дельта 2026-10-09
// §4.2), auth (распарсенный BaseURL + креды в формате, готовом для
// transport.DoOCS — без timeout, он выставлен на http.Client в NewTalkClient).
type TalkClient struct {
	cfg        config.Config
	doer       transport.Doer
	uploadDoer transport.Doer
	auth       transport.Auth
}
```

2. `internal/client/client.go`, `NewTalkClient` (строки 45–53 исходного файла) — найти:

```go
// cross-host auth-leak и https→http downgrade).
func NewTalkClient(cfg config.Config) *TalkClient {
	hc := &http.Client{
		Transport:     http.DefaultTransport,
		Timeout:       cfg.Timeout,
		CheckRedirect: transport.SameHostRedirectPolicy,
	}
	return NewTalkClientWithDoer(cfg, hc)
}
```

заменить на:

```go
// cross-host auth-leak и https→http downgrade).
//
// Для PUT загрузки файла — отдельный http.Client с тем же Transport и
// CheckRedirect, но БЕЗ Timeout (спека-дельта 2026-10-09 §4.2):
// http.Client.Timeout покрывает и передачу тела, большой файл на медленном
// канале не успел бы загрузиться. Срок PUT задаёт контекст (uploadTimeout).
func NewTalkClient(cfg config.Config) *TalkClient {
	hc := &http.Client{
		Transport:     http.DefaultTransport,
		Timeout:       cfg.Timeout,
		CheckRedirect: transport.SameHostRedirectPolicy,
	}
	c := NewTalkClientWithDoer(cfg, hc)
	c.uploadDoer = &http.Client{
		Transport:     hc.Transport,
		CheckRedirect: hc.CheckRedirect,
	}
	return c
}
```

3. `internal/client/client.go`, литерал в `NewTalkClientWithDoer` (строки 67–71 исходного файла) — найти:

```go
	return &TalkClient{
		cfg:  cfg,
		doer: doer,
		auth: transport.Auth{BaseURL: u, Login: cfg.Login, Password: cfg.Password},
	}
```

заменить на:

```go
	return &TalkClient{
		cfg:  cfg,
		doer: doer,
		// Тесты подменяют один doer — им же идёт и загрузка (§4.2).
		uploadDoer: doer,
		auth:       transport.Auth{BaseURL: u, Login: cfg.Login, Password: cfg.Password},
	}
```

- [ ] **Step 6: Прогнать тесты — зелёный, gofmt, гейт**

Run: тот же `CGO_ENABLED=0 go test ./internal/client/ -run 'TestNameCandidate|TestIsPlainSegment|TestNormalizeReferenceId|TestGenerateReferenceId|TestEncodeTalkMetaData|TestNormalizeAttachmentFolder|TestUploadTimeout|TestNewTalkClient' -v`

Expected: PASS — 9 тестов: 7 табличных хелперов + `TestNewTalkClient_UploadDoer` + `TestNewTalkClientWithDoer_UploadDoerIsDoer`.

```sh
gofmt -l internal/client/files_helpers.go internal/client/files_helpers_test.go internal/client/client.go internal/client/client_test.go
CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./...
```

Expected: `gofmt -l` печатает пусто (`client.go` и `client_test.go` были чистыми и остаются чистыми); гейт зелёный.

- [ ] **Step 7: Commit**

```sh
git add internal/client/files_helpers.go internal/client/files_helpers_test.go internal/client/client.go internal/client/client_test.go
git commit -m "feat(client): хелперы вложений и upload-клиент без таймаута

Имена-кандидаты по правилу веб-клиента Talk (report (2).pdf, a.tar (2).gz,
дотфайл .bashrc (2)), проверка сегмента пути (пусто, «.», «..», «/»),
нормализация и генерация referenceId, talkMetaData JSON-строкой, папка
вложений, срок PUT по размеру. PUT загрузки - через отдельный http.Client
без Timeout с той же redirect-политикой. Спека-дельта 2026-10-09 §3-§4.2."
```

---

### Task 4: client — `SendFile` (загрузка, шаринг, поиск id)

**Files:**
- Create: `internal/client/files.go`
- Test: `internal/client/files_test.go` (новый)

**Interfaces:**
- Consumes:
  - Task 2: `transport.DoDAV`, `transport.DAVResponse`.
  - Task 3: хелперы `files_helpers.go` и поле `c.uploadDoer`.
  - Существующий клиент:
    - `c.doOCS(ctx, method, p string, query url.Values, body io.Reader, mutate bool, out any) (http.Header, error)` — при `out == nil` тело не разбирается;
    - `c.GetChat(ctx, token, GetChatOpts{Limit})`;
    - `Message{Id, MessageType, Message, ReferenceId, IsReplyable, ThreadTitle, SystemMessage, MessageParameters}`;
    - `*OCSError{Code, Message}`, константа `pathChat`;
    - тестовые хелперы `testCfg`, `transport.OCSEnvelope[T]`.
  - Фикстуры Task 1 (значения — в Produces Task 1).
- Produces (Task 5 и Task 6 на них завязаны):

```go
type FileUpload struct {
	Name string // имя на сервере (filepath.Base локального пути)
	Size int64
	Body io.ReadSeeker // перематывается (Seek 0) перед каждой попыткой PUT
}

type SendFileOpts struct {
	Caption     string // пусто → без подписи
	ReplyTo     int
	ThreadId    int
	ThreadTitle string
	Silent      bool
	ReferenceId string // пусто → генерируется
}

type SendFileResult struct {
	MessageId int    // 0 — сообщение не найдено (файл отправлен), см. LookupErr
	Path      string // итоговый путь на сервере, напр. "/Talk/report (2).pdf"
	LookupErr error  // причина, если MessageId == 0
}

func (c *TalkClient) SendFile(ctx context.Context, token string, f FileUpload, opts SendFileOpts) (SendFileResult, error)

// unexported — для TestIntegration_SendFile (Task 6):
func (c *TalkClient) currentUserId(ctx context.Context) (string, error)
func (c *TalkClient) attachmentFolder(ctx context.Context) (string, error)
func (c *TalkClient) davDelete(ctx context.Context, uid, sharePath string) error
func davFilesPath(uid, userPath string) string
```

  Контракт ошибок:
  - «не найдено» (тред, сообщение, комната, 404 шаринга) — `*OCSError{Code: 404}`; cli отдаёт exit 2 через существующий `exitFromClientErr`;
  - «не replyable», вложения запрещены, ошибки DAV, сеть — обычные ошибки;
  - промах поиска id — `MessageId: 0` + `LookupErr`, `error == nil`.

  Порядок шагов: валидация → uid и настройки вложений → pre-check → имя + PUT (+ MKCOL) → шаринг (+ cleanup DELETE) → поиск id.

- [ ] **Step 1: Написать падающие тесты** — новый файл `internal/client/files_test.go` целиком.

`fakeNC` — httptest-сервер с маршрутизацией по методу и пути. Ответы по умолчанию — живые фикстуры Task 1, отдельные шаги переключаются полями-ручками. Клиент строится production-конструктором `NewTalkClient`, поэтому в игре настоящие `http.Client` с `SameHostRedirectPolicy` и отдельным upload-клиентом.

```go
package client

// files_test.go — SendFile (спека-дельта 2026-10-09 §5, §7): httptest-сервер
// fakeNC с маршрутизацией по методу и пути; ответы по умолчанию — живые
// фикстуры Task 1 (обезличенные), отдельные шаги переключаются полями fakeNC.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stas-bool/nctalk-cli/internal/transport"
)

// Нормализованные значения живых фикстур (Task 1, протокол нормализации).
const (
	fixtureUID   = "anna.s"
	fixtureToken = "tok-team"

	msgPlain         = 1500 // файл без подписи (refPlain)
	msgCaption       = 1501 // файл с подписью (refCaption)
	msgThreadRoot    = 1502 // файл + threadTitle: корень нового треда (refThread)
	msgThreadCreated = 1503 // system thread_created того же шаринга (refThread)
	msgNeighbor      = 1510 // обычное сообщение — «сосед» для context несуществующего id

	fixtureCaption = "nctalk attach fixture — подпись"
)

var (
	refPlain   = strings.Repeat("0", 63) + "1"
	refCaption = strings.Repeat("0", 63) + "2"
	refThread  = strings.Repeat("0", 63) + "3"
)

// fileFixture читает фикстуру из корневого testdata/.
func fileFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// ncReq — запись одного запроса к fakeNC.
type ncReq struct {
	Method           string
	Path             string // декодированный путь
	EscapedPath      string
	Query            url.Values
	Header           http.Header
	Body             []byte
	ContentLength    int64
	TransferEncoding []string
}

// fakeNC — httptest-сервер Nextcloud для SendFile. Фикстуры читаются в
// newFakeNC (t.Fatalf из горутины сервера недопустим); handler сериализован
// мьютексом — поля-ручки настраиваются тестом ДО вызова SendFile.
type fakeNC struct {
	srv *httptest.Server
	mu  sync.Mutex

	reqs []ncReq

	uid             string          // uid в путях WebDAV (совпадает с телом user)
	user            []byte          // тело cloud/user
	caps            []byte          // тело capabilities
	threadStatus    int             // threads/{id}: 0/200 → thread_info, 404 → thread_not_found
	context         []byte          // тело context при HTTP 200
	contextCode     int             // HTTP context: 0 → 200; 304; 404
	existing        map[string]bool // занятые пути (от корня пользователя): HEAD 200
	headStatus      int             // HEAD незанятого пути: 0 → 404
	putStatuses     []int           // очередь статусов PUT; пусто → 201
	putMessage      string          // s:message для PUT >= 400 (кроме 409/412 — живые XML)
	putRedirect     string          // непусто → PUT отвечает редиректом на этот URL
	putRedirectCode int             // код редиректа PUT (302/307)
	mkcolStatus     int             // 0 → 201
	shareStatus     int             // 0 → 200 (share_create.json)
	shareMessage    string          // meta.message ошибки шаринга
	deleteStatus    int             // 0 → 204
	chatStatus      int             // страница chat: 0 → 200
	chat            func(lastRef string) []byte
	lastRef         string // referenceId последнего шаринга

	fx map[string][]byte
}

func newFakeNC(t *testing.T) *fakeNC {
	t.Helper()
	f := &fakeNC{uid: fixtureUID, existing: map[string]bool{}, fx: map[string][]byte{}}
	for _, name := range []string{
		"cloud_user.json", "capabilities_attachments.json", "share_create.json",
		"chat_file_messages.json", "thread_info.json", "thread_not_found.json",
		"chat_context_message.json", "dav_error_409.xml", "dav_error_412.xml",
	} {
		f.fx[name] = fileFixture(t, name)
	}
	f.user = f.fx["cloud_user.json"]
	f.caps = f.fx["capabilities_attachments.json"]
	f.context = f.fx["chat_context_message.json"]
	chat := f.fx["chat_file_messages.json"]
	f.chat = func(string) []byte { return chat }
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

// client — TalkClient на fakeNC через production-конструктор (настоящие
// http.Client с SameHostRedirectPolicy, отдельный upload-клиент).
func (f *fakeNC) client() *TalkClient { return NewTalkClient(testCfg(f.srv.URL)) }

func (f *fakeNC) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, ncReq{
		Method: r.Method, Path: r.URL.Path, EscapedPath: r.URL.EscapedPath(),
		Query: r.URL.Query(), Header: r.Header.Clone(), Body: body,
		ContentLength: r.ContentLength, TransferEncoding: r.TransferEncoding,
	})

	const davPrefix = pathDAVFiles + "/"
	chatPrefix := pathChat + "/" + fixtureToken
	p := r.URL.Path
	switch {
	case p == "/redirected":
		w.WriteHeader(http.StatusOK)
	case strings.HasPrefix(p, davPrefix+f.uid+"/"):
		f.handleDAV(w, r, strings.TrimPrefix(p, davPrefix+f.uid))
	case r.Method == http.MethodGet && p == pathCloudUser:
		writeOCS(w, http.StatusOK, f.user)
	case r.Method == http.MethodGet && p == pathCapabilities:
		writeOCS(w, http.StatusOK, f.caps)
	case r.Method == http.MethodPost && p == pathShares:
		var req struct {
			ReferenceId string `json:"referenceId"`
		}
		_ = json.Unmarshal(body, &req)
		f.lastRef = req.ReferenceId
		if f.shareStatus != 0 && f.shareStatus != http.StatusOK {
			writeOCS(w, f.shareStatus, ocsFailure(f.shareStatus, f.shareMessage))
			return
		}
		writeOCS(w, http.StatusOK, f.fx["share_create.json"])
	case r.Method == http.MethodGet && p == chatPrefix:
		if f.chatStatus != 0 && f.chatStatus != http.StatusOK {
			writeOCS(w, f.chatStatus, ocsFailure(f.chatStatus, "Internal Server Error"))
			return
		}
		writeOCS(w, http.StatusOK, f.chat(f.lastRef))
	case r.Method == http.MethodGet && strings.HasPrefix(p, chatPrefix+"/threads/"):
		if f.threadStatus == http.StatusNotFound {
			writeOCS(w, http.StatusNotFound, f.fx["thread_not_found.json"])
			return
		}
		writeOCS(w, http.StatusOK, f.fx["thread_info.json"])
	case r.Method == http.MethodGet && strings.HasPrefix(p, chatPrefix+"/") && strings.HasSuffix(p, "/context"):
		switch f.contextCode {
		case http.StatusNotModified:
			w.WriteHeader(http.StatusNotModified)
		case http.StatusNotFound:
			writeOCS(w, http.StatusNotFound, ocsFailure(http.StatusNotFound, "Message not found"))
		default:
			writeOCS(w, http.StatusOK, f.context)
		}
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeNC) handleDAV(w http.ResponseWriter, r *http.Request, userPath string) {
	switch r.Method {
	case http.MethodHead:
		switch {
		case f.existing[userPath]:
			w.WriteHeader(http.StatusOK)
		case f.headStatus != 0:
			w.WriteHeader(f.headStatus)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	case http.MethodPut:
		if f.putRedirect != "" {
			http.Redirect(w, r, f.putRedirect, f.putRedirectCode)
			return
		}
		status := http.StatusCreated
		if len(f.putStatuses) > 0 {
			status, f.putStatuses = f.putStatuses[0], f.putStatuses[1:]
		}
		switch {
		case status == http.StatusConflict:
			writeXML(w, status, f.fx["dav_error_409.xml"])
		case status == http.StatusPreconditionFailed:
			writeXML(w, status, f.fx["dav_error_412.xml"])
		case status >= 400:
			writeXML(w, status, sabreXML(f.putMessage))
		default:
			w.WriteHeader(status)
		}
	case "MKCOL":
		status := f.mkcolStatus
		if status == 0 {
			status = http.StatusCreated
		}
		if status >= 400 {
			writeXML(w, status, sabreXML("mkcol failed"))
			return
		}
		w.WriteHeader(status)
	case http.MethodDelete:
		status := f.deleteStatus
		if status == 0 {
			status = http.StatusNoContent
		}
		w.WriteHeader(status)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func writeOCS(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeXML(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// ocsFailure — OCS-конверт ошибки (OCS v2: HTTP-статус = statuscode).
func ocsFailure(status int, message string) []byte {
	msg, _ := json.Marshal(message)
	return []byte(fmt.Sprintf(`{"ocs":{"meta":{"status":"failure","statuscode":%d,"message":%s},"data":[]}}`, status, msg))
}

// sabreXML — XML-ошибка Sabre с заданным s:message (форма живых фикстур).
func sabreXML(message string) []byte {
	return []byte(`<?xml version="1.0" encoding="utf-8"?>` + "\n" +
		`<d:error xmlns:d="DAV:" xmlns:s="http://sabredav.org/ns">` +
		`<s:exception>Sabre\DAV\Exception</s:exception><s:message>` + message + `</s:message></d:error>`)
}

// capsBody — capabilities с заданными spreed/allowed/folder.
func capsBody(t *testing.T, spreed, allowed bool, folder string) []byte {
	t.Helper()
	caps := map[string]any{}
	if spreed {
		caps["spreed"] = map[string]any{"config": map[string]any{"attachments": map[string]any{"allowed": allowed, "folder": folder}}}
	}
	return ocsBody(t, 200, "OK", map[string]any{"capabilities": caps})
}

func (f *fakeNC) requests() []ncReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ncReq(nil), f.reqs...)
}

// sequence — «МЕТОД путь» всех запросов по порядку.
func (f *fakeNC) sequence() []string {
	var out []string
	for _, r := range f.requests() {
		out = append(out, r.Method+" "+r.Path)
	}
	return out
}

// byMethod — запросы с методом method и путём, начинающимся с prefix.
func (f *fakeNC) byMethod(method, prefix string) []ncReq {
	var out []ncReq
	for _, r := range f.requests() {
		if r.Method == method && strings.HasPrefix(r.Path, prefix) {
			out = append(out, r)
		}
	}
	return out
}

// shareBody — JSON-тело единственного запроса шаринга.
func (f *fakeNC) shareBody(t *testing.T) map[string]any {
	t.Helper()
	shares := f.byMethod(http.MethodPost, pathShares)
	if len(shares) != 1 {
		t.Fatalf("запросов шаринга: got %d, want 1", len(shares))
	}
	var m map[string]any
	if err := json.Unmarshal(shares[0].Body, &m); err != nil {
		t.Fatalf("тело шаринга не JSON: %v; body=%s", err, shares[0].Body)
	}
	return m
}

func upload(name, content string) FileUpload {
	return FileUpload{Name: name, Size: int64(len(content)), Body: strings.NewReader(content)}
}

const davAnna = pathDAVFiles + "/" + fixtureUID

// TestSendFile_HappyPath — HEAD 404 → PUT 201 → шаринг → поиск (§7): порядок
// запросов, заголовки PUT (If-None-Match, Content-Type, Content-Length без
// chunked, Authorization, без OCS-заголовков), тело шаринга, query поиска.
func TestSendFile_HappyPath(t *testing.T) {
	f := newFakeNC(t)
	const content = "PDF-содержимое"
	res, err := f.client().SendFile(context.Background(), fixtureToken, upload("report.pdf", content), SendFileOpts{ReferenceId: refPlain})
	if err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	if res.MessageId != msgPlain || res.Path != "/Talk/report.pdf" || res.LookupErr != nil {
		t.Errorf("result: got %+v, want MessageId=%d Path=/Talk/report.pdf LookupErr=nil", res, msgPlain)
	}
	want := []string{
		"GET " + pathCloudUser,
		"GET " + pathCapabilities,
		"HEAD " + davAnna + "/Talk/report.pdf",
		"PUT " + davAnna + "/Talk/report.pdf",
		"POST " + pathShares,
		"GET " + pathChat + "/" + fixtureToken,
	}
	if got := f.sequence(); !reflect.DeepEqual(got, want) {
		t.Fatalf("запросы:\n got %q\nwant %q", got, want)
	}

	put := f.byMethod(http.MethodPut, davAnna)[0]
	for h, v := range map[string]string{
		"If-None-Match":  "*",
		"Content-Type":   "application/octet-stream",
		"Authorization":  wantAuth(),
		"OCS-APIRequest": "",
	} {
		if got := put.Header.Get(h); got != v {
			t.Errorf("PUT %s: got %q, want %q", h, got, v)
		}
	}
	if put.ContentLength != int64(len(content)) || len(put.TransferEncoding) != 0 {
		t.Errorf("PUT: ContentLength=%d TransferEncoding=%v, want %d без chunked", put.ContentLength, put.TransferEncoding, len(content))
	}
	if string(put.Body) != content {
		t.Errorf("PUT body: got %q, want %q", put.Body, content)
	}

	share := f.byMethod(http.MethodPost, pathShares)[0]
	if share.Header.Get("Content-Type") != "application/json" || share.Header.Get("OCS-APIRequest") != "true" {
		t.Errorf("шаринг: Content-Type=%q OCS-APIRequest=%q", share.Header.Get("Content-Type"), share.Header.Get("OCS-APIRequest"))
	}
	body := f.shareBody(t)
	wantBody := map[string]any{
		"shareType":    float64(10),
		"shareWith":    fixtureToken,
		"path":         "/Talk/report.pdf",
		"referenceId":  refPlain,
		"talkMetaData": "{}",
	}
	if !reflect.DeepEqual(body, wantBody) {
		t.Errorf("тело шаринга:\n got %v\nwant %v", body, wantBody)
	}

	chat := f.byMethod(http.MethodGet, pathChat+"/"+fixtureToken)[0]
	for k, v := range map[string]string{"limit": "50", "lookIntoFuture": "0", "setReadMarker": "0"} {
		if got := chat.Query.Get(k); got != v {
			t.Errorf("поиск: query %s=%q, want %q", k, got, v)
		}
	}
}

// TestSendFile_TalkMetaData — talkMetaData — JSON-СТРОКА только с заданными
// полями; пустой набор → "{}" (§3.5).
func TestSendFile_TalkMetaData(t *testing.T) {
	cases := []struct {
		name string
		opts SendFileOpts
		want string
	}{
		{"пустой набор", SendFileOpts{}, `{}`},
		{"подпись", SendFileOpts{Caption: fixtureCaption}, `{"caption":"nctalk attach fixture — подпись"}`},
		{"ответ", SendFileOpts{ReplyTo: msgCaption}, `{"replyTo":1501}`},
		{"тред", SendFileOpts{ThreadId: msgThreadRoot}, `{"threadId":1502}`},
		{"новый тред без уведомления", SendFileOpts{ThreadTitle: "Тема", Silent: true}, `{"threadTitle":"Тема","silent":true}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeNC(t)
			tc.opts.ReferenceId = refPlain
			if _, err := f.client().SendFile(context.Background(), fixtureToken, upload("report.pdf", "x"), tc.opts); err != nil {
				t.Fatalf("SendFile: %v", err)
			}
			got, ok := f.shareBody(t)["talkMetaData"].(string)
			if !ok {
				t.Fatalf("talkMetaData: не строка (%T)", f.shareBody(t)["talkMetaData"])
			}
			if got != tc.want {
				t.Errorf("talkMetaData: got %s, want %s", got, tc.want)
			}
		})
	}
}

// TestSendFile_GeneratedReferenceId — пустой или пустой после нормализации
// referenceId → 64 hex из crypto/rand (§2.4); поиск находит сообщение по нему.
func TestSendFile_GeneratedReferenceId(t *testing.T) {
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for _, given := range []string{"", "   \t"} {
		f := newFakeNC(t)
		chat := f.fx["chat_file_messages.json"]
		f.chat = func(lastRef string) []byte { return bytes.ReplaceAll(chat, []byte(refPlain), []byte(lastRef)) }
		res, err := f.client().SendFile(context.Background(), fixtureToken, upload("report.pdf", "x"), SendFileOpts{ReferenceId: given})
		if err != nil {
			t.Fatalf("ReferenceId=%q: SendFile: %v", given, err)
		}
		ref, _ := f.shareBody(t)["referenceId"].(string)
		if !hex64.MatchString(ref) {
			t.Errorf("ReferenceId=%q: в шаринг ушёл %q, want 64 hex", given, ref)
		}
		if res.MessageId != msgPlain {
			t.Errorf("ReferenceId=%q: MessageId=%d, want %d (LookupErr=%v)", given, res.MessageId, msgPlain, res.LookupErr)
		}
	}
}

// TestSendFile_ReferenceIdNormalized — заданный referenceId уходит серверу как
// есть, поиск сравнивает с серверной нормализацией: 64 байта, затем trim (§3.6).
func TestSendFile_ReferenceIdNormalized(t *testing.T) {
	f := newFakeNC(t)
	raw := "  " + strings.Repeat("ab", 40)
	stored := strings.Repeat("ab", 31) // trim(substr(raw, 0, 64)) — как сохранит сервер
	chat := f.fx["chat_file_messages.json"]
	f.chat = func(string) []byte { return bytes.ReplaceAll(chat, []byte(refPlain), []byte(stored)) }
	res, err := f.client().SendFile(context.Background(), fixtureToken, upload("report.pdf", "x"), SendFileOpts{ReferenceId: raw})
	if err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	if got := f.shareBody(t)["referenceId"]; got != raw {
		t.Errorf("referenceId шаринга: got %q, want исходный %q", got, raw)
	}
	if res.MessageId != msgPlain {
		t.Errorf("MessageId: got %d, want %d (LookupErr=%v)", res.MessageId, msgPlain, res.LookupErr)
	}
}

// TestSendFile_UniqueName — занятые имена пропускаются по правилу веб-клиента
// (§1): HEAD 200 → следующий кандидат; путь шаринга = итоговое имя.
func TestSendFile_UniqueName(t *testing.T) {
	cases := []struct {
		name     string
		existing []string
		want     string
	}{
		{"report.pdf", []string{"/Talk/report.pdf", "/Talk/report (2).pdf"}, "/Talk/report (3).pdf"},
		{"x (3).pdf", []string{"/Talk/x (3).pdf"}, "/Talk/x (4).pdf"},
		{"Makefile", []string{"/Talk/Makefile"}, "/Talk/Makefile (2)"},
	}
	for _, tc := range cases {
		f := newFakeNC(t)
		for _, p := range tc.existing {
			f.existing[p] = true
		}
		res, err := f.client().SendFile(context.Background(), fixtureToken, upload(tc.name, "x"), SendFileOpts{ReferenceId: refPlain})
		if err != nil {
			t.Fatalf("%s: SendFile: %v", tc.name, err)
		}
		if res.Path != tc.want {
			t.Errorf("%s: Path=%q, want %q", tc.name, res.Path, tc.want)
		}
		if n := len(f.byMethod(http.MethodHead, davAnna)); n != len(tc.existing)+1 {
			t.Errorf("%s: HEAD-запросов %d, want %d", tc.name, n, len(tc.existing)+1)
		}
		puts := f.byMethod(http.MethodPut, davAnna)
		if len(puts) != 1 || puts[0].Path != davAnna+tc.want {
			t.Errorf("%s: PUT %v, want один PUT %s", tc.name, f.sequence(), davAnna+tc.want)
		}
		if got := f.shareBody(t)["path"]; got != tc.want {
			t.Errorf("%s: path шаринга %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestSendFile_NameCandidatesLimit — все имена заняты: после 1000 кандидатов
// ошибка без PUT и шаринга (§3.4).
func TestSendFile_NameCandidatesLimit(t *testing.T) {
	f := newFakeNC(t)
	f.headStatus = http.StatusOK
	_, err := f.client().SendFile(context.Background(), fixtureToken, upload("report.pdf", "x"), SendFileOpts{})
	if err == nil || !strings.Contains(err.Error(), "не удалось подобрать свободное имя") {
		t.Fatalf("err: got %v, want «не удалось подобрать свободное имя»", err)
	}
	if n := len(f.byMethod(http.MethodHead, davAnna)); n != maxNameCandidates {
		t.Errorf("HEAD-запросов %d, want %d", n, maxNameCandidates)
	}
	if n := len(f.byMethod(http.MethodPut, davAnna)) + len(f.byMethod(http.MethodPost, pathShares)); n != 0 {
		t.Errorf("PUT/шаринг: %d запросов, want 0", n)
	}
}

// closeCountingBody — ReadSeeker с Close-счётчиком: net/http закрыл бы его,
// будь он передан телом запроса без не-закрывающей обёртки.
type closeCountingBody struct {
	*bytes.Reader
	closes int32
}

func (b *closeCountingBody) Close() error {
	atomic.AddInt32(&b.closes, 1)
	return nil
}

// TestSendFile_PutRace412 — PUT 412 (имя заняли между HEAD и PUT) → следующий
// кандидат; тело перемотано и не закрыто (повторный PUT несёт тот же файл).
func TestSendFile_PutRace412(t *testing.T) {
	f := newFakeNC(t)
	f.putStatuses = []int{http.StatusPreconditionFailed, http.StatusCreated}
	const content = "race-content"
	body := &closeCountingBody{Reader: bytes.NewReader([]byte(content))}
	res, err := f.client().SendFile(context.Background(), fixtureToken,
		FileUpload{Name: "report.pdf", Size: int64(len(content)), Body: body}, SendFileOpts{ReferenceId: refPlain})
	if err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	if res.Path != "/Talk/report (2).pdf" {
		t.Errorf("Path: got %q, want /Talk/report (2).pdf", res.Path)
	}
	puts := f.byMethod(http.MethodPut, davAnna)
	if len(puts) != 2 {
		t.Fatalf("PUT: %d запросов, want 2 (412 → следующее имя)", len(puts))
	}
	if string(puts[0].Body) != content || string(puts[1].Body) != content {
		t.Errorf("тела PUT: %q / %q, want дважды %q", puts[0].Body, puts[1].Body, content)
	}
	if n := atomic.LoadInt32(&body.closes); n != 0 {
		t.Errorf("тело закрыто %d раз, want 0", n)
	}
}

// TestSendFile_PutRetryRealFile — тот же сценарий 412 на настоящем *os.File:
// без не-закрывающей обёртки второй PUT упал бы «file already closed».
func TestSendFile_PutRetryRealFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "report.pdf")
	if err := os.WriteFile(p, []byte("file-content"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	fh, err := os.Open(p)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer fh.Close()

	f := newFakeNC(t)
	f.putStatuses = []int{http.StatusPreconditionFailed, http.StatusCreated}
	if _, err := f.client().SendFile(context.Background(), fixtureToken,
		FileUpload{Name: "report.pdf", Size: 12, Body: fh}, SendFileOpts{ReferenceId: refPlain}); err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	puts := f.byMethod(http.MethodPut, davAnna)
	if len(puts) != 2 || string(puts[1].Body) != "file-content" {
		t.Fatalf("PUT: %d запросов, want 2 с телом file-content", len(puts))
	}
}

// TestSendFile_MissingFolder — PUT 409 (нет папки вложений) → MKCOL → повтор
// PUT (один раз); MKCOL 405 (гонка) — ок; прочие исходы — ошибка (§3.4).
func TestSendFile_MissingFolder(t *testing.T) {
	cases := []struct {
		name    string
		puts    []int
		mkcol   int
		wantErr string // "" — успех
	}{
		{"409 → MKCOL 201 → PUT 201", []int{409, 201}, 201, ""},
		{"MKCOL 405 (уже есть)", []int{409, 201}, 405, ""},
		{"MKCOL 409 (нет родителя)", []int{409}, 409, "папка вложений /Talk недоступна (MKCOL: HTTP 409"},
		{"PUT 409 после MKCOL", []int{409, 409}, 201, "папка вложений /Talk недоступна (PUT: HTTP 409)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeNC(t)
			f.putStatuses = tc.puts
			f.mkcolStatus = tc.mkcol
			_, err := f.client().SendFile(context.Background(), fixtureToken, upload("report.pdf", "x"), SendFileOpts{ReferenceId: refPlain})
			mk := f.byMethod("MKCOL", davAnna)
			if len(mk) != 1 || mk[0].Path != davAnna+"/Talk" {
				t.Errorf("MKCOL: %v, want один MKCOL %s/Talk", f.sequence(), davAnna)
			}
			shares := len(f.byMethod(http.MethodPost, pathShares))
			if tc.wantErr == "" {
				if err != nil || shares != 1 {
					t.Fatalf("err=%v, шарингов %d; want успех и 1 шаринг", err, shares)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err: got %v, want содержит %q", err, tc.wantErr)
			}
			if shares != 0 {
				t.Errorf("шаринг при ошибке загрузки: %d запросов, want 0", shares)
			}
		})
	}
}

// TestSendFile_DAVErrors — отказы WebDAV → ошибка с методом, путём, кодом,
// подсказкой и s:message; шаринга нет (§3.4).
func TestSendFile_DAVErrors(t *testing.T) {
	cases := []struct {
		status int
		msg    string
		want   []string
	}{
		{413, "Request Entity Too Large", []string{"PUT /Talk/report.pdf: HTTP 413", "лимита", "Request Entity Too Large"}},
		{507, "Insufficient Storage", []string{"HTTP 507", "места", "Insufficient Storage"}},
		{403, "Access denied", []string{"HTTP 403", "нет прав", "Access denied"}},
		{423, "File is locked", []string{"HTTP 423", "заблокирован", "File is locked"}},
		{500, "Internal error", []string{"HTTP 500", "Internal error"}},
	}
	for _, tc := range cases {
		f := newFakeNC(t)
		f.putStatuses = []int{tc.status}
		f.putMessage = tc.msg
		_, err := f.client().SendFile(context.Background(), fixtureToken, upload("report.pdf", "x"), SendFileOpts{})
		if err == nil {
			t.Fatalf("PUT %d: err = nil", tc.status)
		}
		for _, w := range tc.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("PUT %d: err %q не содержит %q", tc.status, err.Error(), w)
			}
		}
		if n := len(f.byMethod(http.MethodPost, pathShares)); n != 0 {
			t.Errorf("PUT %d: шаринг вызван %d раз, want 0", tc.status, n)
		}
	}

	f := newFakeNC(t)
	f.headStatus = http.StatusUnauthorized
	_, err := f.client().SendFile(context.Background(), fixtureToken, upload("report.pdf", "x"), SendFileOpts{})
	if err == nil || !strings.Contains(err.Error(), "HEAD /Talk/report.pdf: HTTP 401") {
		t.Errorf("HEAD 401: err %v, want «HEAD /Talk/report.pdf: HTTP 401»", err)
	}
}

// TestSendFile_ShareNotFoundCleanup — шаринг 404 «Conversation not found» →
// *OCSError{404} (exit 2 в cli) и DELETE загруженного файла (§3.5).
func TestSendFile_ShareNotFoundCleanup(t *testing.T) {
	f := newFakeNC(t)
	f.shareStatus = http.StatusNotFound
	f.shareMessage = "Conversation not found"
	_, err := f.client().SendFile(context.Background(), fixtureToken, upload("report.pdf", "x"), SendFileOpts{})
	var oe *OCSError
	if !errors.As(err, &oe) || oe.Code != http.StatusNotFound {
		t.Fatalf("err: got %v, want *OCSError{404}", err)
	}
	dels := f.byMethod(http.MethodDelete, davAnna)
	if len(dels) != 1 || dels[0].Path != davAnna+"/Talk/report.pdf" {
		t.Errorf("DELETE: %v, want один DELETE %s/Talk/report.pdf", f.sequence(), davAnna)
	}
}

// TestSendFile_ShareErrorCleanupFailed — шаринг 403 и неудачный DELETE → путь
// файла в тексте ошибки (удалить вручную); код ошибки шаринга сохранён.
func TestSendFile_ShareErrorCleanupFailed(t *testing.T) {
	f := newFakeNC(t)
	f.shareStatus = http.StatusForbidden
	f.shareMessage = "Sharing is not allowed"
	f.deleteStatus = http.StatusInternalServerError
	_, err := f.client().SendFile(context.Background(), fixtureToken, upload("report.pdf", "x"), SendFileOpts{})
	if err == nil {
		t.Fatal("err = nil, want ошибку шаринга")
	}
	for _, w := range []string{"Sharing is not allowed", "/Talk/report.pdf", "удалите вручную"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("err %q не содержит %q", err.Error(), w)
		}
	}
	var oe *OCSError
	if !errors.As(err, &oe) || oe.Code != http.StatusForbidden {
		t.Errorf("err: want *OCSError{403} в цепочке, got %v", err)
	}
}

// TestSendFile_LookupSkipsSystem — thread_created того же шаринга несёт тот же
// referenceId и больший id — пропускается: берётся сообщение-файл (§3.6).
func TestSendFile_LookupSkipsSystem(t *testing.T) {
	f := newFakeNC(t)
	res, err := f.client().SendFile(context.Background(), fixtureToken, upload("report.pdf", "x"),
		SendFileOpts{ReferenceId: refThread, ThreadTitle: "Фикстурный тред с файлом"})
	if err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	if res.MessageId != msgThreadRoot {
		t.Errorf("MessageId: got %d, want %d (не system %d)", res.MessageId, msgThreadRoot, msgThreadCreated)
	}
}

// TestSendFile_LookupMiss — сообщения нет в окне или поиск упал: файл уже
// отправлен → error == nil, MessageId 0 и причина в LookupErr (§2.5).
func TestSendFile_LookupMiss(t *testing.T) {
	f := newFakeNC(t)
	res, err := f.client().SendFile(context.Background(), fixtureToken, upload("report.pdf", "x"),
		SendFileOpts{ReferenceId: strings.Repeat("f", 64)})
	if err != nil {
		t.Fatalf("SendFile: %v (промах поиска — не ошибка)", err)
	}
	if res.MessageId != 0 || res.Path != "/Talk/report.pdf" || res.LookupErr == nil ||
		!strings.Contains(res.LookupErr.Error(), "не найдено") {
		t.Errorf("result: got %+v, want MessageId 0, Path /Talk/report.pdf, LookupErr «не найдено»", res)
	}

	f2 := newFakeNC(t)
	f2.chatStatus = http.StatusInternalServerError
	res, err = f2.client().SendFile(context.Background(), fixtureToken, upload("report.pdf", "x"), SendFileOpts{ReferenceId: refPlain})
	if err != nil {
		t.Fatalf("SendFile: %v (сбой поиска — не ошибка)", err)
	}
	if res.MessageId != 0 || res.LookupErr == nil {
		t.Errorf("сбой поиска: got %+v, want MessageId 0 и LookupErr", res)
	}
}

// TestSendFile_PrecheckThread — тред есть (200) → загрузка идёт; нет (404) →
// *OCSError{404} и ни одного HEAD/PUT (§3.3).
func TestSendFile_PrecheckThread(t *testing.T) {
	f := newFakeNC(t)
	if _, err := f.client().SendFile(context.Background(), fixtureToken, upload("report.pdf", "x"),
		SendFileOpts{ThreadId: msgThreadRoot, ReferenceId: refPlain}); err != nil {
		t.Fatalf("тред 200: SendFile: %v", err)
	}
	seq := f.sequence()
	if len(seq) < 4 || seq[2] != "GET "+pathChat+"/"+fixtureToken+"/threads/1502" || !strings.HasPrefix(seq[3], "HEAD ") {
		t.Errorf("тред 200: запросы %q, want threads/1502 перед HEAD", seq)
	}

	f2 := newFakeNC(t)
	f2.threadStatus = http.StatusNotFound
	_, err := f2.client().SendFile(context.Background(), fixtureToken, upload("report.pdf", "x"), SendFileOpts{ThreadId: 999999})
	var oe *OCSError
	if !errors.As(err, &oe) || oe.Code != http.StatusNotFound || !strings.Contains(err.Error(), "тред 999999 не найден") {
		t.Fatalf("тред 404: err %v, want *OCSError{404} «тред 999999 не найден»", err)
	}
	if n := len(f2.byMethod(http.MethodHead, davAnna)) + len(f2.byMethod(http.MethodPut, davAnna)); n != 0 {
		t.Errorf("тред 404: HEAD/PUT %d запросов, want 0", n)
	}
}

// TestSendFile_PrecheckReply — context?limit=0 (§3.3): совпал id и replyable →
// загрузка; сосед, 304, пустой массив, 404 сервера → *OCSError{404};
// isReplyable=false → обычная ошибка. Без загрузки во всех отказах.
func TestSendFile_PrecheckReply(t *testing.T) {
	empty := []byte(`{"ocs":{"meta":{"status":"ok","statuscode":200,"message":"OK"},"data":[]}}`)
	cases := []struct {
		name     string
		fixture  string // фикстура context ("" — тело empty)
		code     int    // HTTP context
		replyTo  int
		want404  bool
		wantText string // "" — успех
	}{
		{"совпал и replyable", "chat_context_message.json", 0, msgCaption, false, ""},
		{"соседнее сообщение", "chat_context_neighbor.json", 0, 999999, true, "сообщение 999999 не найдено"},
		{"304", "chat_context_message.json", http.StatusNotModified, msgCaption, true, "не найдено"},
		{"пустой массив", "", 0, msgCaption, true, "не найдено"},
		{"404 сервера", "chat_context_message.json", http.StatusNotFound, msgCaption, true, "(или комната недоступна)"},
		{"system не replyable", "chat_context_system.json", 0, msgThreadCreated, false, "нельзя ответить"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeNC(t)
			f.context = empty
			if tc.fixture != "" {
				f.context = fileFixture(t, tc.fixture)
			}
			f.contextCode = tc.code
			_, err := f.client().SendFile(context.Background(), fixtureToken, upload("report.pdf", "x"),
				SendFileOpts{ReplyTo: tc.replyTo, ReferenceId: refPlain})
			ctxReqs := f.byMethod(http.MethodGet, pathChat+"/"+fixtureToken+"/")
			if len(ctxReqs) != 1 || ctxReqs[0].Query.Get("limit") != "0" || !strings.HasSuffix(ctxReqs[0].Path, fmt.Sprintf("/%d/context", tc.replyTo)) {
				t.Fatalf("context-запрос: %v", f.sequence())
			}
			heads := len(f.byMethod(http.MethodHead, davAnna))
			if tc.wantText == "" {
				if err != nil || heads != 1 {
					t.Fatalf("err=%v HEAD=%d, want успех и загрузку", err, heads)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantText) {
				t.Fatalf("err: got %v, want содержит %q", err, tc.wantText)
			}
			var oe *OCSError
			if is404 := errors.As(err, &oe) && oe.Code == http.StatusNotFound; is404 != tc.want404 {
				t.Errorf("OCSError{404}: got %v, want %v (err=%v)", is404, tc.want404, err)
			}
			if heads != 0 {
				t.Errorf("загрузка при отказе pre-check: HEAD %d, want 0", heads)
			}
		})
	}
}

// TestSendFile_AttachmentsSettings — allowed=false → ошибка до pre-check и
// загрузки (с ThreadId — ни одного threads/{id}); нет spreed → «Talk
// недоступен» (§3.2, §5 шаг 2).
func TestSendFile_AttachmentsSettings(t *testing.T) {
	f := newFakeNC(t)
	f.caps = capsBody(t, true, false, "/Talk")
	_, err := f.client().SendFile(context.Background(), fixtureToken, upload("report.pdf", "x"), SendFileOpts{ThreadId: msgThreadRoot})
	if err == nil || !strings.Contains(err.Error(), "вложения запрещены") {
		t.Fatalf("allowed=false: err %v, want «вложения запрещены»", err)
	}
	if want := []string{"GET " + pathCloudUser, "GET " + pathCapabilities}; !reflect.DeepEqual(f.sequence(), want) {
		t.Errorf("allowed=false: запросы %q, want %q", f.sequence(), want)
	}

	f2 := newFakeNC(t)
	f2.caps = capsBody(t, false, false, "")
	_, err = f2.client().SendFile(context.Background(), fixtureToken, upload("report.pdf", "x"), SendFileOpts{})
	if err == nil || !strings.Contains(err.Error(), "Talk недоступен") {
		t.Fatalf("нет spreed: err %v, want «Talk недоступен»", err)
	}
}

// TestSendFile_CustomFolderAndUID — нестандартная папка и uid со спецсимволами:
// сегменты экранированы транспортом, path шаринга — от корня пользователя.
func TestSendFile_CustomFolderAndUID(t *testing.T) {
	f := newFakeNC(t)
	f.uid = "anna smith@corp"
	f.user = ocsBody(t, 200, "OK", map[string]any{"id": "anna smith@corp"})
	f.caps = capsBody(t, true, true, "/Docs/Chat/")
	res, err := f.client().SendFile(context.Background(), fixtureToken, upload("отчёт #1.pdf", "x"), SendFileOpts{ReferenceId: refPlain})
	if err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	if res.Path != "/Docs/Chat/отчёт #1.pdf" {
		t.Errorf("Path: got %q", res.Path)
	}
	heads := f.byMethod(http.MethodHead, pathDAVFiles)
	const want = "/remote.php/dav/files/anna%20smith@corp/Docs/Chat/%D0%BE%D1%82%D1%87%D1%91%D1%82%20%231.pdf"
	if len(heads) != 1 || heads[0].EscapedPath != want {
		t.Errorf("HEAD escaped path: %v, want %s", heads, want)
	}
	if got := f.shareBody(t)["path"]; got != "/Docs/Chat/отчёт #1.pdf" {
		t.Errorf("path шаринга: got %q", got)
	}
}

// TestSendFile_Validation — клиентская валидация до сети (§5 шаг 1): ни одного
// запроса.
func TestSendFile_Validation(t *testing.T) {
	ok := upload("report.pdf", "x")
	cases := []struct {
		name string
		f    FileUpload
		opts SendFileOpts
	}{
		{"пустое имя", FileUpload{Name: "", Body: strings.NewReader("")}, SendFileOpts{}},
		{"имя .", FileUpload{Name: ".", Body: strings.NewReader("")}, SendFileOpts{}},
		{"имя ..", FileUpload{Name: "..", Body: strings.NewReader("")}, SendFileOpts{}},
		{"имя с /", FileUpload{Name: "a/b.txt", Body: strings.NewReader("")}, SendFileOpts{}},
		{"нет Body", FileUpload{Name: "a.txt"}, SendFileOpts{}},
		{"Size < 0", FileUpload{Name: "a.txt", Size: -1, Body: strings.NewReader("")}, SendFileOpts{}},
		{"ReplyTo < 0", ok, SendFileOpts{ReplyTo: -1}},
		{"ThreadId < 0", ok, SendFileOpts{ThreadId: -2}},
		{"ReplyTo + ThreadId", ok, SendFileOpts{ReplyTo: 1, ThreadId: 2}},
		{"ReplyTo + ThreadTitle", ok, SendFileOpts{ReplyTo: 1, ThreadTitle: "Т"}},
		{"ThreadId + ThreadTitle", ok, SendFileOpts{ThreadId: 2, ThreadTitle: "Т"}},
	}
	for _, tc := range cases {
		f := newFakeNC(t)
		if _, err := f.client().SendFile(context.Background(), fixtureToken, tc.f, tc.opts); err == nil {
			t.Errorf("%s: err = nil, want ошибку валидации", tc.name)
		}
		if n := len(f.requests()); n != 0 {
			t.Errorf("%s: %d запросов, want 0 (валидация до сети)", tc.name, n)
		}
	}
}

// TestSendFile_EmptyFile — пустой файл допустим (§2.3): PUT с Content-Length 0
// без chunked.
func TestSendFile_EmptyFile(t *testing.T) {
	f := newFakeNC(t)
	if _, err := f.client().SendFile(context.Background(), fixtureToken, upload("empty.txt", ""), SendFileOpts{ReferenceId: refPlain}); err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	put := f.byMethod(http.MethodPut, davAnna)[0]
	if put.ContentLength != 0 || len(put.TransferEncoding) != 0 || len(put.Body) != 0 {
		t.Errorf("PUT пустого файла: ContentLength=%d TransferEncoding=%v body=%d байт", put.ContentLength, put.TransferEncoding, len(put.Body))
	}
}

// deadlineDoer — Doer-обёртка: запоминает методы запросов и остаток срока
// контекста по методу (0 — срока нет).
type deadlineDoer struct {
	mu      sync.Mutex
	methods []string
	left    map[string]time.Duration
	inner   *http.Client
}

func newDeadlineDoer() *deadlineDoer {
	return &deadlineDoer{left: map[string]time.Duration{}, inner: &http.Client{}}
}

func (d *deadlineDoer) Do(r *http.Request) (*http.Response, error) {
	var left time.Duration
	if dl, ok := r.Context().Deadline(); ok {
		left = time.Until(dl)
	}
	d.mu.Lock()
	d.methods = append(d.methods, r.Method)
	d.left[r.Method] = left
	d.mu.Unlock()
	return d.inner.Do(r)
}

// TestSendFile_UploadDoerAndDeadline — PUT идёт через uploadDoer (без общего
// таймаута) со сроком контекста NEXTCLOUD_TIMEOUT + 1 с на 64 КиБ (§4.2);
// остальные запросы — через обычный doer и без контекстного срока.
func TestSendFile_UploadDoerAndDeadline(t *testing.T) {
	f := newFakeNC(t)
	main, up := newDeadlineDoer(), newDeadlineDoer()
	c := NewTalkClientWithDoer(testCfg(f.srv.URL), main) // testCfg: Timeout 5s
	c.uploadDoer = up
	content := strings.Repeat("x", 200<<10) // 200 КиБ → 4 куска по 64 КиБ
	if _, err := c.SendFile(context.Background(), fixtureToken, upload("big.bin", content), SendFileOpts{ReferenceId: refPlain}); err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	want := uploadTimeout(5*time.Second, int64(len(content)))
	if want != 9*time.Second {
		t.Fatalf("uploadTimeout(5s, 200 КиБ) = %v, want 9s", want)
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	main.mu.Lock()
	defer main.mu.Unlock()
	if !reflect.DeepEqual(up.methods, []string{http.MethodPut}) {
		t.Errorf("uploadDoer: запросы %v, want только PUT", up.methods)
	}
	for _, m := range main.methods {
		if m == http.MethodPut {
			t.Errorf("PUT ушёл через обычный doer (Timeout оборвал бы большой файл): %v", main.methods)
		}
	}
	if got := up.left[http.MethodPut]; got <= want-time.Second || got > want {
		t.Errorf("срок PUT: осталось %v, want (%v, %v]", got, want-time.Second, want)
	}
	if got := main.left[http.MethodHead]; got != 0 {
		t.Errorf("HEAD: контекстный срок %v, want нет", got)
	}
}

// TestSendFile_PutRedirect — редиректы PUT (§4.1): чужой хост блокируется без
// пароля в тексте ошибки; 302 на свой хост превращает PUT в GET (200), 307 Go
// не переотправляет — в обоих случаях ошибка, молчаливого успеха и шаринга нет.
func TestSendFile_PutRedirect(t *testing.T) {
	var evilHits int32
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&evilHits, 1)
	}))
	defer evil.Close()

	cases := []struct {
		name string
		to   func(f *fakeNC) string
		code int
		want string
	}{
		{"чужой хост 302", func(*fakeNC) string { return evil.URL + "/leak" }, http.StatusFound, "cross-host redirect blocked"},
		{"свой хост 302 → GET", func(f *fakeNC) string { return f.srv.URL + "/redirected" }, http.StatusFound, "PUT /Talk/report.pdf: HTTP 200"},
		{"свой хост 307", func(f *fakeNC) string { return f.srv.URL + "/redirected" }, http.StatusTemporaryRedirect, "PUT /Talk/report.pdf: HTTP 307"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeNC(t)
			f.putRedirect = tc.to(f)
			f.putRedirectCode = tc.code
			cfg := testCfg(f.srv.URL)
			cfg.Password = "SECRET_MARKER"
			_, err := NewTalkClient(cfg).SendFile(context.Background(), fixtureToken, upload("report.pdf", "x"), SendFileOpts{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err: got %v, want содержит %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "SECRET_MARKER") {
				t.Errorf("УТЕЧКА пароля в тексте ошибки: %q", err.Error())
			}
			if n := len(f.byMethod(http.MethodPost, pathShares)); n != 0 {
				t.Errorf("шаринг после неудачного PUT: %d, want 0", n)
			}
		})
	}
	if n := atomic.LoadInt32(&evilHits); n != 0 {
		t.Errorf("чужой хост получил %d запросов, want 0", n)
	}
}

// TestFileFixtures_LiveFormat — инварианты живого формата (Task 1, спека §1
// «модель»), на которые опираются SendFile и тесты выше: сообщение-файл —
// comment c messageParameters.file (объект), message "{file}" без подписи и
// текст подписи с ней; thread_created — system с тем же referenceId; context —
// одно сообщение. Расхождение с живым API правится здесь и в спеке, не в
// фикстурах.
func TestFileFixtures_LiveFormat(t *testing.T) {
	raw := fileFixture(t, "chat_file_messages.json")
	if bytes.Contains(raw, []byte(`"messageParameters": [{`)) || bytes.Contains(raw, []byte(`"messageParameters":[{`)) {
		t.Error("непустой массив messageParameters — формат /v4/room, в chat-API недопустим")
	}
	var env transport.OCSEnvelope[[]Message]
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode chat_file_messages.json: %v", err)
	}
	byID := map[int]Message{}
	for _, m := range env.OCS.Data {
		byID[m.Id] = m
	}
	if len(byID) != 4 {
		t.Fatalf("сообщений %d, want 4 (1500–1503: только свои)", len(byID))
	}
	for _, id := range []int{msgPlain, msgCaption, msgThreadRoot} {
		m := byID[id]
		if m.MessageType != "comment" || m.SystemMessage != "" || m.MessageParameters["file"].Type != "file" || m.MessageParameters["file"].Name == "" {
			t.Errorf("%d: type=%q system=%q file=%+v, want comment с параметром file", id, m.MessageType, m.SystemMessage, m.MessageParameters["file"])
		}
	}
	if m := byID[msgPlain]; m.Message != "{file}" || m.ReferenceId != refPlain {
		t.Errorf("%d: message=%q ref=%q, want {file}/refPlain", msgPlain, m.Message, m.ReferenceId)
	}
	if m := byID[msgCaption]; m.Message != fixtureCaption || m.ReferenceId != refCaption {
		t.Errorf("%d: message=%q ref=%q, want подпись без {file}/refCaption", msgCaption, m.Message, m.ReferenceId)
	}
	if m := byID[msgThreadRoot]; m.ReferenceId != refThread || m.ThreadTitle == "" {
		t.Errorf("%d: ref=%q threadTitle=%q, want refThread и название треда", msgThreadRoot, m.ReferenceId, m.ThreadTitle)
	}
	if m := byID[msgThreadCreated]; m.MessageType != "system" || m.SystemMessage != "thread_created" || m.ReferenceId != refThread {
		t.Errorf("%d: type=%q system=%q ref=%q, want system thread_created с refThread", msgThreadCreated, m.MessageType, m.SystemMessage, m.ReferenceId)
	}

	for name, want := range map[string]struct {
		id        int
		replyable bool
	}{
		"chat_context_message.json":  {msgCaption, true},
		"chat_context_neighbor.json": {msgNeighbor, true},
		"chat_context_system.json":   {msgThreadCreated, false},
	} {
		var ctxEnv transport.OCSEnvelope[[]Message]
		if err := json.Unmarshal(fileFixture(t, name), &ctxEnv); err != nil {
			t.Fatalf("decode %s: %v", name, err)
		}
		d := ctxEnv.OCS.Data
		if len(d) != 1 || d[0].Id != want.id || d[0].IsReplyable != want.replyable {
			t.Errorf("%s: %d сообщений, первое %+v; want одно id=%d isReplyable=%v", name, len(d), d, want.id, want.replyable)
		}
	}
}
```

- [ ] **Step 2: Прогнать тесты, убедиться в падении**

Run: `CGO_ENABLED=0 go test ./internal/client/ -run 'TestSendFile|TestFileFixtures' -v`

Expected: FAIL — `[build failed]`, строки `undefined: pathDAVFiles`, `undefined: pathCloudUser`, `undefined: pathCapabilities`, `undefined: pathShares`, `undefined: FileUpload`, `f.client().SendFile undefined (type *TalkClient has no field or method SendFile)`, `undefined: SendFileOpts`.

- [ ] **Step 3: Реализовать `internal/client/files.go`** — новый файл целиком:

```go
package client

// files.go — отправка локального файла в чат (спека-дельта 2026-10-09
// `chat send --attach`, §3, §5). Классический механизм Talk: WebDAV-загрузка в
// папку вложений + OCS-шаринг shareType=10. Сообщение в чате создаёт listener
// Talk на событие создания шара, и ответ шаринга id сообщения НЕ содержит —
// сообщение находится поиском по referenceId. Чистые хелперы (имена,
// referenceId, talkMetaData, папка, срок загрузки) — в files_helpers.go.
//
// Безопасность — как во всём пакете (шапка client.go): креды только в
// заголовке Authorization (собирают transport.DoDAV/DoOCS), ошибки WebDAV
// проходят transport.SanitizeErr, dump запросов/ответов запрещён.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"

	"github.com/stas-bool/nctalk-cli/internal/transport"
)

const (
	// pathCloudUser — текущий пользователь (§3.1): ocs.data.id = uid (≠ логин).
	pathCloudUser = "/ocs/v2.php/cloud/user"
	// pathCapabilities — capabilities сервера (§3.2): spreed.config.attachments.
	pathCapabilities = "/ocs/v2.php/cloud/capabilities"
	// pathShares — OCS-шаринг (§3.5).
	pathShares = "/ocs/v2.php/apps/files_sharing/api/v1/shares"
	// pathDAVFiles — база WebDAV файлов пользователя (§3.4): + "/" + uid.
	pathDAVFiles = "/remote.php/dav/files"
	// shareTypeRoom — тип шаринга «в комнату Talk» (IShare::TYPE_ROOM).
	shareTypeRoom = 10
	// lookupWindow — окно поиска id: одна страница из 50 самых свежих
	// сообщений (§3.6, осознанный компромисс).
	lookupWindow = 50
	// maxNameCandidates — потолок подбора имени (§3.4): защита от
	// зацикливания на неисправном сервере.
	maxNameCandidates = 1000
)

// FileUpload — загружаемый файл. Body перематывается (Seek 0) перед каждой
// попыткой PUT: повторы после 409/412.
type FileUpload struct {
	Name string // имя на сервере (filepath.Base локального пути)
	Size int64
	Body io.ReadSeeker
}

// SendFileOpts — параметры SendFile (§5). ReplyTo/ThreadId/ThreadTitle
// взаимоисключаемы (дубль cli-guard-а в validateSendFile).
type SendFileOpts struct {
	Caption     string // пусто → без подписи
	ReplyTo     int
	ThreadId    int
	ThreadTitle string
	Silent      bool
	ReferenceId string // пусто → генерируется
}

// SendFileResult — итог SendFile.
type SendFileResult struct {
	MessageId int    // 0 — сообщение не найдено (файл отправлен), см. LookupErr
	Path      string // итоговый путь на сервере, напр. "/Talk/report (2).pdf"
	LookupErr error  // причина, если MessageId == 0
}

// SendFile загружает файл в папку вложений и публикует его в комнату token
// (спека-дельта 2026-10-09 §5). Шаги:
//
//  1. валидация до сети (дубль cli-guard-ов, прецедент EditMessage);
//  2. uid (§3.1) и настройки вложений (§3.2) — ДО pre-check: при
//     allowed=false файл не отправить ни с какими аргументами, и эта причина
//     не должна маскироваться ошибкой аргумента («тред не найден»);
//  3. pre-check (§3.3): ThreadId → тред существует; ReplyTo → сообщение есть
//     и replyable. Для файлов сервер эти параметры МОЛЧА игнорирует (§1);
//  4. подбор имени + PUT (+ MKCOL при 409) (§3.4);
//  5. шаринг (§3.5); при любой его ошибке — cleanup DELETE файла;
//  6. поиск id по referenceId (§3.6): промах или ошибка поиска — НЕ ошибка
//     (файл уже отправлен): MessageId 0 + LookupErr, error == nil.
//
// «Не найдено» на шагах 3 и 5 — *OCSError{Code: 404} (cli даёт exit 2 через
// существующий exitFromClientErr); прочее — обычные ошибки (exit 1).
func (c *TalkClient) SendFile(ctx context.Context, token string, f FileUpload, opts SendFileOpts) (SendFileResult, error) {
	// 1. Валидация и подготовка — без сети.
	if err := validateSendFile(f, opts); err != nil {
		return SendFileResult{}, err
	}
	// referenceId уходит серверу как задан (нормализует сервер); пустой после
	// нормализации — генерируется (§2.4).
	ref := opts.ReferenceId
	if normalizeReferenceId(ref) == "" {
		gen, err := generateReferenceId()
		if err != nil {
			return SendFileResult{}, err
		}
		ref = gen
	}
	meta, err := encodeTalkMetaData(talkMetaData{
		Caption:     opts.Caption,
		ReplyTo:     opts.ReplyTo,
		ThreadId:    opts.ThreadId,
		ThreadTitle: opts.ThreadTitle,
		Silent:      opts.Silent,
	})
	if err != nil {
		return SendFileResult{}, err
	}

	// 2. uid и настройки вложений.
	uid, err := c.currentUserId(ctx)
	if err != nil {
		return SendFileResult{}, err
	}
	folder, err := c.attachmentFolder(ctx)
	if err != nil {
		return SendFileResult{}, err
	}

	// 3. Pre-check — до загрузки (неверный аргумент не должен оставить файл).
	if opts.ThreadId > 0 {
		if err := c.checkThreadExists(ctx, token, opts.ThreadId); err != nil {
			return SendFileResult{}, err
		}
	}
	if opts.ReplyTo > 0 {
		if err := c.checkReplyable(ctx, token, opts.ReplyTo); err != nil {
			return SendFileResult{}, err
		}
	}

	// 4. Подбор имени и загрузка.
	sharePath, err := c.uploadFile(ctx, uid, folder, f)
	if err != nil {
		return SendFileResult{}, err
	}

	// 5. Публикация; при ошибке — best-effort cleanup тем же ctx (§3.5).
	if err := c.shareToRoom(ctx, token, sharePath, ref, meta); err != nil {
		if derr := c.davDelete(ctx, uid, sharePath); derr != nil {
			return SendFileResult{}, fmt.Errorf("%w; загруженный файл %s не удалён (%v) — удалите вручную", err, sharePath, derr)
		}
		return SendFileResult{}, err
	}

	// 6. Поиск id — промах не ошибка (§2.5).
	id, lerr := c.findFileMessage(ctx, token, normalizeReferenceId(ref))
	return SendFileResult{MessageId: id, Path: sharePath, LookupErr: lerr}, nil
}

// validateSendFile — клиентская валидация SendFile до сети (§5 шаг 1). Имя —
// один сегмент пути: пустое, «.», «..» и с «/» отвергаются (isPlainSegment —
// защитное расширение §5: path.Join схлопнул бы «.»/«..»).
func validateSendFile(f FileUpload, opts SendFileOpts) error {
	if !isPlainSegment(f.Name) {
		return fmt.Errorf("client: SendFile: недопустимое имя файла %q (пустое, «.», «..» или содержит «/»)", f.Name)
	}
	if f.Body == nil {
		return errors.New("client: SendFile: нет содержимого файла (FileUpload.Body == nil)")
	}
	if f.Size < 0 {
		return fmt.Errorf("client: SendFile: отрицательный размер файла %d", f.Size)
	}
	if opts.ReplyTo < 0 || opts.ThreadId < 0 {
		return fmt.Errorf("client: SendFile: id ожидает положительное число (replyTo=%d, threadId=%d)", opts.ReplyTo, opts.ThreadId)
	}
	n := 0
	if opts.ReplyTo > 0 {
		n++
	}
	if opts.ThreadId > 0 {
		n++
	}
	if opts.ThreadTitle != "" {
		n++
	}
	if n > 1 {
		return errors.New("client: SendFile: ReplyTo, ThreadId и ThreadTitle взаимоисключаемы — задайте не больше одного")
	}
	return nil
}

// cloudUser — минимальная форма ocs.data ответа cloud/user: нужен только uid.
type cloudUser struct {
	Id string `json:"id"`
}

// currentUserId — uid текущего пользователя (§3.1): путь WebDAV строится по
// uid, а он ≠ логину (на боевом логин — e-mail). Пустой или не-сегментный id
// — drift формата, ошибка.
func (c *TalkClient) currentUserId(ctx context.Context) (string, error) {
	var u cloudUser
	if _, err := c.doOCS(ctx, http.MethodGet, pathCloudUser, nil, nil, false, &u); err != nil {
		return "", err
	}
	if !isPlainSegment(u.Id) {
		return "", fmt.Errorf("client: SendFile: cloud/user вернул недопустимый id %q (drift формата)", u.Id)
	}
	return u.Id, nil
}

// capabilitiesResp — минимальная форма ocs.data capabilities (§3.2). Spreed —
// указатель: nil = Talk на сервере не установлен/выключен.
type capabilitiesResp struct {
	Capabilities struct {
		Spreed *struct {
			Config struct {
				Attachments struct {
					Allowed bool   `json:"allowed"`
					Folder  string `json:"folder"`
				} `json:"attachments"`
			} `json:"config"`
		} `json:"spreed"`
	} `json:"capabilities"`
}

// attachmentFolder — папка вложений из capabilities (§3.2): нет spreed →
// ошибка; allowed != true → ошибка; folder пуст → /Talk.
func (c *TalkClient) attachmentFolder(ctx context.Context) (string, error) {
	var caps capabilitiesResp
	if _, err := c.doOCS(ctx, http.MethodGet, pathCapabilities, nil, nil, false, &caps); err != nil {
		return "", err
	}
	sp := caps.Capabilities.Spreed
	if sp == nil {
		return "", errors.New("client: SendFile: Talk недоступен на сервере (нет spreed в capabilities)")
	}
	if !sp.Config.Attachments.Allowed {
		return "", errors.New("client: SendFile: вложения запрещены на сервере (spreed.config.attachments.allowed != true)")
	}
	return normalizeAttachmentFolder(sp.Config.Attachments.Folder), nil
}

// checkThreadExists — pre-check треда (§3.3): GET chat/{token}/threads/{id},
// тело 200 не разбирается. 404 (нет треда или комнаты) → *OCSError{404} с
// понятным текстом.
func (c *TalkClient) checkThreadExists(ctx context.Context, token string, threadId int) error {
	// Token в путь КАК ЕСТЬ — эскейп сегмента делает транспорт (инвариант пакета).
	p := pathChat + "/" + token + "/threads/" + strconv.Itoa(threadId)
	if _, err := c.doOCS(ctx, http.MethodGet, p, nil, nil, false, nil); err != nil {
		var oe *OCSError
		if errors.As(err, &oe) && oe.Code == http.StatusNotFound {
			return &OCSError{Code: http.StatusNotFound, Message: fmt.Sprintf("SendFile: тред %d не найден (или комната недоступна)", threadId)}
		}
		return err
	}
	return nil
}

// checkReplyable — pre-check ответа (§3.3): GET chat/{token}/{id}/context
// ?limit=0. Сервер отдаёт БЛИЖАЙШЕЕ сообщение с id <= запрошенного — для
// несуществующего id это сосед, поэтому id сверяется. Нет совпадения, пустой
// массив или 304 → *OCSError{404}; совпало, но isReplyable=false — обычная
// ошибка (exit 1).
func (c *TalkClient) checkReplyable(ctx context.Context, token string, messageId int) error {
	p := pathChat + "/" + token + "/" + strconv.Itoa(messageId) + "/context"
	q := url.Values{}
	q.Set("limit", "0")
	var msgs []Message
	if _, err := c.doOCS(ctx, http.MethodGet, p, q, nil, false, &msgs); err != nil {
		var oe *OCSError
		if errors.As(err, &oe) && oe.Code == http.StatusNotFound {
			return &OCSError{Code: http.StatusNotFound, Message: fmt.Sprintf("SendFile: сообщение %d не найдено (или комната недоступна)", messageId)}
		}
		return err
	}
	for _, m := range msgs {
		if m.Id != messageId {
			continue
		}
		if !m.IsReplyable {
			return fmt.Errorf("client: SendFile: на сообщение %d нельзя ответить (isReplyable=false)", messageId)
		}
		return nil
	}
	return &OCSError{Code: http.StatusNotFound, Message: fmt.Sprintf("SendFile: сообщение %d не найдено", messageId)}
}

// uploadFile подбирает свободное имя и загружает файл (§3.4); возвращает путь
// файла относительно корня пользователя (с ведущим «/») — он же path шаринга.
//
// Кандидаты — по правилу веб-клиента (nameCandidate): HEAD 200 → занято,
// следующий; 404 → PUT с If-None-Match: * (существующее не перезаписывается
// никогда). PUT 412 — имя заняли между HEAD и PUT → следующий кандидат; 409 —
// нет папки вложений (классический путь её не создаёт) → MKCOL и один повтор.
func (c *TalkClient) uploadFile(ctx context.Context, uid, folder string, f FileUpload) (string, error) {
	mkcolTried := false
	for i := 0; i < maxNameCandidates; i++ {
		sharePath := path.Join(folder, nameCandidate(f.Name, i))
		davPath := davFilesPath(uid, sharePath)

		head, err := transport.DoDAV(ctx, c.doer, c.auth, http.MethodHead, davPath, nil, nil, 0)
		if err != nil {
			return "", err
		}
		switch head.StatusCode {
		case http.StatusOK:
			continue // имя занято
		case http.StatusNotFound:
			// свободно — загружаем
		default:
			return "", davStatusErr(http.MethodHead, sharePath, head)
		}

		status, err := c.davPut(ctx, davPath, sharePath, f)
		if err != nil {
			return "", err
		}
		if status == http.StatusConflict && !mkcolTried {
			mkcolTried = true
			if err := c.davMkcol(ctx, uid, folder); err != nil {
				return "", err
			}
			if status, err = c.davPut(ctx, davPath, sharePath, f); err != nil {
				return "", err
			}
		}
		switch status {
		case http.StatusCreated, http.StatusNoContent:
			return sharePath, nil
		case http.StatusPreconditionFailed:
			continue // гонка: имя заняли между HEAD и PUT
		default: // 409 и после MKCOL
			return "", fmt.Errorf("client: SendFile: папка вложений %s недоступна (PUT: HTTP %d)", folder, status)
		}
	}
	return "", fmt.Errorf("client: SendFile: не удалось подобрать свободное имя для %q (проверено %d кандидатов)", f.Name, maxNameCandidates)
}

// davPut — одна попытка PUT (§3.4, §4.2): тело перематывается, срок — по
// размеру файла (uploadTimeout) через контекст, транспорт — uploadDoer без
// общего таймаута. Статусы 201/204/409/412 возвращаются вызывающему (их смысл
// знает цикл подбора имени), прочие — ошибка с s:message.
func (c *TalkClient) davPut(ctx context.Context, davPath, sharePath string, f FileUpload) (int, error) {
	if _, err := f.Body.Seek(0, io.SeekStart); err != nil {
		return 0, fmt.Errorf("client: SendFile: не удалось перемотать файл перед загрузкой: %w", err)
	}
	h := http.Header{}
	h.Set("If-None-Match", "*")
	h.Set("Content-Type", "application/octet-stream")
	putCtx := ctx
	if d := uploadTimeout(c.cfg.Timeout, f.Size); d > 0 {
		var cancel context.CancelFunc
		putCtx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}
	resp, err := transport.DoDAV(putCtx, c.uploadDoer, c.auth, http.MethodPut, davPath, h, f.Body, f.Size)
	if err != nil {
		return 0, err
	}
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusNoContent, http.StatusConflict, http.StatusPreconditionFailed:
		return resp.StatusCode, nil
	}
	return 0, davStatusErr(http.MethodPut, sharePath, resp)
}

// davMkcol создаёт папку вложений (§3.4): 201 — создана, 405 — уже есть
// (гонка) → ок; прочее (в т.ч. 409 — нет родителя у вложенной папки) —
// «папка вложений недоступна».
func (c *TalkClient) davMkcol(ctx context.Context, uid, folder string) error {
	resp, err := transport.DoDAV(ctx, c.doer, c.auth, "MKCOL", davFilesPath(uid, folder), nil, nil, 0)
	if err != nil {
		return err
	}
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusMethodNotAllowed:
		return nil
	}
	return fmt.Errorf("client: SendFile: папка вложений %s недоступна (MKCOL: HTTP %d%s)", folder, resp.StatusCode, davMessageSuffix(resp))
}

// davDelete удаляет загруженный файл (cleanup после неудачного шаринга,
// §3.5): любой 2xx или 404 (файла уже нет) — успех.
func (c *TalkClient) davDelete(ctx context.Context, uid, sharePath string) error {
	resp, err := transport.DoDAV(ctx, c.doer, c.auth, http.MethodDelete, davFilesPath(uid, sharePath), nil, nil, 0)
	if err != nil {
		return err
	}
	if resp.StatusCode/100 == 2 || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return davStatusErr(http.MethodDelete, sharePath, resp)
}

// davFilesPath — путь WebDAV для пути userPath (с ведущим «/») в хранилище
// uid. Сегменты НЕэкранированы — экранирует transport.DoDAV.
func davFilesPath(uid, userPath string) string {
	return pathDAVFiles + "/" + uid + userPath
}

// davStatusErr — ошибка WebDAV-статуса: метод, путь, HTTP-код, подсказка и
// s:message Sabre (§3.4).
func davStatusErr(method, userPath string, resp *transport.DAVResponse) error {
	return fmt.Errorf("client: SendFile: %s %s: HTTP %d%s%s", method, userPath, resp.StatusCode, davStatusHint(resp.StatusCode), davMessageSuffix(resp))
}

// davStatusHint — человекочитаемая подсказка к частым отказам WebDAV.
func davStatusHint(code int) string {
	switch code {
	case http.StatusUnauthorized:
		return " (нет авторизации — проверьте NEXTCLOUD_LOGIN/NEXTCLOUD_PASS)"
	case http.StatusForbidden:
		return " (нет прав на запись)"
	case http.StatusRequestEntityTooLarge:
		return " (файл больше лимита сервера или прокси)"
	case http.StatusLocked:
		return " (файл заблокирован)"
	case http.StatusInsufficientStorage:
		return " (недостаточно места в хранилище)"
	}
	return ""
}

// davMessageSuffix — «: <s:message>» или пусто.
func davMessageSuffix(resp *transport.DAVResponse) string {
	if resp.Message == "" {
		return ""
	}
	return ": " + resp.Message
}

// shareReq — JSON-тело шаринга (§3.5). talkMetaData — JSON-СТРОКА.
type shareReq struct {
	ShareType    int    `json:"shareType"`
	ShareWith    string `json:"shareWith"`
	Path         string `json:"path"`
	ReferenceId  string `json:"referenceId"`
	TalkMetaData string `json:"talkMetaData"`
}

// shareToRoom публикует загруженный файл в комнату (§3.5). Тело ответа
// (share-объект) не разбирается — проверяется только OCS-статус; 404
// «Conversation not found» (нет комнаты / не участник / нет права писать) →
// *OCSError{404}.
func (c *TalkClient) shareToRoom(ctx context.Context, token, sharePath, ref, meta string) error {
	body, err := json.Marshal(shareReq{
		ShareType:    shareTypeRoom,
		ShareWith:    token,
		Path:         sharePath,
		ReferenceId:  ref,
		TalkMetaData: meta,
	})
	if err != nil {
		return fmt.Errorf("client: SendFile: не удалось собрать тело шаринга: %w", err)
	}
	_, err = c.doOCS(ctx, http.MethodPost, pathShares, nil, bytes.NewReader(body), true, nil)
	return err
}

// findFileMessage ищет id сообщения с файлом (§3.6): одна страница GetChat
// (lookupWindow), кандидаты — referenceId == ref и messageType != "system"
// (thread_created того же запроса несёт тот же referenceId), из них —
// наибольший id (не зависит от порядка выдачи). ref — уже нормализованный.
func (c *TalkClient) findFileMessage(ctx context.Context, token, ref string) (int, error) {
	msgs, err := c.GetChat(ctx, token, GetChatOpts{Limit: lookupWindow})
	if err != nil {
		return 0, err
	}
	best := 0
	for _, m := range msgs {
		if m.ReferenceId == ref && m.MessageType != "system" && m.Id > best {
			best = m.Id
		}
	}
	if best == 0 {
		return 0, fmt.Errorf("сообщение с referenceId %s не найдено среди %d последних сообщений комнаты", ref, len(msgs))
	}
	return best, nil
}
```

- [ ] **Step 4: Прогнать тесты — зелёный, gofmt, гейт**

Run: `CGO_ENABLED=0 go test ./internal/client/ -run 'TestSendFile|TestFileFixtures' -v`

Expected: PASS — 23 теста: `TestSendFile_HappyPath`, `…_TalkMetaData`, `…_GeneratedReferenceId`, `…_ReferenceIdNormalized`, `…_UniqueName`, `…_NameCandidatesLimit`, `…_PutRace412`, `…_PutRetryRealFile`, `…_MissingFolder`, `…_DAVErrors`, `…_ShareNotFoundCleanup`, `…_ShareErrorCleanupFailed`, `…_LookupSkipsSystem`, `…_LookupMiss`, `…_PrecheckThread`, `…_PrecheckReply`, `…_AttachmentsSettings`, `…_CustomFolderAndUID`, `…_Validation`, `…_EmptyFile`, `…_UploadDoerAndDeadline`, `…_PutRedirect`, `TestFileFixtures_LiveFormat`.

```sh
gofmt -l internal/client/files.go internal/client/files_test.go
CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./...
```

Expected: `gofmt -l` печатает пусто; гейт зелёный.

- [ ] **Step 5: Commit**

```sh
git add internal/client/files.go internal/client/files_test.go
git commit -m "feat(client): SendFile - загрузка файла и публикация в чат

Классический механизм Talk: uid из cloud/user, папка вложений из
capabilities (allowed до pre-check), pre-check треда и ответа (сервер
для файлов молча игнорирует невалидные), подбор свободного имени
HEAD + PUT If-None-Match: * (412 - следующее имя, 409 - MKCOL и повтор),
шаринг shareType=10 с talkMetaData-строкой и cleanup DELETE при ошибке,
поиск id по referenceId без system-сообщений. Спека-дельта 2026-10-09 §3, §5."
```

---

### Task 5: render + cli — `chat send --attach` (интерфейс, моки, handler, help, e2e)

**Files:**
- Modify: `internal/cli/cli.go` (строка 16 — счётчик; строка 24 — интерфейс)
- Modify: `internal/cli/cli_test.go` (строки 46–48 — стаб `SendFile`)
- Modify: `internal/cli/handlers_reactions_test.go` (строки 61–63 — стаб `SendFile`)
- Modify: `internal/cli/handlers_search_test.go` (строки 53–55 — стаб `SendFile`)
- Modify: `internal/cli/room_test.go` (строки 59–61 — стаб `SendFile`)
- Modify: `internal/cli/handlers_rooms_test.go` (строки 82–84 — стаб `SendFile`)
- Modify: `internal/cli/handlers_chat_test.go` (строки 68–70 — поля spy; строки 100–105 — метод)
- Modify: `internal/render/render_json.go` (строки 59–63); Test: `internal/render/render_test.go` (строки 296–301)
- Modify: `internal/cli/handlers_chat.go` (строки 3–6; строки 15–17; строки 275–307; строки 314–317; строки 345–347; строки 405–408; строки 466–469; строки 508–519)
- Test: `internal/cli/handlers_chat_attach_test.go` (новый), `internal/cli/handlers_chat_attach_unix_test.go` (новый)
- Modify: `internal/cli/help.go` (строка 111; строки 120–125); Test: `internal/cli/help_test.go` (строка 66; строка 180)
- Test: `cmd/nctalk/main_test.go` (импорты — строки 13–20; два теста и хелпер — в конец)

**Interfaces:**
- Consumes:
  - Task 4: `client.FileUpload`, `client.SendFileOpts`, `client.SendFileResult`, `(*client.TalkClient).SendFile`.
  - Существующий cli:
    - `ResolveRoom(ctx, client, positional, nameFlag string, stderr io.Writer) (string, error)`, `exitFromClientErr(error) ExitError`;
    - `render.NewMessageID`, `render.NewMessageIDJSON`, `writeJSON`;
    - `ExitOK`, `ExitGeneric`, `ExitNotFound`, `ExitAmbiguous`.
  - Тестовые хелперы:
    - `newChatSendDeps(spy, stdin io.Reader) Deps` и `newChatDeps(spy) Deps` (Stdin nil), `chatSpyClient{sendCalls, findCalls, findRooms…}` из `handlers_chat_test.go`;
    - `setEnv(t, url)` и `run(args, stdout, stderr, stdin) int` из `cmd/nctalk`.
  - Фикстуры Task 1.
- Produces:

```go
// cli.TalkClient — 11-й метод:
SendFile(ctx context.Context, token string, f client.FileUpload, opts client.SendFileOpts) (client.SendFileResult, error)

// render:
func NewMessageIDNullJSON(w io.Writer) error // {"id": null}

// cli (unexported):
func readCaption(deps Deps, fileFlag, cmd string) (string, error)               // подпись: необязательна, stdin-устройство не читается
func readBody(deps Deps, fileFlag, cmd string, optional bool) (string, error)   // общий блок readMessageBody/readCaption
func isCharDevice(r io.Reader) bool                                             // *os.File на символьном устройстве
func chatSendAttach(ctx context.Context, deps Deps, attachPath, positional, nameFlag, fileFlag string, opts client.SendFileOpts, jsonOut bool) ExitError
var errAttachRepeated error
```

  CLI-контракт (спека §2): `--attach <путь>` / `--attach=<путь>`:
  - повтор или пустое значение → exit 1 до сети;
  - файл проверяется Stat → Open → Stat (не обычный → exit 1), затем читается подпись, разрешается `ResolveRoom` и вызывается `SendFile`;
  - вывод — `<id>\n` / `{"id": <id>}`; при `MessageId == 0` — пустой stdout / `{"id": null}`, предупреждение в stderr и exit 0;
  - `*OCSError{404}` → exit 2.

- [ ] **Step 1: Интерфейс + шесть моков — одним шагом** (без этого пакет `cli` с тестами не соберётся; шаг зелёный сам по себе)

1. `internal/cli/cli.go`, счётчик в комментарии (строка 16 исходного файла) — найти:

```go
// TalkClient — минимальный интерфейс, покрывающий 10 методов реального
```

заменить на:

```go
// TalkClient — минимальный интерфейс, покрывающий 11 методов реального
```

2. `internal/cli/cli.go`, интерфейс `TalkClient` — метод после `SendMessage` (строка 24 исходного файла) — найти:

```go
	SendMessage(ctx context.Context, token string, opts client.SendMessageOpts) (int, error)
```

заменить на:

```go
	SendMessage(ctx context.Context, token string, opts client.SendMessageOpts) (int, error)
	SendFile(ctx context.Context, token string, f client.FileUpload, opts client.SendFileOpts) (client.SendFileResult, error)
```

3. `internal/cli/cli_test.go` — стаб после `ListThreads` (строки 46–48 исходного файла) — найти:

```go
func (m *mockTalkClient) ListThreads(_ context.Context, _ string, _ int) ([]client.ThreadInfo, error) {
	return nil, errMock
}
```

заменить на:

```go
func (m *mockTalkClient) ListThreads(_ context.Context, _ string, _ int) ([]client.ThreadInfo, error) {
	return nil, errMock
}
func (m *mockTalkClient) SendFile(_ context.Context, _ string, _ client.FileUpload, _ client.SendFileOpts) (client.SendFileResult, error) {
	return client.SendFileResult{}, errMock
}
```

4. `internal/cli/handlers_reactions_test.go` — стаб после `ListThreads` (строки 61–63 исходного файла) — найти:

```go
func (m *reactionsSpyClient) ListThreads(_ context.Context, _ string, _ int) ([]client.ThreadInfo, error) {
	return nil, errMock
}
```

заменить на:

```go
func (m *reactionsSpyClient) ListThreads(_ context.Context, _ string, _ int) ([]client.ThreadInfo, error) {
	return nil, errMock
}
func (m *reactionsSpyClient) SendFile(_ context.Context, _ string, _ client.FileUpload, _ client.SendFileOpts) (client.SendFileResult, error) {
	return client.SendFileResult{}, errMock
}
```

5. `internal/cli/handlers_search_test.go` — стаб после `ListThreads` (строки 53–55 исходного файла) — найти:

```go
func (m *searchSpyClient) ListThreads(_ context.Context, _ string, _ int) ([]client.ThreadInfo, error) {
	return nil, errMock
}
```

заменить на:

```go
func (m *searchSpyClient) ListThreads(_ context.Context, _ string, _ int) ([]client.ThreadInfo, error) {
	return nil, errMock
}
func (m *searchSpyClient) SendFile(_ context.Context, _ string, _ client.FileUpload, _ client.SendFileOpts) (client.SendFileResult, error) {
	return client.SendFileResult{}, errMock
}
```

6. `internal/cli/room_test.go` — стаб после `ListThreads` (строки 59–61 исходного файла) — найти:

```go
func (m *roomMockClient) ListThreads(_ context.Context, _ string, _ int) ([]client.ThreadInfo, error) {
	return nil, errMock
}
```

заменить на:

```go
func (m *roomMockClient) ListThreads(_ context.Context, _ string, _ int) ([]client.ThreadInfo, error) {
	return nil, errMock
}
func (m *roomMockClient) SendFile(_ context.Context, _ string, _ client.FileUpload, _ client.SendFileOpts) (client.SendFileResult, error) {
	return client.SendFileResult{}, errMock
}
```

7. `internal/cli/handlers_rooms_test.go` — стаб после `ListThreads` (строки 82–84 исходного файла) — найти:

```go
func (m *roomsSpyClient) ListThreads(_ context.Context, _ string, _ int) ([]client.ThreadInfo, error) {
	return nil, errMock
}
```

заменить на:

```go
func (m *roomsSpyClient) ListThreads(_ context.Context, _ string, _ int) ([]client.ThreadInfo, error) {
	return nil, errMock
}
func (m *roomsSpyClient) SendFile(_ context.Context, _ string, _ client.FileUpload, _ client.SendFileOpts) (client.SendFileResult, error) {
	return client.SendFileResult{}, errMock
}
```

8. `internal/cli/handlers_chat_test.go` — поля `chatSpyClient` (запись `SendFile`) (строки 68–70 исходного файла) — найти:

```go
	// ListThreads-результат
	threadsResult []client.ThreadInfo
	threadsErr    error
```

заменить на:

```go
	// ListThreads-результат
	threadsResult []client.ThreadInfo
	threadsErr    error

	// SendFile-запись (chat send --attach, спека-дельта 2026-10-09)
	gotFileToken   string
	gotFile        client.FileUpload
	gotFileContent string // содержимое Body, прочитанное во время вызова
	gotFileOpts    client.SendFileOpts
	fileCalls      int

	// SendFile-результат
	fileResult client.SendFileResult
	fileErr    error
```

9. `internal/cli/handlers_chat_test.go` — метод-spy после `ListThreads` (`io` в файле уже импортирован) (строки 100–105 исходного файла) — найти:

```go
func (m *chatSpyClient) ListThreads(_ context.Context, token string, limit int) ([]client.ThreadInfo, error) {
	m.threadsCalls++
	m.gotThreadsToken = token
	m.gotThreadsLimit = limit
	return m.threadsResult, m.threadsErr
}
```

заменить на:

```go
func (m *chatSpyClient) ListThreads(_ context.Context, token string, limit int) ([]client.ThreadInfo, error) {
	m.threadsCalls++
	m.gotThreadsToken = token
	m.gotThreadsLimit = limit
	return m.threadsResult, m.threadsErr
}

func (m *chatSpyClient) SendFile(_ context.Context, token string, f client.FileUpload, opts client.SendFileOpts) (client.SendFileResult, error) {
	m.fileCalls++
	m.gotFileToken = token
	m.gotFile = f
	m.gotFileOpts = opts
	if f.Body != nil {
		b, _ := io.ReadAll(f.Body)
		m.gotFileContent = string(b)
	}
	return m.fileResult, m.fileErr
}
```

Run:

```sh
CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./...
grep -n '10 методов' internal/cli/cli.go
```

Expected: гейт зелёный (метод добавлен в интерфейс и во все шесть моков, `cli` его ещё не вызывает); `grep` печатает пусто.

- [ ] **Step 2: Написать падающие тесты** — render, help, cli, e2e.

1. `internal/render/render_test.go` — тест после `TestNewMessageIDJSON` (строки 296–301 исходного файла) — найти:

```go
	if !strings.Contains(buf.String(), `"id"`) {
		t.Errorf("JSON не содержит ключ id:\n%s", buf.String())
	}
}

// ----- Candidates -----
```

заменить на:

```go
	if !strings.Contains(buf.String(), `"id"`) {
		t.Errorf("JSON не содержит ключ id:\n%s", buf.String())
	}
}

// TestNewMessageIDNullJSON — {"id": null}: ключ есть, значение null
// (спека-дельта 2026-10-09 §2.5).
func TestNewMessageIDNullJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := NewMessageIDNullJSON(&buf); err != nil {
		t.Fatalf("NewMessageIDNullJSON: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("Unmarshal: %v\n%s", err, buf.String())
	}
	v, ok := got["id"]
	if !ok || v != nil || len(got) != 1 {
		t.Fatalf("ожидалось ровно {\"id\": null}, получено %s", buf.String())
	}
}

// ----- Candidates -----
```

2. `internal/cli/help_test.go`, канон `TestCmdSpecs_FlagsExactly` (строка 66 исходного файла) — найти:

```go
		"chat send":          {"--name", "--reply-to", "--reference-id", "--silent", "--file", "--thread", "--thread-title"},
```

заменить на:

```go
		"chat send":          {"--name", "--reply-to", "--reference-id", "--silent", "--file", "--thread", "--thread-title", "--attach"},
```

3. `internal/cli/help_test.go`, `TestIsValueFlag` — список флагов со значением (строка 180 исходного файла) — найти:

```go
		"--thread", "--thread-title"}
```

заменить на:

```go
		"--thread", "--thread-title", "--attach"}
```

4. Новый файл `internal/cli/handlers_chat_attach_test.go` целиком:

```go
package cli

// handlers_chat_attach_test.go — `chat send --attach` (спека-дельта
// 2026-10-09 §2, §6, §7): формы флага, guard-ы до сети, проверки файла,
// подпись (необязательная, правило терминала), прокидка опций, вывод и
// exit-коды. Шпион — chatSpyClient (handlers_chat_test.go).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stas-bool/nctalk-cli/internal/client"
)

// attachFile создаёт временный файл name с содержимым content, возвращает путь.
func attachFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

// TestChatSendAttach_Forms — `--attach <путь>` и `--attach=<путь>`: SendFile
// получает token, базовое имя, размер и содержимое; SendMessage не зовётся.
func TestChatSendAttach_Forms(t *testing.T) {
	p := attachFile(t, "report.pdf", "PDF-содержимое")
	for _, args := range [][]string{
		{"TOK", "--attach", p},
		{"TOK", "--attach=" + p},
	} {
		spy := &chatSpyClient{fileResult: client.SendFileResult{MessageId: 901, Path: "/Talk/report.pdf"}}
		deps := newChatSendDeps(spy, strings.NewReader(""))
		ee := chatSendHandler(context.Background(), deps, args, false)
		if ee.Code != ExitOK {
			t.Fatalf("%v: code %d, want 0 (err=%v)", args, ee.Code, ee.Err)
		}
		if spy.fileCalls != 1 || spy.sendCalls != 0 {
			t.Fatalf("%v: SendFile=%d SendMessage=%d, want 1/0", args, spy.fileCalls, spy.sendCalls)
		}
		if spy.gotFileToken != "TOK" || spy.gotFile.Name != "report.pdf" ||
			spy.gotFile.Size != int64(len("PDF-содержимое")) || spy.gotFileContent != "PDF-содержимое" {
			t.Errorf("%v: token=%q name=%q size=%d content=%q", args, spy.gotFileToken, spy.gotFile.Name, spy.gotFile.Size, spy.gotFileContent)
		}
		if out := deps.Stdout.(*bytes.Buffer).String(); out != "901\n" {
			t.Errorf("%v: stdout %q, want \"901\\n\"", args, out)
		}
	}
}

// TestChatSendAttach_FlagErrors — повтор и пустой --attach, конфликты и
// невалидные значения прочих флагов → exit 1 ДО сети (ноль вызовов).
func TestChatSendAttach_FlagErrors(t *testing.T) {
	p := attachFile(t, "a.txt", "x")
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"TOK", "--attach"}, "--attach требует значение"},
		{[]string{"TOK", "--attach", p, "--attach", p}, "только один раз"},
		{[]string{"TOK", "--attach=" + p, "--attach=" + p}, "только один раз"},
		{[]string{"TOK", "--attach="}, "непустой путь"},
		{[]string{"TOK", "--attach", ""}, "непустой путь"},
		{[]string{"TOK", "--attach", p, "--reply-to", "1", "--thread", "2"}, "взаимоисключаемы"},
		{[]string{"TOK", "--attach", p, "--thread", "2", "--thread-title", "Т"}, "взаимоисключаемы"},
		{[]string{"TOK", "--attach", p, "--thread-title", ""}, "не может быть пустым"},
		{[]string{"TOK", "--attach", p, "--reply-to", "0"}, "положительное число"},
		{[]string{"TOK", "--attach", p, "--thread", "abc"}, "целое число"},
		{[]string{"TOK", "--attach", p, "--nope"}, "неизвестный флаг"},
	}
	for _, tc := range cases {
		spy := &chatSpyClient{}
		deps := newChatSendDeps(spy, strings.NewReader("подпись"))
		ee := chatSendHandler(context.Background(), deps, tc.args, false)
		if ee.Code != ExitGeneric || ee.Err == nil || !strings.Contains(ee.Err.Error(), tc.want) {
			t.Errorf("%v: code=%d err=%v, want 1 и «%s»", tc.args, ee.Code, ee.Err, tc.want)
		}
		if spy.fileCalls+spy.sendCalls+spy.findCalls != 0 {
			t.Errorf("%v: вызовы клиента SendFile=%d SendMessage=%d FindRooms=%d, want 0", tc.args, spy.fileCalls, spy.sendCalls, spy.findCalls)
		}
	}
}

// TestChatSendAttach_FileErrors — файл не найден / каталог / нет прав чтения
// → exit 1 до сети: ни FindRooms (с --name), ни SendFile (§2.3).
func TestChatSendAttach_FileErrors(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		path string
		want string
	}{
		{"нет файла", filepath.Join(dir, "missing.pdf"), "no such file"},
		{"каталог", dir, "не обычный файл"},
	}
	if os.Geteuid() != 0 { // root читает файл и без прав
		locked := attachFile(t, "locked.txt", "x")
		if err := os.Chmod(locked, 0o000); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		cases = append(cases, struct{ name, path, want string }{"нет прав чтения", locked, "permission denied"})
	}
	for _, tc := range cases {
		spy := &chatSpyClient{findRooms: []client.Room{{Token: "TOK", DisplayName: "Команда"}}}
		deps := newChatSendDeps(spy, strings.NewReader("подпись"))
		ee := chatSendHandler(context.Background(), deps, []string{"--name", "Команда", "--attach", tc.path}, false)
		if ee.Code != ExitGeneric || ee.Err == nil || !strings.Contains(ee.Err.Error(), tc.want) {
			t.Errorf("%s: code=%d err=%v, want 1 и «%s»", tc.name, ee.Code, ee.Err, tc.want)
		}
		if spy.findCalls != 0 || spy.fileCalls != 0 {
			t.Errorf("%s: FindRooms=%d SendFile=%d, want 0/0 (файл проверяется до сети)", tc.name, spy.findCalls, spy.fileCalls)
		}
	}
}

// TestChatSendAttach_Caption — подпись: stdin или --file, ровно один
// завершающий перевод строки срезается, пустая подпись допустима (§2.2).
func TestChatSendAttach_Caption(t *testing.T) {
	p := attachFile(t, "report.pdf", "x")
	capFile := attachFile(t, "caption.txt", "из файла\r\n")
	cases := []struct {
		name  string
		stdin string
		extra []string
		want  string
	}{
		{"stdin", "Отчёт за сентябрь\n", nil, "Отчёт за сентябрь"},
		{"многострочная", "a\nb\n\n", nil, "a\nb\n"},
		{"пустой stdin", "", nil, ""},
		{"--file", "игнорируется", []string{"--file", capFile}, "из файла"},
	}
	for _, tc := range cases {
		spy := &chatSpyClient{fileResult: client.SendFileResult{MessageId: 1}}
		deps := newChatSendDeps(spy, strings.NewReader(tc.stdin))
		args := append([]string{"TOK", "--attach", p}, tc.extra...)
		if ee := chatSendHandler(context.Background(), deps, args, false); ee.Code != ExitOK {
			t.Fatalf("%s: code %d (err=%v)", tc.name, ee.Code, ee.Err)
		}
		if spy.gotFileOpts.Caption != tc.want {
			t.Errorf("%s: Caption %q, want %q", tc.name, spy.gotFileOpts.Caption, tc.want)
		}
	}
}

// TestIsCharDevice — правило терминала (§2.2) на уровне предиката: символьное
// устройство (/dev/null) — true; пайп, обычный файл, не-*os.File и закрытый
// файл — false.
func TestIsCharDevice(t *testing.T) {
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer devnull.Close()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer pr.Close()
	defer pw.Close()
	regular, err := os.Open(attachFile(t, "r.txt", "x"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer regular.Close()
	closed, err := os.Open(attachFile(t, "c.txt", "x"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	closed.Close()

	cases := []struct {
		name string
		r    io.Reader
		want bool
	}{
		{"/dev/null", devnull, true},
		{"пайп", pr, false},
		{"обычный файл", regular, false},
		{"strings.Reader", strings.NewReader("x"), false},
		{"закрытый файл", closed, false},
	}
	for _, tc := range cases {
		if got := isCharDevice(tc.r); got != tc.want {
			t.Errorf("isCharDevice(%s): got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestChatSendAttach_TerminalStdin — stdin — символьное устройство: при
// --attach подпись не читается (файл уходит без подписи, exit 0), без
// --attach тело читается, как прежде. /dev/null, открытый только на запись, —
// детектор попытки чтения: read(2) на нём — EBADF, так что «прочитал вместо
// правила терминала» и «пропустил чтение тела» видны без терминала.
func TestChatSendAttach_TerminalStdin(t *testing.T) {
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer devnull.Close()
	wronly, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open %s O_WRONLY: %v", os.DevNull, err)
	}
	defer wronly.Close()
	p := attachFile(t, "report.pdf", "x")

	for _, tc := range []struct {
		name  string
		stdin *os.File
	}{{"/dev/null", devnull}, {"/dev/null только на запись", wronly}} {
		spy := &chatSpyClient{fileResult: client.SendFileResult{MessageId: 1}}
		deps := newChatSendDeps(spy, tc.stdin)
		if ee := chatSendHandler(context.Background(), deps, []string{"TOK", "--attach", p}, false); ee.Code != ExitOK {
			t.Fatalf("--attach, %s: code %d (err=%v) — stdin-устройство читалось", tc.name, ee.Code, ee.Err)
		}
		if spy.fileCalls != 1 || spy.gotFileOpts.Caption != "" {
			t.Errorf("--attach, %s: SendFile=%d Caption=%q, want 1 и пусто", tc.name, spy.fileCalls, spy.gotFileOpts.Caption)
		}
	}

	for _, tc := range []struct {
		name  string
		stdin *os.File
		want  string
	}{
		{"/dev/null", devnull, "тело сообщения пусто"},
		{"/dev/null только на запись", wronly, "не удалось прочитать тело"},
	} {
		spy := &chatSpyClient{}
		deps := newChatSendDeps(spy, tc.stdin)
		ee := chatSendHandler(context.Background(), deps, []string{"TOK"}, false)
		if ee.Code != ExitGeneric || ee.Err == nil || !strings.Contains(ee.Err.Error(), tc.want) {
			t.Errorf("без --attach, %s: code=%d err=%v, want 1 «%s»", tc.name, ee.Code, ee.Err, tc.want)
		}
		if spy.sendCalls != 0 {
			t.Errorf("без --attach, %s: SendMessage=%d, want 0", tc.name, spy.sendCalls)
		}
	}
}

// TestChatSendAttach_NilStdinFallback — Deps.Stdin == nil → fallback на
// os.Stdin; правило терминала — на ИТОГОВОМ reader-е (§2.2): os.Stdin —
// /dev/null или терминал → подпись не читается и handler не ждёт ввода;
// os.Stdin — пайп → подпись читается. Подтест «только на запись» ловит
// проверку правила до fallback и без управляющего терминала (чтение = EBADF).
func TestChatSendAttach_NilStdinFallback(t *testing.T) {
	p := attachFile(t, "report.pdf", "x")
	run := func(t *testing.T, stdin *os.File) string {
		t.Helper()
		orig := os.Stdin
		os.Stdin = stdin
		defer func() { os.Stdin = orig }()
		spy := &chatSpyClient{fileResult: client.SendFileResult{MessageId: 1}}
		done := make(chan ExitError, 1)
		go func() {
			done <- chatSendHandler(context.Background(), newChatDeps(spy), []string{"TOK", "--attach", p}, false)
		}()
		select {
		case ee := <-done:
			if ee.Code != ExitOK {
				t.Fatalf("code %d (err=%v)", ee.Code, ee.Err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("handler ждёт ввода: правило терминала проверено не на итоговом reader-е")
		}
		return spy.gotFileOpts.Caption
	}

	t.Run("/dev/null", func(t *testing.T) {
		devnull, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatalf("open %s: %v", os.DevNull, err)
		}
		defer devnull.Close()
		if got := run(t, devnull); got != "" {
			t.Errorf("Caption %q, want пусто", got)
		}
	})
	t.Run("/dev/null только на запись", func(t *testing.T) {
		wronly, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err != nil {
			t.Fatalf("open %s O_WRONLY: %v", os.DevNull, err)
		}
		defer wronly.Close()
		if got := run(t, wronly); got != "" {
			t.Errorf("Caption %q, want пусто", got)
		}
	})
	t.Run("терминал", func(t *testing.T) {
		tty, err := os.Open("/dev/tty")
		if err != nil {
			t.Skipf("нет управляющего терминала: %v", err)
		}
		defer tty.Close()
		if got := run(t, tty); got != "" {
			t.Errorf("Caption %q, want пусто", got)
		}
	})
	t.Run("пайп", func(t *testing.T) {
		pr, pw, err := os.Pipe()
		if err != nil {
			t.Fatalf("pipe: %v", err)
		}
		defer pr.Close()
		if _, err := pw.WriteString("из пайпа\n"); err != nil {
			t.Fatalf("write pipe: %v", err)
		}
		pw.Close()
		if got := run(t, pr); got != "из пайпа" {
			t.Errorf("Caption %q, want «из пайпа»", got)
		}
	})
}

// TestChatSendAttach_OptsPassThrough — --reply-to/--thread/--thread-title/
// --silent/--reference-id доходят до SendFileOpts без изменений.
func TestChatSendAttach_OptsPassThrough(t *testing.T) {
	p := attachFile(t, "report.pdf", "x")
	cases := []struct {
		extra []string
		want  client.SendFileOpts
	}{
		{[]string{"--thread-title", "Тема", "--silent", "--reference-id", "ref-1"},
			client.SendFileOpts{Caption: "подпись", ThreadTitle: "Тема", Silent: true, ReferenceId: "ref-1"}},
		{[]string{"--reply-to", "5"}, client.SendFileOpts{Caption: "подпись", ReplyTo: 5}},
		{[]string{"--thread=7"}, client.SendFileOpts{Caption: "подпись", ThreadId: 7}},
	}
	for _, tc := range cases {
		spy := &chatSpyClient{fileResult: client.SendFileResult{MessageId: 1}}
		deps := newChatSendDeps(spy, strings.NewReader("подпись"))
		if ee := chatSendHandler(context.Background(), deps, append([]string{"TOK", "--attach", p}, tc.extra...), false); ee.Code != ExitOK {
			t.Fatalf("%v: code %d (err=%v)", tc.extra, ee.Code, ee.Err)
		}
		if spy.gotFileOpts != tc.want {
			t.Errorf("%v: opts %+v, want %+v", tc.extra, spy.gotFileOpts, tc.want)
		}
	}
}

// TestChatSendAttach_Output — id найден: текст `<id>\n` / JSON {"id": <id>};
// не найден: пустой stdout / {"id": null}, предупреждение в stderr, exit 0.
func TestChatSendAttach_Output(t *testing.T) {
	p := attachFile(t, "report.pdf", "x")
	lookupErr := errors.New("сообщение с referenceId abc не найдено среди 50 последних сообщений комнаты")
	cases := []struct {
		name       string
		res        client.SendFileResult
		jsonOut    bool
		wantStdout string
		wantStderr string
	}{
		{"id текстом", client.SendFileResult{MessageId: 901}, false, "901\n", ""},
		{"id в JSON", client.SendFileResult{MessageId: 901}, true, "{\n  \"id\": 901\n}\n", ""},
		{"нет id текстом", client.SendFileResult{Path: "/Talk/report.pdf", LookupErr: lookupErr}, false, "",
			"chat send: файл отправлен, но id сообщения не определён: " + lookupErr.Error() + "\n"},
		{"нет id в JSON", client.SendFileResult{Path: "/Talk/report.pdf", LookupErr: lookupErr}, true, "{\n  \"id\": null\n}\n",
			"chat send: файл отправлен, но id сообщения не определён: " + lookupErr.Error() + "\n"},
		{"нет id без причины", client.SendFileResult{}, false, "",
			"chat send: файл отправлен, но id сообщения не определён: причина не указана\n"},
	}
	for _, tc := range cases {
		spy := &chatSpyClient{fileResult: tc.res}
		deps := newChatSendDeps(spy, strings.NewReader(""))
		if ee := chatSendHandler(context.Background(), deps, []string{"TOK", "--attach", p}, tc.jsonOut); ee.Code != ExitOK || ee.Err != nil {
			t.Fatalf("%s: code=%d err=%v, want 0 без ошибки", tc.name, ee.Code, ee.Err)
		}
		if got := deps.Stdout.(*bytes.Buffer).String(); got != tc.wantStdout {
			t.Errorf("%s: stdout %q, want %q", tc.name, got, tc.wantStdout)
		}
		if got := deps.Stderr.(*bytes.Buffer).String(); got != tc.wantStderr {
			t.Errorf("%s: stderr %q, want %q", tc.name, got, tc.wantStderr)
		}
	}
}

// TestChatSendAttach_ClientErrors — *OCSError{404} (комната/тред/сообщение) →
// exit 2; прочие ошибки → 1; --name: 0 совпадений → 2, >1 → 3 без SendFile.
func TestChatSendAttach_ClientErrors(t *testing.T) {
	p := attachFile(t, "report.pdf", "x")
	for _, tc := range []struct {
		err  error
		want int
	}{
		{&client.OCSError{Code: 404, Message: "SendFile: тред 9 не найден (или комната недоступна)"}, ExitNotFound},
		{errors.New("client: SendFile: PUT /Talk/report.pdf: HTTP 507"), ExitGeneric},
		{&client.OCSError{Code: 403, Message: "Forbidden"}, ExitGeneric},
	} {
		spy := &chatSpyClient{fileErr: tc.err}
		deps := newChatSendDeps(spy, strings.NewReader(""))
		if ee := chatSendHandler(context.Background(), deps, []string{"TOK", "--attach", p}, false); ee.Code != tc.want {
			t.Errorf("%v: code %d, want %d", tc.err, ee.Code, tc.want)
		}
	}

	for _, tc := range []struct {
		rooms []client.Room
		want  int
	}{
		{nil, ExitNotFound},
		{[]client.Room{{Token: "A", DisplayName: "Команда 1"}, {Token: "B", DisplayName: "Команда 2"}}, ExitAmbiguous},
	} {
		spy := &chatSpyClient{findRooms: tc.rooms}
		deps := newChatSendDeps(spy, strings.NewReader(""))
		ee := chatSendHandler(context.Background(), deps, []string{"--name", "Команда", "--attach", p}, false)
		if ee.Code != tc.want || spy.fileCalls != 0 {
			t.Errorf("--name (%d комнат): code=%d SendFile=%d, want %d и 0 вызовов", len(tc.rooms), ee.Code, spy.fileCalls, tc.want)
		}
	}
}

// TestChatSendAttach_FileClosed — файл закрывается по завершении handler-а.
func TestChatSendAttach_FileClosed(t *testing.T) {
	p := attachFile(t, "report.pdf", "x")
	spy := &chatSpyClient{fileResult: client.SendFileResult{MessageId: 1}}
	deps := newChatSendDeps(spy, strings.NewReader(""))
	if ee := chatSendHandler(context.Background(), deps, []string{"TOK", "--attach", p}, false); ee.Code != ExitOK {
		t.Fatalf("code %d (err=%v)", ee.Code, ee.Err)
	}
	fh, ok := spy.gotFile.Body.(*os.File)
	if !ok {
		t.Fatalf("Body: got %T, want *os.File", spy.gotFile.Body)
	}
	if _, err := fh.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
		t.Errorf("файл после handler-а: Read err=%v, want os.ErrClosed", err)
	}
}

// TestChatSendAttach_ViaRun — полный путь через Run: роутинг, глобальный --json.
func TestChatSendAttach_ViaRun(t *testing.T) {
	p := attachFile(t, "report.pdf", "x")
	spy := &chatSpyClient{fileResult: client.SendFileResult{MessageId: 901}}
	deps := newChatSendDeps(spy, strings.NewReader("подпись"))
	if code := Run([]string{"chat", "send", "TOK", "--attach", p, "--json"}, deps); code != ExitOK {
		t.Fatalf("Run code %d, stderr=%s", code, deps.Stderr.(*bytes.Buffer).String())
	}
	var got struct {
		Id int `json:"id"`
	}
	if err := json.Unmarshal(deps.Stdout.(*bytes.Buffer).Bytes(), &got); err != nil || got.Id != 901 {
		t.Errorf("stdout %q: id=%d err=%v, want 901", deps.Stdout.(*bytes.Buffer).String(), got.Id, err)
	}
	if spy.gotFileOpts.Caption != "подпись" {
		t.Errorf("Caption %q, want «подпись»", spy.gotFileOpts.Caption)
	}
}
```

5. Новый файл `internal/cli/handlers_chat_attach_unix_test.go` целиком (FIFO без писателя: `os.Open` заблокировался бы навсегда — тест ловит «Open до Stat» по таймауту):

```go
//go:build darwin || linux

package cli

// handlers_chat_attach_unix_test.go — FIFO в `chat send --attach` (спека-дельта
// 2026-10-09 §2.3): syscall.Mkfifo есть только на unix — отдельный файл с
// build-тегом, чтобы пакет собирался везде.

import (
	"context"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestChatSendAttach_FIFO — FIFO без писателя: Stat ДО Open отсекает не-обычный
// файл, и handler не зависает в open(2).
func TestChatSendAttach_FIFO(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Skipf("mkfifo недоступен: %v", err)
	}
	spy := &chatSpyClient{}
	deps := newChatSendDeps(spy, strings.NewReader(""))
	done := make(chan ExitError, 1)
	go func() {
		done <- chatSendHandler(context.Background(), deps, []string{"TOK", "--attach", p}, false)
	}()
	select {
	case ee := <-done:
		if ee.Code != ExitGeneric || ee.Err == nil || !strings.Contains(ee.Err.Error(), "не обычный файл") {
			t.Errorf("code=%d err=%v, want 1 «не обычный файл»", ee.Code, ee.Err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handler завис на FIFO: os.Stat должен идти до os.Open")
	}
	if spy.fileCalls != 0 {
		t.Errorf("SendFile вызван %d раз, want 0", spy.fileCalls)
	}
}
```

6. `cmd/nctalk/main_test.go`, импорты (строки 13–20 исходного файла) — найти:

```go
import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)
```

заменить на:

```go
import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)
```

7. `cmd/nctalk/main_test.go` — дописать в конец файла (через пустую строку):

```go
// attachE2EServer — httptest-сервер шагов SendFile для e2e `chat send
// --attach` (спека-дельта 2026-10-09 §7): cloud/user, capabilities, HEAD 404,
// PUT 201, шаринг, страница chat — живые фикстуры Task 1 из testdata/.
// leakTo непусто → PUT отвечает 302 на этот URL (проверка утечки пароля).
// Возвращает сервер и счётчик запросов шаринга.
func attachE2EServer(t *testing.T, leakTo string) (*httptest.Server, *int32) {
	t.Helper()
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join("..", "..", "testdata", name))
		if err != nil {
			t.Fatalf("read fixture %s: %v", name, err)
		}
		return b
	}
	user := read("cloud_user.json")
	caps := read("capabilities_attachments.json")
	share := read("share_create.json")
	chat := read("chat_file_messages.json")
	var shares int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		writeJSON := func(b []byte) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(b)
		}
		switch {
		case r.Method == http.MethodHead && strings.HasPrefix(p, "/remote.php/dav/files/"):
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPut && strings.HasPrefix(p, "/remote.php/dav/files/"):
			_, _ = io.Copy(io.Discard, r.Body)
			if leakTo != "" {
				http.Redirect(w, r, leakTo, http.StatusFound)
				return
			}
			w.WriteHeader(http.StatusCreated)
		case p == "/ocs/v2.php/cloud/user":
			writeJSON(user)
		case p == "/ocs/v2.php/cloud/capabilities":
			writeJSON(caps)
		case r.Method == http.MethodPost && p == "/ocs/v2.php/apps/files_sharing/api/v1/shares":
			atomic.AddInt32(&shares, 1)
			writeJSON(share)
		case r.Method == http.MethodGet && p == "/ocs/v2.php/apps/spreed/api/v1/chat/tok-team":
			writeJSON(chat)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &shares
}

// TestRun_ChatSendAttach_E2E — полный цикл config → client → cli для
// `chat send <token> --attach <файл> --json`: загрузка, шаринг и поиск id по
// referenceId (фикстура: сообщение 1500 с referenceId 0…01).
func TestRun_ChatSendAttach_E2E(t *testing.T) {
	srv, shares := attachE2EServer(t, "")
	setEnv(t, srv.URL)
	file := filepath.Join(t.TempDir(), "report.pdf")
	if err := os.WriteFile(file, []byte("e2e-содержимое"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	ref := strings.Repeat("0", 63) + "1"

	var out, errOut bytes.Buffer
	code := run([]string{"chat", "send", "tok-team", "--attach", file, "--reference-id", ref, "--json"},
		&out, &errOut, strings.NewReader("подпись e2e"))
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%s", code, errOut.String())
	}
	var got struct {
		Id int `json:"id"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.Id != 1500 {
		t.Errorf("stdout %q: id=%d err=%v, want 1500", out.String(), got.Id, err)
	}
	if n := atomic.LoadInt32(shares); n != 1 {
		t.Errorf("запросов шаринга: %d, want 1", n)
	}
}

// TestRun_PasswordDoesNotLeak_AttachPutRedirect — регресс безопасности
// (спека-дельта 2026-10-09 §4.1) по образцу
// TestRun_PasswordDoesNotLeak_CrossHostRedirect: PUT загрузки получает 302 на
// чужой хост → upload-клиент блокирует редирект, exit 1, SECRET_MARKER нет ни
// в stdout, ни в stderr, шаринга нет.
func TestRun_PasswordDoesNotLeak_AttachPutRedirect(t *testing.T) {
	const secret = "SECRET_MARKER"
	srv, shares := attachE2EServer(t, "https://evil.example.com/leak")
	t.Setenv("NEXTCLOUD_URL", srv.URL)
	t.Setenv("NEXTCLOUD_LOGIN", "user")
	t.Setenv("NEXTCLOUD_PASS", secret)
	t.Setenv("NEXTCLOUD_TIMEOUT", "5s")
	file := filepath.Join(t.TempDir(), "report.pdf")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	var out, errOut bytes.Buffer
	code := run([]string{"chat", "send", "tok-team", "--attach", file}, &out, &errOut, strings.NewReader(""))
	if code != 1 {
		t.Fatalf("exit=%d, want 1 (stderr=%q)", code, errOut.String())
	}
	if strings.Contains(out.String(), secret) || strings.Contains(errOut.String(), secret) {
		t.Errorf("УТЕЧКА: пароль в выводе: stdout=%q stderr=%q", out.String(), errOut.String())
	}
	if !strings.Contains(errOut.String(), "cross-host redirect blocked") {
		t.Errorf("stderr %q, want «cross-host redirect blocked»", errOut.String())
	}
	if n := atomic.LoadInt32(shares); n != 0 {
		t.Errorf("шаринг после заблокированного PUT: %d, want 0", n)
	}
}
```

- [ ] **Step 3: Прогнать тесты, убедиться в падении**

Run: `CGO_ENABLED=0 go test ./internal/render/ ./internal/cli/ ./cmd/nctalk/`

Expected: FAIL —
- `internal/render`: `[build failed]`, `render_test.go:305:12: undefined: NewMessageIDNullJSON`;
- `internal/cli`: `[build failed]`, `handlers_chat_attach_test.go:192:13: undefined: isCharDevice`;
- `cmd/nctalk`: `--- FAIL: TestRun_ChatSendAttach_E2E` (`exit=1, stderr=chat send: неизвестный флаг "--attach"`) и `--- FAIL: TestRun_PasswordDoesNotLeak_AttachPutRedirect`.

- [ ] **Step 4: Реализовать** — render, handler, help.

1. `internal/render/render_json.go` — функция после `NewMessageIDJSON` (строки 59–63 исходного файла) — найти:

```go
func NewMessageIDJSON(w io.Writer, id int) error {
	return writeJSON(w, struct {
		Id int `json:"id"`
	}{Id: id})
}
```

заменить на:

```go
func NewMessageIDJSON(w io.Writer, id int) error {
	return writeJSON(w, struct {
		Id int `json:"id"`
	}{Id: id})
}

// NewMessageIDNullJSON выводит {"id": null} — файл отправлен, но id сообщения
// не определён (спека-дельта 2026-10-09 §2.5: поиск по referenceId промахнулся).
// Ключ id присутствует всегда: потребитель различает «нет id» и «нет ответа».
func NewMessageIDNullJSON(w io.Writer) error {
	return writeJSON(w, struct {
		Id *int `json:"id"`
	}{})
}
```

2. `internal/cli/handlers_chat.go`, шапка файла (строки 3–6 исходного файла) — найти:

```go
// handlers_chat.go — реализации команд `chat show` (Task 4.3), `chat send`
// (Task 4.4, пока stub), `chat threads` (спека-дельта 2026-09-29) и
// `chat edit` (дизайн 2026-09-08). Сигнатуры handler-функций фиксированы
// схемой роутинга cli.go (handlerFn).
```

заменить на:

```go
// handlers_chat.go — реализации команд `chat show` (Task 4.3), `chat send`
// (Task 4.4, пока stub; `--attach` — спека-дельта 2026-10-09), `chat threads`
// (спека-дельта 2026-09-29) и `chat edit` (дизайн 2026-09-08). Сигнатуры
// handler-функций фиксированы схемой роутинга cli.go (handlerFn).
```

3. `internal/cli/handlers_chat.go`, импорт `path/filepath` (строки 15–17 исходного файла) — найти:

```go
	"io"
	"os"
	"strconv"
```

заменить на:

```go
	"io"
	"os"
	"path/filepath"
	"strconv"
```

4. `internal/cli/handlers_chat.go` — `readMessageBody` становится обёрткой общего `readBody`; новые `readCaption` и `isCharDevice`. Попутно исправлен неверный комментарий о production-пути: `main` передаёт `os.Stdin`, `nil` бывает только у тестов и прямых вызовов. Правило терминала проверяется на ИТОГОВОМ reader-е, после fallback (строки 275–307 исходного файла) — найти:

```go
//   - иначе → io.ReadAll(deps.Stdin), при deps.Stdin==nil fallback на os.Stdin
//     (production-путь: main не проставляет Stdin; тесты кладут свой io.Reader);
//   - срезается РОВНО ОДИН завершающий перевод строки (\n или \r\n):
//     `echo "hi" | nctalk chat send` отправил бы "hi\n" (с сохранением сервером
//     \n); многострочные тела (с \n внутри) валидны — пачку не тримим;
//   - пустое тело (пустой stdin/файл или только перевод строки) — ошибка
//     пользователя, не сети; клиент не зовём.
func readMessageBody(deps Deps, fileFlag, cmd string) (string, error) {
	var body []byte
	var readErr error
	if fileFlag != "" {
		body, readErr = os.ReadFile(fileFlag)
	} else {
		stdin := deps.Stdin
		if stdin == nil {
			stdin = os.Stdin
		}
		body, readErr = io.ReadAll(stdin)
	}
	if readErr != nil {
		return "", fmt.Errorf("%s: не удалось прочитать тело: %w", cmd, readErr)
	}
	text := string(body)
	if strings.HasSuffix(text, "\r\n") {
		text = text[:len(text)-2]
	} else if strings.HasSuffix(text, "\n") {
		text = text[:len(text)-1]
	}
	if len(text) == 0 {
		return "", fmt.Errorf("%s: тело сообщения пусто", cmd)
	}
	return text, nil
}
```

заменить на:

```go
//   - иначе → io.ReadAll(deps.Stdin), при deps.Stdin==nil fallback на os.Stdin
//     (main передаёт os.Stdin; nil — у тестов и прямых вызовов handler-а);
//   - срезается РОВНО ОДИН завершающий перевод строки (\n или \r\n):
//     `echo "hi" | nctalk chat send` отправил бы "hi\n" (с сохранением сервером
//     \n); многострочные тела (с \n внутри) валидны — пачку не тримим;
//   - пустое тело (пустой stdin/файл или только перевод строки) — ошибка
//     пользователя, не сети; клиент не зовём.
func readMessageBody(deps Deps, fileFlag, cmd string) (string, error) {
	return readBody(deps, fileFlag, cmd, false)
}

// readCaption — подпись к файлу `chat send --attach` (спека-дельта 2026-10-09
// §2.2): источник и трим — как у readMessageBody, но подпись необязательна
// (пусто → файл без подписи), а stdin-терминал не читается: иначе
// `nctalk chat send x --attach f` в терминале ждал бы Ctrl-D.
func readCaption(deps Deps, fileFlag, cmd string) (string, error) {
	return readBody(deps, fileFlag, cmd, true)
}

// readBody — общий блок readMessageBody/readCaption. optional=true: stdin —
// символьное устройство (терминал, /dev/null) → не читается, результат
// пуст; пустой результат — не ошибка. Правило терминала проверяется на
// ИТОГОВОМ reader-е — после fallback nil → os.Stdin.
func readBody(deps Deps, fileFlag, cmd string, optional bool) (string, error) {
	what := "тело"
	if optional {
		what = "подпись"
	}
	var body []byte
	var readErr error
	if fileFlag != "" {
		body, readErr = os.ReadFile(fileFlag)
	} else {
		stdin := deps.Stdin
		if stdin == nil {
			stdin = os.Stdin
		}
		if optional && isCharDevice(stdin) {
			return "", nil
		}
		body, readErr = io.ReadAll(stdin)
	}
	if readErr != nil {
		return "", fmt.Errorf("%s: не удалось прочитать %s: %w", cmd, what, readErr)
	}
	text := string(body)
	if strings.HasSuffix(text, "\r\n") {
		text = text[:len(text)-2]
	} else if strings.HasSuffix(text, "\n") {
		text = text[:len(text)-1]
	}
	if len(text) == 0 && !optional {
		return "", fmt.Errorf("%s: тело сообщения пусто", cmd)
	}
	return text, nil
}

// isCharDevice — r это *os.File на символьном устройстве (терминал,
// /dev/null). Не *os.File (буфер тестов, strings.Reader) или ошибка Stat →
// false: такой источник читается.
func isCharDevice(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}
```

5. `internal/cli/handlers_chat.go`, doc-комментарий `chatSendHandler` — флаг `--attach` (строки 314–317 исходного файла) — найти:

```go
//   - --silent — отправить без уведомления получателей;
//   - --reference-id <uuid> — клиентский dedup-идентификатор.
//
// Источник тела:
```

заменить на:

```go
//   - --silent — отправить без уведомления получателей;
//   - --reference-id <uuid> — клиентский dedup-идентификатор;
//   - --attach <путь> — отправить файл (спека-дельта 2026-10-09): тело из
//     stdin/--file становится необязательной подписью, ветка — chatSendAttach.
//
// Источник тела:
```

6. `internal/cli/handlers_chat.go`, var-блок `chatSendHandler` (выравнивание — как у соседних строк, gofmt не запускать) (строки 345–347 исходного файла) — найти:

```go
		threadTitle     string
		threadTitleSet  bool
	)
```

заменить на:

```go
		threadTitle     string
		threadTitleSet  bool
		attachPath      string
		attachSet       bool
	)
```

7. `internal/cli/handlers_chat.go`, scan флагов — `--attach` в обеих формах перед веткой «неизвестный флаг» (строки 405–408 исходного файла) — найти:

```go
		case strings.HasPrefix(a, "--reference-id="):
			referenceId = strings.TrimPrefix(a, "--reference-id=")
		case strings.HasPrefix(a, "--"):
			return ExitError{Code: ExitGeneric, Err: fmt.Errorf("chat send: неизвестный флаг %q", a)}
```

заменить на:

```go
		case strings.HasPrefix(a, "--reference-id="):
			referenceId = strings.TrimPrefix(a, "--reference-id=")
		case a == "--attach":
			if i+1 >= len(args) {
				return ExitError{Code: ExitGeneric, Err: errors.New("chat send: --attach требует значение (путь к файлу)")}
			}
			if attachSet {
				return ExitError{Code: ExitGeneric, Err: errAttachRepeated}
			}
			attachPath = args[i+1]
			attachSet = true
			i++
		case strings.HasPrefix(a, "--attach="):
			if attachSet {
				return ExitError{Code: ExitGeneric, Err: errAttachRepeated}
			}
			attachPath = strings.TrimPrefix(a, "--attach=")
			attachSet = true
		case strings.HasPrefix(a, "--"):
			return ExitError{Code: ExitGeneric, Err: fmt.Errorf("chat send: неизвестный флаг %q", a)}
```

8. `internal/cli/handlers_chat.go`, guard 2.7 — после guard-а пустого `--thread-title`, до чтения тела (строки 466–469 исходного файла) — найти:

```go
		return ExitError{Code: ExitGeneric, Err: errors.New("chat send: --thread-title не может быть пустым (сервер безымянные треды не создаёт)")}
	}

	// 3. Чтение тела ДО ResolveRoom: пустое тело отсекается без единого
```

заменить на:

```go
		return ExitError{Code: ExitGeneric, Err: errors.New("chat send: --thread-title не может быть пустым (сервер безымянные треды не создаёт)")}
	}

	// 2.7. --attach (спека-дельта 2026-10-09 §2.1): пустой путь → exit 1 ДО
	// сети. Ветка вложения — chatSendAttach: подпись вместо тела, SendFile
	// вместо SendMessage; прочие флаги и guard-ы выше — общие.
	if attachSet {
		if attachPath == "" {
			return ExitError{Code: ExitGeneric, Err: errors.New("chat send: --attach требует непустой путь к файлу")}
		}
		var positional string
		if len(positionals) > 0 {
			positional = positionals[0]
		}
		return chatSendAttach(ctx, deps, attachPath, positional, nameFlag, fileFlag, client.SendFileOpts{
			ReplyTo:     replyTo,
			ThreadId:    threadId,
			ThreadTitle: threadTitle,
			Silent:      silentFlag,
			ReferenceId: referenceId,
		}, jsonOut)
	}

	// 3. Чтение тела ДО ResolveRoom: пустое тело отсекается без единого
```

9. `internal/cli/handlers_chat.go` — `errAttachRepeated` и `chatSendAttach` после конца `chatSendHandler` (строки 508–519 исходного файла) — найти:

```go
	// 6. Вывод id по ветке jsonOut (спека §6).
	if jsonOut {
		if err := render.NewMessageIDJSON(deps.Stdout, id); err != nil {
			return ExitError{Code: ExitGeneric, Err: err}
		}
	} else {
		if err := render.NewMessageID(deps.Stdout, id); err != nil {
			return ExitError{Code: ExitGeneric, Err: err}
		}
	}
	return ExitError{Code: ExitOK}
}
```

заменить на:

```go
	// 6. Вывод id по ветке jsonOut (спека §6).
	if jsonOut {
		if err := render.NewMessageIDJSON(deps.Stdout, id); err != nil {
			return ExitError{Code: ExitGeneric, Err: err}
		}
	} else {
		if err := render.NewMessageID(deps.Stdout, id); err != nil {
			return ExitError{Code: ExitGeneric, Err: err}
		}
	}
	return ExitError{Code: ExitOK}
}

// errAttachRepeated — повтор --attach (спека-дельта 2026-10-09 §2.1): молча
// брать последний нельзя — повтор означает, что ждали несколько файлов.
var errAttachRepeated = errors.New("chat send: --attach можно указать только один раз (один файл за вызов)")

// chatSendAttach — ветка `chat send --attach <путь>` (спека-дельта 2026-10-09
// §2, §6). Порядок «всё локальное до сети»: файл → подпись → ResolveRoom →
// SendFile → вывод; файл закрывается по завершении.
//
// os.Stat — ДО os.Open: open(2) FIFO без писателя блокируется навсегда, так
// что не-обычный файл отсекается до открытия; повторный Stat открытого
// дескриптора закрывает подмену файла между проверкой и открытием.
func chatSendAttach(ctx context.Context, deps Deps, attachPath, positional, nameFlag, fileFlag string, opts client.SendFileOpts, jsonOut bool) ExitError {
	// 1. Файл: существует, обычный, читается (§2.3).
	st, err := os.Stat(attachPath)
	if err != nil {
		return ExitError{Code: ExitGeneric, Err: fmt.Errorf("chat send: --attach: %w", err)}
	}
	if !st.Mode().IsRegular() {
		return ExitError{Code: ExitGeneric, Err: fmt.Errorf("chat send: --attach: %s — не обычный файл", attachPath)}
	}
	fh, err := os.Open(attachPath)
	if err != nil {
		return ExitError{Code: ExitGeneric, Err: fmt.Errorf("chat send: --attach: %w", err)}
	}
	defer fh.Close()
	if st, err = fh.Stat(); err != nil {
		return ExitError{Code: ExitGeneric, Err: fmt.Errorf("chat send: --attach: %w", err)}
	}
	if !st.Mode().IsRegular() {
		return ExitError{Code: ExitGeneric, Err: fmt.Errorf("chat send: --attach: %s — не обычный файл", attachPath)}
	}

	// 2. Подпись (§2.2): stdin или --file, необязательная.
	caption, err := readCaption(deps, fileFlag, "chat send")
	if err != nil {
		return ExitError{Code: ExitGeneric, Err: err}
	}
	opts.Caption = caption

	// 3. Разрешение <room> — как у текстового chat send.
	token, err := ResolveRoom(ctx, deps.Client, positional, nameFlag, deps.Stderr)
	if err != nil {
		var ee ExitError
		if errors.As(err, &ee) {
			return ee
		}
		return ExitError{Code: ExitGeneric, Err: err}
	}

	// 4. Загрузка и публикация. Имя на сервере — базовое имя локального пути.
	res, err := deps.Client.SendFile(ctx, token, client.FileUpload{
		Name: filepath.Base(attachPath),
		Size: st.Size(),
		Body: fh,
	}, opts)
	if err != nil {
		// «Не найдено» (комната, тред, сообщение) — *OCSError{404} → exit 2.
		return exitFromClientErr(err)
	}

	// 5. Вывод (§2.5). id не определён — файл УЖЕ отправлен: exit 0 и
	// предупреждение в stderr; повтор вызова создал бы дубликат.
	if res.MessageId == 0 {
		reason := "причина не указана"
		if res.LookupErr != nil {
			reason = res.LookupErr.Error()
		}
		fmt.Fprintf(deps.Stderr, "chat send: файл отправлен, но id сообщения не определён: %s\n", reason)
		if jsonOut {
			if err := render.NewMessageIDNullJSON(deps.Stdout); err != nil {
				return ExitError{Code: ExitGeneric, Err: err}
			}
		}
		return ExitError{Code: ExitOK}
	}
	if jsonOut {
		if err := render.NewMessageIDJSON(deps.Stdout, res.MessageId); err != nil {
			return ExitError{Code: ExitGeneric, Err: err}
		}
	} else {
		if err := render.NewMessageID(deps.Stdout, res.MessageId); err != nil {
			return ExitError{Code: ExitGeneric, Err: err}
		}
	}
	return ExitError{Code: ExitOK}
}
```

10. `internal/cli/help.go`, `Short` команды `chat send` (строка 111 исходного файла) — найти:

```go
		Short:       "отправить сообщение (stdin или --file)",
```

заменить на:

```go
		Short:       "отправить сообщение или файл",
```

11. `internal/cli/help.go`, флаг `--attach` и третий пример (строки 120–125 исходного файла) — найти:

```go
			{Name: "--file", Value: "<путь>", Desc: "взять тело из файла (иначе stdin)"},
		},
		Examples: []string{
			"echo \"hi\" | nctalk chat send abc123",
			"nctalk chat send --name \"Команда\" --file msg.txt",
		},
```

заменить на:

```go
			{Name: "--file", Value: "<путь>", Desc: "взять тело из файла (иначе stdin)"},
			{Name: "--attach", Value: "<путь>", Desc: "приложить файл; тело из stdin/--file станет подписью (необязательной)"},
		},
		Examples: []string{
			"echo \"hi\" | nctalk chat send abc123",
			"nctalk chat send --name \"Команда\" --file msg.txt",
			"echo \"Отчёт\" | nctalk chat send abc123 --attach report.pdf",
		},
```

- [ ] **Step 5: Прогнать тесты — зелёный, gofmt новых файлов, гейт**

Run: `CGO_ENABLED=0 go test ./internal/render/ ./internal/cli/ ./cmd/nctalk/ -run 'TestNewMessageIDNullJSON|TestChatSendAttach|TestIsCharDevice|TestCmdSpecs|TestIsValueFlag|TestRun_ChatSendAttach_E2E|TestRun_PasswordDoesNotLeak' -v`

Expected: PASS по трём пакетам. Подтест `TestChatSendAttach_NilStdinFallback/терминал` — SKIP, если нет управляющего терминала (агентская среда, CI); его подстраховывает подтест `/dev/null_только_на_запись`.

```sh
gofmt -l internal/cli/handlers_chat_attach_test.go internal/cli/handlers_chat_attach_unix_test.go \
  internal/render/render_json.go internal/render/render_test.go internal/cli/help.go internal/cli/cli.go \
  internal/cli/handlers_reactions_test.go internal/cli/handlers_search_test.go internal/cli/room_test.go \
  internal/cli/handlers_rooms_test.go cmd/nctalk/main_test.go
CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./...
```

Expected:
- `gofmt -l` печатает пусто: новые файлы чистые, а перечисленные правленые были чистыми и остаются такими. `handlers_chat.go`, `handlers_chat_test.go`, `cli_test.go`, `help_test.go` несут предсуществующий drift и в проверку не входят;
- гейт зелёный.

- [ ] **Step 6: Smoke help** (проверка, не тест)

Run: `CGO_ENABLED=0 go run ./cmd/nctalk chat send --help`

Expected:
- заголовок `nctalk chat send — отправить сообщение или файл`;
- строка флага `--attach <путь>         приложить файл; тело из stdin/--file станет подписью (необязательной)` после `--file`;
- третий пример `echo "Отчёт" | nctalk chat send abc123 --attach report.pdf`;
- exit 0, сеть не нужна.

- [ ] **Step 7: Commit**

```sh
git add internal/cli/cli.go internal/cli/cli_test.go internal/cli/handlers_reactions_test.go \
  internal/cli/handlers_search_test.go internal/cli/room_test.go internal/cli/handlers_rooms_test.go \
  internal/cli/handlers_chat_test.go internal/cli/handlers_chat.go internal/cli/handlers_chat_attach_test.go \
  internal/cli/handlers_chat_attach_unix_test.go internal/cli/help.go internal/cli/help_test.go \
  internal/render/render_json.go internal/render/render_test.go cmd/nctalk/main_test.go
git commit -m "feat(cli): chat send --attach - отправка файла

--attach <путь> / --attach=<путь>: повтор и пустое значение - exit 1 до
сети; файл Stat -> Open -> Stat (FIFO не блокирует); подпись из stdin/--file
необязательна, stdin-устройство (терминал, /dev/null) не читается - правило
на итоговом reader-е после fallback на os.Stdin. Вывод id; id не найден -
пустой stdout (id null в JSON) и предупреждение, exit 0. SendFile - 11-й
метод TalkClient (стабы в пяти моках, spy в chatSpyClient). Help: флаг,
Short, пример. E2E и регресс утечки пароля на редиректе PUT.
Спека-дельта 2026-10-09 §2, §6."
```

---

### Task 6: Интеграционный `TestIntegration_SendFile` (мутационный)

**Files:**
- Modify: `internal/client/integration_test.go` (импорты — строки 20–28; новый тест — в конец файла, после `TestIntegration_Threads`)

**Interfaces:**
- Consumes:
  - существующие хелперы файла: `integrationClient(t)`, `integrationTimeout` (30s);
  - Task 4: `c.SendFile`, `c.currentUserId`, `c.attachmentFolder`, `c.davDelete`, `davFilesPath`;
  - Task 2: `transport.DoDAV`;
  - существующие `c.GetChat` (`GetChatOpts{Limit, ThreadId}`), `c.ListThreads`, `Message.MessageParameters["file"].Name`.
- Produces: `TestIntegration_SendFile` — мутационный e2e по сценарию спеки §7.
  - Защита — как у `TestIntegration_Threads`: `NCTALK_INTEGRATION_SEND=1` + `NCTALK_INTEGRATION_ROOM`, новых env нет.
  - Загруженные файлы удаляются в `t.Cleanup` (best-effort).

- [ ] **Step 1: Написать тест**

1. `internal/client/integration_test.go`, импорты (строки 20–28 исходного файла) — найти:

```go
import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/stas-bool/nctalk-cli/internal/config"
)
```

заменить на:

```go
import (
	"context"
	"errors"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stas-bool/nctalk-cli/internal/config"
	"github.com/stas-bool/nctalk-cli/internal/transport"
)
```

2. Дописать в конец `internal/client/integration_test.go` (через пустую строку):

```go
// TestIntegration_SendFile — мутационный e2e отправки файла (спека-дельта
// 2026-10-09 §7): файл с подписью → id > 0, в истории имя файла и текст
// подписи → то же имя → путь с « (2)» → файл с threadTitle → тред в
// ListThreads → файл в этот тред несёт threadId корня → несуществующий тред →
// 404, файл не загружен. Защита та же, что у TestIntegration_Threads:
// NCTALK_INTEGRATION_SEND=1 + NCTALK_INTEGRATION_ROOM (новых env не вводим).
// Загруженные файлы удаляются через WebDAV (t.Cleanup, best-effort);
// сообщения остаются в integration-комнате («файл больше недоступен»).
func TestIntegration_SendFile(t *testing.T) {
	if got := os.Getenv("NCTALK_INTEGRATION_SEND"); got != "1" {
		t.Skip("NCTALK_INTEGRATION_SEND != 1 — пропускаю мутационный тест SendFile")
	}
	token := os.Getenv("NCTALK_INTEGRATION_ROOM")
	if token == "" {
		t.Skip("NCTALK_INTEGRATION_ROOM не задан — пропускаю SendFile (нет целевой тестовой комнаты)")
	}

	c := integrationClient(t)
	// ~40 запросов к боевому серверу (5 вызовов SendFile по 6–7 шагов + сверки)
	// — общий срок втрое больше обычного.
	ctx, cancel := context.WithTimeout(context.Background(), 3*integrationTimeout)
	defer cancel()

	uid, err := c.currentUserId(ctx)
	if err != nil {
		t.Fatalf("currentUserId: %v", err)
	}
	var uploaded []string
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), integrationTimeout)
		defer ccancel()
		for _, p := range uploaded {
			if err := c.davDelete(cctx, uid, p); err != nil {
				t.Logf("cleanup: %s не удалён: %v", p, err)
			}
		}
	})

	uniq := strconv.FormatInt(time.Now().UnixNano(), 10)
	name := "nctalk-it-" + uniq + ".txt"
	content := "nctalk integration " + uniq + "\n"
	file := func() FileUpload {
		return FileUpload{Name: name, Size: int64(len(content)), Body: strings.NewReader(content)}
	}

	// 1. Файл с подписью → id > 0.
	caption := "nctalk attach it " + uniq
	res, err := c.SendFile(ctx, token, file(), SendFileOpts{Caption: caption})
	if err != nil {
		t.Fatalf("SendFile(caption): %v", err)
	}
	uploaded = append(uploaded, res.Path)
	if res.MessageId <= 0 {
		t.Fatalf("SendFile(caption): MessageId=%d LookupErr=%v, want > 0", res.MessageId, res.LookupErr)
	}
	t.Logf("файл %s → сообщение %d", res.Path, res.MessageId)

	// 2. История: у сообщения messageParameters.file.name = имя, message = подпись.
	msgs, err := c.GetChat(ctx, token, GetChatOpts{Limit: 50})
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	found := false
	for _, m := range msgs {
		if m.Id != res.MessageId {
			continue
		}
		found = true
		if got := m.MessageParameters["file"].Name; got != name {
			t.Errorf("сообщение %d: file.name=%q, want %q", m.Id, got, name)
		}
		if m.Message != caption {
			t.Errorf("сообщение %d: message=%q, want подпись %q", m.Id, m.Message, caption)
		}
	}
	if !found {
		t.Errorf("сообщение %d не найдено в последних 50", res.MessageId)
	}

	// 3. То же имя → « (2)» (существующий файл не перезаписан).
	res2, err := c.SendFile(ctx, token, file(), SendFileOpts{})
	if err != nil {
		t.Fatalf("SendFile(повтор имени): %v", err)
	}
	uploaded = append(uploaded, res2.Path)
	if want := path.Join(path.Dir(res.Path), strings.TrimSuffix(name, ".txt")+" (2).txt"); res2.Path != want {
		t.Errorf("повтор имени: Path=%q, want %q", res2.Path, want)
	}

	// 4. threadTitle → сообщение-файл стал корнем треда: ListThreads видит тред.
	title := "Тред с файлом " + uniq
	root, err := c.SendFile(ctx, token, file(), SendFileOpts{ThreadTitle: title})
	if err != nil {
		t.Fatalf("SendFile(threadTitle): %v", err)
	}
	uploaded = append(uploaded, root.Path)
	if root.MessageId <= 0 {
		t.Fatalf("SendFile(threadTitle): MessageId=%d LookupErr=%v, want > 0", root.MessageId, root.LookupErr)
	}
	threads, err := c.ListThreads(ctx, token, 50)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	threadFound := false
	for _, ti := range threads {
		if ti.Thread.Id == root.MessageId {
			threadFound = true
			if ti.Thread.Title != title {
				t.Errorf("тред %d: Title=%q, want %q", root.MessageId, ti.Thread.Title, title)
			}
		}
	}
	if !threadFound {
		t.Errorf("тред %d не найден в threads/recent (len=%d)", root.MessageId, len(threads))
	}

	// 5. ThreadId → файл в треде: сообщение несёт threadId корня.
	reply, err := c.SendFile(ctx, token, file(), SendFileOpts{ThreadId: root.MessageId})
	if err != nil {
		t.Fatalf("SendFile(threadId=%d): %v", root.MessageId, err)
	}
	uploaded = append(uploaded, reply.Path)
	threadMsgs, err := c.GetChat(ctx, token, GetChatOpts{Limit: 50, ThreadId: root.MessageId})
	if err != nil {
		t.Fatalf("GetChat(threadId=%d): %v", root.MessageId, err)
	}
	replyTh := 0
	for _, m := range threadMsgs {
		if m.Id == reply.MessageId {
			replyTh = m.ThreadId
		}
	}
	if reply.MessageId <= 0 || replyTh != root.MessageId {
		t.Errorf("файл в треде: MessageId=%d threadId=%d, want threadId %d", reply.MessageId, replyTh, root.MessageId)
	}

	// 6. Несуществующий тред → *OCSError{404} до загрузки: файла на сервере нет.
	missing := "nctalk-it-missing-" + uniq + ".txt"
	_, err = c.SendFile(ctx, token, FileUpload{Name: missing, Size: 1, Body: strings.NewReader("x")}, SendFileOpts{ThreadId: 2147483647})
	var oe *OCSError
	if !errors.As(err, &oe) || oe.Code != http.StatusNotFound {
		t.Errorf("несуществующий тред: err=%v, want *OCSError{404}", err)
	}
	folder, err := c.attachmentFolder(ctx)
	if err != nil {
		t.Fatalf("attachmentFolder: %v", err)
	}
	head, err := transport.DoDAV(ctx, c.doer, c.auth, http.MethodHead, davFilesPath(uid, path.Join(folder, missing)), nil, nil, 0)
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	if head.StatusCode != http.StatusNotFound {
		t.Errorf("файл %s загружен вопреки 404 pre-check (HEAD %d)", missing, head.StatusCode)
	}
}
```

- [ ] **Step 2: Компиляция integration-сьюта и gate без env**

```sh
CGO_ENABLED=0 go vet -tags=integration ./internal/client/
env -u NEXTCLOUD_URL -u NEXTCLOUD_LOGIN -u NEXTCLOUD_PASS -u NCTALK_INTEGRATION_SEND -u NCTALK_INTEGRATION_ROOM \
  CGO_ENABLED=0 go test -tags=integration ./internal/client/ -run 'TestIntegration_SendFile$' -v
CGO_ENABLED=0 go test ./... -v 2>&1 | grep -c 'TestIntegration_'
gofmt -l internal/client/integration_test.go
```

Expected:
- `go vet` молчит;
- `--- SKIP: TestIntegration_SendFile` (`NCTALK_INTEGRATION_SEND != 1 — пропускаю мутационный тест SendFile`) и `ok`;
- счётчик `0`: без тега integration-тесты в обычный прогон не попадают;
- `gofmt -l` печатает пусто.

- [ ] **Step 3: Живой прогон** (мутация на боевом: 4 загрузки и 4 сообщения-файла + `thread_created` в integration-комнате; файлы удаляет `t.Cleanup`)

```sh
NCTALK_INTEGRATION_SEND=1 \
NCTALK_INTEGRATION_ROOM='<token integration-комнаты из брифа координатора>' \
CGO_ENABLED=0 go test -tags=integration ./internal/client/ -v -run 'TestIntegration_SendFile$'
```

Expected: `--- PASS: TestIntegration_SendFile` и лог `файл <папка вложений>/nctalk-it-<N>.txt → сообщение <id>`. Папка — из capabilities, обычно `/Talk`; другая папка — не расхождение (строка 19 таблицы Task 1 Step 9).

Если FAIL — сверить с фикстурами Task 1. Живой ответ — истина: исправить код или фикстуру обезличенным реальным ответом, а не подгонять тест; поправить спеку с пометкой «(уточнено по живому API <дата>)» и записать в заметки `## [Task 6] …`.

- [ ] **Step 4: (Необязательно) Smoke CLI на живом сервере** — один вызов shell:

```sh
TOK='<token integration-комнаты из брифа координатора>'
BIN="${TMPDIR:-/tmp}/nctalk-attach-smoke"; F="${TMPDIR:-/tmp}/nctalk-smoke-$(date +%s).txt"
CGO_ENABLED=0 go build -o "$BIN" ./cmd/nctalk && printf 'smoke\n' > "$F"
printf '%s' "nctalk smoke — подпись" | "$BIN" chat send "$TOK" --attach "$F" --json; echo "exit=$?"
"$BIN" chat send "$TOK" --attach "$F" --thread 2147483647 < /dev/null; echo "exit=$?"
# cleanup: удалить загруженный файл (папка — из capabilities, uid — из cloud/user; U, не UID — в zsh UID read-only)
ncurl() { printf 'user = "%s:%s"\n' "$NEXTCLOUD_LOGIN" "$NEXTCLOUD_PASS" | curl -sS -K - "$@"; }
B=${NEXTCLOUD_URL%/}
U=$(ncurl -H 'OCS-APIRequest: true' -H 'Accept: application/json' "$B/ocs/v2.php/cloud/user" | jq -r '.ocs.data.id | @uri')
D=$(ncurl -H 'OCS-APIRequest: true' -H 'Accept: application/json' "$B/ocs/v2.php/cloud/capabilities" \
  | jq -r '.ocs.data.capabilities.spreed.config.attachments.folder // "/Talk" | if . == "" then "/Talk" else sub("/+$"; "") end | split("/") | map(@uri) | join("/")')
ncurl -X DELETE -o /dev/null -w 'DELETE: %{http_code}\n' "$B/remote.php/dav/files/$U$D/$(basename "$F")"
rm -f "$F" "$BIN"
```

Expected:
- JSON `{"id": <N>}` (с отступами, в три строки) и `exit=0`;
- во втором вызове — stderr `client: SendFile: тред 2147483647 не найден (или комната недоступна)` и `exit=2`, файл не загружается;
- `DELETE: 204`.

- [ ] **Step 5: Гейт и коммит**

```sh
CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./...
```

Expected: зелёный.

```sh
git add internal/client/integration_test.go
# если Step 3 вскрыл расхождение — плюс исправленные файлы, спека и docs/superpowers/plans/2026-10-09-nctalk-chat-attach-design.plan.notes.md
git commit -m "test(client): интеграционный TestIntegration_SendFile (мутационный)

Файл с подписью -> id > 0, в истории имя файла и текст подписи -> то же
имя -> путь с (2) -> threadTitle -> тред в ListThreads -> файл в тред
несёт threadId корня -> несуществующий тред -> 404, файл не загружен.
Файлы удаляются через WebDAV в t.Cleanup. Флаги те же:
NCTALK_INTEGRATION_SEND=1 + NCTALK_INTEGRATION_ROOM. Спека-дельта 2026-10-09 §7."
```

---

### Task 7: Сопроводительная документация (спека §8)

**Files:**
- Modify: `README.md` (7 правок)
- Modify: `CLAUDE.md` (6 правок)
- Modify: `docs/integration-run.md` (6 правок)
- Modify (файл ВНЕ репозитория, НЕ коммитится): `~/.claude/skills/nctalk/SKILL.md` (7 правок)

**Interfaces:**
- Consumes: контракт из Task 2–6 — флаг, правило подписи, вывод, exit-коды, таймаут загрузки, инварианты `SendFile`, `TestIntegration_SendFile`.
- Produces: документация, синхронная с кодом. Skill-обёртка знает про `--attach`: без этой правки агент не узнает о возможности (спека §8).

Правки — точными заменами (каждый фрагмент «найти» встречается в файле ровно один раз). Строки — по исходному файлу. Если на Task 1/6 были расхождения — сверить формулировки с итоговым поведением; инвариант «Вложения» в CLAUDE.md поправить по заметкам.

- [ ] **Step 1: `README.md`**

1. README — английское резюме (строка 7) — найти:

```markdown
- 10 primitives over rooms, chat, threads, reactions and search — no business logic, composable in scripts
```

заменить на:

```markdown
- 10 primitives over rooms, chat (incl. file attachments), threads, reactions and search — no business logic, composable in scripts
```

2. README — русский вводный абзац (строка 26, фрагмент внутри строки — заменяется подстрокой) — найти:

```markdown
Только примитивы: список/поиск чатов, участники комнаты, чтение, отправка и редактирование сообщений, треды, реакции, поиск.
```

заменить на:

```markdown
Только примитивы: список/поиск чатов, участники комнаты, чтение, отправка и редактирование сообщений, отправка файлов, треды, реакции, поиск.
```

3. README — таблица окружения, NEXTCLOUD_TIMEOUT (строка 50) — найти:

```markdown
| `NEXTCLOUD_TIMEOUT`  | нет         | HTTP-таймаут, по умолчанию `30s` (Go duration: `45s`, `1m`, `90s`).          |
```

заменить на:

```markdown
| `NEXTCLOUD_TIMEOUT`  | нет         | HTTP-таймаут, по умолчанию `30s` (Go duration: `45s`, `1m`, `90s`); загрузке файла (`chat send --attach`) — плюс 1 с на каждые начатые 64 КиБ. |
```

4. README — секция chat send (строки 121–134) — найти:

````markdown
### `chat send <room>` — отправить сообщение

```sh
echo "Привет" | nctalk chat send kytxaiyc                       # тело из stdin (по умолчанию)
nctalk chat send kytxaiyc --file msg.txt                        # тело из файла
echo "ответ" | nctalk chat send kytxaiyc --reply-to 2927        # ответ на сообщение
echo "тихо"  | nctalk chat send kytxaiyc --silent               # без уведомления
echo "ответ" | nctalk chat send kytxaiyc --thread 2927          # ответ в тред (без цитаты)
echo "текст" | nctalk chat send kytxaiyc --thread-title "Тема"  # создать тред (вывод — id корня)
```

Вывод: `id` отправленного сообщения.

`--reply-to`/`--thread`/`--thread-title` взаимоисключаемы (конфликт → exit `1` до отправки). При создании треда вывод — id корневого сообщения = будущий id треда. `--reply-to` на сообщение треда кладёт ответ в тред с цитатой (сервер).
````

заменить на:

````markdown
### `chat send <room>` — отправить сообщение или файл

```sh
echo "Привет" | nctalk chat send kytxaiyc                       # тело из stdin (по умолчанию)
nctalk chat send kytxaiyc --file msg.txt                        # тело из файла
echo "ответ" | nctalk chat send kytxaiyc --reply-to 2927        # ответ на сообщение
echo "тихо"  | nctalk chat send kytxaiyc --silent               # без уведомления
echo "ответ" | nctalk chat send kytxaiyc --thread 2927          # ответ в тред (без цитаты)
echo "текст" | nctalk chat send kytxaiyc --thread-title "Тема"  # создать тред (вывод — id корня)
nctalk chat send kytxaiyc --attach report.pdf                   # файл без подписи
echo "Отчёт" | nctalk chat send kytxaiyc --attach report.pdf    # файл с подписью (stdin или --file)
nctalk chat send kytxaiyc --attach build.log --thread 2927      # файл в тред
```

Вывод: `id` отправленного сообщения.

`--reply-to`/`--thread`/`--thread-title` взаимоисключаемы (конфликт → exit `1` до отправки). При создании треда вывод — id корневого сообщения = будущий id треда. `--reply-to` на сообщение треда кладёт ответ в тред с цитатой (сервер).

`--attach <путь>` — отправить локальный файл, один за вызов. Файл загружается в папку вложений Talk (по умолчанию `/Talk`; занятое имя → `имя (2).ext`, существующие файлы не перезаписываются) и публикуется в чат. Текст из stdin/`--file` становится подписью — необязательной: stdin-терминал не читается, без пайпа файл уходит без подписи. `--reply-to`/`--thread` проверяются до загрузки: «не найдено» → exit `2`. Вывод — `id` сообщения с файлом; если id найти не удалось, файл всё равно отправлен: exit `0`, пустой stdout (`{"id": null}` в `--json`) и предупреждение в stderr — повторный вызов создал бы дубликат. Срок загрузки — `NEXTCLOUD_TIMEOUT` плюс 1 с на каждые начатые 64 КиБ файла.
````

5. README — exit-коды, код 2 (строка 181) — найти:

```markdown
| `2` | not found — комната/сообщение не найдены, либо 0 совпадений по `--name`        |
```

заменить на:

```markdown
| `2` | not found — комната/сообщение/тред не найдены, либо 0 совпадений по `--name`   |
```

6. README — тесты (строка 197, фрагмент внутри строки — заменяется подстрокой) — найти:

```markdown
требуют env-кредов; отправка сообщения — под флагом.
```

заменить на:

```markdown
требуют env-кредов; мутационные (отправка сообщений и файлов, треды) — под флагом.
```

7. README — архитектура, internal/client (строка 205) — найти:

```markdown
- `internal/client` — `TalkClient` (HTTP, Basic Auth, OCS-конверт, redact).
```

заменить на:

```markdown
- `internal/client` — `TalkClient` (HTTP, Basic Auth, OCS-конверт, WebDAV-загрузка вложений, redact).
```

- [ ] **Step 2: `CLAUDE.md`**

1. CLAUDE.md — первый абзац, перечень дельт (строка 5, фрагмент внутри строки — заменяется подстрокой) — найти:

```markdown
(`rooms participants`) и `docs/superpowers/specs/2026-09-29-nctalk-threads-design.md` (`chat threads` + `--thread`/`--thread-title`; читай при любой правке поведения).
```

заменить на:

```markdown
(`rooms participants`), `docs/superpowers/specs/2026-09-29-nctalk-threads-design.md` (`chat threads` + `--thread`/`--thread-title`) и `docs/superpowers/specs/2026-10-09-nctalk-chat-attach-design.md` (`chat send --attach`; читай при любой правке поведения).
```

2. CLAUDE.md — интеграционные мутации (строка 27, фрагмент внутри строки — заменяется подстрокой) — найти:

```markdown
мутации (`SendMessage`/`EditMessage`) — под флагом
```

заменить на:

```markdown
мутации (`SendMessage`/`EditMessage`/`Threads`/`SendFile`) — под флагом
```

3. CLAUDE.md — методы client (строка 34, фрагмент внутри строки — заменяется подстрокой) — найти:

```markdown
Методы над OCS: `ListRooms/FindRooms/SearchRooms/GetChat/SendMessage/EditMessage/GetReactions/GetParticipants/ListThreads/SearchMessages`.
```

заменить на:

```markdown
Методы над OCS: `ListRooms/FindRooms/SearchRooms/GetChat/SendMessage/SendFile/EditMessage/GetReactions/GetParticipants/ListThreads/SearchMessages` (`SendFile` — OCS + WebDAV через `transport.DoDAV`).
```

4. CLAUDE.md — transport (строка 37, фрагмент внутри строки — заменяется подстрокой) — найти:

```markdown
HTTP/OCS-транспорт (`DoOCS`, конверт `ocs.data`, Basic-auth + OCS-заголовки, redirect-политика)
```

заменить на:

```markdown
HTTP/OCS-транспорт (`DoOCS`, конверт `ocs.data`, Basic-auth + OCS-заголовки, redirect-политика), WebDAV-транспорт `DoDAV` (HTTP-статус — вызывающему, `s:message` Sabre)
```

5. CLAUDE.md — инвариант «Вложения» (строка 48, фрагмент внутри строки — заменяется подстрокой) — найти:

```markdown
send в несуществующий тред = HTTP 400 (`{"error":"reply-to"}`), НЕ 404 → exit `1`.
```

заменить на:

```markdown
send в несуществующий тред = HTTP 400 (`{"error":"reply-to"}`), НЕ 404 → exit `1`.
- **Вложения** (спека-дельта 2026-10-09, `chat send --attach`): классический механизм Talk — WebDAV PUT в папку вложений (`spreed.config.attachments.folder`, дефолт `/Talk`) + OCS-шаринг `shareType=10`; `talkMetaData` — JSON-СТРОКА (`caption`/`replyTo`/`threadId`/`threadTitle`/`silent`, пусто — `"{}"`); conversation subfolders Talk 24 отклонены. Ответ шаринга id сообщения НЕ содержит → поиск `GetChat{Limit: 50}` по нормализованному `referenceId` (`trim(substr(ref, 0, 64))`), system того же запроса (`thread_created`) пропускаются, из кандидатов — max id; промах — exit `0` + предупреждение (`{"id": null}`): файл уже отправлен, повтор дал бы дубликат. Для файлов сервер МОЛЧА игнорирует невалидные `replyTo`/`threadId` → pre-check до загрузки (`threads/{id}`; `{id}/context?limit=0` отдаёт СОСЕДА для несуществующего id — сверять id и `isReplyable`). Порядок `SendFile`: валидация → uid + настройки вложений → pre-check → имя + PUT (+MKCOL) → шаринг (+cleanup DELETE) → поиск id. PUT всегда с `If-None-Match: *` (существующее не перезаписывается; 412 → следующее имя ` (N)` по правилу веб-клиента), `Content-Length` без chunked, тело — `io.NopCloser` (Transport закрыл бы `*os.File`, повтор после 409/412 упал бы), пустой файл — `http.NoBody`. Логин ≠ uid: путь WebDAV — по `GET /cloud/user` → `id`. Классический путь папку вложений НЕ создаёт — MKCOL по 409, один раз. PUT — через отдельный upload-клиент без `Timeout` (срок — контекст: `NEXTCLOUD_TIMEOUT` + 1 с на 64 КиБ). Подпись необязательна; stdin — символьное устройство (терминал, `/dev/null`) → при `--attach` не читается; правило — на итоговом reader-е после fallback `nil → os.Stdin`. В cli `os.Stat` до `os.Open` (FIFO без писателя блокирует open). После любой ошибки шаринга — cleanup DELETE (неудача → путь файла в тексте ошибки).
```

6. CLAUDE.md — Документы (строка 52, фрагмент внутри строки — заменяется подстрокой) — найти:

```markdown
`2026-09-29-nctalk-threads-design.md` — `chat threads` + `--thread`/`--thread-title`; исторические
```

заменить на:

```markdown
`2026-09-29-nctalk-threads-design.md` — `chat threads` + `--thread`/`--thread-title`; `2026-10-09-nctalk-chat-attach-design.md` — `chat send --attach`; исторические
```

- [ ] **Step 3: `docs/integration-run.md`**

1. integration-run — первый абзац (строки 7–10) — найти:

```markdown
Все сценарии читающие и безопасные, кроме мутационных `TestIntegration_SendMessage`
(отправка), `TestIntegration_EditMessage` (правка) и `TestIntegration_Threads`
(создание треда и ответа) — они меняют состояние чата
и защищены отдельным флагом.
```

заменить на:

```markdown
Все сценарии читающие и безопасные, кроме мутационных `TestIntegration_SendMessage`
(отправка), `TestIntegration_EditMessage` (правка), `TestIntegration_Threads`
(создание треда и ответа) и `TestIntegration_SendFile` (загрузка и публикация
файлов) — они меняют состояние чата (и хранилища файлов) и защищены отдельным флагом.
```

2. integration-run — env SEND (строка 28) — найти:

```markdown
| `NCTALK_INTEGRATION_SEND`  | (ноль)   | `1` — включить мутационные `TestIntegration_SendMessage`/`TestIntegration_EditMessage`/`TestIntegration_Threads`. |
```

заменить на:

```markdown
| `NCTALK_INTEGRATION_SEND`  | (ноль)   | `1` — включить мутационные `TestIntegration_SendMessage`/`TestIntegration_EditMessage`/`TestIntegration_Threads`/`TestIntegration_SendFile`. |
```

3. integration-run — env ROOM (строка 29) — найти:

```markdown
| `NCTALK_INTEGRATION_ROOM`  | (пусто)  | Token тестовой комнаты для мутационных `SendMessage`/`EditMessage`/`Threads`. |
```

заменить на:

```markdown
| `NCTALK_INTEGRATION_ROOM`  | (пусто)  | Token тестовой комнаты для мутационных `SendMessage`/`EditMessage`/`Threads`/`SendFile`. |
```

4. integration-run — регулярка мутации (строки 67–71) — найти:

````markdown
  -run 'TestIntegration_(SendMessage|EditMessage|Threads)'
```

Без `NCTALK_INTEGRATION_SEND=1` или без `NCTALK_INTEGRATION_ROOM` тесты
молча скипаются.
````

заменить на:

````markdown
  -run 'TestIntegration_(SendMessage|EditMessage|Threads|SendFile)'
```

Без `NCTALK_INTEGRATION_SEND=1` или без `NCTALK_INTEGRATION_ROOM` тесты
молча скипаются. `TestIntegration_SendFile` удаляет загруженные файлы через
WebDAV (`t.Cleanup`, best-effort — в корзину пользователя); сообщения с
файлами остаются в комнате («файл больше недоступен»).
````

5. integration-run — безопасность (строка 87) — найти:

```markdown
- **Мутация** (`SendMessage`/`EditMessage`/`Threads`) — POST/PUT в чат; выполняются только
```

заменить на:

```markdown
- **Мутация** (`SendMessage`/`EditMessage`/`Threads`/`SendFile`) — POST/PUT в чат (и WebDAV-загрузка в папку вложений у `SendFile`); выполняются только
```

6. integration-run — таблица тестов (строка 104) — найти:

```markdown
| `TestIntegration_Threads` | `POST /chat/{token}` + `threads/recent` + `GET /chat?threadId` | Создание треда (`threadTitle`) → ответ (`threadId`) → `ListThreads`: `numReplies >= 1`, title совпадает → `GetChat(threadId)`: корень и ответ в выдаче → общая история: `threadReplies >= 1` у корня, `threadId` корня у ответа. Мутация — под флагом. |
```

заменить на:

```markdown
| `TestIntegration_Threads` | `POST /chat/{token}` + `threads/recent` + `GET /chat?threadId` | Создание треда (`threadTitle`) → ответ (`threadId`) → `ListThreads`: `numReplies >= 1`, title совпадает → `GetChat(threadId)`: корень и ответ в выдаче → общая история: `threadReplies >= 1` у корня, `threadId` корня у ответа. Мутация — под флагом. |
| `TestIntegration_SendFile` | WebDAV `PUT`/`HEAD`/`DELETE` + `POST …/files_sharing/api/v1/shares` + `GET /chat` | Файл с подписью → `id > 0`, в истории `file.name` и текст подписи → то же имя → путь с ` (2)` → `threadTitle` → тред в `ListThreads` → файл в тред несёт `threadId` корня → несуществующий тред → 404, файл не загружен. Файлы удаляются в `t.Cleanup`. Мутация — под флагом. |
```

- [ ] **Step 4: `~/.claude/skills/nctalk/SKILL.md`** (вне git: правится, но не коммитится)

1. SKILL.md — description (строка 3, фрагмент внутри строки — заменяется подстрокой) — найти:

```markdown
написать или отправить сообщение в чат, ответить, проверить непрочитанные
```

заменить на:

```markdown
написать или отправить сообщение в чат, отправить файл, ответить, проверить непрочитанные
```

2. SKILL.md — триггеры (строка 3, фрагмент внутри строки — заменяется подстрокой) — найти:

```markdown
Триггеры: «напиши в чат», «отправь сообщение», «ответь в чат»,
```

заменить на:

```markdown
Триггеры: «напиши в чат», «отправь сообщение», «ответь в чат», «отправь файл», «скинь лог/скрин в чат», «приложи файл», «ответь файлом в тред»,
```

3. SKILL.md — первый абзац (перечень примитивов) (строка 8, фрагмент внутри строки — заменяется подстрокой) — найти:

```markdown
Тонкий CLI: 10 примитивов (комнаты, чтение, отправка, правка, реакции, поиск, участники, треды).
```

заменить на:

```markdown
Тонкий CLI: 10 примитивов (комнаты, чтение, отправка сообщений и файлов, правка, реакции, поиск, участники, треды).
```

4. SKILL.md — правило 6 (строка 25) — найти:

```markdown
6. **Тело сообщения — stdin или `--file`**, флага `--text` и позиционного текста нет: `printf '%s' "текст" | nctalk chat send <token>`.
```

заменить на:

```markdown
6. **Тело сообщения — stdin или `--file`**, флага `--text` и позиционного текста нет: `printf '%s' "текст" | nctalk chat send <token>`. **Файл — `--attach <путь>`** (один за вызов); stdin/`--file` тогда — необязательная подпись.
```

5. SKILL.md — сценарий 1 (строки 40–43) — найти:

````markdown
printf '%s' "Не срочно" | nctalk chat send kytxaiyc --silent             # без уведомления
```

Вывод — `id` отправленного (с `--json` удобно парсить). Проверка доставки: `nctalk chat show kytxaiyc --last 10`.
````

заменить на:

````markdown
printf '%s' "Не срочно" | nctalk chat send kytxaiyc --silent             # без уведомления
printf '%s' "Лог деплоя" | nctalk chat send kytxaiyc --attach /tmp/deploy.log --json   # файл с подписью
nctalk chat send kytxaiyc --attach screen.png --thread 2927 --json      # файл в тред, без подписи
```

Вывод — `id` отправленного (с `--json` удобно парсить). Проверка доставки: `nctalk chat show kytxaiyc --last 10`. У `--attach` ответ `{"id": null}` с exit 0 — файл отправлен, но id не найден: НЕ повторяй (будет дубликат), проверь `chat show --last 10`.
````

6. SKILL.md — шпаргалка chat send (строка 86) — найти:

```markdown
| `chat send <room>` | отправить | тело stdin/`--file`; `--reply-to id`, `--silent`, `--thread id` (ответ в тред), `--thread-title "Тема"` (создать) — взаимоисключаемы |
```

заменить на:

```markdown
| `chat send <room>` | отправить | тело stdin/`--file`; `--reply-to id`, `--silent`, `--thread id` (ответ в тред), `--thread-title "Тема"` (создать) — взаимоисключаемы; `--attach <путь>` — файл (stdin/`--file` → подпись) |
```

7. SKILL.md — exit 2 (строка 100) — найти:

```markdown
| 2 | not found: комната/сообщение, 0 совпадений `--name` | проверить имя/token |
```

заменить на:

```markdown
| 2 | not found: комната/сообщение/тред, 0 совпадений `--name` | проверить имя/token/id |
```

- [ ] **Step 5: Проверить, что правки на месте**

```sh
grep -c -- '--attach' README.md ~/.claude/skills/nctalk/SKILL.md
grep -c 'SendFile' CLAUDE.md docs/integration-run.md
grep -c '2026-10-09-nctalk-chat-attach-design' CLAUDE.md
grep -rn '10 методов' --include='*.go' internal/ cmd/
```

Expected (`grep -c` считает строки):
- `README.md:5` и `…/SKILL.md:5`;
- `CLAUDE.md:3` и `docs/integration-run.md:7`;
- `2` (без имени файла);
- последний `grep` печатает пусто (счётчик методов обновлён в Task 5).

Если значение меньше — пропущена правка из Step 1–4.

- [ ] **Step 6: Финальный прогон**

```sh
CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 go vet -tags=integration ./internal/client/
```

Expected: всё зелёное.

- [ ] **Step 7: Commit** (только файлы репозитория — SKILL.md вне git)

```sh
git add README.md CLAUDE.md docs/integration-run.md
git commit -m "docs: отправка файлов - README/CLAUDE/integration-run

README: --attach в секции chat send (подпись, примеры, id null, срок
загрузки), тред в exit 2, таймаут. CLAUDE.md: спека-дельта, SendFile в
методах client, DoDAV в transport, инвариант «Вложения».
integration-run: TestIntegration_SendFile в мутационные и таблицу.
Спека-дельта 2026-10-09 §8."
```

---

## Definition of Done (сводка проверок)

1. Гейт `CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./...` зелёный после каждой из 7 задач, у каждой — свой коммит:
   - Conventional Commits обычным текстом, без emoji и без AI-атрибуции;
   - только явные пути;
   - `TODO.md` не тронут.
2. Integration-сьют компилируется (`go vet -tags=integration`). Без тега в обычный прогон он не попадает. Без env `TestIntegration_SendFile` — SKIP.
3. Фикстуры Task 1 — обезличенные живые ответы, снятые ДО тестов:
   - 11 файлов в `testdata/`;
   - `leak: clean`;
   - URL — только `https://nc.example.org` и `http://sabredav.org`;
   - только свои сообщения;
   - загруженные файлы удалены с сервера, сырьё удалено.
4. `chat send <room> --attach <путь>`:
   - загружает файл и печатает id; JSON — `{"id": <id>}`;
   - подпись из stdin/`--file` необязательна, терминал и `/dev/null` не читаются;
   - `--reply-to`/`--thread` проверяются до загрузки: «не найдено» → exit 2, не replyable → exit 1;
   - `--thread-title` делает файл корнем треда;
   - занятое имя → ` (N)`, существующие файлы не перезаписываются;
   - id не найден → exit 0, пустой stdout / `{"id": null}` и предупреждение;
   - без `--attach` поведение `chat send` не изменилось.
5. Exit-коды 0/1/2/3 — по таблице спеки §2.6. Регресс-тесты утечки пароля зелёные: `TestRun_PasswordDoesNotLeak_CrossHostRedirect`, `TestRun_PasswordDoesNotLeak_AttachPutRedirect`, `TestSendFile_PutRedirect`, `TestDoDAV_TransportErrorSanitized`, `TestDoDAV_CrossHostRedirectBlocked`.
6. `cli.TalkClient` — 11 методов, комментарий в `cli.go` честный; шесть моков реализуют `SendFile`. `nctalk chat send --help` показывает `--attach`, канон `TestCmdSpecs_FlagsExactly` и `TestIsValueFlag` зелёные.
7. `TestIntegration_SendFile` проходит на живом сервере с `NCTALK_INTEGRATION_SEND=1` + `NCTALK_INTEGRATION_ROOM`.
8. Новые файлы gofmt-чистые, предсуществующий drift не тронут: `gofmt -l .` печатает ровно 10 файлов из Global Constraints, не больше.
9. README, CLAUDE.md и `docs/integration-run.md` синхронны с кодом; `~/.claude/skills/nctalk/SKILL.md` обновлён (вне репозитория).
10. Каждое расхождение с живым API отражено:
    - в спеке — пометкой «(уточнено по живому API <дата>)»;
    - в заметках — `docs/superpowers/plans/2026-10-09-nctalk-chat-attach-design.plan.notes.md`.
