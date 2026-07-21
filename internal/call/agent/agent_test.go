// agent_test.go — тесты agent.Run (Task 2.8). 8 сценариев из brief'а.
//
// ВНИМАНИЕ (compile-only режим): на этой macOS-машине firewall блокирует любой
// test-binary, поэтому тесты НЕ запускаются здесь. Они пишутся compile-clean
// (go vettypecheck'ает _test.go без запуска) и помечены «отложены до ручного
// запуска». Прогон — на машине без firewall-блока или с правом ответа «разрешить».
//
// Все моки (fakePeer/fakeSignaling/fakeEncoder/fakeDecoder) живут здесь — их
// форма dictated тест-кейсом (например, fakePeer.failed — канал инъекции ошибки).
//
// Имена моков с префикса fake, чтобы не коллидировать с peer_test.go/signaling_test.go
// при возможном будущем слиянии тестов в один пакет (сейчас каждый пакет — свой).

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/stas/nctalk/internal/call/media"
	"github.com/stas/nctalk/internal/call/peer"
	"github.com/stas/nctalk/internal/call/signaling"
	"github.com/stas/nctalk/internal/exit"
)

// ---- Compile-time гарантии: моки удовлетворяют интерфейсам agent'а ----

var (
	_ peerConn    = (*fakePeer)(nil)
	_ sigClient   = (*fakeSignaling)(nil)
	_ media.AudioSource = (*fakeEncoder)(nil)
	_ media.AudioSink   = (*fakeDecoder)(nil)
)

// ---- fakePeer — имплементация peerConn с управляемыми каналами ----

// fakePeer имитирует *peer.Peer для тестов. Каналы outgoing/failed/done —
// управляются тестом (инъекция сообщений/ошибок). Close закрывает done и
// инкрементирует closeCount (идемпотентно).
type fakePeer struct {
	outgoing chan signaling.Message // тест шлёт сюда → sendPeer читает
	failed   chan error             // тест шлёт сюда → watchPeer срабатывает
	done     chan struct{}          // закрывается в Close

	handleEvMu sync.Mutex
	handleEvents []signaling.Event // запись всех HandleEvent-вызовов

	attachedTrack *webrtc.TrackLocalStaticSample // review замечание 1: track вместо src
	onSink        media.AudioSink

	// onConnected — callback, зарегистрированный agent'ом через OnConnected.
	// fireConnected() вызывает его, имитируя pion OnICEConnectionStateChange(connected).
	onConnMu  sync.Mutex
	onConnected func()

	closeMu  sync.Mutex
	closed   bool
	closeCnt int
}

func newFakePeer() *fakePeer {
	return &fakePeer{
		outgoing: make(chan signaling.Message, 16),
		failed:   make(chan error, 1),
		done:     make(chan struct{}),
	}
}

func (p *fakePeer) HandleEvent(ev signaling.Event) error {
	p.handleEvMu.Lock()
	p.handleEvents = append(p.handleEvents, ev)
	p.handleEvMu.Unlock()
	return nil
}

func (p *fakePeer) Outgoing() <-chan signaling.Message { return p.outgoing }
func (p *fakePeer) Done() <-chan struct{}              { return p.done }
func (p *fakePeer) Failed() <-chan error               { return p.failed }

func (p *fakePeer) AttachOutgoingTrack(track *webrtc.TrackLocalStaticSample) error {
	p.attachedTrack = track
	return nil
}

func (p *fakePeer) OnIncomingAudio(sink media.AudioSink) {
	p.onSink = sink
}

// CreateOffer — stub: реальный peer.Peer инициирует SDP-exchange (impolite-роль);
// в тестах agent.Run эффект offer'а не проверяется (seed-peer sendrecv-путь
// покрыт на pion-уровне в peer_test.go/TestPeerLoop_NoGlare). Удовлетворяет
// peerConn interface (CreateOffer добавлен в незавершёнке audio-pipe).
func (p *fakePeer) CreateOffer() error { return nil }

// OnConnected — stub peerConn-метода. Production-peer вызывает fn из pion-
// callbackа OnICEConnectionStateChange(connected); fakePeer просто хранит fn,
// а тест дёргает её через fireConnected().
func (p *fakePeer) OnConnected(fn func()) {
	p.onConnMu.Lock()
	p.onConnected = fn
	p.onConnMu.Unlock()
}

// fireConnected имитирует переход peer'а в ICE connected — вызывает callback,
// зарегистрированный через OnConnected. Идемпотентность (1 раз) на совести
// production-peer'а (поле onConnectedFired); в тесте вызываем явно N раз по мере
// надобности.
func (p *fakePeer) fireConnected() {
	p.onConnMu.Lock()
	fn := p.onConnected
	p.onConnMu.Unlock()
	if fn != nil {
		fn()
	}
}

func (p *fakePeer) Close() error {
	p.closeMu.Lock()
	defer p.closeMu.Unlock()
	if !p.closed {
		close(p.done)
		p.closed = true
	}
	p.closeCnt++
	return nil
}

// events возвращает снапшот всех HandleEvent-вызовов (потокобезопасно).
func (p *fakePeer) events() []signaling.Event {
	p.handleEvMu.Lock()
	defer p.handleEvMu.Unlock()
	cp := make([]signaling.Event, len(p.handleEvents))
	copy(cp, p.handleEvents)
	return cp
}

// closeCount возвращает текущее число Close-вызовов.
func (p *fakePeer) closeCount() int {
	p.closeMu.Lock()
	defer p.closeMu.Unlock()
	return p.closeCnt
}

// isClosed — convenience-флаг: Close был вызван хотя бы раз.
func (p *fakePeer) isClosed() bool {
	p.closeMu.Lock()
	defer p.closeMu.Unlock()
	return p.closed
}

// ---- fakeSignaling — sigClient с записью вызовов и инъекцией PollLoop-логики ----

type fakeSignaling struct {
	mu sync.Mutex

	joinCount   int
	joinFlags   int
	joinErr     error // если задано — возвращается из JoinCall
	leaveCount  int
	sentMsgs    []signaling.Message
	leaveErr    error

	// pollLoop — инъекция поведения PollLoop. nil → блок на ctx.Done.
	pollLoop func(ctx context.Context, ch chan<- signaling.Event) error
}

func (s *fakeSignaling) JoinCall(_ context.Context, _ string, flags int) error {
	s.mu.Lock()
	s.joinCount++
	s.joinFlags = flags
	err := s.joinErr
	s.mu.Unlock()
	return err
}

func (s *fakeSignaling) LeaveCall(context.Context, string) error {
	s.mu.Lock()
	s.leaveCount++
	err := s.leaveErr
	s.mu.Unlock()
	return err
}

func (s *fakeSignaling) PollLoop(ctx context.Context, _ string, ch chan<- signaling.Event) error {
	if s.pollLoop != nil {
		return s.pollLoop(ctx, ch)
	}
	<-ctx.Done()
	return nil
}

func (s *fakeSignaling) Send(_ context.Context, _ string, msg signaling.Message) error {
	s.mu.Lock()
	s.sentMsgs = append(s.sentMsgs, msg)
	s.mu.Unlock()
	return nil
}

// snapshot возвращает снапшот записанных Send-сообщений (потокобезопасно).
func (s *fakeSignaling) sentMessages() []signaling.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]signaling.Message, len(s.sentMsgs))
	copy(cp, s.sentMsgs)
	return cp
}

// ---- fakeEncoder / fakeDecoder — простые моки media.AudioSource / AudioSink ----

// fakeEncoder имитирует «живой» encoder: ReadSample блокирует до Close —
// потом возвращает io.EOF. Это нужно, чтобы agentEncodeLoop (один на звонок,
// review замечание 1) НЕ завершался преждевременно в тестах и не уводил Run
// из main-loop раньше, чем отработает reconcile и создаст peers.
type fakeEncoder struct {
	closeCnt int32
	closed   atomic.Bool
	unblock  chan struct{}
}

func newFakeEncoder() *fakeEncoder {
	return &fakeEncoder{unblock: make(chan struct{})}
}

func (e *fakeEncoder) ReadSample() ([]byte, time.Duration, error) {
	<-e.unblock // блок до Close
	return nil, 0, io.EOF
}

func (e *fakeEncoder) Close() error {
	atomic.AddInt32(&e.closeCnt, 1)
	if e.closed.CompareAndSwap(false, true) {
		close(e.unblock)
	}
	return nil
}

func (e *fakeEncoder) closeCount() int32 { return atomic.LoadInt32(&e.closeCnt) }

// fakeDecoder пишет payload напрямую в pcmW (как будто это уже PCM s16le).
//pcmW — это *pcmMixerWriter из agent'а; тест может использовать эту связь,
// чтобы прогнать PCM-кадры через Mixer.
type fakeDecoder struct {
	pcmW     io.Writer
	closeCnt int32
}

func (d *fakeDecoder) WriteSample(payload []byte, _ time.Duration) error {
	// Симулируем FFmpegDecoder: пишем payload в pcmW (например, 1920 байт PCM).
	_, err := d.pcmW.Write(payload)
	return err
}

func (d *fakeDecoder) Close() error {
	atomic.AddInt32(&d.closeCnt, 1)
	return nil
}

func (d *fakeDecoder) closeCount() int32 { return atomic.LoadInt32(&d.closeCnt) }

// ---- Общие helpers для тестов ----

// makeConfig собирает Config с мок-фабриками, считающими создания через атомики.
// Возвращает cfg + указатели на каунтеры и capture-каналы для синхронизации.
type testCounters struct {
	peerCreated     int32
	decoderCreated  int32
	encoderCreated  int32
	peerCh          chan *fakePeer     // каждый созданный fakePeer падает сюда
	decoderCh       chan *fakeDecoder  // каждый созданный fakeDecoder — сюда
}

func newTestCounters() *testCounters {
	return &testCounters{
		peerCh:    make(chan *fakePeer, 16),
		decoderCh: make(chan *fakeDecoder, 16),
	}
}

// buildConfig — собирает Config с моками; eventsFn задаёт поведение PollLoop
// (какие события отправлять до блокировки на ctx).
func (c *testCounters) buildConfig(fs *fakeSignaling, inFlags int, out io.Writer) Config {
	cfg := Config{
		Signaling:    fs,
		Token:        "test-room-token",
		InFlags:      inFlags,
		OwnSessionId: "own-session-id",
		Stdin:        bytes.NewReader(nil),
		Stdout:       out,
		Stderr:       io.Discard,
		NewPeer: func(_ peer.Config) (peerConn, error) {
			atomic.AddInt32(&c.peerCreated, 1)
			p := newFakePeer()
			c.peerCh <- p
			return p, nil
		},
		NewEncoder: func(io.Reader) (media.AudioSource, error) {
			atomic.AddInt32(&c.encoderCreated, 1)
			return newFakeEncoder(), nil
		},
		NewDecoder: func(w io.Writer) (media.AudioSink, error) {
			atomic.AddInt32(&c.decoderCreated, 1)
			d := &fakeDecoder{pcmW: w}
			c.decoderCh <- d
			return d, nil
		},
	}
	return cfg
}

// pollSendThenBlock — helper для fakeSignaling.pollLoop: отправляет события по
// очереди (уважая ctx), затем блокирует до ctx.Done.
func pollSendThenBlock(events []signaling.Event) func(ctx context.Context, ch chan<- signaling.Event) error {
	return func(ctx context.Context, ch chan<- signaling.Event) error {
		for _, ev := range events {
			select {
			case ch <- ev:
			case <-ctx.Done():
				return nil
			}
		}
		<-ctx.Done()
		return nil
	}
}

// users — helper: собирает []signaling.User из пар (sessionId, inCall).
func users(pairs ...struct{ sid string; flags int }) []signaling.User {
	out := make([]signaling.User, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, signaling.User{SessionId: p.sid, InCall: p.flags})
	}
	return out
}

// waitRecvTimeout — читает из канала с таймаутом; t.Helper + Fatal по таймауту.
func waitRecvTimeout[T any](t *testing.T, ch <-chan T, timeout time.Duration) T {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case v := <-ch:
		return v
	case <-time.After(timeout):
		t.Fatalf("timeout %s waiting on channel", timeout)
	}
	var zero T
	return zero
}

// ---- Test 1: JoinAndLeave — recvonly, 1 user → 1 peer, 1 decoder, 0 encoder ----

func TestRun_JoinAndLeave(t *testing.T) {
	fs := &fakeSignaling{
		pollLoop: pollSendThenBlock([]signaling.Event{
			{Kind: signaling.EvUsersUpdated, Users: users(
				struct{ sid string; flags int }{"peer-A", 3},
			)},
		}),
	}
	counters := newTestCounters()
	cfg := counters.buildConfig(fs, 1, io.Discard) // InFlags=1 → recv-only

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	if err := Run(ctx, cfg); err != nil {
		t.Fatalf("Run err = %v, want nil", err)
	}

	if fs.joinCount != 1 {
		t.Errorf("JoinCall count = %d, want 1", fs.joinCount)
	}
	if fs.joinFlags != 1 {
		t.Errorf("JoinCall flags = %d, want 1 (recvonly)", fs.joinFlags)
	}
	if fs.leaveCount != 1 {
		t.Errorf("LeaveCall count = %d, want 1", fs.leaveCount)
	}
	if got := atomic.LoadInt32(&counters.peerCreated); got != 1 {
		t.Errorf("peer created = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&counters.decoderCreated); got != 1 {
		t.Errorf("decoder created = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&counters.encoderCreated); got != 0 {
		t.Errorf("encoder created = %d, want 0 (recvonly)", got)
	}
}

// ---- Test 2: SendRecv — InFlags=3 → encoderFactory 1x, decoderFactory per-peer ----

func TestRun_SendRecv(t *testing.T) {
	fs := &fakeSignaling{
		pollLoop: pollSendThenBlock([]signaling.Event{
			{Kind: signaling.EvUsersUpdated, Users: []signaling.User{
				{SessionId: "peer-A", InCall: 3},
			}},
		}),
	}

	counters := newTestCounters()
	cfg := counters.buildConfig(fs, 3, io.Discard) // InFlags=3 → sendrecv

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	if err := Run(ctx, cfg); err != nil {
		t.Fatalf("Run err = %v, want nil", err)
	}

	if got := atomic.LoadInt32(&counters.encoderCreated); got != 1 {
		t.Errorf("encoder created = %d, want 1 (один encoder на звонок)", got)
	}
	if got := atomic.LoadInt32(&counters.decoderCreated); got != 1 {
		t.Errorf("decoder created = %d, want 1 (per-peer)", got)
	}
	if got := atomic.LoadInt32(&counters.peerCreated); got != 1 {
		t.Errorf("peer created = %d, want 1", got)
	}

	// Проверяем, что к созданному peer подключён общий audioTrack
	// (AttachOutgoingTrack — review замечание 1).
	// peerCh буферизован — забираем первый (и единственный) fakePeer.
	select {
	case p := <-counters.peerCh:
		if p.attachedTrack == nil {
			t.Error("AttachOutgoingTrack не вызван на peer (audioTrack не подключён)")
		}
	default:
		t.Error("peerCh пуст — peer не создан")
	}
}

// ---- Test 3: DecoderPerPeer — 2 пира → ровно 2 decoder'а ----

func TestRun_DecoderPerPeer(t *testing.T) {
	fs := &fakeSignaling{
		pollLoop: pollSendThenBlock([]signaling.Event{
			{Kind: signaling.EvUsersUpdated, Users: []signaling.User{
				{SessionId: "peer-A", InCall: 3},
				{SessionId: "peer-B", InCall: 3},
			}},
		}),
	}
	counters := newTestCounters()
	cfg := counters.buildConfig(fs, 1, io.Discard) // recv-only (encoder не нужен)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond) // больше времени — 2 пира создаются
		cancel()
	}()

	if err := Run(ctx, cfg); err != nil {
		t.Fatalf("Run err = %v, want nil", err)
	}

	if got := atomic.LoadInt32(&counters.peerCreated); got != 2 {
		t.Errorf("peer created = %d, want 2", got)
	}
	if got := atomic.LoadInt32(&counters.decoderCreated); got != 2 {
		t.Errorf("decoder created = %d, want 2 (decoder-per-peer)", got)
	}
}

// ---- Test 4: MixerDrains — fakeDecoder пишет PCM → Mixer → Stdout ----

func TestRun_MixerDrains(t *testing.T) {
	// Управляемый тик: тест пошлёт несколько тиков после инъекции PCM.
	mixerTick := make(chan time.Time, 16)

	fs := &fakeSignaling{
		pollLoop: pollSendThenBlock([]signaling.Event{
			{Kind: signaling.EvUsersUpdated, Users: []signaling.User{
				{SessionId: "peer-A", InCall: 3},
			}},
		}),
	}

	stdout := &bytes.Buffer{}
	counters := newTestCounters()
	cfg := counters.buildConfig(fs, 1, stdout)
	cfg.MixerTick = mixerTick

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() {
		runDone <- Run(ctx, cfg)
	}()

	// Ждём создания decoder'а (после EvUsersUpdated).
	dec := waitRecvTimeout(t, counters.decoderCh, 500*time.Millisecond)

	// Пишем ровно 1 PCM-кадр (1920 байт s16le) через fakeDecoder → pcmMixerWriter.
	// Ненулевые байты, чтобы отличить от тишины (хотя mixer.Mix и тишину выводит).
	pcmFrame := make([]byte, mixerFrameBytes)
	for i := range pcmFrame {
		pcmFrame[i] = 0x10
	}
	if err := dec.WriteSample(pcmFrame, 20*time.Millisecond); err != nil {
		t.Fatalf("WriteSample err = %v", err)
	}

	// Прогоняем несколько тиков — mixerDrain пишет Mix() в Stdout.
	for i := 0; i < 5; i++ {
		mixerTick <- time.Now()
	}
	// Закрываем tick — drain обработает buffered-тики и выйдет по ok=false.
	close(mixerTick)

	// Завершаем Run: cancel → финализация дождётся выхода drain (через drainDone).
	cancel()
	if err := <-runDone; err != nil {
		t.Fatalf("Run err = %v, want nil", err)
	}

	// После <-runDone drain гарантированно вышел (happens-before через drainDone) —
	// никто не пишет в Stdout, проверка без data race.
	// Stdout должен содержать ненулевые байты (mixer.Mix вернул кадр).
	if stdout.Len() == 0 {
		t.Error("Stdout пуст — mixer-drain ничего не записал")
	}
}

// ---- Test 5: SinglePeerFailure — peer.Failed → close + decoder.Close, остальные живут ----

func TestRun_SinglePeerFailure(t *testing.T) {
	fs := &fakeSignaling{
		pollLoop: pollSendThenBlock([]signaling.Event{
			{Kind: signaling.EvUsersUpdated, Users: []signaling.User{
				{SessionId: "peer-A", InCall: 3},
				{SessionId: "peer-B", InCall: 3},
			}},
		}),
	}
	counters := newTestCounters()
	cfg := counters.buildConfig(fs, 1, io.Discard)

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() {
		runDone <- Run(ctx, cfg)
	}()

	// Ждём создания обоих пиров.
	pA := waitRecvTimeout(t, counters.peerCh, 500*time.Millisecond)
	pB := waitRecvTimeout(t, counters.peerCh, 500*time.Millisecond)

	// Достаём их decoders (для проверки close).
	dA := waitRecvTimeout(t, counters.decoderCh, 500*time.Millisecond)
	dB := waitRecvTimeout(t, counters.decoderCh, 500*time.Millisecond)

	// Триггерим failure на pA.
	pA.failed <- errors.New("ICE failed")

	// Даём watcher-горутине время обработать.
	time.Sleep(100 * time.Millisecond)

	// pA должен быть закрыт, его decoder — закрыт.
	if !pA.isClosed() {
		t.Error("pA не закрыт после Failed()")
	}
	if dA.closeCount() == 0 {
		t.Error("decoder A не закрыт после Failed()")
	}

	// pB должен остаться активным (decoder тоже).
	if pB.isClosed() {
		t.Error("pB закрыт преждевременно — single-peer failure не должен ронять остальных")
	}
	if dB.closeCount() != 0 {
		t.Error("decoder B закрыт преждевременно")
	}

	// Run НЕ должен завершиться до ctx.Cancel.
	select {
	case <-runDone:
		t.Fatal("Run завершился после single-peer failure — должен продолжать")
	case <-time.After(50 * time.Millisecond):
		// OK — Run продолжает работать.
	}

	// Завершаем.
	cancel()
	if err := <-runDone; err != nil {
		t.Fatalf("Run err = %v, want nil", err)
	}
}

// ---- Test 6: PeerLeft_Reconcile — EvUsersUpdated без peer-A → Close + decoder.Close ----

func TestRun_PeerLeft_Reconcile(t *testing.T) {
	fs := &fakeSignaling{
		pollLoop: pollSendThenBlock([]signaling.Event{
			// Первый снапшот: peer-A в звонке.
			{Kind: signaling.EvUsersUpdated, Users: []signaling.User{
				{SessionId: "peer-A", InCall: 3},
			}},
			// Второй снапшот: peer-A ушёл (пустой список).
			{Kind: signaling.EvUsersUpdated, Users: []signaling.User{}},
		}),
	}
	counters := newTestCounters()
	cfg := counters.buildConfig(fs, 1, io.Discard)

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() {
		runDone <- Run(ctx, cfg)
	}()

	// Ждём создания peer-A.
	pA := waitRecvTimeout(t, counters.peerCh, 500*time.Millisecond)
	dA := waitRecvTimeout(t, counters.decoderCh, 500*time.Millisecond)

	// Ждём, пока reconcile отработает второй EvUsersUpdated (peer-A покинул).
	//Даём горутине время.
	time.Sleep(150 * time.Millisecond)

	// peer-A должен быть закрыт.
	if !pA.isClosed() {
		t.Error("peer-A не закрыт после reconcile (покинул звонок)")
	}
	if dA.closeCount() == 0 {
		t.Error("decoder-A не закрыт после reconcile")
	}

	cancel()
	if err := <-runDone; err != nil {
		t.Fatalf("Run err = %v, want nil", err)
	}
}

// ---- Test 7: OutgoingRouting — peer.Outgoing → signaling.Send ----

func TestRun_OutgoingRouting(t *testing.T) {
	fs := &fakeSignaling{
		pollLoop: pollSendThenBlock([]signaling.Event{
			{Kind: signaling.EvUsersUpdated, Users: []signaling.User{
				{SessionId: "peer-A", InCall: 3},
			}},
		}),
	}
	counters := newTestCounters()
	cfg := counters.buildConfig(fs, 1, io.Discard)

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() {
		runDone <- Run(ctx, cfg)
	}()

	// Ждём создания peer-A.
	pA := waitRecvTimeout(t, counters.peerCh, 500*time.Millisecond)

	// Инъекция исходящего сообщения от peer-A.
	wantMsg := signaling.Message{
		Type:    "answer",
		To:      "remote-session",
		Payload: []byte(`{"type":"answer","sdp":"fake-sdp"}`),
	}
	pA.outgoing <- wantMsg

	// Даём sendPeer-горутине время отработать.
	time.Sleep(100 * time.Millisecond)

	got := fs.sentMessages()
	if len(got) != 1 {
		t.Fatalf("signaling.Send count = %d, want 1", len(got))
	}
	if got[0].Type != wantMsg.Type || string(got[0].Payload) != string(wantMsg.Payload) {
		t.Errorf("signaling.Send msg = %+v, want %+v", got[0], wantMsg)
	}

	cancel()
	if err := <-runDone; err != nil {
		t.Fatalf("Run err = %v, want nil", err)
	}
}

// ---- Test 8: EvError_Exits — PollLoop шлёт EvError → Run возвращает *exit.ExitError{Code:1} ----

func TestRun_EvError_Exits(t *testing.T) {
	wantErr := exit.Exit(exit.ExitGeneric, errors.New("401 unauthorized"))
	fs := &fakeSignaling{
		pollLoop: func(ctx context.Context, ch chan<- signaling.Event) error {
			// Отправляем EvError с *exit.ExitError — агент должен выйти с ним.
			select {
			case ch <- signaling.Event{Kind: signaling.EvError, Err: wantErr}:
			case <-ctx.Done():
				return nil
			}
			<-ctx.Done()
			return nil
		},
	}
	counters := newTestCounters()
	cfg := counters.buildConfig(fs, 1, io.Discard)

	// ctx с длинным timeout — Run должен выйти сам по EvError, не дожидаясь cancel.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := Run(ctx, cfg)
	if err == nil {
		t.Fatal("Run err = nil, want exit.ExitError")
	}

	// Проверяем, что это именно exit.ExitError (value-тип) с правильным кодом.
	var ee exit.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("Run err тип = %T, want exit.ExitError", err)
	}
	if ee.Code != exit.ExitGeneric {
		t.Errorf("ExitError.Code = %d, want %d", ee.Code, exit.ExitGeneric)
	}

	// LeaveCall всё равно должен быть вызван (best-effort cleanup).
	if fs.leaveCount != 1 {
		t.Errorf("LeaveCall count = %d, want 1 (best-effort cleanup после EvError)", fs.leaveCount)
	}
}

// ---- Test: Spreed unmute при ICE connected (spike-gate root cause 2026-07-20) ----

// TestRun_SendsUnmuteOnICEConnected проверяет, что sendrecv-agent при установке
// ICE-соединения с peer'ом шлёт Spreed signaling-сообщение unmute {name:'audio'}.
//
// Почему это нужно (spike-gate 2026-07-20, browser-side верификация через
// playwright MCP): Spreed CallParticipantsAudioPlayer создаёт <audio>-sink для
// remote участника, но держит его MUTED, пока CallParticipantModel.audioAvailable
// !== true. audioAvailable ставится ИСКЛЮЧИТЕЛЬНО из signaling-сообщения unmute
// (CallParticipantModel._handleUnmute ← simplewebrtc unmute ← peer.send('unmute')).
// Без unmute от нас sink остаётся muted → audioLevel=0 → уши не слышат, хотя RTP
// доходит (packetsReceived растёт, decoder активен). Mute/unmute — это signaling
// (simplewebrtc.js: sendToAll('unmute', {name:'audio'})), НЕ DataChannel.
//
// Поэтому sendrecv-agent должен при ICE connected послать unmute. В этом тесте
// fireConnected() имитирует pion-callback OnICEConnectionStateChange(connected).
func TestRun_SendsUnmuteOnICEConnected(t *testing.T) {
	fs := &fakeSignaling{
		pollLoop: pollSendThenBlock([]signaling.Event{
			{Kind: signaling.EvUsersUpdated, Users: []signaling.User{
				{SessionId: "peer-A", InCall: 3},
			}},
		}),
	}
	counters := newTestCounters()
	cfg := counters.buildConfig(fs, 3, io.Discard) // InFlags=3 → sendrecv (audioTrack создан)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- Run(ctx, cfg) }()

	// Ждём создания peer'а (addPeer после EvUsersUpdated).
	fp := waitRecvTimeout(t, counters.peerCh, 500*time.Millisecond)

	// Симулируем ICE connected — production-peer зовёт callback из pion.
	fp.fireConnected()

	// Ждём unmute в sentMsgs (agent шлёт асинхронно из callback'а).
	deadline := time.Now().Add(500 * time.Millisecond)
	var unmute *signaling.Message
	for time.Now().Before(deadline) && unmute == nil {
		fs.mu.Lock()
		for i := range fs.sentMsgs {
			if fs.sentMsgs[i].Type == "unmute" {
				m := fs.sentMsgs[i]
				unmute = &m
				break
			}
		}
		fs.mu.Unlock()
		if unmute == nil {
			time.Sleep(5 * time.Millisecond)
		}
	}
	cancel()
	<-runDone

	if unmute == nil {
		t.Fatalf("unmute не отправлен после ICE connected; sentMsgs=%v", fs.sentMsgs)
	}
	if unmute.To != "peer-A" {
		t.Errorf("unmute.To = %q, want %q (sessionId remote-пира)", unmute.To, "peer-A")
	}
	// payload — {"name":"audio"} (Spreed _handleUnmute различает audio/video по name).
	var p map[string]string
	if err := json.Unmarshal(unmute.Payload, &p); err != nil {
		t.Fatalf("unmute payload не JSON-object: %v (raw=%s)", err, unmute.Payload)
	}
	if p["name"] != "audio" {
		t.Errorf("unmute payload name = %q, want %q", p["name"], "audio")
	}
}

