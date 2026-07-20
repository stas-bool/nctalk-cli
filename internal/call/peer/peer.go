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
// В этой задаче (Task 2.4) реализован МИНИМАЛЬНЫЙ flow БЕЗ perfect-negotiation:
// accept offer → CreateAnswer → send; incoming ICE буферизуется до
// SetRemoteDescription. Glare-handling (одновременные offer'ы двух пиров с
// ролями polite/impolite) — Task 2.6; поле Config.IsPolite сохранено, но
// логики glare здесь нет.
package peer

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/stas/nctalk/internal/call/media"
	"github.com/stas/nctalk/internal/call/signaling"
)

// Config для New. ICEServers — из capability сервера (Task 2.10), НЕ из env:
// TURN-credentials (Username/Credential/CredentialType) поставляются capability
// вместе с URL-ами серверов. Empty list допустим — pion работает с host
// candidates (тесты на localhost ICE). Спека §5.
type Config struct {
	ICEServers []webrtc.ICEServer
	// IsPolite — роль perfect-negotiation (спека §7). Назначается детерминированно
	// по сравнению sessionId/actorId пиров (Task 2.6). В Task 2.4 поле сохранено,
	// но логики glare нет — минимальный flow предполагает, что инициатор один.
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
type Peer struct {
	pc *webrtc.PeerConnection

	// outTrack — общий TrackLocalStaticSample, добавленный в эту PC через
	// AddTrack в AttachOutgoingTrack. nil до вызова AttachOutgoingTrack.
	// Сам track создаётся ВЫШЕСТОЯЩИМ слоем (agent) — один на звонок, добавляется
	// в каждую PC; pion внутри каждой PC строит отдельный RTPSender и
	// пакует samples в RTP независимо. Это и есть fanout — без N encodeLoop'ов.
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
	pendingICE []webrtc.ICECandidateInit

	// remoteDescr — атомарный флаг: SetRemoteDescription уже выполнен.
	// Используется в HandleEvent(EvCandidate) для решения «буферизовать или
	// AddICECandidate сразу».
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

	// sendCounter — DEBUG-instrumentation (spike-gate 2026-07-20): interceptor,
	// считающий отправленные pion'ом RTP-пакеты. Отвечает на гипотезу (a)
	// «pion RTPSender не отправляет RTP»: BindLocalStream-хук вызывается на
	// каждый уходящий пакет. packets=0 при работающем encodeLoop → pion НЕ
	// вызывает SendRTP → проблема в RTPSender/transceiver-binding/SRTP.
	// УБРАТЬ вместе с остальными debug-логами (next step #2 в state-файле).
	sendCounter *sendCounterInterceptor
}

// New создаёт PeerConnection с указанной конфигурацией ICE-серверов и
// регистрирует pion-callback'и: OnICECandidate (→ outCh), OnConnectionStateChange
// (→ failedCh при Failed), OnTrack (→ decodeLoop в sink). Возвращает ошибку
// только если pion не смог создать PC (повреждён ICEServer, системный лимит и
// т.п.) — прикладной код должен трактовать это как фатальное.
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
	// DEBUG-instrumentation (spike-gate 2026-07-20): interceptor-счётчик
	// отправленных RTP-пакетов. packets>0 при encodeLoop → pion SendRPT
	// вызывается (гипотеза (a) исключена). УБРАТЬ с остальными debug-логами.
	counter := &sendCounterInterceptor{}
	ir := &interceptor.Registry{}
	ir.Add(&counterFactory{c: counter})
	api := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(ir))
	pc, err := api.NewPeerConnection(webrtc.Configuration{
		ICEServers: cfg.ICEServers,
	})
	if err != nil {
		return nil, fmt.Errorf("peer: NewPeerConnection: %w", err)
	}

	p := &Peer{
		pc:          pc,
		outCh:       make(chan signaling.Message, 64),
		failedCh:    make(chan error, 1),
		closed:      make(chan struct{}),
		sendCounter: counter,
	}

	// Локальные ICE-candidates → outCh как signaling.Message{Type:"candidate"}.
	// c == nil означает end-of-candidates marker (pion собрал все локальные
	// кандидаты) — ничего не отправляем (Spreed-стиль с empty candidate для
	// тестов не критичен).
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			log.Printf("DEBUG peer: ICE gathering DONE (end-of-candidates)")
			return
		}
		log.Printf("DEBUG peer: LOCAL candidate typ=%s addr=%s port=%d",
			c.Typ, c.Address, c.Port)
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
		log.Printf("DEBUG peer: PC state=%s", state)
		if state == webrtc.PeerConnectionStateFailed {
			p.signalFailure(errors.New("peer: PeerConnectionStateFailed (ICE/DTLS)"))
		}
	})

	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		log.Printf("DEBUG peer: ICE-conn state=%s", state)
		if state == webrtc.ICEConnectionStateConnected {
			senders := p.pc.GetSenders()
			log.Printf("DEBUG peer: senders=%d", len(senders))
			for i, s := range senders {
				t := s.Track()
				tn := "<nil>"
				if t != nil {
					tn = t.ID()
				}
				log.Printf("DEBUG peer: sender[%d] track=%s", i, tn)
			}
		}
	})

	// OnTrack — входящий media-track. pion вызывает handler в своей горутине
	// при появлении remote track. Запускаем decodeLoop, который читает
	// RTP-пакеты и пишёт Opus-payload в зарегистрированный sink.
	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		log.Printf("DEBUG peer: OnTrack id=%s", track.ID())
		p.handleIncomingTrack(track)
	})

	// DEBUG: периодический лог счётчика отправленных RTP (1с) + финальный
	// снимок при Close. Показывает, уходит ли RTP при работающем encodeLoop.
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		var lastPackets, lastBytes uint64
		for {
			select {
			case <-ticker.C:
				pkts := counter.packets.Load()
				byts := counter.bytes.Load()
				// Безусловный лог (даже при 0) — различает «интерсептор не привязан»
				// от «привязан, но pion не отправляет».
				log.Printf("DEBUG peer: RTP sent packets=%d (+%d) bytes=%d (+%d)",
					pkts, pkts-lastPackets, byts, byts-lastBytes)
				lastPackets = pkts
				lastBytes = byts
			case <-p.closed:
				log.Printf("DEBUG peer: RTP sent FINAL packets=%d bytes=%d",
					counter.packets.Load(), counter.bytes.Load())
				return
			}
		}
	}()

	return p, nil
}

// HandleEvent обрабатывает одно signaling-событие. SDP/ICE flow — минимальный,
// без glare (Task 2.6):
//
//   - EvOffer: SetRemoteDescription(offer) → drain pendingICE → CreateAnswer →
//     SetLocalDescription → отправка answer в outCh.
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
		if err := p.pc.SetRemoteDescription(webrtc.SessionDescription{
			Type: webrtc.SDPTypeOffer, SDP: ev.SDP,
		}); err != nil {
			return fmt.Errorf("peer: SetRemoteDescription(offer): %w", err)
		}
		log.Printf("DEBUG OFFER (remote) SDP:\n%s", ev.SDP)
		p.remoteDescr.Store(true)
		p.drainPendingICE()

		answer, err := p.pc.CreateAnswer(nil)
		if err != nil {
			return fmt.Errorf("peer: CreateAnswer: %w", err)
		}
		log.Printf("DEBUG ANSWER (local) SDP:\n%s", answer.SDP)
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
		log.Printf("DEBUG ANSWER (remote) SDP:\n%s", ev.SDP)
		if err := p.pc.SetRemoteDescription(webrtc.SessionDescription{
			Type: webrtc.SDPTypeAnswer, SDP: ev.SDP,
		}); err != nil {
			return fmt.Errorf("peer: SetRemoteDescription(answer): %w", err)
		}
		p.remoteDescr.Store(true)
		p.drainPendingICE()
		return nil

	case signaling.EvCandidate:
		log.Printf("DEBUG peer: REMOTE candidate: %s", ev.Candidate.Candidate)
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
// попасть в SDP. Повторный вызов не поддерживается (вернёт ошибку).
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
func (p *Peer) OnIncomingAudio(sink media.AudioSink) {
	p.sinkMu.Lock()
	p.sink = sink
	p.sinkMu.Unlock()
}

// CreateOrder — ЭКСПЕРИМЕНТАЛЬНЫЙ helper для инициатора offer'а. В минимальном
// flow (без glare, Task 2.4) один из пиров создаёт offer, второй отвечает.
// Agent (Task 2.8) будет звать CreateOffer при появлении нового участника в
// usersInRoom (для impolite-пира, при glare — см. perfect-negotiation в §7).
//
// Side-effect: после SetLocalDescription pion начинает собирать local ICE
// candidates, они пойдут в outCh через OnICECandidate.
func (p *Peer) CreateOffer() error {
	offer, err := p.pc.CreateOffer(nil)
	if err != nil {
		return fmt.Errorf("peer: CreateOffer: %w", err)
	}
	log.Printf("DEBUG OFFER (local CreateOffer) SDP:\n%s", offer.SDP)
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

// ---- DEBUG: interceptor-счётчик отправленных RTP (spike-gate 2026-07-20) ----
//
// sendCounterInterceptor встраивает NoOp и переопределяет только BindLocalStream:
// pion вызывает writer.Write на каждый уходящий RTP-пакет. Счётчики показывают,
// вызывает ли pion SendRTP при track.WriteSample (encodeLoop). УБРАТЬ с debug-логами.

type sendCounterInterceptor struct {
	interceptor.NoOp
	packets atomic.Uint64
	bytes   atomic.Uint64
}

func (c *sendCounterInterceptor) BindLocalStream(info *interceptor.StreamInfo, writer interceptor.RTPWriter) interceptor.RTPWriter {
	log.Printf("DEBUG peer: BindLocalStream called ssrc=%d", info.SSRC)
	return interceptor.RTPWriterFunc(func(header *rtp.Header, payload []byte, attrs interceptor.Attributes) (int, error) {
		n, err := writer.Write(header, payload, attrs)
		if err == nil && n > 0 {
			c.packets.Add(1)
			c.bytes.Add(uint64(len(payload)))
		}
		return n, err
	})
}

// counterFactory реализует interceptor.Factory, возвращая тот же экземпляр
// счётчика — pion вызовет на нём BindLocalStream при создании outgoing track.
type counterFactory struct{ c *sendCounterInterceptor }

func (f *counterFactory) NewInterceptor(_ string) (interceptor.Interceptor, error) {
	return f.c, nil
}
