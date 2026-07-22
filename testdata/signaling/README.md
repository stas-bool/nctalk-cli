# `testdata/signaling/` — фикстуры signaling-обмена Nextcloud Talk (Spreed)

**Происхождение:** синтетические фикстуры, собранные из **публичного исходного
кода Spreed** (`nextcloud/spreed`, ветка `main` на дату 2026-07-19). **Не**
сняты с боевого сервера. Реальный трафик нужно сверить в **Task 2.3**
(`internal-signaling` spike).

Назначение — дать парсеру `internal/call/signaling` (Task 2.2) структурно
корректные тестовые данные для разбора `usersInRoom`/`offer`/`answer`/`candidate`
и для генерации исходящих POST.

## Источники (авторитетные)

- `lib/Controller/SignalingController.php`
  — `pullMessages()`, `sendMessages()`, `getSettings()`, `getUsersInRoom()`.
- `lib/Controller/CallController.php`
  — `getPeersForCall()` (это `GET /call/{token}`).
- `src/utils/signaling.js`
  — клиентский `Signaling.Internal`: `_startPullingMessages`, `sendCallMessage`,
    `_sendMessages`, `_sendMessageWithCallback`.
- `src/services/signalingService.js`
  — `pullSignalingMessages`, `fetchSignalingSettings`.

Все URL'ы сырого исходника:
- <https://raw.githubusercontent.com/nextcloud/spreed/main/lib/Controller/SignalingController.php>
- <https://raw.githubusercontent.com/nextcloud/spreed/main/lib/Controller/CallController.php>
- <https://raw.githubusercontent.com/nextcloud/spreed/main/src/utils/signaling.js>
- <https://raw.githubusercontent.com/nextcloud/spreed/main/src/services/signalingService.js>

## Сводка фикстур

| Файл | Эндпоинт (метод) | Что содержит |
|---|---|---|
| `usersInRoom.json` | `GET /signaling/{token}` | Полный ответ long-pull'а: bundle из `message`(offer + candidate) и финального `usersInRoom`-снапшота на 2 участника |
| `offer.json` | `GET /signaling/{token}` | Одна входящая `message` типа `offer` (audio-only sendrecv SDP) |
| `answer.json` | `GET /signaling/{token}` | Одна входящая `message` типа `answer` (audio-only sendrecv SDP, `setup:active`) |
| `candidate.json` | `GET /signaling/{token}` | Входящие `message` типа `candidate`: host + srflx + end-of-candidates (trickle) |
| `call_participants.json` | `GET /call/{token}` | Список пиров в звонке (`getPeersForCall`), 3 участника |
| `capability.json` | `GET /signaling/settings?token=...` | STUN/TURN-конфиг + `signalingMode:"internal"` |

## Ключевые контрактные факты (сверены с PHP-бэкендом)

1. **OCS-конверт.** Все ответы обёрнуты в `{"ocs":{"meta":{...},"data":...}}`.
   `meta.statuscode` — нижний регистр (как в существующих фикстурах проекта).
2. **`GET /signaling/{token}` (long-poll).**
   `ocs.data` — **массив сообщений**. Каждый элемент `{type, data}`:
   - `type:"usersInRoom"` → `data` — массив участников; такой элемент
     сервер **всегда добавляет в конец** ответа (даже при 404 — тогда с пустым
     `data:[]`).
   - `type:"message"` → `data` — **строка**, JSON-кодированное signaling-сообщение
     (сервер складирует через `json_encode($decodedMessage)`). Клиент парсит её
     сам; TS/JS-клиент делает `if (typeof data === 'string') data = JSON.parse(data)`.
3. **Форма участника `usersInRoom`** (см. `SignalingController::getUsersInRoom`):
   ```json
   {
     "userId": "...",            // только если actorType=="users", иначе ""
     "roomId": 1234,
     "lastPing": 1721410000,
     "sessionId": "...",
     "inCall": 3,                // битмаск: 1=IN_CALL, 2=WITH_AUDIO, 4=WITH_VIDEO
     "participantPermissions": 251,  // битмаск permissions
     "actorType": "users",
     "actorId": "..."
   }
   ```
4. **Форма входящего сообщения `message.data`** (после `JSON.parse`):
   ```json
   {
     "type": "offer|answer|candidate|control",
     "from": "<sender_sessionId>",     // добавляется сервером
     "to":   "<recipient_sessionId>",  // наше sessionId
     "roomType": "video|screen",       // "video" и для audio-only звонков!
     "payload": { /* type-специфичное */ }
   }
   ```
   - `payload` для `offer`/`answer`: `{"type":"offer|answer", "sdp":"v=0\r\n..."}`.
   - `payload` для `candidate`: `{"candidate":"candidate:...", "sdpMLineIndex":0, "sdpMid":"0", "usernameFragment":"..."}`.
     Признак конца trickle-последовательности — `payload.candidate === ""`.
5. **`roomType:"video"` для audio-only.** Spreed всегда открывает peer-соединение
   с `roomType:"video"`; направление аудио/видео регулируется SDP m-line
   (`a=sendrecv`/`a=recvonly`/`a=sendonly`), а не полем `roomType`. Парсер
   должен на это не опираться.

## Исходящий POST — `POST /signaling/{token}`

Тело запроса — `application/x-www-form-urlencoded`, **не** JSON. Один параметр
`messages`, значение которого — JSON-строка массива записей:

```sh
# Пример: отправка offer'а. JSON показан отформатированным; в теле запроса
# он уходит одной строкой (json.stringify) и затем form-encode'ится.
curl -X POST "https://nc.example.org/ocs/v2.php/apps/spreed/api/v3/signaling/$TOKEN" \
  -u "$NEXTCLOUD_LOGIN:$NEXTCLOUD_PASS" \
  -H 'OCS-APIRequest: true' \
  -H 'Accept: application/json' \
  --data-urlencode 'messages=[{"ev":"message","fn":"{\"type\":\"offer\",\"to\":\"SESSION_2\",\"roomType\":\"video\",\"payload\":{\"type\":\"offer\",\"sdp\":\"v=0\\r\\n...\"}}","sessionId":"SESSION_1"}]'
```

- `ev:"message"` — тип события (для P2P-пересылки SDP/ICE всегда `"message"`).
- `fn` — JSON-строка полезной нагрузки `{type,to,roomType,payload}`. **Без**
  поля `from` — сервер его сам подставит (из `sessionId` записи) и добавит в
  хранимое для получателя сообщение.
- `sessionId` — **свой** sessionId (отправителя), сервер проверяет его против
  сессии в комнате.
- Ответ на успешный POST — `{"ocs":{"meta":{...},"data":null}}`.

## Обезличивание

| Реальное значение | Замена |
|---|---|
| `sessionId` (~32-hex хеш Nextcloud-сессии) | `SESSION_1`, `SESSION_2`, `SESSION_3` |
| `actorId` (логин пользователя Nextcloud) | `alice`, `bob`, `carol` |
| `userId` (то же, что actorId для `actorType=="users"`) | `alice` и т.п. |
| `displayName` | `Alice`, `Bob`, `Carol` |
| `actorType` | `users` (реальный для case-тестов) |
| Room token | `tok-test-call` |
| STUN/TURN хосты | `stun.example.org` / `turn.example.org` (RFC 2606) |
| TURN credentials | `1714060800:alice` / `REDACTED-TURN-AUTH` (фиктивные) |
| Публичные IP в ICE-candidates | RFC 5737 TEST-NET: `192.0.2.x`, `198.51.100.x`, `203.0.113.x` |
| DTLS- fingerprint'ы в SDP | Синтетические hex-последовательности |
| Имена пользователей/пароли | Отсутствуют |

## Расхождение со спекой (важно!)

Спека `docs/superpowers/specs/2026-07-19-nctalk-call-design.md` §7 требует:

> Long-poll `GET /ocs/v2.php/apps/spreed/api/v4/signaling/{token}`

**Это неверно для актуального Spreed.** PHP-бэкенд hard-restrict'ит
signaling-эндпоинты к `apiVersion => '(v3)'`:

- `GET  /api/v3/signaling/settings`
- `GET  /api/v3/signaling/{token}` (long-poll)
- `POST /api/v3/signaling/{token}`
- `POST /api/v3/signaling/backend`
- `GET  /api/v3/signaling/welcome/{serverId}`

Тогда как **Call API** — действительно `v4`:
`GET/POST/PUT/DELETE /api/v4/call/{token}`.

Сама автор спеки это и предусмотрел: «Формат сообщений (`usersInRoom`,
`message{type: offer/answer/candidate}`) — из JS-кода `spreed`, **не из доков**.
Точный протокол — предмет spike (§12).» Расхождение должно быть разрешено
в Task 2.3 при сверке с боевым: либо обновить спеку на `v3`, либо
убедиться, что в интересующей версии Spreed действительно используется `v4`
(маловероятно — миграция v3→v4 не зафиксирована в публичной истории).

Структура **тела** ответа/запроса от версии пути не зависит: фикстуры
корректны независимо от того, по какому пути их будет запрашивать
реализация.

## Что в фикстурах — предположения (educated guesses)

Помечены явно в случаях, когда не выводятся напрямую из исходника:

- Точные **значения** `participantPermissions` (битмаск `251` —
  `DEFAULTS | MAX_PUBLIC_FLAGS` из `Participant` в Spreed; для обычного
  участника это разумное значение, но в реальности может отличаться в
  зависимости от роли). Сам **тип** (int) и название поля — авторитетные.
- **Формат SDP** (payload-type numbers, extmap, fmtp) — типовой для
  Talk/WebRTC audio-only, но реальные клиенты могут присылать другой набор
  PT. Важно для парсера только то, что SDP — это строка; саму структуру
  парсер не разбирает (передаётся в pion как есть).
- **Порт TURN** (3478/443), **типы транспортов** (udp/tcp) — стандартные,
  но у конкретного деплоя могут быть другими.

## Валидация

Все JSON-фикстуры прошли `python3 -m json.tool` без ошибок (см. отчёт по
Task 2.1). Маркерная проверка на триггерные слова-индикаторы кредов по
`testdata/` (см. DoD в плане Task 2.1) возвращается пустой — реальных
кредов и PII нет; только placeholder'ы и имена RFC-reserved доменов.
