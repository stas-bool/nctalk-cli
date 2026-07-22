Проведу code-review Этапа 4. Начну с получения diff и структуры изменений.Получу полный diff и параллельно прочитаю ключевые файлы целиком.Прочитаю оставшуюся часть agent.go и параллельно новые файлы пакета interactive.Прочитаю интерактивный orchestrator, View contract, ansiView и точку входа.Прочитаю тесты и пакет media для сравнения MicSource с FFmpegEncoder, а также check изоляции pion.Прочитаю FFmpegEncoder для сравнения с MicSource и проверю test guards, а также agent_test на регрессию.Прочитаю тесты interactive и проверю изоляцию pion.Проверю remaining test files, pion isolation test и exit value-type usage.Прочитаю pion isolation test и newFakeEncoder, проверю exit.ExitError usage в main.go и unmute в peer (для подтверждения что не сломан).Прочитаю fakeEncoder и peer (для проверки OnConnected wiring и Spreed unmute flow), а также exit package для проверки value-type.Прочитаю exit.go и фрагмент peer.go для подтверждения value-type и unmute flow.Проверю тесты с race detector и валидирую критичные гипотезы.Проверю pion isolation и подтвержу финальные детали.Все тесты зелёные, vet чистый. Готовлю полный отчёт.

---

# Code Review: Этап 4 (nctalk-talk TUI), `8f96e62..HEAD`, 12 коммитов

Проверено: `CGO_ENABLED=0 go test ./...` — все зелёные; `go vet ./...` — чисто.

## 1. КОРРЕКТНОСТЬ

### MEDIUM-1: `ansiView.Close` не будит `readLoop` — утечка горутины + потеря keystroke
**Файл:** `internal/call/interactive/tui.go:81-102, 188-203`

`readLoop` блокирует в `r.ReadByte()` (tty raw-mode read). `Close` ставит `stopped=true` и закрывает `stopCh`, но эти проверки **снаружи** `ReadByte` — поток не выходит из blocked syscall.

```go
func (v *ansiView) readLoop() {
    defer close(v.events)
    r := bufio.NewReader(v.stdin)
    for {
        if v.stopped.Load() { return }   // не проверяется, пока идёт ReadByte
        b, err := r.ReadByte()           // ← блокирует навсегда в tty-режиме
        ...
    }
}
```

Эффект: после `Q`→`Close`→`return` из `run` горутина ещё живёт и держит fd. Первый keystroke пользователя после выхода (например в shell-промпте) уходит в эту мёртвую горутину и **поглощается**. Для one-shot CLI процесс скоро выйдет, но keystroke-loss реален.

Фикс: либо писать байт в stdin пост-`Restore` (хак), либо отдельно читать stdin в горутине → `select { case b := <-reads; case <-stopCh }`, с `Close` дёргающим отдельный pipe. На macOS привычный паттерн — `(*os.File).SetReadDeadline` не работает с tty, поэтому обычно делают отдельный wakeup-pipe.

### MEDIUM-2: `peer failed: <sid>` статус «липкий» — никогда не сбрасывается
**Файл:** `internal/call/agent/agent.go:1060-1064`

`watchPeer` ставит `"peer failed: "+b.sid` в collector при падении пира. Этот статус **никогда не сбрасывается обратно** в `"joined"` — даже если приходят новые `EvUsersUpdated` с живыми пирами и `updateParticipants` обновляет список. `updateParticipants` не трогает `status`. В long-lived звонке с churn'ом TUI будет вечно показывать failure давно ушедшего пира.

Фикс: в `updateParticipants` сбрасывать `status → "joined"` при наличии живых участников, либо вынести peer-failure в отдельное transient-поле/событие.

### MEDIUM-3: Порядок участников случайный (map iteration) → дрожание UI
**Файл:** `internal/call/agent/agent.go:630-640`

`snapshot()` итерирует `map[string]*Participant` → порядок недетерминирован. На каждом OnState (10Гц) `enricher` сохраняет этот порядок, `renderTTY` рисует список заново (`\x1b[2J\x1b[H`) → имена визуально прыгают каждые 100мс.

Фикс: сортировать `out.Participants` по `Name` (или `SessionId`) перед возвратом из `snapshot()`.

### MEDIUM-4: `speakerWriter` ffmpeg-crash невидим — `mixerDrain` игнорирует Write-ошибки
**Файл:** `internal/call/agent/agent.go:1212` (pre-existing, но усилилось в Этап 4)

```go
_, _ = out.Write(buf)   // out = volumeWriter → speakerWriter
```

Если playback-ffmpeg упал (невалидный `audio_device_index`, аудио-устройство пропало), `s.stdin.Write` начнёт возвращать `EPIPE`/`broken pipe` — но ошибка теряется. Пользователь видит «работающий» TUI без звука и без сообщения. До Этапа 4 этим `out` был stdout процесса — там silently-fail ок. Теперь это устройство вывода, и для интерактивного режима провал** detection** необходим.

Фикс: для интерактивного режима добавить детекцию персистентной Write-ошибки и логировать/выходить (минимум — в `speakerWriter.Write` отслеживать первый error и слать в `Logf`, максимум — `fail-fast` через возвращение ошибки из `mixerDrain`).

### LOW-1: `MicSource.Close`/`start` race (latent)
**Файл:** `internal/call/interactive/audio_io.go:64-105, 170-181`

`startOnce` и `closeOnce` — независимые `sync.Once`. Если `Close` выполнит свой `Do` первым и увидит `m.cmd == nil`, он вернётся; затем `start` отработает и запустит ffmpeg → orphan.

В текущем использовании **не триггерится**: `encodeLoop` вызывает `ReadSample→start` строго до того, как `agent.Run` дойдёт до finalization и вызовет `encoder.Close`. Но инвариант не гарантирован кодом `MicSource` (нет общего mutex).

Фикс: mutex на чтение/запись `m.cmd`, либо единый `sync.Once`/состояние «not-started | running | closed».

### LOW-2: `volumeWriter` clipping асимметричный (off-by-one)
**Файл:** `internal/call/interactive/volume.go:38-43`

```go
case scaled > 32767:  scaled = 32767
case scaled < -32767: scaled = -32767   // ← должно быть -32768
```

Диапазон int16: `[-32768, 32767]`. На минус full-scale теряется 1 LSB — на слух незаметно, но математически некорректно (тест `TestVolumeWriter_Gain200_Clipping` явно кодирует `-32767` как ожидаемое, что легитимизирует баг).

### LOW-3: `renderTTY` без dedup → flicker
**Файл:** `internal/call/interactive/tui.go:130-151`

Каждый `Update` делает полный `\x1b[2J\x1b[H` (clear screen + cursor home) на 10Гц. В отличие от `renderFallback` (где есть `lastFallback` dedup), tty-режим перерисовывает даже если состояние не изменилось. На стабильном звонке экран мерцает.

Фикс: cursor-home `\x1b[H` + per-line `\x1b[K` вместо full clear; или dedup по `participantsKey` (который уже заготовлен в `interactive.go:185`).

### LOW-4: `stateEnricher.speaking` — утечка памяти на churn
**Файл:** `internal/call/interactive/interactive.go:146-181`

`e.speaking[sessionId]` добавляется при `Level ≥ onLevel`, но **никогда не удаляется** для ушедших участников (map только растёт). За долгый звонок с churn'ом память копится. Minor, но стоит чистить по `raw.Participants` (удалять тех, кого нет в текущем снапшоте).

### OK (без замечаний)
- **`callStateCollector` mutex** покрывает все доступы: `setStatus`/`updateParticipants`/`setLevel`/`snapshot` — все под `c.mu`. Тест `TestCallStateCollector_Concurrent` это верифицирует.
- **`OnState` throttle vs reconcile**: `stateThrottle` single-goroutine, `snapshot` под локом; `reconcile`/`setStatus` под локом — гонок нет.
- **`muteSource.ReadSample` busy-loop в mute**: крутит `inner.ReadSample`, который блокирует (MicSource — channel read). Не busy-loop.
- **`pump()`/`MicSource` shutdown**: `ctx` canceled → `ReadByte`/`NextPacket` разблокируется; `done` cap 1, `packets` cap 50 с `<-ctx.Done()` escape — дедлоков нет.
- **`speakerWriter.Close` idempotent** (`closeOnce` + `waitOnce`).
- **`io.EOF`/`context.Cancel` в encodeLoop** корректно → `done<-nil`.

---

## 2. ИНВАРИАНТЫ

### OK: Изоляция pion
`TestCmdNctalkDoesNotDependOnPion` (`internal/call/isolation_test.go`) зелёный. Дифф не трогает `transport`/`room`/`exit`/`config`/`client`/`cli`/`render` — только `internal/call/agent`, `internal/call/interactive`, `cmd/nctalk-talk`. `go.mod` меняется корректно: `pion/interceptor` и `pion/rtp` стали `// indirect` (раньше были direct deps `cmd/nctalk-call`), добавился только `golang.org/x/term v0.29.0`.

### OK: `exit.ExitError` VALUE-тип
`cmd/nctalk-talk/main.go:92, 133, 181`: везде `var ee exit.ExitError` (value-target) + `errors.As(mapped, &ee)` — корректно. `exit.ExitError` имеет value-receiver `Error()`/`Unwrap()` (exit.go:52, 60). `FromClientErr` возвращает `error`, но реальный тип — `ExitError` (не указатель). Маппинг 404→2 работает.

### OK: Mesh fanout не сломан
Этап 4 заменяет только **источник** encoder'а (`cfg.AudioIn` вместо `NewEncoder(Stdin)`). Инвариант «ОДИН `audioTrack` + ОДИН `encodeLoop` + `AddTrack` в каждую PC» сохранён:
- `audioTrack` создаётся один раз (`agent.go:294`).
- `encodeLoop` один (`agent.go:308`), с корректным `pacing` flag.
- `addPeer` вызывает `p.AttachOutgoingTrack(a.audioTrack)` (`agent.go:861`) — без изменений.
- pion делает fanout через RTPSender'ы внутри каждой PC.

Тест `TestRun_AudioIn_ReplacesNewEncoder` верифицирует: `encoderCreated==0` при заданном `AudioIn`, encodeLoop читает из AudioIn.

### OK: Backward-compat `agent.Config`
Новые поля `AudioIn`/`OnState` — optional (nil по умолчанию). При `nil`:
- `AudioIn == nil` → `NewEncoder(cfg.Stdin)` (существующий путь), `pacing=true` (тест `TestRun_AudioInNil_PacingTrue_Regression`).
- `OnState == nil` → `collector == nil`, throttle-горутина не запускается, nil-checks в `reconcile`/`watchPeer`/ICE-timeout — no-op.

Существующие 17 регрессионных тестов `agent_test.go` + новые тесты проходят без изменений.

### OK: Spreed unmute в `addPeer` не сломан
`agent.go:902-921` — `OnConnected` callback с `signaling.Send{Type:"unmute", To:sid, Payload:{"name":"audio"}}`. Не затронут Этапом 4. `peer.go:236-244` — `fireOnConnected` идемпотентный. `recreatePCForGlare` сбрасывает `onConnectedFired` (`peer.go:286-288`) — unmute уйдёт снова при glare-recreate.

---

## 3. AUDIOTOOLBOX (CRITICAL check)

### OK: Все ffmpeg-команды корректны

**MicSource (input):** `audio_io.go:67-77`
```
ffmpeg -f avfoundation -i ":<idx>" -ac 1 -ar 48000 -c:a libopus -application voip -f opus -
```
`-f avfoundation` — **input** muxer (D), `-f opus` — **output** muxer. Корректно.

**speakerWriter (output):** `audio_io.go:212-221`
```
ffmpeg -f s16le -ar 48000 -ac 1 -i - -f audiotoolbox -audio_device_index <N> -
```
`-f s16le` — input demuxer, `-f audiotoolbox` — **output-only** muxer (E), `-audio_device_index` — корректный индекс. `avfoundation` **нигде** не фигурирует как output. Корректно.

Тесты `TestMicSource_FfmpegArgs` (ищет `-f avfoundation`) и `TestSpeakerWriter_FfmpegArgs` (ищет `-f audiotoolbox`) верифицируют канон. Default device (`""`/`"default"` → `"-1"`) корректен (`TestSpeakerWriter_DefaultDevice`).

---

## 4. УПРОЩЕНИЯ / ЭФФЕКТНОСТЬ

### MEDIUM-5: RMS per-frame для каждого пира — CPU hot-path
**Файл:** `internal/call/agent/agent.go:1126-1153, 1158-1181`

`pcmMixerWriter.Write` вызывает `rmsToLevel` на **каждом** 20мс-кадре для **каждого** пира. При full mesh (8 пиров × 50Гц) = 400 вызовов/сек, каждый итерирует 960 int16→float64 conversions + `math.Sqrt` + `math.Log10` = **~384k float64 ops/сек только на RMS**. На ARM/x86 это десятки % одного ядра.

Фикс (по убыванию усилий):
- Sampling: RMS каждые N кадров (например 5 = 10Гц на пир — достаточно для TUI).
- Fixed-point RMS (int arithmetic, без float64/Sqrt/Log10).
- Inline: убрать `make([]int16, mixerFrameSamples)` (уже создаётся для `mixer.Push` — можно переиспользовать тот же slice для RMS).

### LOW-5: Дублирование `MicSource` vs `FFmpegEncoder` — оправдано
`MicSource` (`audio_io.go:33-181`) структурно близок к `FFmpegEncoder` (`media/codec.go:73-237`): те же `pump`/`done`/`packets`/`closeOnce`/`waitOnce`, отличается только ffmpeg-command (avfoundation input vs stdin) + lazy-start. Можно было вынести общий `oggSourcePump` в `media`. Но:
- `MicSource` добавляет `sync.Once` lazy-start (нет в encoder) — семантическое отличие.
- Вынос общего кода в `media` нарушил бы изоляцию (interactive → media уже импортирует, но расширение API media ради одного потребителя — over-engineering).
- Текущее дублирование ~100 строк — приемлемо для изолированного модуля.

**Вердикт:** дублирование оправдано (manifesto: prefer duplication over wrong abstraction), не фиксить.

### LOW-6: `participantsKey` dead code
**Файл:** `internal/call/interactive/interactive.go:185-191`

Функция `participantsKey` объявлена, но **нигде не вызывается** (коммент «заготовка для future оптимизации»). `go vet` не ругается (экспортируемых имён нет, но unused-private-function в Go не ошибка). Убрать или использовать в `renderTTY` dedup (fixes LOW-3).

---

## Сводка

| Severity | Count | Findings |
|----------|-------|----------|
| **CRITICAL** | 0 | — |
| **HIGH** | 0 | — |
| **MEDIUM** | 5 | M-1 readLoop leak+keystroke loss · M-2 sticky "peer failed" · M-3 random participant order · M-4 silent speaker crash · M-5 RMS CPU hot-path |
| **LOW** | 6 | L-1 Close/start race · L-2 volume off-by-one · L-3 renderTTY flicker · L-4 speaking-map leak · L-5 (оправдано) · L-6 dead code |

**Инварианты (pion isolation, ExitError value-type, mesh fanout, backward-compat, Spreed unmute) — все соблюдены.** AUDIOTOOLBOX-канон — корректен везде.

Код качественный: production-hardened `agent.Run` расширен минимально-инвазивно (2 optional-поля + collector), тесты покрывают regression/concurrency/enrichment. Основные замечания — UX/cosmetic (flicker, random order, sticky status) и один реальный correctness-баг (readLoop goroutine leak на tty). Blockers для merge нет; рекомендую фиксить M-1 (корректность), M-3 (random order — тривіально, одна сортировка) и M-4 (silent speaker crash) до spike-gate, остальные можно после.

---

## Верификация координатором (glm-5.2, шаг 10 spec-to-code)

Каждое замечание перепроверено против кода. Итог: **7 исправлено, 4 отложено, 0 false-positive.**

### Исправлено
- **M-2** (sticky "peer failed"): transient через поле `failedSid` в `callStateCollector`; сбрасывается в `updateParticipants`, когда peer уходит из signaling. (agent.go)
- **M-3** (random order): `sort.Slice` по `Name` в `snapshot()`. (agent.go)
- **M-4** (silent speaker crash): `speakerWriter.LastError()` + логирование в `interactive.Run` после `agent.Run`. (audio_io.go, interactive.go)
- **L-2** (volume off-by-one): clip `-32767` → `-32768` (диапазон int16 `[-32768, 32767]`); тест обновлён. (volume.go, volume_test.go)
- **L-3** (renderTTY flicker): full `\x1b[2J` заменён на cursor-home + per-line `\x1b[K` + `\x1b[J`. (tui.go)
- **L-4** (speaking-map leak): cleanup ушедших участников в `onState`. (interactive.go)
- **L-6** (dead code): удалён `participantsKey` + unused `strings` import. (interactive.go)

### Отложено (валидные, но не блокируют spike-gate — follow-up)
- **M-1** (readLoop goroutine leak): wakeup-pipe нетривиален (`io.Reader` без fd для select), рискован; для one-shot CLI процесс выходит мгновенно, keystroke-loss в окне Restore→exit — cosmetic. Не блокирует spike-gate.
- **M-5** (RMS CPU hot-path): perf, не correctness; на spike-gate 1-2 пира не проявляется. Sampling (RMS каждые N кадров) — follow-up.
- **L-1** (MicSource Close/start race): latent, не триггерируется в текущем flow (`encodeLoop→ReadSample→start` строго до `finalization→Close`). Mutex — follow-up.
- **L-5** (MicSource vs FFmpegEncoder duplication): вердикт ревьюера — оправдано (duplication over wrong abstraction).

### Тесты после правок (CGO_ENABLED=0, `-race`)
agent PASS · interactive PASS · cmd/nctalk-talk PASS · pion-free (transport/config/room/signaling/media) PASS · `TestCmdNctalkDoesNotDependOnPion` PASS (`cmd/nctalk` — 0 pion deps) · `go vet ./...` чист.
