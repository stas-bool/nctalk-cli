# Интеграционные тесты nctalk (`-tags=integration`)

Интеграционные тесты идут в отдельном файле `internal/client/integration_test.go`
за build-тегом `integration` и **НЕ входят** в обычный прогон `go test ./...`
(так задумано — их запускает человек на боевом сервере с реальными кредами).

Все сценарии читающие и безопасные, кроме мутационных `TestIntegration_SendMessage`
(отправка) и `TestIntegration_EditMessage` (правка) — они меняют состояние чата
и защищены отдельным флагом.

## Обязательные окружение

| Переменная           | Назначение                                          |
| -------------------- | --------------------------------------------------- |
| `NEXTCLOUD_URL`      | База сервера Nextcloud, напр. `https://nc.example.org` (схема и host обязательны, без userinfo и trailing slash). |
| `NEXTCLOUD_LOGIN`    | Логин пользователя Nextcloud.                       |
| `NEXTCLOUD_PASS`     | App-password пользователя (НЕ основной пароль аккаунта). |

При отсутствии любого из них тесты **скипаются** через `t.Skip` (не падают) —
это нормально для машины без кредов.

## Опциональное окружение

| Переменная             | По умолчанию | Назначение                                                      |
| ---------------------- | ------------ | -------------------------------------------------------------- |
| `NEXTCLOUD_TIMEOUT`    | `30s`        | HTTP-таймаут клиента (Go duration: `45s`, `1m`, `90s`, и т.д.). |
| `NCTALK_INTEGRATION_SEND`  | (ноль)   | `1` — включить мутационные `TestIntegration_SendMessage`/`TestIntegration_EditMessage`. |
| `NCTALK_INTEGRATION_ROOM`  | (пусто)  | Token тестовой комнаты для мутационных `SendMessage`/`EditMessage`. |

> Креды **никогда** не попадают в вывод/ошибки/логи тестов — санитайз на
> стороне `internal/client` (спека §5, §9). Не подсуньте их в `git`-конфиг
> или скрипты с `set -x`.

## Запуск

Все команды — с `CGO_ENABLED=0` (macOS Tahoe 26.5 + Go 1.21.4: dyld-проблема
CGO). Тег `-tags=integration` обязателен — без него файл просто не
компилируется.

### Все читающие сценарии

```sh
CGO_ENABLED=0 go test -tags=integration ./internal/client/... -v \
  -run 'TestIntegration_(ListRooms|SearchRooms|GetChat|SearchMessages|GetReactions|GetParticipants)'
```

### По одному сценарию

```sh
CGO_ENABLED=0 go test -tags=integration ./internal/client/... -v -run TestIntegration_ListRooms
CGO_ENABLED=0 go test -tags=integration ./internal/client/... -v -run TestIntegration_SearchRooms
CGO_ENABLED=0 go test -tags=integration ./internal/client/... -v -run TestIntegration_GetChat
CGO_ENABLED=0 go test -tags=integration ./internal/client/... -v -run TestIntegration_SearchMessages
CGO_ENABLED=0 go test -tags=integration ./internal/client/... -v -run TestIntegration_GetReactions
CGO_ENABLED=0 go test -tags=integration ./internal/client/... -v -run TestIntegration_GetParticipants
```

### Мутация (отправка и правка сообщений)

**Только в тестовую комнату.** Двухслойная защита: флаг + token.

```sh
NCTALK_INTEGRATION_SEND=1 \
NCTALK_INTEGRATION_ROOM='<token-тестовой-комнаты>' \
CGO_ENABLED=0 go test -tags=integration ./internal/client/... -v \
  -run 'TestIntegration_(SendMessage|EditMessage)'
```

Без `NCTALK_INTEGRATION_SEND=1` или без `NCTALK_INTEGRATION_ROOM` тесты
молча скипаются.

### Проверка, что integration не входит в CI-прогон

```sh
CGO_ENABLED=0 go test ./...   # без -tags=integration
```

В выводе не должно быть `TestIntegration_*` — это гарантирует build-тег в
шапке `integration_test.go`.

## Безопасность сценариев

- **Читающие** (`ListRooms`, `SearchRooms`, `GetChat`, `SearchMessages`,
  `GetReactions`, `GetParticipants`) — GET-запросы, состояние сервера не меняют.
  Безопасны для прогона на production-аккаунте.
- **Мутация** (`SendMessage`/`EditMessage`) — POST/PUT в чат; выполняются только
  в токен, заданный в `NCTALK_INTEGRATION_ROOM`, и только при
  `NCTALK_INTEGRATION_SEND=1`. Удалять тестовые сообщения нужно вручную — тесты
  этого не делают (спека §12 „мутация, не проверено“).

## Что проверяет каждый тест

| Тест                        | Эндпоинт                                        | Контракт                                                              |
| --------------------------- | ----------------------------------------------- | --------------------------------------------------------------------- |
| `TestIntegration_ListRooms` | `/ocs/v2.php/apps/spreed/api/v4/room`           | `len >= 1`; для type=1 комнаты проверяется непустой `ActorId`.        |
| `TestIntegration_SearchRooms` | Unified `talk-conversations`                  | Безошибочный ответ; для каждого результата — непустой `Title`.        |
| `TestIntegration_GetChat`   | `/ocs/v2.php/apps/spreed/api/v1/chat/{token}`   | `Limit=5` → `len <= 5`; маппинг `Message.Token` и `Timestamp > 0`.    |
| `TestIntegration_SearchMessages` | Unified `talk-message`                     | Безошибочный ответ; `Attributes.Timestamp` — валидный int64 (`>= 0`). |
| `TestIntegration_GetReactions` | `/ocs/v2.php/apps/spreed/api/v1/reaction/{token}/{id}` | Non-nil map; если реакции есть — у каждой есть актёры.      |
| `TestIntegration_GetParticipants` | `/ocs/v2.php/apps/spreed/api/v4/room/{token}/participants` | `>= 1` участник; непустые `actorId`/`actorType`; есть `participantType=1` (владелец). |
| `TestIntegration_SendMessage` | POST `/chat/{token}`                          | Возвращает `id > 0`. Мутация — под флагом.                            |
| `TestIntegration_EditMessage` | PUT `/chat/{token}/{messageId}`             | `parent.id == id`; `GetChat` видит новый текст. Мутация — под флагом. |

## Signaling integration (Task 2.3)

Отдельный интеграционный тест signaling-протокола P2P-звонков:
`internal/call/signaling/integration_test.go` (build-tag `integration`).

В отличие от читающих сценариев `client/`, этот тест **мутационный**:
`JoinCall` меняет состояние сервера (добавляет нас в список участников
звонка). Поэтому он защищён двухслойной защитой, как `SendMessage` —
флаг `NCTALK_INTEGRATION_CALL=1` + token `NCTALK_INTEGRATION_ROOM`.

Сценарий (спека 2026-07-19 §7):

1. `JoinCall(token, flags=3)` — войти в звонок (sendrecv).
2. `PollLoop` крутится 5 секунд в goroutine'е.
3. Логируется каждое событие, полученное из signaling-канала.
4. `LeaveCall(token)` — отписаться (deferred).

Тест НЕ проверяет полный WebRTC-обмен (нужен браузерный собеседник для
offer/answer/candidate). Цель — подтвердить:

- **Путь signaling-эндпоинта** (v3 или v4) отвечает на боевом.
- **Формат `usersInRoom`** (фикстура `testdata/signaling/usersInRoom.json`)
  совпадает с реальным ответом.
- **Формат `message.data`** — это JSON-строка (не объект); парсер Task 2.2
  делает двойной unmarshal и должен корректно разбирать реальный трафик.
- **Флаги `inCall`** приходят с ожидаемой битмаской (1=IN_CALL, 2=WITH_AUDIO,
  4=WITH_VIDEO).

### Обязательное окружение

Те же базовые переменные, что и для остальных integration-тестов, плюс
две дополнительные для gate'а мутации:

| Переменная                | Назначение                                                       |
| ------------------------- | ---------------------------------------------------------------- |
| `NEXTCLOUD_URL`           | База сервера (см. общие требования выше).                        |
| `NEXTCLOUD_LOGIN`         | Логин пользователя Nextcloud.                                    |
| `NEXTCLOUD_PASS`          | App-password (НЕ основной пароль).                               |
| `NCTALK_INTEGRATION_ROOM` | Token комнаты, в которой пойдёт звонок. Должна существовать и быть доступной пользователю. |
| `NCTALK_INTEGRATION_CALL` | `1` — явный opt-in для мутационного signaling-теста.             |

При отсутствии любого из них тест **скипается** через `t.Skip` (не падает).

> Комнату для теста лучше создать заранее — например, групповой звонок
> `test-call` с одним участником (тестовым пользователем). Запускать
> предпочтительно в комнате **без других активных участников**, чтобы
> не мешать реальным звонкам. Если в комнате есть другой участник с
> браузером — можно заодно проверить полный SDP/ICE-обмен, но это не
> обязательно для базового smoke-теста.

### Запуск

```sh
NCTALK_INTEGRATION_ROOM='<token-комнаты-для-звонка>' \
NCTALK_INTEGRATION_CALL=1 \
CGO_ENABLED=0 go test -tags=integration ./internal/call/signaling/... \
  -run TestSignalingPollLoop -v
```

Команда завершится за ~5 секунд (столько живёт poll-контекст в тесте)
плюс сетевые задержки на `JoinCall`/`LeaveCall` (по ~30с потолок).

### Что смотреть в выводе

Тест логирует каждый наблюдённый `Event`:

```
Event[0]: Kind=EvUsersUpdated
  usersInRoom: 1 participant(s)
  user[0]: sessionId=... actorType=users actorId=... inCall=3
```

Если первый Event — `EvError`, тест упадёт на assert'е протокола; в логе
будет `ExitError.Code`:

- `code=1` — общая ошибка / 401 / 403 (креды или права).
- `code=2` — 404 (комната или signaling-эндпоинт не найден; см. ниже
  «Открытые вопросы»).

### Открытые вопросы для разрешения при первом прогоне

Эти вопросы выявлены при разработке (Task 2.1, 2.2), но не могут быть
закрыты без прогона на реальном сервере. Финальное решение принимает
человек по результатам этого теста.

1. **Версия эндпоинта signaling'а: `v3` или `v4`.**

   Спека `docs/superpowers/specs/2026-07-19-nctalk-call-design.md` §7
   требует `GET /ocs/v2.php/apps/spreed/api/v4/signaling/{token}`.
   PHP-бэкенд Spreed, по данным Task 2.1, hard-restrictит signaling к
   `v3` (см. `testdata/signaling/README.md` §«Расхождение со спекой»).

   Реализация в `internal/call/signaling/types.go` идёт по спеке (`v4`).
   **Если тест падает с `EvError code=2` (404)** — заменить константу
   `pathSignalingFmt` в `types.go` на `v3` и перезапустить. Структура
   тела ответа от версии пути НЕ зависит — фикстуры корректны для обеих.

2. **STUN/TURN-серверы: где их брать.**

   Task 2.1 нашёл, что в Spreed STUN/TURN-конфиг приходит с
   `GET /api/v3/signaling/settings?token=...` (фикстура
   `testdata/signaling/capability.json`). Task 2.10 (capability-клиент)
   будет это разбирать; для signaling-теста (Task 2.3) не критично, но
   на первом прогоне стоит параллельно дёрнуть:

   ```sh
   curl -s -u "$NEXTCLOUD_LOGIN:$NEXTCLOUD_PASS" \
     -H 'OCS-APIRequest: true' -H 'Accept: application/json' \
     "$NEXTCLOUD_URL/ocs/v2.php/apps/spreed/api/v3/signaling/settings?token=$NCTALK_INTEGRATION_ROOM" \
     | python3 -m json.tool
   ```

   Зафиксировать для peer-слоя (Task 2.4+): какие именно STUN/TURN
   сервера отдаёт бой, есть ли TURN-credentials, какой `signalingMode`
   (`internal` / `external` / `hpb`). Если `hpb` — internal-signaling
   **не работает**, нужен другой подход (внешний signaling-сервер).

3. **`message.data` — JSON-строка или объект?**

   Парсер `decodeInnerMessage` делает двойной unmarshal: сначала в
   `string`, потом в `innerMessage`. Это предположение основано на
   PHP-бэкенде (`SignalingController::pullMessages` складирует через
   `json_encode($decodedMessage)`). Если на реальном трафике `data`
   идёт как объект — парсер пропустит все `message`-события (silent
   skip, см. `TestParse_MalformedData_SilentSkip` в Task 2.2).

   **Признак проблемы** — в логе теста есть только `EvUsersUpdated`,
   а `EvOffer`/`EvCandidate` отсутствуют, при том что собеседник
   точно делал offer (можно проверить через браузер DevTools параллельно).
   В этом случае убрать первый unmarshal в `decodeInnerMessage`.

### Как снять реальный трафик для обновления фикстур

Если зафиксировано расхождение с фикстурами Task 2.1 — обновить
`testdata/signaling/*.json` с боевого сервера. Способы (в порядке
предпочтения):

1. **DevTools браузера** (самый надёжный). Открыть Nextcloud Talk в
   Chrome/Firefox → F12 → Network → отфильтровать по `signaling` →
   совершить звонок с собеседником → для каждого запроса:
   - **Copy as cURL** (контекстное меню) — получить полный запрос;
   - или **Response → Copy** — тело ответа.

   Затем обезличить (см. `testdata/signaling/README.md` §«Обезличивание»):
   `sessionId` → `SESSION_N`, `actorId` → `alice/bob`, IP в ICE → RFC 5737
   TEST-NET (`192.0.2.x`/`198.51.100.x`/`203.0.113.x`), DTLS-fingerprint'ы
   → синтетика, room-token → `tok-test-call`.

2. **mitmproxy** с декодированием HTTPS — если нужен весь поток под рукой.
   Внимание: креды в `Authorization` будут видны в mitm'е — запускать на
   изолированной машине и **не сохранять дамп** в репо.

3. **Логи Nextcloud** (`data/nextcloud.log` на сервере с уровнем DEBUG
   для `spreed`) — для верификации структуры server-side; не для фиксации
   wire-формата (там его нет).

> **БЕЗОПАСНОСТЬ:** никогда не коммитьте в репо реальные креды, IP или
> PII. Все фикстуры должны быть обезличены (см. раздел «Обезличивание»
> в `testdata/signaling/README.md`). Перед коммитом проверить:
>
> ```sh
> # Маркерная проверка на триггерные слова в testdata/ (Task 2.1 DoD):
> grep -REn 'password|secret|token=[a-f0-9]{20,}|[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}' \
>   testdata/signaling/ | grep -vE '(example\.org|test-net|candidate:|0\.0\.0\.0|127\.0\.0\.1|192\.0\.2\.|198\.51\.100\.|203\.0\.113\.|SESSION_|alice|bob|carol)'
> ```
>
> Должен быть пустым. Если нет — добить обезличивание.

### Что проверяет signaling-тест

| Тест                                  | Эндпоинт                                        | Контракт                                                              |
| ------------------------------------- | ----------------------------------------------- | --------------------------------------------------------------------- |
| `TestSignalingPollLoop_JoinGetOneEventLeave` | `POST /call/{token}` + `GET /signaling/{token}` + `DELETE /call/{token}` | `JoinCall` без ошибки; за 5с получен хотя бы один Event (EvUsersUpdated или EvError). |

## nctalk-call integration (Task 3.3)

Интеграционные тесты точки входа `cmd/nctalk-call` (`//go:build integration`).
In-process: дёргают `run()` напрямую с реальным env — НЕ требуют второго
participant / аудио-верификации (это manual spike-gate, см. ниже). Проверяют
exit-контракт (0/1/2/3) интеграционно на реальном сервере.

### Обязательное окружение

Те же `NEXTCLOUD_URL/LOGIN/PASS`, плюс:

| Переменная | Значение |
| --- | --- |
| `NCTALK_INTEGRATION_CALL` | `1` — явный opt-in (звонки = мутация: JoinCall/LeaveCall меняют состояние комнаты). |
| `NCTALK_INTEGRATION_ROOM` | token целевой комнаты (для `JoinLeave_Alone`; `UnknownToken` использует захардкоженный несуществующий). |
| `NCTALK_ICE_TIMEOUT` (опц.) | Короткий (напр. `3s`) для `JoinLeave_Alone` — иначе 30с default (Task 3.2). |

### Запуск

```sh
# Docker Talk 20.1.11 (localhost:8484, admin/adminpass):
NCTALK_INTEGRATION_CALL=1 \
NEXTCLOUD_URL=http://localhost:8484 \
NEXTCLOUD_LOGIN=admin \
NEXTCLOUD_PASS=adminpass \
NCTALK_INTEGRATION_ROOM=<token> \
CGO_ENABLED=0 go test -tags=integration -run TestIntegration -v -timeout 60s ./cmd/nctalk-call/
```

⚠️ test-binary тянет pion → macOS Application Firewall спросит сеть (один раз,
потом запоминает). На Linux/CI — без вопроса.

### Что проверяет каждый тест

| Тест | Сценарий | Контракт |
| --- | --- | --- |
| `TestIntegration_UnknownToken_Exit2` | позиционный `<room>` = несуществующий token | JoinRoom 404 → **exit 2** (§10, `mapJoinCallErr`). Стабильно. **PASS** Docker Talk 20.1.11 (2026-07-21). |
| `TestIntegration_JoinLeave_Alone_Exit0` | `--recvonly` в комнату без участников с аудио, `NCTALK_ICE_TIMEOUT=3s` | никто не подключился → ICE-timeout → «я один» → **exit 0** (§6/§10, Task 3.2). **Требует пустую комнату** — иначе exit 1 (корректно, но тест ожидает 0). |

### Spike-gate (manual, аудио-верификация) — отдельно

`TestSpike_AudioBothDirections` (DEFERRED/skip) — ручной: реальный звонок с
браузером, voice-loop.pcm → запись → уши. НЕ автоматизирован (second participant
в браузере + прослушивание). Процедура — в шапке `cmd/nctalk-call/integration_test.go`.
**PASS на Docker Talk 20.1.11 (2026-07-21)** после правок 3.1/3.2/3.4:
«говорить» (voice-loop → браузер, слышно ушами) + «слушать» (браузер →
nctalk-call → rec.pcm, mean −25 dB — сигнал есть). См. `docs/session-state/2026-07-21-nctalk-calls.md`.
