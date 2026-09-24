# nctalk

**English summary** — full documentation in Russian below.

`nctalk` is a thin terminal CLI for [Nextcloud Talk](https://github.com/nextcloud/spreed) (Spreed) built on top of the OCS API.

- 8 primitives over rooms, chat, reactions and search — no business logic, composable in scripts
- Machine-friendly: `--json` on every command, predictable exit codes (`0`/`1`/`2`/`3`)
- Credentials only via environment variables (app-password), never in flags or files
- Pure Go 1.21 stdlib, CGO-free single binary
- [MIT License](LICENSE)

```sh
go install github.com/stas-bool/nctalk-cli/cmd/nctalk@latest
export NEXTCLOUD_URL=https://nc.example.org NEXTCLOUD_LOGIN=me NEXTCLOUD_PASS=...
nctalk rooms list
```

[Полная документация — ниже.](#nctalk-ru)

---

<a name="nctalk-ru"></a>
# nctalk (рус.)

Тонкий CLI-клиент над **Nextcloud Talk** (приложение Spreed) поверх OCS-API. Только примитивы: список/поиск чатов, участники комнаты, чтение, отправка и редактирование сообщений, реакции, поиск. Основной потребитель — агент (через skill-обёртку), вторичный — человек в терминале. Бизнес-логики не содержит.

Написан на Go 1.21 (только стандартная библиотека).

## Установка

```sh
go install github.com/stas-bool/nctalk-cli/cmd/nctalk@latest   # установка
# или из исходников:
git clone https://github.com/stas-bool/nctalk-cli && cd nctalk-cli
CGO_ENABLED=0 go build -o nctalk ./cmd/nctalk
```

> `CGO_ENABLED=0` обязателен на macOS (иначе сборка/тесты могут падать с `dyld: missing LC_UUID`).

## Конфигурация

Все настройки — через переменные окружения. Креды **никогда** не передаются аргументами и не попадают в вывод/ошибки/логи.

| Переменная           | Обязательно | Назначение                                                                  |
| -------------------- | ----------- | ---------------------------------------------------------------------------- |
| `NEXTCLOUD_URL`      | да          | База сервера, напр. `https://nc.example.org` (без userinfo и trailing `/`).  |
| `NEXTCLOUD_LOGIN`    | да          | Логин пользователя.                                                          |
| `NEXTCLOUD_PASS`     | да          | App-password (НЕ основной пароль аккаунта).                                  |
| `NEXTCLOUD_TIMEOUT`  | нет         | HTTP-таймаут, по умолчанию `30s` (Go duration: `45s`, `1m`, `90s`).          |

```sh
export NEXTCLOUD_URL=https://nc.example.org
export NEXTCLOUD_LOGIN=alice
export NEXTCLOUD_PASS='********'   # app-password
```

Глобальный флаг `--json` доступен во всех командах (в любой позиции) и переводит вывод в машиночитаемый формат (для агента).

## Команды

Общий вид: `nctalk <resource> <verb> ...` (кроме `search`). Справка: `nctalk --help`, по команде — `nctalk <команда> --help`.

### `rooms list` — список чатов

```sh
nctalk rooms list                       # все комнаты
nctalk rooms list --type 1              # только личные (1=личные, 2=группы, 3=публичные)
nctalk rooms list --unread              # только с непрочитанными
nctalk rooms list --include-former      # включить «former» (типы 4/5/6), по умолчанию скрыты
```

### `rooms find <запрос>` — поиск по имени (client-side)

```sh
nctalk rooms find дайна                 # подстрока в displayName
nctalk rooms find --user daiana.zhukotskaia   # личный чат 1-на-1 с пользователем (по actorId)
```

Возвращает **все** совпадения (exit `0`, даже если пусто). Главное в выводе — `token`.

### `rooms search <term>` — server-side fuzzy-поиск чатов

```sh
nctalk rooms search review --limit 5
```

Через Unified Search (`talk-conversations`). Минимальная длина `term` — 1 символ.

### `rooms participants <room>` — участники комнаты

```sh
nctalk rooms participants kytxaiyc             # <room> = token позиционно
nctalk rooms participants --name "review team" # ...или по имени
nctalk rooms participants kytxaiyc --json      # каноническая модель (actorId и др.)
```

Колонки: `ИМЯ` (у гостя без имени — `actorId`), `РОЛЬ` (владелец/модератор/участник/гость/по ссылке/гость-модератор), `ОНЛАЙН` (есть живая сессия), `ID` (`actorId` — для упоминаний и `--from`). Сортировка: роль → имя. `--json` — массив объектов `actorType, actorId, displayName, participantType, sessionIds, inCall, lastPing`. Пусто → exit `0`.

### `chat show <room>` — читать сообщения

```sh
nctalk chat show kytxaiyc               # <room> = token позиционно
nctalk chat show --name "review team"   # ...или fuzzy по имени
nctalk chat show kytxaiyc --last 50     # последние N (по умолчанию 20)
nctalk chat show kytxaiyc --from daiana.zhukotskaia   # фильтр по автору (actorId)
nctalk chat show kytxaiyc --since 2h    # сообщения за последние 2 часа
nctalk chat show kytxaiyc --since 2026-07-17
nctalk chat show kytxaiyc --system      # показать системные сообщения (по умолчанию скрыты)
```

`<room>` разрешается так: позиционный аргумент = **token** (primary); `--name` = case-insensitive подстрока в `displayName`. **1 совпадение** → выполняем; **>1** → кандидаты + exit `3`; **0** → exit `2`.

Форматы `--since`: относительные `30s`/`15m`/`2h`/`1d`, дата `2026-07-17` (начало дня), полный ISO `2026-07-17T13:00:00[+03:00]`. Без явной зоны — локальное время процесса.

`--from`/`--since` — клиентские фильтры (в chat-API нет серверного фильтра по автору/времени). Если они заданы без `--last`, выборка идёт до cap 200 сообщений.

### `chat send <room>` — отправить сообщение

```sh
echo "Привет" | nctalk chat send kytxaiyc                       # тело из stdin (по умолчанию)
nctalk chat send kytxaiyc --file msg.txt                        # тело из файла
echo "ответ" | nctalk chat send kytxaiyc --reply-to 2927        # ответ на сообщение
echo "тихо"  | nctalk chat send kytxaiyc --silent               # без уведомления
```

Вывод: `id` отправленного сообщения.

### `chat edit <room> <messageId>` — отредактировать сообщение

```sh
echo "исправлено" | nctalk chat edit kytxaiyc 2927      # новый текст из stdin (по умолчанию)
nctalk chat edit kytxaiyc 2927 --file new.txt          # новый текст из файла
nctalk chat edit --name "Команда" 2927 --file new.txt  # комнату по имени
```

Вывод: `id` отредактированного сообщения. Правки ограничивает сервер (ошибки 400/403/404/405 показывает сервер): свои сообщения — в течение 24 часов после отправки, чужие — только модератору, тип — comment.

### `reactions get <room> <messageId>` — кто поставил реакции

```sh
nctalk reactions get kytxaiyc 2927
```

Вывод: `реакция → [авторы]`. Если реакций нет — пусто, exit `0`.

### `search <term>` — глобальный поиск сообщений (server-side)

```sh
nctalk search "release notes"
nctalk search deploy --from daiana.zhukotskaia --limit 25
nctalk search deploy --all              # собрать несколько страниц (до 5)
```

Через Unified Search (`talk-message`). `--from` — серверный фильтр по автору (actorId), точнее клиентского `chat show --from`.

## Exit-коды

| Код | Значение                                                                     |
| --- | ------------------------------------------------------------------------------ |
| `0` | успех (в т.ч. пустой результат поиска `rooms find`/`rooms search`/`search`)    |
| `1` | общая ошибка (сеть, авторизация 401, таймаут)                                 |
| `2` | not found — комната/сообщение не найдены, либо 0 совпадений по `--name`        |
| `3` | неоднозначно — >1 совпадение по `--name`                                      |

Для агентского конвейера: `rooms find`/`search` → взять `token` → далее работать с каноническим id.

## Идентификация пользователей

Все флаги «человека» (`--from`, `--user`) принимают **actorId** (userId, напр. `daiana.zhukotskaia`), а не displayName. Узнать actorId: из `rooms list`/`rooms find` (колонка/`--json`) или `chat show` (`--json`).

## Тесты

```sh
CGO_ENABLED=0 go test ./...              # unit-тесты (httptest + table-driven)
CGO_ENABLED=0 go vet ./...
```

Интеграционные тесты на боевом сервере — за build-тегом `integration`, не входят в обычный прогон; требуют env-кредов; отправка сообщения — под флагом. См. [`docs/integration-run.md`](docs/integration-run.md).

## Архитектура

Четыре слоя: `config` (env) → `client` (ядро над OCS-API) → `cli` (роутинг, exit-коды) → `render` (вывод). Только стандартная библиотека. Подробности — в [`CLAUDE.md`](CLAUDE.md) и спеке `docs/superpowers/specs/`.

- `cmd/nctalk/main.go` — точка входа, связка слоёв.
- `internal/config` — чтение/валидация env.
- `internal/client` — `TalkClient` (HTTP, Basic Auth, OCS-конверт, redact).
- `internal/cli` — разбор флагов, роутинг, правило разрешения `<room>`, exit-коды.
- `internal/render` — текстовые таблицы и JSON.
- `testdata/` — обезличенные JSON-фикстуры ответов Talk.

## Лицензия

MIT — см. [`LICENSE`](LICENSE).
