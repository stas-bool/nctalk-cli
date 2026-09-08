# Дизайн: `nctalk chat edit` — редактирование отправленных сообщений

- **Дата:** 2026-09-08
- **Статус:** дизайн одобрен пользователем; реализация через /spec-to-code --auto
- **Базовая спека:** `docs/superpowers/specs/2026-07-17-nctalk-cli-design.md` (эталон контракта
  базового CLI; этот документ — дельта, добавляющая восьмую команду)
- **Стек:** Go 1.21+, только stdlib, `CGO_ENABLED=0`

## 1. Цель и контекст

Добавить в `nctalk` примитив «отредактировать уже отправленное сообщение». Потребитель —
агент (через skill-обёртку) и человек в терминале. Как и весь CLI, команда остаётся тонким
примитивом над OCS-API: без бизнес-логики, без подтверждений, без интерактива.

Выбор подхода (из брейншторма 2026-09-08):

- **A (принято): отдельная команда `chat edit`** — симметричная `chat send`. Чистая
  семантика: send = создать, edit = переписать.
- B (отклонено): флаг `--edit` у `chat send` — ломает семантику send, конфликтует с
  `--reply-to`.
- C (отклонено): блок правок edit+delete+пометки в истории — шире запроса.

Решения пользователя по развилкам:

1. Источник текста — **как у `chat send`**: stdin по умолчанию, `--file <путь>` как
   альтернатива. Без `--text`.
2. Вывод при успехе — **id сообщения** (как у `chat send`); в `--json` — `{"id": <int>}`.
3. `chat show` **не меняется** — признак «отредактировано» не показывается (тонкий
   клиент; правка видна просто новым текстом).

### Out of scope

- Удаление сообщений (`DELETE` того же эндпоинта) — отдельная задача.
- Пометка редактирования в выводе `chat show` (поля `lastEditActorId/lastEditTimestamp`
  в API есть) — отклонено, тонкий клиент.
- Проверка capability `edit-messages` до запроса — НЕ делаем: тонкий клиент не строит
  матрицу возможностей, ошибку сервера (404/405 на старом Talk) достаточно.

## 2. Контракт команды

```
nctalk chat edit <room> <messageId> [flags]
```

- `<room>` — token комнаты позиционно **или** `--name "<имя>"` — то же правило
  разрешения, что у `chat show`/`chat send` (базовая спека §7): 1 совпадение → ок,
  >1 → кандидаты в stderr, exit `3`; 0 → exit `2`.
- `<messageId>` — id сообщения, целое число. Невалидное значение (не число /
  `<= 0`) → клиентская ошибка exit `1` ДО любых сетевых вызовов (ни `FindRooms`,
  ни `EditMessage`).
- Распределение позиционных аргументов — **единообразно с `reactions get`**
  (команда с той же парой `<room> <messageId>`):
  - ≥2 позиционных → первый = `<room>` (token), второй = `<messageId>`; заданный
    при этом `--name` игнорируется с предупреждением (поведение `ResolveRoom`);
  - 1 позиционный + `--name` → позиционный это `<messageId>`, комната
    разрешается по `--name`;
  - 1 позиционный без `--name` → ошибка exit `1` «ожидается <room> <messageId>»;
  - 0 позиционных без `--name` → ошибка exit `1` «ожидается <room> <messageId>
    или --name <имя> <messageId>»;
  - лишние позиционные (третий и далее) игнорируются.
- Тело нового текста:
  - stdin по умолчанию (`io.ReadAll(deps.Stdin)`, при nil — `os.Stdin`);
  - `--file <путь>` — файл целиком (`os.ReadFile`);
  - срезается РОВНО ОДИН завершающий `\n` / `\r\n` (многострочные тела валидны) —
    идентично `chat send`;
  - пустое тело (пустой stdin/файл или только перевод строки) → exit `1` до сети.
- Флаги: `--name <имя>`, `--file <путь>`. Других флагов НЕТ: `--silent`,
  `--reply-to`, `--reference-id` для правки бессмысленны. Глобальный `--json` —
  в любой позиции (извлекается слоем `Run` до роутинга, как везде).
- Лишние позиционные аргументы (третий и далее) игнорируются — единообразно с
  `chat send`.

### Вывод

- Успех: exit `0`; в stdout id отредактированного сообщения — текстом `<int>\n`
  или `{"id": <int>}` при `--json` (переиспользуем `render.NewMessageID` /
  `render.NewMessageIDJSON` — тот же вывод, что у `chat send`).
- Значение id берётся из ответа сервера (`ocs.data.parent.id`, см. §3), а НЕ
  эхом входного аргумента — фиксирует контракт формата ответа тестами.

### Exit-коды (наследуют базовый контракт §7, без изменений)

| Ситуация | Код |
|---|---|
| успех (в т.ч. `202 Accepted`) | `0` |
| пустое тело / невалидный `messageId` / неизвестный флаг / сеть / 401 / 5xx | `1` |
| `404` OCS (комната или сообщение не найдены) · 0 совпадений `--name` | `2` |
| >1 совпадение `--name` | `3` |

Серверные отказа правки (все — OCS-error с текстом сервера, exit `1`):
`400` (сообщение старше 24 часов или правка запрещена), `403` (чужое сообщение,
не модератор; read-only комната), `405` (не обычное comment-сообщение),
`412` (lobby). Клиент ничего из этого не предугадывает — просто показывает
ошибку сервера.

## 3. API-контракт (Talk chat-API)

Эндпоинт (официальная документация Nextcloud Talk, Chat API «Editing a Chat
Message»; доступность сигнализируется capability `edit-messages`):

```
PUT /ocs/v2.php/apps/spreed/api/v1/chat/{token}/{messageId}
```

- Заголовки: `Content-Type: application/json`, `OCS-APIRequest: true`,
  `Accept: application/json` + Basic-auth (mutate-путь `doOCS`, как у
  `SendMessage`).
- Тело: `{"message": "<новый текст>"}` — единственное поле.
- Ответ `200 OK` (или `202 Accepted` при бот-интеграции): `ocs.data` — объект
  **системного сообщения** о правке, в котором поле `parent` содержит
  **обновлённое** сообщение. Клиенту из ответа нужен только
  `ocs.data.parent.id` (id отредактированного сообщения = входной id).
- Документация явно предупреждает: системное сообщение предназначено для
  обновления кэша клиентов, не для отображения — поэтому больше ничего из
  `ocs.data` не разбираем.
- Права и ограничения (серверные, не дублируются клиентом): редактировать можно
  свои сообщения (модераторам — чужие); правка только в течение 24 часов с
  отправки; тип сообщения — comment.

**Не проверено на живом API (мутация):** формат PUT-ответа взят из официальной
документации Talk; проверяется интеграционным тестом при реализации (цикл
send → edit → chat show, см. §6). Фикстура unit-тестов обязана отражать этот
формат (правило базовой спеки про testdata).

## 4. Слой client

Новый метод `TalkClient` (в `internal/client/chat.go`, рядом с `SendMessage`):

```go
type EditMessageOpts struct {
    Message string // обязательный; пустой → клиентская ошибка ДО сети
}

func (c *TalkClient) EditMessage(ctx context.Context, token string, messageId int, opts EditMessageOpts) (int, error)
```

- Клиентские guard-ы до сети (защита прямых вызовов, дублем с CLI — как у send):
  `opts.Message == ""` и `messageId <= 0` → ошибка без запроса.
- Запрос: `PUT pathChat/{token}/{messageId}` (pathChat — существующая константа
  `/ocs/v2.php/apps/spreed/api/v1/chat`), `url.PathEscape` обоих сегментов,
  тело `{"message": "..."}` через `json.Marshal`, `mutate=true`.
- Ответ: минимальная форма `ocs.data.parent.id`:

```go
type editMessageResp struct {
    Parent struct {
        Id int `json:"id"`
    } `json:"parent"`
}
```

- Возвращает `parent.id`; OCS-error (любой statusCode >= 400) — стандартная
  обработка `doOCS` → `*client.OCSError` (санитайзинг сохранён).
- Метод добавляется в интерфейс `cli.TalkClient` (`internal/cli/cli.go`) —
  compile-time проверка `var _ TalkClient = (*client.TalkClient)(nil)` ловит
  рассинхрон.

## 5. Слой cli

- `chatEditHandler` в `internal/cli/handlers_chat.go` — по образцу
  `chatSendHandler` (чтение тела) и `reactionsGetHandler` (распределение
  позиционных `<room> <messageId>`), порядок шагов:
  1. ручной scan флагов (`--name`, `--file`; обе формы `--flag value` и
     `--flag=value`; неизвестный `--*` → exit `1`);
  2. распределение позиционных по правилу §2 (как `reactionsGetHandler`):
     нехватка аргументов → exit `1` с подсказкой;
  3. чтение тела ДО `ResolveRoom` (пустое → exit `1` без сети) + трим одного
     trailing `\n`/`\r\n`;
  4. парсинг `messageId` (`strconv.Atoi`; невалид/`<=0` → exit `1` до сети);
  5. `ResolveRoom(roomPos, nameFlag)` — коды `2`/`3` сохраняются;
  6. `EditMessage`;
  7. вывод id через `render.NewMessageID{,JSON}` по `jsonOut`.
- Роутинг: `"edit": chatEditHandler` в `routes["chat"]` (`internal/cli/cli.go`);
  подсказка «ожидается verb (list/find/search/show/send/get)» дополняется `edit`.
- Help: запись в `cmdSpecs` (`internal/cli/help.go`) — единый источник правды;
  general/resource/detailed help и anti-drift тесты (`TestCmdSpecs_...`,
  `TestAntiDrift_...`) подхватывают декларацию автоматически.

  ```
  Path: ["chat", "edit"]
  Short: "отредактировать отправленное сообщение (stdin или --file)"
  UsageExtras: "<room> <messageId>"
  Flags: --name <имя>, --file <путь>
  Examples:
    echo "исправлено" | nctalk chat edit abc123 100
    nctalk chat edit --name "Команда" 100 --file new.txt
  ```

- `render` не расширяется.

### Сопроводительные правки документации (входят в задачу)

- README: команда в списке/доках команд (полная RU-документация уже в README —
  добавить секцию `chat edit` по образцу `chat send`).
- CLAUDE.md: «7 команд» → «8 команд».

## 6. Тестирование

- **client (`internal/client/chat_test.go`):** httptest-сервер:
  - успех: фикс-ответ реального формата (конверт `ocs.data` = системное сообщение
    с `parent.id`) → метод вернул `parent.id`; проверка метода/пути/тела
    запроса (`PUT`, `chat/{token}/{messageId}`, `{"message":...}`, заголовок
    Content-Type);
  - OCS-ошибки 400/403/404/405 → `*client.OCSError` с кодом и текстом;
  - guard-ы: пустое сообщение и `messageId<=0` → ошибка без сетевого вызова
    (сервер не запускался/не вызывался).
  - Фикстура в `internal/client/testdata/` — обезличенная, реального формата.
- **cli (`internal/cli/handlers_chat_test.go`):** table-driven, клиент — mock:
  успех (stdout = id, текст и `--json`), пустое тело (клиент не звался),
  невалидный `messageId` (клиент не звался), неизвестный флаг, `--name`
  (однозначно/неоднозначно/ноль → 0/3/2), OCS 404 → exit 2, OCS 403 → exit 1
  с текстом сервера, лишние позиционные игнорируются.
- **help (`internal/cli/help_test.go`):** автоматически анти-drift'ом после
  добавления в `cmdSpecs` (декларация ↔ handler ↔ help-тексты).
- **Интеграционный (`internal/client/integration_test.go`, build-тег
  `integration`):** цикл под существующими мутационными флагами
  `NCTALK_INTEGRATION_SEND=1` + `NCTALK_INTEGRATION_ROOM` (новых env не
  вводим): `SendMessage` → `EditMessage` → `GetChat` проверяет новый текст.
  Побочный мусор (исправленное сообщение + system-сообщение о правке) —
  приемлемо, убирается вручную так же, как за send-тестами.

## 7. Будущее (явно за рамками)

- `chat delete <room> <messageId>` — `DELETE` того же эндпоинта.
- Пометка «(ред.)» в `chat show` по `lastEditTimestamp` (если понадобится).
