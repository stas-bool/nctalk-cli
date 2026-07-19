# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

`nctalk` — тонкий CLI-клиент над Nextcloud Talk (Spreed) поверх OCS-API на Go 1.21 (только stdlib). Потребитель — агент (через skill-обёртку) и человек в терминале. Бизнес-логику клиент не содержит — только примитивы (7 команд). Эталон контракта команд, exit-кодов и поведения — **спека** `docs/superpowers/specs/2026-07-17-nctalk-cli-design.md` (читай при любой правке поведения).

## Сборка, тесты, запуск (CGO_ENABLED=0 обязательно)

На этой машине (macOS Tahoe 26.5 + Go 1.21.4) обычный `go test`/`go build` падает с `dyld: missing LC_UUID` — **всегда ставь `CGO_ENABLED=0`**.

```sh
CGO_ENABLED=0 go build ./...                       # сборка
CGO_ENABLED=0 go build -o nctalk ./cmd/nctalk       # бинарник
CGO_ENABLED=0 go vet ./...                         # линт
CGO_ENABLED=0 go test ./...                        # все unit-тесты (не integration)
CGO_ENABLED=0 go test ./internal/client -run TestName -v   # один тест
```

Запуск:
```sh
export NEXTCLOUD_URL=https://nc.example.org NEXTCLOUD_LOGIN=... NEXTCLOUD_PASS=...  # PASS — app-password, только env
./nctalk rooms list
```

Интеграционные тесты на боевом сервере — за build-тегом `integration` (не входят в `go test ./...`), требуют env-кредов; мутация (`SendMessage`) — под флагом `NCTALK_INTEGRATION_SEND=1` + `NCTALK_INTEGRATION_ROOM`. Подробно: `docs/integration-run.md`.

## Архитектура

Четыре слоя, строго однонаправленные: **config → client → cli → render**. main.go только связывает их через тестируемую `run(args, stdout, stderr, stdin) int` (вся логика вне `os.Exit`, чтобы тестировать вывод в `*bytes.Buffer`).

- **`internal/config`** — читает env (`NEXTCLOUD_URL/LOGIN/PASS/TIMEOUT`), нормализует URL (трим trailing slash, схлопывание `//`, запрет userinfo в URL, схема http/https). Сообщения об ошибках содержат **только имена env-переменных, никогда значения**.
- **`internal/client`** — ядро `TalkClient`. Ничего не знает про CLI/вывод. Методы над OCS: `ListRooms/FindRooms/SearchRooms/GetChat/SendMessage/GetReactions/SearchMessages`. Зависит от интерфейса `httpDoer` → мокируется в тестах. Единый транспорт `doOCS(ctx, method, path, query url.Values, body, mutate, out) (http.Header, error)` собирает URL, Basic-auth + заголовки `OCS-APIRequest: true`/`Accept: application/json`, разворачивает конверт `ocs.data`, возвращает заголовки ответа (нужно для пагинации).
- **`internal/cli`** — роутер `<resource> <verb>` (таблица `routes` в `cli.go`); `search` — special-case без verb-уровня. Глобальный `--json` извлекается до роутинга и передаётся хендлерам как `jsonOut`. `Deps{Client, Stdout, Stderr, Stdin, Now}` — потоки/время инъектируются (тесты кладут `bytes.Buffer`). Контракт `TalkClient`-интерфейса — compile-time проверка `var _ TalkClient = (*client.TalkClient)(nil)` (рассинхрон методов роняет сборку).
- **`internal/render`** — текстовые таблицы и `--json`; `SubstituteParams` (подстановка плейсхолдеров), `ParseSince`/`ParseRelativeAt` (относительное время `1h`/`2d`), `FormatTime`.

## Неочевидные инварианты (нарушать опасно — проверяй тестами)

- **`messageParameters` имеет РАЗНЫЙ формат по эндпоинтам**: chat-API (`/api/v1/chat`) — всегда **объект** `{actor,file,mention-*}`; `/v4/room` `lastMessage` для `comment` — **массив** `[]`, для `system` — объект. Тип `client.MsgParams` с `UnmarshalJSON` принимает оба. **Фикстуры `testdata/` обязаны отражать реальный формат каждого эндпоинта** — самопальные объектные фикстуры однажды скрыли баг, который вскрылся только e2e на боевом.
- **Redact кредов**: пароль только в заголовке `Authorization` (никогда в URL — config отвергает userinfo); все ошибки проходят через `sanitizeErr` (чистит URL userinfo/query); `CheckRedirect` блокирует cross-host и **https→http даунгрейд**. Регресс-тест `TestRun_PasswordDoesNotLeak_CrossHostRedirect` (с `NEXTCLOUD_PASS=SECRET_MARKER`) — должен оставаться зелёным.
- **Exit-коды** (`internal/cli/exit.go`, спека §7): `0` успех (в т.ч. пустой результат поиска) · `1` общая ошибка (сеть/**401**/5xx) · `2` not found · `3` ambiguous. Маппинг: `exitFromClientErr` → `*client.OCSError{Code:404}` даёт `2`, остальное `1`. `ResolveRoom` (`room.go`) → `3` (>1 совпадение по `--name`) или `2` (0 совпадений). Константа для кода 1 — `ExitGeneric` (не `ExitError`: то имя занято типом).
- **Пагинация `GetChat`**: курсор следующей страницы — из **заголовка ответа `X-Chat-Last-Given`** (не из тела, не зависит от порядка new→old); стоп при пустом/неизменившемся заголовке; `304`/`204` = пусто/конец истории (трактовать как пустую страницу, без decode-ошибки). Серверу шлём `setReadMarker=0` (чтение не сбрасывает unread) и `limit=min(opts.Limit, 200)`.
- **Фильтры `chat show`**: `--from`/`--since` — клиентские (серверного фильтра по автору/времени в chat-API нет). `--last=0` означает «не задан» → потолок cap 200 при наличии `--from`/`--since`, иначе дефолт 20. System-сообщения скрыты по умолчанию (`--system` показывает).
- **`*client.OCSError{Code}`**: `Code` = OCS `meta.statusCode`; HTTP 404 (включая серверный `998` «Invalid query») нормализуется в `Code=404`.

## Документы

`docs/superpowers/` — `specs/` (эталон контракта), `plans/` (декомпозиция реализации), `reviews/` (code-review). `docs/integration-run.md` — запуск интеграционных тестов.
