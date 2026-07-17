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
  `Accept: application/json`, распаковка конверта `ocs.data`, маппинг JSON →
  struct. Ничего не знает про CLI/вывод. Зависит от интерфейса `httpDoer` →
  мокируется в тестах.
- **`cli`** — парсит `os.Args`, применяет правило разрешения room, ставит
  exit-коды. Зависит от `client` через интерфейс (для mockability) и от `render`.
- **`render`** — превращает структуры в таблицу или JSON. Зависит от типов `client`.

## 5. Конфиг и безопасность

Переменные окружения (всё, что чувствительно — только из env):
- `NEXTCLOUD_URL` — базовый URL сервера (напр. `https://nc.example.org`).
- `NEXTCLOUD_LOGIN` — логин.
- `NEXTCLOUD_PASS` — app-password. **Никогда** не из аргументов (чтобы не светился
  в `ps`/истории) и **никогда** не попадает в вывод/ошибки/паники/отладку.
- `NEXTCLOUD_TIMEOUT` — HTTP-таймаут (по умолч. `30s`).

Все HTTP-запросы идут с `context.Context` → Ctrl-C отменяет.

## 6. Команды (MVP — 7 примитивов)

Общий вид: `nctalk <resource> <verb> ...`. Глобальный флаг `--json` везде.

### `rooms list`
Список чатов. Флаги: `--type <1|2|3>` (личные/группы/публичные), `--unread`,
`--json`. Эндпоинт: `GET /ocs/v2.php/apps/spreed/api/v4/room`.
Вывод: таблица `ТИП | ЧАТ | TOKEN | НЕПРОЧИТАНО | ПОСЛЕДНЕЕ (время, автор, превью)`.

### `rooms find <запрос>`
Сопоставление по подстроке `displayName` + флаг `--user <имя>` (найти личный чат
1-на-1 с человеком). Источник: `/v4/room` с фильтрацией на клиенте. Главное в
выводе — **token**; возвращает полные данные комнаты (type, unread). `find`
возвращает **все** совпадения (одно или несколько); правило «exit 3 /
неоднозначно» к нему **не применяется** — оно относится только к разрешению
`<room>` в chat/reactions-командах (§7).

### `rooms search <term>`  *(server-side fuzzy)*
Fuzzy-поиск чатов по названию через Unified Search. Эндпоинт:
`GET /ocs/v2.php/search/providers/talk-conversations/search?term=…&limit=…`.
Возвращает `title` + `attributes.conversation` (token). В отличие от `find` —
быстрый server-side fuzzy, но бедные данные (только name+token).

### `chat show <room>`
Читать сообщения комнаты.
- `<room>` — token позиционно **или** `--name "<имя>"` (fuzzy, правило разрешения ниже).
- фильтры: `--from <actorId>` (локально, см. §8), `--last <N>` (по умолч. 20),
  `--since <время>` (ISO или относительное `1h` / `2026-07-17`).
- Эндпоинт: `GET /ocs/v2.php/apps/spreed/api/v1/chat/{token}` с
  `limit` / `lookIntoFuture=0` / `lastKnownMessageId`.
- Вывод построчно: `[id] время автор: текст   👍×2 ✅×1` (реакции счётчиком).

### `chat send <room>`
Отправить сообщение. Источник тела: **stdin по умолчанию** (`-`), либо
`--file <path>`. Флаги: `--reply-to <id>`, `--silent`.
Эндпоинт: `POST /ocs/v2.php/apps/spreed/api/v1/chat/{token}`.
Вывод: id отправленного сообщения.

### `reactions get <room> <messageId>`
Кто поставил реакции (детально). Эндпоинт:
`GET /ocs/v2.php/apps/spreed/api/v1/reaction/{token}/{messageId}`.
Вывод: `реакция → [авторы]`.

### `search <term>`
Глобальный поиск сообщений по содержимому через Unified Search. Эндпоинт:
`GET /ocs/v2.php/search/providers/talk-message/search?term=…&person=…&limit=…`
(поддерживает пагинацию через `cursor`). Флаги: `--from <actorId>` (фильтр по
автору, серверный), `--limit <N>`, `--json`.
Вывод: `время | автор | chat#messageId | текст`. Закрывает кейс №2.

## 7. Идентификация ресурсов и exit-коды

**Правило разрешения `<room>`** (для `chat show` / `chat send` / `reactions get`):
- позиционный аргумент = **token** (primary): `nctalk chat show kytxaiyc`;
- `--name "<имя>"` — fuzzy-поиск по имени через `/v4/room`;
- **1 совпадение** → выполняем; **>1** → не угадываем, выводим кандидатов
  `ТИП | имя | token`, exit `3`; **0** → exit `2`.

Почему token primary: он канонический, однозначный и стабильный; имя могут
переименовать. Конвейер агента: `rooms find/search` → взял token → далее
работает с canonical id.

**Exit-коды:** `0` ок · `1` общая ошибка (сеть/авторизация) · `2` not found ·
`3` неоднозначно.

## 8. Поведение фильтров и ограничения

- `chat show --from` — Talk chat-API **не умеет** серверный фильтр по автору,
  поэтому фильтруем на клиенте: тянем с запасом (`--last` или страницами) и
  режем локально. Для глобального поиска предпочтительнее `search` (серверный
  фильтр `person`, точнее).
- `search --from` принимает **actorId (userId)** типа `daiana.zhukotskaia`, а не
  displayName. Резолв «имя → actorId» — вне MVP (будет `users find` поверх
  `/ocs/v2.php/core/autocomplete/get`); временно агент добывает actorId сам
  (напр. из `rooms find --user` / `chat show`).
- Минимальная длина `term` для Unified Search — есть нижний порог (пустой term →
  `400 Bad Request`), клиент должен валидировать и давать понятную ошибку.

## 9. Обработка ошибок

- Сеть/HTTP → exit `1` + понятное сообщение (`timeout` / `401 bad credentials` /
  `404 not found`).
- OCS-error-конверт (`ocs.meta.statusCode` + `message`) → извлекаем и показываем.
- not found / ambiguous → exit `2` / `3`.
- Пароль **редьюсится** из любых диагностических сообщений.

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
