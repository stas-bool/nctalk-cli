# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

`nctalk` — тонкий CLI-клиент над Nextcloud Talk (Spreed) поверх OCS-API на Go 1.21 (только stdlib). Потребитель — агент (через skill-обёртку) и человек в терминале. Бизнес-логику клиент не содержит — только примитивы (9 команд). Эталон контракта команд, exit-кодов и поведения — **спека** `docs/superpowers/specs/2026-07-17-nctalk-cli-design.md` (базовые 7 команд) и дельты `docs/superpowers/specs/2026-09-08-nctalk-chat-edit-design.md` (`chat edit`) и `docs/superpowers/specs/2026-09-09-nctalk-rooms-participants-design.md` (`rooms participants`; читай при любой правке поведения).

> 📞 **Аудио-звонки WebRTC** (`nctalk-call`/`nctalk-talk`) — эксперимент (spike-first), уже в `main`. `nctalk-call` прошёл spike-gate (двусторонний звонок на Docker Talk 20.1.11); `nctalk-talk` (TUI) реализован (Этап 4), автотесты зелёные. См. секцию «Аудио-звонки WebRTC» ниже и спеки `docs/superpowers/specs/2026-07-19-nctalk-call-design.md` (pipe/агент) и `2026-07-21-nctalk-talk-tui-design.md` (TUI).

## Сборка, тесты, запуск (CGO_ENABLED=0 обязательно)

На этой машине (macOS Tahoe 26.5 + Go 1.21.4) обычный `go test`/`go build` падает с `dyld: missing LC_UUID` — **всегда ставь `CGO_ENABLED=0`**.

```sh
CGO_ENABLED=0 go build ./...                       # сборка
CGO_ENABLED=0 go build -o nctalk ./cmd/nctalk       # бинарник
CGO_ENABLED=0 go vet ./...                         # линт
CGO_ENABLED=0 go test ./...                        # все unit-тесты (не integration)
CGO_ENABLED=0 go test ./internal/client -run TestName -v   # один тест
```

**Makefile**: `make build-signed` собирает `nctalk`/`nctalk-call`/`nctalk-talk` под `CGO_ENABLED=0` и подписывает stable-identity `nctalk-dev` (one-time `make setup-codesign`, см. секцию звонков). Для базового `nctalk` достаточно команд выше.

Запуск:
```sh
export NEXTCLOUD_URL=https://nc.example.org NEXTCLOUD_LOGIN=... NEXTCLOUD_PASS=...  # PASS — app-password, только env
./nctalk rooms list
```

Интеграционные тесты на боевом сервере — за build-тегом `integration` (не входят в `go test ./...`), требуют env-кредов; мутации (`SendMessage`/`EditMessage`) — под флагом `NCTALK_INTEGRATION_SEND=1` + `NCTALK_INTEGRATION_ROOM`. Подробно: `docs/integration-run.md`.

## Архитектура

Четыре слоя, строго однонаправленные: **config → client → cli → render**. main.go только связывает их через тестируемую `run(args, stdout, stderr, stdin) int` (вся логика вне `os.Exit`, чтобы тестировать вывод в `*bytes.Buffer`).

- **`internal/config`** — читает env (`NEXTCLOUD_URL/LOGIN/PASS/TIMEOUT`), нормализует URL (трим trailing slash, схлопывание `//`, запрет userinfo в URL, схема http/https). Сообщения об ошибках содержат **только имена env-переменных, никогда значения**.
- **`internal/client`** — ядро `TalkClient`. Ничего не знает про CLI/вывод. Методы над OCS: `ListRooms/FindRooms/SearchRooms/GetChat/SendMessage/EditMessage/GetReactions/SearchMessages`. Зависит от интерфейса `httpDoer` → мокируется в тестах. Единый транспорт `doOCS(ctx, method, path, query url.Values, body, mutate, out) (http.Header, error)` собирает URL, Basic-auth + заголовки `OCS-APIRequest: true`/`Accept: application/json`, разворачивает конверт `ocs.data`, возвращает заголовки ответа (нужно для пагинации).
- **`internal/cli`** — роутер `<resource> <verb>` (таблица `routes` в `cli.go`); `search` — special-case без verb-уровня. Глобальный `--json` извлекается до роутинга и передаётся хендлерам как `jsonOut`. `Deps{Client, Stdout, Stderr, Stdin, Now}` — потоки/время инъектируются (тесты кладут `bytes.Buffer`). Контракт `TalkClient`-интерфейса — compile-time проверка `var _ TalkClient = (*client.TalkClient)(nil)` (рассинхрон методов роняет сборку).
- **`internal/render`** — текстовые таблицы и `--json`; `SubstituteParams` (подстановка плейсхолдеров), `ParseSince`/`ParseRelativeAt` (относительное время `1h`/`2d`), `FormatTime`.

## Неочевидные инварианты (нарушать опасно — проверяй тестами)

- **`messageParameters` имеет РАЗНЫЙ формат по эндпоинтам**: chat-API (`/api/v1/chat`) — всегда **объект** `{actor,file,mention-*}`; `/v4/room` `lastMessage` для `comment` — **массив** `[]`, для `system` — объект. Тип `client.MsgParams` с `UnmarshalJSON` принимает оба. **Фикстуры `testdata/` обязаны отражать реальный формат каждого эндпоинта** — самопальные объектные фикстуры однажды скрыли баг, который вскрылся только e2e на боевом.
- **Redact кредов**: пароль только в заголовке `Authorization` (никогда в URL — config отвергает userinfo); все ошибки проходят через `sanitizeErr` (чистит URL userinfo/query); `CheckRedirect` блокирует cross-host и **https→http даунгрейд**. Регресс-тест `TestRun_PasswordDoesNotLeak_CrossHostRedirect` (с `NEXTCLOUD_PASS=SECRET_MARKER`) — должен оставаться зелёным.
- **Exit-коды** (`internal/cli/exit.go`, спека §7): `0` успех (в т.ч. пустой результат поиска) · `1` общая ошибка (сеть/**401**/5xx) · `2` not found · `3` ambiguous. Маппинг: `exitFromClientErr` → `*client.OCSError{Code:404}` даёт `2`, остальное `1`. `ResolveRoom` (`room.go`) → `3` (>1 совпадение по `--name`) или `2` (0 совпадений). Константа для кода 1 — `ExitGeneric` (не `ExitError`: то имя занято типом).
- **Пагинация `GetChat`**: курсор следующей страницы — из **заголовка ответа `X-Chat-Last-Given`** (не из тела, не зависит от порядка new→old); стоп при пустом/неизменившемся заголовке; `304`/`204` = пусто/конец истории (трактовать как пустую страницу, без decode-ошибки). Серверу шлём `setReadMarker=0` (чтение не сбрасывает unread) и `limit=min(opts.Limit, 200)`.
- **Фильтры `chat show`**: `--from`/`--since` — клиентские (серверного фильтра по автору/времени в chat-API нет). `--last=0` означает «не задан» → потолок cap 200 при наличии `--from`/`--since`, иначе дефолт 20. System-сообщения скрыты по умолчанию (`--system` показывает).
- **`*client.OCSError{Code}`**: `Code` = OCS `meta.statusCode`; HTTP 404 (включая серверный `998` «Invalid query») нормализуется в `Code=404`.
- **`chat edit` / `EditMessage`** (PUT `/chat/{token}/{messageId}`, спека 2026-09-08): ответ сервера — **системное сообщение, где обновлённое лежит в `parent`**; id для вывода берём из `ocs.data.parent.id` (НЕ эхо аргумента) и guard `parent.id <= 0` обязателен — иначе format-drift даёт молчаливый `0` с exit 0 (регресс-тест `TestEditMessage_ResponseWithoutParent_FormatDrift`). Чтение тела (общий `readMessageBody`: stdin/`--file`, трим одного `\n`) — ДО `ResolveRoom`, пустое тело отсекается без сети. Распределение позиционных `<room> <messageId>` — как у `reactions get` (1 позиционный при `--name` = `messageId`). Ограничения правки (24 ч, свои/модератор, comment-only) — серверные, клиент не дублирует. Мутация проверена на живом API 2026-09-09 (`TestIntegration_EditMessage` PASS).

## Аудио-звонки WebRTC (эксперимент, уже в `main`)

Реальное аудио в звонках Talk (слушать + говорить) из терминала/процесса, как **изолированный модуль** к базовому `nctalk`. В `main` (влито из `feat/nctalk-calls`); spike-first. Спеки: `docs/superpowers/specs/2026-07-19-nctalk-call-design.md` (pipe/агент), `2026-07-21-nctalk-talk-tui-design.md` (TUI).

**Стек:** `github.com/pion/webrtc/v4` (pure-Go WebRTC), macOS, `CGO_ENABLED=0` сохранён глобально — codec и audio-IO через `ffmpeg`-subprocess (не CGO).

**Структура:**
- **Рефакторинг фундамента** (общий для nctalk и звонков): `internal/transport` (HTTP/OCS-примитивы `DoOCS`/`OCSError`/`OCSEnvelope`/Basic-auth+OCS-заголовки/redirect-политика вынесены из `internal/client`; client делегирует через alias для backcompat), `internal/room` (`ResolveRoom` без зависимости от `render`), `internal/exit` (exit-контракт 0/1/2/3 + value-тип `ExitError` + `FromClientErr`).
- **WebRTC:** `internal/call/signaling` (OCS-polling signaling `/api/v3/signaling/{token}` + retry/backoff), `call/capability` (STUN/TURN из signaling-settings), `call/peer` (pion `PeerConnection` на участника, ICE-буферизация, perfect-negotiation/glare через пересоздание PC), `call/media` (+`media/ogg` — OGG-парсер OpusHead/comment, `FFmpegEncoder`/`FFmpegDecoder`, `Mixer` N→1 PCM mixdown), `call/agent` (pipe-режим `Run`), `call/weblogin` (web-login → PHP-session для signaling pull), `call/interactive` (TUI: device-IO avfoundation/audiotoolbox через ffmpeg, mute/volume wrappers, View raw-mode+alt-screen, OnState-обогащение). Точки входа: `cmd/nctalk-call` (pipe/агент), `cmd/nctalk-talk` (TUI), `cmd/spike-check` (Goertzel 440 Гц — объективный spike-gate).

**Инварианты звонков (нарушать опасно — проверяй тестами):**
- **Изоляция от pion:** `cmd/nctalk` и фундамент НЕ зависят от WebRTC. Статический guard `TestCmdNctalkDoesNotDependOnPion` (парсит `go list -deps`). Граница удаления: `rm -rf internal/call cmd/nctalk-call cmd/nctalk-talk && go mod tidy` (пакеты `transport`/`room`/`exit` — НЕ входят, это рефакторинг фундамента).
- **`exit.ExitError` — VALUE-тип** (не указатель): `errors.As(err, &ee)` с `var ee exit.ExitError` (value-target), НЕ `*exit.ExitError`. Был баг — consumers ждали указатель, `errors.As` не матчило, 404→exit 2 ломался (всегда fallback на 1).
- **Opus framing:** `pion` не содержит Opus-кодека/фрейминга; `ffmpeg -f opus` пишет OGG-контейнер, а pion `WriteSample` ждёт raw Opus-пакет → нужен OGG-парсер (`media/ogg`, заголовки OpusHead+vorbis-comment обязательны). encode/decode — ffmpeg-subprocess по pipe.
- **Mesh fanout:** ОДИН общий `TrackLocalStaticSample` в agent, `AddTrack` в каждую `PeerConnection` — pion сам множит отправку через RTPSender'ы внутри каждой PC. НЕ запускать encodeLoop на каждого peer (round-robin по общему encoder даёт каждому 1/N пакетов).
- **Signaling API contract (Spreed 20+, найдено на spike-gate 2026-07-20, реализовано в `call/signaling` + `call/weblogin`):** signaling = **v3** (`/api/v3/signaling/{token}` pull GET / send POST; `/api/v3/signaling/settings` **БЕЗ token**), call = **v4** (`/api/v4/call/{token}`), joinRoom = `POST /api/v4/room/{token}/participants/active`. Canonical flow: **weblogin → joinRoom → pull v3 → joinCall v4**. `pullMessages` требует PHP `$_SESSION` (Basic-auth stateless, её не даёт) → обязателен web-login flow (`internal/call/weblogin`: логин через loginClient с no-redirect, session-cookie в **shared cookiejar**, его подхватывают capability/signaling через httpClient; без session pull → 404). Источник: `custom_apps/spreed/appinfo/routes/`. Проверять на Docker-сервере, НЕ compile-only (compile-only ранее скрыл 5 фундаментальных несоответствий).

**Статус:** `nctalk-call` (pipe/агент) — production-hardened, **spike-gate PASS** обоих направлений на Docker Talk 20.1.11 (2026-07-21: говорить + слушать). Этапы 0–3 завершены, unit + integration тесты зелёные. `nctalk-talk` (TUI, Этап 4) — реализован (`internal/call/interactive`), code-review пройден, статический isolation-check (`TestCmdNctalkDoesNotDependOnPion`) PASS; spike-gate на боевом — pending. Ветка `feat/nctalk-calls` влита в `main` (merge `93e98cf`). Канон device-out = PCM `s16le` (`-f s16le -ar 48000 -ac 1 -i - -f audiotoolbox`), НЕ Opus (уточнено в спеке TUI §3). Полный контекст: `docs/session-state/2026-07-21-nctalk-calls.md`. Любая правка поведения звонков — через `/spec-to-code` с **обязательной** проверкой на Docker-сервере (не compile-only).

**Сборка/запуск звонков:**
```sh
make setup-codesign                        # ОДИН раз: создать codesign-identity nctalk-dev (one-time GUI «Always Allow» доступа к ключу)
make build-signed                          # собрать + подписать nctalk/nctalk-call/nctalk-talk (CGO=0, non-interactive после setup)
NCTALK_INTEGRATION_CALL=1 NCTALK_INTEGRATION_ROOM=<token> ./nctalk-call <room>   # spike-gate — реальный звонок
```
Подпись нужна, чтобы macOS Application Firewall/TCC не спрашивали при каждой пересборке (stable designated requirement по cert-subject, не меняется между сборками).

## Документы

`docs/superpowers/` — `specs/` (эталон контракта: `2026-07-17-nctalk-cli-design.md` — базовый CLI; `2026-09-08-nctalk-chat-edit-design.md` — `chat edit`; `2026-09-09-nctalk-rooms-participants-design.md` — `rooms participants`; `2026-07-19-nctalk-call-design.md` — звонки pipe/агент; `2026-07-21-nctalk-talk-tui-design.md` — звонки TUI), `plans/` (декомпозиция реализации), `reviews/` (code-review). `docs/integration-run.md` — запуск интеграционных тестов. Актуальный state звонков — `docs/session-state/2026-07-21-nctalk-calls.md` (вне git).
