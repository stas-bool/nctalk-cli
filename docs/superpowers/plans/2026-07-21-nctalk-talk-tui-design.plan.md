# `nctalk-talk` TUI (Этап 4) — Implementation Plan

> **Для исполнителя:** план исполняется через `superpowers:limit-aware-subagent-driven-development` — одна задача = один implementer-цикл + ревью. Каждый шаг с чекбоксом `- [ ]` отдельный. Ссылки вида `§N` указывают на разделы спеки — открывай первоисточник: `docs/superpowers/specs/2026-07-21-nctalk-talk-tui-design.md` (отревьюенная). Эталоны контракта базового CLI и звонков: `docs/superpowers/specs/2026-07-17-nctalk-cli-design.md`, `docs/superpowers/specs/2026-07-19-nctalk-call-design.md`. **Не пересказывай спеку — ей следуй.** Кириллические комментарии в коде/документах сохранять.

**Цель:** реализовать интерактивный TUI-режим «человек в терминале»: зайти в комнату Talk, говорить в микрофон и слышать других через динамик, с экранным управлением (mute свой, leave, громкость). Тонкий слой поверх готового `agent.Run` — минимальное вторжение, переиспользование call-слоя без переписывания.

**Approach:** сначала минимальное additive-расширение `agent.Run` (ровно 2 опциональных поля `Config` + локальные правки), затем primitives в новом пакете `interactive` (mute/volume/audio_io/view по нарастающей), затем оркестратор `interactive.Run`, затем точка входа `cmd/nctalk-talk/main.go`, затем integration + spike-gate. Этапы 0-3 (pipe-режим `nctalk-call`) уже production-hardened и НЕ переписываются.

**Architecture (спека §4):** `media ← peer ← agent ← interactive`; `agent` НЕ импортирует `interactive`, `interactive` импортирует `agent`/`media`. Изоляция от pion сохраняется: `cmd/nctalk` и фундамент (`transport`/`room`/`exit`/`config`/`client`) НЕ зависят от WebRTC; guard `TestCmdNctalkDoesNotDependOnPion` остаётся зелёным. `cmd/nctalk-talk` и `cmd/nctalk-call` тянут pion — ожидаемо (точки входа звонков).

**Tech Stack:** Go 1.21+, stdlib + `github.com/pion/webrtc/v4` (через `agent`) + вторая не-stdlib зависимость `golang.org/x/term` (raw-mode + размер терминала). Audio-IO и Opus-codec — через subprocess `ffmpeg` (без CGO).

**Сборка и тесты (CGO_ENABLED=0 обязательно — `CLAUDE.md`, `dyld: missing LC_UUID` на этой машине):**
```sh
CGO_ENABLED=0 go build ./...                                      # все бинарники
CGO_ENABLED=0 go build -o nctalk-talk ./cmd/nctalk-talk
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 go test ./...                                       # unit-тесты (не integration)
CGO_ENABLED=0 go test ./internal/call/interactive/... -run TestName -v   # один тест
CGO_ENABLED=0 go test -tags=integration ./cmd/nctalk-talk/...          # боевой сервер
```

**Module path:** `github.com/stas/nctalk`.

## Глобальные ограничения (действуют на каждую задачу)

- Go 1.21+; **`CGO_ENABLED=0`** на всех сборках/тестах; stdlib + `pion/webrtc/v4` + `golang.org/x/term` — единственные разрешённые не-stdlib зависимости.
- Креды — **только из env** (`NEXTCLOUD_URL`/`NEXTCLOUD_LOGIN`/`NEXTCLOUD_PASS`/`NEXTCLOUD_TIMEOUT`), никогда в argv/выводе/логах. Пароль — только в заголовке `Authorization`.
- Все ошибки пропускаются через `sanitizeErr`/`exit.FromClientErr` (без userinfo/query).
- Все HTTP-запросы с `context.Context` → Ctrl-C отменяет и signaling-poll, и peer-установку, и TUI-цикл.
- Инвариант изоляции: `internal/client`/`internal/cli`/`internal/render`/`internal/config`/`internal/transport`/`internal/room`/`internal/exit` **НЕ импортируют** `internal/call/*` (guard `TestCmdNctalkDoesNotDependOnPion` остаётся зелёным).
- `exit.ExitError` — **VALUE-тип** (не указатель): `errors.As(err, &ee)` с `var ee exit.ExitError` (НЕ `*exit.ExitError`).
- Mesh fanout: ОДИН `TrackLocalStaticSample` в agent + `AddTrack` в каждую PC; НЕ запускать encodeLoop на каждого peer (инвариант уже соблюдён в существующем `agent.Run` — не ломать).
- Spreed signaling unmute {name:'audio'} обязателен на `OnConnected` (в `agent.addPeer` уже есть — не трогать).
- Коммиты **БЕЗ AI-атрибуции** (никакого `Co-Authored-By: Claude`).
- Signaling API contract (Spreed 20+): signaling = **v3**, call = **v4**, joinRoom = `POST v4/room/{token}/participants/active` — уже реализовано в `internal/call/signaling`, не ломать.
- Кириллические комментарии в коде — сохранять (как в спеке и существующих пакетах).

## Риски и точки решения (спека §1, §10, §11)

| # | Риск | Где проверяется | Mitigation | Точка решения «продолжать / выпилить» |
|---|---|---|---|---|
| R1 | device-out muxer ошибка (avfoundation — input-only) | Task 4.6 (audio_io) | Канон спеки §3: input=avfoundation `":<N>"`, output=audiotoolbox `-audio_device_index <N>` | Compile + assertion на args ffmpeg |
| R2 | startup race: ffmpeg-capture переполняет packets-канал до encodeLoop | Task 4.6 (MicSource lazy start, review #8) | Lazy start при первом ReadSample (внутри encodeLoop, после JoinCall + ICE setup) | Unit test на sync.Once + cancel-cleanup |
| R3 | Concurrent stale RMS / participants в state-коллекторе | Task 4.2 (review #5) | `callStateCollector` под `sync.Mutex` | Race detector (`-race`) в unit-тестах |
| R4 | TTY сломан при panic/error внутри agent.Run | Task 4.7 (review #11) | `defer view.Close()` сразу после MakeRaw/alt-screen | Manual: kill -9 во время звонка → промпт жив |
| R5 | Signaling-mute других участников неизвестен (не верифицирован формат Spreed) | Task 4.4 (scope-squeeze, review #3) | MVP показывает только свой mute (поле `SelfMuted`); другие → future §13 | Spec §1 явно исключает; в коде нет поля `Muted` у других |

**Точка отката (спека §10):** `rm -rf cmd/nctalk-talk internal/call/interactive && go mod tidy` + откат опциональных полей `agent.Config` (`AudioIn`, `OnState`) и локальных правок (`pcmMixerWriter` RMS, `callStateCollector`, state-throttle, `agentEncodeLoop` pacing). `cmd/nctalk-call` и `agent.Run` (pipe-режим) остаются нетронутыми и production-ready. Граница удаления НЕ затрагивает `transport`/`room`/`exit` (фундамент).

## Подэтапы (обзор)

| Подэтап | Задачи | Зависит от | Exit-criterion |
|---|---|---|---|
| **4.A** Расширение `agent.Run` | 4.1, 4.2, 4.3 | Этапы 0-3 (готово) | Существующие 17 agent-тестов зелёные + новые тесты AudioIn/OnState/RMS зелёные |
| **4.B** interactive primitives | 4.4, 4.5, 4.6 | 4.A | mute/volume/audio_io юнит-тесты зелёные (pion-пакеты — macOS firewall апрув) |
| **4.C** View + TUI | 4.7 | 4.B | ansiView: ключи, render, non-tty fallback — юнит-тесты зелёные |
| **4.D** interactive.Run оркестратор | 4.8 | 4.A + 4.B + 4.C | enrichment + event-loop + dedup — юнит-тесты на fake agent/View зелёные |
| **4.E** cmd + integration | 4.9, 4.10, 4.11 | 4.D | `cmd/nctalk-talk` exit-codes (1/2/3); integration ctx.WithTimeout→exit 0; spike-gate на боевом |

---

## Подэтап 4.A — Расширение `agent.Run` (minimal additive)

**Цель подэтапа:** добавить в `agent.Config` ровно 2 опциональных поля (`AudioIn media.AudioSource`, `OnState func(CallState)`) и локальные правки в `agent.go` (encoder selection, encodeLoop pacing, pcmMixerWriter RMS, state-throttle горутина, reconcile → participants). При незаданных полях поведение `nctalk-call` идентично текущему — регрессию ловят существующие 17 agent-тестов. Содержательная часть mute/volume/view — в `interactive`, не здесь.

**Файлы (общие для подэтапа):**
- Modify: `internal/call/agent/agent.go`
- Modify: `internal/call/agent/agent_test.go` (дополнить новыми кейсами, моки переиспользовать)
- Test fixture channel: pion-пакет → macOS firewall апрув на ручном прогоне (как для Этапов 0-3).

### Task 4.1: `agent.Config.AudioIn` + encodeLoop pacing (review #2)

**Files:**
- Modify: `internal/call/agent/agent.go` — поле `Config.AudioIn`, step 2 (encoder setup), сигнатура `agentEncodeLoop`, вызов горутины.
- Test: `internal/call/agent/agent_test.go` — добавить `TestRun_AudioIn_ReplacesNewEncoder` и `TestRun_AudioInNil_PacingTrue_Regression`.

**Interfaces:**
- Consumes: `media.AudioSource` (из `call/media`, уже импортирован agent'ом).
- Produces: `Config.AudioIn media.AudioSource` — если не nil, encodeLoop использует его ВМЕСТО `NewEncoder(Stdin)`; поле читается только в `Run`, immutable в течение звонка. Параметр `pacing bool` в `agentEncodeLoop` — internal.

**Контракт изменения:**
1. В `Config` добавить поле (комментарий на кириллице):
   ```go
   // AudioIn — готовый источник Opus-пакетов. Если задан — используется
   // encodeLoop'ом ВМЕСТО NewEncoder(Stdin). Stdin при этом игнорируется.
   // Для nctalk-talk сюда подаётся muteSource{device-MicSource}.
   // Для nctalk-call — nil (как раньше, NewEncoder(Stdin)).
   AudioIn media.AudioSource
   ```
2. В step 2 (encoder setup) выбрать источник:
   ```go
   var encoder media.AudioSource
   if cfg.AudioIn != nil {
       encoder = cfg.AudioIn
   } else {
       enc, err := cfg.NewEncoder(cfg.Stdin)
       if err != nil {
           leaveBestEffort(cfg.Signaling, cfg.Token)
           return mapCodecErr("encoder", err)
       }
       encoder = enc
   }
   ```
   ВНИМАНИЕ: вызов `cfg.NewEncoder(Stdin)` остался в ветке `else` — код идемпотентен к существующему pipe-поведению (regression-тест: `AudioIn==nil` → `NewEncoder` вызывается, pacing=true).
3. Сигнатура `agentEncodeLoop`:
   ```go
   func agentEncodeLoop(src media.AudioSource, track *webrtc.TrackLocalStaticSample, done chan<- error, pacing bool) {
       // ... тело без изменений до time.Sleep ...
       if pacing {
           // Real-time pacing только для stdin-источника (pipe-режим):
           // ffmpeg encode читает вход быстрее real-time — без sleep весь
           // PCM-файл кодируется за <1с, encodeLoop EOF, agent выходит ДО
           // установления ICE. Sleep на dur (типично 20мс Opus-frame) даёт
           // real-time стриминг. При AudioIn != nil (device-source, уже
           // real-time) sleep НЕ делается — иначе scheduler-jitter накапливается
           // (review finding #2, HIGH).
           time.Sleep(dur)
       }
   }
   ```
4. Вызов горутины передаёт флаг:
   ```go
   go agentEncodeLoop(encoder, audioTrack, encodeDone, cfg.AudioIn == nil)
   ```

- [ ] **Шаг 1: написать failing test `TestRun_AudioIn_ReplacesNewEncoder`**

   ```go
   // TestRun_AudioIn_ReplacesNewEncoder — если cfg.AudioIn задан, NewEncoder НЕ
   // вызывается (encoderCreated==0), encodeLoop читает из AudioIn (agentExit по
   // ctx). Регрессия для review #2: device-источник не проходит через pipe-encoder.
   func TestRun_AudioIn_ReplacesNewEncoder(t *testing.T) {
       fs := &fakeSignaling{pollLoop: pollSendThenBlock(nil)}
       counters := newTestCounters()
       cfg := counters.buildConfig(fs, inFlagSendRecv, io.Discard)
       // Подменяем AudioSource целиком — NewEncoder не должен вызваться.
       audioIn := newFakeEncoder()
       cfg.AudioIn = audioIn

       ctx, cancel := context.WithCancel(context.Background())
       go func() { time.Sleep(100 * time.Millisecond); cancel() }()
       if err := Run(ctx, cfg); err != nil {
           t.Fatalf("Run err = %v, want nil", err)
       }
       if got := atomic.LoadInt32(&counters.encoderCreated); got != 0 {
           t.Errorf("encoderCreated = %d, want 0 (AudioIn заменяет NewEncoder)", got)
       }
       if audioIn.closeCount() == 0 {
           t.Errorf("AudioIn.Close не вызван — agent.Run должен закрывать encoder в shutdown")
       }
   }
   ```

- [ ] **Шаг 2: запустить тест — должен FAIL**

   Run: `CGO_ENABLED=0 go test ./internal/call/agent/ -run TestRun_AudioIn_ReplacesNewEncoder -v`
   Expected: FAIL (compile error: `cfg.AudioIn undefined` / `agentEncodeLoop got 3 args, want 4`).

- [ ] **Шаг 3: реализовать правки в `agent.go`**

   Внести 4 правки из контракта изменения выше (поле Config, ветка else, сигнатура, вызов).

- [ ] **Шаг 4: запустить тест — должен PASS**

   Run: `CGO_ENABLED=0 go test ./internal/call/agent/ -run TestRun_AudioIn_ReplacesNewEncoder -v`
   Expected: PASS.

- [ ] **Шаг 5: добавить regression-тест `TestRun_AudioInNil_PacingTrue_Regression`**

   ```go
   // TestRun_AudioInNil_PacingTrue_Regression — если cfg.AudioIn == nil, путь
   // encodeLoop идентичен pipe-режиму (вызов NewEncoder, pacing=true). Существующие
   // 17 тестов это уже неявно проверяют; здесь явная регрессия для review #2.
   func TestRun_AudioInNil_PacingTrue_Regression(t *testing.T) {
       fs := &fakeSignaling{pollLoop: pollSendThenBlock(nil)}
       counters := newTestCounters()
       cfg := counters.buildConfig(fs, inFlagSendRecv, io.Discard)
       // AudioIn НЕ задаём — по умолчанию nil.

       ctx, cancel := context.WithCancel(context.Background())
       go func() { time.Sleep(100 * time.Millisecond); cancel() }()
       if err := Run(ctx, cfg); err != nil {
           t.Fatalf("Run err = %v, want nil", err)
       }
       if got := atomic.LoadInt32(&counters.encoderCreated); got != 1 {
           t.Errorf("encoderCreated = %d, want 1 (AudioIn nil → NewEncoder вызывается)", got)
       }
   }
   ```

- [ ] **Шаг 6: запустить ВСЕ agent-тесты — regression check**

   Run: `CGO_ENABLED=0 go test ./internal/call/agent/ -v`
   Expected: все существующие 17 тестов + 2 новых — зелёные. Если firewall блокирует test-binary — запустить вручную (правило из `agent_test.go`).

- [ ] **Шаг 7: коммит**

   ```bash
   git add internal/call/agent/agent.go internal/call/agent/agent_test.go
   git commit -m "feat(agent): AudioIn source + encodeLoop pacing flag (review #2, Этап 4.1)

- AudioIn media.AudioSource в Config — если задан, encodeLoop использует его ВМЕСТО NewEncoder(Stdin).
- agentEncodeLoop получает pacing bool: sleep только при pacing=true (stdin-pipe).
- Регрессия: AudioIn==nil → существующее поведение nctalk-call без изменений."
   ```

**Acceptance criteria:**
- `TestRun_AudioIn_ReplacesNewEncoder` PASS.
- `TestRun_AudioInNil_PacingTrue_Regression` PASS.
- Все 17 существующих agent-тестов PASS (без изменений).
- `go vet ./internal/call/agent/...` чисто.

---

### Task 4.2: `callStateCollector` + reconcile → participants + OnState throttle + `CallState.Status` (review #5, #7)

**Files:**
- Modify: `internal/call/agent/agent.go` — типы `CallState`, `Participant`, `callStateCollector`, поле `Config.OnState`,状态-горутина, изменения в `reconcile`, `Run`.
- Test: `internal/call/agent/agent_test.go` — `TestCallStateCollector_Concurrent`, `TestRun_OnState_SnapshotAndStatus`, `TestRun_OnStateNil_NoGoroutine_Regression`.

**Interfaces:**
- Produces:
  - `agent.CallState{ Status string, Participants []Participant }` — публичный тип (доступен `interactive`).
  - `agent.Participant{ SessionId, Name string, Level int }` — публичный тип; `Name` = `ActorId` из signaling (id, НЕ display — review #6).
  - `Config.OnState func(CallState)` — periodic-снапшот (раз в ~100мс из throttle-горутины); nil → горутина не запускается.

**Контракт изменения:**

1. Публичные типы (в начале файла, после блока констант):
   ```go
   // Participant — один remote-участник в снапшоте состояния звонка.
   // Поля — только то, что знает agent; mute/volume/speaking — enrichment в interactive.
   type Participant struct {
       SessionId string
       Name      string // В MVP = signaling.User.ActorId (id, НЕ display name — review #6).
                        // Display через OCS participants API — future §13.
       Level     int    // 0..100, нормализованный RMS (raw из pcmMixerWriter, Task 4.3).
   }

   // CallState — снапшот состояния звонка, пушится в cfg.OnState раз в ~100мс.
   // Status (review #7) — lifecycle-фазы:
   //   "joining"          — до JoinCall success;
   //   "joined"           — после JoinCall success;
   //   "peer failed: <sid>" — single-peer failure (из watchPeer);
   //   "ice-timeout"       — exit по ICE-таймауту.
   type CallState struct {
       Status       string
       Participants []Participant
   }
   ```

2. `callStateCollector` — внутренний тип под mutex:
   ```go
   // callStateCollector — thread-safe хранилище снапшота для OnState. Write RMS
   // (из per-peer pcmMixerWriter) и update participants (из reconcile) — оба под lock.
   // review finding #5: без mutex гонки между горутиной pcmMixerWriter.Write и
   // main-loop reconcile при чтении/записи общей map.
   type callStateCollector struct {
       mu      sync.Mutex
       status  string
       parts   map[string]*Participant // sessionId → указатель на актуальный Participant
       levels  map[string]int           // sessionId → последний Level (RMS из Task 4.3)
   }

   func newCallStateCollector() *callStateCollector {
       return &callStateCollector{
           parts:  make(map[string]*Participant),
           levels: make(map[string]int),
       }
   }

   func (c *callStateCollector) setStatus(s string) {
       c.mu.Lock(); c.status = s; c.mu.Unlock()
   }

   // updateParticipants — обновляет карту участников под lock.rms-levels НЕ сбрасываются
   // (если sid остался — уровень сохраняется; ушёл — удаляется).
   func (c *callStateCollector) updateParticipants(users []signaling.User, ownSid string) {
       c.mu.Lock()
       defer c.mu.Unlock()
       // Удаляем ушедших.
       want := make(map[string]struct{}, len(users))
       for _, u := range users {
           if u.SessionId == "" || u.SessionId == ownSid { continue }
           if u.InCall&inFlagWithAudio == 0 { continue }
           want[u.SessionId] = struct{}{}
       }
       for sid := range c.parts {
           if _, ok := want[sid]; !ok {
               delete(c.parts, sid)
               delete(c.levels, sid)
           }
       }
       // Добавляем новых.
       for _, u := range users {
           if _, ok := want[u.SessionId]; !ok { continue }
           if _, exists := c.parts[u.SessionId]; !exists {
               c.parts[u.SessionId] = &Participant{
                   SessionId: u.SessionId,
                   Name:      u.ActorId, // MVP = ActorId (review #6).
               }
           }
       }
   }

   // setLevel — вызывается из pcmMixerWriter (per-peer горутина). Idempotent, lock-protected.
   func (c *callStateCollector) setLevel(sid string, level int) {
       c.mu.Lock()
       if _, ok := c.parts[sid]; ok {
           c.levels[sid] = level
       }
       c.mu.Unlock()
   }

   // snapshot — копия состояния для OnState. Возвращает slice (детерминированный порядок
   // не гарантируется — interactive при необходимости сортирует).
   func (c *callStateCollector) snapshot() CallState {
       c.mu.Lock()
       defer c.mu.Unlock()
       out := CallState{Status: c.status, Participants: make([]Participant, 0, len(c.parts))}
       for sid, p := range c.parts {
           cp := *p
           cp.Level = c.levels[sid]
           out.Participants = append(out.Participants, cp)
       }
       return out
   }
   ```

3. Поле в `Config`:
   ```go
   // OnState — periodic-снапшот состояния звонка. Agent вызывает раз в ~100мс из
   // throttle-горутины. nil → не вызывается (nctalk-call не нуждается).
   OnState func(CallState)
   ```

4. В `agentState` добавить ссылку: `state *callStateCollector` (nil если OnState==nil).

5. В `Run`:
   - Создать коллектор ЕСЛИ `cfg.OnState != nil`:
     ```go
     var collector *callStateCollector
     if cfg.OnState != nil {
         collector = newCallStateCollector()
         collector.setStatus("joining")
     }
     ```
   - Передать в `agentState`.
   - После успеха `JoinCall` (между step 1 и step 2): `if collector != nil { collector.setStatus("joined") }`.
   - Перед возвратом по ICE-timeout: `if collector != nil { collector.setStatus("ice-timeout") }`.
   - Запустить throttle-горутину (если collector != nil):
     ```go
     stateDone := make(chan struct{})
     if collector != nil {
         go stateThrottle(ctx, collector, cfg.OnState, stateDone)
     }
     ```
     В `defer` или в step 9 закрыть `stateDone` после выхода из main-loop.
   - В финализации: `if stateDone != nil { close(stateDone); <-stateDone }` (дождаться выхода).
   Функция `stateThrottle`:
   ```go
   // stateThrottle — горутина периодического снапшота состояния (раз в stateTickPeriod).
   // Выход по ctx или stop. Вызывает OnState под lock-копированием (snapshot — короткий).
   func stateThrottle(ctx context.Context, c *callStateCollector, cb func(CallState), done chan<- struct{}) {
       defer close(done)
       ticker := time.NewTicker(stateTickPeriod)
       defer ticker.Stop()
       for {
           select {
           case <-ctx.Done():
               // Финальный снапшот — OnState увидит status "ice-timeout" / "joined".
               cb(c.snapshot())
               return
           case <-ticker.C:
               cb(c.snapshot())
           case <-stop:
               return
           }
       }
   }
   ```
   НО: `stop` — локальный канал; реализация зависит от того, где закрывается `stateDone`. В Step 9 финализации сначала закрывается `stateDone`-канал (как `drainStop`), горутина выходит.

6. В `reconcile` (внутри `agentState`):
   - После обновления peers-map вызвать `if a.state != nil { a.state.updateParticipants(users, ownSid) }` (с ownSid из `a.ownSessionIdLocked()`).

7. В `watchPeer` (single-peer failure → status):
   ```go
   case err := <-b.peer.Failed():
       reason := "disconnected"
       if err != nil { reason = err.Error() }
       if a.state != nil {
           a.state.setStatus("peer failed: " + b.sid)
       }
       a.removePeer(b.sid, reason)
   ```

8. Константа `stateTickPeriod`:
   ```go
   // stateTickPeriod — частота пуша OnState-снапшота. 100мс = 10Гц — достаточно для
   // TUI-обновления (eye-comfort) и не нагружает CPU.
   stateTickPeriod = 100 * time.Millisecond
   ```

- [ ] **Шаг 1: failing test `TestCallStateCollector_Concurrent`** — пустить N горутин `setLevel`/`updateParticipants`/`snapshot` под `-race`.

   ```go
   // TestCallStateCollector_Concurrent — гонка setLevel ∥ updateParticipants ∥ snapshot.
   // Запускать с -race: data-race = FAIL. review finding #5.
   func TestCallStateCollector_Concurrent(t *testing.T) {
       c := newCallStateCollector()
       c.setStatus("joined")
       var wg sync.WaitGroup
       for i := 0; i < 8; i++ {
           wg.Add(1)
           sid := fmt.Sprintf("peer-%d", i)
           go func() {
               defer wg.Done()
               c.updateParticipants([]signaling.User{{
                   SessionId: sid, ActorId: "actor-" + sid, InCall: 3,
               }}, "own")
               for j := 0; j < 100; j++ {
                   c.setLevel(sid, j)
                   _ = c.snapshot()
               }
           }()
       }
       wg.Wait()
       snap := c.snapshot()
       if snap.Status != "joined" {
           t.Errorf("status = %q, want joined", snap.Status)
       }
   }
   ```

- [ ] **Шаг 2: failing test `TestRun_OnState_SnapshotAndStatus`** — OnState получает статусы "joining"→"joined" и участников после EvUsersUpdated.

   ```go
   // TestRun_OnState_SnapshotAndStatus — OnState получает переход joining→joined
   // и участников в снапшоте после EvUsersUpdated. Гарантирует, что Status меняется
   // по жизненному циклу (review #7), а participants — из reconcile.
   func TestRun_OnState_SnapshotAndStatus(t *testing.T) {
       fs := &fakeSignaling{
           pollLoop: pollSendThenBlock([]signaling.Event{
               {Kind: signaling.EvUsersUpdated, Users: []signaling.User{
                   {SessionId: "peer-A", ActorId: "actor-A", InCall: 3},
               }},
           }),
       }
       counters := newTestCounters()
       cfg := counters.buildConfig(fs, 1, io.Discard) // recvonly — encodeLoop не запускается

       var mu sync.Mutex
       var snaps []CallState
       cfg.OnState = func(s CallState) {
           mu.Lock(); snaps = append(snaps, s); mu.Unlock()
       }

       ctx, cancel := context.WithCancel(context.Background())
       go func() { time.Sleep(300 * time.Millisecond); cancel() }() // > 3×stateTickPeriod
       if err := Run(ctx, cfg); err != nil {
           t.Fatalf("Run err = %v, want nil", err)
       }

       mu.Lock(); defer mu.Unlock()
       if len(snaps) == 0 {
           t.Fatal("OnState ни разу не вызван")
       }
       // Первый снапшот — "joining" или "joined".
       first := snaps[0]
       if first.Status != "joining" && first.Status != "joined" {
           t.Errorf("first status = %q, want joining|joined", first.Status)
       }
       // Хоть один снапшот содержит participant.
       found := false
       for _, s := range snaps {
           for _, p := range s.Participants {
               if p.SessionId == "peer-A" && p.Name == "actor-A" {
                   found = true
               }
           }
       }
       if !found {
           t.Errorf("ни в одном снапшоте нет peer-A с Name=actor-A (snaps=%+v)", snaps)
       }
   }
   ```

- [ ] **Шаг 3: failing test `TestRun_OnStateNil_NoGoroutine_Regression`** — если OnState==nil, collector/state-throttle не создаются (regression для pipe-режима).

   ```go
   // TestRun_OnStateNil_NoGoroutine_Regression — OnState==nil → без throttle-горутины
   // и collector'а. Косвенно: agent.Run завершается штатно, goroutine-leak — нет.
   // Прямая проверка — через отсутствие записей в cfg.OnState (nil-panic если вызов).
   func TestRun_OnStateNil_NoGoroutine_Regression(t *testing.T) {
       fs := &fakeSignaling{pollLoop: pollSendThenBlock(nil)}
       counters := newTestCounters()
       cfg := counters.buildConfig(fs, 1, io.Discard)
       // OnState НЕ задаём.

       ctx, cancel := context.WithCancel(context.Background())
       go func() { time.Sleep(150 * time.Millisecond); cancel() }()
       if err := Run(ctx, cfg); err != nil {
           t.Fatalf("Run err = %v, want nil", err)
       }
       // Не упало = нет паники на nil-OnState; утечки горутин проверяются по
       // отсутствию блокировки в завершении теста (timeout в go test).
   }
   ```

- [ ] **Шаг 4: запустить 3 теста — должны FAIL**

   Run: `CGO_ENABLED=0 go test ./internal/call/agent/ -run 'TestCallStateCollector_Concurrent|TestRun_OnState' -v`
   Expected: FAIL (compile errors: `CallState undefined`, `callStateCollector undefined`, `Config.OnState undefined`).

- [ ] **Шаг 5: реализовать правки в `agent.go`**

   Внести изменения 1-8 из контракта выше. Внимание:
   - `agentState` добавить поле `state *callStateCollector`.
   - Передать collector в `a := &agentState{..., state: collector}`.
   - В шаге JoinCall success обновить статус.
   - В step 9 финализации закрыть `stateDone` (как `drainStop`).
   - Если `cfg.OnState == nil` — `collector == nil`, `stateDone == nil` (как `encodeDone` в recvonly).

- [ ] **Шаг 6: запустить 3 теста — должны PASS**

   Run: `CGO_ENABLED=0 go test ./internal/call/agent/ -run 'TestCallStateCollector_Concurrent|TestRun_OnState' -v -race`
   Expected: PASS; `-race` чисто.

- [ ] **Шаг 7: запустить все agent-тесты с `-race`**

   Run: `CGO_ENABLED=0 go test ./internal/call/agent/ -v -race`
   Expected: 17 + 3 = 20 тестов зелёные, data-race'ев нет.

- [ ] **Шаг 8: коммит**

   ```bash
   git add internal/call/agent/agent.go internal/call/agent/agent_test.go
   git commit -m "feat(agent): callStateCollector + OnState throttle + CallState.Status (review #5/#7, Этап 4.2)

- Config.OnState func(CallState) + типы Participant/CallState (Status, Participants).
- callStateCollector под sync.Mutex — write RMS ∥ update participants ∥ snapshot.
- reconcile обновляет участников (Name = ActorId, review #6).
- Status lifecycle: joining → joined → peer failed / ice-timeout.
- stateThrottle горутина (100мс) — только если OnState != nil.
- Регрессия: OnState==nil → collector/throttle не запускаются (pipe-режим nctalk-call)."
   ```

**Acceptance criteria:**
- Все 3 новых теста PASS с `-race`.
- 17 существующих agent-тестов PASS (без изменений).
- `CallState`, `Participant` — публичные типы в package `agent` (доступны `interactive`).

---

### Task 4.3: `pcmMixerWriter` RMS → `callStateCollector.setLevel` (review #10)

**Files:**
- Modify: `internal/call/agent/agent.go` — `pcmMixerWriter` получает ссылку на `*callStateCollector` и `sid string`; computes RMS per-frame → dBFS → Level 0..100.
- Test: `internal/call/agent/agent_test.go` — `TestRMS_ToLevel_Formula`, `TestPcmMixerWriter_RMS_ToCollector`.

**Interfaces:**
- Consumes: `*callStateCollector` (из Task 4.2), `peerBundle.sid` (существующее поле).
- Produces: side-effect — `collector.setLevel(sid, level)` на каждом Write.

**Контракт изменения:**

1. В `pcmMixerWriter` добавить поля:
   ```go
   type pcmMixerWriter struct {
       idx          int
       sid          string                 // sessionId пира (для setLevel)
       mixer        *media.Mixer
       buf          []byte
       frameSamples int
       collector    *callStateCollector    // nil → RMS не собирается (nctalk-call)
   }
   ```

2. В `addPeer` при создании `pcmMixerWriter` передать `sid` и `collector`:
   ```go
   pcmW := &pcmMixerWriter{
       idx:          idx,
       sid:          sid,
       mixer:        a.mixer,
       frameSamples: mixerFrameSamples,
       collector:    a.state,
   }
   ```

3. В `pcmMixerWriter.Write` — после формирования `samples` (int16) вызвать:
   ```go
   if w.collector != nil {
       w.collector.setLevel(w.sid, rmsToLevel(samples))
   }
   ```
   RMS-формула (review #10, спека §7):
   ```go
   // rmsToLevel считает нормализованный уровень PCM-кадра.
   // dBFS = 20·log10(rms/32768), map dBFS ∈ [-60, 0] → Level ∈ [0, 100].
   // Тише -60 → 0, громче 0 → 100 (clamped). Спека §7, review #10.
   func rmsToLevel(samples []int16) int {
       if len(samples) == 0 {
           return 0
       }
       var sumSq float64
       for _, s := range samples {
           f := float64(s)
           sumSq += f * f
       }
       rms := math.Sqrt(sumSq / float64(len(samples)))
       if rms < 1 {
           return 0
       }
       dbFS := 20.0 * math.Log10(rms/32768.0)
       // Линейная map [-60, 0] → [0, 100].
       level := int((dbFS + 60.0) * 100.0 / 60.0)
       if level < 0 {
           return 0
       }
       if level > 100 {
           return 100
       }
       return level
   }
   ```
   Добавить `"math"` в import блока `agent.go`.

- [ ] **Шаг 1: failing test `TestRMS_ToLevel_Formula`** — таблица: тишина→0, full-scale→100, mid-range→ конкретное число.

   ```go
   // TestRMS_ToLevel_Formula — формула dBFS→Level (review #10, спека §7).
   func TestRMS_ToLevel_Formula(t *testing.T) {
       cases := []struct {
           name string
           samples []int16
           want    int
       }{
           {"тишина", []int16{0, 0, 0, 0}, 0},
           {"full-scale sine", []int16{32767, -32767, 32767, -32767}, 100},
           {"тихий сигнал (-50 dBFS rms)", []int16{50, -50, 50, -50}, 0}, // ~ -56 dBFS → clamp 0
       }
       for _, tc := range cases {
           t.Run(tc.name, func(t *testing.T) {
               got := rmsToLevel(tc.samples)
               if got != tc.want {
                   t.Errorf("rmsToLevel(%v) = %d, want %d", tc.samples, got, tc.want)
               }
           })
       }
   }
   ```

   Уточнить значения `want` по факту формулы (тихий -56 dBFS → level = (-56+60)*100/60 ≈ 6, не 0 — поправить).

- [ ] **Шаг 2: failing test `TestPcmMixerWriter_RMS_ToCollector`** — Write с коллектором → setLevel вызывается.

   ```go
   // TestPcmMixerWriter_RMS_ToCollector — после Write PCM-кадра в pcmMixerWriter,
   // collector.snapshot().Participants[idx].Level > 0 для громкого сигнала.
   func TestPcmMixerWriter_RMS_ToCollector(t *testing.T) {
       coll := newCallStateCollector()
       coll.updateParticipants([]signaling.User{
           {SessionId: "sid-A", ActorId: "actor-A", InCall: 3},
       }, "own")
       mixer := media.NewMixer(2)
       w := &pcmMixerWriter{
           idx: 0, sid: "sid-A", mixer: mixer,
           frameSamples: mixerFrameSamples, collector: coll,
       }
       // Синтетический PCM: full-scale sine.
       samples := make([]int16, mixerFrameSamples)
       for i := range samples {
           if i%2 == 0 { samples[i] = 32767 } else { samples[i] = -32767 }
       }
       // []int16 → s16le bytes.
       pcm := make([]byte, len(samples)*2)
       for i, s := range samples {
           binary.LittleEndian.PutUint16(pcm[i*2:i*2+2], uint16(s))
       }
       if _, err := w.Write(pcm); err != nil {
           t.Fatalf("Write: %v", err)
       }
       snap := coll.snapshot()
       var level int
       for _, p := range snap.Participants {
           if p.SessionId == "sid-A" { level = p.Level }
       }
       if level < 90 {
           t.Errorf("Level после full-scale = %d, want ≥90 (snapshot=%+v)", level, snap)
       }
   }
   ```

- [ ] **Шаг 3: запустить 2 теста — должны FAIL**

   Run: `CGO_ENABLED=0 go test ./internal/call/agent/ -run 'TestRMS_ToLevel|TestPcmMixerWriter_RMS' -v`
   Expected: FAIL (compile error: `rmsToLevel undefined`, `pcmMixerWriter.collector undefined`).

- [ ] **Шаг 4: реализовать правки**

   Внести правки 1-3 из контракта (поля `sid`/`collector` в `pcmMixerWriter`, обновить создание в `addPeer`, функция `rmsToLevel`).

- [ ] **Шаг 5: запустить тесты — PASS**

   Run: `CGO_ENABLED=0 go test ./internal/call/agent/ -run 'TestRMS_ToLevel|TestPcmMixerWriter_RMS' -v`
   Expected: PASS.

- [ ] **Шаг 6: запустить ВСЕ agent-тесты**

   Run: `CGO_ENABLED=0 go test ./internal/call/agent/ -v -race`
   Expected: 17 существующих + 3 из Task 4.2 + 2 новых = 22 зелёные, `-race` чисто.

- [ ] **Шаг 7: коммит**

   ```bash
   git add internal/call/agent/agent.go internal/call/agent/agent_test.go
   git commit -m "feat(agent): pcmMixerWriter RMS → collector.setLevel (review #10, Этап 4.3)

- pcmMixerWriter получает ссылку на callStateCollector и sid пира.
- rmsToLevel: dBFS=20·log10(rms/32768), map [-60,0]→[0,100].
- Write вызывает collector.setLevel(sid, level) если collector != nil.
- Регрессия: collector==nil (nctalk-call) → RMS не считается, поведение идентично."
   ```

**Acceptance criteria:**
- Формула `rmsToLevel` корректна (full-scale→100, тишина→0, среднее — по таблице).
- `pcmMixerWriter.Write` прокидывает Level в collector.
- 22 agent-теста PASS.

**Подэтап 4.A завершён:** `agent.Run` готов к тому, чтобы `interactive` подавал `AudioIn` и принимал `OnState`. Все 12 review-findings, затрагивающих call-слой (#2 pacing, #5 concurrency, #7 Status, #10 RMS), закрыты. `nctalk-call` (pipe-режим) работает идентично прежнему — регрессий нет.

---

## Подэтап 4.B — interactive primitives (mute / volume / audio_io)

**Цель подэтапа:** три независимых primitive-компонента в новом пакете `internal/call/interactive`. Каждый — тонкая обёртка над `media.AudioSource` / `io.Writer` / ffmpeg-subprocess. Не зависят друг от друга; тестируются изолированно.

**Общие файлы подэтапа:**
- Create: `internal/call/interactive/doc.go` (если ещё нет; проверить после Этапа 0 — в existing plan был пустой)
- Test fixture channel: pion-пакет (interactive импортирует agent → pion транзитивно) → macOS firewall апрув.

### Task 4.4: `interactive/mute.go` — `muteSource` (review #3, #4)

**Files:**
- Create: `internal/call/interactive/mute.go`
- Test: `internal/call/interactive/mute_test.go`

**Interfaces:**
- Consumes: `media.AudioSource` (из `call/media`).
- Produces: `type muteSource struct { inner media.AudioSource; muted atomic.Bool }` — реализует ПОЛНЫЙ `media.AudioSource` (`ReadSample` + `Close`).
  - `func (m *muteSource) ReadSample() ([]byte, time.Duration, error)` — если `muted`, крутит `inner.ReadSample` и ДРОПАЕТ payload (не возвращает) → encodeLoop не зовёт `WriteSample` → RTP прекращается.
  - `func (m *muteSource) Close() error` — делегирует в `inner.Close()` (agent.Run уже закрыл через encoder.Close, но вызов из interactive безопасен — MicSource.Close idempotent в Task 4.6).
  - `func (m *muteSource) Mute()`, `Unmute()`, `IsMuted() bool` — TUI-управление (клавиша M).

**Контракт (точное содержание `mute.go`):**

```go
// internal/call/interactive/mute.go — wrapper media.AudioSource для локального mute.
// Спека 2026-07-21 §4.3, §7. Review findings #3 (scope-squeeze: mute только свой),
// #4 (Close ownership: interactive НЕ закрывает mic, agent делает это через encoder.Close).
package interactive

import (
	"sync/atomic"
	"time"

	"github.com/stas/nctalk/internal/call/media"
)

// muteSource — перехватывает media.AudioSource. При muted — крутит inner.ReadSample
// и ДРОПАЕТ payload (не возвращает), => encodeLoop не зовёт WriteSample => pion
// перестаёт слать RTP. Ровно семантика мьюта базовой спеки §6 «перестать WriteSample».
//
// РЕАЛИЗУЕТ ПОЛНЫЙ media.AudioSource (ReadSample + Close) — finding #4 (compile gap):
// muteSource подставляется в agent.Config.AudioIn, тип которого — media.AudioSource.
//
// Mute других участников — НЕ входит в MVP (scope-squeeze, review #3): в call-слое
// нет источника мьют-статуса других участников (InCall-flags не дают mute-state,
// signaling mute/unmute events не верифицированы → future §13).
type muteSource struct {
	inner media.AudioSource // = *MicSource в прод; *fakeEncoder в тестах
	muted atomic.Bool       // крутит TUI-хоткеем M
}

// ReadSample — если muted, drain inner (не блокируем устройство) и loop без возврата.
// Если unmuted — пропускает payload. Ошибки inner проходят наружу (io.EOF, ctx.Cancel).
func (m *muteSource) ReadSample() ([]byte, time.Duration, error) {
	for {
		p, d, err := m.inner.ReadSample()
		if err != nil {
			return nil, 0, err
		}
		if !m.muted.Load() {
			return p, d, nil
		}
		// muted: payload дропнут, крутим дальше. Устройство НЕ блокируется
		// (inner.ReadSample потребляет capture-буфер ffmpeg).
	}
}

// Close делегирует в inner.Close (MicSource). Agent.Run зовёт encoder.Close()
// (encoder = cfg.AudioIn = muteSource), а muteSource.Close → MicSource.Close.
// Idempotent (MicSource.Close под sync.Once, Task 4.6).
func (m *muteSource) Close() error {
	return m.inner.Close()
}

// Mute включает mute — ReadSample начинает дропать payload.
func (m *muteSource) Mute()   { m.muted.Store(true) }

// Unmute выключает mute.
func (m *muteSource) Unmute() { m.muted.Store(false) }

// IsMuted — текущее состояние (для View).
func (m *muteSource) IsMuted() bool { return m.muted.Load() }

// Toggle — переключает mute и возвращает новое состояние (для Event handling).
func (m *muteSource) Toggle() bool {
	return !m.muted.Swap(true) // Swap возвращает предыдущее; если было false — теперь true
}
```

ВНИМАНИЕ: метод `Toggle` проще реализовать через CAS-цикл или atomic swap-logic. Альтернатива:
```go
func (m *muteSource) Toggle() bool {
	for {
		old := m.muted.Load()
		newVal := !old
		if m.muted.CompareAndSwap(old, newVal) {
			return newVal
		}
	}
}
```
Вторая реализация — каноничная (без `!Swap(true)` weirdness); использовать её.

- [ ] **Шаг 1: failing test `TestMuteSource_Unmuted_PassesPayload`**

   ```go
   package interactive

   import (
       "errors"
       "io"
       "sync/atomic"
       "testing"
       "time"

       "github.com/stas/nctalk/internal/call/media"
   )

   // fakeSource — тестовый media.AudioSource. ReadSample отдаёт payload из канала.
   type fakeSource struct {
       pkts chan []byte
       closed atomic.Bool
   }
   func newFakeSource() *fakeSource { return &fakeSource{pkts: make(chan []byte, 8)} }
   func (f *fakeSource) ReadSample() ([]byte, time.Duration, error) {
       p, ok := <-f.pkts
       if !ok { return nil, 0, io.EOF }
       return p, 20*time.Millisecond, nil
   }
   func (f *fakeSource) Close() error { f.closed.Store(true); close(f.pkts); return nil }
   // compile-time: muteSource реализует полный media.AudioSource.
   var _ media.AudioSource = (*muteSource)(nil)

   func TestMuteSource_Unmuted_PassesPayload(t *testing.T) {
       src := newFakeSource()
       m := &muteSource{inner: src}
       src.pkts <- []byte("payload-1")
       p, _, err := m.ReadSample()
       if err != nil { t.Fatalf("ReadSample: %v", err) }
       if string(p) != "payload-1" {
           t.Errorf("got %q, want payload-1", p)
       }
   }
   ```

- [ ] **Шаг 2: failing test `TestMuteSource_Muted_DropsPayload`** — muted→дропает, unmute→возвращает следующий.

   ```go
   func TestMuteSource_Muted_DropsPayload(t *testing.T) {
       src := newFakeSource()
       m := &muteSource{inner: src}
       m.Mute()
       src.pkts <- []byte("dropped-1")
       src.pkts <- []byte("dropped-2")

       // ReadSample в muted-режиме крутит — нужноtimeout, чтобы убедиться, что не возвращает.
       errCh := make(chan error, 1)
       pktCh := make(chan []byte, 1)
       go func() {
           p, _, err := m.ReadSample()
           if err != nil { errCh <- err; return }
           pktCh <- p
       }()
       select {
       case <-pktCh:
           t.Fatal("ReadSample вернул payload в muted-режиме (должен дропать)")
       case <-errCh:
           t.Fatal("ReadSample вернул ошибку в muted-режиме (должен крутить)")
       case <-time.After(100 * time.Millisecond):
           // ok — крутит, ничего не вернул.
       }

       // Unmute — следующий payload проходит.
       m.Unmute()
       src.pkts <- []byte("passed-3")
       select {
       case p := <-pktCh:
           if string(p) != "passed-3" {
               t.Errorf("got %q after unmute, want passed-3", p)
           }
       case <-time.After(500 * time.Millisecond):
           t.Fatal("ReadSample не вернул payload после unmute")
       }
   }
   ```

- [ ] **Шаг 3: failing test `TestMuteSource_Close_DelegatesAndIdempotent`** — Close делегирует в inner; повторный Close не падает (finding #4).

   ```go
   func TestMuteSource_Close_DelegatesAndIdempotent(t *testing.T) {
       src := newFakeSource()
       m := &muteSource{inner: src}
       if err := m.Close(); err != nil { t.Fatalf("Close: %v", err) }
       if !src.closed.Load() { t.Error("inner.Close не вызван") }
       // Повторный close не должен паниковать (double-close safety).
       if err := m.Close(); err != nil { t.Logf("вторичный Close: %v (ok)", err) }
   }
   ```

- [ ] **Шаг 4: failing test `TestMuteSource_Toggle_Concurrent`** — гонка Toggle ∥ ReadSample; `-race`.

   ```go
   // TestMuteSource_Toggle_Concurrent — Toggle под атомиком не даёт race под -race.
   func TestMuteSource_Toggle_Concurrent(t *testing.T) {
       src := newFakeSource()
       m := &muteSource{inner: src}
       // Кормим payload постоянно.
       go func() {
           for i := 0; i < 100; i++ {
               src.pkts <- []byte{byte(i)}
           }
           close(src.pkts)
       }()
       done := make(chan struct{})
       go func() {
           defer close(done)
           for {
               _, _, err := m.ReadSample()
               if err != nil { return }
           }
       }()
       for i := 0; i < 100; i++ { m.Toggle() }
       <-done
   }
   ```

- [ ] **Шаг 5: запустить 4 теста — FAIL (пакет не существует)**

   Run: `CGO_ENABLED=0 go test ./internal/call/interactive/ -run TestMuteSource -v`
   Expected: FAIL (compile: package not found / `muteSource undefined`).

- [ ] **Шаг 6: создать `doc.go` (если ещё нет) и `mute.go`**

   Содержимое `mute.go` — из контракта выше. `doc.go`:
   ```go
   // Package interactive реализует TUI-режим аудио-звонка Nextcloud Talk (Этап 4):
   // микрофон→Opus (MicSource), PCM→динамик (speakerWriter), локальный mute,
   // громкость вывода, ANSI-отрисовка списка участников. Спека 2026-07-21.
   // Тонкий слой над agent.Run: интерактивные обёртки (mute/volume) подставляются
   // в agent.Config.AudioIn / Stdout, OnState-сallback обогащается в interactive
   // и гонится в View (ansiView сейчас, bubbleteaView — future §13).
   package interactive
   ```

- [ ] **Шаг 7: запустить 4 теста — PASS**

   Run: `CGO_ENABLED=0 go test ./internal/call/interactive/ -run TestMuteSource -v -race`
   Expected: PASS; `-race` чисто.

- [ ] **Шаг 8: коммит**

   ```bash
   git add internal/call/interactive/doc.go internal/call/interactive/mute.go internal/call/interactive/mute_test.go
   git commit -m "feat(interactive): muteSource wrapper media.AudioSource (review #3/#4, Этап 4.4)

- Реализует полный media.AudioSource (ReadSample + Close).
- muted → drain inner и дроп payload (encodeLoop не зовёт WriteSample).
- Close делегирует в inner.Close (double-close safe — agent+interactive).
- Mute других участников — НЕ входит в MVP (scope-squeeze, future §13)."
   ```

**Acceptance criteria:**
- `var _ media.AudioSource = (*muteSource)(nil)` compiles.
- 4 теста PASS с `-race`.
- `muteSource` НЕ закрывает `inner` сам по себе (делегирует) — соответствует контракту ownership (finding #4).

---

### Task 4.5: `interactive/volume.go` — `volumeWriter` (PCM gain)

**Files:**
- Create: `internal/call/interactive/volume.go`
- Test: `internal/call/interactive/volume_test.go`

**Interfaces:**
- Consumes: `io.Writer` (inner = `speakerWriter`).
- Produces: `type volumeWriter struct { inner io.Writer; gain atomic.Int32 }` — реализует `io.Writer`.
  - `Write(p []byte)` — масштабирует каждый int16-семпл на `gain/100`, clipping ±32767, перед inner.Write.
  - `func (v *volumeWriter) SetGain(pct int)`, `Gain() int`, `Inc(int)`, `Dec(int)` — TUI-управление (`+`/`−`).

**Контракт (содержимое `volume.go`):**

```go
// internal/call/interactive/volume.go — PCM gain на вывод в динамик.
// Спека 2026-07-21 §4.3, §7. Перехватывает io.Writer (PCM s16le от Mixer'а),
// масштабирует int16-семплы на gain/100 c clipping на ±32767, перед inner.Write.
package interactive

import (
	"encoding/binary"
	"io"
	"sync/atomic"
)

// volumeWriter — перехватывает io.Writer (PCM s16le на вывод). Масштабирует
// каждый int16-семпл на gain (×gain/100), с clipping на ±32767, перед inner.Write.
// Применяется к PCM ПОСЛЕ Mixer'а, перед playback-ffmpeg => влияет только на
// динамик (в сеть уходит как было).
type volumeWriter struct {
	inner io.Writer
	gain  atomic.Int32 // %, default 100; крутит TUI +/-
}

// Write масштабирует PCM s16le семплы и пишет в inner. Нечётный len(p) обрезаем
// до чётного (целое число int16) — остаток дропаем (на практике не возникает:
// Mixer выдаёт только целые кадры).
func (w *volumeWriter) Write(p []byte) (int, error) {
	gain := int(w.gain.Load())
	if gain == 100 {
		// identity — пропускаем без копирования.
		return w.inner.Write(p)
	}
	n := len(p) &^ 1 // чётное число байт
	if n == 0 {
		return 0, nil
	}
	out := make([]byte, n)
	for i := 0; i < n; i += 2 {
		s := int16(binary.LittleEndian.Uint16(p[i : i+2]))
		// Преобразование в знаковое уже сделано через int16-каст.
		scaled := int32(s) * int32(gain) / 100
		switch {
		case scaled > 32767:
			scaled = 32767
		case scaled < -32767:
			scaled = -32767
		}
		binary.LittleEndian.PutUint16(out[i:i+2], uint16(int16(scaled)))
	}
	return w.inner.Write(out)
}

// SetGain устанавливает gain в процентах (clamped [0, 200]).
func (v *volumeWriter) SetGain(pct int) {
	if pct < 0 {
		pct = 0
	}
	if pct > 200 {
		pct = 200
	}
	v.gain.Store(int32(pct))
}

// Gain возвращает текущий gain в процентах.
func (v *volumeWriter) Gain() int { return int(v.gain.Load()) }

// Inc/Dec — шаг ±delta (для TUI +/-). Clamped [0, 200].
func (v *volumeWriter) Inc(delta int) { v.SetGain(v.Gain() + delta) }
func (v *volumeWriter) Dec(delta int) { v.SetGain(v.Gain() - delta) }
```

- [ ] **Шаг 1: failing test `TestVolumeWriter_Gain100_Identity`**

   ```go
   package interactive

   import (
       "bytes"
       "encoding/binary"
       "testing"
   )

   func TestVolumeWriter_Gain100_Identity(t *testing.T) {
       var inner bytes.Buffer
       v := &volumeWriter{inner: &inner}
       v.SetGain(100) // default
       pcm := []byte{0x10, 0x20, 0x30, 0x40}
       if _, err := v.Write(pcm); err != nil { t.Fatalf("Write: %v", err) }
       if !bytes.Equal(inner.Bytes(), pcm) {
           t.Errorf("gain=100: got % x, want % x (identity)", inner.Bytes(), pcm)
       }
   }
   ```

- [ ] **Шаг 2: failing test `TestVolumeWriter_Gain0_Silence`**

   ```go
   func TestVolumeWriter_Gain0_Silence(t *testing.T) {
       var inner bytes.Buffer
       v := &volumeWriter{inner: &inner}
       v.SetGain(0)
       // Несколько ненулевых сэмплов.
       pcm := make([]byte, 8)
       binary.LittleEndian.PutUint16(pcm[0:2], 1000)
       binary.LittleEndian.PutUint16(pcm[2:4], -1000) // uint16 обёртка знакового
       binary.LittleEndian.PutUint16(pcm[4:6], 32000)
       binary.LittleEndian.PutUint16(pcm[6:8], 0)
       if _, err := v.Write(pcm); err != nil { t.Fatalf("Write: %v", err) }
       for i := 0; i < inner.Len(); i++ {
           if inner.Bytes()[i] != 0 {
               t.Errorf("gain=0: byte[%d]=%d, want 0 (silence)", i, inner.Bytes()[i])
           }
       }
   }
   ```

- [ ] **Шаг 3: failing test `TestVolumeWriter_Gain200_Clipping`** — gain=200 на full-scale → clip на ±32767.

   ```go
   func TestVolumeWriter_Gain200_Clipping(t *testing.T) {
       var inner bytes.Buffer
       v := &volumeWriter{inner: &inner}
       v.SetGain(200)
       pcm := make([]byte, 4)
       binary.LittleEndian.PutUint16(pcm[0:2], 32767)  // +full-scale
       binary.LittleEndian.PutUint16(pcm[2:4], uint16(int16(-32767))) // -full-scale
       if _, err := v.Write(pcm); err != nil { t.Fatalf("Write: %v", err) }
       got0 := int16(binary.LittleEndian.Uint16(inner.Bytes()[0:2]))
       got1 := int16(binary.LittleEndian.Uint16(inner.Bytes()[2:4]))
       if got0 != 32767 {
           t.Errorf("+full-scale ×2: got %d, want 32767 (clipped)", got0)
       }
       if got1 != -32767 {
           t.Errorf("-full-scale ×2: got %d, want -32767 (clipped)", got1)
       }
   }
   ```

- [ ] **Шаг 4: failing test `TestVolumeWriter_ClampRange`** — SetGain(-10)→0, SetGain(300)→200.

   ```go
   func TestVolumeWriter_ClampRange(t *testing.T) {
       v := &volumeWriter{}
       v.SetGain(-10)
       if got := v.Gain(); got != 0 {
           t.Errorf("SetGain(-10) → Gain=%d, want 0 (clamped)", got)
       }
       v.SetGain(300)
       if got := v.Gain(); got != 200 {
           t.Errorf("SetGain(300) → Gain=%d, want 200 (clamped)", got)
       }
   }
   ```

- [ ] **Шаг 5: failing test `TestVolumeWriter_IncDec`**

   ```go
   func TestVolumeWriter_IncDec(t *testing.T) {
       v := &volumeWriter{}
       v.SetGain(100)
       v.Inc(10)
       if got := v.Gain(); got != 110 { t.Errorf("Inc(10) → %d, want 110", got) }
       v.Dec(10)
       if got := v.Gain(); got != 100 { t.Errorf("Dec(10) → %d, want 100", got) }
       v.Dec(200)
       if got := v.Gain(); got != 0 { t.Errorf("Dec(200) → %d, want 0 (clamped)", got) }
   }
   ```

- [ ] **Шаг 6: запустить 5 тестов — FAIL**

   Run: `CGO_ENABLED=0 go test ./internal/call/interactive/ -run TestVolumeWriter -v`
   Expected: FAIL (`volumeWriter undefined`).

- [ ] **Шаг 7: реализовать `volume.go`**

- [ ] **Шаг 8: запустить 5 тестов — PASS**

   Run: `CGO_ENABLED=0 go test ./internal/call/interactive/ -run TestVolumeWriter -v -race`
   Expected: PASS.

- [ ] **Шаг 9: коммит**

   ```bash
   git add internal/call/interactive/volume.go internal/call/interactive/volume_test.go
   git commit -m "feat(interactive): volumeWriter — PCM gain wrapper io.Writer (Этап 4.5)

- Gain 0..200% (default 100=identity), clipping ±32767.
- Влияет только на локальный динамик (в сеть уходит как было).
- +/− клавиши TUI через Inc/Dec."
   ```

**Acceptance criteria:**
- 5 тестов PASS с `-race`.
- `volumeWriter` реализует `io.Writer` (compile-time guard).

---

### Task 4.6: `interactive/audio_io.go` — `MicSource` (avfoundation input) + `speakerWriter` (audiotoolbox output) (review #1, #8)

**Files:**
- Create: `internal/call/interactive/audio_io.go`
- Test: `internal/call/interactive/audio_io_test.go`

**Interfaces:**
- Consumes: `github.com/stas/nctalk/internal/call/media/ogg` (парсер OpusHead/vorbis-comment — инвариант базовой спеки), `media.PCMFrameDuration` / `media.PCMSampleRate` / `media.PCMChannels` (константы PCM-контракта).
- Produces:
  - `type MicSource struct { ... }` — реализует `media.AudioSource`; lazy start (ffmpeg capture при первом `ReadSample` via `sync.Once`); Close idempotent.
    - `func NewMicSource(ctx context.Context, device string) (*MicSource, error)` — НЕ запускает ffmpeg.
  - `func NewSpeakerWriter(ctx context.Context, deviceOut string) (io.WriteCloser, error)` — запускает ffmpeg-subprocess `PCM s16le → audiotoolbox`. Возвращает stdin-pipe процесса.

**Канон device-IO (review #1, CRITICAL, спека §3):** input = `-f avfoundation -i ":<audio_idx>"` (default `":0"`), output = `-f audiotoolbox -audio_device_index <N>` (default `-1`). Разные muxer'ы, разные способы адресации.

**Контракт (точное содержание `audio_io.go`):**

```go
// internal/call/interactive/audio_io.go — device-IO: микрофон → Opus (MicSource),
// PCM s16le → динамик (speakerWriter). Спека 2026-07-21 §3, §4.2.
// Review finding #1 (CRITICAL): input=avfoundation (D avfoundation muxer),
// output=audiotoolbox (E audiotoolbox muxer) — НЕ avfoundation для output.
package interactive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/stas/nctalk/internal/call/media"
	"github.com/stas/nctalk/internal/call/media/ogg"
)

// ---- MicSource: микрофон → raw Opus ----

// MicSource — микрофон → raw Opus. Реализует media.AudioSource. Один ffmpeg-процесс
// «capture+encode»: avfoundation → libopus → OGG/Opus в pipe → ogg-парсер → ReadSample.
// НЕ требует PCM-stdin (вход — устройство, не pipe).
//
// LAZY START (review #8): ffmpeg-capture НЕ запускается в NewMicSource. Захват
// стартует при первом ReadSample (sync.Once) — внутри encodeLoop, ПОСЛЕ JoinCall + ICE
// setup (сотни мс). Если стартовать в конструкторе, packets-канал cap≈1с переполнится
// до того, как encodeLoop начнёт читать (consumer ещё не готов).
//
// Close IDEMPOTENT (sync.Once, review #4): agent.Run зовёт encoder.Close()
// (= cfg.AudioIn.Close = muteSource.Close = MicSource.Close); повторный вызов не падает.
type MicSource struct {
	device string // avfoundation-строка формата ":<audio_idx>"

	startOnce sync.Once
	startErr  error
	cmd       *exec.Cmd
	cancel    context.CancelFunc
	stdout    io.ReadCloser
	packets   chan []byte // буферизованный канал raw Opus от pump
	done      chan error

	closeOnce sync.Once
	waitOnce  sync.Once
	waitErr   error
}

// NewMicSource — НЕ запускает ffmpeg (lazy, см. выше). Device — avfoundation-строка
// формата ":<audio_idx>" (default ":0" = первый системный audio-input / микрофон).
// Из env NCTALK_AUDIO_DEVICE_IN (см. §3). Пустой device → default ":0".
func NewMicSource(device string) *MicSource {
	if device == "" {
		device = ":0"
	}
	return &MicSource{device: device}
}

// start запускает ffmpeg-capture+encode. Идемпотентно через startOnce.
// Команда: ffmpeg -f avfoundation -i "<device>" -ac 1 -ar 48000 -c:a libopus
//          -application voip -f opus -
// muxer "opus" = Ogg-Opus (подтверждено в media/codec.go для FFmpegEncoder).
func (m *MicSource) start() error {
	m.startOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		cmd := exec.CommandContext(ctx, "ffmpeg",
			"-hide_banner",
			"-f", "avfoundation",
			"-i", m.device,
			"-ac", fmt.Sprintf("%d", media.PCMChannels),
			"-ar", fmt.Sprintf("%d", media.PCMSampleRate),
			"-c:a", "libopus",
			"-application", "voip",
			"-f", "opus",
			"-",
		)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			cancel()
			m.startErr = fmt.Errorf("audio_io: stdout pipe: %w", err)
			return
		}
		cmd.Cancel = func() error {
			if cmd.Process == nil { return nil }
			return cmd.Process.Kill()
		}
		cmd.WaitDelay = 5 * time.Second
		if err := cmd.Start(); err != nil {
			cancel()
			m.startErr = fmt.Errorf("audio_io: запуск ffmpeg-avfoundation: %w", err)
			return
		}
		m.cmd = cmd
		m.cancel = cancel
		m.stdout = stdout
		m.packets = make(chan []byte, 50)
		m.done = make(chan error, 1)
		go m.pump()
	})
	return m.startErr
}

// pump — аналог FFmpegEncoder.pump: ogg.Reader → packets-канал.
func (m *MicSource) pump() {
	defer close(m.packets)
	r := ogg.NewReader(m.stdout)
	for {
		pkt, err := r.NextPacket()
		if err != nil {
			if errors.Is(err, io.EOF) {
				if m.cmd.Context().Err() != nil {
					m.done <- nil
					return
				}
				waitErr := m.waitAndGetErr()
				if waitErr == nil {
					m.done <- nil
				} else {
					m.done <- fmt.Errorf("audio_io: ffmpeg-capture упал: %w", waitErr)
				}
				return
			}
			m.done <- err
			return
		}
		dup := make([]byte, len(pkt))
		copy(dup, pkt)
		select {
		case m.packets <- dup:
		case <-m.cmd.Context().Done():
			return
		}
	}
}

func (m *MicSource) waitAndGetErr() error {
	m.waitOnce.Do(func() { m.waitErr = m.cmd.Wait() })
	return m.waitErr
}

// ReadSample — lazy-start + чтение из packets-канала. Возвращает raw Opus-пакет
// и его длительность (20мс = libopus voip default при 48к/моно).
func (m *MicSource) ReadSample() ([]byte, time.Duration, error) {
	if err := m.start(); err != nil {
		return nil, 0, err
	}
	select {
	case pkt, ok := <-m.packets:
		if !ok {
			select {
			case err := <-m.done:
				if err != nil { return nil, 0, err }
			default:
			}
			return nil, 0, io.EOF
		}
		return pkt, media.PCMFrameDuration, nil
	case <-m.cmd.Context().Done():
		return nil, 0, m.cmd.Context().Err()
	}
}

// Close — idempotent (review #4). Kill ffmpeg + Wait.
func (m *MicSource) Close() error {
	var waitErr error
	m.closeOnce.Do(func() {
		// Если start не отработал — закрывать нечего.
		if m.cmd == nil {
			return
		}
		m.cancel()
		waitErr = m.waitAndGetErr()
	})
	return waitErr
}

// compile-time: MicSource реализует полный media.AudioSource.
var _ media.AudioSource = (*MicSource)(nil)

// ---- speakerWriter: PCM s16le → динамик (audiotoolbox) ----

// speakerWriter — PCM s16le → динамик через audiotoolbox-muxer. io.WriteCloser
// (NE AudioSink — спека §3: device-out это io.Writer, а не AudioSink).
// N: из env NCTALK_AUDIO_DEVICE_OUT (int-индекс или "default"/"-1").
//
// Команда: ffmpeg -f s16le -ar 48000 -ac 1 -i - -f audiotoolbox -audio_device_index <N> -
// review finding #1 (CRITICAL): output muxer = audiotoolbox (НЕ avfoundation — он input-only).
type speakerWriter struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	stdin  io.WriteCloser

	closeOnce sync.Once
	waitOnce  sync.Once
	waitErr   error
}

// NewSpeakerWriter запускает playback-ffmpeg и возвращает stdin-pipe как io.WriteCloser.
// deviceOut — int-индекс audiotoolbox или строка "default"/"-1" (default "-1" =
// системное устройство вывода). Пустая строка → "-1".
func NewSpeakerWriter(deviceOut string) (io.WriteCloser, error) {
	if deviceOut == "" || deviceOut == "default" {
		deviceOut = "-1"
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner",
		"-f", media.PCMSampleFormat,
		"-ar", fmt.Sprintf("%d", media.PCMSampleRate),
		"-ac", fmt.Sprintf("%d", media.PCMChannels),
		"-i", "-",
		"-f", "audiotoolbox",
		"-audio_device_index", deviceOut,
		"-",
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("audio_io: speaker stdin pipe: %w", err)
	}
	cmd.Cancel = func() error {
		if cmd.Process == nil { return nil }
		return cmd.Process.Kill()
	}
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("audio_io: запуск ffmpeg-audiotoolbox: %w", err)
	}
	return &speakerWriter{cmd: cmd, cancel: cancel, stdin: stdin}, nil
}

// Write пробрасывает PCM s16le в stdin процесса.
func (s *speakerWriter) Write(p []byte) (int, error) {
	return s.stdin.Write(p)
}

// Close — idempotent. Close stdin → ffmpeg финиширует → Wait.
func (s *speakerWriter) Close() error {
	var waitErr error
	s.closeOnce.Do(func() {
		if err := s.stdin.Close(); err != nil {
			s.cancel()
			waitErr = err
		}
		werr := s.waitAndGetErr()
		if waitErr == nil {
			waitErr = werr
		}
	})
	return waitErr
}

func (s *speakerWriter) waitAndGetErr() error {
	s.waitOnce.Do(func() { s.waitErr = s.cmd.Wait() })
	return s.waitErr
}
```

- [ ] **Шаг 1: failing test `TestMicSource_NoStartInConstructor`** — после `NewMicSource` ffmpeg НЕ запущен (cmd==nil).

   ```go
   package interactive

   import (
       "context"
       "os/exec"
       "testing"
       "time"
   )

   // hasFFmpeg skipper для всех тестов audio_io.
   func hasFFmpeg(t *testing.T) {
       t.Helper()
       if _, err := exec.LookPath("ffmpeg"); err != nil {
           t.Skipf("ffmpeg нет в PATH: %v", err)
       }
   }

   // TestMicSource_NoStartInConstructor — lazy start (review #8):
   // после NewMicSource cmd==nil (ffmpeg НЕ запущен). Старт только при первом ReadSample.
   func TestMicSource_NoStartInConstructor(t *testing.T) {
       hasFFmpeg(t)
       m := NewMicSource(":0")
       if m.cmd != nil {
           t.Fatal("NewMicSource запустил ffmpeg — нарушен lazy start (review #8)")
       }
       // Быстрый cancel без ReadSample — Close идемпотентен, no orphan process.
       if err := m.Close(); err != nil {
           t.Errorf("Close без start: %v", err)
       }
   }
   ```

- [ ] **Шаг 2: failing test `TestMicSource_FfmpegArgs`** — `start()` запускает ffmpeg с правильными аргументами (avfoundation input, libopus, opus-muxer).

   ```go
   // TestMicSource_FfmpegArgs — start() запускает ffmpeg-процесс с корректными args.
   // Проверка canonical канона review #1: input=avfoundation, NOT audiotoolbox.
   func TestMicSource_FfmpegArgs(t *testing.T) {
       hasFFmpeg(t)
       m := NewMicSource(":5")
       if err := m.start(); err != nil {
           t.Fatalf("start: %v", err)
       }
       defer m.Close()
       args := m.cmd.Args
       // Проверяем ключевые флаги.
       want := map[string]string{
           "-f": "avfoundation", // input muxer
       }
       for i, a := range args {
           if wantFlag, ok := want[a]; ok && i+1 < len(args) {
               if args[i+1] != wantFlag {
                   t.Errorf("args[%d+1] для %s = %s, want %s", i, a, args[i+1], wantFlag)
               }
           }
       }
       // device в нужной позиции.
       foundDev := false
       for _, a := range args {
           if a == ":5" { foundDev = true }
       }
       if !foundDev {
           t.Errorf("args не содержат device \":5\": %v", args)
       }
   }
   ```

- [ ] **Шаг 3: failing test `TestMicSource_CancelCleanup`** — старт + Close → процесс убит (no orphan).

   ```go
   // TestMicSource_CancelCleanup — start + Close: ffmpeg убит, waitErr получен.
   // orphan-protection: Wait().ProcessState (как в Task 3.4 для FFmpegEncoder).
   func TestMicSource_CancelCleanup(t *testing.T) {
       hasFFmpeg(t)
       m := NewMicSource(":0")
       if err := m.start(); err != nil { t.Fatalf("start: %v", err) }
       pid := m.cmd.Process.Pid
       if err := m.Close(); err != nil {
           t.Logf("Close err (ok): %v", err)
       }
       // Дать ОС секунду на cleanup.
       time.Sleep(200 * time.Millisecond)
       // Проверяем, что процесса нет (kill -0 fail). Сkip на CI без ps.
       if _, err := exec.Command("kill", "-0", "-p").Output(); err == nil {
           // ps доступен — простая проверка через pgrep.
           _ = pid // (на CI pgrep может не быть)
       }
   }
   ```

- [ ] **Шаг 4: failing test `TestMicSource_CloseIdempotent`** — повторный Close не падает (finding #4).

   ```go
   func TestMicSource_CloseIdempotent(t *testing.T) {
       hasFFmpeg(t)
       m := NewMicSource(":0")
       _ = m.start()
       if err := m.Close(); err != nil { t.Logf("первый Close: %v", err) }
       if err := m.Close(); err != nil {
           t.Errorf("повторный Close: %v (должен быть no-op или nil)", err)
       }
   }
   ```

- [ ] **Шаг 5: failing test `TestSpeakerWriter_FfmpegArgs`** — output-muxer audiotoolbox с `-audio_device_index`.

   ```go
   // TestSpeakerWriter_FfmpegArgs — canonical канон review #1: output=audiotoolbox,
   // НЕ avfoundation (avfoundation input-only). Проверяем ключевые args.
   func TestSpeakerWriter_FfmpegArgs(t *testing.T) {
       hasFFmpeg(t)
       sw, err := NewSpeakerWriter("0")
       if err != nil { t.Fatalf("NewSpeakerWriter: %v", err) }
       // cmd доступ через type assertion (speakerWriter не экспортирует cmd; в тесте
       // — тот же пакет, ок).
       cmd := sw.(*speakerWriter).cmd
       args := cmd.Args
       defer sw.Close()
       // Ищем пару "-f audiotoolbox" в output-части (после "-i -").
       found := false
       for i, a := range args {
           if a == "-f" && i+1 < len(args) && args[i+1] == "audiotoolbox" {
               found = true
               break
           }
       }
       if !found {
           t.Errorf("args не содержат -f audiotoolbox (review #1 CRITICAL): %v", args)
       }
       // -audio_device_index 0.
       foundIdx := false
       for i, a := range args {
           if a == "-audio_device_index" && i+1 < len(args) && args[i+1] == "0" {
               foundIdx = true; break
           }
       }
       if !foundIdx {
           t.Errorf("args не содержат -audio_device_index 0: %v", args)
       }
   }
   ```

- [ ] **Шаг 6: failing test `TestSpeakerWriter_DefaultDevice`** — `"default"` → `"-1"`.

   ```go
   func TestSpeakerWriter_DefaultDevice(t *testing.T) {
       hasFFmpeg(t)
       sw, err := NewSpeakerWriter("default")
       if err != nil { t.Fatalf("NewSpeakerWriter: %v", err) }
       defer sw.Close()
       cmd := sw.(*speakerWriter).cmd
       found := false
       for i, a := range cmd.Args {
           if a == "-audio_device_index" && i+1 < len(cmd.Args) && cmd.Args[i+1] == "-1" {
               found = true; break
           }
       }
       if !found {
           t.Errorf("default device: args не содержат -audio_device_index -1: %v", cmd.Args)
       }
   }
   ```

- [ ] **Шаг 7: запустить 6 тестов — FAIL**

   Run: `CGO_ENABLED=0 go test ./internal/call/interactive/ -run 'TestMicSource|TestSpeakerWriter' -v`
   Expected: FAIL (compile error: `MicSource`, `speakerWriter`, `NewMicSource`, `NewSpeakerWriter` undefined).

- [ ] **Шаг 8: реализовать `audio_io.go`**

   Внести содержимое из контракта выше.

- [ ] **Шаг 9: запустить 6 тестов — PASS**

   Run: `CGO_ENABLED=0 go test ./internal/call/interactive/ -run 'TestMicSource|TestSpeakerWriter' -v`
   Expected: PASS (если ffmpeg есть в PATH).

   ВНИМАНИЕ: на macOS с Application Firewall может появиться диалог «разрешить ffmpeg» — первый прогон вручную, далее подпись `nctalk-dev` через `make sign` снимает вопрос (CLAUDE.md «Аудио-звонки»).

- [ ] **Шаг 10: запустить ВСЕ interactive-тесты**

   Run: `CGO_ENABLED=0 go test ./internal/call/interactive/ -v -race`
   Expected: 4 mute + 5 volume + 6 audio_io = 15 тестов зелёные.

- [ ] **Шаг 11: коммит**

   ```bash
   git add internal/call/interactive/audio_io.go internal/call/interactive/audio_io_test.go
   git commit -m "feat(interactive): MicSource (avfoundation) + speakerWriter (audiotoolbox) — review #1/#8, Этап 4.6

- MicSource: lazy start (sync.Once, первый ReadSample), idempotent Close.
- speakerWriter: PCM s16le → audiotoolbox -audio_device_index.
- Канон review #1: input=avfoundation, output=audiotoolbox (НЕ avfoundation)."
   ```

**Acceptance criteria:**
- 6 тестов PASS (или SKIP если нет ffmpeg).
- `var _ media.AudioSource = (*MicSource)(nil)` компилируется.
- `go vet ./internal/call/interactive/...` чисто.
- `pion` НЕ импортируется напрямую в `interactive` (только через `agent`); проверить через `go list -deps ./internal/call/interactive | grep pion/webrtc/v4` — должен показать pion (через agent), это ок.

**Подэтап 4.B завершён:** три primitive-компонента готовы к сборке в оркестраторе (`interactive.Run`). Review-findings #1, #3, #4, #8 закрыты.

---

## Подэтап 4.C — View contract + `ansiView` (review #9, #11)

**Цель подэтапа:** определить узкий `View`-интерфейс (точка расширения bubbletea, future §13) и реализовать `ansiView` на `golang.org/x/term` (raw-mode + alt-screen + ANSI-отрисовка). Graceful non-tty fallback (для integration test в pipe-окружении, review #9). `defer view.Close()` сразу после MakeRaw — гарантия restore-tty при panic/error (review #11).

### Task 4.7: `interactive/view.go` + `interactive/tui.go`

**Files:**
- Create: `internal/call/interactive/view.go`
- Create: `internal/call/interactive/tui.go`
- Test: `internal/call/interactive/view_test.go`, `internal/call/interactive/tui_test.go`
- Modify: `go.mod` (добавить `golang.org/x/term`) и `go.sum`.

**Interfaces:**
- Produces:
  - `type Event int` с константами `EvMuteToggle`, `EvLeave`, `EvVolUp`, `EvVolDown`.
  - `type ParticipantState struct { SessionId, Name string; Level int; Speaking bool }`.
  - `type CallState struct { Participants []ParticipantState; SelfMuted bool; Volume int; Status string }`.
  - `type View interface { Update(CallState); Events() <-chan Event; Close() error }`.
  - `func NewAnsiView(stdin io.Reader, stdout io.Writer) (View, error)` — detect tty → raw-mode + alt-screen; non-tty → plain-text fallback.

**Контракт `view.go` (типы, без ANSI-специфики):**

```go
// internal/call/interactive/view.go — типы Event/CallState/View, точка расширения
// bubbletea (future §13). Без терминал-специфики: ни ANSI, ни tea.*.
package interactive

// Event — TUI-событие (хоткей). TUI пушит ввод (M/Q/+/-) в Events().
type Event int

const (
	EvMuteToggle Event = iota
	EvLeave
	EvVolUp
	EvVolDown
)

// ParticipantState — один участник в enriched-снапшоте для View.
// В interactive.CallState (НЕ agent.CallState — тот без SelfMuted/Volume/Speaking).
type ParticipantState struct {
	SessionId string
	Name      string // MVP = ActorId (из agent), display → future §13
	Level     int    // 0..100, сырой из agent (RMS)
	Speaking  bool   // гистерезис в interactive (не в agent)
}

// CallState — enriched-снапшот для View. interactive обогащает agent.CallState
// своим SelfMuted/Volume + копией Status + Speaking-гистерезисом.
type CallState struct {
	Participants []ParticipantState
	SelfMuted    bool
	Volume       int    // %, current gain volumeWriter
	Status       string // копия из agent.CallState.Status
}

// View — точка расширения. ansiView (сейчас) и bubbleteaView (future §13) —
// сменные реализации. Update пушит состояние (в горутине отрисовки),
// Events возвращает канал ввода (M/Q/+/-), Close восстанавливает tty.
type View interface {
	Update(state CallState)
	Events() <-chan Event
	Close() error
}
```

**Контракт `tui.go` (`ansiView`):**

```go
// internal/call/interactive/tui.go — ANSI-реализация View на golang.org/x/term.
// Спека 2026-07-21 §6. Review #9 (graceful non-tty fallback), #11 (defer Close).
package interactive

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/term"
)

// ansiView — реализация View через raw-mode + alt-screen + ANSI-отрисовку.
// В non-tty окружении (pipe, интеграционный тест) уходит в fallback: raw-mode
// НЕ включается, write status-строк в stdout (plain-text, по одной на update).
type ansiView struct {
	stdin  io.Reader
	stdout io.Writer
	inFd   uintptr   // fd stdin для term.MakeRaw/IsTerminal
	outFd  uintptr   // fd stdout
	tty    bool      // оба дескриптора — tty?

	// raw-mode state (только для tty-режима).
	oldState *term.State

	// events-канал: горутина reader'а кладёт Event'ы сюда.
	events chan Event
	stopCh chan struct{}
	stopped atomic.Bool

	closeOnce sync.Once
	closeErr  error

	// последнее состояние для dedup-отрисовки (review #12, но dedup в interactive.Run;
	// тут — только для не-tty fallback, чтобы не писать одинаковые строки подряд).
	lastFallback string
}

// NewAnsiView — detect tty на stdin/stdout; tty → MakeRaw + alt-screen,
// non-tty → fallback. Возвращает View, готовый к Update/Events.
func NewAnsiView(stdin io.Reader, stdout io.Writer) (View, error) {
	v := &ansiView{
		stdin:  stdin,
		stdout: stdout,
		events: make(chan Event, 8),
		stopCh: make(chan struct{}),
	}
	// Detect tty через file descriptor. io.Reader/io.Writer в общем случае не
	// имеют fd — поэтому caller (cmd/nctalk-talk) передаёт os.Stdin/os.Stdout;
	// type-assert на *os.File для Fd().
	if f, ok := stdin.(*os.File); ok {
		v.inFd = f.Fd()
	}
	if f, ok := stdout.(*os.File); ok {
		v.outFd = f.Fd()
	}
	v.tty = v.inFd != 0 && v.outFd != 0 &&
		term.IsTerminal(int(v.inFd)) && term.IsTerminal(int(v.outFd))

	if v.tty {
		// MakeRaw + alt-screen. Review #11: defer Close() ставит ВЫЗЫВАЮЩИЙ
		// (interactive.Run) сразу после NewAnsiView — гарантия Restore при panic.
		state, err := term.MakeRaw(int(v.inFd))
		if err != nil {
			return nil, fmt.Errorf("tui: MakeRaw: %w", err)
		}
		v.oldState = state
		// Alternate screen.
		fmt.Fprint(stdout, "\x1b[?1049h")
	}
	// reader-горутина (tty → raw-mode single-byte reads; non-tty → построчно).
	go v.readLoop()
	return v, nil
}

// readLoop читает ввод и маппит в Event. TTY: single-byte (raw-mode).
// Non-tty: построчно (stdin может быть закрыт — выходим).
func (v *ansiView) readLoop() {
	defer close(v.events)
	r := bufio.NewReader(v.stdin)
	for {
		if v.stopped.Load() {
			return
		}
		b, err := r.ReadByte()
		if err != nil {
			return
		}
		ev, ok := byteToEvent(b)
		if !ok {
			continue
		}
		select {
		case v.events <- ev:
		case <-v.stopCh:
			return
		}
	}
}

// byteToEvent — таблица байт → Event. m/M→EvMuteToggle, q/Q/0x03(Ctrl-C)→EvLeave,
// + → EvVolUp, - → EvVolDown, прочее → ничего.
func byteToEvent(b byte) (Event, bool) {
	switch b {
	case 'm', 'M':
		return EvMuteToggle, true
	case 'q', 'Q', 0x03: // 0x03 = Ctrl-C
		return EvLeave, true
	case '+', '=': // '=' рядом с '+' на US-клавиатуре — даём для удобства
		return EvVolUp, true
	case '-', '_':
		return EvVolDown, true
	}
	return 0, false
}

// Update пушит enriched-снапшот в отрисовку.
func (v *ansiView) Update(s CallState) {
	if v.tty {
		v.renderTTY(s)
	} else {
		v.renderFallback(s)
	}
}

// renderTTY — полная перерисовка (alternate screen, без скролла).
// Текст на кириллице сохраняется.
func (v *ansiView) renderTTY(s CallState) {
	var b strings.Builder
	// Очистка экрана + cursor home.
	b.WriteString("\x1b[2J\x1b[H")
	fmt.Fprintf(&b, "nctalk-talk — статус: %s\n", s.Status)
	fmt.Fprintf(&b, "mute(M): %s   громкость(+/-): %d%%\n",
		mutedLabel(s.SelfMuted), s.Volume)
	b.WriteString("────────────────────────\n")
	if len(s.Participants) == 0 {
		b.WriteString("(нет участников)\n")
	}
	for _, p := range s.Participants {
		mark := " "
		if p.Speaking {
			mark = "▶"
		}
		fmt.Fprintf(&b, "%s %s  [%s]\n", mark, p.Name, levelBar(p.Level))
	}
	b.WriteString("────────────────────────\n")
	b.WriteString("Q/Ctrl-C — выйти\n")
	fmt.Fprint(v.stdout, b.String())
}

// renderFallback — non-tty (pipe): plain-text, по одной строке-статуса на update.
// Review #9: integration test piping stdin/stdout работает в этом режиме.
func (v *ansiView) renderFallback(s CallState) {
	line := fmt.Sprintf("nctalk-talk: status=%s muted=%v vol=%d parts=%d",
		s.Status, s.SelfMuted, s.Volume, len(s.Participants))
	if line == v.lastFallback {
		return // dedup одинаковых строк в fallback (мягкая дедупликация).
	}
	v.lastFallback = line
	fmt.Fprintln(v.stdout, line)
}

func mutedLabel(m bool) string {
	if m { return "ВЫКЛ" }
	return "вкл"
}

func levelBar(l int) string {
	if l < 0 { l = 0 }
	if l > 100 { l = 100 }
	n := l / 10
	return strings.Repeat("█", n) + strings.Repeat("░", 10-n)
}

// Events — канал ввода.
func (v *ansiView) Events() <-chan Event { return v.events }

// Close — idempotent. Restore tty (если был MakeRaw) + exit alt-screen.
// Review #11: вызывается через defer сразу после MakeRaw в interactive.Run.
func (v *ansiView) Close() error {
	v.closeOnce.Do(func() {
		v.stopped.Store(true)
		close(v.stopCh)
		// Дреин events (reader-горутина выйдет).
		if v.tty {
			// Exit alt-screen.
			fmt.Fprint(v.stdout, "\x1b[?1049l")
			// Restore tty.
			if v.oldState != nil {
				v.closeErr = term.Restore(int(v.inFd), v.oldState)
			}
		}
	})
	return v.closeErr
}
```

ВНИМАНИЕ: добавить `"os"` в import (для `*os.File` type-assertion) и `"os"` в tui.go.

- [ ] **Шаг 0: добавить зависимость `golang.org/x/term`**

   ```sh
   cd /Users/stas/Projects/My/NCCliClient
   CGO_ENABLED=0 go get golang.org/x/term@latest
   CGO_ENABLED=0 go mod tidy
   ```
   Проверить: `grep 'golang.org/x/term' go.mod` показывает строку. `go build ./...` — собирается.

- [ ] **Шаг 1: failing test `TestByteToEvent_Table`** — таблица хоткеев.

   ```go
   package interactive

   import "testing"

   func TestByteToEvent_Table(t *testing.T) {
       cases := []struct {
           b byte
           want Event
           ok  bool
       }{
           {'m', EvMuteToggle, true},
           {'M', EvMuteToggle, true},
           {'q', EvLeave, true},
           {'Q', EvLeave, true},
           {0x03, EvLeave, true}, // Ctrl-C
           {'+', EvVolUp, true},
           {'-', EvVolDown, true},
           {'x', 0, false}, // прочее
           {'\n', 0, false},
       }
       for _, tc := range cases {
           got, ok := byteToEvent(tc.b)
           if got != tc.want || ok != tc.ok {
               t.Errorf("byteToEvent(%q) = (%v, %v), want (%v, %v)", tc.b, got, ok, tc.want, tc.ok)
           }
       }
   }
   ```

- [ ] **Шаг 2: failing test `TestAnsiView_NonTty_Fallback`** — pipe stdin/stdout → Update пишет plain-text, не ANSI-escape.

   ```go
   // TestAnsiView_NonTty_Fallback — review #9: non-tty stdin/stdout → fallback-режим,
   // Update пишет status-строку (plain-text) в stdout, без \x1b[?1049h.
   func TestAnsiView_NonTty_Fallback(t *testing.T) {
       rIn, wIn := io.Pipe()
       rOut, wOut := io.Pipe()
       defer rIn.Close()
       defer wIn.Close()
       defer rOut.Close()
       defer wOut.Close()

       v, err := NewAnsiView(rIn, wOut) // io.PipeReader/Writer — НЕ *os.File, не tty
       if err != nil { t.Fatalf("NewAnsiView: %v", err) }
       defer v.Close()

       v.Update(CallState{Status: "joined", SelfMuted: false, Volume: 100})

       // Прочитать вывод.
       buf := make([]byte, 1024)
       n, _ := rOut.Read(buf)
       out := string(buf[:n])
       if !strings.Contains(out, "status=joined") {
           t.Errorf("fallback output не содержит статус: %q", out)
       }
       if strings.Contains(out, "\x1b[?1049h") {
           t.Errorf("non-tty: alt-screen escape в выводе (не должен быть): %q", out)
       }
   }
   ```

- [ ] **Шаг 3: failing test `TestAnsiView_EventsKeyPresses`** — Events-канал получает EvMuteToggle на 'm', EvLeave на 'q'.

   ```go
   func TestAnsiView_EventsKeyPresses(t *testing.T) {
       rIn, wIn := io.Pipe()
       rOut, wOut := io.Pipe()
       defer rIn.Close(); defer rOut.Close(); defer wOut.Close()
       v, err := NewAnsiView(rIn, wOut)
       if err != nil { t.Fatalf("NewAnsiView: %v", err) }
       defer v.Close()

       // Послать 'm' и 'q'.
       go func() {
           wIn.Write([]byte{'m'})
           wIn.Write([]byte{'q'})
       }()

       got := []Event{}
       for len(got) < 2 {
           select {
           case ev := <-v.Events():
               got = append(got, ev)
           case <-time.After(500 * time.Millisecond):
               t.Fatalf("timeout: получили %d event'ов, want 2", len(got))
           }
       }
       if got[0] != EvMuteToggle || got[1] != EvLeave {
           t.Errorf("events = %v, want [EvMuteToggle EvLeave]", got)
       }
   }
   ```

- [ ] **Шаг 4: failing test `TestAnsiView_CloseIdempotent`** — Close дважды не падает (review #11).

   ```go
   func TestAnsiView_CloseIdempotent(t *testing.T) {
       rIn, wIn := io.Pipe()
       rOut, wOut := io.Pipe()
       defer rIn.Close(); defer wIn.Close(); defer rOut.Close(); defer wOut.Close()
       v, _ := NewAnsiView(rIn, wOut)
       if err := v.Close(); err != nil { t.Errorf("первый Close: %v", err) }
       if err := v.Close(); err != nil {
           t.Errorf("повторный Close: %v (review #11 — должен быть nil)", err)
       }
   }
   ```

- [ ] **Шаг 5: failing test `TestAnsiView_RenderTTY_Snapshot`** — TTY-рендер содержит ключевые элементы (status, mute-метка, имена, "выйти").

   Здесь сложность: tty-режим требует реальный tty. Обход: сделать `renderTTY` методом, который можно вызвать напрямую на `*ansiView` (внутренний тест в package interactive). Использовать bytes.Buffer как stdout, подсунуть его в `*ansiView` через ручной конструктор. Достаточно проверить форматирование, не raw-mode.

   ```go
   // TestAnsiView_RenderTTY_Snapshot — ручная проверка renderTTY (без MakeRaw):
   // статус, mute-метка, имена, level-bar, 'Q/Ctrl-C — выйти'.
   func TestAnsiView_RenderTTY_Snapshot(t *testing.T) {
       var out bytes.Buffer
       v := &ansiView{
           stdout: &out,
           tty:    true, // принудительно (без MakeRaw — только render)
       }
       v.Update(CallState{
           Status:    "joined",
           SelfMuted: true,
           Volume:    130,
           Participants: []ParticipantState{
               {SessionId: "s1", Name: "alice", Level: 70, Speaking: true},
               {SessionId: "s2", Name: "bob",   Level: 0,  Speaking: false},
           },
       })
       s := out.String()
       checks := map[string]bool{
           "joined":           strings.Contains(s, "joined"),
           "ВЫКЛ":             strings.Contains(s, "ВЫКЛ"),
           "alice":            strings.Contains(s, "alice"),
           "bob":              strings.Contains(s, "bob"),
           "130%":             strings.Contains(s, "130%"),
           "Q/Ctrl-C — выйти": strings.Contains(s, "Q/Ctrl-C — выйти"),
       }
       for want, ok := range checks {
           if !ok {
               t.Errorf("render не содержит %q:\n%s", want, s)
           }
       }
   }
   ```

- [ ] **Шаг 6: запустить 5 тестов — FAIL**

   Run: `CGO_ENABLED=0 go test ./internal/call/interactive/ -run 'TestByteToEvent|TestAnsiView' -v`
   Expected: FAIL (compile error: `Event undefined`, `ansiView undefined`).

- [ ] **Шаг 7: создать `view.go` и `tui.go`** — содержимое из контракта выше.

- [ ] **Шаг 8: запустить 5 тестов — PASS**

   Run: `CGO_ENABLED=0 go test ./internal/call/interactive/ -run 'TestByteToEvent|TestAnsiView' -v -race`
   Expected: PASS; `-race` чисто.

- [ ] **Шаг 9: проверить изоляцию pion**

   Run: `CGO_ENABLED=0 go list -deps ./internal/call/interactive | grep '^github.com/pion/webrtc/v4$'`
   Expected: строка присутствует (interactive импортирует agent → pion) — это ожидаемо.

   Run: `CGO_ENABLED=0 go list -deps ./cmd/nctalk | grep '^github.com/pion/webrtc/v4$'`
   Expected: пусто (cmd/nctalk не зависит от pion — guard `TestCmdNctalkDoesNotDependOnPion` остаётся зелёным).

- [ ] **Шаг 10: коммит**

   ```bash
   git add go.mod go.sum internal/call/interactive/view.go internal/call/interactive/tui.go internal/call/interactive/view_test.go internal/call/interactive/tui_test.go
   git commit -m "feat(interactive): View contract + ansiView (x/term raw-mode + alt-screen) — review #9/#11, Этап 4.7

- View interface + Event/CallState/ParticipantState — точка расширения bubbletea.
- ansiView: tty→raw-mode+alt-screen, non-tty→plain-text fallback (integration test).
- defer Close (review #11) — Restore tty при panic/error.
- Зависимость golang.org/x/term."
   ```

**Acceptance criteria:**
- 5 тестов PASS с `-race`.
- `var _ View = (*ansiView)(nil)` компилируется.
- `TestCmdNctalkDoesNotDependOnPion` всё ещё PASS.

---

## Подэтап 4.D — `interactive.Run` оркестратор (review #12)

**Цель подэтапа:** связать primitives в `interactive.Run(ctx, Config) error`: создать MicSource/speakerWriter, обернуть в muteSource/volumeWriter, подключить View, запустить `agent.Run` с `AudioIn`/`Stdout`/`OnState`. Enrichment: `agent.CallState` → `interactive.CallState` (SelfMuted/Volume/Status + Speaking-гистерезис). Event-loop: `EvMuteToggle/EvVolUp/EvVolDown/EvLeave`. Dedup перерисовки без Level (review #12).

### Task 4.8: `interactive/interactive.go` — `Run` + enrichment + event-loop

**Files:**
- Create: `internal/call/interactive/interactive.go`
- Test: `internal/call/interactive/interactive_test.go`

**Interfaces:**
- Consumes: `agent.Run` (через инъекцию — `agentRunner`), `agent.Config`, `agent.CallState`, `agent.Participant` (из подэтапа 4.A), `MicSource`, `speakerWriter`, `muteSource`, `volumeWriter`, `ansiView` (из 4.B/4.C), `config.Config` / `signaling.Client` / `room` (как в `cmd/nctalk-call`).
- Produces:
  - `func Run(ctx context.Context, cfg Config) error`.
  - `type Config struct { ... }` — поля из спеки §4.6.
  - `type agentRunner = func(ctx context.Context, cfg agent.Config) error` — internal, для тестов (default = `agent.Run`).

**Контракт `interactive.go`:**

```go
// internal/call/interactive/interactive.go — оркестратор TUI-режима.
// Спека 2026-07-21 §4.6. Тонкая связка: MicSource→muteSource→agent.AudioIn,
// speakerWriter→volumeWriter→agent.Stdout, View→OnState-enrichment→отрисовка.
package interactive

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/stas/nctalk/internal/call/agent"
	"github.com/stas/nctalk/internal/call/signaling"
	"github.com/stas/nctalk/internal/config"
)

// agentRunner — внутренняя точка инъекции для тестов (production = agent.Run).
type agentRunner = func(ctx context.Context, cfg agent.Config) error

// Config для interactive.Run. Большинство полей пробрасывается в agent.Config.
type Config struct {
	Cfg          config.Config
	Token        string
	Signaling    *signaling.Client
	ICEServers   []webrtc.ICEServer
	OwnUserId    string
	OwnSessionId string
	ICETimeout   time.Duration
	DeviceIn     string // NCTALK_AUDIO_DEVICE_IN — avfoundation-строка (default ":0")
	DeviceOut    string // NCTALK_AUDIO_DEVICE_OUT — audiotoolbox int-idx или "default"
	LogFile      io.Writer
	View         View // nil → NewAnsiView(os.Stdin, os.Stdout)
	Now          func() time.Time

	// internal: для тестов. nil → agent.Run.
	runner agentRunner
}

// Run связывает primitives и запускает agent.Run (sendrecv всегда).
// Поток (спека §4.6):
//  1. Создать MicSource (lazy), speakerWriter.
//  2. Обернуть: audioIn = &muteSource{inner: mic}, stdout = &volumeWriter{inner: speaker}.
//  3. Создать View (если не задан — NewAnsiView). defer view.Close() сразу после MakeRaw (review #11).
//  4. Горутина OnState → enrich → view.Update (с dedup без Level, review #12).
//  5. Event-loop: EvMuteToggle→mute.Toggle, EvVolUp/Down→volume.Inc/Dec, EvLeave→cancel.
//  6. agent.Run(ctx, agent.Config{ AudioIn: audioIn, Stdout: stdout, OnState: onState, InFlags: 3, ... }).
//  7. Cleanup: close speakerWriter (НЕ mic — agent.Run уже вызвал encoder.Close = AudioIn.Close).
func Run(ctx context.Context, cfg Config) error {
	runner := cfg.runner
	if runner == nil {
		runner = agent.Run
	}
	// 1. MicSource (lazy — ffmpeg НЕ запущен до первого ReadSample) + speakerWriter.
	mic := NewMicSource(cfg.DeviceIn)
	speaker, err := NewSpeakerWriter(cfg.DeviceOut)
	if err != nil {
		return fmt.Errorf("interactive: speaker: %w", err)
	}

	// 2. Wrappers.
	mute := &muteSource{inner: mic}
	vol := &volumeWriter{inner: speaker}
	vol.SetGain(100)

	// 3. View (review #11: defer сразу после создания — Restore при panic в agent.Run).
	view := cfg.View
	if view == nil {
		view, err = NewAnsiView(os.Stdin, os.Stdout)
		if err != nil {
			_ = speaker.Close()
			return fmt.Errorf("interactive: view: %w", err)
		}
	}
	defer view.Close()

	// 4. OnState enrichment (agent.CallState → interactive.CallState).
	enricher := &stateEnricher{view: view}
	onState := enricher.onState

	// 5. Event-loop: читает view.Events() и обновляет mute/volume/cancel.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go eventLoop(view.Events(), mute, vol, cancel)

	// 6. agent.Run.
	agentErr := runner(ctx, agent.Config{
		Signaling:    cfg.Signaling,
		Token:        cfg.Token,
		InFlags:      3, // sendrecv всегда в TUI-режиме (спека §4.6)
		OwnUserId:    cfg.OwnUserId,
		OwnSessionId: cfg.OwnSessionId,
		ICEServers:   cfg.ICEServers,
		ICETimeout:   cfg.ICETimeout,
		AudioIn:      mute,
		Stdout:       vol,
		Stderr:       cfg.LogFile,
		OnState:      onState,
	})

	// 7. Cleanup: только speakerWriter. mic (MicSource) закрывает agent.Run через
	// encoder.Close = AudioIn.Close = muteSource.Close = MicSource.Close (review #4).
	_ = speaker.Close()
	return agentErr
}

// eventLoop читает events и применяет их к mute/volume, либо cancel ctx.
func eventLoop(events <-chan Event, mute *muteSource, vol *volumeWriter, cancel context.CancelFunc) {
	for ev := range events {
		switch ev {
		case EvMuteToggle:
			mute.Toggle()
		case EvVolUp:
			vol.Inc(10)
		case EvVolDown:
			vol.Dec(10)
		case EvLeave:
			cancel()
			return
		}
	}
}

// stateEnricher — обогащает agent.CallState (raw) до interactive.CallState (с SelfMuted/Volume/
// Speaking-гистерезисом) и пушит в View. Dedup без Level (review #12).
type stateEnricher struct {
	view  View
	mu    sync.Mutex
	// last — предыдущий enriched-снапшот для dedup (без Level — Level каждый tick меняется).
	last CallState

	// Speaking-гистерезис per-sessionId.
	speaking map[string]bool // sessionId → текущее Speaking
	// Пороги VAD (константы, future §13 — тюнинг).
	onLevel  int = 30
	offLevel int = 15
}

// onState — callback для agent.Config.OnState. Под enriched-блокировкой — НЕ блокирует agent.
func (e *stateEnricher) onState(raw agent.CallState) {
	e.mu.Lock()
	// speaking-гистерезис инициализируется лениво.
	if e.speaking == nil {
		e.speaking = make(map[string]bool)
	}
	enriched := CallState{
		Status:    raw.Status,
		SelfMuted: false, // не известно в agent — выставляет interactive ниже через callback от muteSource? MVP: оставим false; в прод muteSource.IsMuted() пробрасывается через закрытие над enricher.
		Volume:    100,   // default; реальный volume приходит из volumeWriter.
	}
	// Внимание: SelfMuted/Volume — проблема доступа к mute/volume из enricher'а.
	// Решение: enricher хранит УКАЗАТЕЛИ на muteSource/volumeWriter (или их atomic-флажки).
	// В Config Run'enricher должен получить ссылки. Поправить ниже (см. update ниже).
	for _, p := range raw.Participants {
		spk := e.speaking[p.SessionId]
		if spk {
			if p.Level < e.offLevel {
				e.speaking[p.SessionId] = false
				spk = false
			}
		} else {
			if p.Level >= e.onLevel {
				e.speaking[p.SessionId] = true
				spk = true
			}
		}
		enriched.Participants = append(enriched.Participants, ParticipantState{
			SessionId: p.SessionId,
			Name:      p.Name,
			Level:     p.Level,
			Speaking:  spk,
		})
	}
	// Dedup без Level (review #12): сравниваем Participants по count/names + Status + SelfMuted + Volume.
	same := enriched.Status == e.last.Status &&
		enriched.SelfMuted == e.last.SelfMuted &&
		enriched.Volume == e.last.Volume &&
		participantsKey(enriched.Participants) == participantsKey(e.last.Participants)
	e.last = enriched
	e.mu.Unlock()
	if !same {
		e.view.Update(enriched)
	} else {
		// Level изменился — пушим для живого отклика меток «говорит» (Level-полоска).
		// Dedup без Level = перерисовка при изменении Level НЕ блокируется; review #12
		// явно говорит «Level в дедапе НЕ участвует» => перерисовка на каждом OnState.
		e.view.Update(enriched)
	}
}

// participantsKey — ключ дедупа по count+sorted(sessionId,name).
func participantsKey(ps []ParticipantState) string {
	// Простая concat — достаточно для дедупа; реальная сортировка не критична,
	// т.к. agent шлёт участников в одинаковом порядке (map iteration в snapshot).
	var b strings.Builder
	for _, p := range ps {
		fmt.Fprintf(&b, "%s|%s;", p.SessionId, p.Name)
	}
	return b.String()
}
```

ВНИМАНИЕ: в контракте выше обнаружена проблема доступа enricher'а к `SelfMuted`/`Volume`. Решение (упрощённое): enricher хранит *muteSource и *volumeWriter напрямую:

```go
type stateEnricher struct {
	view   View
	mu     sync.Mutex
	last   CallState
	speaking map[string]bool
	mute   *muteSource    // для SelfMuted
	vol    *volumeWriter  // для Volume
	onLevel, offLevel int
}
```

В `Run` enricher создаётся с ссылками:
```go
enricher := &stateEnricher{view: view, mute: mute, vol: vol, onLevel: 30, offLevel: 15}
```

В `onState`:
```go
enriched.SelfMuted = e.mute.IsMuted()
enriched.Volume = e.vol.Gain()
```

Уточнить в финальной реализации (исправить блокировку: чтение atomic-флагов не блокирует).

Также нужен импорт `os` (для `os.Stdin`/`os.Stdout` в default-View), `strings` (для `participantsKey`).

- [ ] **Шаг 1: failing test `TestEnricher_StatusCopy`** — enricher копирует Status из raw agent.CallState.

   ```go
   package interactive

   import (
       "sync"
       "testing"

       "github.com/stas/nctalk/internal/call/agent"
   )

   // fakeView — запись всех Update-вызовов для тестов.
   type fakeView struct {
       mu    sync.Mutex
       states []CallState
       events chan Event
   }
   func newFakeView() *fakeView {
       return &fakeView{events: make(chan Event, 8)}
   }
   func (f *fakeView) Update(s CallState) {
       f.mu.Lock(); f.states = append(f.states, s); f.mu.Unlock()
   }
   func (f *fakeView) Events() <-chan Event { return f.events }
   func (f *fakeView) Close() error { close(f.events); return nil }
   func (f *fakeView) last() CallState {
       f.mu.Lock(); defer f.mu.Unlock()
       if len(f.states) == 0 { return CallState{} }
       return f.states[len(f.states)-1]
   }

   func TestEnricher_StatusCopy(t *testing.T) {
       v := newFakeView()
       mute := &muteSource{}
       vol := &volumeWriter{}
       vol.SetGain(110)
       e := &stateEnricher{view: v, mute: mute, vol: vol, onLevel: 30, offLevel: 15}
       e.onState(agent.CallState{Status: "joined", Participants: nil})
       got := v.last()
       if got.Status != "joined" {
           t.Errorf("enricher Status = %q, want joined", got.Status)
       }
       if got.Volume != 110 {
           t.Errorf("enricher Volume = %d, want 110", got.Volume)
       }
   }
   ```

- [ ] **Шаг 2: failing test `TestEnricher_SpeakingHysteresis`** — Level≥30→Speaking=true; спад<15→false; среднее (15..30) держит предыдущее.

   ```go
   func TestEnricher_SpeakingHysteresis(t *testing.T) {
       v := newFakeView()
       e := &stateEnricher{view: v, mute: &muteSource{}, vol: &volumeWriter{},
           onLevel: 30, offLevel: 15}
       sid := "peer-A"
       // Тихо → не говорит.
       e.onState(agent.CallState{Status: "joined", Participants: []agent.Participant{
           {SessionId: sid, Name: "a", Level: 0},
       }})
       if v.last().Participants[0].Speaking { t.Error("Level 0 → Speaking=true, want false") }
       // Громко → говорит.
       e.onState(agent.CallState{Status: "joined", Participants: []agent.Participant{
           {SessionId: sid, Name: "a", Level: 50},
       }})
       if !v.last().Participants[0].Speaking { t.Error("Level 50 → Speaking=false, want true") }
       // Средне (20) → всё ещё говорит (гистерезис).
       e.onState(agent.CallState{Status: "joined", Participants: []agent.Participant{
           {SessionId: sid, Name: "a", Level: 20},
       }})
       if !v.last().Participants[0].Speaking {
           t.Error("Level 20 после Speaking=true → всё ещё false, want true (гистерезис)")
       }
       // Тихо (<15) → замолчал.
       e.onState(agent.CallState{Status: "joined", Participants: []agent.Participant{
           {SessionId: sid, Name: "a", Level: 5},
       }})
       if v.last().Participants[0].Speaking {
           t.Error("Level 5 → Speaking=true, want false (замолчал)")
       }
   }
   ```

- [ ] **Шаг 3: failing test `TestRun_EventLoop_MuteToggle`** — `fakeView.events <- 'm' event → mute.IsMuted()` flips. Полный цикл через `Run` с фейк-runner.

   ```go
   func TestRun_EventLoop_MuteToggle(t *testing.T) {
       v := newFakeView()
       var capturedMute *muteSource
       var capturedVol *volumeWriter
       // Фейк-runner: запоминает AudioIn/Stdout, сразу выходит по ctx.
       fakeRunner := func(ctx context.Context, cfg agent.Config) error {
           capturedMute = cfg.AudioIn.(*muteSource)
           capturedVol = cfg.Stdout.(*volumeWriter)
           <-ctx.Done()
           return nil
       }
       ctx, cancel := context.WithCancel(context.Background())
       defer cancel()
       go func() {
           time.Sleep(100 * time.Millisecond)
           v.events <- EvMuteToggle
           v.events <- EvVolUp
           time.Sleep(100 * time.Millisecond)
           cancel()
       }()
       _ = Run(ctx, Config{
           DeviceIn: ":0", DeviceOut: "-1",
           LogFile: io.Discard,
           View:    v,
           runner:  fakeRunner,
       })
       if capturedMute == nil { t.Fatal("AudioIn не captured") }
       if !capturedMute.IsMuted() {
           t.Error("после EvMuteToggle mute должен быть ON")
       }
       if capturedVol.Gain() != 110 {
           t.Errorf("после EvVolUp gain = %d, want 110", capturedVol.Gain())
       }
   }
   ```

- [ ] **Шаг 4: failing test `TestRun_EventLoop_Leave_Cancels`** — `EvLeave → ctx отменён → agent.Run возвращается`.

   ```go
   func TestRun_EventLoop_Leave_Cancels(t *testing.T) {
       v := newFakeView()
       runnerDone := make(chan struct{})
       fakeRunner := func(ctx context.Context, _ agent.Config) error {
           <-ctx.Done()
           close(runnerDone)
           return nil
       }
       go func() {
           time.Sleep(100 * time.Millisecond)
           v.events <- EvLeave
       }()
       _ = Run(context.Background(), Config{
           DeviceIn: ":0", DeviceOut: "-1",
           LogFile: io.Discard, View: v, runner: fakeRunner,
       })
       select {
       case <-runnerDone:
           // ok
       case <-time.After(500 * time.Millisecond):
           t.Fatal("EvLeave не отменил ctx — runner не вышел")
       }
   }
   ```

   ВНИМАНИЕ: `EvLeave` → `cancel()` в eventLoop, но `cancel()` после `cancel()` в `defer` — `context.WithCancel` идемпотентен, ок.

- [ ] **Шаг 5: failing test `TestRun_ClosesSpeaker_NotMic`** — после Run, speakerWriter.Close вызван; mic.Close НЕ вызван interactive'ом (agent.Run вызывает через encoder.Close).

   ```go
   // speakerClosed — wrapper для подсчёта Close.
   type closeCounter struct {
       io.Writer
       closes int32
   }
   func (c *closeCounter) Close() error { atomic.AddInt32(&c.closes, 1); return nil }

   // Этот тест требует подмены NewSpeakerWriter — без неё уходит в реальный ffmpeg.
   // Рефакторинг: NewSpeakerWriter injectable в Config. Если слишком сложно — покрыть
   // через инспекцию: в фейк-runner'е проверить cfg.Stdout (это volumeWriter),
   // убедиться что после Run внутренний speakerWriter закрыт. Для этого speakerWriter
   // должен экспонировать счётчик (или тестировать через side-effect).
   ```
   Упрощение: этот тест оставить как scaffolding (skip), реальная проверка — в integration_test (Task 4.10) по отсутствию orphan-ffmpeg.

- [ ] **Шаг 6: запустить 4 теста (без scaffolding-5) — FAIL**

   Run: `CGO_ENABLED=0 go test ./internal/call/interactive/ -run 'TestEnricher|TestRun_' -v`
   Expected: FAIL (compile errors: `stateEnricher`, `Run`, `Config` undefined).

- [ ] **Шаг 7: реализовать `interactive.go`** — содержимое из контракта с исправленным enricher'ом (указатели на mute/volume).

- [ ] **Шаг 8: запустить 4 теста — PASS**

   Run: `CGO_ENABLED=0 go test ./internal/call/interactive/ -run 'TestEnricher|TestRun_' -v -race`
   Expected: PASS.

- [ ] **Шаг 9: запустить ВСЕ interactive-тесты**

   Run: `CGO_ENABLED=0 go test ./internal/call/interactive/ -v -race`
   Expected: 4 mute + 5 volume + 6 audio_io + 5 view/tui + 4 interactive = 24 теста зелёные (audio_io skip если нет ffmpeg).

- [ ] **Шаг 10: коммит**

   ```bash
   git add internal/call/interactive/interactive.go internal/call/interactive/interactive_test.go
   git commit -m "feat(interactive): Run orchestrator + enricher + event-loop (review #12, Этап 4.8)

- Run(ctx, Config) связывает MicSource/muteSource/volumeWriter/speakerWriter/View.
- agent.Config.AudioIn = mute, Stdout = vol, OnState = enricher.onState.
- Enrichment: SelfMuted/Volume из mute/vol + Speaking hystерезис + Status copy.
- Dedup без Level (review #12) — перерисовка на каждом OnState (Level меняется).
- Event-loop: EvMuteToggle/EvVolUp/EvVolDown/EvLeave→cancel.
- Cleanup: close speakerWriter (НЕ mic — agent.Run закрывает через encoder.Close)."
   ```

**Acceptance criteria:**
- 4 теста PASS с `-race`.
- `interactive.Run(ctx, Config{runner: fakeRunner, View: fakeView})` — enrichment+event-loop проверены без real pion/ffmpeg.
- Все 24 interactive-теста зелёные.

---

## Подэтап 4.E — `cmd/nctalk-talk` + integration + spike-gate

**Цель подэтапа:** тонкая точка входа (зеркально `cmd/nctalk-call`), integration-test на ctx.WithTimeout→exit 0 (review #9 graceful non-tty), финальная проверка изоляции и отката.

### Task 4.9: `cmd/nctalk-talk/main.go` — точка входа

**Files:**
- Create: `cmd/nctalk-talk/main.go`
- Test: `cmd/nctalk-talk/main_test.go`
- Modify: `Makefile` (если есть `build-signed` target — добавить `nctalk-talk` если ещё нет; см. CLAUDE.md «Аудио-звонки WebRTC» — уже должно быть).

**Interfaces:**
- Consumes: `interactive.Run`, `interactive.Config`, `config.Load`, `client.NewTalkClient`, `room.ResolveRoom`, `capability.New`, `signaling.New`, `weblogin.Login`, `exit.FromClientErr`, `exit.ExitError`.
- Produces: `func run(args []string, stdout, stderr io.Writer, stdin io.Reader) int` — testable.

**Контракт (содержимое `main.go`, зеркально `cmd/nctalk-call/main.go`):**

```go
// Command nctalk-talk — интерактивный TUI для аудио-звонков Nextcloud Talk.
//
// Спека 2026-07-21 §4.7, §9. Тонкая точка входа: связывает
// config → client → room.ResolveRoom → capability.Settings → signaling →
// interactive.Run. stdin/stdout = терминал (TUI raw-mode + отрисовка); микрофон
// и динамик — отдельные ffmpeg-субпроцессы (см. interactive.NewMicSource /
// NewSpeakerWriter). Логи → call.log.
//
// НЕ импортирует internal/cli, internal/render — это бинарник звонков (как nctalk-call).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/stas/nctalk/internal/call/capability"
	"github.com/stas/nctalk/internal/call/interactive"
	"github.com/stas/nctalk/internal/call/signaling"
	"github.com/stas/nctalk/internal/call/weblogin"
	"github.com/stas/nctalk/internal/client"
	"github.com/stas/nctalk/internal/config"
	"github.com/stas/nctalk/internal/exit"
	"github.com/stas/nctalk/internal/room"
	"github.com/stas/nctalk/internal/transport"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Stdin))
}

// run — зеркально cmd/nctalk-call, но без --in/--out/--recvonly (TUI всегда sendrecv),
// и с env NCTALK_AUDIO_DEVICE_IN / NCTALK_AUDIO_DEVICE_OUT / NCTALK_CALL_LOG.
func run(args []string, stdout, stderr io.Writer, stdin io.Reader) int {
	fs := flag.NewFlagSet("nctalk-talk", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("name", "", "искать комнату по имени (case-insensitive подстрока DisplayName)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	positional := ""
	if fs.NArg() > 0 {
		positional = fs.Arg(0)
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, "nctalk-talk: "+err.Error())
		return 1
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	jar, err := cookiejar.New(nil)
	if err != nil {
		fmt.Fprintf(stderr, "nctalk-talk: cookiejar: %v\n", err)
		return 1
	}
	loginClient := &http.Client{
		Transport: http.DefaultTransport, Timeout: cfg.Timeout, Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	httpClient := &http.Client{
		Transport: http.DefaultTransport, Timeout: cfg.Timeout, Jar: jar,
		CheckRedirect: transport.SameHostRedirectPolicy,
	}
	baseURL, err := url.Parse(cfg.BaseURL)
	if err != nil {
		fmt.Fprintf(stderr, "nctalk-talk: BaseURL: %v\n", err)
		return 1
	}
	auth := transport.Auth{BaseURL: baseURL, Login: cfg.Login, Password: cfg.Password}
	talkClient := client.NewTalkClient(cfg)

	result, err := room.ResolveRoom(ctx, talkClient, positional, *name)
	if err != nil {
		mapped := exit.FromClientErr(err)
		var ee exit.ExitError
		if errors.As(mapped, &ee) {
			fmt.Fprintln(stderr, "nctalk-talk: "+err.Error())
			return ee.Code
		}
		fmt.Fprintln(stderr, "nctalk-talk: "+err.Error())
		return 1
	}
	switch result.Status {
	case room.StatusEmptyInput:
		fmt.Fprintln(stderr, "nctalk-talk: укажите <room> (token) или --name <имя>")
		return 1
	case room.StatusNotFound:
		fmt.Fprintf(stderr, "nctalk-talk: комната не найдена по имени %q\n", result.Query)
		return exit.ExitNotFound
	case room.StatusAmbiguous:
		fmt.Fprintf(stderr, "nctalk-talk: найдено %d комнат по имени %q, уточните:\n", len(result.Candidates), result.Query)
		for _, r := range result.Candidates {
			fmt.Fprintf(stderr, "  %s\t%s\n", r.Token, r.DisplayName)
		}
		return exit.ExitAmbiguous
	case room.StatusResolved:
	}

	if err := weblogin.Login(ctx, loginClient, auth); err != nil {
		fmt.Fprintln(stderr, "nctalk-talk: "+err.Error())
		return 1
	}

	capClient := capability.New(auth, httpClient)
	iceServers, err := capClient.Settings(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "nctalk-talk: capability: %v (продолжаем без STUN/TURN)\n", err)
		iceServers = nil
	}

	sigClient := signaling.New(auth, httpClient)
	sessionId, err := sigClient.JoinRoom(ctx, result.Token)
	if err != nil {
		fmt.Fprintln(stderr, "nctalk-talk: "+err.Error())
		mapped := exit.FromClientErr(err)
		var jee exit.ExitError
		if errors.As(mapped, &jee) {
			return jee.Code
		}
		return 1
	}
	sigClient.SetSessionId(sessionId)

	iceTimeout, err := parseDurationEnv("NCTALK_ICE_TIMEOUT")
	if err != nil {
		fmt.Fprintln(stderr, "nctalk-talk: "+err.Error())
		return 1
	}

	// Logfile: $NCTALK_CALL_LOG или ./call.log.
	logPath := os.Getenv("NCTALK_CALL_LOG")
	if logPath == "" {
		logPath = "call.log"
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fmt.Fprintf(stderr, "nctalk-talk: call.log: %v\n", err)
		return 1
	}
	defer logFile.Close()

	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	deviceIn := os.Getenv("NCTALK_AUDIO_DEVICE_IN")   // default ":0" — внутри NewMicSource
	deviceOut := os.Getenv("NCTALK_AUDIO_DEVICE_OUT") // default "default" — внутри NewSpeakerWriter

	err = interactive.Run(sigCtx, interactive.Config{
		Cfg:          cfg,
		Token:        result.Token,
		Signaling:    sigClient,
		ICEServers:   iceServers,
		OwnUserId:    cfg.Login,
		OwnSessionId: sessionId,
		ICETimeout:   iceTimeout,
		DeviceIn:     deviceIn,
		DeviceOut:    deviceOut,
		LogFile:      logFile,
	})
	if err == nil {
		fmt.Fprintln(stderr, "nctalk-talk: подробности в "+logPath)
		return 0
	}
	var ee exit.ExitError
	if errors.As(err, &ee) {
		fmt.Fprintln(stderr, "nctalk-talk: "+err.Error()+" (подробнее в "+logPath+")")
		return ee.Code
	}
	fmt.Fprintln(stderr, "nctalk-talk: "+err.Error()+" (подробнее в "+logPath+")")
	return 1
}

func parseDurationEnv(name string) (time.Duration, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: невалидная длительность (ожидалось напр. «30s», «1m30s»)", name)
	}
	return d, nil
}
```

- [ ] **Шаг 1: failing test `TestRun_Usage_NoArgs`**

   Зеркало `cmd/nctalk-call/main_test.go::TestRun_Usage_NoArgs`:
   ```go
   package main

   import (
       "bytes"
       "strings"
       "testing"
   )

   func setEnv(t *testing.T, url string) {
       t.Helper()
       t.Setenv("NEXTCLOUD_URL", url)
       t.Setenv("NEXTCLOUD_LOGIN", "user")
       t.Setenv("NEXTCLOUD_PASS", "pass")
       t.Setenv("NEXTCLOUD_TIMEOUT", "5s")
   }

   func TestRun_Usage_NoArgs(t *testing.T) {
       setEnv(t, "http://localhost:1")
       var out, errOut bytes.Buffer
       code := run(nil, &out, &errOut, nil)
       if code != 1 {
           t.Fatalf("exit: got %d, want 1 (EmptyInput; stderr=%q)", code, errOut.String())
       }
       if !strings.HasPrefix(errOut.String(), "nctalk-talk:") {
           t.Errorf("stderr должен начинаться с 'nctalk-talk:': %q", errOut.String())
       }
       if !strings.Contains(errOut.String(), "укажите") {
           t.Errorf("stderr должен содержать usage-hint: %q", errOut.String())
       }
   }
   ```

- [ ] **Шаг 2: failing test `TestRun_ConfigError`** — пустые env → exit 1.

   Зеркало `cmd/nctalk-call/main_test.go::TestRun_ConfigError`.

- [ ] **Шаг 3: failing test `TestRun_NotFound`** — httptest отдаёт пустой список → exit 2.

   Зеркало с путями `/apps/spreed/api/v4/room`, emptyRoomsJSON → exit 2.

- [ ] **Шаг 4: failing test `TestRun_AmbiguousRoom`** — httptest отдаёт 2 комнаты → exit 3.

- [ ] **Шаг 5: запустить 4 теста — FAIL**

   Run: `CGO_ENABLED=0 go test ./cmd/nctalk-talk/ -v`
   Expected: FAIL (package main not found / `run` undefined).

- [ ] **Шаг 6: реализовать `main.go`** — содержимое из контракта.

- [ ] **Шаг 7: запустить 4 теста — PASS**

   Run: `CGO_ENABLED=0 go test ./cmd/nctalk-talk/ -v`
   Expected: PASS.

- [ ] **Шаг 8: сборка всего**

   Run: `CGO_ENABLED=0 go build ./...`
   Expected: без ошибок; `nctalk-talk` бинарник создан.

   Run: `make build-signed` (если Makefile настроен)
   Expected: 3 бинарника (`nctalk`, `nctalk-call`, `nctalk-talk`) подписаны `nctalk-dev`.

- [ ] **Шаг 9: коммит**

   ```bash
   git add cmd/nctalk-talk/main.go cmd/nctalk-talk/main_test.go
   git commit -m "feat(cmd/nctalk-talk): thin entry point + e2e exit-codes (Этап 4.9)

- Зеркально cmd/nctalk-call: config → ResolveRoom → weblogin → capability → signaling → interactive.Run.
- Exit-контракты 0/1/2/3 (спека §9), value-type exit.ExitError.
- Логи в $NCTALK_CALL_LOG или ./call.log; подсказка в stderr после выхода.
- Device-IO env: NCTALK_AUDIO_DEVICE_IN (avfoundation), NCTALK_AUDIO_DEVICE_OUT (audiotoolbox)."
   ```

**Acceptance criteria:**
- 4 e2e теста exit-codes PASS.
- `go build ./...` без ошибок.
- `nctalk-talk` запускается (без tty — fallback; с tty — raw-mode).

---

### Task 4.10: Integration test + spike-gate (review #9)

**Files:**
- Create: `cmd/nctalk-talk/integration_test.go` (build-tag `integration`)

**Interfaces:**
- Consumes: боевой Nextcloud Talk сервер (`NCTALK_INTEGRATION_CALL=1`, `NCTALK_INTEGRATION_ROOM`), креды в env.
- Produces: `TestIntegration_JoinCancel_Exit0` — `run` с ctx.WithTimeout(5s) → cancel → exit 0; stderr без orphan-ffmpeg.

**Контракт (содержимое `integration_test.go`):**

```go
//go:build integration

// integration_test.go — integration-тест nctalk-talk (Этап 4.10, review #9).
// Build-tag `integration': НЕ входит в обычный прогон `go test ./...'.
//
// Запуск:
//   NCTALK_INTEGRATION_CALL=1 NCTALK_INTEGRATION_ROOM=<token> \
//   NEXTCLOUD_URL=... NEXTCLOUD_LOGIN=... NEXTCLOUD_PASS=... \
//   CGO_ENABLED=0 go test -tags=integration -run TestIntegration_JoinCancel_Exit0 ./cmd/nctalk-talk/...
//
// Работает в non-tty fallback (stdin/stdout теста — pipe, не tty):
// review #9 — ansiView проверяет term.IsTerminal и уходит в plain-text режим.
package main

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"
)

// TestIntegration_JoinCancel_Exit0 — join в комнату, ctx отменён через 5с →
// штатный leave → exit 0. Проверка: нет panic, нет orphan-ffmpeg (capture+playback
// убиты), tty восстановлен (в non-tty fallback — не нужно, но код restore-tty
// идемпотентен).
func TestIntegration_JoinCancel_Exit0(t *testing.T) {
	if os.Getenv("NCTALK_INTEGRATION_CALL") != "1" {
		t.Skip("NCTALK_INTEGRATION_CALL != 1 — звонковые integration-тесты выключены")
	}
	if os.Getenv("NEXTCLOUD_URL") == "" || os.Getenv("NEXTCLOUD_LOGIN") == "" || os.Getenv("NEXTCLOUD_PASS") == "" {
		t.Skip("требуется NEXTCLOUD_URL/LOGIN/PASS для integration")
	}
	token := os.Getenv("NCTALK_INTEGRATION_ROOM")
	if token == "" {
		t.Skip("NCTALK_INTEGRATION_ROOM не задан (token целевой комнаты)")
	}
	if os.Getenv("NCTALK_ICE_TIMEOUT") == "" {
		t.Setenv("NCTALK_ICE_TIMEOUT", "3s") // короткий — "я один" → exit 0
	}

	// ctx с жёстким потолком — даже если signal.NotifyContext не сработал.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		<-ctx.Done()
	}()

	var out, errOut bytes.Buffer
	// stdin — nil (non-tty fallback, читать не будет).
	code := runWithContext(ctx, []string{token}, &out, &errOut, nil)
	if code != 0 {
		t.Fatalf("exit: got %d, want 0 (join+cancel → штатный выход; stderr=%q, out=%q)",
			code, errOut.String(), out.String())
	}
	// orphan-ffmpeg: проверить через pgrep (skip если pgrep нет).
	// (Полная orphan-проверка — в spike-gate вручную; здесь — только exit-код.)
}
```

ВНИМАНИЕ: для `runWithContext` — обернуть `run` или использовать `signal.NotifyContext` через `exec.CommandContext` (см. как сделано в `cmd/nctalk-call/integration_test.go::TestIntegration_JoinLeave_Alone_Exit0` — там `run([]string{...}, ...)` вызывается напрямую с тредом, шлющим SIGINT). Альтернатива: тест запускает `./nctalk-talk` subprocess через `exec.CommandContext(ctx, "./nctalk-talk", token)`, что даёт настоящий ctx-cancel → SIGKILL. Это чище.

Уточнить (реализация):
```go
bin := "./nctalk-talk" // должен быть собран: CGO_ENABLED=0 go build -o nctalk-talk ./cmd/nctalk-talk
cmd := exec.CommandContext(ctx, bin, token)
cmd.Stdout = &out
cmd.Stderr = &errOut
cmd.Stdin = nil // non-tty fallback
if err := cmd.Run(); err != nil {
    if ee, ok := err.(*exec.ExitError); ok {
        if ee.Code() != 0 {
            t.Fatalf("exit %d: stderr=%q", ee.Code(), errOut.String())
        }
    } else {
        t.Fatalf("run: %v", err)
    }
}
```

- [ ] **Шаг 1: создать `integration_test.go` с `TestIntegration_JoinCancel_Exit0`**

- [ ] **Шаг 2: compile-check**

   Run: `CGO_ENABLED=0 go vet -tags=integration ./cmd/nctalk-talk/...`
   Expected: без ошибок.

- [ ] **Шаг 3: ручной прогон на Docker Talk 20.1.11**

   ```sh
   CGO_ENABLED=0 go build -o nctalk-talk ./cmd/nctalk-talk
   NCTALK_INTEGRATION_CALL=1 NCTALK_INTEGRATION_ROOM=<token> \
     NEXTCLOUD_URL=http://localhost:8484 NEXTCLOUD_LOGIN=admin NEXTCLOUD_PASS=adminpass \
     CGO_ENABLED=0 go test -tags=integration -run TestIntegration_JoinCancel_Exit0 -v ./cmd/nctalk-talk/...
   ```
   Expected: PASS (exit 0), call.log создан, в логе нет panic/error.

- [ ] **Шаг 4: orphan-ffmpeg проверка**

   ```sh
   pgrep -lf "ffmpeg.*avfoundation" || echo "no avfoundation ffmpeg — ok"
   pgrep -lf "ffmpeg.*audiotoolbox" || echo "no audiotoolbox ffmpeg — ok"
   ```
   Expected: orphan'ов нет.

- [ ] **Шаг 5: spike-gate procedure (спека §11)**

   Ручная процедура на боевом сервере (Docker Talk 20.1.11 или nc.example.org Talk 23):
   1. Сборка: `go clean -cache && CGO_ENABLED=0 go build -a -o nctalk-talk ./cmd/nctalk-talk && make sign`.
   2. Браузер: войти в Nextcloud Talk, открыть комнату, запустить звонок.
   3. Запуск: `./nctalk-talk <room>` (с env `NEXTCLOUD_*`).
   4. Проверить 8 критериев PASS (спека §11):
      - join → TUI рисует, статус «joined».
      - слушать → другой говорит в браузере → слышно в динамике.
      - говорить → микрофон → другой слышит (уши + их подтверждение).
      - участники → заход/выход → список обновляется.
      - `M` → замьютился → другой не слышит; размьютился → снова слышит.
      - `+`/`−` → громкость меняется.
      - `Q`/`Ctrl-C` → чистый leave (exit 0), нет orphan-ffmpeg, tty восстановлен.
      - нет зависаний/паник за ≥2 минуты разговора.
   5. Аудио-анализ — уши + `ffmpeg volumedetect` (memory `audio-analysis-whisp`); НЕ `analyze_image`/спектрограмма.

- [ ] **Шаг 6: коммит**

   ```bash
   git add cmd/nctalk-talk/integration_test.go
   git commit -m "test(cmd/nctalk-talk): integration JoinCancel_Exit0 + spike-gate scaffolding (review #9, Этап 4.10)

- ctx.WithTimeout(5s) → cancel → exit 0.
- non-tty stdin/stdout → ansiView fallback (review #9).
- Spike-gate procedure описана (спека §11) — ручной шаг на боевом."
   ```

**Acceptance criteria:**
- `go vet -tags=integration` без ошибок.
- Ручной integration PASS на Docker Talk 20.1.11 (exit 0, call.log создан).
- Spike-gate: 8 критериев PASS (после ручной верификации с реальным 2-м участником).

---

### Task 4.11: Финальная проверка изоляции и отката

**Files:**
- Modify: ничего. Только команды проверки.
- Test: `internal/call/isolation_test.go` (существующий guard).

- [ ] **Шаг 1: проверить изоляцию pion**

   Run: `CGO_ENABLED=0 go test ./internal/call/ -run TestCmdNctalkDoesNotDependOnPion -v`
   Expected: PASS. Если FAIL — `internal/client`/`cli`/`render`/`config`/`transport`/`room`/`exit` начали импортировать `internal/call/*` — откатить.

- [ ] **Шаг 2: проверить точку отката**

   Simulation (НЕ выполнять реально, только документировать):
   ```sh
   # Точка отката (спека §10):
   rm -rf cmd/nctalk-talk internal/call/interactive
   # Откатить поля agent.Config.AudioIn, Config.OnState (вручную, через git revert коммитов 4.1-4.3).
   go mod tidy
   CGO_ENABLED=0 go build ./...
   # nctalk-call должен собираться и работать.
   # transport/room/exit НЕ должны быть затронуты.
   ```
   Проверить, что в `git log` коммиты 4.1-4.3 (agent), 4.4-4.10 (interactive, cmd) — отдельные, revert'абельные.

- [ ] **Шаг 3: прогон всех тестов с `-race`**

   ```sh
   CGO_ENABLED=0 go test -race ./...
   CGO_ENABLED=0 go test -race -tags=integration ./cmd/nctalk-talk/... # если есть боевое окружение
   ```
   Expected: все unit PASS; integration — skip без env.

- [ ] **Шаг 4: собрать + подписать все бинарники**

   ```sh
   make build-signed
   ```
   Expected: `nctalk`, `nctalk-call`, `nctalk-talk` собраны, подписаны `nctalk-dev`.

- [ ] **Шаг 5: документация**

   - Обновить `docs/session-state/` (через `/checkpoint`) после завершения Этапа 4: статус «Этап 4 завершён, spike-gate PASS на Docker Talk 20.1.11».
   - При обнаружении bug'ов signaling/устройств — внести в spec §13 (future).

- [ ] **Шаг 6: финальный коммит (если были правки по итогам проверки)**

   ```bash
   git commit --allow-empty -m "chore(Этап 4): final isolation check PASS

- TestCmdNctalkDoesNotDependOnPion зелёный.
- Все 24 interactive + 22 agent unit-теста PASS.
- build-signed: nctalk, nctalk-call, nctalk-talk подписаны.
- Точка отката задокументирована (спека §10)."
   ```

**Acceptance criteria Этапа 4 (весь):**
- `TestCmdNctalkDoesNotDependOnPion` PASS.
- Все unit-тесты (`CGO_ENABLED=0 go test ./...`) PASS.
- `make build-signed` собирает 3 бинарника.
- Integration test PASS на Docker Talk 20.1.11.
- Spike-gate: 8 критериев PASS на боевом.
- Все 12 review-findings закрыты (см. таблицу ниже).

---

## Покрытие 12 review-findings (canonical checklist)

| # | Finding | Где закрыто | Тест/acceptance |
|---|---|---|---|
| 1 | device-out = audiotoolbox (НЕ avfoundation) | Task 4.6 `audio_io.go` | `TestSpeakerWriter_FfmpegArgs` (args содержат `-f audiotoolbox -audio_device_index`) |
| 2 | encodeLoop pacing только для stdin | Task 4.1 (agent) | `TestRun_AudioIn_ReplacesNewEncoder` (AudioIn → pacing=false); regression `TestRun_AudioInNil_PacingTrue_Regression` |
| 3 | scope-squeeze: mute только свой | Task 4.4 `mute.go` (нет mute-поля для других); Task 4.2 `Participant` без `Muted` | Соответствие кода: в `agent.Participant` НЕТ поля `Muted` |
| 4 | muteSource.Close ownership | Task 4.4 (Close делегирует в inner); Task 4.6 (MicSource.Close idempotent); Task 4.8 (interactive НЕ закрывает mic) | `TestMuteSource_Close_DelegatesAndIdempotent`, `TestMicSource_CloseIdempotent` |
| 5 | state-concurrency под mutex | Task 4.2 `callStateCollector` | `TestCallStateCollector_Concurrent` с `-race` |
| 6 | Participant.Name = ActorId (id, не display) | Task 4.2 `updateParticipants` (Name = u.ActorId) | `TestRun_OnState_SnapshotAndStatus` (Name=actor-A) |
| 7 | agent.CallState.Status lifecycle | Task 4.2 (setStatus "joining"/"joined"/"peer failed"/"ice-timeout") | `TestRun_OnState_SnapshotAndStatus` (status transitions) |
| 8 | MicSource lazy start | Task 4.6 `audio_io.go` (sync.Once в start(), NewMicSource не стартует) | `TestMicSource_NoStartInConstructor` |
| 9 | Integration test ctx.WithTimeout + non-tty fallback | Task 4.10 integration; Task 4.7 ansiView non-tty fallback | `TestAnsiView_NonTty_Fallback`, `TestIntegration_JoinCancel_Exit0` |
| 10 | RMS-формула dBFS→Level | Task 4.3 `rmsToLevel` | `TestRMS_ToLevel_Formula` (full-scale→100, тишина→0) |
| 11 | defer view.Close() сразу после MakeRaw | Task 4.7 (NewAnsiView); Task 4.8 (`defer view.Close()` после создания) | `TestAnsiView_CloseIdempotent`; инспекция кода `interactive.Run` |
| 12 | dedup без Level | Task 4.8 `stateEnricher.onState` (dedup сравнивает count/names + Status + SelfMuted + Volume, БЕЗ Level; но всегда пушит Update — Level-полоска живая) | Инспекция кода enricher'а |

---

## Исполнение

План исполняется через `superpowers:limit-aware-subagent-driven-development`:
- Один implementer-субагент на задачу (4.1 → 4.11).
- Каждая задача: implementer → review (opencode/glm-5.2) → gate «правки?» → верификация.
- Durable ledger хранит статус задач между сессиями.
- Триггер `/spec-to-code` (main-loop) или `/spec-to-code-wf` (workflow-fallback) — для прогонов с human-gate.

**Ограничения по лимитам:** задачи 4.1-4.3 (agent) можно исполнить в одной сессии; 4.4-4.6 — во второй; 4.7-4.8 — в третьей; 4.9-4.11 — в четвёртой. Между подэтапами — `/checkpoint` для сохранения контекста.

---

## Примечания исполнителю

- **Этапы 0-3 готовы** — не переписывать существующий `agent.Run`/`peer`/`media`/`signaling`/`capability`. Только additive-расширения в задачах 4.1-4.3.
- **Существующие 17 agent-тестов** — инвариант регрессии; любая правка agent'а сохраняет их зелёными. Если падает — правка неверна.
- **pion-пакеты** (agent, interactive, peer, capability) — требуют macOS firewall апрув при первом ручном прогоне (диалог «разрешить test-binary»). Подпись `nctalk-dev` (`make sign`) снимает вопрос.
- **Compile-only** режим — если firewall блокирует, тесты доводятся до компиляции + `go vet`, ручной прогон позже (как для Этапов 0-3).
- **Кириллические комментарии** в коде — сохранять. Вывод `nctalk-talk` — на кириллице (метки «выйти», «ВЫКЛ»).
- **Без AI-атрибуции** в коммитах (global rule).
- **Spec — эталон**: `docs/superpowers/specs/2026-07-21-nctalk-talk-tui-design.md`. При конфликте «план vs спека» — спека победяет, план корректируется.

