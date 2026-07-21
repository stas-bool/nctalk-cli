# Дизайн: `nctalk-talk` — интерактивный TUI для аудио-звонков (Этап 4)

## 0. Контекст и связи

Это **конкретизация Этапа 4** базовой звонковой спеки
`docs/superpowers/specs/2026-07-19-nctalk-call-design.md`. Базовая спека описывает
`nctalk-talk` высокоуровнево (§6 команда, §8 ffmpeg-процессы, §9 поток данных) и
оставляет открытыми ряд архитектурных решений. Здесь эти решения зафиксированы по
итогу brainstorming-сессии (2026-07-21), исправлено противоречие device-out (§3),
и определён точный объём вторжения в готовый `agent.Run`.

Ссылки:
- Базовая спека: `2026-07-19-nctalk-call-design.md` (§6 команды, §8 codec/device-IO,
  §9 потоки данных, §10 exit-коды, §13 future).
- План: `docs/superpowers/plans/2026-07-19-nctalk-call-design.plan.md`, Этап 4
  (~строка 1101+, Tasks 4.1–4.4 — набросок; настоящая спека его уточняет/заменяет).
- State: `docs/session-state/2026-07-21-nctalk-calls.md` — Этапы 0-3 завершены,
  `agent.Run` production-hardened, spike-gate пройден в обоих направлениях.

**Ветка:** `feat/nctalk-calls`. **Не в `main`** (spike-first, точка отката — §11).

## 1. Цель и scope

Реализовать интерактивный режим «человек в терминале»: зайти в комнату Talk,
говорить в микрофон и слышать других через динамик, с экранным управлением.

**Scope — полный (базовая спека §6):**
- Список участников (из signaling `usersInRoom`), с пометкой mute и «говорит».
- Хоткеи: `M` (мьют своего источника), `Q`/`Ctrl-C` (выйти), `+`/`−` (громкость вывода).
- Индикатор говорящего через уровень PCM (VAD) per-peer.

**Не входит (future, §13):** bubbletea-надстройка, `sox`/Linux-ALSA backend,
`--mute-updates-flags` (снимать `WITH_AUDIO` через Call API), self-VAD (свой
«говорит»), видеo.

## 2. Решения brainstorming-сессии (сводка)

| Развилка | Решение |
|---|---|
| TUI-библиотека | `golang.org/x/term` (raw-mode + размер терминала) + ручная ANSI-отрисовка. Вторая не-stdlib зависимость после pion. |
| TUI-граница | Узкий `View`-интерфейс в `interactive`; `ansiView` (сейчас) и `bubbleteaView` (future) — сменные реализации. |
| №1 Оркестрация | **Подход A** — переиспользовать `agent.Run`, расширив `agent.Config` опциональными полями. `interactive` остаётся тонким. |
| №3 Mute/громкость | **Вариант A** — wrapper-interceptors в `interactive` (`muteSource` на вход, `volumeWriter` на выход). `agent.Run` под них не трогаем. |
| №4 VAD/состояние | **Вариант A** — один `OnState`-callback в `agent.Config`: agent пушит участников + per-peer уровни (raw); `interactive` обогащает своим mute/volume и гонит в `View`. |
| №5 Терминал | **Вариант A** — TUI владеет экраном (alternate screen + raw-mode), весь технический вывод → `call.log`. |

## 3. Исправление противоречия базовой спеки по device-out

Базовая спека внутренне противоречива:
- §8 рисует playback как `ffmpeg -f opus -i - -f avfoundation "<device>"` (ест **Opus**).
- §9 (поток данных) и реальный `agent.go` дают на выходе Mixer'а **PCM s16le**, не Opus.

**Канон (фиксируем здесь):** Mixer всегда выдаёт PCM s16le/48к/моно (это контракт
`agent.Run.Stdout`). Значит device-out ест PCM:
```
ffmpeg -f s16le -ar 48000 -ac 1 -i - -f avfoundation "<NCTALK_AUDIO_DEVICE_OUT или :0>"
```
Следствие (важное): device-out — **не** `media.AudioSink`, а просто `io.Writer`
(stdin процесса playback-ffmpeg), который подставляется как `agent.Config.Stdout`.
Базовую спеку §8/§9 поправить под этот канон (отдельным коммитом, см. §12).

## 4. Архитектура

Четыре компонента + минимальное расширение `agent.Run`. DAG как в базовой спеке:
`media ← peer ← agent ← interactive`; `agent` НЕ импортирует `interactive`,
`interactive` импортирует `agent`/`media`. Изоляция от pion сохраняется
(`TestCmdNctalkDoesNotDependOnPion`).

```
cmd/nctalk-talk/main.go          ← точка входа (тонкая, как cmd/nctalk-call)
        │
        ▼
internal/call/interactive/
   ├─ interactive.go   Run(ctx, Config) — оркестратор
   ├─ audio_io.go      MicSource (avfoundation→Opus), speakerWriter (PCM→avfoundation)
   ├─ mute.go          muteSource — wrapper media.AudioSource
   ├─ volume.go        volumeWriter — wrapper io.Writer (PCM gain)
   ├─ tui.go           ansiView — реализация View на x/term
   ├─ view.go          типы Event/CallState/View + View-контракт
   └─ doc.go           (есть)
```

### 4.1. Что меняем в `agent.Run` (подход A) — ровно две additive-точки

Текущий `agent.Config` (см. `internal/call/agent/agent.go`) получает два опциональных
поля. При незаданных — поведение `nctalk-call` идентично текущему (backward compat,
регрессию ловят существующие 17 agent-тестов).

```go
type Config struct {
    // ... существующие поля без изменений ...
    Stdin      io.Reader
    Stdout     io.Writer
    NewEncoder encoderFactory // func(io.Reader) (media.AudioSource, error)
    NewDecoder decoderFactory // func(io.Writer) (media.AudioSink, error)
    // ...

    // НОВОЕ (Этап 4). Оба опциональны; nil → текущее поведение nctalk-call.

    // AudioIn — готовый источник Opus-пакетов. Если задан — используется
    // encodeLoop'ом ВМЕСТО NewEncoder(Stdin). Stdin при этом игнорируется.
    // Для nctalk-talk сюда подаётся muteSource{device-MicSource}.
    // Для nctalk-call — nil (как раньше, NewEncoder(Stdin)).
    AudioIn media.AudioSource

    // OnState — periodic-снапшот состояния звонка (участники + per-peer уровни,
    // raw). Agent вызывает раз в ~100мс из throttle-горутины. nil → не вызывается
    // (nctalk-call не нуждается). Тип CallState определён в agent (см. 4.5).
    OnState func(CallState)
}
```

Локальные правки в `agent.go`:
1. **Step 2 (encoder setup):** при `InFlags==3` (sendrecv) источник выбирается так: `if cfg.AudioIn != nil { src = cfg.AudioIn } else { src, err = cfg.NewEncoder(cfg.Stdin) }`. При `InFlags==1` (recvonly) `AudioIn` игнорируется (encoder не запускается, как и сегодня). encodeLoop далее как прежде.
2. **`pcmMixerWriter.Write`:** копит RMS уровня для своего `idx` (running window ~50мс) в общий state-коллектор.
3. **`reconcile`:** при изменении users — обновляет participants-карту в state-коллекторе (sessionId→{name, muted}).
4. **state-throttle-горутина** (новая, запускается если `OnState != nil`): раз в `stateTickPeriod` (~100мс) собирает `CallState{Participants: [...]}` из коллектора и зовёт `cfg.OnState`. Останавливается по ctx.

Это **всё** вторжение в `agent.Run`. Mute, громкость, VAD-гистерезис, отрисовка —
в `interactive`.

### 4.2. `audio_io.go` — device-IO через ffmpeg+avfoundation

```go
// MicSource — микрофон → raw Opus. Реализует media.AudioSource.
// Один ffmpeg-процесс «capture+encode»: avfoundation → libopus → OGG/Opus в pipe → ogg-парсер → ReadSample.
// НЕ требует PCM-stdin (вход — устройство, не pipe).
type MicSource struct { /* cmd, ogg reader, ... */ }
func NewMicSource(ctx context.Context, device string) (*MicSource, error)
// Команда: ffmpeg -f avfoundation -i "<device>" -ac 1 -ar 48000 -c:a libopus -application voip -f opus -
// device default ":0"; из env NCTALK_AUDIO_DEVICE_IN.
// Реализует ReadSample() (как FFmpegEncoder, делит ogg-парсинг) + Close() (kill ffmpeg).

// speakerWriter — PCM s16le → динамик. Просто io.Writer (НЕ AudioSink).
// stdin процесса: ffmpeg -f s16le -ar 48000 -ac 1 -i - -f avfoundation "<device>"
// device default ":0"; из env NCTALK_AUDIO_DEVICE_OUT.
func NewSpeakerWriter(ctx context.Context, device string) (io.WriteCloser, *exec.Cmd, error)
```

MicSource структурно близок к существующему `FFmpegEncoder` (cmd + ogg.Reader + pump),
но ffmpeg захватывает устройство вместо чтения PCM-stdin. Переиспользуем `media/ogg`
(парсер OpusHead/comment обязателен — инвариант базовой спеки).

**SoX-fallback** (`--audio-backend sox`) — **future** (§13): в sox-режиме device-IO
даёт PCM (а не Opus), т.е. сводится к pipe-схеме `nctalk-call` (PCM `io.Reader`/`Writer`
через существующие `FFmpegEncoder`/`FFmpegDecoder`). MVP — только avfoundation.

### 4.3. Wrappers в `interactive` (mute + громкость)

```go
// muteSource — перехватывает media.AudioSource. При muted — крутит inner.ReadSample
// и ДРОПАЕТ payload (не возвращает), => encodeLoop не зовёт WriteSample => pion
// перестаёт слать RTP. Ровно семантика мьюта базовой спеки §6 ("перестать WriteSample").
type muteSource struct {
    inner media.AudioSource
    muted atomic.Bool // крутит TUI-хоткеем M
}
func (m *muteSource) ReadSample() ([]byte, time.Duration, error) {
    for {
        p, d, err := m.inner.ReadSample()
        if err != nil { return nil, 0, err }
        if !m.muted.Load() { return p, d, nil }
        // muted: drain inner (не блокируем устройство), loop без возврата
    }
}

// volumeWriter — перехватывает io.Writer (PCM s16le на вывод). Масштабирует
// каждый int16-семпл на gain (×gain/100), с clipping на ±32767, перед inner.Write.
// Применяется к PCM ПОСЛЕ Mixer'а, перед playback-ffmpeg => влияет только на динамик.
type volumeWriter struct {
    inner io.Writer
    gain  atomic.Int32 // %, default 100; крутит TUI +/-
}
```

Связка с `agent.Config`: `AudioIn = &muteSource{inner: micSource}`,
`Stdout = &volumeWriter{inner: speakerWriter}`. Agent не знает про mute/volume.

### 4.4. `View`-контракт (`view.go`) — точка расширения bubbletea

```go
type Event int
const (
    EvMuteToggle Event = iota
    EvLeave
    EvVolUp
    EvVolDown
)

type ParticipantState struct {
    SessionId string
    Name      string
    Muted     bool
    Level     int  // 0..100, нормализованный RMS (raw из agent)
    Speaking  bool // гистерезис в interactive, не в agent
}

type CallState struct {
    Participants []ParticipantState
    SelfMuted    bool
    Volume       int    // %
    Status       string // "joining…", "joined", "one peer failed: …", …
}

type View interface {
    Update(state CallState)       // interactive пушит (в горутине отрисовки)
    Events() <-chan Event         // TUI пушит ввод (M/Q/+/-)
    Close() error                 // Restore tty
}
```

`Event`/`CallState` **без терминал-специфики** (ни ANSI, ни `tea.*`). Поэтому
`bubbleteaView` (future) — drop-in: `Update`→`program.Send`, `tea.KeyMsg`→`Event`,
рендер tea-Model из `CallState`. Adapter живёт внутри `bubbleteaView`, типы `tea.*`
не пересекают границу.

### 4.5. `CallState` из `agent` (raw) vs обогащение в `interactive`

Чтобы `agent` не импортировал `interactive`, **raw-тип живёт в `agent`**:
```go
// agent.CallState — только то, что agent знает.
type Participant struct {
    SessionId string
    Name      string        // displayName/userId из signaling.User
    Muted     bool          // из inCall-flags участника
    Level     int           // 0..100, нормализованный RMS (raw)
}
type CallState struct{ Participants []Participant }
```
`interactive` принимает `agent.CallState` в `OnState`, **обогащает** своим
`SelfMuted`/`Volume`/`Status`, считает гистерезис `Speaking` (порог включения >
порог выключения, чтобы метка не мигала), и пушит `interactive.CallState` в `View`.
Так `agent` остаётся свободным от TUI-концептов («говорит», «громкость»).

### 4.6. `interactive.Run` — оркестратор

```go
func Run(ctx context.Context, cfg Config) error
type Config struct {
    Cfg         config.Config
    Token       string
    Signaling   *signaling.Client   // проброс в agent.Config.Signaling
    ICEServers  []webrtc.ICEServer
    OwnUserId   string
    OwnSessionId string
    ICETimeout  time.Duration
    DeviceIn    string              // NCTALK_AUDIO_DEVICE_IN (":0")
    DeviceOut   string              // NCTALK_AUDIO_DEVICE_OUT (":0")
    LogFile     io.Writer           // call.log (stderr агента)
    View        View                // инъекция (тесты — fake; прод — ansiView)
    Now         func() time.Time    // тесты
}
```
Поток внутри `Run`:
1. Создать `MicSource` (audio_io), `speakerWriter` (audio_io).
2. Обернуть: `audioIn = &muteSource{inner: mic}`, `stdout = &volumeWriter{inner: speaker}`.
3. Создать `View` (если не задан — `ansiView`).
4. Запустить горутну: `agent.OnState` → обогатить (mute/volume/status + гистерезис Speaking) → `view.Update`. Throttle на стороне agent уже есть (~100мс), дополнительно дедуплируем одинаковые снапшоты в interactive (не перерисовываем, если состояние не изменилось).
5. Запустить горутну event-loop: `select { case ev := <-view.Events(): применить (EvMuteToggle→mute.muted.Toggle; EvVolUp/Down→volume.gain; EvLeave→cancel) }`.
6. `agent.Run(ctx, agent.Config{ AudioIn: audioIn, Stdout: stdout, Stderr: cfg.LogFile, OnState: onState, InFlags: 3 /*sendrecv всегда*/, ... })`.
7. По выходу: `view.Close()` (Restore tty), закрыть MicSource/speakerWriter (kill ffmpeg).

`InFlags = 3` всегда (sendrecv): спека базовая §6 — в TUI-режиме оба направления активны, SDP `sendrecv`. `--recvonly` **не** предусматривается (это прерогатива `nctalk-call`).

### 4.7. `cmd/nctalk-talk/main.go` — точка входа

Минимальный, зеркально `cmd/nctalk-call/main.go`:
`config.Load → room.ResolveRoom → weblogin.Login → capability.Settings → signaling.New → JoinRoom → parseDurationEnv(NCTALK_ICE_TIMEOUT) → signal.NotifyContext(SIGINT,SIGTERM) → interactive.Run`. Exit-коды — те же, что у `nctalk-call` (базовая §10).

Флаги: позиционный `<room>` (token), `--name <подстрока>`. `NCTALK_AUDIO_DEVICE_IN/OUT` — env (default `:0`). Логи → `call.log` (см. §6). **stdin/stdout процесса `nctalk-talk` = терминал** (TUI raw-mode + отрисовка); микрофон и динамик — **отдельные ffmpeg-субпроцессы** (не stdin/stdout), см. §4.2/§5.

## 5. Поток данных (device-режим, канон)

```
микрофон ─► ffmpeg(avfoundation→libopus→OGG/Opus) ─► media/ogg ─► MicSource ─► muteSource ─► [encodeLoop в agent]
                                                                                                        │
                                                                                                        ▼
                                                                     ОДИН audioTrack ─► pion ─► WebRTC ─► N remotes

remote_1..N ─► OnTrack ─► [decoder-per-peer в agent: Opus→PCM] ─► pcmMixerWriter(+RMS) ─► Mixer ─► PCM ─► volumeWriter ─► ffmpeg(PCM→avfoundation) ─► динамик
                                                  │
                                                  └─► RMS-коллектор ─► OnState(CallState) ─► interactive(обогащение) ─► View.Update ─► экран

экран ─► клавиши M/Q/+/- ─► View.Events ─► interactive-event-loop ─► muteSource.muted / volumeWriter.gain / ctx-cancel(leave)
```

`SIGINT`/`SIGTERM`/`Q` → cancel ctx → `agent.Run` штатный leave (`DELETE /call/{token}`, best-effort 3с) + гарантированный kill ffmpeg (capture + playback) + Restore tty.

## 6. Терминал и логи (развилка №5, вариант A)

- **TUI владеет экраном:** alternate screen (`\x1b[?1049h` на старте, `\x1b[?1049l` в `defer`/`Close`), raw-mode (`x/term.MakeRaw`/`Restore`), отрисовка на `os.Stdout`, ввод с `os.Stdin`. Redraw по `View.Update` (полная перерисовка — alternate screen без скролла).
- **Логи → `call.log`:** `agent.Config.Stderr = callLogFile` (открывается в `cmd/nctalk-talk`, путь `./call.log` или `$NCTALK_CALL_LOG`). Все статусные/ошибочные строки agent'а и interactive — туда. После выхода (`Restore` tty) — одна строка в настоящий stderr: `nctalk-talk: подробности в call.log`.
- **Resize:** `signal.Notify(SIGWINCH)` → `term.GetSize` → следующий `View.Update` перерисует под новый размер (список участников обрезается/скроллится по высоте).

## 7. Семантика mute / громкость / VAD

- **`M` (mute):** `muteSource.muted.Toggle()` — прекратить отправку Opus (RTP прекращается, удалённая сторона слышит тишину). Call API `flags` **не меняются** (`WITH_AUDIO` остаётся, SDP `sendrecv` остаётся) — локальный мьют источника. `--mute-updates-flags` (снимать `WITH_AUDIO` через `PUT /call/{token}`) — future (базовая §13). Собственный mute-статус показывается в TUI; mute-статус других — из signaling `usersInRoom` (поле `inCall` flags).
- **`+`/`−` (громкость):** `volumeWriter.gain` ±10% (диапазон 0–200%, default 100). Влияет только на локальный динамик (в сеть уходит как было). Значение показывается в TUI.
- **VAD «говорит»:** `Speaking = true` при `Level ≥ ON` (напр. 15), `false` при `Level < OFF` (напр. 8) — гистерезис, пороги константы в `interactive` (тюнинг — future). `Level` — нормализованный RMS из `pcmMixerWriter` (0..100).

## 8. Команды и флаги

```
nctalk-talk <room>                 # TUI: участники, M=mute, Q=leave, +/−=громкость
nctalk-talk --name <подстрока>     # разрешить комнату по имени (как nctalk-call)
```
Env (помимо `NEXTCLOUD_*`): `NCTALK_AUDIO_DEVICE_IN`, `NCTALK_AUDIO_DEVICE_OUT`
(default `:0`), `NCTALK_ICE_TIMEOUT`, `NCTALK_CALL_LOG` (default `./call.log`).

Список avfoundation-устройств — отдельно пользователем:
`ffmpeg -f avfoundation -list_devices true -i ""` (базовая §8).

## 9. Exit-коды

Наследуют `nctalk-call` (базовая §10): `0` — штатный выход (`Q`/`Ctrl-C`/`SIGINT` =
корректный leave, **не** ошибка); `1` — общая ошибка / все peer'ы упали за ICE-таймаут;
`2` — not found; `3` — ambiguous. Маппинг `exit.ExitError` (value-тип, инвариант
базовой спеки) — как в `cmd/nctalk-call/main.go`.

## 10. Точка отката

Если device-IO (avfoundation) или TUI нестабильны и не чинятся в разумный срок —
`rm -rf cmd/nctalk-talk internal/call/interactive && go mod tidy` + откат опциональных
полей `agent.Config` (`AudioIn`, `OnState`) и локальных правок (`pcmMixerWriter` RMS,
state-throttle). `cmd/nctalk-call` и `agent.Run` (pipe-режим) остаются нетронутыми
и production-ready. Граница удаления НЕ затрагивает `transport`/`room`/`exit`
(фундамент) — как и в базовой спеке §3.

## 11. Spike-gate (критерии на боевом)

Платформа: Docker Talk 20.1.11 (localhost:8484, admin/adminpass — как `nctalk-call`);
опционально nc.example.org (Talk 23). Команды сборки — как в state-файле
(`go clean -cache && CGO_ENABLED=0 go build -a -o nctalk-talk ./cmd/nctalk-talk && make sign`).

Spike-gate PASS, если на боевом с реальным вторым участником (браузер):
1. **join:** `nctalk-talk <room>` → TUI рисует, статус «joined», exit не падает.
2. **слушать:** другой говорит в браузере → слышно в динамике, его строка помечена «говорит».
3. **говорить:** говорю в микрофон → другой слышит в браузере (уши + их подтверждение).
4. **участники:** заход/выход других → список обновляется (live `usersInRoom`).
5. **`M`:** замьютился → другой перестал слышать; размьютился → снова слышит. `flags` не менялись (проверить в signaling).
6. **`+`/`−`:** громкость динамка меняется, значение в TUI.
7. **`Q`/`Ctrl-C`:** чистый leave (exit 0), **нет orphan-ffmpeg** (capture+playback убиты), tty восстановлен (промпт не сломан).
8. **нет зависаний/паник** за ≥2 минуты разговора.

Аудио-анализ — уши + `ffmpeg volumedetect` (memory `audio-analysis-whisp`); `analyze_image`/спектрограмма — НЕ использовать.

## 12. Тесты

**Автотесты (CGO_ENABLED=0):**
- `interactive/mute_test.go` — `muteSource`: muted→дропает (ReadSample не возвращает / encodeLoop не WriteSample), unmuted→пропускает; гонка muted-toggle↔ReadSample.
- `interactive/volume_test.go` — `volumeWriter`: масштабирование ×gain, clipping ±32767, gain=0→тишина, gain=100→identity.
- `interactive/view_test.go` — хоткеи: таблица байт→`Event` (`m`/`M`→EvMuteToggle, `q`/`Q`/0x03→EvLeave, `+`→EvVolUp, `-`→EvVolDown, прочее→ничего). Рендер `CallState`→строки (snapshot).
- `interactive/interactive_test.go` — обогащение `agent.CallState`→`interactive.CallState` (SelfMuted/Volume/Status + гистерезис Speaking); event-loop (EvLeave→cancel, EvMuteToggle→muteSource) на fake-View.
- `agent` — дополнить существующий набор: `OnState` шлёт участников+levels раз в tick; `AudioIn != nil` используется вместо `NewEncoder(Stdin)`; `AudioIn == nil` → текущее поведение (регресс). pion-пакеты → firewall-апрув.
- `interactive/audio_io_test.go` — skip если `exec.LookPath("ffmpeg")` нет; старт/быстрый-cancel MicSource → не завис, ffmpeg убит; команда ffmpeg содержит правильные флаги (assert на args).

**Ручные / integration:**
- `cmd/nctalk-talk/integration_test.go` (build-tag `integration`): join + 5с + leave → exit 0 (как `nctalk-call` UnknownToken-тест).
- Spike-gate (§11) — ручной, на боевом.

**Изоляция:** `TestCmdNctalkDoesNotDependOnPion` остаётся зелёным (`cmd/nctalk` не тянет WebRTC). `cmd/nctalk-talk` И `cmd/nctalk-call` тянут pion — это ожидаемо (точки входа звонков), guard на них не распространяется.

## 13. Future / open

- **bubbletea-надстройка** — `bubbleteaView` через тот же `View`-контракт; отдельный build-tag или подкоманда, не ломая `ansiView`.
- **`sox` backend / Linux ALSA** — `--audio-backend`; в sox-режиме device-IO даёт PCM (сводится к pipe-схеме через `FFmpegEncoder`/`FFmpegDecoder`). MVP — avfoundation/macOS.
- **`--mute-updates-flags`** — снимать `WITH_AUDIO` через `PUT /call/{token}` (базовая §13); второй уровень mute.
- **Self-VAD** — свой «говорит» в TUI (снять уровень с `muteSource`-входа).
- **Тюнинг VAD-порогов** — `ON`/`OFF` уровни; вынести в env.
- **Engine-рефакторинг (развилка №1 → C)** — если `agent.Config` начнёт распухать хуками, вынести ядро в `internal/call/engine`, `agent.Run`/`interactive.Run` — фасады. Не сейчас.
- **Flaky localhost ICE peer-тесты под load** (из state) — рассмотреть `t.Cleanup`/leak-проверки в Этапе 4 или отдельно.
