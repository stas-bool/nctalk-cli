// Package peer — обёртка над pion/webrtc/v4: одна PeerConnection на удалённого
// участника, SDP/ICE через signaling, audio-track на отправку (media.AudioSource
// → TrackLocalStaticSample), OnTrack на приём (TrackRemote → media.AudioSink).
// Спека 2026-07-19 §7. Ничего не знает про формат звука — работает через
// интерфейсы media.AudioSource / media.AudioSink (определены в call/media).
//
// DAG-инвариант (спека §4): media ← peer (peer импортирует media), обратной
// дуги НЕТ. peer также потребляет signaling.Event / signaling.Message, но сам
// signaling не зовёт — транспорт исходящих сообщений (signaling.Client.Send)
// лежит на вышестоящем слое (agent, Task 2.8).
//
// Perfect-negotiation (Task 2.6, спека §7): glare = оба пира в have-local-offer.
// impolite игнорирует входящий offer; polite — откатывает свой и принимает
// входящий. Pion v4 НЕ умеет rollback из have-local-offer (state-machine в
// signalingstate.go блокирует have-local-offer→SetLocal(rollback)→stable), а
// спека §7 требует именно rollback для polite — поэтому polite-glare разрешается
// ПЕРЕСОЗДАНИЕМ PeerConnection (recreatePCForGlare): старый pc закрывается,
// новый стартует в stable и принимает входящий offer. Логика решения
// (polite/impolite/ignore) вынесена в negotiation.go.
package peer

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/pion/webrtc/v4"
	"github.com/stas-bool/nctalk-cli/internal/call/media"
	"github.com/stas-bool/nctalk-cli/internal/call/signaling"
)

// Config для New. ICEServers — из capability сервера (Task 2.10), НЕ из env:
// TURN-credentials (Username/Credential/CredentialType) поставляются capability
// вместе с URL-ами серверов. Empty list допустим — pion работает с host
// candidates (тесты на localhost ICE). Спека §5.
type Config struct {
	ICEServers []webrtc.ICEServer
	// IsPolite — роль perfect-negotiation (спека §7). Назначается вышестоящим
	// слоем (agent.addPeer) детерминированно по сравнению sessionId: мы polite,
	// если наш sessionId больше remote. При glare polite откатывает свой offer
	// (через recreatePCForGlare), impolite игнорирует входящий. См. negotiation.go.
	IsPolite bool
}

// Peer — одна PeerConnection на одного удалённого участника. Создаётся через
// New, обменивается SDP/ICE через HandleEvent (входящие) и Outgoing (исходящие),
// подключает аудио через AttachOutgoingTrack / OnIncomingAudio.
//
// Жизненный цикл: New → (AttachOutgoingTrack / OnIncomingAudio) → HandleEvent
// по событиям signaling → Close. После Close Peer непригоден.
//
// Потоки: ОДИН encodeLoop живёт в agent'е (НЕ в peer'е) и пишет в общий
// audio-track, расшаренный между всеми PeerConnection'ами через AttachOutgoingTrack
// (pion сам размножает отправку через RTPSender'ы внутри каждой PC — fanout на
// стороне pion, review замечание 1). decodeLoop (приём Opus в AudioSink) —
// внутренняя горутина Peer'а, завершается по Close() (close(p.closed) + pc.Close).
// Failed() срабатывает на DTLS/ICE-fail — вышележащий слой зовёт Close и
// удаляет Peer из активного набора, ПРОДОЛЖАЯ работу с оставшимися (mesh
// переживёт отказ одного пира — спека §10).
//
// Concurrency: все обращения к p.pc идут из agent-goroutine (HandleEvent /
// CreateOffer / AttachOutgoingTrack / Close — single-threaded), поэтому
// recreatePCForGlare меняет p.pc простым присваиванием без синхронизации.
// Pion-callbacks (OnICECandidate/OnConnectionStateChange/OnICEConnectionStateChange/
// OnTrack) замыкаются на p (методы sendOut/signalFailure/fireOnConnected/
// handleIncomingTrack) и НЕ читают p.pc напрямую — stale-callbacks закрытого
// pc гаснут после Close(), гонок с актуальным pc нет.
type Peer struct {
	pc *webrtc.PeerConnection

	// api/iceServers сохраняются для recreatePCForGlare: новый pc создаётся с
	// тем же audio-only Opus MediaEngine (через api) и тем же списком ICE-серверов.
	api        *webrtc.API
	iceServers []webrtc.ICEServer

	// outTrack — общий TrackLocalStaticSample, добавленный в эту PC через
	// AddTrack в AttachOutgoingTrack. nil до вызова AttachOutgoingTrack.
	// Сам track создаётся ВЫШЕСТОЯЩИМ слоем (agent) — один на звонок, добавляется
	// в каждую PC; pion внутри каждой PC строит отдельный RTPSender и
	// пакует samples в RTP независимо. Это и есть fanout — без N encodeLoop'ов.
	// При recreatePCForGlare track пере-добавляется на новый pc (AddTrack).
	outTrack *webrtc.TrackLocalStaticSample

	// outCh — исходящие signaling-сообщения (offer/answer/candidate).
	// Буфер 64: normal exchange = 1 offer + 1 answer + ~5 candidates = 7
	// сообщений на peer; 64 даёт запас. НЕ закрывается в Close() — потребитель
	// должен использовать Done() для определения конца (см. ниже).
	outCh chan signaling.Message

	// failedCh — non-blocking нотификация о фатальной ошибке peer'а. cap=1:
	// send через select с default не блокирует даже если никто не читает.
	failedCh chan error

	// pendingICE — входящие ICE-candidates, пришедшие ДО SetRemoteDescription.
	// Сервер Spreed не гарантирует порядок: candidate может прийти раньше
	// offer/answer, и pion в таком случае падает на AddICECandidate
	// (ErrNoRemoteDescription). Поэтому буферизуем и дренируем сразу после
	// SetRemoteDescription. Спека §7 «Буферизация ICE-candidates».
	// Сбрасывается в nil при recreatePCForGlare (новый pc стартует чистым).
	pendingICE []webrtc.ICECandidateInit

	// remoteDescr — атомарный флаг: SetRemoteDescription уже выполнен.
	// Используется в HandleEvent(EvCandidate) для решения «буферизовать или
	// AddICECandidate сразу». Сбрасывается в false при recreatePCForGlare.
	remoteDescr atomic.Bool

	// sinkMu защищает sink при установке через OnIncomingAudio и чтении в
	// handleIncomingTrack (pion OnTrack срабатывает асинхронно).
	sinkMu sync.RWMutex
	sink   media.AudioSink // nil до OnIncomingAudio

	// closeOnce — идемпотентный Close. closed — канал-сигнал завершения
	// (закрывается первым в Close), используется в sendOut/decodeLoop для
	// неблокирующего выхода.
	closeOnce sync.Once
	closed    chan struct{}
	closeErr  error

	// onConnected — callback, вызываемый один раз при ICEConnectionStateConnected.
	// Регистрируется agent'ом (через OnConnected) для отправки signaling unmute —
	// без него Spreed держит audio-sink muted (spike-gate root cause 2026-07-20).
	// onConnectedMu защищает onConnected при установке (addPeer) и вызове
	// (pion callback — другая горутина). onConnectedFired — идемпотентность;
	// сбрасывается в false при recreatePCForGlare (новый pc → новое ICE → unmute
	// должен уйти снова).
	onConnectedMu    sync.Mutex
	onConnected      func()
	onConnectedFired bool

	// isPolite — роль perfect-negotiation (спека §7). Назначается вышестоящим
	// слоем (agent.addPeer) детерминированно по сравнению sessionId: мы polite,
	// если наш sessionId больше remote. При glare (входящий offer в
	// have-local-offer) polite откатывает свой offer (recreatePCForGlare) и
	// принимает входящий, impolite — игнорирует. См. negotiation.go.
	isPolite bool
}

// New создаёт PeerConnection с указанной конфигурацией ICE-серверов и
// регистрирует pion-callback'и (через initPC). Возвращает ошибку только если
// pion не смог создать PC (повреждён ICEServer, системный лимит и т.п.) —
// прикладной код должен трактовать это как фатальное.
func New(cfg Config) (*Peer, error) {
	// Audio-only MediaEngine: регистрируем ТОЛЬКО Opus. Без video-codec'ов
	// pion отвечает на m=video в offer'е браузера port 0 (отклоняет) — nctalk-call
	// строго audio-only (спека §6). Раньше default-API pion'а согласовывал
	// m=video (VP8/H264/...) с Chrome → answer содержал video m-line
	// sendonly/sendrecv БЕЗ реального sender → Chrome не стартовал media,
	// OnTrack не срабатывал, recording.pcm пуст (spike-gate 2026-07-20).
	m := &webrtc.MediaEngine{}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeOpus,
			ClockRate:   48000,
			Channels:    2,
			SDPFmtpLine: "minptime=10;useinbandfec=1",
		},
		PayloadType: 111,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, fmt.Errorf("peer: RegisterCodec(opus): %w", err)
	}
	api := webrtc.NewAPI(webrtc.WithMediaEngine(m))

	p := &Peer{
		outCh:      make(chan signaling.Message, 64),
		failedCh:   make(chan error, 1),
		closed:     make(chan struct{}),
		isPolite:   cfg.IsPolite,
		api:        api,
		iceServers: cfg.ICEServers,
	}
	pc, err := p.initPC()
	if err != nil {
		return nil, err
	}
	p.pc = pc
	return p, nil
}

// initPC создаёт PeerConnection через сохранённый api (тот же audio-only Opus
// MediaEngine) и регистрирует pion-callback'и. Используется в New и в
// recreatePCForGlare — при glare-polite новый pc стартует в stable и принимает
// входящий offer. Callbacks замыкаются на p (не на pc): методы sendOut /
// signalFailure / fireOnConnected / handleIncomingTrack НЕ читают p.pc, поэтому
// после замены p.pc в recreate они корректно работают с новым pc, а stale-callbacks
// закрытого pc гаснут после Close().
func (p *Peer) initPC() (*webrtc.PeerConnection, error) {
	pc, err := p.api.NewPeerConnection(webrtc.Configuration{
		ICEServers: p.iceServers,
	})
	if err != nil {
		return nil, fmt.Errorf("peer: NewPeerConnection: %w", err)
	}

	// Локальные ICE-candidates → outCh как signaling.Message{Type:"candidate"}.
	// c == nil означает end-of-candidates marker (pion собрал все локальные
	// кандидаты) — ничего не отправляем (Spreed-стиль с empty candidate для
	// тестов не критичен).
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		// c.ToJSON() возвращает плоский pion ICECandidateInit с *string/*uint16
		// и json-тегами lowercase. Spreed wire-format — ВЛОЖЕННЫЙ: payload.candidate
		// обязан быть ОБЪЕКТОМ {candidate,sdpMLineIndex,sdpMid} (не строкой), иначе
		// удалённый talk-main.js addIceCandidate(a.payload.candidate) падает с
		// TypeError ×N → peer-pipeline не достраивается → нет audio-sink → нет
		// звука (баг #6, spike-gate 2026-07-20). Оборачиваем плоский pion-формат
		// через signaling.WrapCandidatePayload (wire-format живёт в signaling,
		// симметрично входящему icePayload; см. фикстуру candidate.json).
		initJSON, err := json.Marshal(c.ToJSON())
		if err != nil {
			return
		}
		payload, err := signaling.WrapCandidatePayload(initJSON)
		if err != nil {
			return
		}
		p.sendOut(signaling.Message{Type: "candidate", Payload: payload})
	})

	// Single-peer failure (review замечание 13, спека §10): при
	// PeerConnectionStateFailed сигнализируем вышестоящему слою через failedCh.
	// Вышестоящий слой (agent, Task 2.8) читает Failed(), зовёт Close, удаляет
	// peer из активного набора и ПРОДОЛЖАЕТ работу с оставшимися.
	// PeerConnectionStateClosed НЕ триггерит failedCh — это штатный конец
	// (наш собственный Close либо удалённый side graceful shutdown); клиент
	// узнаёт о завершении через Done().
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateFailed {
			p.signalFailure(errors.New("peer: PeerConnectionStateFailed (ICE/DTLS)"))
		}
	})

	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		if state == webrtc.ICEConnectionStateConnected {
			// ICE connected — дёрнем onConnected-callback (unmute signaling для
			// Spreed-sink, spike-gate root cause 2026-07-20). Идемпотентно — pion
			// может повторять connected-state при ICE restart, unmute нужен 1 раз
			// за жизнь pc (флаг сбрасывается в recreatePCForGlare для нового pc).
			p.fireOnConnected()
		}
	})

	// OnTrack — входящий media-track. pion вызывает handler в своей горутине
	// при появлении remote track. Запускаем decodeLoop, который читает
	// RTP-пакеты и пишёт Opus-payload в зарегистрированный sink.
	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		p.handleIncomingTrack(track)
	})

	return pc, nil
}

// recreatePCForGlare пересоздаёт PeerConnection для polite-glare: pion v4 не
// умеет rollback из have-local-offer (state-machine в signalingstate.go блокирует
// have-local-offer→SetLocal(rollback)→stable), а спека §7 polite-branch требует
// именно rollback. Workaround: закрываем старый pc, создаём новый (тот же
// audio-only MediaEngine/ICE через initPC), переносим outTrack, сбрасываем
// per-pc state. Новый pc стартует в stable → вызывающий код (HandleEvent EvOffer)
// делает на нём SetRemoteDescription(offer) → CreateAnswer → SetLocalDescription.
//
// Sink (OnIncomingAudio) НЕ переносится явно — он остаётся в p.sink, а OnTrack
// нового pc переиспользует handleIncomingTrack → тот же sink. onConnectedFired
// сбрасывается: новый pc → новое ICE-соединение → unmute должен уйти снова.
//
// Конcurrency-инвариант: вызывается из agent-goroutine (HandleEvent,
// single-threaded). Старый pc.Close() гасит его callbacks (OnICECandidate/
// OnTrack/...); возможен короткий хвост от старого decodeLoop, но sink
// thread-safe (FFmpegDecoder-mutex, review замечание 2), и decodeLoop выйдет
// по ошибке track.ReadRTP на закрытом pc.
func (p *Peer) recreatePCForGlare() error {
	old := p.pc
	newPC, err := p.initPC()
	if err != nil {
		return err
	}
	p.pc = newPC
	_ = old.Close() // stale-callbacks старого pc гаснут; decodeLoop выйдет по ReadRTP-ошибке

	// Сброс per-pc state: новый pc стартует без pending candidates / remote descr.
	p.remoteDescr.Store(false)
	p.pendingICE = nil
	// Новый pc → новое ICE-соединение → onConnected (unmute) должен уйти снова.
	p.onConnectedMu.Lock()
	p.onConnectedFired = false
	p.onConnectedMu.Unlock()

	// Перенос исходящего track на новый pc (как в AttachOutgoingTrack).
	if p.outTrack != nil {
		if _, err := newPC.AddTrack(p.outTrack); err != nil {
			return fmt.Errorf("peer: re-AddTrack: %w", err)
		}
	}
	return nil
}

// HandleEvent обрабатывает одно signaling-событие. SDP/ICE flow с glare-handling
// (Task 2.6, спека §7):
//
//   - EvOffer: perfect-negotiation проверка glare (resolveIncomingOffer):
//       impolite при have-local-offer → игнорирует входящий (свой в полёте);
//       polite при have-local-offer   → recreatePCForGlare (pion не умеет
//         rollback), затем SetRemoteDescription(offer) → CreateAnswer → ...
//     stable/нет glare → обычный приём.
//   - EvAnswer: SetRemoteDescription(answer) → drain pendingICE.
//   - EvCandidate: если SetRemoteDescription уже был — AddICECandidate сразу,
//     иначе складываем в pendingICE (будет дренирован после SetRemoteDescription).
//
// Возвращает ошибку при сбое pion-операций (SetRemoteDescription, CreateAnswer,
// AddICECandidate). Ошибка не приводит к автоматическому Close — решение
// принимает вышестоящий слой.
func (p *Peer) HandleEvent(ev signaling.Event) error {
	switch ev.Kind {
	case signaling.EvOffer:
		// Perfect-negotiation glare (Task 2.6, спека §7): если мы в
		// have-local-offer (свой CreateOffer в полёте), входящий offer = glare.
		// impolite игнорирует входящий; polite откатывает свой offer и принимает
		// входящий. Решение — чистая функция в negotiation.go.
		action := resolveIncomingOffer(p.pc.SignalingState(), p.isPolite)
		if action == offerIgnore {
			// impolite при glare: свой offer в приоритете, входящий игнорируем.
			return nil
		}
		// offerAccept. Если glare (have-local-offer) — это polite: pion v4 не
		// умеет rollback из have-local-offer, поэтому пересоздаём pc в stable
		// (recreatePCForGlare), иначе SetRemoteDescription(offer) упадёт с
		// InvalidModificationError (have-local-offer → have-remote-offer).
		if p.pc.SignalingState() == webrtc.SignalingStateHaveLocalOffer {
			if err := p.recreatePCForGlare(); err != nil {
				return fmt.Errorf("peer: recreate for glare: %w", err)
			}
		}
		if err := p.pc.SetRemoteDescription(webrtc.SessionDescription{
			Type: webrtc.SDPTypeOffer, SDP: ev.SDP,
		}); err != nil {
			return fmt.Errorf("peer: SetRemoteDescription(offer): %w", err)
		}
		p.remoteDescr.Store(true)
		p.drainPendingICE()

		answer, err := p.pc.CreateAnswer(nil)
		if err != nil {
			return fmt.Errorf("peer: CreateAnswer: %w", err)
		}
		if err := p.pc.SetLocalDescription(answer); err != nil {
			return fmt.Errorf("peer: SetLocalDescription(answer): %w", err)
		}
		// Payload: {"type":"answer","sdp":"..."} — формат sdpPayload в signaling
		// (см. signaling/types.go). Marshal теоретически не падает на struct'е.
		payload, err := json.Marshal(sdpOutPayload{Type: "answer", SDP: answer.SDP})
		if err != nil {
			return fmt.Errorf("peer: marshal answer: %w", err)
		}
		p.sendOut(signaling.Message{Type: "answer", Payload: payload})
		return nil

	case signaling.EvAnswer:
		if err := p.pc.SetRemoteDescription(webrtc.SessionDescription{
			Type: webrtc.SDPTypeAnswer, SDP: ev.SDP,
		}); err != nil {
			return fmt.Errorf("peer: SetRemoteDescription(answer): %w", err)
		}
		p.remoteDescr.Store(true)
		p.drainPendingICE()
		return nil

	case signaling.EvCandidate:
		init, err := toPionICEInit(ev.Candidate)
		if err != nil {
			return fmt.Errorf("peer: ICECandidateInit: %w", err)
		}
		if !p.remoteDescr.Load() {
			// Remote description ещё не установлен — буферизуем. Spreed не
			// гарантирует порядок candidate-vs-offer (спека §7).
			p.pendingICE = append(p.pendingICE, init)
			return nil
		}
		if err := p.pc.AddICECandidate(init); err != nil {
			return fmt.Errorf("peer: AddICECandidate: %w", err)
		}
		return nil
	}
	return nil
}

// Outgoing возвращает канал исходящих signaling-сообщений (offer/answer/
// candidate). Потребитель (agent, Task 2.8) читает его и вызывает
// signaling.Client.Send для каждого сообщения. Канал НЕ закрывается в Close():
// для определения завершения используйте Done() (или Failed()) в том же select.
func (p *Peer) Outgoing() <-chan signaling.Message { return p.outCh }

// Done возвращает канал, закрываемый при Close(). Используется потребителем
// Outgoing() для выхода из select-цикла отправки. Аналог ctx.Done() в
// стандартных паттернах.
func (p *Peer) Done() <-chan struct{} { return p.closed }

// Failed возвращает канал односторонней нотификации о фатальной ошибке peer'а
// (DTLS/ICE-fail на одном из transport'ов). Получив значение, вышестоящий слой
// зовёт Close() и удаляет peer из активного набора, ПРОДОЛЖАЯ работу с
// оставшимися (mesh переживёт отказ — спека §10).
//
// Канал буферизирован (cap=1): send inside signalFailure не блокирует даже
// если никто не читает (например, во время shutdown).
func (p *Peer) Failed() <-chan error { return p.failedCh }

// AttachOutgoingTrack подключает ОБЩИЙ исходящий audio-track к этой PC (review
// замечание 1). Track создаётся ВЫШЕСТОЯЩИМ слоем (agent) — один на звонок,
// передаётся во все Peer'ы; pion сам размножает отправку через RTPSender'ы
// внутри каждой PC. Запускать encodeLoop на каждый peer НЕЛЬЗЯ — N горутин
// читали бы общий AudioSource.ReadSample round-robin и каждый peer получал
// 1/N пакетов (старый баг fanout'а). encodeLoop живёт в agent'е и пишёт в
// общий track — pion внутри каждой PC делает свой RTPSender и packets.
//
// Вызывать ДО CreateOffer/HandleEvent(EvOffer): m-line для audio-track'а должна
// попасть в SDP. При recreatePCForGlare track пере-добавляется на новый pc.
// Повторный вызов не поддерживается (вернёт ошибку).
func (p *Peer) AttachOutgoingTrack(track *webrtc.TrackLocalStaticSample) error {
	if p.outTrack != nil {
		return errors.New("peer: AttachOutgoingTrack уже вызван (один track на peer)")
	}
	if _, err := p.pc.AddTrack(track); err != nil {
		return fmt.Errorf("peer: AddTrack: %w", err)
	}
	p.outTrack = track
	return nil
}

// OnIncomingAudio регистрирует media.AudioSink для входящих audio-track'ов от
// этого peer'а. Вызывается до установления соединения (OnTrack срабатывает
// асинхронно — без зарегистрированного sink'а входящие треки игнорируются, см.
// handleIncomingTrack). В mesh (Task 2.8) инстанцируется ОТДЕЛЬНЫЙ sink на
// каждого remote-пира (decoder-per-peer), их PCM-выводы сводит media.Mixer.
// Сохраняется при recreatePCForGlare — OnTrack нового pc переиспользует его.
func (p *Peer) OnIncomingAudio(sink media.AudioSink) {
	p.sinkMu.Lock()
	p.sink = sink
	p.sinkMu.Unlock()
}

// CreateOffer — инициация SDP-exchange: создаёт offer и SetLocalDescription
// (pc → have-local-offer), отправляет offer в outCh. Agent (Task 2.8) зовёт
// при появлении нового участника в usersInRoom. При glare (браузер тоже
// позвал offer) — см. HandleEvent(EvOffer) + negotiation.go: impolite игнорирует
// входящий, polite откатывает свой (recreatePCForGlare).
//
// Side-effect: после SetLocalDescription pion начинает собирать local ICE
// candidates, они пойдут в outCh через OnICECandidate.
func (p *Peer) CreateOffer() error {
	offer, err := p.pc.CreateOffer(nil)
	if err != nil {
		return fmt.Errorf("peer: CreateOffer: %w", err)
	}
	if err := p.pc.SetLocalDescription(offer); err != nil {
		return fmt.Errorf("peer: SetLocalDescription(offer): %w", err)
	}
	payload, err := json.Marshal(sdpOutPayload{Type: "offer", SDP: offer.SDP})
	if err != nil {
		return fmt.Errorf("peer: marshal offer: %w", err)
	}
	p.sendOut(signaling.Message{Type: "offer", Payload: payload})
	return nil
}

// OnConnected регистрирует callback, вызываемый один раз при установке ICE
// соединения (ICEConnectionStateConnected). Вышестоящий слой (agent) использует
// его для отправки signaling unmute — без него Spreed держит audio-sink muted
// (spike-gate root cause 2026-07-20). Должен вызываться ДО CreateOffer/HandleEvent
// (как OnIncomingAudio/AttachOutgoingTrack), чтобы не пропустить первый переход
// в connected.
func (p *Peer) OnConnected(fn func()) {
	p.onConnectedMu.Lock()
	p.onConnected = fn
	p.onConnectedMu.Unlock()
}

// fireOnConnected вызывает зарегистрированный OnConnected-callback один раз
// (идемпотентно в рамках жизни pc). Вызывается из pion OnICEConnectionStateChange
// (connected). Флаг onConnectedFired сбрасывается в recreatePCForGlare — новый
// pc получает новое ICE-соединение, unmute должен уйти снова. Callback
// выполняется ВНЕ мьютекса — он может блокировать (signaling.Send), а лок нужен
// только для чтения/установки onConnected/onConnectedFired.
func (p *Peer) fireOnConnected() {
	p.onConnectedMu.Lock()
	if p.onConnectedFired || p.onConnected == nil {
		p.onConnectedMu.Unlock()
		return
	}
	fn := p.onConnected
	p.onConnectedFired = true
	p.onConnectedMu.Unlock()
	fn()
}

// Close — закрывает PeerConnection. Идемпотентный (sync.Once), не паникует,
// не блокирует надолго: pc.Close() синхронный (без DTLS-wait по умолчанию —
// GracefulClose не используем). Безопасен для вызова из горутины мониторинга
// при Failed().
//
// НЕ закрывает AudioSource/AudioSink и НЕ отменяет общий encodeLoop — ими
// владеет вышестоящий слой (agent). pc.Close() асинхронно останавливает
// RTPReceiver → decodeLoop выйдет по ошибке track.ReadRTP.
//
// Гонка decoder.Close ∥ WriteSample защищена отдельно (mutex в FFmpegDecoder,
// review замечание 2) — peer.Close НЕ ждёт выхода decodeLoop.
func (p *Peer) Close() error {
	p.closeOnce.Do(func() {
		close(p.closed) // сигнал decodeLoop/sendOut выйти
		p.closeErr = p.pc.Close()
	})
	return p.closeErr
}

// ---- внутренние helpers ----

// sendOut — non-blocking send в outCh. Если канал полон (маловероятно при
// cap=64) — дропаем сообщение, чтобы не блокировать pion-callback'и. На
// закрытом peer'е (p.closed) — no-op.
func (p *Peer) sendOut(m signaling.Message) {
	select {
	case p.outCh <- m:
	case <-p.closed:
		// Peer закрывается — выходим без panic (outCh не закрывается, см. Close).
	default:
		// Переполнение outCh — дропаем. В normal-flow не возникает (64 buffer);
		// в worst-case теряем candidate (recoverable через ICE-restart) — лучше
		// чем заблокировать pion-callback goroutine.
	}
}

// signalFailure — non-blocking send в failedCh. cap=1 + select-default
// гарантирует, что не блокирует даже если никто не читает Failed().
func (p *Peer) signalFailure(err error) {
	select {
	case p.failedCh <- err:
	default:
		// Кто-то уже положил ошибку (или буфер занят) — не блокируем.
	}
}

// drainPendingICE вызывается после SetRemoteDescription: применяет все
// буферизованные candidates через AddICECandidate. Ошибки отдельных candidates
// игнорируем (один битый candidate не должен ронять peer'а — pion внутри сам
// логирует и продолжает).
func (p *Peer) drainPendingICE() {
	pending := p.pendingICE
	p.pendingICE = nil
	for _, c := range pending {
		_ = p.pc.AddICECandidate(c)
	}
}

// toPionICEInit конвертирует signaling.ICECandidate в pion webrtc.ICECandidateInit.
// Нужно из-за расхождения типов: signaling хранит SDPMLineIndex как *int (т.к.
// external JSON может прийти с любым числом), pion требует *uint16. Конверсия
// int → uint16 безопасна для значений 0..65535 (m-line index всегда в этом
// диапазоне — это индекс m-line в SDP, типично 0).
func toPionICEInit(c signaling.ICECandidate) (webrtc.ICECandidateInit, error) {
	init := webrtc.ICECandidateInit{Candidate: c.Candidate}
	if c.SDPMLineIndex != nil {
		// int → uint16. При переполнении (>65535) возвращаем ошибку — это
		// никогда не должно случиться для реального ICE, но защитно.
		v := *c.SDPMLineIndex
		if v < 0 || v > 65535 {
			return webrtc.ICECandidateInit{}, fmt.Errorf("SDPMLineIndex %d вне диапазона uint16", v)
		}
		u := uint16(v)
		init.SDPMLineIndex = &u
	}
	if c.SDPMid != nil {
		// Копируем значение в новую переменную — *c.SDPMid указывает на
		// чужую память (signaling.Event), берём адрес локальной копии.
		mid := *c.SDPMid
		init.SDPMid = &mid
	}
	return init, nil
}

// sdpOutPayload — JSON-форма исходящего offer/answer. Соответствует
// signaling.sdpPayload (см. internal/call/signaling/types.go): {"type","sdp"}.
// Не экспортируется — это деталь реализации peer.
type sdpOutPayload struct {
	Type string `json:"type"` // "offer" или "answer"
	SDP  string `json:"sdp"`
}
