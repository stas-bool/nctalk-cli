# nctalk

**English summary** — full documentation in Russian below.

`nctalk` is a thin terminal CLI for [Nextcloud Talk](https://github.com/nextcloud/spreed) (Spreed) built on top of the OCS API.

- 7 primitives over rooms, chat, reactions and search — no business logic, composable in scripts
- Machine-friendly: `--json` on every command, predictable exit codes (`0`/`1`/`2`/`3`)
- Credentials only via environment variables (app-password), never in flags or files
- Pure Go 1.21 stdlib, CGO-free single binary
- Experimental: terminal WebRTC audio calls (`nctalk-call` pipe-mode, `nctalk-talk` TUI)

```sh
go install github.com/stas-bool/nctalk-cli/cmd/nctalk@latest
export NEXTCLOUD_URL=https://nc.example.org NEXTCLOUD_LOGIN=me NEXTCLOUD_PASS=...
nctalk rooms list
```

[Полная документация — ниже.](#nctalk-ru)

---

<a name="nctalk-ru"></a>
# nctalk (рус.)

`nctalk` — тонкий CLI-клиент над Nextcloud Talk (Spreed) поверх OCS-API. Потребители — скрипты/агенты (в т.ч. через обёртки-скиллы) и человек в терминале. Клиент не содержит бизнес-логики — только примитивы, из которых собирается нужное поведение.

## Возможности

- **7 команд** над комнатами, чатом, реакциями и поиском (см. [Команды](#команды))
- **`--json`** в любой команде и в любой позиции — для скриптов и агентов
- **Предсказуемые exit-коды** — 0/1/2/3, удобно в shell-условиях и CI
- **Креды только через env** (app-password): никогда через флаги, файлы или URL
- **Go 1.21, только stdlib** для базового CLI — один CGO-free бинарник без зависимостей
- **Эксперимент:** аудио-звонки WebRTC из терминала (`nctalk-call`, `nctalk-talk`)

## Требования

- Go 1.21+
- Nextcloud с приложением Talk (Spreed)
- Для звонков (эксперимент): macOS, `ffmpeg` в `PATH`

## Установка и сборка

```sh
go install github.com/stas-bool/nctalk-cli/cmd/nctalk@latest   # установка
```

или из исходников:

```sh
git clone https://github.com/stas-bool/nctalk-cli
cd nctalk-cli
CGO_ENABLED=0 go build -o nctalk ./cmd/nctalk
```

Звонки (`nctalk-call`, `nctalk-talk`) собираются через `make build-signed` — см. [Аудио-звонки](#аудио-звонки-webrtc-эксперимент).

## Конфигурация

Всё через переменные окружения:

| Переменная | Назначение |
|---|---|
| `NEXTCLOUD_URL` | базовый URL сервера (`https://nc.example.org`) |
| `NEXTCLOUD_LOGIN` | логин пользователя |
| `NEXTCLOUD_PASS` | **app-password** (только env; создать: Настройки → Безопасность → Пароли приложений) |
| `NEXTCLOUD_TIMEOUT` | таймаут HTTP (по умолчанию `30s`) |

## Команды

```
nctalk <команда> [flags]        # справка: nctalk --help
```

| Команда | Что делает |
|---|---|
| `rooms list` | список комнат |
| `rooms find` | найти комнату по фильтру |
| `rooms search` | поиск комнат на сервере |
| `chat show <room>` | история чата комнаты |
| `chat send <room>` | отправить сообщение (stdin или `--file`) |
| `reactions get <room> <msgId>` | реакции на сообщение |
| `search` | глобальный поиск по сообщениям |

`<room>` — token комнаты позиционно, либо `--name "имя"` для разрешения по имени.

Примеры:

```sh
nctalk rooms list --json                     # комнаты в JSON
nctalk chat show abc123 --last 50            # последние 50 сообщений
nctalk chat show --name "Команда" --since 1h # за последний час
nctalk chat show --name "Команда" --from u-alice   # только сообщения автора
echo "привет" | nctalk chat send --name "Команда"  # отправка через stdin
nctalk chat send abc123 --file msg.txt --silent    # из файла, без уведомления
nctalk reactions get abc123 100              # реакции на сообщение 100
```

### Exit-коды

| Код | Значение |
|---|---|
| `0` | успех (включая пустой результат поиска) |
| `1` | общая ошибка: сеть, 401, 5xx |
| `2` | не найдено |
| `3` | неоднозначное совпадение |

## Аудио-звонки WebRTC (эксперимент)

Реальное аудио в звонках Talk (слушать и говорить) из терминала или процесса:

- **`nctalk-call`** — pipe/агент-режим: звук через stdin/stdout, управление через JSON-протокол. Production-hardened, прошёл spike-gate на двусторонний звонок.
- **`nctalk-talk`** — интерактивный TUI-клиент звонков (mute, громкость, список участников).

Стек: [`pion/webrtc`](https://github.com/pion/webrtc) v4 (pure-Go WebRTC), кодеки и аудио-ввод/вывод через subprocess `ffmpeg` (CGO сохранён выключенным). Сборка на macOS:

```sh
make setup-codesign   # один раз: codesign-identity для firewall/TCC
make build-signed
```

Дизайн-документы: `docs/superpowers/specs/2026-07-19-nctalk-call-design.md` (pipe/агент), `docs/superpowers/specs/2026-07-21-nctalk-talk-tui-design.md` (TUI).

## Разработка

```sh
CGO_ENABLED=0 go test ./...    # unit-тесты
CGO_ENABLED=0 go vet ./...
```

Интеграционные тесты на реальном сервере — за build-тегом `integration`, требуют env-кредов: см. `docs/integration-run.md`.

Контракт CLI (команды, exit-коды, поведение) зафиксирован спецификацией: `docs/superpowers/specs/2026-07-17-nctalk-cli-design.md` — при правках поведения ориентируйтесь на неё.
