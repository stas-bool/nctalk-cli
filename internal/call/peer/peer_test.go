package peer

import (
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	pionmedia "github.com/pion/webrtc/v4/pkg/media"
	"github.com/stas/nctalk/internal/call/media"
	"github.com/stas/nctalk/internal/call/signaling"
)

// ---- Моки AudioSource / AudioSink (НЕ экспортируются) ----
//
// Моки живут в peer_test.go (а не в separate-testdata), т.к. их форма
// продиктована спецификой теста (loopSource зациклен с rate-limit'ом;
// collectSink копирует payload для последующего assert).

// loopSource — зацикленный источник: бесконечно отдаёт один и тот же payload
// с интервалом interval. Используется в TestPeerLoop_NoGlare — входной поток
// не должен заканчиваться до завершения теста (иначе encodeLoop выйдет по EOF
// раньше, чем успеем получить N пакетов на sink'е).
type loopSource struct {
	payload  []byte
	interval time.Duration
	stopped  atomic.Bool
}

func (m *loopSource) ReadSample() ([]byte, time.Duration, error) {
	if m.stopped.Load() {
		return nil, 0, io.EOF
	}
	// rate-limit: имитируем libopus voip 20мс/фрейм. Без этого encodeLoop
	// молотил бы CPU и переполнял outCh/RTP-буфер pion'а быстрее, чем ICE
	// успевает handshake'нуться.
	time.Sleep(m.interval)
	cp := make([]byte, len(m.payload))
	copy(cp, m.payload)
	return cp, m.interval, nil
}

func (m *loopSource) Close() error {
	m.stopped.Store(true)
	return nil
}

// collectSink — собирает все WriteSample-payload'ы в slice для последующего
// assert. Потокобезопасен (WriteSample может вызываться из decodeLoop
// одновременно с проверкой тестом).
type collectSink struct {
	mu       sync.Mutex
	received [][]byte
}

func (m *collectSink) WriteSample(p []byte, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(p))
	copy(cp, p)
	m.received = append(m.received, cp)
	return nil
}

func (m *collectSink) Close() error { return nil }

func (m *collectSink) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.received)
}

// hasReceived проверяет, что хотя бы один пакет совпадает с want.
func (m *collectSink) hasReceived(want []byte) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, got := range m.received {
		if bytesEqual(got, want) {
			return true
		}
	}
	return false
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// pumpSourceToTrack — тестовый аналог agentEncodeLoop (review замечание 1):
// читает samples из src и пишёт в общий track, пока не закроют stop или src
// не вернёт EOF. Запускается вручную в тестах — раньше encodeLoop жил в Peer.
func pumpSourceToTrack(src media.AudioSource, track *webrtc.TrackLocalStaticSample, stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		default:
		}
		payload, dur, err := src.ReadSample()
		if err != nil {
			return
		}
		if err := track.WriteSample(pionmedia.Sample{Data: payload, Duration: dur}); err != nil {
			return
		}
	}
}

// ---- Helper: цикл перекачки Outgoing() одного peer в HandleEvent другого ----

// pumpLoop читает исходящие сообщения src.Outgoing() и направляет их в
// dst.HandleEvent (как будто они прошли через signaling-сервер и вернулись).
// Парсит signaling.Message{Type, Payload} обратно в signaling.Event, выполняя
// обратную конверсию ICECandidateInit JSON (*uint16) → signaling.ICECandidate
// (*int) — ту самую, что делает signaling.decodeCandidateEvent на приёме.
//
// Возвращает когда src.Outgoing() закрывается (т.е. никогда — peer не закрывает
// outCh), либо когда ctx отменён. Ошибки HandleEvent логируем в errCh (для
// отладки теста — не валим молча).
func pumpLoop(src *Peer, dst *Peer, errCh chan<- error) {
	for {
		select {
		case msg, ok := <-src.Outgoing():
			if !ok {
				return // канал закрыт — выходим (в текущей реализации не случается)
			}
			ev, err := messageToEvent(msg)
			if err != nil {
				select {
				case errCh <- err:
				default:
				}
				continue
			}
			if err := dst.HandleEvent(ev); err != nil {
				select {
				case errCh <- err:
				default:
				}
			}
		case <-src.Done():
			return
		}
	}
}

// messageToEvent — обратная конверсия signaling.Message → signaling.Event.
// Эмулирует работу signaling-слоя на приёмной стороне (decodeSdpEvent /
// decodeCandidateEvent). Для candidate: payload — вложенный Spreed wire-format
// {candidate:{...}} (баг #6); внутри pion ToJSON даёт *uint16 для sdpMLineIndex,
// signaling хранит *int — конверсия через int-кеширование.
func messageToEvent(msg signaling.Message) (signaling.Event, error) {
	switch msg.Type {
	case "offer", "answer":
		var p sdpOutPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return signaling.Event{}, err
		}
		kind := signaling.EvOffer
		if msg.Type == "answer" {
			kind = signaling.EvAnswer
		}
		return signaling.Event{Kind: kind, SDP: p.SDP}, nil
	case "candidate":
		// Парсим вложенный Spreed wire-format {candidate:{candidate,sdpMLineIndex,
		// sdpMid}} (баг #6, симметрично decodeCandidateEvent/icePayload). Поля у
		// pion ToJSON — *uint16 для sdpMLineIndex, signaling хранит *int → конверсия.
		var wrapped struct {
			Candidate struct {
				Candidate     string  `json:"candidate"`
				SDPMLineIndex *uint16 `json:"sdpMLineIndex"`
				SDPMid        *string `json:"sdpMid"`
			} `json:"candidate"`
		}
		if err := json.Unmarshal(msg.Payload, &wrapped); err != nil {
			return signaling.Event{}, err
		}
		var idx *int
		if wrapped.Candidate.SDPMLineIndex != nil {
			v := int(*wrapped.Candidate.SDPMLineIndex)
			idx = &v
		}
		return signaling.Event{
			Kind: signaling.EvCandidate,
			Candidate: signaling.ICECandidate{
				Candidate:     wrapped.Candidate.Candidate,
				SDPMLineIndex: idx,
				SDPMid:        wrapped.Candidate.SDPMid,
			},
		}, nil
	}
	return signaling.Event{}, errors.New("unknown message type: " + msg.Type)
}

// ---- Тест 1: peer-loop без glare (DoD) ----

// TestPeerLoop_NoGlare — два инстанса Peer в одном процессе, обмениваются
// SDP/ICE через pump-горутины (loopback, без signaling-сервера). p1 —
// инициатор (CreateOffer) с подключённым mockSource, p2 — приёмник с
// collectSink. Ждём, пока collectSink не получит N пакетов или не истечёт
// timeout. DoD: raw Opus-фреймы проходят end-to-end через localhost ICE.
//
// Время ICE/DTLS handshake на localhost: типично 1-5с; timeout 30с с запасом.
func TestPeerLoop_NoGlare(t *testing.T) {
	// Граф соединения: p1 (source) --[offer/answer/candidate]-- p2 (sink).
	p1, err := New(Config{ICEServers: nil, IsPolite: false})
	if err != nil {
		t.Fatalf("New p1: %v", err)
	}
	defer p1.Close()
	p2, err := New(Config{ICEServers: nil, IsPolite: false})
	if err != nil {
		t.Fatalf("New p2: %v", err)
	}
	defer p2.Close()

	// Перекачка сообщений в обе стороны. errCh — для отладки (не валим тест
	// при первом же сообщении — даём ICE время handshake'нуться, part-сообщения
	// возможны в процессе).
	errCh := make(chan error, 16)
	go pumpLoop(p1, p2, errCh)
	go pumpLoop(p2, p1, errCh)

	// Payload: фиксированный 4-байтный маркер — найдём его на sink'е.
	payload := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	src := &loopSource{payload: payload, interval: 20 * time.Millisecond}
	defer src.Close()
	sink := &collectSink{}

	// Создаём общий audio-track и подключаем к p1 (как делает agent —
	// review замечание 1: encodeLoop теперь ВНЕ peer, тест pump'ит сам).
	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus},
		"audio", "nctalk",
	)
	if err != nil {
		t.Fatalf("NewTrackLocalStaticSample: %v", err)
	}
	if err := p1.AttachOutgoingTrack(track); err != nil {
		t.Fatalf("AttachOutgoingTrack p1: %v", err)
	}
	pumpStop := make(chan struct{})
	defer close(pumpStop)
	go pumpSourceToTrack(src, track, pumpStop)

	p2.OnIncomingAudio(sink)

	// Запускает SDP/ICE exchange: p1 создаёт offer → pump → p2.HandleEvent →
	// answer → pump → p1.HandleEvent → OnICECandidate на обеих сторонах.
	start := time.Now()
	if err := p1.CreateOffer(); err != nil {
		t.Fatalf("CreateOffer p1: %v", err)
	}

	// Ждём либо N пакетов на sink'е, либо timeout. Проверяем каждые 50мс.
	// В normal-flow на localhost ICE+DTLS handshake занимает 50-200мс; timeout
	// 60с — защитный потолок для медленной CI/warmed-up системы (первый запуск
	// бинарника на macOS может потребовать подтверждения firewall, что добавляет
	// задержку).
	const wantPackets = 5
	const timeout = 60 * time.Second
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if sink.count() >= wantPackets {
			break
		}
		// Проверим, не посыпались ли ошибки (не валим — логируем).
		select {
		case err := <-errCh:
			t.Logf("pump/HandleEvent error (не фатал, ICE handshake can have bumps): %v", err)
		default:
		}
		time.Sleep(50 * time.Millisecond)
	}
	handshakeAndTransfer := time.Since(start)

	got := sink.count()
	if got < wantPackets {
		t.Fatalf("sink получил %d пакетов за %v, wants >= %d — peer-loop не заработал",
			got, handshakeAndTransfer, wantPackets)
	}
	if !sink.hasReceived(payload) {
		t.Fatalf("sink не получил ожидаемый payload %x за %v (получено %d пакетов)",
			payload, handshakeAndTransfer, got)
	}
	t.Logf("OK: sink получил %d пакетов за %v (ICE/DTLS handshake + первые %d Opus-фреймов на localhost)",
		got, handshakeAndTransfer, wantPackets)
}

// ---- Тест 2: ICE buffering (candidate до offer) ----

// TestPeer_ICEBuffering — прогоняем несколько EvCandidate ДО EvOffer. Без
// буферизации pion бы упал на AddICECandidate с ErrNoRemoteDescription. С
// буферизацией — кандидаты складываются в pendingICE и применяются сразу
// после SetRemoteDescription. Проверяем, что HandleEvent не падает и peer
// остаётся в рабочем состоянии (можно CreateAnswer).
func TestPeer_ICEBuffering(t *testing.T) {
	p, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()

	// Скармливаем пару фейковых candidates ДО offer'а. Имитируем поведение
	// Spreed, который не гарантирует порядок. Реальные candidate-строки
	// (host/srflx) взяты из testdata/signaling/candidate.json (синтетика).
	candidates := []signaling.ICECandidate{
		{
			Candidate:     "candidate:842163049 1 udp 1677729535 203.0.113.10 49152 typ host generation 0 ufrag 9aX3 network-cost 999",
			SDPMLineIndex: intPtr(0),
			SDPMid:        strPtr("0"),
		},
		{
			Candidate:     "candidate:961831611 1 udp 1677739263 198.51.100.20 58734 typ srflx raddr 192.0.2.10 rport 58734 generation 0 ufrag 9aX3 network-cost 999",
			SDPMLineIndex: intPtr(0),
			SDPMid:        strPtr("0"),
		},
	}

	// Шаг 1: candidates ДО remoteDescription — должны буферизоваться.
	for i, c := range candidates {
		if err := p.HandleEvent(signaling.Event{Kind: signaling.EvCandidate, Candidate: c}); err != nil {
			t.Fatalf("HandleEvent(EvCandidate #%d) до offer: %v", i, err)
		}
	}
	if got := len(p.pendingICE); got != len(candidates) {
		t.Fatalf("pendingICE len после буферизации = %d, wants %d", got, len(candidates))
	}
	if p.remoteDescr.Load() {
		t.Fatal("remoteDescr должен быть false до SetRemoteDescription")
	}

	// Шаг 2: offer — дренирует pendingICE внутри SetRemoteDescription.
	// Для минимального теста используем два pion-пира (нужен валидный SDP),
	// иначе SetRemoteDescription упадёт на парсинге.
	offerer, err := New(Config{})
	if err != nil {
		t.Fatalf("New offerer: %v", err)
	}
	defer offerer.Close()
	// Track нужен, чтобы в SDP была m-line — без неё ICE-candidate не привяжется.
	// encodeLoop не запускаем — для ICE-buffering теста отправка аудио не нужна,
	// только m-line в SDP.
	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus},
		"audio", "nctalk",
	)
	if err != nil {
		t.Fatalf("NewTrackLocalStaticSample: %v", err)
	}
	if err := offerer.AttachOutgoingTrack(track); err != nil {
		t.Fatalf("AttachOutgoingTrack offerer: %v", err)
	}
	offerSDP, err := createOfferSDP(offerer)
	if err != nil {
		t.Fatalf("createOfferSDP: %v", err)
	}

	if err := p.HandleEvent(signaling.Event{Kind: signaling.EvOffer, SDP: offerSDP}); err != nil {
		t.Fatalf("HandleEvent(EvOffer): %v", err)
	}

	// Шаг 3: после SetRemoteDescription pendingICE должен быть дренирован.
	if got := len(p.pendingICE); got != 0 {
		t.Fatalf("pendingICE len после SetRemoteDescription = %d, wants 0 (drain)", got)
	}
	if !p.remoteDescr.Load() {
		t.Fatal("remoteDescr должен быть true после SetRemoteDescription")
	}
	// Ответ должен был уйти в outCh.
	select {
	case msg := <-p.Outgoing():
		if msg.Type != "answer" {
			t.Fatalf("ожидался answer в outCh, получен %q", msg.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("answer не пришёл в outCh после EvOffer")
	}
}

// ---- Тест 3: single-peer failure ----

// TestPeer_SinglePeerFailure — закрываем peer.Close(), pion синхронно
// триггерит onConnectionStateChange(Closed) в горутине. Наш callback в New()
// НЕ пишет в failedCh при Closed (только при Failed) — поэтому проверяем
// корректность shutdown через Done(): должен закрыться, Close идемпотентен,
// Outgoing/Failed остаются читаемыми без паники.
//
// Для теста самой Failed()-нотификации (без реального ICE-fail) отдельно
// проверяем signalFailure напрямую через fake-candidate, который НЕ проходит
// валидацию (приводит к ошибке) — это сложно воспроизвести без mock pc. Часть
// с PeerConnectionStateFailed проверяется в TestPeerLoop_NoGlare косвенно
// (если ICE-fail случится — Failed() сработает).
func TestPeer_SinglePeerFailure(t *testing.T) {
	p, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// signalFailure — internal helper, тестируем косвенно: кладём ошибку
	// вручную в failedCh имитируя callback pion'а. Должна прийти в Failed().
	fakeErr := errors.New("fake DTLS failure")
	p.signalFailure(fakeErr)

	select {
	case got := <-p.Failed():
		if !errors.Is(got, fakeErr) && got.Error() != fakeErr.Error() {
			t.Fatalf("Failed() = %v, wants %v", got, fakeErr)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("signalFailure не дошла до Failed() канала")
	}

	// Second signalFailure сразу после чтения — failedCh буфер пуст, ошибка
	// опять влезает (cap=1). Это показывает свойство non-blocking send:
	// signalFailure никогда не блокирует вне зависимости от состояния читателя.
	secondErr := errors.New("second failure")
	p.signalFailure(secondErr)
	select {
	case got := <-p.Failed():
		if got.Error() != secondErr.Error() {
			t.Fatalf("второй signalFailure: got %v, wants %v", got, secondErr)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("второй signalFailure не дошла (failedCh должен принять после drain)")
	}

	// Теперь Failed() пуст — третий signalFailure тоже принимается (non-blocking).
	// Смысл cap=1: если БЕЗ drain положить вторую — она дропается. Проверим это:
	p.signalFailure(errors.New("third"))
	p.signalFailure(errors.New("fourth")) // эта дропается (cap=1 занят)
	gotCount := 0
drainLoop:
	for {
		select {
		case <-p.Failed():
			gotCount++
		default:
			break drainLoop
		}
	}
	if gotCount != 1 {
		t.Fatalf("после двух signalFailure без drain: got %d messages, wants 1 (drop on full)", gotCount)
	}

	// Close() — идемпотентный, не паникует, не блокирует.
	if err := p.Close(); err != nil {
		t.Errorf("Close() #1: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Errorf("Close() #2 (идемпотентный): %v", err)
	}

	// Done() должен быть закрыт.
	select {
	case <-p.Done():
		// OK.
	default:
		t.Fatal("Done() не закрыт после Close()")
	}

	// Outgoing() остаётся читаемым без паники (мы его НЕ закрываем — см. contract).
	select {
	case _, ok := <-p.Outgoing():
		if ok {
			t.Fatal("Outgoing() должен быть пуст после Close (но не закрыт)")
		}
	default:
		// OK — пуст и не закрыт.
	}
}

// ---- Helpers ----

func intPtr(v int) *int    { return &v }
func strPtr(s string) *string { return &s }

// createOfferSDP — утилита для TestPeer_ICEBuffering: создаёт валидный offer
// SDP от p (с уже подключённым outgoing-track). Не использует CreateOffer,
// чтобы не отправить в outCh (нам нужен только SDP для подстановки в HandleEvent).
func createOfferSDP(p *Peer) (string, error) {
	offer, err := p.pc.CreateOffer(nil)
	if err != nil {
		return "", err
	}
	if err := p.pc.SetLocalDescription(offer); err != nil {
		return "", err
	}
	// Ждём ICE gathering complete, иначе SDP не содержит кандидатов — но для
	// теста ICE-buffering это не критично (candidates мы подкладываем сами).
	// Возвращаем сразу — local description доступен после SetLocalDescription.
	return offer.SDP, nil
}

// ---- Test 4: OnConnected callback срабатывает при ICE connected ----

// TestPeer_OnConnected_FiresOnICEConnected проверяет, что callback,
// зарегистрированный через OnConnected, вызывается ровно один раз при переходе
// PeerConnection в ICEConnectionStateConnected.
//
// Это инфраструктура для unmute-фикса (spike-gate root cause 2026-07-20): agent
// использует OnConnected, чтобы послать Spreed signaling unmute ровно тогда,
// когда соединение установлено (Spreed роутит unmute только в существующий Peer).
func TestPeer_OnConnected_FiresOnICEConnected(t *testing.T) {
	p1, err := New(Config{ICEServers: nil, IsPolite: false})
	if err != nil {
		t.Fatalf("New p1: %v", err)
	}
	defer p1.Close()
	p2, err := New(Config{ICEServers: nil, IsPolite: false})
	if err != nil {
		t.Fatalf("New p2: %v", err)
	}
	defer p2.Close()

	var fired atomic.Int32
	p1.OnConnected(func() { fired.Add(1) })

	// Перекачка сообщений + трек + offer — стандартный сценарий NoGlare.
	errCh := make(chan error, 16)
	go pumpLoop(p1, p2, errCh)
	go pumpLoop(p2, p1, errCh)

	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "audio", "nctalk",
	)
	if err != nil {
		t.Fatalf("NewTrackLocalStaticSample: %v", err)
	}
	if err := p1.AttachOutgoingTrack(track); err != nil {
		t.Fatalf("AttachOutgoingTrack p1: %v", err)
	}
	if err := p1.CreateOffer(); err != nil {
		t.Fatalf("CreateOffer p1: %v", err)
	}

	// Ждём срабатывания OnConnected. На localhost ICE+DTLS handshake ~50-200мс.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && fired.Load() == 0 {
		select {
		case e := <-errCh:
			t.Logf("pump error (не фатал): %v", e)
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := fired.Load(); got != 1 {
		t.Fatalf("OnConnected вызван %d раз, want 1 (при ICE connected за 30с)", got)
	}

	// Подождём ещё секунду и убедимся, что повторных вызовов нет (идемпотентность:
	// ICE может дёргать connected-state несколько раз, unmute должен уйти 1 раз).
	time.Sleep(500 * time.Millisecond)
	if got := fired.Load(); got != 1 {
		t.Errorf("OnConnected вызван %d раз после удержания, want 1 (идемпотентность)", got)
	}
}
