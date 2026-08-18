# nctalk

**English summary** — full documentation in Russian below.

`nctalk` is a thin terminal CLI for [Nextcloud Talk](https://github.com/nextcloud/spreed) (Spreed) built on top of the OCS API.

- 7 primitives over rooms, chat, reactions and search — no business logic, composable in scripts
- Machine-friendly: `--json` on every command, predictable exit codes (`0`/`1`/`2`/`3`)
- Credentials only via environment variables (app-password), never in flags or files
- Pure Go 1.21 stdlib, CGO-free single binary
- Experimental: terminal WebRTC audio calls — `nctalk-call` (pipe/agent mode), `nctalk-talk` (TUI)
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

Тонкий CLI-клиент над **Nextcloud Talk** (приложение Spreed) поверх OCS-API. Только примитивы: список/поиск чатов, чтение и отправка сообщений, реакции, поиск. Основной потребитель — агент (через skill-обёртку), вторичный — человек в терминале. Бизнес-логики не содержит.

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

## Аудио-звонки (эксперимент)

Реальное аудио в звонках Talk (слушать + говорить) из терминала/процесса — два отдельных бинарника к базовому `nctalk`. Стек: `github.com/pion/webrtc/v4` + `ffmpeg` (codec и audio-IO, без CGO), macOS. `cmd/nctalk` изолирован от WebRTC (не зависит от pion).

> Экспериментально (spike-first). `nctalk-call` прошёл spike-gate — реальный двусторонний звонок на Docker Talk 20.1.11 (говорить + слушать). `nctalk-talk` (TUI) реализован, автотесты зелёные. Спеки — в [`docs/superpowers/specs/`](docs/superpowers/specs/) (`2026-07-19-nctalk-call-design.md`, `2026-07-21-nctalk-talk-tui-design.md`).

Сборка (подпись убирает вопросы фаервола/TCC при пересборке):

```sh
make setup-codesign        # one-time: codesign-identity nctalk-dev
make build-signed          # собрать + подписать nctalk/nctalk-call/nctalk-talk (CGO=0)
```

### `nctalk-call <room>` — pipe/агент (PCM через stdin/stdout)

Отправляет и принимает аудио как raw PCM `s16le`/48 кГц/моно через stdin/stdout — для скрипта, бота, записи.

```sh
nctalk-call kytxaiyc                            # stdin → звонок; звонок → stdout (sendrecv)
nctalk-call kytxaiyc --name "review team"       # разрешить комнату по имени
nctalk-call kytxaiyc --in voice.pcm             # отправить свой PCM из файла (вместо stdin)
nctalk-call kytxaiyc --out rec.pcm              # записать входящее аудио в файл (вместо stdout)
nctalk-call kytxaiyc --recvonly --out rec.pcm   # только приём (своё не отправлять)
```

| Флаг        | Назначение                                                        |
| ----------- | ------------------------------------------------------------------ |
| `<room>`    | token позиционно; либо `--name` для поиска по имени.               |
| `--name`    | case-insensitive подстрока в `displayName` (как у `chat show`).    |
| `--in`      | PCM для отправки: путь файла или `-` (stdin, по умолчанию).        |
| `--out`     | куда писать входящий PCM: путь файла или `-` (stdout, по умолчанию).|
| `--recvonly`| только приём чужого аудио (своё не отправлять).                    |
| `--debug`   | подробные signaling-логи.                                          |

Env (помимо `NEXTCLOUD_*`): `NCTALK_ICE_TIMEOUT` (бюджет на установку первого peer-соединения, по умолчанию `30s`), `NCTALK_DEBUG`.

`--out` пишет **raw PCM `s16le`/48 кГц/моно**, а не контейнер. Конверт в WAV:

```sh
ffmpeg -f s16le -ar 48000 -ac 1 -i rec.pcm rec.wav
```

Разрешение `<room>`/`--name` и exit-коды — те же, что у `nctalk`.

### `nctalk-talk <room>` — интерактивный TUI (человек)

Микрофон и динамик — через `ffmpeg` (macOS: avfoundation для входа, audiotoolbox для выхода). Экранное управление: список участников, mute, громкость, индикатор «говорит».

```sh
nctalk-talk kytxaiyc                            # зайти в звонок; микрофон/динамик по умолчанию
nctalk-talk --name "review team"                # разрешить комнату по имени
```

Управление:

- `M` — mute своего микрофона (своё аудио перестаёт уходить в сеть; флаг звонка не меняется).
- `+` / `−` — громкость локального динамика ±10 % (диапазон 0–200 %, по умолчанию 100 %).
- `Q` или `Ctrl-C` — выйти из звонка (штатный leave, exit `0`).
- Кто говорит — подсвечивается по уровню (VAD).

Env (помимо `NEXTCLOUD_*`):

| Переменная                | Назначение                                                                           |
| ------------------------- | -------------------------------------------------------------------------------------- |
| `NCTALK_AUDIO_DEVICE_IN`  | Микрофон, avfoundation-строка `":<idx>"` (по умолчанию `":0"` — первый audio-input).   |
| `NCTALK_AUDIO_DEVICE_OUT` | Динамик: int-индекс audiotoolbox либо `default`/`-1` (по умолчанию `default`).        |
| `NCTALK_ICE_TIMEOUT`      | Бюджет на установку peer-соединения (по умолчанию `30s`).                              |
| `NCTALK_CALL_LOG`         | Путь лога (по умолчанию `./call.log`). Все технические строки — туда.                  |
| `NCTALK_DEBUG`            | Подробные signaling-логи.                                                              |

Листинг устройств (input и output — разные muxers `ffmpeg`):

```sh
ffmpeg -f avfoundation -list_devices true -i ""   # input  → NCTALK_AUDIO_DEVICE_IN  (":<idx>")
ffmpeg -f audiotoolbox -list_devices true -i ""   # output → NCTALK_AUDIO_DEVICE_OUT (индекс из списка)
```

Exit-коды — те же (`0`/`1`/`2`/`3`). В не-терминале (pipe) TUI уходит в fallback-режим без raw-mode/alt-screen и пишет статус построчно в stdout — это позволяет гонять его в интеграционных тестах.

## Лицензия

MIT — см. [`LICENSE`](LICENSE).
