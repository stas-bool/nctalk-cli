# Code-review: `nctalk-call` / `nctalk-talk` (ветка `feat/nctalk-calls`)

- **Дата:** 2026-07-19
- **Ветка:** `feat/nctalk-calls` (diff `main...HEAD`, ~12k строк: рефакторинг фундамента + WebRTC-ядро + 3 cmd-точки)
- **Исполнитель:** opencode/glm-5.2 через acpx (изолированный read-only контекст)
- **Режим реализации:** compile-only — тесты написаны, но **не запускались** (macOS firewall блокировал; `go build ./...` и `go vet ./...` зелёные). Оценок покрытия тестами нет.
- **Эталон:** `docs/superpowers/specs/2026-07-19-nctalk-call-design.md`

## Сводка

| Severity | Кол-во |
|---|---|
| Critical | 1 |
| Major | 5 |
| Minor | 8 |
| Nitpick | 5 |

**Общая оценка:** архитектура слоёв `transport → room → exit → call/{signaling, capability, peer, media, agent}` чистая, изоляция `cmd/nctalk` от pion обеспечена (`isolation_test.go`), инвариант redact/redirect переиспользован через `internal/transport`. Рефакторинг фундамента аккуратный, alias'ы сохраняют backcompat.

В WebRTC-ядре — один critical (фанаут encoder'а в mesh кодифицирует неверное предположение о pion) и несколько major (гонка `decoder.Close ∥ WriteSample`, `OwnSessionId` не извлекается, ffmpeg-crash silent, нет `--recvonly`, узкая shutdown-race с потерей exit-кода).

**Рекомендация до spike на боевом:** починить **[1]** (для N>1) и **[3]** (чтобы не звонить себе). [2]/[4]/[5] желательны до production-этапа (Этап 3). Остальное допустимо отложить.

---

## Critical

### [1] `internal/call/peer/peer.go:282` + `internal/call/peer/track.go:112` — N encodeLoop'ов читают из одного AudioSource → audio fanout сломан для mesh (N>1)

**Проблема:** agent создаёт ОДИН `FFmpegEncoder`, а в `AttachOutgoingAudio` (вызывается на каждого peer'а) запускает ОТДЕЛЬНЫЙ `encodeLoop`, читающий из общего `encoder.ReadSample()`. `FFmpegEncoder.pump` шлёт raw-Opus пакеты в один буферизованный канал `packets` (cap=50); несколько горутин-приёмников распределяют пакеты round-robin — каждый peer получает 1/N пакетов. Комментарий в `track.go:106` («Один encoder/encodeLoop на звонок … pion сам размножает отправку через свои RTPSender'ы») кодирует неверную модель: pion НЕ множит отправку между РАЗНЫМИ `PeerConnection`; множит только RTPSender'ы ВНУТРИ одной PC, а у каждого peer'а здесь своя PC со своим `TrackLocalStaticSample`.

**Почему важно / сценарий:** 2+ собеседников слышат по ~50%/~33% тонов/тишины вместо полного потока — звонок неработоспособен. При N=1 (spike «один браузер против агента») баг невиден, но spec §6/§9 явно требует mesh ≤7.

**Предложение:** создать ОДИН `TrackLocalStaticSample` в agent'е (не в peer'е), `AddTrack`'нуть его в каждую PC (pion это поддерживает — один track → N RTPSender'ов), и пустить ОДИН encodeLoop, пишущий в общий track. pion сам разберётся с fanout'ом. Альтернатива — pub-sub `AudioSource`, но это усложнение.

---

## Major

### [2] `internal/call/media/codec.go:283-302` + `ogg/ogg.go:303-378` — data race на ogg.Writer при `decoder.Close ∥ decodeLoop.WriteSample`

**Проблема:** `peer.decodeLoop` вызывает `sink.WriteSample → FFmpegDecoder.WriteSample → ogg.Writer.WritePacket` (мутирует `w.granule`, `w.pageSeq`, пишет в stdin pipe). Параллельно `agent.reconcile`/`closeAllPeers`/`removePeer` вызывает `decoder.Close → ogg.Writer.Flush` (читает `w.granule`, мутирует `pageSeq`, пишет в stdin). Синхронизации нет — `peer.Close` не дожидается завершения `decodeLoop` (`pc.Close` асинхронно останавливает RTPReceiver). `closeOnce` защищает только повторный `Close`, не in-flight `WriteSample`. Проявляется в 3 местах: reconcile (toRemove-loop), closeAllPeers, watchPeer→removePeer.

**Почему важно / сценарий:** при уходе собеседника (или single-peer failure) — гонка на internal-состоянии ogg.Writer и интерливинг байтов в stdin-pipe ffmpeg → повреждённый OGG → ffmpeg-decode падает (худший случай) либо мусор в PCM-выводе. `go test -race` поймал бы сразу.

**Предложение:** либо `sync.Mutex` в `FFmpegDecoder` (лочить WriteSample и Close), либо в `peer.Close` дождаться выхода `decodeLoop` (per-peer `sync.WaitGroup` на decode-goroutines + `Wait` в Close). Второй вариант чище: `peer.Close` становится точкой барьера.

### [3] `cmd/nctalk-call/main.go:206` — `OwnSessionId=""` → агент подключается к собственному sessionId

**Проблема:** `main.go` явно передаёт `OwnSessionId: ""`. Фильтр reconcile (`agent.go:338`) проверяет `if u.SessionId == a.cfg.OwnSessionId { continue }` — при пустом `OwnSessionId` НИКОГДА не совпадает с реальным sessionId, поэтому собственная сессия из `usersInRoom` попадает в wanted-set и для неё создаётся `PeerConnection`. TODO в `main.go:187` признаёт пробел, но не оценивает последствия.

**Почему важно / сценарий:** при JOIN агента браузеры присылают offer на наш sessionId — agent попытается `SetRemoteDescription(offer)` на пир с sid=мы, pion будет ICE-connect'иться к нам же самим. ICE-candidates укажут на тот же хост — минимум wasted work и отправка своего же Opus себе; в худшем случае (loopback PC) pion падает на ICE-consistency check или создаёт RTP-петлю.

**Предложение:** вытащить ownSessionId из signaling-settings (`capability.Settings` уже дёргает тот же эндпоинт — заодно декодировать поле `sessionId`/`userId`), либо из первого `usersInRoom` (где сервер знает нашу сессию), и выставить до `agent.Run`.

### [4] `internal/call/media/codec.go:139-161` + `84-131` — ffmpeg-subprocess может упасть молча, encodeLoop выходит по io.EOF без диагностики

**Проблема:** `pump` goroutine читает OGG из stdout ffmpeg. Если ffmpeg стартовал, но упал в процессе (нет libopus, устройство недоступно, OOM) — stdout закрылся, `ogg.Reader` вернёт `io.EOF`, `pump` запишет nil в `done` и закроет `packets`. `encodeLoop` получит io.EOF и тихо выйдет (`peer.go:129` «Штатные причины конца потока»). Симметрично для decoder: ffmpeg-decode crash → WriteSample падает с broken pipe → decodeLoop тихо выходит (`track.go:84-87`).

**Почему важно / сценарий:** spec §10 явно требует «ffmpeg/sox отсутствуют или упали → exit 1 с указанием backend'а». Текущая реализация продолжит работу с помёрзшим audio-pipe, не сигнализируя `peer.Failed()` и не логируя. Пользователь слышит тишину и не знает почему.

**Предложение:** в `pump` проверить `cmd.Wait()` после io.EOF; если exit code != 0 — запихать в `done` ненулевую ошибку (ffmpeg может закрыть stdout до финальной ошибки). В `encodeLoop` не-EOF ошибку `src.ReadSample` через `signalFailure`, не тихий выход. Симметрично в `decodeLoop` — ошибка WriteSample при живом peer должна идти в `signalFailure`.

### [5] `cmd/nctalk-call/main.go:170` — нет listening-only режима (`InFlags=1`), хотя spec §6 явно описывает `nctalk-call <room> --out rec.pcm`

**Проблема:** `main.go` всегда ставит `inFlags = inFlagSendRecv (3)`. TODO ссылается на «Task 3.x», но spec §6 «Флаги Call API зависят от --in/--out» — часть контракта текущей спеки, не future-work. Нет способа запустить агент в recvonly (запись чужого аудио без отправки своего).

**Почему важно / сценарий:** use-case «listening-only» явно в spec; при `--out rec.pcm` без `--in` пользователь вынужден подавать фиктивный PCM на stdin (иначе ffmpeg-encode сразу получит EOF). SDP отправной m-line будет присутствовать даже когда мы не хотим вещать — пиры ждут нашего звука и не получают.

**Предложение:** при `*inPath == ""` (или флаг `--recvonly`) ставить `InFlags=1` и НЕ создавать encoder. Spec §6: «только --out → flags = IN_CALL без WITH_AUDIO».

### [6] `internal/call/agent/agent.go:222-225` + `243-255` — узкое race-окно теряет фатальную ошибку signaling → exit 0 вместо 1/2

**Проблема:** порядок `close(events); pollDone <- err`. Main-loop при `ok=false` делает `select { case pollErr = <-pollDone: default: }`. По Go memory model: `close(events)` happens-before `pollDone<-err` в горутине, но main, увидев close, НЕ гарантированно видит последующий send. Default-ветка может сработать даже при летящем в буфер значении. `pollErr` остаётся nil, `Run` возвращает nil, cmd выходит 0. Контракт §7/§10 нарушается: фатальная ошибка (404 → exit 2) даёт exit 0.

**Почему важно / сценарий:** при shutdown'е во время прихода signaling-фаталы (Ctrl-C в момент 401/404) — «exit 0» вместо «exit 1/2», нет индикации, что звонок упал по ошибке.

**Предложение:** переставить местами — `pollDone <- err; close(events)`. Тогда `close(events)` гарантирует, что pollDone-send уже виден (memory barrier через close), и select-default всегда берёт значение.

---

## Minor

- **[7]** `peer.go:63,280` — `outTrackSender` сохраняется, но нигде не читается. Мёртвый код; удалить или использовать.
- **[8]** `peer.go:41` + `agent.go:424` — `Config.IsPolite` прокидывается агентом, но `peer.New` не сохраняет/не читает. До реализации perfect-negotiation (Task 2.6) — удалить или пометить TODO-заглушкой.
- **[9]** `agent.go:402-408` — `nextIdx` монотонно растёт, не переиспользуется. После 8 пиров суммарно (за весь звонок, не одновременно) `addPeer` вернёт ошибку «достигнут лимит пиров (8)». Долгий звонок с churn'ом участников заблокирует новых. Переиспользовать слоты или документировать «не более 8 за сессию».
- **[10]** `agent.go` (весь Run) — не реализован `NCTALK_ICE_TIMEOUT` (spec §6/§10) и различение «я один в звонке → exit 0» vs «никто не ответил ICE → exit 1» vs «все peer'ы упали → exit 1». Без ICE-timeout пользователь обязан Ctrl-C. (План Task 3.2 — отложен.)
- **[11]** `peer.go:143,205,316` — исходящие сообщения (candidate/answer/offer) не проставляют `Message.To`. `signaling.Send` кодирует To с omitempty → сервер broadcast'ит всем. Для spike (1 собеседник) — ОК; в mesh ответ A→B попадает и к C → лишний трафик и spam-ошибки в pion-логах.
- **[12]** `ogg/ogg.go:58` — `opusPreSkip` захардкожен =312. Реальный pre-skip libopus voip обычно 312, но при смене application-mode меняется. В agent'е шлём на decode свой собственный OpusHead (отличный от encoder-вывода) — рассинхрон даст смещение звука на pre-skip сэмплов (~6,5мс). spike незаметно; для AEC/диаризации существенно. Парсить pre-skip из encoder-вывода или зафиксировать mode явно.
- **[13]** `peer.go:327-336` + `track.go:112-159` — `peer.Close` отменяет `srcCtx`, но `encodeLoop` может быть заблокирован в `src.ReadSample` (ждёт на `e.packets`/`e.ctx.Done`, НЕ на `peer.srcCtx`). Для single-peer failure (encoder не закрывается) — encodeLoop завис до след. пакета, гоняя память уже ненужного peer'а. Goroutine-leak вплоть до `encoder.Close`.
- **[14]** `track.go:67-90` — при падении ffmpeg-decode `track.ReadRTP` продолжает возвращать RTP-пакеты/ошибки, но `sink.WriteSample` пишет в закрытый pipe → silent exit. peer не сигнализирует `Failed()`, агент не переподключает. Симметрично к [4] на стороне приёма.

---

## Nitpicks (стиль/мелочи, не в счёт)

- `ogg/ogg.go:287` — опечатка в комментарии «наbashlich» (русско-английская смесь).
- `ogg/ogg.go:53-66` — несколько констант без склейки комментариев (`opusTagsMagic`, `opusVendorString`) — выбиваются из стиля.
- `agent.go:570-594` (`pcmMixerWriter.Write`) — аллоцирует `samples := make([]int16, frameSamples)` на каждый кадр (50 Гц × N peers). Для spike незаметно; в проде worth reuse через `sync.Pool`/pre-allocation.
- `peer.go:38-41` — комментарий про perfect-negotiation описывает будущую логику; поле можно закомментировать или `// TODO(2.6):`.
- Микс русско-английских комментариев в нескольких местах — в CLAUDE.md кириллица приветствуется, но единообразие стóит выправить.

---

## Примечание о тестах

Тесты написаны добротно, но **не ловят ни одного из перечисленных багов** (mock-encoder в `agent_test.go` — `fakeEncoder` с `io.EOF` сразу; mock-sink не обнаруживает гонок). Это ожидаемо для compile-only режима и подтверждает, что прогон `go test -race` на CI обязателен после снятия firewall-блока.
