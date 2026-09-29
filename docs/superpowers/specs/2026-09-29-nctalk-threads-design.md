# Дизайн: `nctalk` — поддержка тредов (`chat threads`, `--thread`, `--thread-title`)

- **Дата:** 2026-09-29
- **Статус:** дизайн одобрен пользователем; реализация через /spec-to-code
- **Базовая спека:** `docs/superpowers/specs/2026-07-17-nctalk-cli-design.md` (эталон контракта
  базового CLI; этот документ — дельта, добавляющая десятую команду и флаги к двум существующим).
  Предыдущие дельты: `chat edit` (2026-09-08), `rooms participants` (2026-09-09).
- **Стек:** Go 1.21+, только stdlib, `CGO_ENABLED=0`

## 1. Цель и контекст

Дать `nctalk` читать и писать треды Nextcloud Talk. Сегодня клиент слеп к тредам: `chat show`
не помечает их, ответ в тред отправить нельзя (`--reply-to` шлёт цитату — в общий чат, если
цель не в треде), список тредов недоступен. Потребитель — агент через skill-обёртку: без
тредов пересказ «что нового в talk» теряет целые обсуждения.

Решения пользователя по развилкам (брейншторм 2026-09-29):

1. **Охват — читать + писать** (вариант B): список тредов, чтение треда, ответ в тред,
   создание треда. Без rename/notify-level/subscribed-threads (вариант C отклонён).
2. **Форма — надстройки над `chat`** (вариант A): новый verb `threads` + флаги `--thread` /
   `--thread-title` у существующих команд. Отдельный ресурс `threads …` (вариант B) отклонён —
   дробит chat-команды по двум местам.
3. **Аннотации в общем `chat show` — лёгкие пометки** (вариант A): `⟲N` у корня треда,
   `↩<id>` у ответов. «Не трогать вывод» отклонено — теряется видимость обсуждений.

**Сервер:** Nextcloud 34.0.3 + Talk (spreed) 24.0.4; capability `threads` подтверждена живым
запросом capabilities 2026-09-29. API-контракт сверен **по исходникам тега v24.0.4**
(`lib/Controller/ThreadController.php`, `lib/Controller/ChatController.php`,
`lib/Chat/ChatManager.php`, `lib/Model/Thread.php`, `lib/Model/ThreadAttendee.php`):
официальные docs (chat.md) endpoints тредов не документируют — роуты объявлены PHP-атрибутами
`#[ApiRoute]` (appinfo/routes.php в spreed 24 больше не существует).

### Модель тредов (инварианты сервера, неочевидные)

- **Тред = корневое сообщение + ответы. Id треда = id корневого сообщения.**Читать,
  отвечать и ссылаться — одним и тем же числом.
- **Thread-запись создаётся ТОЛЬКО отправкой нового сообщения с непустым `threadTitle`**
  (сервер: `$createThread = $replyTo === 0 && $threadId === 0 && $threadTitle !== ''`).
  Конвертировать существующее сообщение в тред API не умеет — веб-клиент тоже создаёт треды
  только новым сообщением с названием.
- **Ответ в существующий тред** — два пути: параметр `threadId=N` (без цитаты) или
  `replyTo=X`, где X — любое сообщение треда (с цитатой X; сервер сам резолвит тред:
  `topmostParentId(X) ?: id(X)`). Если Thread-записи нет — `replyTo` даёт обычную цитату
  в общий чат.
- В выдаче треда **корень включён**: фильтр comments-manager
  `id = threadId OR topmost_parent_id = threadId` (NC 34 `CommentsManager::getCommentsWithVerbForObjectSinceComment`,
  строки 443–448 исходников).
- Общая история (без `threadId`) **не фильтрует** ответы тредов — они приходят как обычные
  сообщения (с `threadId` чужим), поэтому аннотации в `chat show` осмысленны.

### Out of scope

- `PUT /chat/{token}/threads/{id}` (rename), `POST /chat/{token}/threads/{id}/notify`
  (уровень уведомлений), `GET /chat/subscribed-threads` (подписки по всем комнатам) —
  дёшево добавить отдельной дельтой при надобности.
- Capability-гаrd на клиенте: на серверах без capability `threads` сервер **молча
  игнорирует** `threadId` в show/send — `chat show --thread` вернёт общую историю,
  `chat send --thread` уйдёт не в тред. Наш сервер поддерживает; клиент остаётся тонким,
  поведение документируется (§4), не проверяется.
- Unread-индикация тредов: сервер не отдаёт per-thread read-marker
  (`ThreadAttendee` сериализует только `notificationLevel`) — считать не из чего.
- Изменение семантики существующего `--reply-to` — не трогаем (сервер сам кладёт ответ
  в тред, если цель в нём).

## 2. Контракт команд

### 2.1 `chat threads <room> [--last N]` — новая команда (десятая)

- `<room>` — token позиционно **или** `--name "<имя>"` — то же правило разрешения, что у
  `chat show` (базовая спека §7): позиционный приоритетен (при конфликте — предупреждение
  в stderr, `--name` игнорируется); `--name` 1 совпадение → ок, >1 → кандидаты в stderr,
  exit `3`; 0 → exit `2`; оба пусты → exit `1`.
- Позиционные: первый = `<room>`; лишние игнорируются — единообразно с остальными.
- `--last N` — количество тредов: нецелое → exit `1`; `N <= 0` → не задан → дефолт `20`;
  `N > 50` → клэмп до `50` (серверный потолок `int<1,50>`, дефолт сервера 50 — наш дефолт
  ниже для читаемого вывода, симметрия с `chat show --last 20`).
- Глобальный `--json` — в любой позиции, как везде.

Вывод (текст) — построчно в стиле `MessagesTable` (без tabwriter), порядок сервера
(по `lastActivity` по убыванию):

```
[1234] 2026-09-29 14:02:05  Отчёт за сентябрь  ⟲5  Вася: цифры сверил, вчера уложились
[1107] 2026-09-28 10:41:12  Выбор даты митапа  ⟲2  Петя: пятница ок
```

- `[id]` — id треда (= id корневого сообщения: им же читаем `--thread`, им же отвечаем).
- Время — `FormatTime(LastActivity)` (Unix-секунды; формат `2006-01-02 15:04:05`, как
  везде в клиенте).
- Название — `thread.title`; пустое (старые данные) → текст корневого сообщения
  (`first.message` как есть, без подстановки плейсхолдеров — тот же принцип, что у
  `MessagesTable`); `first == nil` (корень истёк/удалён) → `—`.
- `⟲N` — `thread.numReplies`; печатается всегда, включая `⟲0` (недавно созданный тред).
- Превью последнего — `last.actorDisplayName: last.message` (system-фолбэк как в
  `lastMessagePreview`); `last == nil` → без превью.

`--json` — канонический массив `ThreadInfo` (§4) в порядке сервера; пустой список → `[]`,
не `null`. Пустой список → пустой stdout, exit `0` (поисковая семантика, как `rooms list`).

### 2.2 `chat show <room> --thread=<id>` — новый флаг

- `<id>` — положительное целое (= id корневого сообщения). Нецелое, `0` или отрицательное
  при явном флаге → exit `1` («`--thread` ожидает положительное число»). Не задан —
  поведение прежнее, общая история.
- Реализация — тот же `GET /chat/{token}` c дополнительным query-параметром `threadId`;
  **вся существующая механика наследуется без изменений**: пагинация по заголовку
  `X-Chat-Last-Given`, `304`/`204`, потолки `--last` (дефолт 20 / cap 200 при фильтрах),
  клиентские фильтры `--from`/`--since`, `--system`, рендер.
- В выдаче треда корень присутствует (§1); рендер как у обычной истории, но суффикс `↩`
  в этом режиме НЕ печатается (все сообщения по построению из одного треда), `⟲N` на
  корне — печатается.

### 2.3 `chat send <room> [--thread=<id> | --thread-title="<тема>"] "msg"` — новые флаги

- `--thread <id>` — ответ в тред **без цитаты** (серверный параметр `threadId`). `<id>`
  положительное целое, иначе exit `1` без сети.
- `--thread-title "<тема>"` — создать тред: текст сообщения станет корневым, тема —
  названием. Непустое значение: пустое → exit `1` без сети (сервер безымянные треды
  не создаёт).
- **Взаимоисключаемость**: любые два из `--reply-to` / `--thread` / `--thread-title`
  одновременно → exit `1` **до** сетевого вызова (сервер молча игнорирует лишние параметры —
  нельзя допустить молчаливую подмену действия; сообщение об ошибке перечисляет конфликт).
- Существующие `--file`/stdin/`--silent`/`--reference-id` — без изменений.
- Выход прежний — id нового сообщения. При создании треда это id корневого сообщения =
  будущий id треда (можно сразу подставить в `--thread`).
- `--reply-to` не меняем: если цель в существующем треде, сервер сам положит ответ в тред
  (с цитатой) — поведение уже правильное, просто раньше клиент об этом не знал.

### 2.4 Аннотации тредов в общем `chat show`

В `MessagesTable` к строке добавляется суффикс (после реакций, разделитель как у реакций):

```
[1234] 2026-09-29 14:02:05 Вася: Отчёт за сентябрь   👍×2   ⟲5
[1300] 2026-09-29 14:05:00 Петя: цифры сверил   ↩1234
```

- Корень треда: `ThreadId == Id && ThreadReplies > 0` → `⟲<ThreadReplies>` (при `0` —
  не печатается: это ещё не обсуждение).
- Ответ в треде: `ThreadId != Id && ThreadTitle != ""` → `↩<ThreadId>` — только в режиме
  общей истории (в `--thread`-режиме подавляется, §2.2). Условие `ThreadTitle != ""` —
  «контекст треда загружен», т.е. Thread-запись существует (§3.4). Без него quote-ответ
  (`replyTo` на сообщение без треда) получил бы `↩id` со ссылкой на несуществующий тред —
  `chat show --thread=<id>` там вернул бы 404. Quote-ответы без треда не помечаются.
- `⟲` (U+27F2) и `↩` (U+21A9) — символы юникода, в духе существующих `×`/эмодзи реакций.

### Exit-коды (наследуют базовый контракт §7, без новых кодов)

| Ситуация | Код |
|---|---|
| успех (в т.ч. пустой список тредов) | `0` |
| нецелое/неположительное `--thread`, пустой `--thread-title`, **конфликт флагов**, неизвестный флаг, сеть, 401, 400 (в т.ч. send в несуществующий тред), 5xx | `1` |
| OCS 404 — тред не найден (`{"error":"thread"}`) или комната не найдена · 0 совпадений `--name` | `2` |
| >1 совпадение `--name` | `3` |

Серверный OCS 404 треда ложится на существующий маппинг `*client.OCSError{Code:404}` →
exit `2` без новых правил.

## 3. API-контракт (Talk chat-API, сверен по исходникам spreed v24.0.4)

Все — OCS, тот же транспорт `doOCS` (Basic-auth + `OCS-APIRequest: true` +
`Accept: application/json`), конверт `ocs.data`. Требуемая capability: `threads`
(на боевом включена, §1).

### 3.1 Список тредов

```
GET /ocs/v2.php/apps/spreed/api/v1/chat/{token}/threads/recent?limit=<1..50>
```

- Ответ `ocs.data` — массив, порядок по `lastActivity` убыванию (сервер):
- Элемент (`TalkThreadInfo`):
  ```json
  {
    "thread": {
      "id": 1234,                  // = id корневого сообщения
      "roomToken": "abc123",
      "lastMessageId": 1300,
      "numReplies": 5,
      "lastActivity": 1760000000,  // Unix-секунды
      "title": "Отчёт за сентябрь"
    },
    "attendee": { "notificationLevel": 0 },
    "first": { …сообщение chat-API… },   // может быть null (корень истёк/удалён)
    "last":  { …сообщение chat-API… }    // может быть null
  }
  ```
- `first`/`last` — обычные сообщения chat-API: **`messageParameters` — объект**
  (инвариант CLAUDE.md: у chat-API всегда объект; формат массива бывает только в
  `/v4/room` `lastMessage`). Фикстуры обязаны отражать это.
- `attendee` — подписка запросившего на тред (`notificationLevel`: 0 default, 1 always,
  2 mention, 3 never). В текстовый вывод не идёт (out of scope), в `--json` — канонически.

### 3.2 Чтение треда

```
GET /ocs/v2.php/apps/spreed/api/v1/chat/{token}?threadId=<id>
```

- Тот же эндпоинт, что `chat show`; добавляется один query-параметр. Пагинация
  (`X-Chat-Last-Given`, `lookIntoFuture` и пр.) — без изменений, клиент уже умеет.
- Выдача включает корень (`id = threadId OR topmost_parent_id = threadId`, §1).
- Несуществующий/чужой тред → OCS 404 `{"error":"thread"}` → exit `2`.
- Прочие параметры (`setReadMarker=0`, `limit=min(N,200)`) — как у текущего `GetChat`.

### 3.3 Отправка

```
POST /ocs/v2.php/apps/spreed/api/v1/chat/{token}
{ "message": "…", "threadId": 1234 }          // ответ в тред без цитаты
{ "message": "…", "threadTitle": "Тема" }     // создать тред
```

- `threadId` — ответ в тред без цитаты; тред должен существовать, иначе
  **HTTP 400** `{"error":"reply-to"}` (сверено по исходникам: `STATUS_BAD_REQUEST`, не 404!)
  → exit `1` по общему контракту.
- `threadTitle` — создание треда; действует только при `replyTo=0 && threadId=0`
  (клиент гарантирует отсутствие конфликтов сам, §2.3). Пустое значение сервером
  игнорируется (тред не создаётся) — поэтому клиент проверяет непустоту до сети.
- Существующий `replyTo` (цитата) семантически расширен сервером: цель в треде → ответ
  попадает в тред. Клиент не меняет ничего.
- Ответ — отправленное сообщение (уже разбирается `SendMessage`; новых полей разбирать
  не нужно, существующий разбор id не меняется).
- 400 (пустое сообщение и пр.) → exit `1` по общему контракту.

### 3.4 Поля сообщений

- `threadId` — **всегда присутствует**: `topmostParentId ?: id` — у нетредового сообщения
  равен собственному id (уже декодируется клиентом, поле `Message.ThreadId` существует).
- `threadTitle` (string), `threadReplies` (int) — только когда для сообщения сервер
  загрузил объект треда, т.е. **Thread-запись существует**: в `threads/recent` у
  `first`/`last`, в обычной истории — у корней и ответов тредов (ChatController
  резолвит `findByThreadIds` для каждой страницы). У quote-ответов без треда поля
  отсутствуют. Декодируем `omitempty`. Это условие — основа правила аннотации `↩` (§2.4).

## 4. Слой client

Новый файл `internal/client/threads.go` (своя группа эндпоинта — по аналогии с
`reactions.go`/`participants.go`):

```go
// Thread — каноническое представление треда (спека-дельта §3.1).
type Thread struct {
    Id            int    `json:"id"`            // = id корневого сообщения
    RoomToken     string `json:"roomToken"`
    LastMessageId int    `json:"lastMessageId"`
    NumReplies    int    `json:"numReplies"`
    LastActivity  int64  `json:"lastActivity"`  // СЕКУНДЫ Unix
    Title         string `json:"title"`         // может быть пустым → fallback в render
}

// ThreadAttendee — подписка запросившего на тред.
type ThreadAttendee struct {
    NotificationLevel int `json:"notificationLevel"` // 0 default, 1 always, 2 mention, 3 never
}

// ThreadInfo — элемент выдачи threads/recent; first/last nullable.
type ThreadInfo struct {
    Thread   Thread         `json:"thread"`
    Attendee ThreadAttendee `json:"attendee"`
    First    *Message       `json:"first"`
    Last     *Message       `json:"last"`
}

func (c *TalkClient) ListThreads(ctx context.Context, token string, limit int) ([]ThreadInfo, error)
```

- `ListThreads`: `GET pathChat + "/" + url.PathEscape(token) + "/threads/recent"` +
  `query: limit` (клэмп [1,50] выполняет cli-слой; сюда приходит уже готовое значение,
  метод не валидирует — тонкий). Путь строится от существующей константы `pathChat`
  (`chat.go`). Разбор: `doOCS` → `[]ThreadInfo`; `data: null`/`[]` → пустой non-nil
  слайс, nil error (guard-прецедент `GetParticipants`).
- `GetChatOpts` + поле `ThreadId int` (`0` = не задан → параметр не шлётся; `>0` →
  query `threadId`). Прокидывается в существующий `getChatPage`.
- `SendMessageOpts` + поля `ThreadId int`, `ThreadTitle string` (в JSON-body `omitempty`:
  `threadId`/`threadTitle`).
- `Message` + `ThreadTitle string \`json:"threadTitle,omitempty"\``,
  `ThreadReplies int \`json:"threadReplies,omitempty"\`` (`ThreadId` уже есть, §3.4).
- OCS-ошибки — стандартная обработка `doOCS` → `*client.OCSError`; 404 → exit `2` в cli.
- `ListThreads` добавляется в интерфейс `cli.TalkClient` (`internal/cli/cli.go`) —
  compile-time проверка `var _ TalkClient = (*client.TalkClient)(nil)` ловит рассинхрон;
  это ломает сборку всех существующих тест-моков интерфейса — stub-правки в §6.

## 5. Слой cli

- Роутинг: `"threads": chatThreadsHandler` в `routes["chat"]` (`cli.go`); подсказка
  «ожидается verb (…show/send/edit)» пополняется `threads`.
- `chatThreadsHandler` (в `handlers_chat.go` — verb ресурса chat): ручной scan флагов
  (`--name`, `--last`, обе формы; неизвестный `--*` → exit `1`) → первый позиционный =
  room, лишние игнорируются → `ResolveRoom` (коды `1/2/3` сохраняются) → `ListThreads` →
  `render.ThreadsTable` / `render.ThreadsJSON` по `jsonOut`; пустой список → пустой
  stdout, exit `0`.
- `chatShowHandler`: + флаг `--thread` (обе формы); валидация положительного целого;
  прокидывание в `GetChatOpts.ThreadId`.
- `chatSendHandler`: + флаги `--thread`, `--thread-title` (обе формы); guard
  взаимоисключаемости троицы `--reply-to`/`--thread`/`--thread-title` (exit `1` до сети);
  guard непустого `--thread-title` (exit `1` до сети); прокидывание в `SendMessageOpts`.
- `render`:
  - `ThreadsTable(w, ts)` — построчный вывод §2.1; фолбэки: пустой `title` →
    `First.Message` (nil → `—`), `Last` → превью или ничего.
  - `ThreadsJSON(w, ts)` — `writeJSON` по образцу `RoomsJSON` (пустой слайс → `[]`).
  - `MessagesTable` — параметр показа `↩`-ссылок (в общей истории — да, в `--thread`
    режиме — нет); суффиксы §2.4 после реакций.
- Help (`internal/cli/help.go`, `cmdSpecs` — единый источник правды):
  - новая запись `chat threads` (позиция — после `chat send`, до `chat edit`):
    `UsageExtras: "<room>"`, флаги `--name`, `--last <N>`;
  - `chat show` += флаг `--thread <id>`;
  - `chat send` += флаги `--thread <id>`, `--thread-title "<тема>"`.
  Детальные/resource help и анти-drift тесты подхватывают автоматически; каноны с
  hardcoded-структурой правятся руками (§6).

### Сопроводительные правки документации (входят в задачу)

- README: секция `chat threads` по образцу `chat show`; флаги в секциях `chat show`/`send`.
- CLAUDE.md: «9 команд» → «10 команд» (первый абзац); блок «Неочевидные инварианты»
  пополняется: id треда = id корня; Thread-запись создаётся только `threadTitle`;
  `replyTo` в тред кладёт ответ в тред; выдача `?threadId` включает корень; общая история
  включает ответы тредов; молчаливое игнорирование `threadId` серверами без capability
  `threads`; взаимоисключаемость `--reply-to`/`--thread`/`--thread-title`.
- Счётчики команд в док-комментариях кода (`internal/cli/help.go`, `internal/cli/cli.go`)
  — заменить 9 → 10 (тот же drift, который ловят анти-drift-тесты).
- `docs/integration-run.md`: новый **мутационный** сценарий `TestIntegration_Threads`
  в таблицу тестов + в перечень мутационных (флаги `NCTALK_INTEGRATION_SEND=1` +
  `NCTALK_INTEGRATION_ROOM`).
- Skill-обёртка `nctalk` (вне репозитория): `chat threads` в шпаргалку команд; `chat show
  --thread` как способ дочитать обсуждение; `chat send --thread` для ответа в тред.
  Без правки skill не узнает о новых командах.

## 6. Тестирование

- **Фикстуры — только с живого API, ДО написания тестов** (урок CLAUDE.md: самопальные
  фикстуры скрыли баг). Протокол: на боевом сервере (integration-комната) вручную через
  curl: создать тред (`threadTitle`), ответить (`threadId`), снять обезличенные ответы
  `threads/recent`, `chat?threadId=N`, send-ответ — сложить в корневой `testdata/`
  (конвенция репо). Особо сверить: `first`/`last` в `threads/recent` несут
  `messageParameters`-**объект**; в обычной истории у корня есть `threadId == id` +
  `threadReplies`, у ответов — `threadId != id`.
- **Моки интерфейса (compile-правка):** `ListThreads` добавляется во все шесть
  существующих реализаций `TalkClient` в тестах. `chatSpyClient`
  (`handlers_chat_test.go`) получает полноценный spy (поля результат/ошибка/факт вызова —
  тот же паттерн, что у его `GetChat`/`SendMessage`): handler-тесты `chat threads` живут
  в `handlers_chat_test.go` и нуждаются в преднастраиваемом результате. Остальные пять —
  `mockTalkClient` (`cli_test.go`), `reactionsSpyClient` (`handlers_reactions_test.go`),
  `searchSpyClient` (`handlers_search_test.go`), `roomMockClient` (`room_test.go`),
  `roomsSpyClient` (`handlers_rooms_test.go`) — errMock-стабы (их сценарии треды не
  трогают).
- **client (`internal/client/threads_test.go` + правки `chat_test.go`):** httptest:
  - `ListThreads`: фикстура реального формата → декод всех полей, `first/last == null`
    → nil-указатели; проверка метода/пути (`chat/{token}/threads/recent`, PathEscape)
    и query `limit`; `data: []`/`null` → пустой non-nil слайс; OCS 404 →
    `*client.OCSError{Code:404}`;
  - `GetChat` c `ThreadId` → query содержит `threadId`; без — не содержит;
  - `SendMessage`: body содержит `threadId`/`threadTitle` только когда заданы
    (omitempty); комбинации флагов НЕ здесь — это cli;
  - `Message` decode: `threadTitle`/`threadReplies` при отсутствии полей → нули.
- **cli (`handlers_chat_test.go`):** table-driven:
  - `chat threads`: успех (формат строк: `[id] время title ⟲N превью`, фолбэки пустого
    title, `⟲0`, nil first/last), `--json` (включая `[]` для пустого), пустой список →
    пустой stdout exit `0`, `--last` (дефолт/клэмп 50/нецелое → `1`), `--name` ветки
    `0/3/2`, OCS 404 → exit `2` с текстом сервера, неизвестный флаг → `1`;
  - `chat show --thread`: query-прокидывание (через spy), нецелое/`0`/отрицательное →
    exit `1`, рендер без `↩`, с `⟲N` на корне;
  - `chat send`: `--thread` → `SendMessageOpts.ThreadId`; `--thread-title` →
    `ThreadTitle`; все три пары конфликтов → exit `1` ДО сети (spy: ноль вызовов),
    пустой `--thread-title` → exit `1` до сети;
  - рендер аннотаций: `⟲N` только при `ThreadReplies > 0` и `ThreadId == Id`;
    `↩id` при `ThreadId != Id && ThreadTitle != ""` (включая негатив: quote-ответ без
    треда — `ThreadId != Id`, но `ThreadTitle == ""` — без пометки); совместимость с
    суффиксом реакций (`текст   👍×2   ⟲5`).
- **help (`internal/cli/help_test.go`):** каноны правятся руками:
  - `TestCmdSpecs_OrderMatchesHelpOrder`: `"chat threads"` после `"chat send"`;
  - `TestCmdSpecs_FlagsExactly`: `"chat threads": {"--name", "--last"}`,
    `"chat show"` += `--thread`, `"chat send"` += `--thread`, `--thread-title`;
  - `buildPositionalArgs`: кейс `"chat threads"` → `["tok123"]`.
- **Run-level каноны (`internal/cli/cli_test.go`):** кейс `{"chat threads", ["chat",
  "threads", "tok"]}` в `TestRunRoutesAllStubs`; кейс в `TestRunRoutesToCorrectHandler`.
- **Интеграционный (`internal/client/integration_test.go`, build-тег `integration`,
  мутационный — `NCTALK_INTEGRATION_SEND=1` + `NCTALK_INTEGRATION_ROOM`, по образцу
  `TestIntegration_EditMessage`):** сценарий e2e:
  1. `SendMessage(threadTitle)` → id корня;
  2. `SendMessage(threadId=id корня)` → id ответа;
  3. `ListThreads` → тред с `numReplies >= 1`, title совпадает;
  4. `GetChat(ThreadId)` → в выдаче есть корень и ответ (сверка §1-инвариантов на живом);
  5. `GetChat` без фильтра → корень несёт `threadReplies >= 1`, ответ несёт `threadId`
     корня.
  Логи и фикстуры — обезличенные (репозиторий публичный). Сценарий создаёт сообщения в
  integration-комнате — та же семантика допуска, что у `TestIntegration_EditMessage`.

## 7. Будущее (явно за рамками)

- `chat thread rename` / notify-level / `subscribed-threads` — эндпоинты уже разведены
  в §3–out-of-scope; отдельная дельта при надобности.
- Unread-индикация тредов — если сервер начнёт отдавать per-thread read-marker.
