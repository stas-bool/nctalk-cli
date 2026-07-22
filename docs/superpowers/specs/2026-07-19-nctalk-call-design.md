# Дизайн: `nctalk-call` / `nctalk-talk` — аудио-звонки Nextcloud Talk поверх WebRTC

- **Дата:** 2026-07-19
- **Статус:** правки по review v1 (13 замечаний) внесены, ожидает одобрения → план реализации (spike-first)
- **Стек:** Go 1.21+ (stdlib + `github.com/pion/webrtc/v4`), macOS
- **Репозиторий:** `/Users/stas/Projects/My/NCCliClient` (git, ветка `main`)
- **Эталон контракта:** `docs/superpowers/specs/2026-07-17-nctalk-cli-design.md` (базовый CLI `nctalk`)

## 1. Цель и контекст

Реальное аудио в звонках Nextcloud Talk (Spreed): **слушать собеседников и
говорить** из терминала/процесса, минуя браузер и десктоп-клиент. Звонок в Talk
состоит из **двух независимых слоёв**:

1. **Call API** (`POST/GET/PUT/DELETE /ocs/v2.php/apps/spreed/api/v4/call/{token}`)
   — чистый OCS-HTTP, фиксирует присутствие участника в звонке и флаги потоков
   (audio/video/speaking), но **медиа не передаёт**. Базовый `nctalk` уже стоит на
   этом транспорте.
2. **Signaling + WebRTC** — реальный медиа-обмен (SDP/ICE, DTLS-SRTP). Этот слой в
   проекте пока отсутствует целиком; именно его добавляет данный spec.

Два режима работы — **два отдельных модуля/бинарника** (общее ядро см. §4):

- **`nctalk-call`** — режим «агент/pipe»: звук течёт через stdin/stdout (PCM
  `s16le`/48 кГц/моно). Headless, для скрипта/бота/записи.
- **`nctalk-talk`** — режим «человек/TUI»: микрофон/динамик через системные
  утилиты (`ffmpeg`/`sox`), интерактивный терминальный интерфейс.

Потребитель — человек в терминале и агент-процесс. Базовый `nctalk` (7 команд,
`stdlib-only`, `CGO_ENABLED=0`) **остаётся нетронутым**: новая функциональность —
отдельный изолированный модуль с чистой границей удаления (§3).

### Ключевые риски (три)

1. **Signaling-протокол Talk плохо документирован.** Call API описан в доках, а
   внутренний signaling-обмен (как клиенты договариваются об SDP/ICE для P2P через
   OCS-polling `/signaling`) фактически читается по JS-коду `spreed`. Это главная
   точка провала — отсюда подход **spike-first** (§12).
2. **Framing raw Opus-пакетов ↔ контейнер между ffmpeg и pion.** `pion` работает
   на уровне Sample API: `TrackLocalStaticSample.WriteSample` ожидает **один raw
   Opus-пакет** за вызов (≈20 мс), `ReadSample` отдаёт raw Opus. А `ffmpeg -f opus`
   на выходе пишет **контейнер OGG** (со страничной рамкой), и `ffmpeg -f opus -i
   -` на входе тоже ждёт OGG. Проблема не в кодеке Opus как таковом, а в том, что
   формат обмена ffmpeg↔pipe — контейнер, а pion хочет raw-пакеты. Решение — явная
   codec/framing-стратегия (§8): либо pure-Go OGG-парсер с обеих сторон, либо
   формат, который ffmpeg умеет и который тривиально режется на отдельные пакеты.
3. **Opus-кодек отсутствует в `pion`.** `pion/webrtc` работает на уровне
   RTP/Sample с Opus-payload, сам кодек не содержит. Без CGO codec-стратегия —
   `ffmpeg`-subprocess (§8). Развязка локализована в `call/media` и не касается
   ядра signaling/peer.

### Out of scope (в этот spec НЕ входит)

- ❌ **Видео** (в т.ч. video-track) — только аудио.
- ❌ **HPB/SFU** и звонки >7 участников — только P2P-mesh.
- ❌ **Windows/Linux** — только macOS.
- ❌ **Нативная CoreAudio через CGO** — только subprocess (`ffmpeg`/`sox`), пока.
- ❌ Любые изменения в существующем `nctalk` и его командах.
- ❌ Многопользовательское эхо-гашение (AEC) — рассчитываем на AEC ОС/устройства.

## 2. Исследования

- **WebRTC на Go:** `github.com/pion/webrtc/v4` — зрелая pure-Go реализация WebRTC
  API; используем как основу peer-слоя. Кодеки (Opus) **не входят** — см. §8.
- **Call API** (`docs/call.md` репо `nextcloud/spreed`): `POST /call/{token}` join
  с `flags` (битмаск: in-call / with-audio / with-video / speaking / …), `GET` —
  список участников (`actorType/actorId/displayName/sessionId/lastPing`), `PUT` —
  обновить flags, `DELETE` — leave (с опцией `all` для модератора — завершить
  звонок), `POST /call/{token}/ring/{attendeeId}` — позвонить.
- **Internal signaling (P2P, без HPB):** OCS-polling
  `/ocs/v2.php/apps/spreed/api/v4/signaling/{token}` (та же версия `v4`, что и у
  Call API; ранние drafts указывали `v1`/без версии — **некорректно**, см. §7).
  Формат сообщений (`usersInRoom`, `message{type: offer/answer/candidate}`) — из
  JS-кода `spreed`, **не из доков**. Точный протокол — предмет spike (§12).
- **STUN/TURN:** конфигурация доступна клиенту через capability/endpoint сервера;
  для P2P в одной сети хватает STUN, через NAT нужен TURN (creds из signaling).
- **Audio-IO без CGO на macOS:** `ffmpeg` через avfoundation (захват/вывод
  CoreAudio) и/или `sox` (`rec`/`play`) с raw PCM по pipe. Оба — внешние бинарники,
  вызываемые через `os/exec`; **CGO не требуется**.
- Готового CLI/TUI-клиента Talk-звонков на Go не найдено; боты на стороне сервера
  (Talk Transcriber) решают другую задачу.

## 3. Архитектурная форма и граница удаления

Принцип: **существующий `nctalk` и его фундамент (`internal/client`, `config`,
`redact`) не получают ни одной новой зависимости и ни одной строки про WebRTC.**

- **Один `go.mod`** (не отдельные go-модули) — обязательно: иначе Go-правило
  `internal/` запретит `nctalk-call`/`nctalk-talk` импортировать `internal/client`.
  Изоляция достигается **отдельными точками входа** (`cmd/`); линкер тащит в
  `./cmd/nctalk` только достижимое из него → `pion` физически не попадает в базовый
  бинарник.
- **Переиспользование транспорта `internal/client`.** Транспортные примитивы
  (`httpDoer`-интерфейс, `doOCS`-метод, `sameHostRedirectPolicy`, сборка Basic-auth
  + OCS-заголовков) сейчас **unexported в `internal/client`** — `call/signaling`
  не может переиспользовать их напрямую. Рассмотрены три варианта:
  - (а) минимальные export'ы из `internal/client` либо вынос в новый тонкий пакет
    `internal/transport` (переиспользуют и `client`, и `signaling`);
  - (б) дублирование транспорта в `signaling` — **отклонено**: дублируется redirect-
    политика, теряется защита от cross-host auth-leak и https→http downgrade;
  - (в) public signaling-методы на `TalkClient` — **отклонено**: раздувает
    публичный API базового клиента.
  **Выбрано (а) в форме нового пакета `internal/transport`** (чистый HTTP без
  WebRTC): туда переезжают `httpDoer`, `doOCS`, `sameHostRedirectPolicy`, сборка
  Basic-auth+OCS-заголовков; `internal/client` становится потребителем
  `internal/transport` (механический перенос, без изменения поведения —
  регресс-тест `TestRun_PasswordDoesNotLeak_CrossHostRedirect` остаётся зелёным).
  `call/signaling` переиспользует тот же транспорт → защита https→http downgrade
  действует и в signaling-слое.
- **Граница удаления WebRTC:** всё WebRTC-добро живёт под `internal/call/` + два
  новых `cmd/`. Откат = `rm -rf internal/call cmd/nctalk-call cmd/nctalk-talk && go
  mod tidy`. Проверка отката: `go.mod` снова без `pion`,
  `CGO_ENABLED=0 go build ./cmd/nctalk` и `go test ./...` — зелёные.
  **Важно:** `internal/transport` **НЕ входит в `rm -rf`** — это рефакторинг
  общего фундамента (чистый HTTP), не WebRTC; после отката звонков он остаётся
  переиспользоваться базовым `nctalk`. Инвариант «0 строк про WebRTC в базовом
  бинарнике» сохраняется в полном объёме.
- **Общее ядро** (`internal/call/{signaling,peer,media}`) делят оба режима; режимы
  расходятся только в реализациях `AudioSource/AudioSink` (`call/agent` vs
  `call/interactive`).
- **`CGO_ENABLED=0` сохраняется глобально:** ни один бинарник не использует CGO —
  audio-IO и Opus-кодек через subprocess (§8). Это намеренный выбор против
  варианта с libopus/CoreAudio-CGO (см. §8, §13).
- Тесты и сборка — по-прежнему под `CGO_ENABLED=0` (см. `CLAUDE.md`, `dyld:
  missing LC_UUID` на этой машине).

## 4. Структура пакетов

```
NCCliClient/
├─ go.mod, go.sum                     # + github.com/pion/webrtc/v4 (только в call/*)
├─ cmd/nctalk/         ← СУЩЕСТВУЮЩИЙ, нетронут
├─ internal/
│  ├─ client/ config/ redact/         ← ОБЩИЙ фундамент. client переходит на internal/transport.
│  ├─ transport/                      ← НОВОЕ (рефакторинг, НЕ WebRTC): httpDoer, doOCS,
│  │                                    redirect-политика, Basic-auth+OCS-заголовки.
│  ├─ room/                           ← НОВОЕ (рефакторинг): ResolveRoom вынесен из internal/cli.
│  ├─ cli/    render/                 ← только nctalk, нетронуты по поведению
│  │
│  ╔══ граница удаления WebRTC (всё ниже — одним rm -rf) ══╗
│  ║ call/                                           ║
│  ║   signaling/  OCS signaling-клиент (polling)    ║
│  ║   peer/       pion-обёртка: PeerConnection,     ║
│  ║                SDP offer/answer, ICE            ║
│  ║   media/      интерфейсы AudioSource/AudioSink   ║
│  ║                + Opus-кодек-стратегия (ffmpeg)   ║
│  ║                + Mixer (сведение N входящих)     ║
│  ║   agent/      PCM-pipe реализация (stdin/stdout) ║
│  ║   interactive/ TUI + sox/ffmpeg subprocess       ║
│  ╚═════════════════════════════════════════════════╝
├─ cmd/nctalk-call/    ← НОВОЕ: точка входа «агент/pipe»
└─ cmd/nctalk-talk/    ← НОВОЕ: точка входа «человек/TUI»
```

> `internal/transport` и `internal/room` — рефакторинг фундамента, **не входят в
> `rm -rf` WebRTC** (см. §3): после отката звонков они остаются в дереве и
> переиспользуются базовым `nctalk`.

### Ответственность слоёв

- **`internal/transport`** — чистый HTTP-транспорт: интерфейс `httpDoer`, метод
  `doOCS` (сборка URL, Basic-auth + заголовки `OCS-APIRequest: true`/
  `Accept: application/json`, разворот OCS-конверта, возврат заголовков ответа),
  `sameHostRedirectPolicy` (блокировка cross-host auth-leak и https→http
  downgrade), `sanitizeErr` (redact URL). Потребители — `internal/client` и
  `internal/call/signaling`. **Не знает про WebRTC и про специфику ресурсов Spreed**
  (rooms/chat/call/signaling) — только HTTP и OCS-конверт.
- **`internal/room`** — вынесенный из `internal/cli/room.go` механизм `ResolveRoom`
  (разрешение `<room>`/`--name` → token, exit-коды `2`/`3`). Зависит от типов
  `internal/client` (через интерфейс, как раньше). **Потребители:** `internal/cli`
  (базовые команды) и `cmd/nctalk-call`/`cmd/nctalk-talk` (тонкие точки входа,
  без подтягивания cli-роутера и `render`).
- **`call/signaling`** — клиент internal-signaling: long-poll
  `/ocs/v2.php/apps/spreed/api/v4/signaling/{token}`, разбор `usersInRoom` и
  peer-сообщений (`offer`/`answer`/`candidate`), отправка своих сообщений.
  Зависит от `internal/transport` и `internal/config`. Ничего не знает про WebRTC-пир
  и аудио.
- **`call/peer`** — обёртка над `pion/webrtc`: создание `PeerConnection` на
  каждого удалённого участника, связывание SDP/ICE с `signaling`, создание
  исходящего audio-track, подписка на входящие треки (`OnTrack`), буферизация
  входящих ICE-candidates, perfect-negotiation (см. §7). Зависит от `pion` и
  `call/signaling`. Ничего не знает про формат звука (Opus/PCM) — работает через
  интерфейсы `media`.
- **`call/media`** — интерфейсы `AudioSource`/`AudioSink` (уровень Opus-фреймов),
  реализация codec-стратегии (ffmpeg-subprocess) **и `Mixer`** (сведение N
  входящих потоков в единый исходящий, см. §7/§9). Зависит от `os/exec`.
  Зависит от выбора ядра signaling/peer **только через интерфейсы** — сменa
  codec-стратегии (§8) не требует правок ядра.
- **`call/agent`** — источник/приёмник звука через stdin/stdout (PCM `s16le`/48к/
  моно), связка с `media` (PCM↔Opus). Зависит от `media`, `peer`, `signaling`.
- **`call/interactive`** — TUI (список участников, mute/leave, индикатор
  говорящего) + захват/вывод звука через `sox`/`ffmpeg` subprocess. Зависит от
  `media`, `peer`, `signaling`.
- **`cmd/nctalk-call` / `cmd/nctalk-talk`** — тонкие точки входа: читают env
  (через `internal/config`), резолвят room через `internal/room` (без подтягивания
  cli-роутера), связывают слои и крутят loop до сигнала/leave.

## 5. Конфиг и безопасность

Наследует правила базового `nctalk` (`CLAUDE.md`, спека 2026-07-17 §5):

- Те же env: `NEXTCLOUD_URL/LOGIN/PASS/TIMEOUT`. Креды — только из env, только в
  заголовке `Authorization`, никогда в URL/argv/логах.
- Все ошибки через существующий `sanitizeErr`; `CheckRedirect` (same-host,
  https→http даунгрейд заблокирован) — **переиспользуется без изменений**.
- Все HTTP-запросы с `context.Context` → Ctrl-C отменяет и signalling-poll, и
  peer-установку.

Дополнительно для звонков:

- **`NCTALK_AUDIO_DEVICE_IN` / `NCTALK_AUDIO_DEVICE_OUT`** (опц.) — аудио-устройства
  для `nctalk-talk`. `IN` передаётся в `ffmpeg -f avfoundation` как avfoundation-
  строка `":<audio_idx>"` (default `":0"`); `OUT` — в `ffmpeg -f audiotoolbox
  -audio_device_index <N>` как int-индекс или `-1` (default — системные default,
  подробности в §8). При `--audio-backend sox` оба игнорируются.
- **STUN/TURN** — берётся из capability сервера (`/ocs/.../signaling`-config /
  `spreed-*` capability), **не** из env пользователя. TURN-credentials (если нужны)
  приходят в той же signaling-config от сервера — клиент их не хранит и не логирует.
- **P2P ICE-кандидаты** содержат локальные/публичные IP — это норма для WebRTC, но
  они не пишутся в stderr/логи в отладке без явного `NCTALK_DEBUG_ICE=1`.

## 6. Команды

### `nctalk-call <room>` — режим «агент/pipe»

Headless: звук через stdin/stdout, без TUI, для скрипта/бота.

```sh
nctalk-call <room>                                  # stdin → в звонок; из звонка → stdout (PCM s16le/48к/моно)
nctalk-call <room> --in rec.pcm --out play.pcm      # ввод/вывод через файлы (оба опциональны и независимы)
nctalk-call <room> --out rec.pcm                    # только запись (listening-only, своего звука не шлём)
```

- `<room>` — token позиционно или `--name` (правило разрешения из базовой спеки §7
  **переиспользуется**; 1 совпадение → ок, >1 → exit `3`, 0 → exit `2`). Механизм
  `ResolveRoom` вынесен в `internal/room` (см. §4/§11).
- `--in <path>` / `--out <path>` — опциональны и независимы: по умолчанию ввод из
  stdin, вывод в stdout; можно задать только один (например, `--out` для записи
  звонка, без передачи своего звука).
- **Контракт PCM:** формат входа/вывода pipe/файла — **жёстко `s16le`/48 кГц/моно**.
  Валидации формата **нет**: ответственность пользователя подавать корректный
  PCM (документировано в `--help`). При неверном формате звук будет искажён, но
  процесс не упадёт. Будущая авто-детекция/конвертация через `ffmpeg` — в §13.
- **Флаги Call API зависят от `--in`/`--out`:**
  - только `--out` (listening-only, записи) → `flags = IN_CALL` без `WITH_AUDIO`
    (численное значение `=1`). SDP содержит только приёмную (`recvonly`) m-line
    аудио, отправной m-line нет; удалённые пиры знают, что мы не вещаем.
  - `--in` (или оба) → `flags = IN_CALL | WITH_AUDIO` (`=3`). SDP содержит
    двунаправленную (`sendrecv`) m-line аудио.
  - **Соответствие флагов и SDP m-line:** `WITH_AUDIO` включён ⇔ в SDP есть
    `send*` m-line аудио. Listeners не объявляют send-track, поэтому к ним не
    подключаются `OnTrack`-обработчики на стороне пиров (mesh короче).
- Поток: PCM из stdin → Opus (ffmpeg) → `AudioSource` → pion-track → WebRTC →
  удалённые участники; обратно — `OnTrack` → Opus → PCM (ffmpeg) → stdout.
- Завершение: `SIGINT`/`SIGTERM` или EOF на stdin → корректный leave
  (`DELETE /call/{token}`), закрытие peer'ов, exit `0`.
- Вывод в stderr: статусные строки (joined, участники, ошибки); stdout **только
  PCM** (чтобы pipe был чистым).

### `nctalk-talk <room>` — режим «человек/TUI»

Интерактивный: микрофон/динамик через `sox`/`ffmpeg`, TUI в терминале.

```sh
nctalk-talk <room>                   # TUI: список участников, M=mute, Q=leave, V=громкость
```

- Присоединение и leave — те же Call API, что у `nctalk-call`. В TUI-режиме оба
  направления активны всегда (mic есть, speaker есть), поэтому join всегда с
  `flags = IN_CALL | WITH_AUDIO` (`=3`), SDP — `sendrecv` m-line (правила из §6
  для `nctalk-call` тут вырождаются в этот единственный случай).
- Поток: mic → `sox`/`ffmpeg` (device→PCM) → Opus → track → WebRTC; обратно —
  WebRTC → `OnTrack` → Opus → PCM → `sox`/`ffmpeg` → speaker.
- **TUI:** список участников (с отметкой говорящего через VAD/уровень), хоткеи
  `M` (mute/unmute своего источника), `Q`/`Ctrl-C` (leave), `+`/`-` (громкость
  вывода). Минималистично, без видео.
- **Семантика `M` (mute):** прекратить `WriteSample` на свой исходящий track
  (локальный мьют источника). Call API `flags` **не меняются автоматически**:
  флаг `WITH_AUDIO` остаётся выставленным, send-m-line в SDP остаётся, просто
  отправляется тишина (на уровне источника — `media` не пишет фреймы в track).
  Второй уровень — `--mute-updates-flags` (снимать `WITH_AUDIO` при mute через
  `PUT /call/{token}`) — вынесен в future (§13). Mute-статус других участников
  приходит через signaling `usersInRoom` (поле `inCall` flags у каждого
  участника).
- Выбор audio-IO-backend: по умолчанию `ffmpeg` с macOS-native muxer'ами
  (`avfoundation` для input, `audiotoolbox` для output, см. §8); переключатель
  `--audio-backend sox|ffmpeg` (`sox` — fallback). Список доступных устройств:
  `ffmpeg -f avfoundation -list_devices true -i ""` (input) и
  `ffmpeg -f audiotoolbox -list_devices true -i ""` (output) — см. §8.

### Exit-коды

Наследуют базовый контракт (спека 2026-07-17 §7): `0` успех · `1` общая ошибка
(сеть/`401`/5xx) · `2` not found · `3` ambiguous. Дополнительно:

- **Корректный leave по сигналу/EOF** (`nctalk-call`) или `Q`/`Ctrl-C`
  (`nctalk-talk`) → exit `0` (это штатное завершение звонка, не ошибка).
- Не установилось ни одного peer-соединения за ICE-таймаут (~30с, настраивается
  `NCTALK_ICE_TIMEOUT`) → exit `1` с диагностикой (отличать «я один в звонке» —
  это `0`, от «никто не ответил ICE» — `1`).
- **Отказ всех peer'ов в mesh:** если peer'ы были установлены, но поочерёдно
  упали (DTLS/ICE-fail) и ни одного живого не осталось **по истечении
  `NCTALK_ICE_TIMEOUT`** → exit `1` с диагностикой «все peer-соединения
  потеряны». До истечения таймаута — продолжаем работу/ждём возвращения пиров или
  прихода новых участников (не падаем мгновенно от временного отвала).

## 7. Общее ядро: signaling и peer

### Signaling-клиент (`call/signaling`)

- Long-poll `GET /ocs/v2.php/apps/spreed/api/v4/signaling/{token}` (внутренний
  OCS-polling signaling, **версия `v4`** — та же, что у Call API; ранее в ранних
  drafts указывалась `v1`/без версии, что **некорректно** — такой версии в Spreed
  нет). На `2xx`/`304` (изменения или пусто) — обработка и **немедленный**
  повторный poll. Отправка сообщений — `POST` на тот же эндпоинт.
- Разбираемые сообщения (формат уточняется в spike по JS-коду `spreed`):
  - `usersInRoom` / список участников signaling'а → управление набором
    `PeerConnection`'ов в `peer`; здесь же приходят `inCall` flags каждого
    участника (mute-статус — §6);
  - `message` с `data.type`: `offer`, `answer`, `candidate` → ретранслируются в
    соответствующий `PeerConnection`.
- Исходящее: свой `offer`/`answer`/`candidate` от `peer` → POST в signaling.
- Мокируется интерфейсом транспорта (`internal/transport`/`httpDoer`) в
  unit-тестах.

#### Polling retry/backoff

- **`2xx`/`304`** → немедленный повторный poll (long-poll без задержки).
- **Сетевой сбой / timeout / обрыв / `5xx`** → retry с экспоненциальным backoff
  (база ~1с, потолок ~30с, без шума в stderr на каждой попытке; в лог — не чаще
  раза в 30с). Не выходим из loop'а, пока не `ctx.Done()`.
- **`401`/`403`** (не network, а auth/permissions) → exit `1` без retry
  (повторять бессмысленно: креды/права не изменятся от ретрая).
- **`404`** (комната/signaling исчезли) → exit `2` без retry.
- Любой выход из polling-loop по `ctx.Done()` (Ctrl-C / SIGTERM) инициирует
  штатный leave (`DELETE /call/{token}`).

### Peer-обёртка (`call/peer`)

- На каждого удалённого участника — своя `pion.PeerConnection` (P2P-mesh, до 7).
- На чужой `offer` → `SetRemoteDescription` → `CreateAnswer` → в signaling;
  `candidate`'ы — trickle через signaling (своя посылка и приём).
- Исходящий audio-track: `TrackLocalStaticSample` (кодек `audio/opus`); пишет
  Opus-фреймы из `AudioSource`.
- Входящие треки: `OnTrack` → `ReadSample` → Opus-фреймы в соответствующий
  `AudioSink`.
- ICE-серверы из signaling-config сервера; если TURN не настроен, а peer за NAT —
  фиксируется как диагностика в spike.

#### Буферизация ICE-candidates

Входящие `candidate` от signaling могут прийти **до** `SetRemoteDescription`
(pion в таком случае падает на `AddICECandidate`). Правило: входящие `candidate`
для peer'а, у которого ещё не было `SetRemoteDescription`, **буферизуются** в
очередь; сразу после успешного `SetRemoteDescription` очередь дренируется
(`AddICECandidate` для каждого). Аналогично для входящего `offer` после
`answer`-в--полёте (см. ниже — glare).

#### Perfect-negotiation (анти-glare)

При 3+ участниках возможен **glare**: оба пира одновременно шлют `offer`
(`CreateOffer` в обоих направлениях). Решение — **perfect-negotiation (W3C)** с
ролями `polite`/`impolite`:

- Роли назначаются **детерминированно** по сравнению `sessionId`/`actorId`
  пиров: меньший id = `impolite`, больший = `polite`. Оба пира приходят к
  одному и тому же распределению ролей без дополнительного обмена.
- При одновременных offer'ах (входящий пришёл, пока свой в полёте):
  - **polite**-пир откатывает свой offer (rollback `setLocalDescription`),
    принимает входящий;
  - **impolite**-пир игнорирует входящий, свой offer уже уйдёт и будет
    обработан polite-стороной.
- Реализация этой логики — в `call/peer`, не в signaling; Talk-JS (spreed web
  client) реализует ту же схему — повторяем рабочий эталон.

#### Mixdown входящего аудио для mesh

В mesh каждый удалённый участник пишет в свою `PeerConnection`; входящее аудио
приходит с **N пиров одновременно** (N обработчиков `OnTrack`, по одному на
peer). Правило сведения:

- Каждый входящий поток разбирается независимо: `OnTrack → ReadSample (raw
  Opus) → media.PCMDecoder → PCM-s16le-буфер` (свой `AudioSink`/буфер на пира).
- Все N PCM-буферов сводятся `media.Mixer` в **единый PCM-микс** (простое
  суммирование отсчётов s16 с **насыщением/clipping** на ±32767 для избежания
  переполнения), уже который идёт на вывод (stdout для `nctalk-call`, speaker
  через ffmpeg/sox для `nctalk-talk`).
- Микширование **локализовано в `call/media.Mixer`** — signaling и peer о нём не
  знают: они только доставляют Opus-фреймы в свой `AudioSink`. Это позволяет
  тестировать Mixer отдельно (unit-тесты на clipping, latency, переполнение).
- Альтернатива «N выходных PCM-потоков» (без mixdown) отклонена: pipe/speaker
  ждут один PCM-поток, а N параллельных выводов на устройство невозможны.

## 8. Opus-кодек-стратегия

`pion` не содержит Opus-кодека. Варианты:

| Вариант | CGO | Что делает | Риск |
|---|---|---|---|
| **(a) ffmpeg-subprocess** (выбрано для spike/MVP) | нет | codec Opus↔PCM **и** audio-IO в одном/нескольких процессах `ffmpeg` по pipe | средний: framing raw Opus ↔ OGG-контейнер (см. ниже), тайминги |
| (b) CGO libopus (`hraban/opus`) | да | только codec; IO отдельно | ломает `CGO_ENABLED=0` |
| (c) pure-Go Opus | нет | codec на Go | высокий: незрелые libs |

### Framing raw Opus ↔ контейнер (главный под-риск варианта (a))

`pion` работает на уровне Sample API: `TrackLocalStaticSample.WriteSample` ждёт
**один raw Opus-пакет** за вызов (~20 мс), `ReadSample` отдаёт raw Opus. А
`ffmpeg -f opus` пишет **контейнер OGG** (страничная рамка с заголовками), и
обратный декодер `ffmpeg -f opus -i -` ждёт OGG на входе. Прямое перенаправление
pipe → `WriteSample` (или `ReadSample` → `ffmpeg`) **не работает**. codec-стратегия
должна решить framing:

- **(a1) Pure-Go OGG-парсер с обеих сторон** — пишем минимальный OGG-демультиплексор
  на stdlib (`internal/call/media/ogg`), режем OGG-страницы ffmpeg-encode-вывода на
  отдельные Opus-пакеты → `WriteSample`. В обратную сторону — оборачиваем raw
  Opus из `ReadSample` в OGG-страницы перед подачей в `ffmpeg`-decode-input.
- **(a2) Формат, который ffmpeg пишет raw-пакетами** — напр. `matroska`/`webm`
  тоже контейнерные; чистого «raw Opus без контейнера» ffmpeg в stdout не умеет
  (по крайней мере без кастомных muxer-флагов). Это делает (a1) предпочтительным.
- **(a3) Обход через RTP** — `ffmpeg -f rtp` пишет Opus в RTP-пакетах; pion умеет
  принимать RTP напрямую (`TrackLocalStaticRTP`). Рассматривается как второй
  кандидат в spike, если OGG-парсер окажется сложнее ожидаемого.

**Spike должен доказать** как минимум один рабочий путь (a1 или a3) — это и есть
 проверка риска 2 из §1.

### Решение

По умолчанию **(a)** с подвариантом **(a1)** (pure-Go OGG-парсер в `call/media`).
Команды ffmpeg:

- **encode (nctalk-call, режим pipe):**
  ```
  ffmpeg -f s16le -ar 48000 -ac 1 -i - -c:a libopus -application voip -f opus -
  ```
  PCM из pipe → Opus в OGG-контейнере в pipe → `media/ogg` режет на raw Opus-пакеты
  → `AudioSource.WriteSample`.
- **decode (nctalk-call, режим pipe):**
  ```
  ffmpeg -f opus -i - -f s16le -ar 48000 -ac 1 -
  ```
  `ReadSample` (raw Opus) → `media/ogg` оборачивает в OGG → ffmpeg-stdin →
  PCM s16le/48к/моно на stdout.

Связка с pion — через Sample API (`ReadSample`/`WriteSample`), без ручной
RTP-упаковки. Если в spike связка Sample↔OGG-парсер окажется неподъёмной — fallback
на (a3); если непреодолим и он — fallback на (b) CGO libopus; если абсолютный
`CGO=0` критичнее звона — выпиливание (§12).

### ffmpeg-процессы для `nctalk-talk`

В TUI-режиме нет stdin/stdout-pipe: устройство → device. Принцип — **2
ffmpeg-процесса на направление** по схеме «capture+encode в одном процессе» /
«decode+play в одном». **Важно (review finding #1, CRITICAL):** input и output
используют **разные muxer'ы** — `avfoundation` (input-only) для захвата и
`audiotoolbox` (output-only) для вывода:

- **mic → Opus (capture+encode в одном процессе):**
  ```
  ffmpeg -f avfoundation -i "<NCTALK_AUDIO_DEVICE_IN или :0>" \
         -ac 1 -ar 48000 -c:a libopus -application voip -f opus -
  ```
  avfoundation-capture → PCM в процессе → libopus-encode → Opus в OGG в pipe →
  `media/ogg` → `AudioSource`. Дефолтный вход `:0` — первый системный input
  (микрофон); вывода нет, только вход. Адресация устройства — avfoundation-строка
  формата `":<audio_idx>"`.
- **PCM → speaker (вывод через audiotoolbox):** Mixer всегда выдаёт **PCM s16le**
  (контракт `agent.Run.Stdout`), поэтому playback-ffmpeg ест PCM, а не Opus:
  ```
  ffmpeg -f s16le -ar 48000 -ac 1 -i - -f audiotoolbox -audio_device_index <N> -
  ```
  `NCTALK_AUDIO_DEVICE_OUT` — int-индекс (default `-1` = системное устройство
  вывода). `avfoundation` как output НЕ работает (input-only muxer).
- **Список устройств — отдельные команды для input/output** (different muxers,
  разные способы адресации):
  ```
  ffmpeg -f avfoundation  -list_devices true -i ""   # input (avfoundation-строка ":<audio_idx>")
  ffmpeg -f audiotoolbox  -list_devices true -i ""   # output (int -audio_device_index <N>)
  ```
- **fallback на sox** (если avfoundation/audiotoolbox недоступен или `--audio-backend sox`):
  capture — `rec -q -r 48000 -c 1 -b 16 -e signed-encoding -t raw -`; playback —
  `play -q -r 48000 -c 1 -b 16 -e signed-encoding -t raw -`.

### Локализация риска

Выбор codec-стратегии инкапсулирован в `call/media` за интерфейсами
`AudioSource/AudioSink`. Переход (a1)→(a3)→(b) затрагивает только `media`, не
трогая `signaling`/`peer`/режимы. Контракт PCM на границе режимов (`call/agent`,
`call/interactive`) зафиксирован: **s16le/48к/моно, без валидации** (см. §6).

## 9. Поток данных

**Режим «агент» (`nctalk-call`), mesh с N участниками на приёме:**
```
stdin (PCM s16le/48к/моно) ─► ffmpeg(enc→OGG/Opus) ─► media/ogg ─► AudioSource ─► pion ─► WebRTC ─► N remotes

remote_1 ─► WebRTC ─► pion.OnTrack ─► ReadSample(Opus) ─┐
remote_2 ─► WebRTC ─► pion.OnTrack ─► ReadSample(Opus) ─┤
  …                                                    ├─► media.Mixer (sum + clip s16) ─► ffmpeg(dec) ─► stdout (PCM)
remote_N ─► WebRTC ─► pion.OnTrack ─► ReadSample(Opus) ─┘
```

`media.Mixer` (§7) сводит N входящих PCM-буферов в один, с насыщением/clipping
на ±32767; для `nctalk-call` вывод единственного микса в stdout сохраняет контракт
«pipe чистый, только PCM».

**Режим «человек» (`nctalk-talk`), 2 ffmpeg-процесса на направление:**
```
mic ─► ffmpeg(capture+enc: avfoundation→OGG/Opus) ─► media/ogg ─► AudioSource ─► pion ─► WebRTC ─► N remotes

remote_1..N ─► OnTrack ─► ReadSample ─► media.Mixer ─► PCM ─► ffmpeg(dec+play: PCM→audiotoolbox) ─► speaker
TUI: участники · M=mute · Q=leave · +/−=громкость · индикатор говорящего
```
Mixer выдаёт PCM s16le (контракт `agent.Run.Stdout`); playback-ffmpeg ест PCM и
пишет в устройство через `-f audiotoolbox -audio_device_index <N>` (`avfoundation`
input-only, как output НЕ работает — review finding #1).

В обоих режимах `SIGINT`/`SIGTERM` = корректный leave (`DELETE /call/{token}`) с
гарантированной очисткой peer'ов и subprocess'ов (ffmpeg-процессы — через
`context.Cancel` + `cmd.Wait`, без orphan-процессов).

## 10. Обработка ошибок

- Сеть/HTTP/`401`/5xx (на Call API и разовых запросах) → exit `1` + понятное
  сообщение (наследует базовый клиент, redact через `sanitizeErr`).
- Call API `403` (read-only / нет прав на старт звонка) → exit `1` с сообщением
  сервера; `404` (комната) → exit `2`; `412` (lobby) → exit `1` с пояснением.
- **Signaling long-poll:** отдельная стратегия retry/backoff, **не выходит в
  exit** на transient-сбоях (см. §7 «Polling retry/backoff»): 5xx/timeout → retry
  с backoff; `401`/`403` → exit `1` без retry; `404` → exit `2`. Только `ctx.Done`
  (Ctrl-C) штатно завершает polling-loop.
- Не удалось поднять ни один peer (ICE timeout) → exit `1`; «я один в звонке» →
  `0` (работаем, ждём других). Если peer'ы были, но поочерёдно упали и ни одного
  живого не осталось по истечении `NCTALK_ICE_TIMEOUT` → exit `1` (см. §6).
- `ffmpeg`/`sox` отсутствуют или упали → exit `1` с указанием, какой backend и
  как установить; не падать молча.
- WebRTC-fatal (DTLS/ICE-fail на конкретном peer) → лог в stderr + продолжаем
  работу с оставшимися участниками (mesh переживает отказ одного peer).

## 11. Тестирование

В духе базового проекта (`CLAUDE.md`):

- **`internal/transport`** — рефакторинг перенесён механически, без изменения
  поведения: регресс-тест `TestRun_PasswordDoesNotLeak_CrossHostRedirect` (базовый
  клиент, `NEXTCLOUD_PASS=SECRET_MARKER`) и тесты redirect-политики/`sanitizeErr`
  остаются зелёными и работают для обоих потребителей (`internal/client` и
  `call/signaling`).
- **`internal/room`** — `ResolveRoom` перенесён из `internal/cli/room.go`
  без изменения контракта (1 → ок, >1 → exit `3`, 0 → exit `2`); тесты
  переезжают вместе с кодом, поведение базовых команд `nctalk` не меняется
  (проверяется их существующими тестами).
- **`call/signaling`** — против мока транспорта (фикстуры реальных signaling-
  сообщений, собранные на боевом в spike); проверка разбора `usersInRoom`/
  `offer`/`answer`/`candidate`, корректность исходящих POST, retry/backoff
  стратегия (5xx → retry, `401`/`403` → exit `1` без retry, `404` → exit `2`).
- **`call/peer`** — два инстанса `peer` в одном тесте связываются напрямую (loop),
  без сети: проверка обмена SDP/ICE, буферизации `candidate` до `SetRemoteDescription`,
  perfect-negotiation (glare-scenarios с имитацией одновременных offer'ов) и
  прохождения Opus-фреймов через `media`-мок.
- **`call/media`** — на тестовых Opus/PCM-файлах: round-trip PCM→Opus→PCM через
  ffmpeg-subprocess + OGG-парсер (требует установленного `ffmpeg` в окружении
  теста; пропуск через `t.Skip`, если отсутствует).
- **`call/media.Mixer`** — unit-тесты на сведение N PCM-буферов: проверка
  насыщения/clipping при переполнении s16, поведение на пустом наборе, на
  одном источнике, latency-бюджет.
- **`call/agent`** — подача PCM на stdin, проверка PCM на stdout через фейковый
  peer (мок `AudioSource/Sink`), проверка listening-only режима (`--out` без
  `--in`, нет send-track).
- **Интеграционные** — за build-тегом `integration` (не входят в `go test ./...`),
  требуют env-кредов; реальный звонок — под флагом `NCTALK_INTEGRATION_CALL=1` +
  `NCTALK_INTEGRATION_ROOM` (по аналогии с `NCTALK_INTEGRATION_SEND`). Документация
  дописывается в `docs/integration-run.md`.
- Все сборки/тесты — под `CGO_ENABLED=0`.

## 12. Поэтапный план (spike-first, с точкой выпиливания)

1. **Spike** (отдельная ветка, можно выкинуть): `call/signaling` (polling + разбор)
   + `call/peer` (pion, один участник) + `call/media` (ffmpeg codec + OGG-парсер
   + Mixer) + `call/agent` (pipe-режим). Цель: **доказать, что аудио идёт между
   `nctalk-call` и звонком в браузере.** Проверяет все три ключевых риска
   (signaling-протокол + framing Opus/OGG + Opus-codec без CGO). Не взлетело →
   `rm -rf`, вывод «WebRTC-аудио к Talk в Go не окупается», **стоп**.
2. **Модуль агента** (`nctalk-call`) в полноценный вид: TUI нет, только pipe;
   unit-тесты + e2e на боевом за build-тегом. Только если spike взлетел.
3. **Модуль человека** (`nctalk-talk`): TUI + `sox`/`ffmpeg` device-IO поверх того
   же ядра.
4. *(опционально, позже)* нативная CoreAudio вместо subprocess; HPB/SFU; расширение
   платформ — отдельные модули, не блокируют п.1–3.

Точка выпиливания на каждом шаге: п.1 не взлетел — выкидываем всё. После п.2/п.3
можно выкинуть один из режимов, не трогая другой (благодаря общему ядру + разным
`cmd/`).

## 13. Будущее (явно за рамками)

- Видео (video-track, захват камеры, рендер кадра в pipe/файл для агента).
- HPB/SFU для звонков >7 участников (standalone signaling-сервер по WebSocket +
  Janus) — отдельная реализация `call/signaling`, интерфейс позволит не трогать
  `peer`/`media`.
- Windows/Linux (другие audio-backends — ALSA/PulseAudio/PipeWire).
- Нативная CoreAudio через CGO как опция качества (меньше задержка) за build-тегом
  — если subprocess-вариант упрётся в latency.
- Запись/транскрипция звонка как надстройка над pipe-режимом (`nctalk-call |
  whisper-…`) или серверная (Vosk live-transcription app).
- AEC/шумоподавление в процессе (сейчас — на стороне ОС/устройства).
- **`--mute-updates-flags`** (второй уровень mute, §6): при mute снимать `WITH_AUDIO`
  через `PUT /call/{token}`, чтобы пиры знали об отключении на уровне Call API, а
  не только по отсутствию звука. Сейчас mute — локальный (тишина на источнике).
- **Авто-детекция/конвертация входного PCM** через `ffmpeg`-прелоад (если входной
  формат не `s16le/48к/моно`) — сейчас контракт pipe жёсткий, валидации нет (§6/§8).
