// agent.go — реализация pipe-режима (Task 2.8 спеки 2026-07-19).
//
// Agent связывает signaling/peer/media в единый pipe-режим: PCM s16le из stdin
// → единственный FFmpegEncoder → единственный encodeLoop, пишущий в общий
// TrackLocalStaticSample; track добавляется в каждую PC через
// AttachOutgoingTrack (pion внутри каждой PC строит свой RTPSender — fanout
// на стороне pion, review замечание 1). Входящий Opus от каждого пира →
// собственный FFmpegDecoder (decoder-per-peer) → pcmMixerWriter → media.Mixer
// → сводный PCM s16le в stdout.
//
// Диаграмма потоков (спека §6/§9):
//
//	stdin ──PCM──▶ FFmpegEncoder ──Opus──▶ encodeLoop ──▶ audioTrack ──▶ peer*N ──▶ signaling
//	                                                   (один encoder + один encodeLoop + один track на звонок)
//
//	signaling ──▶ peer*N ──Opus──▶ FFmpegDecoder*N ──PCM──▶ pcmMixerWriter*N
//		                                                  └──▶ Mixer ──PCM──▶ stdout
//		                                                  (один mixer на звонок)
//
// Главный инвариант: ОДИН encoder (если sendrecv) + ОДИН encodeLoop + ОДИН
// audioTrack на звонок; per-peer decoder; shared Mixer с фиксированным числом
// слотов (maxPeers). Single-peer failure (спека §10): при Failed() одного
// пира — Close + decoder.Close + mixer.Push(idx,nil), продолжаем работу с
// оставшимися. Выход только по ctx.Done, EvError или завершению encodeLoop
// (EOF на stdin / crash ffmpeg).

package agent

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
	pionmedia "github.com/pion/webrtc/v4/pkg/media"
	"github.com/stas/nctalk/internal/call/media"
	"github.com/stas/nctalk/internal/call/peer"
	"github.com/stas/nctalk/internal/call/signaling"
	"github.com/stas/nctalk/internal/exit"
)

// ---- DI: маленькие интерфейсы в package agent ----
//
// Agent должен быть тестируем БЕЗ запуска pion/ffmpeg/сети (compile-only mode,
// macOS firewall блокирует test-binary). Поэтому абстракции живут здесь, а
// production-типы (*peer.Peer, *signaling.Client) удовлетворяют им неявно — что
// проверяется compile-time guards ниже.

// peerConn — подмножество *peer.Peer, нужное agent'у. Production: *peer.Peer
// удовлетворяет неявно. Тесты: мок-имплементация (fakePeer).
type peerConn interface {
	HandleEvent(signaling.Event) error
	Outgoing() <-chan signaling.Message
	Done() <-chan struct{}
	Failed() <-chan error
	// AttachOutgoingTrack подключает общий audio-track к этой PC. Track
	// создаётся в agent'е (один на звонок) — fanout делается через pion,
	// не через N encodeLoop'ов (review замечание 1).
	AttachOutgoingTrack(*webrtc.TrackLocalStaticSample) error
	OnIncomingAudio(media.AudioSink)
	// CreateOffer инициирует SDP-exchange (impolite-роль). Без него nctalk-call
	// только ждёт offer от браузера, а браузер шлёт offer по своей perfect-
	// negotiation логике и может не прислать (glare, Task 2.6 pending).
	CreateOffer() error
	Close() error
}

// sigClient — подмножество *signaling.Client. SetSessionId сюда НЕ входит —
// sessionId устанавливается вне agent (cmd/nctalk-call), через отдельный метод
// signaling.Client; agent его не использует.
type sigClient interface {
	JoinCall(ctx context.Context, token string, flags int) error
	LeaveCall(ctx context.Context, token string) error
	PollLoop(ctx context.Context, token string, ch chan<- signaling.Event) error
	Send(ctx context.Context, token string, msg signaling.Message) error
}

// peerFactory — конструктор пира. Production: оборачивает peer.New. Тесты:
// мок, возвращающий fakePeer.
type peerFactory func(cfg peer.Config) (peerConn, error)

// encoderFactory / decoderFactory — для подмены ffmpeg-subprocess в тестах.
type encoderFactory func(stdin io.Reader) (media.AudioSource, error)
type decoderFactory func(pcmW io.Writer) (media.AudioSink, error)

// Compile-time гарантии: *peer.Peer и *signaling.Client реализуют agent'ские
// интерфейсы. Если в peer/signaling переименуют/изменят метод — agent не
// соберётся (как с var _ TalkClient в internal/cli/cli.go).
var (
	_ peerConn  = (*peer.Peer)(nil)
	_ sigClient = (*signaling.Client)(nil)
)

// ---- Константы pipe-режима (спека §6/§8) ----

const (
	// maxPeers — верхняя граница числа одновременных remote-пиров. Спека §6:
	// P2P-mesh ≤7 участников + запас. Если число wanted-пиров превышает —
	// лишние Close'аются с логом (не валить весь звонок).
	maxPeers = 8
	// mixerFrameSamples — размер одного PCM-кадра в int16-отсчётах. 20мс при
	// 48к/моно = 960 сэмплов = 1920 байт s16le. Совпадает с libopus voip default
	// frame size и media.PCMFrameDuration.
	mixerFrameSamples = 960
	// mixerFrameBytes — то же в байтах (s16le: 2 байта на отсчёт).
	mixerFrameBytes = mixerFrameSamples * 2
	// mixerTickPeriod — частота дренирования Mixer → Stdout. 20мс = 50Гц = Opus
	// frame rate (спека §6/§9).
	mixerTickPeriod = 20 * time.Millisecond
	// leaveCallTimeout — бюджет на LeaveCall при shutdown. ctx уже отменен, но
	// сервер должен получить DELETE — иначе we hang on the call list серверно.
	leaveCallTimeout = 3 * time.Second
	// inFlagSendRecv — битмаск InCall: 1=IN_CALL, 3=IN_CALL|WITH_AUDIO (спека §6).
	// При InFlags==3 запускаем encoder + encodeLoop + создаём общий audioTrack
	// (один на звонок), подключаемый в каждую PC через AttachOutgoingTrack.
	inFlagSendRecv = 3
	// inFlagWithAudio — бит WITH_AUDIO в InCall-флаге юзера (для фильтрации
	// users: подключаемся только к тем, у кого есть аудио).
	inFlagWithAudio = 2
)

// Config для Run. Все поля читаются только в момент вызова Run (immutable в
// течение звонка) — мьютексов не требует.
type Config struct {
	Signaling    sigClient        // уже готовый (вне agent: сборка из transport.Auth)
	Token        string           // room token (уже ResolveRoom'ом)
	InFlags      int              // 1=recvonly, 3=sendrecv (спека §6)
	OwnSessionId string           // собственный sessionId — статический фильтр (если известен заранее)
	OwnUserId    string           // NEXTCLOUD_LOGIN: для извлечения ownSessionId из usersInRoom (review замечание 3)
	ICEServers   []webrtc.ICEServer // из capability (Task 2.10)
	Stdin        io.Reader        // PCM на отправку (nil если InFlags==1)
	Stdout       io.Writer        // PCM приём: Mixer → stdout
	Stderr       io.Writer        // статус/диагностика (НЕ содержит кредов)

	// Injection points для тестов. nil → production-обёртки над peer.New /
	// media.NewFFmpeg*. Тесты кладут свои мок-фабрики.
	NewPeer     peerFactory
	NewEncoder  encoderFactory
	NewDecoder  decoderFactory
	// MixerTick — канал тиков дренирования Mixer'а. nil → внутренний
	// time.NewTicker(20ms) (стоп в defer Run). Тесты передают управляемый
	// канал, чтобы детерминированно прогонять кадры через Mixer.
	MixerTick <-chan time.Time
}

// ---- Run: главный loop agent'а (pipe-режим) ----

// Run связывает signaling/peer/media в pipe-режиме. Шаги:
//
//  1. Signaling.JoinCall(Token, InFlags) → при ошибке сразу return
//     (маппинг в *exit.ExitError через exit.FromClientErr — код 1 или 2).
//  2. Если InFlags==3 (sendrecv) — запускает ОДИН encoder (encoderFactory).
//  3. Запускает Mixer + mixer-drain горутину: каждый тик Mix() → s16le в Stdout.
//  4. Запускает signaling.PollLoop в горутине → events-канал.
//  5. Главный select: events / peer.Failed / peer.Done / ctx.Done.
//  6. На EvUsersUpdated — reconcile peers (новые/ушедшие), фильтр own-sessionId.
//  7. На EvOffer/EvAnswer/EvCandidate — route к peer'у по ev.From.
//  8. На peer.Failed() — Close + decoder.Close + mixer.Push(idx,nil) + лог.
//     ПРОДОЛЖАЕМ с оставшимися (single-peer failure, спека §10).
//  9. ctx.Done → LeaveCall (best-effort 3s) + close everything, return nil.
//
// Возвращает:
//   - nil — штатный ctx.Done;
//   - *exit.ExitError — если signaling.PollLoop вышел с EvError (401→1, 404→2)
//     ИЛИ JoinCall упал (через exit.FromClientErr).
//   - прочие ошибки — наверх (cmd даст exit 1).
func Run(ctx context.Context, cfg Config) error {
	// Defaults для тестов и прод-безопасности: nil-writers → io.Discard,
	// фабрики → production-обёртки.
	if cfg.Stdout == nil {
		cfg.Stdout = io.Discard
	}
	if cfg.Stderr == nil {
		cfg.Stderr = io.Discard
	}
	if cfg.NewPeer == nil {
		cfg.NewPeer = func(c peer.Config) (peerConn, error) {
			p, err := peer.New(c)
			if err != nil {
				return nil, err
			}
			return p, nil
		}
	}
	if cfg.NewEncoder == nil {
		cfg.NewEncoder = func(r io.Reader) (media.AudioSource, error) {
			return media.NewFFmpegEncoder(r)
		}
	}
	if cfg.NewDecoder == nil {
		cfg.NewDecoder = func(w io.Writer) (media.AudioSink, error) {
			return media.NewFFmpegDecoder(w)
		}
	}

	// Step 1: JoinCall. При ошибке — маппинг через exit.FromClientErr
	// (*transport.OCSError{Code:404} → ExitError{Code:2}, остальное → Code:1).
	if err := cfg.Signaling.JoinCall(ctx, cfg.Token, cfg.InFlags); err != nil {
		return exit.FromClientErr(err)
	}

	// Step 2: encoder + общий audioTrack (если sendrecv). encodeLoop ОДИН на
	// звонок — пишёт в общий track; pion внутри каждой PC строит свой RTPSender
	// и сам делает fanout (review замечание 1). Раньше encodeLoop запускался
	// per-peer и читал общий AudioSource.ReadSample — N горутин делили пакеты
	// round-robin, каждый peer получал 1/N (звонок для mesh был неработоспособен).
	var encoder media.AudioSource
	var audioTrack *webrtc.TrackLocalStaticSample
	var encodeDone chan error
	if cfg.InFlags == inFlagSendRecv {
		enc, err := cfg.NewEncoder(cfg.Stdin)
		if err != nil {
			leaveBestEffort(cfg.Signaling, cfg.Token)
			return fmt.Errorf("agent: encoder: %w", err)
		}
		encoder = enc
		track, err := webrtc.NewTrackLocalStaticSample(
			webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus},
			"audio", "nctalk",
		)
		if err != nil {
			_ = encoder.Close()
			leaveBestEffort(cfg.Signaling, cfg.Token)
			return fmt.Errorf("agent: audio track: %w", err)
		}
		audioTrack = track
		encodeDone = make(chan error, 1)
		go agentEncodeLoop(encoder, audioTrack, encodeDone)
	}

	// Step 3: Mixer + drain-горутина. Тикер инъектируется (тесты) или создаётся
	// внутренний (прод). drain крутится до drainStop.
	mixer := media.NewMixer(maxPeers)
	mixerTick := cfg.MixerTick
	var ticker *time.Ticker
	if mixerTick == nil {
		ticker = time.NewTicker(mixerTickPeriod)
		mixerTick = ticker.C
	}
	drainStop := make(chan struct{})
	drainDone := make(chan struct{})
	go mixerDrain(mixer, mixerTick, drainStop, cfg.Stdout, drainDone)

	// Step 4: PollLoop в горутине. events-канал буферизован — signaling отправляет
	// без блокировки (но уважает ctx.Done). После выхода PollLoop — close(events)
	// даёт main-луку сигнал «источник пуст».
	events := make(chan signaling.Event, 16)
	pollDone := make(chan error, 1)
	go func() {
		err := cfg.Signaling.PollLoop(ctx, cfg.Token, events)
		// ПОРЯДОК ВАЖЕН (review замечание 6): pollDone<-err ДО close(events).
		// Main-loop при `ok=false` из events делает select { case pollErr =
		// <-pollDone: default: }. close(events) выступает memory-barrier'ом:
		// после того как main увидел close, pollDone-send уже гарантированно
		// виден — default-ветка не сработает, ошибка не теряется. Обратный
		// порядок (close первой) давал race: main видел close, но select-default
		// уходил в пустую ветку до того, как pollDone-send публикуется в буфер.
		pollDone <- err
		close(events)
	}()

	// agentState — разделяемое состояние main loop'а и per-peer горутин.
	a := &agentState{
		cfg:           cfg,
		ctx:           ctx,
		mixer:         mixer,
		encoder:       encoder,
		audioTrack:    audioTrack,
		peers:         make(map[string]*peerBundle),
		ownSessionId:  cfg.OwnSessionId, // может быть пустым — извлечём из EvUsersUpdated
	}

	// Step 5-8: main select.
	var pollErr error
mainLoop:
	for {
		select {
		case <-ctx.Done():
			break mainLoop
		case ev, ok := <-events:
			if !ok {
				// PollLoop вышел. Если через EvError — pollErr уже установлен в case ниже.
				// Если через ctx.Done — pollErr остаётся nil.
				// Проверяем pollDone, чтобы подхватить ошибку, не дошедшую через EvError
				// (контракт signaling: EvError шлётся ДО return, но при race с ctx.Cancel
				// может не дойти).
				select {
				case pollErr = <-pollDone:
				default:
				}
				break mainLoop
			}
			switch ev.Kind {
			case signaling.EvUsersUpdated:
				fmt.Fprintf(cfg.Stderr, "DEBUG ev=UsersUpdated users=%d\n", len(ev.Users))
				a.reconcile(ev.Users)
			case signaling.EvOffer, signaling.EvAnswer, signaling.EvCandidate:
				fmt.Fprintf(cfg.Stderr, "DEBUG ev=%d from=%.12s\n", ev.Kind, ev.From)
				if b, ok := a.peerBySid(ev.From); ok {
					_ = b.peer.HandleEvent(ev)
				} else {
					fmt.Fprintf(cfg.Stderr, "DEBUG   peer not in map for from=%.12s\n", ev.From)
				}
				// Если пира нет в map — событие пришло раньше, чем EvUsersUpdated для
				// этого sessionId. Скипаем (баг сервера/сети; recoverable следующим
				// EvUsersUpdated + повторным offer'ом от пира).
			case signaling.EvError:
				pollErr = ev.Err
				break mainLoop
			}
		case err, ok := <-encodeDoneCh(encodeDone):
			// encodeLoop вышел. nil — stdin EOF / shutdown; non-nil — ffmpeg-crash
			// (review замечание 4, спека §10). В любом случае audio-pipe кончился —
			// выходим из main loop; leaveCall/peer-cleanup — в finalization.
			if ok && err != nil {
				pollErr = err
			}
			break mainLoop
		}
	}

	// Step 9: финализация. Порядок важен:
	//   1. drainStop + ждём drain — больше никто не пишет в Stdout.
	//   2. close peers/decoders/encoder — корректный shutdown pion'а и ffmpeg'ов.
	//   3. LeaveCall best-effort — сервер убирает нас из usersInRoom.
	//   4. Останавливаем внутренний тикер (если был).
	close(drainStop)
	<-drainDone
	if ticker != nil {
		ticker.Stop()
	}
	a.closeAllPeers()
	if encoder != nil {
		_ = encoder.Close()
	}
	leaveBestEffort(cfg.Signaling, cfg.Token)

	return pollErr
}

// encodeDoneCh возвращает канал encodeDone или nil-канал (если encodeLoop не
// запускался — recvonly). nil-канал в select'е никогда не срабатывает —
// идиома Go для «не выбирать эту ветку».
func encodeDoneCh(ch chan error) <-chan error {
	if ch == nil {
		return nil
	}
	return ch
}

// agentEncodeLoop — ОДИН encodeLoop на звонок (review замечание 1). Читает
// Opus-сэмплы из src (encoder), пишёт в общий audioTrack, который расшарен
// между всеми PeerConnection'ами через AttachOutgoingTrack. pion внутри каждой
// PC строит отдельный RTPSender и пакует samples в RTP независимо — fanout
// делается на стороне pion, без нашего вмешательства.
//
// Завершение:
//   - io.EOF / ctx.Canceled / ctx.DeadlineExceeded — штатный конец (stdin EOF,
//     encoder.Close). Сигналим done<-nil.
//   - прочая ошибка src.ReadSample (ffmpeg crash, review замечание 4) —
//     done<-err, агент выйдет с этим кодом.
//   - track.WriteSample ошибка (все PC закрылись) — done<-nil (не фатал).
func agentEncodeLoop(src media.AudioSource, track *webrtc.TrackLocalStaticSample, done chan<- error) {
	var count int
	for {
		payload, dur, err := src.ReadSample()
		if err != nil {
			if errors.Is(err, io.EOF) ||
				errors.Is(err, context.Canceled) ||
				errors.Is(err, context.DeadlineExceeded) {
				done <- nil
				return
			}
			done <- fmt.Errorf("agent: encodeLoop: %w", err)
			return
		}
		if err := track.WriteSample(pionmedia.Sample{Data: payload, Duration: dur}); err != nil {
			// Все RTPSender'ы отвалились — кодировать некому. Не фатал для звонка
			// (могут ещё приходить новые пиры), но и кодить впустую нет смысла.
			done <- nil
			return
		}
		count++
		if count == 1 || count%50 == 0 {
			log.Printf("DEBUG encodeLoop: wrote %d samples (dur=%v)", count, dur)
		}
		// Real-time pacing. pion WriteSample отправляет RTP немедленно (без
		// pacing), а ffmpeg encode читает вход быстрее real-time — без sleep весь
		// PCM-файл кодируется за <1с, encodeLoop тут же EOF, agent выходит ДО
		// установления ICE (spike-gate 2026-07-20: sendrecv recording.pcm пуст,
		// exit 0 за <1с). Sleep на dur (типично 20мс Opus-frame) даёт real-time
		// стриминг — sendrecv держится всё время --in, ICE успевает, audio идёт.
		time.Sleep(dur)
	}
}

// ---- agentState: разделяемое состояние ----

// agentState инкапсулирует peers-map и счетчик idx. Защищён мьютексом для
// конкурентного доступа из main loop (reconcile) и per-peer горутин (watcher
// → removePeer).
type agentState struct {
	cfg     Config
	ctx     context.Context // основной ctx Run — пробрасывается в signaling.Send
	mixer   *media.Mixer
	encoder media.AudioSource

	// audioTrack — общий TrackLocalStaticSample для отправки своего аудио во
	// все PC (review замечание 1). nil в recvonly-режиме или до создания.
	audioTrack *webrtc.TrackLocalStaticSample

	mu      sync.Mutex
	peers   map[string]*peerBundle
	nextIdx int // монотонная верхняя граница занятых слотов Mixer'а
	// freeIdx — стек освободившихся idx (review замечание 9: без переиспользования
	// долгий звонок с churn'ом участников заблокирует новых после 8 суммарных
	// пиров). addPeer сначала берёт из freeIdx, потом — из nextIdx.
	freeIdx []int

	// ownSessionIdMu защищает ownSessionId (извлекается лениво из первого
	// EvUsersUpdated по совпадению UserId == cfg.OwnUserId, review замечание 3).
	// Если cfg.OwnSessionId задан статически — используется без поиска.
	// ownUserIdFound защищает от повторного лога «не нашли себя» на каждом EvUsersUpdated.
	ownSessionIdMu   sync.Mutex
	ownSessionId     string
	ownUserIdChecked bool
}

// peerBundle — связка «peer + decoder + idx в Mixer'е» для одного remote-пира.
// Жизненный цикл: создание в addPeer → удаление в removePeer (Failed / left-room /
// shutdown). Cleanup идемпотентен (peer.Close — sync.Once, decoder.Close —
// closeOnce, mixer.Push(idx,nil) — безвреден при повторе).
type peerBundle struct {
	sid     string
	idx     int
	peer    peerConn
	decoder media.AudioSink
}

// reconcile синхронизирует набор активных пиров с wanted-снапшотом из
// EvUsersUpdated. Алгоритм (спека §6):
//
//   - wanted = sessionId тех, у кого InCall&WITH_AUDIO и sessionId != "" и !=
//     OwnSessionId;
//   - удаляем тех, кто был, но исчез из wanted (peer.Close + decoder.Close +
//     mixer.Push(idx,nil) + удаление из map);
//   - добавляем тех, кто есть в wanted, но ещё нет в map (createPeerBundle).
//
// НЕ переиспользуем idx ушедших пиров — Mixer игнорирует inactive слоты, а
// переиспользование усложнило бы семантику (гонки с push от только что закрытого
// decoder'а). Cap N = maxPeers — лишних просто не добавляем (с логом).
//
// OwnSessionId (review замечание 3): если cfg.OwnSessionId пуст, а cfg.OwnUserId
// задан — пытаемся извлечь ownSessionId из users по совпадению UserId. Spreed
// signalling.js использует тот же механизм (session identification по userId).
func (a *agentState) reconcile(users []signaling.User) {
	// Ленивое извлечение ownSessionId (если ещё не известно).
	a.resolveOwnSessionId(users)

	ownSid := a.ownSessionIdLocked()

	// Фильтр пользователей: только sendrecv-участники, не мы, с непустым sid.
	wanted := make(map[string]struct{}, len(users))
	for _, u := range users {
		if u.SessionId == "" {
			continue
		}
		if u.SessionId == ownSid {
			continue
		}
		// DEBUG (spike-gate 2026-07-20): фильтр WITH_AUDIO ВРЕМЕННО отключён,
		// чтобы sendrecv-отправитель (A) подключался к recvonly-слушателю (B)
		// для чистого A→B теста без glare (оба sendrecv дают glare без PN,
		// Task 2.6). ВЕРНУТЬ фильтр после теста.
		// if u.InCall&inFlagWithAudio == 0 {
		// 	continue
		// }
		wanted[u.SessionId] = struct{}{}
	}

	// Снимаем список ушедших/новых под локом; cleanup/add — снаружи (мьютекс
	// только для map).
	a.mu.Lock()
	var toRemove []*peerBundle
	for sid, b := range a.peers {
		if _, ok := wanted[sid]; !ok {
			toRemove = append(toRemove, b)
			delete(a.peers, sid) // резервируем удаление сразу — watcher.removePeer станет no-op
		}
	}
	toAdd := make([]string, 0, len(wanted))
	for sid := range wanted {
		if _, exists := a.peers[sid]; !exists {
			toAdd = append(toAdd, sid)
		}
	}
	a.mu.Unlock()

	fmt.Fprintf(a.cfg.Stderr, "DEBUG reconcile: wanted=%d toAdd=%d toRemove=%d ownSid=%.12s\n",
		len(wanted), len(toAdd), len(toRemove), ownSid)

	// Cleanup ушедших (вне лока — Close/decoder.Close могут блокировать).
	for _, b := range toRemove {
		_ = b.peer.Close()
		_ = b.decoder.Close()
		a.mixer.Push(b.idx, nil)
		a.releaseIdx(b.idx)
		fmt.Fprintf(a.cfg.Stderr, "nctalk: peer %s покинул звонок\n", b.sid)
	}

	// Add новых.
	for _, sid := range toAdd {
		if err := a.addPeer(sid); err != nil {
			fmt.Fprintf(a.cfg.Stderr, "nctalk: не удалось создать peer %s: %v\n", sid, err)
		}
	}
}

// addPeer создаёт связку (decoder + peer + mixer-adapter), регистрирует в map
// и запускает per-peer горутины (watcher + send). Вызывается из reconcile
// (main loop goroutine).
//
// Порядок (спека §7):
//  1. Выделить idx (проверка maxPeers).
//  2. Создать decoder с pcmW = &pcmMixerWriter{idx, mixer}.
//  3. Создать peer через NewPeer(peer.Config{ICEServers, IsPolite:false}).
//     IsPolite=false: glare-handling — Task 2.6, пока не реализован.
//  4. OnIncomingAudio(decoder) — ДО HandleEvent (pion OnTrack асинхронен).
//  5. AttachOutgoingTrack(a.audioTrack) если sendrecv — ДО SDP (m-line в SDP).
//  6. Зарегистрировать bundle в map (ДО запуска горутин — watcher должен видеть
//     sid в map, иначе removePeer станет no-op при мгновенном Failed).
//  7. Запустить watcher и send-горутину.
//
// CreateOffer НЕ вызываем — инициация SDP-exchange останется на Task 2.6
// (perfect-negotiation: один из пиров impolite, другой polite).
func (a *agentState) addPeer(sid string) error {
	a.mu.Lock()
	// Slot allocation (review замечание 9): сначала переиспользуем освободившиеся
	// idx из freeIdx, потом — следующие номера. Лимит maxPeers считается по
	// текущему числу занятых слотов (len(peers)), а не по суммарно выданных —
	// долгий звонок с churn'ом участников не заблокирует новых.
	if len(a.peers) >= maxPeers {
		a.mu.Unlock()
		return fmt.Errorf("достигнут лимит пиров (%d)", maxPeers)
	}
	var idx int
	if n := len(a.freeIdx); n > 0 {
		idx = a.freeIdx[n-1]
		a.freeIdx = a.freeIdx[:n-1]
	} else {
		idx = a.nextIdx
		a.nextIdx++
	}
	a.mu.Unlock()

	// 1. Decoder с writer-адаптером в Mixer[idx].
	pcmW := &pcmMixerWriter{
		idx:          idx,
		mixer:        a.mixer,
		frameSamples: mixerFrameSamples,
	}
	decoder, err := a.cfg.NewDecoder(pcmW)
	if err != nil {
		return fmt.Errorf("decoder: %w", err)
	}

	// 2. Peer (pion PeerConnection).
	p, err := a.cfg.NewPeer(peer.Config{
		ICEServers: a.cfg.ICEServers,
		IsPolite:   false, // Task 2.6 — glare пока не реализован
	})
	if err != nil {
		_ = decoder.Close()
		return fmt.Errorf("peer: %w", err)
	}

	// 3. Wire audio. OnIncomingAudio — до любого HandleEvent/OnTrack.
	p.OnIncomingAudio(decoder)
	if a.audioTrack != nil {
		if err := p.AttachOutgoingTrack(a.audioTrack); err != nil {
			_ = p.Close()
			_ = decoder.Close()
			return fmt.Errorf("attach outgoing: %w", err)
		}
		// sendrecv: nctalk-call сам инициирует offer (минимальный glare-обход,
		// Task 2.6 — полноценный perfect-negotiation pending). Браузер шлёт
		// offer по своей perfect-negotiation логике (по сравнению sessionId) и
		// может не прислать → nctalk-call ждёт зря → PC state только closed,
		// audio-pipe не встаёт (spike-gate 2026-07-20: sendrecv parseEnvelopes
		// только usersInRoom, без offer). В recvonly (audioTrack==nil) НЕ
		// offerим — там браузер сам offer'ит по PN-логике.
		if err := p.CreateOffer(); err != nil {
			_ = p.Close()
			_ = decoder.Close()
			return fmt.Errorf("create offer: %w", err)
		}
	}

	b := &peerBundle{
		sid:     sid,
		idx:     idx,
		peer:    p,
		decoder: decoder,
	}

	// 4. Регистрация в map ДО запуска горутин (гарантия watcher.RemovePeer найдёт sid).
	a.mu.Lock()
	a.peers[sid] = b
	a.mu.Unlock()

	// 5. Per-peer горутины.
	go a.watchPeer(b)
	go a.sendPeer(b)

	return nil
}

// removePeer — идемпотентный cleanup связки по sid. Безопасно вызывать из любой
// горутины (watcher). Если sid уже удалён (reconcile опередил) — no-op.
func (a *agentState) removePeer(sid string, reason string) {
	a.mu.Lock()
	b, ok := a.peers[sid]
	if !ok {
		a.mu.Unlock()
		return
	}
	delete(a.peers, sid)
	a.mu.Unlock()

	_ = b.peer.Close()
	_ = b.decoder.Close()
	a.mixer.Push(b.idx, nil)
	a.releaseIdx(b.idx)
	fmt.Fprintf(a.cfg.Stderr, "nctalk: peer %s отключён (%s)\n", sid, reason)
}

// releaseIdx возвращает idx в пул свободных слотов (под локом agentState.mu).
// Используется в removePeer / reconcile-cleanup для переиспользования
// (review замечание 9). closeAllPeers не возвращает — shutdown идёт, индексы
// больше не нужны.
func (a *agentState) releaseIdx(idx int) {
	a.mu.Lock()
	a.freeIdx = append(a.freeIdx, idx)
	a.mu.Unlock()
}

// peerBySid — снапшот-чтение из map под локом. Возвращает bundle и флаг наличия.
func (a *agentState) peerBySid(sid string) (*peerBundle, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	b, ok := a.peers[sid]
	return b, ok
}

// resolveOwnSessionId извлекает ownSessionId из users по совпадению
// u.UserId == cfg.OwnUserId (review замечание 3). Если ownSessionId уже
// установлен статически (cfg.OwnSessionId) или найден ранее — no-op.
// Логирует один раз, если ownUserId задан, но не найден в users — это
// диагностика, что агент потенциально попытается звонить самому себе.
func (a *agentState) resolveOwnSessionId(users []signaling.User) {
	a.ownSessionIdMu.Lock()
	defer a.ownSessionIdMu.Unlock()
	if a.ownSessionId != "" || a.cfg.OwnUserId == "" || a.ownUserIdChecked {
		return
	}
	for _, u := range users {
		if u.UserId == a.cfg.OwnUserId && u.SessionId != "" {
			a.ownSessionId = u.SessionId
			break
		}
	}
	a.ownUserIdChecked = true
	if a.ownSessionId == "" {
		fmt.Fprintf(a.cfg.Stderr,
			"nctalk: WARNING: ownSessionId не извлечён из usersInRoom по userId=%q — "+
				"агент может попытаться позвонить самому себе. Сообщите серверу/вендору, "+
				"если reproducible.\n", a.cfg.OwnUserId)
	} else {
		fmt.Fprintf(a.cfg.Stderr, "nctalk: ownSessionId извлечён из usersInRoom (%s)\n", a.cfg.OwnUserId)
	}
}

// ownSessionIdLocked возвращает текущий ownSessionId (потокобезопасно).
func (a *agentState) ownSessionIdLocked() string {
	a.ownSessionIdMu.Lock()
	defer a.ownSessionIdMu.Unlock()
	return a.ownSessionId
}

// closeAllPeers — shutdown-версия removePeer для всех: под локом собираем
// список, чистим map, вне лока закрываем. Без reason в логе (штатный выход).
func (a *agentState) closeAllPeers() {
	a.mu.Lock()
	all := make([]*peerBundle, 0, len(a.peers))
	for sid, b := range a.peers {
		all = append(all, b)
		delete(a.peers, sid)
	}
	a.mu.Unlock()

	for _, b := range all {
		_ = b.peer.Close()
		_ = b.decoder.Close()
		a.mixer.Push(b.idx, nil)
	}
}

// ---- per-peer горутины ----

// watchPeer слушает peer.Failed() и peer.Done(), вызывает removePeer при
// срабатывании. Выход также по ctx (shutdown) — но shutdown идёт через основной
// ctx.Run, который отменяется ВНЕ agent.Run; мы ориентируемся на peer.Done()
// (вызванный closeAllPeers → peer.Close). При Failed() — лог + cleanup + ПРОДОЛЖАЕМ
// работу с оставшимися (спека §10).
func (a *agentState) watchPeer(b *peerBundle) {
	select {
	case err := <-b.peer.Failed():
		// ICE/DTLS failure — фатал для этого пира, НЕ для всего звонка.
		reason := "disconnected"
		if err != nil {
			reason = err.Error()
		}
		a.removePeer(b.sid, reason)
	case <-b.peer.Done():
		// Штатное закрытие: либо наш Close (reconcile/shutdown), либо удалённый
		// side graceful disconnect. reconcile/shutdown уже удалили bundle (или
		// удалят); removePeer идемпотентен — безопасно вызвать и здесь.
		a.removePeer(b.sid, "closed")
	}
}

// sendPeer копирует peer.Outgoing() → signaling.Send. Выходит по peer.Done()
// (peer.Close закрывает closed-канал; Outgoing НЕ закрывается — см. peer.go).
// signaling.Send ошибки не логируем: сетевой сбой → signaling.PollLoop всё равно
// фаталит с EvError, дублировать нет смысла.
//
// ctx: используется a.ctx (основной ctx Run) — это гарантирует, что на shutdown
// отменится и in-flight Send, а не только ожидание следующего сообщения.
//
// review замечание 11: peer не знает sessionId удалённой стороны и не может
// проставить Message.To — но agent знает (peerBundle.sid). Ставим To перед
// Send, иначе сервер Spreed бродкастит сообщение всем участникам, и в mesh
// ответ A→B попадает и к C (лишний трафик, spam-ошибки в pion-логах C).
func (a *agentState) sendPeer(b *peerBundle) {
	for {
		select {
		case msg, ok := <-b.peer.Outgoing():
			if !ok {
				return // канал закрыт (в тек. реализации peer не закрывает outCh — defensive)
			}
			if msg.To == "" {
				msg.To = b.sid
			}
			_ = a.cfg.Signaling.Send(a.ctx, a.cfg.Token, msg)
		case <-b.peer.Done():
			return
		}
	}
}

// ---- pcmMixerWriter: адаптер io.Writer → media.Mixer.Push ----

// pcmMixerWriter аккумулирует s16le-байты до целого int16-кадра (mixerFrameSamples
// отсчётов = mixerFrameBytes байт) и вызывает mixer.Push(idx, samples).
// Передаётся в decoderFactory как pcmW; FFmpegDecoder пишет PCM напрямую в
// cmd.Stdout = pcmW (один писатель — ОС-pipe, гонок нет).
//
// НЕ экспортируется — деталь реализации agent'а.
//
// Частичный буфер (меньше frameBytes) хранится до следующего Write; при Close
// пира decoder.Close убивает ffmpeg — недописанный кадр теряется (это норма:
// 20мс звука на shutdown — не слышно).
type pcmMixerWriter struct {
	idx          int
	mixer        *media.Mixer
	buf          []byte
	frameSamples int
}

// Write реализует io.Writer. Аккумулирует p в buf, при заполнении кадра
// конвертирует s16le → []int16 и делает mixer.Push(idx, samples).
func (w *pcmMixerWriter) Write(p []byte) (int, error) {
	n := len(p)
	frameBytes := w.frameSamples * 2
	for len(p) > 0 {
		need := frameBytes - len(w.buf)
		if need > len(p) {
			// Не хватает до целого кадра — буферизуем, выходим.
			w.buf = append(w.buf, p...)
			break
		}
		w.buf = append(w.buf, p[:need]...)
		p = p[need:]

		// Конверсия s16le → []int16. Little-endian (x86/arm64 native, и ffmpeg
		// пишет s16le явно через -f s16le).
		samples := make([]int16, w.frameSamples)
		for i := 0; i < w.frameSamples; i++ {
			samples[i] = int16(binary.LittleEndian.Uint16(w.buf[i*2 : i*2+2]))
		}
		w.mixer.Push(w.idx, samples)
		// Сбрасываем buf, сохраняя underlying array (ре-use в следующих Write).
		w.buf = w.buf[:0]
	}
	return n, nil
}

// ---- mixerDrain: горутина дренирования Mixer → Stdout ----

// mixerDrain крутится до drainStop. На каждый тик:
//   - mixer.Mix() → nil если активных слотов нет (skip);
//   - конверсия []int16 → []byte (s16le);
//   - Stdout.Write.
//
// Выход по drainStop (shutdown) или закрытию tickCh (закрытый канал в select
// срабатывает немедленно — не отличается от обычного тика по данным, но ok=false).
func mixerDrain(mixer *media.Mixer, tick <-chan time.Time, stop <-chan struct{}, out io.Writer, done chan<- struct{}) {
	defer close(done)
	for {
		select {
		case <-stop:
			return
		case _, ok := <-tick:
			if !ok {
				// Канал закрыт (тест закрыл свой MixerTick) — выходим.
				return
			}
			samples := mixer.Mix()
			if len(samples) == 0 {
				continue
			}
			// []int16 → s16le []byte.
			buf := make([]byte, len(samples)*2)
			for i, s := range samples {
				binary.LittleEndian.PutUint16(buf[i*2:i*2+2], uint16(s))
			}
			_, _ = out.Write(buf)
		}
	}
}

// ---- helpers ----

// leaveBestEffort вызывает LeaveCall с отдельным ctx (3с timeout): основной ctx
// уже отменен, но сервер должен получить DELETE — иначе висим в usersInRoom
// серверно до таймаута сессии (минуты). Ошибки игнорируем: shutdown и так уже
// идёт, fail-closed не нужен.
func leaveBestEffort(sig sigClient, token string) {
	ctx, cancel := context.WithTimeout(context.Background(), leaveCallTimeout)
	defer cancel()
	_ = sig.LeaveCall(ctx, token)
}
