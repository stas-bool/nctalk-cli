package peer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/pion/webrtc/v4"
	pionmedia "github.com/pion/webrtc/v4/pkg/media"
	"github.com/stas/nctalk/internal/call/media"
)

// handleIncomingTrack — pion OnTrack handler (вызывается из горутины pion при
// появлении remote track). Фильтрует по kind/codec (только audio/opus — спека
// §7: голосовой звонок, video не поддерживается), берёт sink под RLock и
// запускает decodeLoop в своей горутине (не блокирует pion callback).
//
// Если sink не зарегистрирован (OnIncomingAudio не звали) — return без запуска
// decodeLoop: входящие пакеты будут буферизоваться RTPReceiver'ом, но без
// потребителя буфер переполнится и pion отбросит пакеты. Это нормально для
// initial-setup window — decodeLoop запустится при следующем OnTrack, либо
// агент регистрирует sink до установления соединения.
func (p *Peer) handleIncomingTrack(track *webrtc.TrackRemote) {
	if track.Kind() != webrtc.RTPCodecTypeAudio {
		return
	}
	codec := track.Codec()
	if codec.MimeType != webrtc.MimeTypeOpus {
		return
	}
	p.sinkMu.RLock()
	sink := p.sink
	p.sinkMu.RUnlock()
	if sink == nil {
		return
	}
	go p.decodeLoop(track, sink)
}

// decodeLoop читает RTP-пакеты с входящего track'а и пишёт Opus-payload в sink.
// Запускается одной goroutine на каждый принятый track (в normal-flow — один
// audio-track на peer).
//
// Завершение: при pc.Close() pion останавливает RTPReceiver, что вызывает
// return из track.ReadRTP с ошибкой — goroutine выходит. Альтернативный путь:
// sink.WriteSample возвращает ошибку (decoder закрыт вышестоящим слоем) — тоже
// выход.
//
// Длительность фрейма: берём консервативные 20мс (libopus voip default frame
// size при 48к/моно = 960 сэмплов). Реальная длительность упакована в Opus-TOC
// byte, но мы её не парсим — downstream FFmpegDecoder принимает raw Opus и
// сам определяет длительность. 20мс здесь — только метадата для логов/сэмплинг.
func (p *Peer) decodeLoop(track *webrtc.TrackRemote, sink media.AudioSink) {
	const frameDuration = 20 * time.Millisecond
	for {
		// Быстрый выход при Close(): если pion уже остановил receiver, ReadRTP
		// сразу вернёт ошибку. Если нет — заблокируем до следующего пакета.
		// Это ОК: pc.Close() синхронно останавливает RTPReceiver.
		select {
		case <-p.closed:
			return
		default:
		}
		pkt, _, err := track.ReadRTP()
		if err != nil {
			// io.EOF / ErrReceiverClosed — штатный выход при Close пира.
			// Прочие ошибки тоже не recoverable — логируем через Failed().
			if !errors.Is(err, io.EOF) {
				// Не все ошибки pion оборачивает в io.EOF (например, «receiver
				// closed» — это собственный тип). Тихо выходим — корректное
				// завершение при Close не должно триггерить Failed().
				select {
				case <-p.closed:
					return
				default:
				}
				// Действительно посторонняя ошибка — сигнализируем.
				p.signalFailure(fmt.Errorf("peer: TrackRemote.ReadRTP: %w", err))
			}
			return
		}
		if err := sink.WriteSample(pkt.Payload, frameDuration); err != nil {
			// Sink закрыт (decoder убит вышестоящим слоем). Тихо выходим —
			// это не фатал peer'а.
			return
		}
	}
}

// encodeLoop читает Opus-фреймы из AudioSource и пишёт их в outgoingTrack через
// WriteSample (pion сам пакетирует в RTP). Запускается одной goroutine в
// AttachOutgoingAudio.
//
// Завершение:
//   - p.closed закрыт (peer.Close()) → выход.
//   - srcCtx отменён (peer.Close() через srcCancel) → оборвать ReadSample можно
//     только если сам src уважает ctx (FFmpegEncoder — уважает через ctx.Done()
//     в select'е ReadSample).
//   - src.ReadSample вернул io.EOF — штатный конец потока (например, устройство
//     ввода закрылось). Тихо выходим.
//   - src.ReadSample вернул прочую ошибку — фатал, сигнализируем через Failed().
//   - outTrack.WriteSample вернул ошибку (pc закрывается) — тихо выходим.
//
// Один encoder/encodeLoop на звонок (НЕ на peer): agent (Task 2.8) создаёт
// ОДИН media.FFmpegEncoder и подключает его к каждому peer через
// AttachOutgoingAudio — pion сам размножает отправку через свои RTPSender'ы
// (см. план §note к Task 2.4 «Направление encode — одно на звонок»). В этой
// задаче это отразится так: encodeLoop здесь один на Peer, но Caller (agent)
// использует один общий AudioSource для всех Peer'ов.
func (p *Peer) encodeLoop(src media.AudioSource) {
	for {
		select {
		case <-p.closed:
			return
		case <-p.srcCtx.Done():
			return
		default:
		}
		payload, dur, err := src.ReadSample()
		if err != nil {
			// Штатные причины конца потока — тихо выходим:
			//   - io.EOF: источник исчерпан (например, input device закрыт,
			//     файл до конца прочитан);
			//   - context.Canceled / DeadlineExceeded: вышестоящий слой
			//     закрыл источник (FFmpegEncoder.Close отменяет внутренний ctx).
			//     Не трактуем как фатал peer'а — это нормальный shutdown path.
			if errors.Is(err, io.EOF) ||
				errors.Is(err, context.Canceled) ||
				errors.Is(err, context.DeadlineExceeded) {
				return
			}
			// Проверяем, не был ли peer уже закрыт (тогда любая ошибка src
			// не интересна — мы и так выходим).
			select {
			case <-p.closed:
				return
			default:
			}
			p.signalFailure(fmt.Errorf("peer: AudioSource.ReadSample: %w", err))
			return
		}
		// Дополнительная проверка перед WriteSample — между ReadSample и Write
		// мог произойти Close.
		select {
		case <-p.closed:
			return
		default:
		}
		if err := p.outTrack.WriteSample(pionmedia.Sample{
			Data:     payload,
			Duration: dur,
		}); err != nil {
			// WriteSample падает при закрытии PC — тихо выходим.
			return
		}
	}
}
