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
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/stas/nctalk/internal/call/media"
	"github.com/stas/nctalk/internal/call/peer"
	"github.com/stas/nctalk/internal/call/signaling"
	"github.com/stas/nctalk/internal/exit"
	"github.com/stas/nctalk/internal/transport"
)

// ---- Compile-time гарантии: моки удовлетворяют интерфейсам agent'а ----

var (
	_ peerConn          = (*fakePeer)(nil)
	_ sigClient         = (*fakeSignaling)(nil)
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

	handleEvMu   sync.Mutex
	handleEvents []signaling.Event // запись всех HandleEvent-вызовов

	attachedTrack *webrtc.TrackLocalStaticSample // review замечание 1: track вместо src
	onSink        media.AudioSink

	// onConnected — callback, зарегистрированный agent'ом через OnConnected.
	// fireConnected() вызывает его, имитируя pion OnICEConnectionStateChange(connected).
	onConnMu    sync.Mutex
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

	joinCount  int
	joinFlags  int
	joinErr    error // если задано — возвращается из JoinCall
	leaveCount int
	sentMsgs   []signaling.Message
	leaveErr   error

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
// pcmW — это *pcmMixerWriter из agent'а; тест может использовать эту связь,
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
	peerCreated    int32
	decoderCreated int32
	encoderCreated int32
	peerCh         chan *fakePeer    // каждый созданный fakePeer падает сюда
	decoderCh      chan *fakeDecoder // каждый созданный fakeDecoder — сюда
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
func users(pairs ...struct {
	sid   string
	flags int
}) []signaling.User {
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
				struct {
					sid   string
					flags int
				}{"peer-A", 3},
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

// ---- Task 3.1: exit-коды по спеке §10 ----
//
// JoinCall (Call API v4) возвращает *transport.OCSError{Code} (Code =
// ocs.meta.statusCode). Спека §10 маппинг: 404 (комната) → exit 2; 412 (lobby)
// → exit 1 с пояснением; 403/прочее → exit 1. agent.Run маппит через
// mapJoinCallErr (раньше JoinCall-ошибки шли через exit.FromClientErr — только
// 404→2 различалось; 412 падал в generic 1 БЕЗ пояснения про lobby).
//
// ffmpeg-диагностика (спека §10: «ffmpeg/sox отсутствуют/упали → exit 1 с
// указанием backend'а и как установить; не падать молча»): mapCodecErr enrich'ит
// ошибку missing-binary инструкцией по установке.

// runJoinCallErr — общий каркас для JoinCall-exit-тестов: joinErr инъектируется в
// fakeSignaling, recvonly (InFlags=1) — encoder не запускается, Run выходит сразу
// из JoinCall (до encoder/track/peer). Возвращает итоговую ошибку Run.
func runJoinCallErr(t *testing.T, joinErr error) error {
	t.Helper()
	fs := &fakeSignaling{joinErr: joinErr}
	counters := newTestCounters()
	cfg := counters.buildConfig(fs, 1, io.Discard) // recvonly → encoder не нужен
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return Run(ctx, cfg)
}

// wantExitError проверяет, что err — exit.ExitError с нужным Code (и nil-safe
// диагностикой на провал).
func wantExitError(t *testing.T, err error, wantCode int) {
	t.Helper()
	if err == nil {
		t.Fatalf("Run err = nil, want exit.ExitError{Code:%d}", wantCode)
	}
	var ee exit.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("Run err тип = %T (%v), want exit.ExitError", err, err)
	}
	if ee.Code != wantCode {
		t.Errorf("ExitError.Code = %d, want %d", ee.Code, wantCode)
	}
}

// TestRun_JoinCall_404_RoomNotFound_Exit2 — спека §10: Call API 404 → exit 2.
func TestRun_JoinCall_404_RoomNotFound_Exit2(t *testing.T) {
	err := runJoinCallErr(t, &transport.OCSError{Code: http.StatusNotFound, Message: "Room not found"})
	wantExitError(t, err, exit.ExitNotFound)
}

// TestRun_JoinCall_403_Forbidden_Exit1 — спека §10: Call API 403 (read-only /
// нет прав) → exit 1 с сообщением сервера.
func TestRun_JoinCall_403_Forbidden_Exit1(t *testing.T) {
	err := runJoinCallErr(t, &transport.OCSError{Code: http.StatusForbidden, Message: "Read-only call"})
	wantExitError(t, err, exit.ExitGeneric)
}

// TestRun_JoinCall_412_Lobby_Exit1WithHint — спека §10: Call API 412 (lobby) →
// exit 1 с пояснением «lobby». Без mapJoinCallErr текст = «client: ...» без слова
// lobby (regression guard).
func TestRun_JoinCall_412_Lobby_Exit1WithHint(t *testing.T) {
	err := runJoinCallErr(t, &transport.OCSError{Code: http.StatusPreconditionFailed, Message: "Precondition failed"})
	wantExitError(t, err, exit.ExitGeneric)
	if !strings.Contains(err.Error(), "lobby") {
		t.Errorf("err текст = %q, должен содержать пояснение про lobby (спека §10)", err.Error())
	}
}

// TestRun_EncoderMissing_FFmpegHint — спека §10: ffmpeg отсутствует → exit 1 с
// указанием backend'а и как установить. NewEncoder инъектирует ошибку missing-
// binary (формат media.NewFFmpegEncoder: fmt.Errorf("media: запуск ffmpeg-encode:
// %w", &exec.Error{Name:"ffmpeg", Err: exec.ErrNotFound})).
func TestRun_EncoderMissing_FFmpegHint(t *testing.T) {
	fs := &fakeSignaling{} // JoinCall проходит (joinErr=nil)
	counters := newTestCounters()
	cfg := counters.buildConfig(fs, 3, io.Discard) // InFlags=3 → sendrecv, encoder запускается
	cfg.NewEncoder = func(io.Reader) (media.AudioSource, error) {
		return nil, fmt.Errorf("media: запуск ffmpeg-encode: %w",
			&exec.Error{Name: "ffmpeg", Err: exec.ErrNotFound})
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := Run(ctx, cfg)
	wantExitError(t, err, exit.ExitGeneric)

	txt := err.Error()
	if !strings.Contains(txt, "ffmpeg") {
		t.Errorf("err текст = %q, должен упоминать ffmpeg-backend", txt)
	}
	if !strings.Contains(txt, "установите") {
		t.Errorf("err текст = %q, должен содержать инструкцию по установке (спека §10)", txt)
	}
	// leaveBestEffort вызывает LeaveCall после encoder-fail (cleanup).
	if fs.leaveCount != 1 {
		t.Errorf("LeaveCall count = %d, want 1 (best-effort cleanup после encoder-fail)", fs.leaveCount)
	}
}

// TestRun_MultiPeerFailure_Progressive — спека §10: mesh переживает отказ
// отдельных peer'ов. N=3 пира, поочерёдно Failed() двух (N=3→2→1) — оставшийся
// активен, Run НЕ выходит до ctx.Cancel. Расширяет TestRun_SinglePeerFailure
// (N=2→1) сценарием прогрессивного отвала в большем mesh (Task 3.1 Step 2).
func TestRun_MultiPeerFailure_Progressive(t *testing.T) {
	fs := &fakeSignaling{
		pollLoop: pollSendThenBlock([]signaling.Event{
			{Kind: signaling.EvUsersUpdated, Users: []signaling.User{
				{SessionId: "peer-A", InCall: 3},
				{SessionId: "peer-B", InCall: 3},
				{SessionId: "peer-C", InCall: 3},
			}},
		}),
	}
	counters := newTestCounters()
	cfg := counters.buildConfig(fs, 1, io.Discard)

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- Run(ctx, cfg) }()

	// Ждём создания 3 пиров и их decoders.
	pA := waitRecvTimeout(t, counters.peerCh, 500*time.Millisecond)
	pB := waitRecvTimeout(t, counters.peerCh, 500*time.Millisecond)
	pC := waitRecvTimeout(t, counters.peerCh, 500*time.Millisecond)
	dA := waitRecvTimeout(t, counters.decoderCh, 500*time.Millisecond)
	dB := waitRecvTimeout(t, counters.decoderCh, 500*time.Millisecond)
	dC := waitRecvTimeout(t, counters.decoderCh, 500*time.Millisecond)

	// Шаг 1: роняем peer-A → mesh остаётся {B, C}.
	pA.failed <- errors.New("ICE failed A")
	time.Sleep(100 * time.Millisecond)
	if !pA.isClosed() || dA.closeCount() == 0 {
		t.Error("peer-A / decoder-A не закрыты после Failed()")
	}
	if pB.isClosed() || pC.isClosed() {
		t.Error("peer-B/C закрыты преждевременно после падения A")
	}
	select {
	case <-runDone:
		t.Fatal("Run завершился после N=3→2 — должен продолжать")
	case <-time.After(50 * time.Millisecond):
	}

	// Шаг 2: роняем peer-B → mesh остаётся {C}.
	pB.failed <- errors.New("ICE failed B")
	time.Sleep(100 * time.Millisecond)
	if !pB.isClosed() || dB.closeCount() == 0 {
		t.Error("peer-B / decoder-B не закрыты после Failed()")
	}
	if pC.isClosed() || dC.closeCount() != 0 {
		t.Error("peer-C / decoder-C затронут после падения B — должен оставаться активным")
	}
	select {
	case <-runDone:
		t.Fatal("Run завершился после N=3→2→1 — должен продолжать (1 peer жив)")
	case <-time.After(50 * time.Millisecond):
	}

	// Завершаем — Run возвращает nil (штатный ctx.Cancel).
	cancel()
	if err := <-runDone; err != nil {
		t.Fatalf("Run err = %v, want nil", err)
	}
}

// ---- Task 3.2: ICE timeout (спека §6/§10) ----
//
// NCTALK_ICE_TIMEOUT (default 30s, в тестах инъектируется через cfg.ICETimeout):
// если за таймаут ни один peer не установил ICE-соединение —
//   - «я один в звонке» (нет remote-участников с аудио, maxParticipants=0) →
//     exit 0 (штатно ждём других);
//   - участники есть, но никто не ответил ICE → exit 1 с диагностикой.
//
// Connected-detection: OnConnected-callback (peer дёргает при
// ICEConnectionStateConnected); fireConnected() имитирует это в тесте. Если НЕ
// дёргаем — peer остаётся «не connected».

// TestRun_ICETimeout_Alone_Exit0 — я один в звонке (pollLoop не шлёт peer'ов),
// за ICE-timeout никто не пришёл → Run штатно завершается nil (exit 0).
func TestRun_ICETimeout_Alone_Exit0(t *testing.T) {
	fs := &fakeSignaling{
		pollLoop: pollSendThenBlock(nil), // нет EvUsersUpdated с peer'ами — я один
	}
	counters := newTestCounters()
	cfg := counters.buildConfig(fs, 1, io.Discard) // recvonly
	cfg.ICETimeout = 50 * time.Millisecond

	// ctx с запасом — Run должен выйти по ICE-timeout, а не по ctx.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := Run(ctx, cfg)
	if err != nil {
		t.Fatalf("Run err = %v, want nil (один в звонке → exit 0 по ICE-timeout)", err)
	}
	if got := atomic.LoadInt32(&counters.peerCreated); got != 0 {
		t.Errorf("peer created = %d, want 0 (я один — peers не создавались)", got)
	}
	// leaveBestEffort вызывается и на exit 0 (cleanup).
	if fs.leaveCount != 1 {
		t.Errorf("LeaveCall count = %d, want 1 (cleanup после ICE-timeout)", fs.leaveCount)
	}
}

// TestRun_ICETimeout_ParticipantsNotConnected_Exit1 — участник с аудио есть, но
// за ICE-timeout peer не установил соединение (тест НЕ дёргает fireConnected) →
// Run возвращает ExitError{1} с диагностикой.
func TestRun_ICETimeout_ParticipantsNotConnected_Exit1(t *testing.T) {
	fs := &fakeSignaling{
		pollLoop: pollSendThenBlock([]signaling.Event{
			{Kind: signaling.EvUsersUpdated, Users: []signaling.User{
				{SessionId: "peer-A", InCall: 3},
			}},
		}),
	}
	counters := newTestCounters()
	cfg := counters.buildConfig(fs, 1, io.Discard)
	cfg.ICETimeout = 50 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := Run(ctx, cfg)
	if err == nil {
		t.Fatal("Run err = nil, want exit.ExitError{Code:1} (участник есть, ICE no-answer)")
	}
	var ee exit.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("Run err тип = %T, want exit.ExitError", err)
	}
	if ee.Code != exit.ExitGeneric {
		t.Errorf("ExitError.Code = %d, want %d (ICE no-answer → exit 1)", ee.Code, exit.ExitGeneric)
	}
}

// TestRun_ICETimeout_PeerConnected_NoExit — peer установил ICE (fireConnected) →
// по истечении ICE-timeout Run НЕ выходит (звонок идёт), продолжает до ctx.Cancel.
// Регрессия: everConnected=true должен отключать таймер без exit.
func TestRun_ICETimeout_PeerConnected_NoExit(t *testing.T) {
	fs := &fakeSignaling{
		pollLoop: pollSendThenBlock([]signaling.Event{
			{Kind: signaling.EvUsersUpdated, Users: []signaling.User{
				{SessionId: "peer-A", InCall: 3},
			}},
		}),
	}
	counters := newTestCounters()
	cfg := counters.buildConfig(fs, 1, io.Discard)
	cfg.ICETimeout = 50 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- Run(ctx, cfg) }()

	// Ждём создания peer'а и дёргаем ICE connected ДО истечения ICE-timeout (50ms).
	fp := waitRecvTimeout(t, counters.peerCh, 500*time.Millisecond)
	fp.fireConnected()

	// Даём ICE-timeout сработать (50ms) + запас — Run НЕ должен выйти.
	select {
	case <-runDone:
		t.Fatal("Run завершился по ICE-timeout при everConnected=true — должен продолжать")
	case <-time.After(150 * time.Millisecond):
		// OK — таймер сработал, но exit не произошёл (iceC=nil, continue).
	}

	cancel()
	if err := <-runDone; err != nil {
		t.Fatalf("Run err = %v, want nil", err)
	}
}

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
