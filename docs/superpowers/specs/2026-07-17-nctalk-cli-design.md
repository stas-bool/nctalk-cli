# Дизайн: `nctalk` — CLI-клиент Nextcloud Talk

- **Дата:** 2026-07-17
- **Статус:** одобрен дизайн, ожидает ревью спеки → план реализации
- **Стек:** Go 1.21+
- **Репозиторий:** `/Users/stas/Projects/My/NCCliClient` (git, ветка `main`)

## 1. Цель и контекст

Тонкий CLI-клиент над Nextcloud Talk (приложение Spreed) поверх OCS-API. Только
примитивы: список/поиск чатов, чтение и отправка сообщений, реакции, поиск.
Основной потребитель — **агент** (через skill-обёртку), вторичный — человек в
терминале. Бизнес-логику (например, «найди MR без реакции») клиент **не
содержит** — её агент собирает из примитивов.

Исходные пользовательские сценарии:
1. «зайди в чат ссылок на ревью, найди MR без реакции, скачай в ворктри и сделай ревью»;
2. «посмотри последнее сообщение от Ярослава, не понимаю что он хочет»;
3. «создай MR в GitLab и скинь ссылку в чат ревью».

Сценарии 1 и 3 частично закрыты существующим skill `pp-code-review`
(`gitlab-mr.sh`, `gitlab-api.md`). Новая, недостающая часть — именно Talk.

### Out of scope (на этот spec НЕ входит)
- Запись звука/видео из звонков — отдельная подсистема (WebRTC/HPB); в этом
  проекте не делаем. У сервера уже работает бот «Talk Transcriber».
- GitLab/MR и сам code-review — отдельные skills.
- «Умные» высокоуровневые команды (`review pending` и т.п.) — отложены в
  `presets/` на будущее (см. §6).

## 2. Исследования (готового клиента нет)

- Готового CLI/TUI-клиента Talk для чтения чатов — не найдено.
- Официальный `nextcloud/client-sdks` (Go) — сырой прототип, **Talk не покрывает**
  (только core/files_sharing/provisioning/user_status/weather). Как основу не
  используем.
- `nextcloud/talk-desktop` — десктоп (JS), `nextcloudcmd` — только файлы. Не наш кейс.
- Talk REST-API отлично документирован — пишем свой тонкий клиент на ~7 эндпоинтов.
- **Unified Search** (core-API) даёт два полезных Talk-провайдера:
  `talk-message` (поиск сообщений) и `talk-conversations` (поиск чатов).

## 3. Архитектурная форма

- Единый Go-бинарник **`nctalk`** + тонкий skill-обёртка (описывает параметры и
  примеры вызова для агента).
- Подход **A — «тонкий клиент»** (Unix-философия): только примитивы над API.
- **Ядро изолировано** от CLI/вывода; будущие пресеты уровня C живут отдельным
  пакетом `presets/` поверх публичного `client` и ядро о них не знают.
- Только стандартная библиотека (`net/http`, `encoding/json`, разбор флагов).
  `cobra` добавим, только если станет тесно.
- Вывод по умолчанию человекочитаемый (таблицы), флаг `--json` — машиночитаемый.

## 4. Структура пакетов

```
nccliclient/
├─ cmd/nctalk/main.go     # точка входа: config → client → cli
├─ internal/
│  ├─ config/             # env: NEXTCLOUD_URL/LOGIN/PASS/TIMEOUT + валидация
│  ├─ client/             # ЯДРО: HTTP-клиент Talk (Rooms/Chat/Reactions/Search)
│  ├─ cli/                # разбор флагов, роутинг, правило room, exit-коды
│  └─ render/             # вывод: text-таблица / json
├─ presets/               # (будущее) обёртки уровня C поверх client
└─ testdata/              # обезличенные JSON-фикстуры ответов Talk
```

### Ответственность слоёв
- **`config`** — читает и валидирует env (URL и креды обязательны, timeout по
  умолчанию 30с). Зависимостей нет.
- **`client`** — тип `TalkClient` с методами `ListRooms`, `FindRooms`,
  `SearchRooms`, `GetChat`, `SendMessage`, `GetReactions`, `SearchMessages`.
  Внутри: `net/http`, Basic Auth, заголовки `OCS-APIRequest: true` /
  `Accept: application/json` (а для mutation-запросов — дополнительно
  `Content-Type: application/json`, см. §6 `chat send` и §9), распаковка
  конверта `ocs.data`, маппинг JSON → struct. Сообщение маппится в полный набор
  полей (минимум: `id, actorType, actorId, actorDisplayName, messageType,
  systemMessage, message, messageParameters, reactions, timestamp, isReplyable`;
  полный список с реального API — см. §12). Ничего не знает про CLI/вывод.
  Зависит от интерфейса `httpDoer` → мокируется в тестах.
- **`cli`** — парсит `os.Args`, применяет правило разрешения room, ставит
  exit-коды. Зависит от `client` через интерфейс (для mockability) и от `render`.
- **`render`** — превращает структуры в таблицу или JSON. Зависит от типов `client`.

## 5. Конфиг и безопасность

Переменные окружения (всё, что чувствительно — только из env):
- `NEXTCLOUD_URL` — базовый URL сервера (напр. `https://nc.example.org`).
  Указывает на **корень Nextcloud** (откуда доступен `/ocs/...`). Нормализация
  при загрузке: трим trailing slash; пути клеятся через path-join без двойных
  слэшей. Корректно обрабатывается `301` на canonical URL — редирект
  отслеживается через `CheckRedirect` (same-host, см. ниже), canonical-форма в
  конфиг не перезаписывается.
- `NEXTCLOUD_LOGIN` — логин.
- `NEXTCLOUD_PASS` — app-password. **Никогда** не из аргументов (чтобы не светился
  в `ps`/истории) и **никогда** не попадает в вывод/ошибки/паники/отладку.
- `NEXTCLOUD_TIMEOUT` — HTTP-таймаут (по умолч. `30s`).

Все HTTP-запросы идут с `context.Context` → Ctrl-C отменяет.

### Redact креденшалов (механизм, не только обещание)
- Креды передаются **только** в заголовке `Authorization` (Basic), никогда в URL,
  argv или логах. В `NEXTCLOUD_URL` userinfo не подставляется.
- Все ошибки пропускаются через функцию-санитайзер: `*url.Error` и любые URL
  приводятся к виду `scheme://host` (без userinfo/query; путь оставляется только
  когда он необходим для диагностики, query/userinfo вырезаются всегда).
- Dump `http.Request` в лог (`httputil.DumpRequestOut` / `DumpResponse`) —
  **запрещён** даже в debug-режиме.
- `http.Client.CheckRedirect`: разрешается только same-host redirect
  (`redirect.Host == req.Host`); на cross-host redirect — ошибка (не следуем,
  не теряем и не утекает `Authorization`). Это закрывает риск утечки Basic-auth
  на чужой хост при злом редиректе.

## 6. Команды (MVP — 7 примитивов)

Общий вид: `nctalk <resource> <verb> ...`. Глобальный флаг `--json` везде.

### `rooms list`
Список чатов. Флаги: `--type <1|2|3>` (личные/группы/публичные), `--unread`,
`--include-former`, `--json`. Эндпоинт: `GET /ocs/v2.php/apps/spreed/api/v4/room`.
`--type` в MVP принимает значения `1`/`2`/`3`; типы `4`/`5`/`6` (former
one-to-one/group/public) **по умолчанию скрыты** и показываются только при явном
`--type` либо при флаге `--include-former` (см. §12 про типы комнат).
Вывод: таблица `ТИП | ЧАТ | TOKEN | НЕПРОЧИТАНО | ПОСЛЕДНЕЕ (время, автор, превью)`;
в таблице и в `--json` всегда присутствует `actorId` комнаты (для type=1 — это
собеседник).

### `rooms find <запрос>`
Сопоставление по case-insensitive подстроке `displayName` + флаг
`--user <actorId>` (найти личный чат 1-на-1 с человеком). Источник: `/v4/room` с
фильтрацией на клиенте. `--user` принимает **только actorId (userId)**, не
displayName — комната type=1 несёт `actorId` собеседника (см. §12), поэтому
резолв «пользователь → личный чат» делается прямым сравнением `actorId`.
Главное в выводе — **token**; возвращает полные данные комнаты (type, unread,
actorId). `find` возвращает **все** совпадения (одно или несколько); правило
«exit 3 / неоднозначно» к нему **не применяется** — оно относится только к
разрешению `<room>` в chat/reactions-командах (§7). Пустой результат — exit `0`
с пустым выводом (поисковая команда, см. §7).

### `rooms search <term>`  *(server-side fuzzy)*
Fuzzy-поиск чатов по названию через Unified Search. Эндпоинт:
`GET /ocs/v2.php/search/providers/talk-conversations/search?term=…&limit=…`.
Возвращает `title` + `attributes.conversation` (token). В отличие от `find` —
быстрый server-side fuzzy, но бедные данные (только name+token). Порог `term` =
1 символ (см. §8); пустой результат — exit `0` с пустым выводом (поисковая
команда, см. §7).

### `chat show <room>`
Читать сообщения комнаты.
- `<room>` — token позиционно **или** `--name "<имя>"` (case-insensitive
  **подстрока** по `displayName`, без Levenshtein в MVP; правило разрешения из §7
  применяется к `--name` так же, как к позиционному аргументу).
- Фильтры и потолок выборки:
  - `--last <N>` (по умолч. `20`) — **потолок выборки ДО применения фильтров**:
    «взять последние N сообщений комнаты». `chat show --last N --from X` =
    «среди последних N сообщений показать от X».
  - `--from <actorId>` — фильтр **поверх** `--last`, применяемый только к
    comment-сообщениям (по `actorId`); работает с actorId, не displayName.
  - `--since <время>` — фильтр **поверх** `--last`; форматы см. ниже.
  - Если `--from`/`--since` заданы **без** явного `--last`, потолок выборки =
    **cap 200** сообщений (тянем до 200, режем локально). Если достигли cap, а
    совпадений по фильтру мало — вывести в stderr предупреждение:
    «выборка ограничена 200 сообщениями, используй narrower-диапазон».
- **Пагинация:** тянутся страницы по `limit=200` (max, проверено на живом API,
  см. §12), перебирая `lastKnownMessageId` назад (`lookIntoFuture=0`). **Ранний
  стоп:** если задан `--since`, перестаём тянуть, когда дошли до сообщения старше
  `--since` (или до начала чата). По `--from` раннего стопа нет — тянем в рамках
  cap.
- **Форматы `--since`:** относительные `30s`/`15m`/`2h`/`1d` (суффиксы s/m/h/d);
  дата `2026-07-17` (= начало дня 00:00:00); полный ISO `2026-07-17T13:00:00`
  или с зоной `2026-07-17T13:00:00+03:00`. Относительные значения и дата-без-зоны
  интерпретируются в **локальном времени процесса** (TZ из окружения);
  ISO-с-зоной — как есть. Вывод времени в `chat show` — локальный. `--since` —
  клиентский фильтр (серверного в chat-API нет).
- **System-сообщения:** по умолчанию system-сообщения (`messageType != "comment"`)
  **скрыты**; флаг `--system` показывает их.
- **Эндпоинт:** `GET /ocs/v2.php/apps/spreed/api/v1/chat/{token}` с
  `limit` / `lookIntoFuture=0` / `lastKnownMessageId`.
- **Структура сообщения:** маппится минимум полей `id, actorType, actorId,
  actorDisplayName, messageType, systemMessage, message, messageParameters,
  reactions, timestamp, isReplyable` (полный набор — см. §12). `timestamp` —
  секунды Unix (number, 10 цифр).
- **Подстановка `messageParameters`:** плейсхолдеры `{<key>}` в `message`
  заменяются на человекочитаемое значение: `{actor}`→`name`, `{file}`→`name`
  (в `--json` опционально прикладывается ссылка), `{mention-userN}`→`name`. В MVP
  покрываем минимум: `actor`, `file`, `mention-*`. Нераспознанные плейсхолдеры
  оставляем как есть. Поле вывода «текст» = результат подстановки.
- **Реакции:** поле `reactions` (мапа `emoji → count`) присутствует в каждом
  сообщении (см. §12) — счётчики `👍×2` в выводе берутся из него **без N+1**
  запросов. Детальный вывод (кто именно) — отдельная команда `reactions get`.
- **Вывод** построчно: `[id] время автор: текст   👍×2 ✅×1`.

### `chat send <room>`
Отправить сообщение. Источник тела: **stdin по умолчанию** (`-`), либо
`--file <path>`. Флаги: `--reply-to <id>` (id сообщения, int), `--silent`,
`--reference-id <uuid>` (опц., клиентский dedup).
- **Эндпоинт:** `POST /ocs/v2.php/apps/spreed/api/v1/chat/{token}`.
- **Заголовки:** `Content-Type: application/json`, `OCS-APIRequest: true`,
  `Accept: application/json` (плюс `Authorization` — общее правило §5).
- **Тело запроса (JSON):**
  `{"message": "<текст>", "replyTo": <int, id сообщения, опц.>, "silent": <bool, опц.>, "referenceId": "<uuid, опц.>"}`.
  `replyTo` — число (id сообщения); невалидный `replyTo` → OCS-error.
- **Ответ:** id нового сообщения берётся из `ocs.data.id`.
- **Вывод:** id отправленного сообщения.
- *(Формат по документации Talk; POST на боевом сервере намеренно не
  проверялся — см. §12.)*

### `reactions get <room> <messageId>`
Кто поставил реакции (детально). Эндпоинт:
`GET /ocs/v2.php/apps/spreed/api/v1/reaction/{token}/{messageId}`.
- **Формат ответа:** `ocs.data` — мапа `emoji → [объекты-актёры]`; каждый актёр
  содержит `actorType, actorId, actorDisplayName, timestamp`.
- **Пустой случай:** при отсутствии реакций `data={}` → вывод «реакций нет»,
  exit **0** (не 2).
- **Вывод:** `реакция → [авторы]`.

### `search <term>`
Глобальный поиск сообщений по содержимому через Unified Search. Эндпоинт:
`GET /ocs/v2.php/search/providers/talk-message/search?term=…&person=…&limit=…`
(поддерживает пагинацию через `cursor`). Флаги: `--from <actorId>` (фильтр по
автору, серверный, маппится в `person=`), `--limit <N>`, `--all`, `--json`.
- **Пагинация:** по умолчанию тянется **одна страница**. `--limit` (по умолч.
  `10`, max `25`) маппится в `limit=` URL. Многостраничный сбор — флаг `--all` с
  верхним cap (5 страниц): перебираем `cursor` из ответа, стоп при
  `isPaginated=false` либо пустом `cursor`.
- **Единицы timestamp:** в `attributes.timestamp` приходит **строка секунд** —
  парсить в int перед сравнением/выводом (см. §12).
- **Вывод:** `время | автор | chat#messageId | текст`. Закрывает кейс №2.
- Пустой результат — exit `0` с пустым выводом (поисковая команда, см. §7).

## 7. Идентификация ресурсов и exit-коды

**Правило разрешения `<room>`** (для `chat show` / `chat send` / `reactions get`):
- позиционный аргумент = **token** (primary): `nctalk chat show kytxaiyc`;
- `--name "<имя>"` — case-insensitive **подстрока** по `displayName` через
  `/v4/room` (без Levenshtein в MVP);
- **1 совпадение** → выполняем; **>1** → не угадываем, выводим кандидатов
  `ТИП | имя | token`, exit `3`; **0** → exit `2`.

Почему token primary: он канонический, однозначный и стабильный; имя могут
переименовать. Конвейер агента: `rooms find/search` → взял token → далее
работает с canonical id.

**Exit-коды:** `0` ок · `1` общая ошибка (сеть/авторизация) · `2` not found ·
`3` неоднозначно.

**Empty results vs not-found:** exit `2` (not found) — **только** для разрешения
`<room>` в `chat show` / `chat send` / `reactions get` (0 совпадений по имени).
Для **поисковых** команд (`rooms find`, `rooms search`, `search`) пустой
результат = **exit `0`** с пустым выводом — агентский конвейер не должен
прерываться на «ничего не нашлось».

## 8. Поведение фильтров и ограничения

### Единая модель идентификаторов «человека»
- Все флаги, принимающие «человека» (`chat show --from`, `rooms find --user`,
  `search` через `person=`), работают **только с actorId (userId)**, не с
  displayName. displayName — только выводится.
- `rooms find --user <actorId>` находит личный чат (type=1) с этим пользователем,
  т.к. комната type=1 несёт `actorId` собеседника (см. §12).
- Узнать actorId по displayName можно из вывода существующих команд: `rooms list`
  / `rooms find` показывают `actorId` (в `--json` — всегда; в таблице — колонка),
  `chat show` показывает `actorId` каждого сообщения (в `--json`). Полноценный
  резолв «displayName → actorId» через autocomplete (`users find`) — за рамками
  MVP (§11).

### `chat show` — стратегия выборки и фильтры
- Talk chat-API **не умеет** серверный фильтр по автору, поэтому `--from` и
  `--since` режутся локально поверх выборки.
- Потолок выборки = `--last N` (по умолч. `20`). Если `--from`/`--since` заданы
  без явного `--last`, потолок = **cap 200** сообщений. При достижении cap и
  малом числе совпадений — предупреждение в stderr (см. §6).
- **Ранний стоп пагинации:** при `--since` перестаём тянуть, когда дошли до
  сообщения старше `--since` (или до начала чата). По `--from` раннего стопа нет
  (тянем в рамках cap).
- Для глобального поиска предпочтительнее `search` (серверный фильтр `person`,
  точнее).

### Timestamp-единицы (нормализация перед сравнением)
- chat-API: `timestamp` — **секунды (number, 10 цифр)**.
- Unified Search: `attributes.timestamp` — **строка секунд** (парсить в int).
- Перед сравнением с `--since` (и любыми временем-фильтрами) — нормализовать в
  int-секунды.

### Unified Search: минимальная длина `term`
- Порог = **1 символ** (проверено на живом API, см. §12). Пустой `term` →
  серверный `400 Bad Request`; клиент валидирует непустой `term` и даёт понятную
  ошибку, а серверный `400` показывается с его `message`.

## 9. Обработка ошибок

- Сеть/HTTP → exit `1` + понятное сообщение (`timeout` / `401 bad credentials` /
  `404 not found`).
- OCS-error-конверт (`ocs.meta.statusCode` + `message`) → извлекаем и показываем.
  В т.ч. невалидный `replyTo` в `chat send` → OCS-error с сообщением сервера.
- not found / ambiguous → exit `2` / `3` (с учётом §7: empty results у поисковых
  команд = exit `0`).
- **Redact креденшалов:** пароль редьюсится из любых диагностических сообщений
  конкретным механизмом — см. §5 «Redact креденшалов» (санитайзер `*url.Error` /
  URL → `scheme://host`, запрет dump'а `http.Request`, `CheckRedirect` same-host
  only). Для mutation-запросов (`chat send`) заголовок `Content-Type:
  application/json` обязателен — иначе сервер вернёт OCS-error.

## 10. Тестирование

- `client` — через `httptest.Server` с фикс-ответами Talk: проверка маппинга и
  распаковки OCS-конверта, без живого сервера. Реальные (обезличенные)
  JSON-фикстуры — в `testdata/`.
- `cli`/`render` — table-driven тесты, клиент мокается через интерфейс.
- Интеграция с боевым сервером — **не в CI** (креды/сеть); опционально
  `go test -tags=integration` вручную при наличии env-кредов.

## 11. Будущее (явно за рамками MVP)

- Пресеты уровня C в `presets/` (например, сборка «MR без реакции»).
- `users find` (autocomplete) для резолва имени → actorId.
- Мутации реакций (поставить/снять), упоминания, вложения, файлы.
- Поллинг новых сообщений (`lookIntoFuture`), отметка прочитанным, mute.
- Запись звонков (отдельная подсистема WebRTC/HPB).

## 12. Проверка на живом API (выполнено 2026-07-17)

Подтверждено `HTTP 200` под реальными кредами:
- `/ocs/v2.php/apps/spreed/api/v4/room` → 43 комнаты, поля `displayName/token/type/unreadMessages/lastMessage`.
- `/ocs/v2.php/search/providers` → есть `talk-message`, `talk-conversations`.
- `talk-message/search?term=…` → entries: `title`(автор), `subline`(текст),
  `resourceUrl`(`…/call/{token}#message_{id}`), `attributes.{conversation,messageId,actorType,actorId,timestamp}`.
- `talk-conversations/search?term=…` → entries: `title`(имя), `attributes.conversation`(token).
- Фильтр `person=<actorId>` принимается и применяется.

### Детальные факты API (проверено 2026-07-17)

- **`GET /ocs/v2.php/apps/spreed/api/v4/room`** — 43 комнаты. Комната несёт
  поля: `displayName, token, type, unreadMessages, lastMessage, actorId,
  actorType, listable, …`. **Для `type=1` (one-to-one) `actorId` комнаты =
  собеседник** — этого достаточно для резолва «пользователь → личный чат».
  Типы комнат в выдаче: `1` (личные), `2` (группы), `3` (публичные),
  `4`/`5`/`6` (former one-to-one/group/public).
- **`GET /ocs/v2.php/apps/spreed/api/v1/chat/{token}?limit=N&lookIntoFuture=0`** —
  `200 OK`. Сервер принимает **большой limit** (проверено `limit=200` → вернулось
  200 сообщений). Объект сообщения содержит поля: `id, actorType, actorId,
  actorDisplayName, messageType, systemMessage, message, messageParameters,
  reactions, referenceId, timestamp, isReplyable, markdown, threadId,
  expirationTimestamp, token`.
  - `timestamp` — **секунды Unix (number, 10 цифр)**.
  - `message` может содержать плейсхолдеры вида `{file}`, `{actor}`,
    `{mention-userN}`; реальные значения — в **объекте** `messageParameters`
    (ключи = имена плейсхолдеров; каждое значение имеет `type/id/name/…`).
    Подтверждён пример: `message="{file}"`,
    `messageParameters.file={type:"file", id, name, path, link, …}`.
    **В chat-API `messageParameters` — ВСЕГДА объект** (и для comment, и для
    system) — это канонический формат для подстановки плейсхолдеров.
  - **ВАЖНО: `messageParameters` имеет разный формат в разных эндпоинтах**
    (подтверждено на живом API 2026-07-17 при отладке падения `rooms list`):
      * **chat-API** (`/ocs/v2.php/apps/spreed/api/v1/chat/{token}`) — ВСЕГДА
        **объект** `{actor: {...}, file: {...}, mention-userN: {...}, ...}`
        (и для comment, и для system).
      * **`/ocs/v2.php/apps/spreed/api/v4/room` → `lastMessage.messageParameters`** —
        для `messageType=comment` — **массив** `[]` (как правило пустой; для
        comment `message` уже человекочитаемый текст, параметров нет), для
        `messageType=system` — **объект** (как в chat-API).
    Единый тип `map[string]MsgParam` не маппит массив → `rooms list` падал с
    `cannot unmarshal array into Go struct field`. Поэтому в коде введён
    именованный тип `MsgParams` с `UnmarshalJSON`: массив трактуется как
    nil-карта (параметров нет), объект парсится как обычно. Логика подстановки
    плейсхолдеров работает только для объектного случая.
  - `messageType` принимает значения `comment` и `system` (а также
    `voice-message` и др.); `systemMessage` — непустой маркер для системных
    сообщений.
  - `reactions` в сообщении — объект-мапа `emoji → count` (для счётчиков в
    `chat show`).
- **`GET /ocs/v2.php/apps/spreed/api/v1/reaction/{token}/{messageId}`** —
  `200 OK`. При отсутствии реакций → `data: {}` (пустой объект). Формат — мапа
  `emoji → [объекты-актёры]` (каждый актёр: `actorType, actorId,
  actorDisplayName, timestamp`).
- **Unified Search `talk-conversations` / `talk-message`** — `term` минимальной
  длины **1 символ** (пустой `term` → `400`). talk-message entry `attributes`:
  `actorId, actorType, conversation(=token), messageId, timestamp`, причём
  `timestamp` — **строка секунд** (нормализовать в int). Конверт пагинации: поля
  `cursor` (строка), `isPaginated` (bool), `entries`, `name`.

### Не проверено (мутации на боевом сервере)
- `POST /ocs/v2.php/apps/spreed/api/v1/chat/{token}` (отправка сообщения) и
  `POST` / `DELETE`
  `/ocs/v2.php/apps/spreed/api/v1/reaction/{token}/{messageId}/{emoji}` — формат
  берётся из документации Talk (не проверено на живом API). См. §6 `chat send`
  и §11 «мутации реакций».
