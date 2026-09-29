# Ревью спеки: поддержка тредов (дельта 2026-09-29)

Ревью `2026-09-29-nctalk-threads-design.md`. Два прохода: документ сам по себе + заземление всех
утверждений о существующей системе. Заземление по коду репозитория пройдено (cli-роутер и подсказка
verb-ов, handlers_chat, ResolveRoom, client: chat/reactions/participants/paths/types, transport
DoOCS, render (MessagesTable/reactionsSuffix/lastMessagePreview/writeJSON/FormatTime), help-слой и
канон-тесты, все шесть тестовых моков `TalkClient`, render_test.go, корневой testdata/,
README/CLAUDE.md/integration-run.md, базовая спека §7). Серверные утверждения выборочно сверены по
исходникам spreed v24.0.4 и server v34.0.3 на GitHub: `threads/recent` (маршрут, `limit int<1,50>`,
дефолт 50, сортировка `last_activity DESC`), 404 `{"error":"thread"}`, 400 `{"error":"reply-to"}`
при несуществующем `threadId`, условие создания треда `replyTo===0 && threadId===0 &&
threadTitle!==''`, `threadId = topmostParentId ?: id` всегда, `threadTitle`/`threadReplies` только
при резолвнутом Thread, фильтр `id = X OR topmost_parent_id = X` в NC 34 core (корень включён),
`topmost_parent_id` проставляется ядром любому ответу с parentId (негатив-кейс «quote-ответ без
треда имеет `ThreadId != Id`, `ThreadTitle == ""`» — подтверждён), `ThreadAttendee` сериализует
только `notificationLevel`. Блокеров нет; замечания:

## 🟡 Стоит уточнить

### 1. `url.PathEscape(token)` в `ListThreads` противодействует задокументированному инварианту транспорта (двойной эскейп)

- **Цитата (§4):** «`ListThreads`: `GET pathChat + "/" + url.PathEscape(token) +
  "/threads/recent"`»; §6 закрепляет: «проверка метода/пути (`chat/{token}/threads/recent`,
  PathEscape)».
- **Что не так:** в репо два конфликтующих шаблона сборки пути. `GetParticipants` сознательно НЕ
  делает предварительный PathEscape, и это задокументировано как инвариант:
  [participants.go:33-36](file:///Users/stas/Projects/My/NCCliClient/internal/client/participants.go)
  («Предварительный url.PathEscape дал бы двойной эскейп (пробел → %2520)»), с регресс-тестом
  `TestGetParticipants_PathEscape`
  ([participants_test.go:99](file:///Users/stas/Projects/My/NCCliClient/internal/client/participants_test.go)).
  Причина — транспорт сам эскейпит Path при сборке URL (`RawPath` сбрасывается, `url.String()`
  переэкранирует `%`):
  [transport.go:131-133](file:///Users/stas/Projects/My/NCCliClient/internal/transport/transport.go).
  `url.PathEscape` в [chat.go:151](file:///Users/stas/Projects/My/NCCliClient/internal/client/chat.go)
  и [reactions.go:40](file:///Users/stas/Projects/My/NCCliClient/internal/client/reactions.go) —
  старый шаблон, оставшийся до открытия этой проблемы; спека прописывает новый код по нему. Для
  реальных token-ов Talk (alphanum) разницы нет, но новый код закрепляет шаблон, который репо уже
  признал ошибочным и прикрыл тестом.
- **Что сделать:** строить путь без предварительного эскейпа — как `GetParticipants`
  (`pathChat + "/" + token + "/threads/recent"`), либо явно обосновать в спеке выбор старого
  шаблона (и тогда §6-тест «PathEscape» проверяет двойной эскейп).

### 2. Печатается ли `⟲0` на корне в режиме `chat show --thread` — не определено

- **Цитата (§2.2):** «суффикс `↩` в этом режиме НЕ печатается …, `⟲N` на корне — печатается»;
  против **(§2.4):** «Корень треда: `ThreadId == Id && ThreadReplies > 0` → `⟲<ThreadReplies>`
  (при `0` — не печатается: это ещё не обсуждение)».
- **Что не так:** условие `> 0` сформулировано в §2.4, чей охват — «аннотации тредов в **общем**
  `chat show`». §2.2 для `--thread`-режима говорит лишь «печатается» без повтора условия. Для
  свежесозданного треда (0 ответов) возможны две реализации: `⟲0` (как в `chat threads`, §2.1,
  где `⟲0` печатается всегда) или ничего (правило §2.4). Тест из §6 («рендер без `↩`, с `⟲N` на
  корне») требует зафиксированного ответа.
- **Что сделать:** в §2.2 явно указать: `⟲0` на корне в `--thread`-режиме печатается или
  подавляется (и отразить в тест-кейсе §6).

### 3. Смена сигнатуры `MessagesTable` ломает компиляцию `render_test.go` — правки нет в плане §6

- **Цитата (§5):** «`MessagesTable` — параметр показа `↩`-ссылок (в общей истории — да, в
  `--thread` режиме — нет)»; §6 перечисляет compile-правки только моков («Моки интерфейса
  (compile-правка): … все шесть существующих реализаций `TalkClient`»).
- **Что не так:** `MessagesTable` вызывается напрямую в существующих тестах render-пакета:
  [render_test.go:101](file:///Users/stas/Projects/My/NCCliClient/internal/render/render_test.go)
  и `:115` (`TestMessagesTable`, `TestMessagesTableNoReactions`). Добавление параметра роняет
  сборку `internal/render` с тестами — правка механическая, но §6, претендующий на полноту
  compile-влияния (учёл урок прошлого ревью про шесть моков), её не называет. Производственный
  caller один ([handlers_chat.go:224](file:///Users/stas/Projects/My/NCCliClient/internal/cli/handlers_chat.go)) — он в §5 упомянут.
- **Что сделать:** дополнить §6: правка call-site-ов `MessagesTable` в `render_test.go` под новую
  сигнатуру (+ кейсы нового параметра в render-тестах, если аннотации тестируются там, а не через
  handler).

### 4. `ThreadsTable`: сырые плейсхолдеры в fallback-названии и превью — обоснование ссылается на «принцип MessagesTable», которым pipeline `chat show` не пользуется

- **Цитата (§2.1):** «пустое (старые данные) → текст корневого сообщения (`first.message` как
  есть, без подстановки плейсхолдеров — тот же принцип, что у `MessagesTable`)»; «Превью
  последнего — `last.actorDisplayName: last.message`».
- **Что не так:** «принцип MessagesTable» описывает только половину конвейера: рендер не
  подставляет, но в `chat show` подстановку делает CLI-слой ДО рендера
  ([handlers_chat.go:212-216](file:///Users/stas/Projects/My/NCCliClient/internal/cli/handlers_chat.go),
  `render.SubstituteParams`), и пользователь видит готовый текст. В `chat threads` по спеке
  подстановки нет ни в одном слое — превью последнего сообщения (частый путь: ответы с
  `{mention-*}`, `{file}`) будет печататься сырыми плейсхолдерами, вразрез с выводом того же
  треда через `chat show --thread`. Для заявленного в §1 потребителя-агента (пересказ «что
  нового») это деградация читаемости. Решение может быть любым, но оно должно быть осознанным, а
  ссылка на «тот же принцип, что у MessagesTable» сейчас маскирует расхождение.
- **Что сделать:** либо подставлять плейсхолдеры в `first`/`last` на handler-уровне в
  текстовом режиме (как `chat show`), либо оставить сырыми и поправить обоснование (это
  сознательное отличие, а не «тот же принцип»).

## 🟢 По красоте

### 5. Usage-синопсис `chat send` показывает позиционное `"msg"`, которого нет в контракте

- **Цитата (§2.3):** «`chat send <room> [--thread=<id> | --thread-title="<тема>"] "msg"`».
- **Что не так:** тело `chat send` приходит из stdin/`--file`, позиционного аргумента-сообщения
  нет (§2.3 сам говорит «Существующие `--file`/stdin … — без изменений»;
  [help.go:109-112](file:///Users/stas/Projects/My/NCCliClient/internal/cli/help.go) —
  `UsageExtras: "<room>"`). Синопсис можно прочитать как появление позиционного `msg`.
- **Что сделать:** убрать `"msg"` из синопсиса: `chat send <room> [--thread=<id> |
  --thread-title="<тема>"]` (тело — stdin/--file, как раньше).

### 6. Битая ссылка «(§4)» и опечатка в Out of scope

- **Цитата (§1):** «поведение документируется (§4), не проверяется».
- **Что не так:** §4 («Слой client») про молчаливое игнорирование `threadId` серверами без
  capability ничего не говорит; фактическое место документирования — §5, сопроводительные правки
  (bullet CLAUDE.md: «молчаливое игнорирование `threadId` серверами без capability `threads`»).
  Там же опечатка «Capability-гаrd» (латинское rd в кириллическом слове).
- **Что сделать:** заменить ссылку на §5 (или на конкретный bullet CLAUDE.md-правки) и поправить
  «guard».

### 7. Смежные фразы про пустой список в §2.1 противоречат друг другу для `--json`

- **Цитата (§2.1):** «`--json` — канонический массив `ThreadInfo` (§4) в порядке сервера; пустой
  список → `[]`, не `null`. Пустой список → пустой stdout, exit `0`».
- **Что не так:** вторая фраза без уточнения режима противоречит первой (при `--json` и пустом
  списке stdout не пуст, а `[]`; §5/`ThreadsJSON` это разрешает, но в §2.1 читается как
  конфликт).
- **Что сделать:** «Пустой список **в текстовом режиме** → пустой stdout, exit `0`».

### 8. Счётчики «9 → 10» перечислены не везде

- **Цитата (§5):** «Счётчики команд в док-комментариях кода (`internal/cli/help.go`,
  `internal/cli/cli.go`) — заменить 9 → 10».
- **Что не так:** упоминаний «9 команд/методов» в коде пять, а не два: помимо
  [help.go:37](file:///Users/stas/Projects/My/NCCliClient/internal/cli/help.go) и
  [cli.go:16](file:///Users/stas/Projects/My/NCCliClient/internal/cli/cli.go), ещё
  [cli_test.go:64](file:///Users/stas/Projects/My/NCCliClient/internal/cli/cli_test.go)
  (`TestRunRoutesAllStubs — все 9 команд`),
  [help_test.go:12](file:///Users/stas/Projects/My/NCCliClient/internal/cli/help_test.go)
  (`TestCmdSpecs_CoverAllRoutes — … ровно те же 9 команд`) и
  [help_test.go:323](file:///Users/stas/Projects/My/NCCliClient/internal/cli/help_test.go)
  (`TestHandleHelp_DetailedContent — для каждой из 9 команд`). Это комментарии, тесты не упадут,
  но спека позиционирует список как полный.
- **Что сделать:** дополнить перечень тремя док-комментариями в `*_test.go`.
