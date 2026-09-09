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
(`participantType` по возрастанию) → имя (case-insensitive). Сортировка — клиентская,
прецедент «клиентская пост-обработка» — фильтры `rooms list`:

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
(поисковая семантика `rooms list`; на практике недостижимо — сам запрашивающий всегда
участник доступной ему комнаты).

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
    ActorId         string   `json:"actorId"`         // для guests — "guest::<anon-id>"
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
- Нормализация после decode: `SessionIds == nil` → пустой слайс (детерминированный
  `--json`: `[]`, не `null`) — прецедент non-nil map у `GetReactions`.
- Пустой ответ сервера → пустой (non-nil) слайс, nil error.
- OCS-error — стандартная обработка `doOCS` → `*client.OCSError` (404 → exit 2 в cli).
- Метод добавляется в интерфейс `cli.TalkClient` (`internal/cli/cli.go`) — compile-time
  проверка `var _ TalkClient = (*client.TalkClient)(nil)` ловит рассинхрон.

## 5. Слой cli

- `roomsParticipantsHandler` в `internal/cli/handlers_rooms.go` — по образцу
  `roomsFindHandler` + room-resolution из `chatShowHandler`. Порядок шагов:
  1. ручной scan флагов (`--name`, обе формы `--flag value` / `--flag=value`; неизвестный
     `--*` → exit `1`);
  2. первый позиционный = room, лишние игнорируются;
  3. `ResolveRoom(room, nameFlag)` — коды `1/2/3` сохраняются;
  4. `GetParticipants`;
  5. сортировка `sort.SliceStable`: `ParticipantType` asc → `DisplayName`
     case-insensitive asc (stable — равные сохраняют порядок сервера);
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
  автоматически. Исключение — три канона с hardcoded-структурой, правятся руками (шаги —
  §6): `TestCmdSpecs_OrderMatchesHelpOrder`, `TestCmdSpecs_FlagsExactly`,
  `buildPositionalArgs`.
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
- `docs/integration-run.md`: строка нового read-only теста в таблицу тестов (мутационная
  семантика не меняется — флагов не добавляется).
- Комментарии-счётчики «8 команд/8 методов» в коде: `internal/cli/help.go` (док-комментарий
  cmdSpecs), `internal/cli/cli.go` (интерфейс TalkClient), `internal/cli/cli_test.go`,
  `internal/cli/help_test.go` — заменить на 9. Комментарии, не поведение, но это тот же
  drift, который проект ловит анти-drift-тестами.

## 6. Тестирование

- **client (`internal/client/participants_test.go`):** httptest-сервер:
  - успех: фикстура реального формата (обезличенный ответ, снятый с живого 2026-09-09,
    §3) → декод всех семи полей; проверка метода/пути (`GET`,
    `room/{token}/participants`, PathEscape) и заголовков;
  - `sessionIds: null` → пустой слайс, не nil (нормализация);
  - пустой `data: []` → пустой non-nil слайс, nil error;
  - OCS 404 → `*client.OCSError{Code:404}`.
  - Фикстура в корневом `testdata/` (конвенция репо), реального формата. В фикстуру можно
    добавить участника-гостя (`participantType=4`, пустой `displayName`) — по полям формат
    идентичен (живая выборка гостей не содержала; значения 4–6 — документация, §3).
- **cli (`internal/cli/handlers_rooms_test.go`):** table-driven, клиент — mock:
  успех (stdout: сортировка роль→имя, fallback имени, тексты ролей, онлайн да/нет),
  `--json` (поля + `sessionIds` не null), пустой список → пустой stdout exit `0`,
  `--name` однозначно/неоднозначно/ноль → `0/3/2` (при 3/2 клиент не звался), OCS 404 →
  exit `2`, OCS 403 → exit `1` с текстом сервера, неизвестный флаг → exit `1`, лишние
  позиционные игнорируются, оба пусты (нет `<room>` и `--name`) → exit `1`.
- **help (`internal/cli/help_test.go`):** детальные/анти-drift проверки покрывают команду
  автоматически после добавления в `cmdSpecs`; тест-инфраструктура дополняется руками:
  - канон `TestCmdSpecs_OrderMatchesHelpOrder`: вставить `"rooms participants"` после
    `"rooms search"` (тест сверяет и длину, и порядок);
  - канон `TestCmdSpecs_FlagsExactly`: ключ `"rooms participants": {"--name"}`;
  - `buildPositionalArgs`: кейс `"rooms participants"` → `["tok123"]` (иначе handler
    получает пустой позиционный → exit 1, и prong-и анти-drift'а вырождаются).
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
