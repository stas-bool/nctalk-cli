package peer

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/stas-bool/nctalk-cli/internal/call/media"
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
			// Sink закрыт. Если peer тоже закрывается — штатный выход.
			// Иначе (review замечание 14 / 4 на стороне decode) — ffmpeg-decode
			// упал, peer продолжает получать RTP, но sink пишет в закрытый
			// pipe. Не глотаем это молча — сигнализируем Failed(), агент
			// уберёт пир из набора.
			select {
			case <-p.closed:
				return
			default:
			}
			p.signalFailure(fmt.Errorf("peer: sink.WriteSample: %w", err))
			return
		}
	}
}
