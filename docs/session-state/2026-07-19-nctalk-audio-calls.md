# nctalk-audio-calls — аудио-звонки WebRTC для nctalk

## Контекст
Встроить в `nctalk` реальное аудио в звонках Nextcloud Talk (слушать + говорить) из
терминала/процесса, как **изолированный модуль**. Провели через `spec-to-code`
(спека → план → реализация → code-review → правки → коммит). Потребитель — человек
в терминале (`nctalk-talk`) и агент/pipe (`nctalk-call`).

## Текущее состояние
**ПЕРЕРАБОТКА SIGNALING LAYER ВЫПОЛНЕНА (2026-07-20)** — все 5 багов починены через
TDD с обязательной Docker-проверкой каждого (НЕ compile-only — урок из прошлой
сессии). Все 16 пакетов зелёные, build/vet OK, integration на Docker PASS
(canonical flow end-to-end). **Остаётся spike-gate round-trip** (полная audio-pipe
проверка с браузером). Изменения НЕ закоммичены (git: modified на feat/nctalk-calls).

Чек-лист:
- [x] Спека + план + реализация Этапов 0–2 + code-review (9 valid правок, `8a8684b`)
- [x] Тесты зелёные, `exit.ExitError` value-type баг чинен
- [x] Подпись `nctalk-dev`, Makefile, CLAUDE.md/README (`5656e06`)
- [x] Spike-gate attempt (2026-07-20): найдены 5 багов signaling
- [x] Переоценка HPB: v3 работает на обоих серверах
- [x] **Переработка signaling (5 багов) — ВЫПОЛНЕНА (2026-07-20):** weblogin пакет
  (баг #5), signaling v3 (#1), settings без token (#2), joinRoom шаг (#3),
  pull/joinCall порядок (#4). TDD red-green + Docker integration PASS на каждый.
- [ ] **Закоммитить переработку signaling** (отдельный commit на feat/nctalk-calls)
- [ ] Spike-gate round-trip 440 Гц на Docker (nctalk-call + браузер, задача #4)
- [ ] Этап 3 (`NCTALK_ICE_TIMEOUT`, exit-контракты, orphan-ffmpeg), Этап 4 (TUI),
  Task 2.6 (glare) — после spike PASS
- [ ] Merge `feat/nctalk-calls` → main — после spike PASS

## Найденные баги signaling-слоя (5 — всё скрыл compile-only)

| # | Баг | Файл | Правильно | Evidence |
|---|---|---|---|---|
| 1 | signaling **v4** | `internal/call/signaling/types.go:100` `pathSignalingFmt` | **v3** | `custom_apps/spreed/appinfo/routes/routesSignalingController.php`: `'apiVersion'=>'(v3)'`; curl GET v3 → 200, v4 → 998 |
| 2 | `signaling/settings` **с token** | `internal/call/capability/capability.go:68` `pathSignalingSettingsFmt` | **без token** `/api/v3/signaling/settings` | route `Signaling#getSettings` без `{token}`; curl без token → 200 `signalingMode:internal` |
| 3 | нет **joinRoom** (`POST /api/v4/room/{token}/participants/active`) | `internal/call/agent/agent.go` Run | добавить шаг (создаёт participant session) | `CallController.php:234` → 404 без session; `RoomController::joinRoom` (стр.1633) вызывает `setSessionForRoom` |
| 4 | JoinCall **до** pull | `agent.go` Step 1 (JoinCall первым) | pull/joinRoom **перед** joinCall | access.log браузера: GET signaling (08:46:42) → POST call (08:46:53) |
| 5 | **нет PHP-session** (web-login) | `transport`/`config` (http.Client) | Basic-auth её не даёт — нужен web-login flow | `pullMessages` читает `$_SESSION` (`getSessionForRoom`); curl Basic-auth pull → 404 даже с cookie+joinRoom, браузер (web-login) → 200 |

## Фикс багов 2026-07-20 (реализация — TDD red-green + Docker integration)

| Баг | Реализация | Файлы | Docker-проверка |
|---|---|---|---|
| #5 web-login | Новый пакет `internal/call/weblogin`: `Login(ctx, doer, auth)` — GET /login → parse `requesttoken="([^"]+)"` → POST /login (form user/pass/requesttoken) → verify `303 Location` (не `direct`). cmd: shared `cookiejar` + `loginClient` (no-redirect) + `httpClient` (same-host) — session живёт в jar. | `weblogin/{doc,weblogin}.go`, `weblogin_test.go`, `weblogin/integration_test.go`, `cmd/nctalk-call/main.go` | `cloud/user` → залогинен admin |
| #1 v3 | `pathSignalingFmt` v4→v3 | `signaling/types.go`, `signaling_test.go` (Send path) | pull v3 → 200 `usersInRoom` |
| #2 settings без token | `pathSignalingSettings` без `%s`; `Settings(ctx)` без token | `capability/capability.go`, `capability_test.go`, `capability/integration_test.go` | Settings → 1 STUN (раньше 404) |
| #3 joinRoom | `Client.JoinRoom(ctx,token)(sid,err)` — POST `/api/v4/room/{token}/participants/active`, парсит `data.sessionId`; cmd: joinRoom → `SetSessionId` → `OwnSessionId` в `agent.Config` | `signaling/{types,signaling}.go`, `signaling_test.go`, `cmd/nctalk-call/main.go` | joinRoom → sid (255 chars) |
| #4 порядок | `JoinRoom` перед `JoinCall` (в cmd перед `agent.Run`). Порядок pull/joinCall некритичен (оба 200 на Docker) | `cmd/nctalk-call/main.go` | canonical flow PASS |

`signaling/integration_test.go` переписан под canonical flow (`TestSignalingPollLoop_CanonicalFlow`: weblogin → joinRoom → SetSessionId → joinCall → PollLoop → EvUsersUpdated с own session). Старый Task 2.3 scaffolding был баговый (без weblogin/joinRoom, v4).

## Canonical Spreed signaling flow (из access.log браузера, internal mode)

```
1. Web-login → PHP session cookie
2. Открыть комнату → POST /api/v4/room/{token}/participants/active (joinRoom)
   → setSessionForRoom в $_SESSION
3. GET  /api/v3/signaling/{token} (pull) → 200 (session есть)
4. POST /api/v4/call/{token} (joinCall, flags:3 sendrecv) → 200
5. Poll loop: GET /api/v3/signaling/{token} (long-poll ~30с)
6. DELETE /api/v4/call/{token} (leave)
```

Endpoints (Spreed 20.1.11, `/var/www/html/custom_apps/spreed/appinfo/routes/`):
- `routesSignalingController.php`: signaling = **v3**
  (`pullMessages` GET, `sendMessages` POST, `getSettings` GET **без token**, `backend` POST)
- `routesCallController.php`: call = **v4**
  (`joinCall` POST, `leaveCall` DELETE, `getPeersForCall` GET)
- `routesRoomController.php`: `joinRoom` POST `/api/v4/room/{token}/participants/active`

## Инсайты и решения
- **compile-only — главная причина багов:** реализация Этапов 0–2 (SDD) прошла в
  compile-only режиме. Это скрыло ВСЕ 5 signaling-багов. Unit-тесты не покрывают
  реальный Spreed API contract (они на моках с предположениями о v4/порядке/session).
  **Урок:** signaling/integration layer требует **обязательной** проверки на реальном
  сервере до code-review; compile-only ≥ code-review = отложенный провал.
- **HPB-переоценка:** `capabilities.spreed.config.signaling.mode = "external"` на
  nc.example.org НЕ означает, что OCS-polling отключён. v3 signaling endpoint
  существует независимо. Ранний вывод был преждевременным — проверялся v4 (наш баг),
  не v3.
- **PHP-session blocker для API клиентов:** `pullMessages` (internal signaling)
  требует PHP `$_SESSION`, инициализируемую **только web-login'ом**. Basic-auth
  (app-password) — stateless, не даёт `$_SESSION`. Для nctalk-call нужно реализовать
  web-login flow (GET /login → requesttoken + session cookie → POST /login →
  обновлённый session cookie → использовать cookie для signaling). Либо проверить
  альтернативы (см. Открытые вопросы).
- **Docker-тестовый сервер работает:** Nextcloud 30.0.17 + Talk 20.1.11, internal
  signaling, localhost:8484 (8080 занят контейнером `buggregator`). admin (пароль — в `~/tmp/nctalk-docker/docker-compose.yml`, вне репо).
  Комната TEST token aszjebv5. Talk включена через `occ app:enable spreed`.
- **macOS codesign:** stable-identity `nctalk-dev` (см. предыдущие инсайты).
- **`exit.ExitError` — VALUE-тип**, Mesh fanout (один track, AddTrack в каждую PC) —
  см. CLAUDE.md.
- **tg-me:** флаг `-w/--wait` **обязывает** `-t 435`. Общение по проекту — топик 435.
- **say:** звать пользователя без `-v` (глобальная memory в `~/.claude/CLAUDE.md`).
- **Basic-auth НЕ мешает signaling pull при session-cookie** (проверено Docker
  Talk 20.1.11): cookie+Basic+OCS-APIRequest → 200. Заметка в таблице багов №5
  «curl Basic-auth pull → 404 даже с cookie+joinRoom» — неточна: там не было
  валидной PHP-session (login failed на пустом requesttoken). БЕЗ session (Basic
  only) → 404; С session (cookie) → 200 независимо от Basic. Значит `DoOCS`
  (всегда ставит Basic) остаётся как есть для signaling.
- **Порядок pull/joinCall некритичен** (обе последовательности → 200 на Docker).
  Canonical browser = pull до joinCall, но минимальная правка = joinRoom перед
  JoinCall (session создаёт именно joinRoom, не pull).
- **weblogin детали (подтверждено curl+Docker):** requesttoken в HTML — атрибут
  `requesttoken="..."` (НЕ name=value), regex `requesttoken="([^"]+)"`; login
  success = `303 Location: /apps/dashboard/` (провал = `/login?direct=1`);
  мутирующим OCS с session-cookie НЕ нужен requesttoken-header (`OCS-APIRequest:
  true` достаточен); `OCS-APIRequest` обязателен (без него → 412).
- **Архитектура shared cookiejar:** `loginClient` (CheckRedirect=
  ErrUseLastResponse — для weblogin читать 303) и `httpClient`
  (SameHostRedirectPolicy — для capability/signaling) разделяют один `cookiejar`.
  weblogin наполняет jar session-cookie, capability/signaling подхватывают.
  `talkClient` (ResolveRoom) — отдельный, без jar (Basic-auth достаточно).
- **Способ работы (feedback):** state-файл обновляется ТОЛЬКО через `/checkpoint`,
  не во время работы (memory `checkpoint-only-session-state`). Переработку делали
  инкрементально через TaskCreate (по багам), НЕ монолитным spec-to-code.

## Отвергнутые подходы
- pion + CGO CoreAudio/libopus — ломает `CGO_ENABLED=0`.
- headless Chrome / gstreamer-webrtc — ломает «тонкий CLI».
- Ad-hoc подпись — меняется при пересборке.
- **HPB/WebSocket signaling как единственный путь — переоценено:** изначально думали,
  что external mode требует WebSocket. Реально — OCS-polling (v3) работает, нужен лишь
  web-login session для API-клиента.

## Открытые вопросы
- ~~**web-login flow в Go**~~ → **РЕШЁНО:** пакет `internal/call/weblogin` реализован,
  Docker PASS (cloud/user → залогинен).
- **nc.example.org (Talk 23):** проверить после spike-gate — v3 signaling + web-login
  должны работать (v3 endpoint существует, подтверждено ранее). Spreed version compat
  (тестовый Talk 20.1.11 vs nc.example.org Talk 23) — signaling v3 ожидаемо стабилен,
  но проверить на боевом.
- **Spike-gate round-trip 440 Гц:** требует ручного шага (браузер в комнате TEST с
  аудио + nctalk-call sendrecv). Если audio-pipe не заработает (peer/media/agent) —
  отладка SDP/ICE/codec, НЕ signaling (signaling подтверждён working canonical flow).
- ~~**Docker-сервер**~~ → оставлен running (Up, готов к spike-gate).

## Next steps
1. **Закоммитить переработку signaling** (5 багов) — отдельный commit на
   `feat/nctalk-calls` (НЕ в main). `git add` изменённых файлов (см. «Фикс багов
   2026-07-20»). Без AI-атрибуции (memory `commit-no-attribution`).
2. **Spike-gate round-trip 440 Гц** на Docker (задача #4):
   - `make build-signed` (nctalk-call подписан `nctalk-dev`).
   - Браузер: войти admin в комнату TEST (`aszjebv5`), запустить звонок с аудио.
   - `NCTALK_INTEGRATION_CALL=1 NCTALK_INTEGRATION_ROOM=aszjebv5 ./nctalk-call <room>`
     (sendrecv). Или integration-test `TestSpike_AudioBothDirections` (`-tags=integration`).
   - Spike-check: `./spike-check recording.pcm` → 440 Гц в обе стороны = PASS.
3. **PASS →** Этап 3 (`NCTALK_ICE_TIMEOUT`, exit-контракты, orphan-ffmpeg), Этап 4
   (TUI `nctalk-talk`), Task 2.6 (glare/perfect-negotiation), merge → main.
   **FAIL →** отладка audio-pipe (peer ICE/DTLS, media codec/ffmpeg, agent flow),
   НЕ signaling (signaling подтверждён working canonical flow).
4. **nc.example.org (Talk 23):** проверить после spike PASS (v3 + weblogin).

## Точка входа
- `docs/superpowers/specs/2026-07-19-nctalk-call-design.md` — спека (эталон).
- `docs/superpowers/plans/2026-07-19-nctalk-call-design.plan.md` — план.
- `docs/superpowers/reviews/2026-07-19-nctalk-call-design.review.md` — code-review.
- `CLAUDE.md` — секция «Аудио-звонки WebRTC».
- **Переработка signaling (2026-07-20, не закоммичено):** `git diff` на
  `feat/nctalk-calls` — изменены `signaling/{types,signaling,signaling_test}.go`,
  `signaling/integration_test.go` (переписан), `capability/capability.go` +
  `capability_test.go`, `cmd/nctalk-call/main.go`; **новый пакет**
  `internal/call/weblogin/` (`doc.go`, `weblogin.go`, `weblogin_test.go`,
  `integration_test.go`); **новый** `capability/integration_test.go`.
- **Integration-тесты (запуск на Docker):**
  - `weblogin`: `TestIntegration_Login_Docker` — web-login → session.
  - `capability`: `TestIntegration_Settings_Docker` — STUN/TURN без token.
  - `signaling`: `TestSignalingPollLoop_CanonicalFlow` — полный canonical flow.
  - Запуск: `-tags=integration` + env `NCTALK_INTEGRATION_CALL=1
    NCTALK_INTEGRATION_ROOM=aszjebv5 NEXTCLOUD_URL=http://localhost:8484
    NEXTCLOUD_LOGIN=admin NEXTCLOUD_PASS=adminpass`.
- **Docker-тестовый сервер:** `~/tmp/nctalk-docker/docker-compose.yml`
  (Nextcloud 30 + Talk 20.1.11, localhost:8484, admin (пароль — в `~/tmp/nctalk-docker/docker-compose.yml`, вне репо), TEST=aszjebv5).
  Запуск: `docker compose -f ~/tmp/nctalk-docker/docker-compose.yml up -d`.
  Включить Talk: `docker exec nctalk-app php /var/www/html/occ app:enable spreed`.
  **access.log:** `docker logs nctalk-app 2>&1 | grep spreed/api` (Apache logs →
  /dev/stdout → docker logs; НЕ cat access.log — это symlink на /dev/stdout).
- Ветка `feat/nctalk-calls`: `git log` (commits `3418a44`…`5656e06` + незакоммиченная переработка signaling).
- Memory: `nccli-nextcloud-talk.md`, `tg-me-topic.md`, `checkpoint-only-session-state`.
