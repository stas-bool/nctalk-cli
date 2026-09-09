# Дизайн: `nctalk rooms participants` — список участников комнаты

- **Дата:** 2026-09-09
- **Статус:** дизайн одобрен пользователем; реализация через /spec-to-code
- **Базовая спека:** `docs/superpowers/specs/2026-07-17-nctalk-cli-design.md` (эталон контракта
  базового CLI; этот документ — дельта, добавляющая девятую команду)
- **Стек:** Go 1.21+, только stdlib, `CGO_ENABLED=0`

## 1. Цель и контекст

Добавить в `nctalk` примитив «список участников комнаты». Закрывает боль из TODO: сегодня
присутствие человека в комнате приходится доказывать косвенно (system-сообщения `user_added`),
а прямого способа нет. Команда read-only: без мутаций, без мутационных env-флагов.

Выбор подхода (брейншторм 2026-09-09):

- **A (принято): verb `participants` у ресурса `rooms`** — симметрично `list/find/search`;
  имя предопределено TODO. Read-only GET.
- B (отклонено): флаг `--participants` у `rooms list`/`find` — ломает семантику list
  (список комнат, не людей).
- C (отклонено): новый top-level ресурс — ломает паттерн `<resource> <verb>`.

Решение пользователя по развилкам:

1. Вывод — **имя + роль + онлайн + ID** (actorId нужен агенту для упоминаний и `--from`;
   полный ответ API доступен через `--json`).

### Out of scope

- Остальные строки TODO — `rooms mark-read` и `reactions add/remove` (мутации, отдельные
  дельты).
- Мутации участников: приглашение/исключение/повышение до модератора.
- Права (`permissions`, `attendeePermissions`), `attendeePin`, `phoneNumber`, `callId` —
  не входят в каноническую модель и в текстовый вывод.
- Онлайн-статус по `lastPing` (эвристика «недавно пинговался») — используется только
  `sessionIds` (живая сессия = онлайн).
- Поведение на former-комнате, разрезолвленной через `--name`: `FindRooms` ищет по всем
  комнатам включая former (`IncludeFormer: true`), а в покинутой комнате запрашивающий —
  не участник. Ожидаем OCS-ошибку → exit `1`/`2` по общему контракту, специальной
  обработки нет; проверка на живом сервере — вне этой дельты.

## 2. Контракт команды

```
nctalk rooms participants <room> [flags]
```

- `<room>` — token комнаты позиционно **или** `--name "<имя>"` — то же правило разрешения,
  что у `chat show` (базовая спека §7): позиционный приоритетен (при конфликте —
  предупреждение в stderr, `--name` игнорируется); `--name` 1 совпадение → ок, >1 →
  кандидаты в stderr, exit `3`; 0 → exit `2`; оба пусты → exit `1` «укажите token
  позиционно или --name».
- Позиционные: первый = `<room>`; лишние (второй и далее) игнорируются — единообразно с
  остальными командами.
- Флаги: `--name <имя>`. Других нет. Глобальный `--json` — в любой позиции (извлекается
  слоем `Run` до роутинга, как везде).

### Вывод

Текст — таблица в стиле `rooms list` (tabwriter, заголовок капсом), сортировка: роль
(`participantType` по возрастанию) → имя (case-insensitive; ключ сортировки —
итоговое имя колонки: fallback `actorId` при пустом `displayName` применяется ДО
сортировки, чтобы безымянные гости не скучковались как пустая строка). Сортировка —
клиентская, прецедент «клиентская пост-обработка» — фильтры `rooms list`:

```
ИМЯ                     РОЛЬ        ОНЛАЙН   ID
Анна Смирнова           владелец    нет      anna.s
Борис Крылов            модератор   нет      boris.k
Вера Тимофеева          участник    да       vera.t
```

- **ИМЯ** — `displayName`; пустое (гость без имени) → `actorId` — тот же fallback, что в
  `reactions get`.
- **РОЛЬ** — текст по `participantType` (значения — §3): 1→`владелец`, 2→`модератор`,
  3→`участник`, 4→`гость`, 5→`по ссылке`, 6→`гость-модератор`; **неизвестное число →
  печатается само число** (format-drift-safe: новые значения сервера не ломают вывод).
- **ОНЛАЙН** — `да`/`нет`: непустой `sessionIds` = есть живая сессия.
- **ID** — `actorId`.

`--json` — отсортированный так же массив канонических объектов `Participant` (§4):
`actorType, actorId, displayName, participantType, sessionIds, inCall, lastPing`.
Сырой ответ API НЕ проксируется — тонкий клиент отдаёт каноническую модель (как `Room`).

Пустой список участников → пустой stdout, exit `0`, одинаково для текста и `--json`
(поисковая семантика `rooms list`; на практике недостижимо при позиционном token — сам
запрашивающий всегда участник доступной ему комнаты; исключение — former-комната,
разрезолвленная через `--name`, см. Out of scope).

### Exit-коды (наследуют базовый контракт §7, без изменений)

| Ситуация | Код |
|---|---|
| успех (в т.ч. пустой список) | `0` |
| неизвестный флаг / сеть / 401 / 5xx / оба пусты (нет `<room>` и `--name`) | `1` |
| `404` OCS (комната не найдена) · 0 совпадений `--name` | `2` |
| >1 совпадение `--name` (кандидаты в stderr) | `3` |

## 3. API-контракт (Talk room-API)

Эндпоинт **сверен с живым сервером 2026-09-09** (GET, HTTP 200, реальные аккаунты/комнаты):

```
GET /ocs/v2.php/apps/spreed/api/v4/room/{token}/participants
```

- Заголовки: `OCS-APIRequest: true`, `Accept: application/json` + Basic-auth — путь
  `doOCS`, как у `ListRooms` (GET без mutate).
- Ответ `ocs.data` — массив участников; наблюдаемые на живом поля одного участника:
  `roomToken, inCall, lastPing, sessionIds, participantType, attendeeId, actorId,
  actorType, displayName, permissions, attendeePermissions, attendeePin, phoneNumber,
  callId`. Клиент декодирует только семь канонических полей (§4), остальные игнорирует.
- Наблюдения с живого:
  - `sessionIds` — массив строк; пустой `[]` = офлайн (у одного участника может быть
    несколько сессий).
  - В one-to-one оба участника имеют `participantType=1` (обе стороны — «владельцы»).
  - Значения 1/2/3 подтверждены живым ответом; 4–6 в живой выборке не встретились —
    берутся из официальной документации Talk (константы participant types):
    1=OWNER, 2=MODERATOR, 3=USER, 4=GUEST, 5=USER_FOLLOWING_LINK, 6=GUEST_MODERATOR.
  - `displayName` может быть пустым (гости без имени).
- Пагинации нет (сервер отдаёт всех участников одной выдачей; комнат с сотнями участников
  у этого пользователя нет, рост обрабатывает сервер).

## 4. Слой client

Новый файл `internal/client/participants.go` (своя группа эндпоинта — по аналогии с
`reactions.go`; НЕ пихать в `rooms.go`):

```go
// Participant — каноническое представление участника комнаты (спека-дельта §3–4).
type Participant struct {
    ActorType       string   `json:"actorType"`       // "users" / "guests" / "emails" / ...
    ActorId         string   `json:"actorId"`         // для guests — "guest::<anon-id>" (assumption, см. ниже)
    DisplayName     string   `json:"displayName"`     // может быть пустым (гость)
    ParticipantType int      `json:"participantType"` // 1–6, см. §3
    SessionIds      []string `json:"sessionIds"`      // непустой = онлайн
    InCall          int      `json:"inCall"`          // 0 = не в звонке
    LastPing        int64    `json:"lastPing"`        // СЕКУНДЫ Unix, 0 = никогда
}

func (c *TalkClient) GetParticipants(ctx context.Context, token string) ([]Participant, error)
```

- Запрос: `GET pathRooms + "/" + url.PathEscape(token) + "/participants"` (`pathRooms` —
  существующая константа `/ocs/v2.php/apps/spreed/api/v4/room`). Guard-ов до сети нет —
  read-only GET, как `GetReactions` (непустоту token гарантирует `ResolveRoom`).
- Разбор: `doOCS` → `[]Participant`.
- Нормализация после decode: `SessionIds == nil` → пустой слайс; сам слайс участников
  `nil` → пустой (его делает Unmarshal при `data: null` — guard `len(data)>0`
  пропускает `RawMessage("null")`, прецедент nil-map у `GetReactions`).
  Детерминированный `--json`: `[]`, не `null`.
- Пустой ответ сервера (`data: []` ИЛИ `data: null`) → пустой (non-nil) слайс, nil
  error.
- OCS-error — стандартная обработка `doOCS` → `*client.OCSError` (404 → exit 2 в cli).
- Метод добавляется в интерфейс `cli.TalkClient` (`internal/cli/cli.go`) — compile-time
  проверка `var _ TalkClient = (*client.TalkClient)(nil)` ловит рассинхрон. Это ломает
  компиляцию всех существующих тестовых реализаций интерфейса — stub-правки в §6.
- Формат гостевых полей (`actorId` "guest::<anon-id>", пустой `displayName`) —
  assumption: источник — reactions-эндпоинт (`ReactionActor`) + документация; на этом
  эндпоинте живьем не сверен (§3, живая выборка гостей не содержала). При первом живом
  прогоне комнаты с гостем — сверить фактические поля.

## 5. Слой cli

- `roomsParticipantsHandler` в `internal/cli/handlers_rooms.go` — по образцу
  `roomsFindHandler` + room-resolution из `chatShowHandler`. Порядок шагов:
  1. ручной scan флагов (`--name`, обе формы `--flag value` / `--flag=value`; неизвестный
     `--*` → exit `1`);
  2. первый позиционный = room, лишние игнорируются;
  3. `ResolveRoom(room, nameFlag)` — коды `1/2/3` сохраняются;
  4. `GetParticipants`;
  5. сортировка `sort.SliceStable`: `ParticipantType` asc → итоговое имя
     (`DisplayName`, при пустом — `ActorId`) case-insensitive asc — ключ §2
     (fallback до сортировки; stable — равные сохраняют порядок сервера);
  6. вывод: `render.ParticipantsTable` / `render.ParticipantsJSON` по `jsonOut`; пустой
     список → пустой stdout, exit `0` (как `rooms list`).
- Роутинг: `"participants": roomsParticipantsHandler` в `routes["rooms"]`; подсказка
  «ожидается verb (list/find/search/show/send/edit/get)» в `cli.go` дополняется
  `participants`.
- Help: запись в `cmdSpecs` (`internal/cli/help.go`, позиция — сразу после `rooms search`:
  порядок слайса = порядок общего help) — единый источник правды:

  ```
  Path: ["rooms", "participants"]
  Short: "участники комнаты"
  UsageExtras: "<room>"
  Flags: --name <имя> — разрешить комнату по имени (вместо token)
  Examples:
    nctalk rooms participants abc123
    nctalk rooms participants --name "Команда"
  ```

  Детальные/resource help и анти-drift тесты (`TestCmdSpecs_CoverAllRoutes`,
  `TestHandleHelp_DetailedContent`, `TestAntiDrift_...`) подхватывают декларацию
  автоматически. Исключение — пять канонов с hardcoded-структурой, правятся руками (шаги —
  §6): `TestCmdSpecs_OrderMatchesHelpOrder`, `TestCmdSpecs_FlagsExactly`,
  `buildPositionalArgs` и два Run-level канона в `internal/cli/cli_test.go` —
  `TestRunRoutesAllStubs`, `TestRunRoutesToCorrectHandler` (таблицы по 8 кейсов; без
  добавления кейса новый verb не покрыт на уровне роутинга).
- `render` (`render.go` / `render_json.go`):
  - `ParticipantsTable(w, ps)` — tabwriter, заголовок `ИМЯ\tРОЛЬ\tОНЛАЙН\tID`; имя —
    fallback `actorId` при пустом `displayName`; роль — текст по мапе §2 (маппинг живёт
    в render — это представление, не модель); онлайн — `да`/`нет`.
  - `ParticipantsJSON(w, ps)` — `writeJSON` по образцу `RoomsJSON`.

### Сопроводительные правки документации (входят в задачу)

- README: секция `rooms participants` по образцу `rooms find` + упоминание в списке
  команд, если список где-то перечисляет все команды.
- CLAUDE.md: «8 команд» → «9 команд» (первый абзац).
- `TODO.md`: удалить строку про участников (задача закрыта).
- Комментарии-счётчики «8 команд/8 методов» в коде: `internal/cli/help.go` (док-комментарий
  cmdSpecs), `internal/cli/cli.go` (интерфейс TalkClient), `internal/cli/cli_test.go`,
  `internal/cli/help_test.go` — заменить на 9. Комментарии, не поведение, но это тот же
  drift, который проект ловит анти-drift-тестами. Исключение — `cli_test.go`: там
  правка НЕ только комментарий, «все 8 команд» честен над таблицей из 8 кейсов только
  вместе с добавлением кейсов в Run-level каноны (§6).
- `docs/integration-run.md`: строка нового read-only теста в таблицу тестов + имя в
  batch-регулярку «Все читающие сценарии» и в per-test примеры — иначе
  документированный прогон «все read-only одной командой» не включит новый тест
  (мутационная семантика не меняется — флагов не добавляется).
- Skill-обёртка `nctalk` (`~/.claude/skills/nctalk/SKILL.md` — файл ВНЕ репозитория):
  «8 примитивов» → 9, строка `rooms participants` в шпаргалку команд и в правило про
  источники actorId — `rooms participants --json` становится самым прямым источником
  actorId участников. Заявленный потребитель — агент через skill (CLAUDE.md); без
  правки skill не узнает о девятой команде.

## 6. Тестирование

- **Моки интерфейса (compile-правка, без неё пакет cli не соберётся с тестами):** stub
  `GetParticipants` → `errMock` в пять существующих реализаций `TalkClient` в
  тестах: `mockTalkClient` (`cli_test.go`), `chatSpyClient` (`handlers_chat_test.go`),
  `reactionsSpyClient` (`handlers_reactions_test.go`), `searchSpyClient`
  (`handlers_search_test.go`), `roomMockClient` (`room_test.go`); шестая —
  `roomsSpyClient` (`handlers_rooms_test.go`) — получает полноценный spy (поля
  result/err/calls — тот же паттерн, что у list/find/search): новым handler-тестам
  нужен преднастраиваемый результат, errMock-стаб этого не даёт. У каждой compile-time
  проверка `var _ TalkClient = (*...)(nil)`. `internal/room` не трогаем (свой
  минимальный `RoomLister`).
- **client (`internal/client/participants_test.go`):** httptest-сервер:
  - успех: фикстура реального формата (обезличенный ответ, снятый с живого 2026-09-09,
    §3) → декод всех семи полей; проверка метода/пути (`GET`,
    `room/{token}/participants`, PathEscape) и заголовков;
  - `sessionIds: null` → пустой слайс, не nil (нормализация);
  - пустой `data: []` ИЛИ `data: null` → пустой non-nil слайс, nil error (`null`
    проходит guard `len(data)>0` и обнуляет слайс — прецедент `GetReactions`);
  - OCS 404 → `*client.OCSError{Code:404}`.
  - Фикстура в корневом `testdata/` (конвенция репо), реального формата — без гостей.
    Синтетический гость (`participantType=4`, пустой `displayName`) — только отдельной
    фикстурой с пометкой «synthetic» в имени файла, НЕ строкой внутри основной
    «реального формата»: формат гостевых полей — assumption (§4), а фикстуры обязаны
    отражать реальный формат эндпоинта (инвариант CLAUDE.md). При первом живом прогоне
    комнаты с гостем — сверить фактические поля и заменить синтетику обезличенным
    реальным ответом.
- **cli (`internal/cli/handlers_rooms_test.go`):** table-driven, клиент — mock:
  успех (stdout: сортировка роль→имя, fallback имени, тексты ролей, онлайн да/нет),
  `--json` (поля + `sessionIds` не null), пустой список → пустой stdout exit `0`,
  `--name` однозначно/неоднозначно/ноль → `0/3/2` (при 3/2 клиент не звался), OCS 404 →
  exit `2`, OCS 403 → exit `1` с текстом сервера, неизвестный флаг → exit `1`, конфликт
  positional + `--name` → предупреждение в stderr и комната = positional (поведение
  `chat show`), лишние позиционные игнорируются, оба пусты (нет `<room>` и `--name`) →
  exit `1`.
- **help (`internal/cli/help_test.go`):** детальные/анти-drift проверки покрывают команду
  автоматически после добавления в `cmdSpecs`; тест-инфраструктура дополняется руками:
  - канон `TestCmdSpecs_OrderMatchesHelpOrder`: вставить `"rooms participants"` после
    `"rooms search"` (тест сверяет и длину, и порядок);
  - канон `TestCmdSpecs_FlagsExactly`: ключ `"rooms participants": {"--name"}`;
  - `buildPositionalArgs`: кейс `"rooms participants"` → `["tok123"]` (иначе handler
    получает пустой позиционный → exit 1, и prong-и анти-drift'а вырождаются).
- **Run-level каноны (`internal/cli/cli_test.go`):** кейс `{"rooms participants",
  []string{"rooms", "participants", "tok"}}` в `TestRunRoutesAllStubs`; кейс
  `{"rooms/participants", []string{"rooms", "participants", "TOK123"},
  []string{"TOK123"}, ""}` в `TestRunRoutesToCorrectHandler` — новый verb получает
  покрытие роутинга, а комментарий «все 8 команд» после замены счётчика на 9 остаётся
  честным над таблицей.
- **Интеграционный (`internal/client/integration_test.go`, build-тег `integration`,
  read-only — БЕЗ мутационных флагов, по образцу `TestIntegration_GetChat`):**
  token из `ListRooms` (первая комната); `GetParticipants` → ≥1 участника; у каждого
  непустые `actorId`/`actorType`; хотя бы один участник с `participantType=1` (владелец —
  инвариант комнаты); `t.Logf` первых строк для диагностики. Логи и фикстуры —
  обезличенные (репозиторий публичный).

## 7. Будущее (явно за рамками)

- `rooms mark-read` и `reactions add/remove` — оставшиеся строки TODO (мутации, отдельные
  дельты).
- Мутации участников: `rooms invite/remove/promote`.
- Колонка прав (permissions) и вывод `attendeePin` для dial-in.
