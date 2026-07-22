// Package media определяет интерфейсы AudioSource/AudioSink (уровень
// raw Opus-фреймов), Opus-codec-стратегию через ffmpeg-subprocess и Mixer
// (сведение N входящих потоков в единый исходящий — Task 2.7). Спека
// 2026-07-19 §4/§8/§9.
//
// Codec-стратегия инкапсулирована здесь, за интерфейсами
// AudioSource/AudioSink. peer (Task 2.4) потребляет только интерфейсы —
// смена стратегии (a1 OGG-парсер → a3 RTP → b CGO libopus) затрагивает
// только этот пакет, не трогая peer. Контракт PCM на границе режимов
// зафиксирован: s16le/48к/моно, без валидации (§6).
package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/stas/nctalk/internal/call/media/ogg"
)

// ---- Контракт PCM (фиксированный, спека §6/§8) ----

const (
	// PCMSampleRate — частота PCM на границе с режимами (call/agent,
	// call/interactive) и с ffmpeg-субпроцессами. libopus native rate = 48 кГц,
	// поэтому меньше брать нельзя.
	PCMSampleRate = 48000
	// PCMChannels — моно. Для разговорной речи больше не нужно; libopus voip
	// настроен на моно.
	PCMChannels = 1
	// PCMFrameDuration — длительность одного Opus-фрейма (libopus voip default
	// 20мс). На 48к/моно = 960 сэмплов = 1920 байт s16le.
	PCMFrameDuration = 20 * time.Millisecond
	// PCMSampleFormat — строка формата для ffmpeg (-f).
	PCMSampleFormat = "s16le"
)

// ---- AudioSource / AudioSink (спека §4/§7) ----

// AudioSource — источник raw Opus-фреймов для отправляющего трека (реализации:
// FFmpegEncoder, AudioDeviceIn). peer (Task 2.4) использует только интерфейс.
//
// Контракт: один вызов ReadSample = один raw Opus-пакет (~20мс). io.EOF —
// штатный конец потока (например, завершилось чтение с устройства или
// источник исчерпан). Другие ошибки — фатальные, приводят к разрыву звонка.
type AudioSource interface {
	// ReadSample возвращает один raw Opus-пакет и его расчётную длительность
	// (для метаданных в pion WriteSample; libopus voip = 20мс на 48к/моно).
	ReadSample() (payload []byte, duration time.Duration, err error)
	Close() error
}

// AudioSink — приёмник raw Opus-фреймов с входящего трека. Спека §4/§7.
// В mesh (Task 2.8) инстанцируется ОТДЕЛЬНЫЙ sink на каждого remote-пира
// (decoder-per-peer); их PCM-выводы сводит media.Mixer.
type AudioSink interface {
	WriteSample(payload []byte, duration time.Duration) error
	Close() error
}

// ---- FFmpegEncoder: PCM (s16le/48к/моно) → raw Opus ----

// FFmpegEncoder запускает ffmpeg-encode, по stdin принимает PCM, по stdout
// читает OGG-поток, ogg.Reader режет на raw Opus → ReadSample.
// Реализует AudioSource. Спека §8 (encode команда).
//
// Один FFmpegEncoder = один ffmpeg-субпроцесс = один PCM→Opus поток.
// Потокобезопасен на ReadSample/Close.
type FFmpegEncoder struct {
	cmd     *exec.Cmd
	ctx     context.Context
	cancel  context.CancelFunc
	stdout  io.ReadCloser
	packets chan []byte // буферизованный канал raw Opus-пакетов от pump-горутины
	done    chan error  // сюда pump кладёт финальную ошибку (nil если io.EOF)
	errOnce sync.Once
	err     error // первая фатальная ошибка pump'а (для ReadSample после закрытия)

	closeOnce sync.Once
	// waitOnce гарантирует единственный cmd.Wait() (гоночная безопасность
	// между pump и Close, review замечание 4). exec.Cmd.Wait в принципе
	// идемпотентен, но явный Once делает инвариант очевидным.
	waitOnce sync.Once
	waitErr  error
}

// NewFFmpegEncoder запускает ffmpeg с параметрами encode (спека §8).
// stdin — источник PCM (s16le/48к/моно).
//
// Контекст субпроцессу создаём внутренний — Close() его отменяет. Базовая
// orphan-protection через cmd.Cancel (Kill) + WaitDelay 5с (Task 3.4 расширит).
func NewFFmpegEncoder(stdin io.Reader) (*FFmpegEncoder, error) {
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner",
		"-f", PCMSampleFormat,
		"-ar", fmt.Sprintf("%d", PCMSampleRate),
		"-ac", fmt.Sprintf("%d", PCMChannels),
		"-i", "-",
		"-c:a", "libopus",
		"-application", "voip",
		"-f", "opus", // muxer "opus" существует в ffmpeg — это Ogg-Opus
		"-",
	)
	cmd.Stdin = stdin
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("media: stdout pipe: %w", err)
	}
	// Базовая orphan-protection (Task 3.4 расширит).
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
	cmd.WaitDelay = 5 * time.Second

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("media: запуск ffmpeg-encode: %w", err)
	}

	e := &FFmpegEncoder{
		cmd:     cmd,
		ctx:     ctx,
		cancel:  cancel,
		stdout:  stdout,
		packets: make(chan []byte, 50), // ~1с буфера при 20мс/пакет
		done:    make(chan error, 1),
	}
	go e.pump()
	return e, nil
}

// pump читает OGG из stdout ffmpeg через ogg.Reader, кладёт raw Opus-пакеты
// в канал. Завершается при io.EOF от Reader'а (ffmpeg закрыл stdout) или
// при ctx.Cancel (вызывая тем самым блокировку select'а).
//
// Контракт диагностики (review замечание 4, спека §10 «ffmpeg/sox упали →
// exit 1»): io.EOF от Reader'а означает «ffmpeg закрыл stdout». Это может быть
//   - нормальный конец входа (stdin EOF → ffmpeg финишировал, exit 0),
//   - наш shutdown (Close вызвал cancel → Kill → stdout закрыт),
//   - авария (OOM, нет libopus, битый вход — exit != 0).
//
// Различаем по коду выхода: cmd.Wait() после io.EOF возвращает код, по нему
// судим. exec.Cmd.Wait идемпотентен; для надёжности обёрнут в waitOnce —
// pump и Close могут звать Wait конкурентно.
func (e *FFmpegEncoder) pump() {
	defer close(e.packets)
	r := ogg.NewReader(e.stdout)
	for {
		pkt, err := r.NextPacket()
		if err != nil {
			if errors.Is(err, io.EOF) {
				if e.ctx.Err() != nil {
					// Наш shutdown (cancel уже сработал) — штатно.
					e.done <- nil
					return
				}
				// ctx НЕ отменён, но stdout закрыт. ffmpeg либо закончил
				// обработку stdin (exit 0), либо упал (exit != 0). Различаем
				// по коду ожидания — это критично для диагностики §10
				// («упали → exit 1 с указанием backend'а»).
				waitErr := e.waitAndGetErr()
				if waitErr == nil {
					e.done <- nil
				} else {
					e.done <- fmt.Errorf("media: ffmpeg-encode упал: %w", waitErr)
				}
				return
			}
			e.done <- err
			return
		}
		// Копируем payload — Reader держит общий slice до следующей страницы.
		dup := make([]byte, len(pkt))
		copy(dup, pkt)
		select {
		case e.packets <- dup:
		case <-e.ctx.Done():
			return
		}
	}
}

// waitAndGetErr возвращает результат cmd.Wait, вызывается ровно один раз
// (sync.Once). Безопасно для конкурентного вызова из pump и Close.
func (e *FFmpegEncoder) waitAndGetErr() error {
	e.waitOnce.Do(func() {
		e.waitErr = e.cmd.Wait()
	})
	return e.waitErr
}

// ReadSample возвращает следующий raw Opus-пакет. Блокирует до прибытия
// пакета, контекстного Cancel или окончания потока (io.EOF). После io.EOF
// возвращает io.EOF во всех последующих вызовах.
func (e *FFmpegEncoder) ReadSample() ([]byte, time.Duration, error) {
	select {
	case pkt, ok := <-e.packets:
		if !ok {
			// Канал закрыт — pump завершился. Возвращаем причину.
			select {
			case err := <-e.done:
				if err != nil {
					return nil, 0, err
				}
			default:
			}
			return nil, 0, io.EOF
		}
		// Консервативная длительность: 20мс = стандартный libopus voip
		// frame size при 48к/моно (960 сэмплов). Метадата для pion (он сам
		// считает timestamp через RTP).
		return pkt, PCMFrameDuration, nil
	case <-e.ctx.Done():
		return nil, 0, e.ctx.Err()
	}
}

// Close отменяет контекст субпроцесса (Kill) и ждёт cmd.Wait. Безопасен
// для повторного вызова. Wait обёрнут в waitOnce — безопасно, даже если pump
// уже дождался процесса (см. waitAndGetErr, review замечание 4).
func (e *FFmpegEncoder) Close() error {
	var waitErr error
	e.closeOnce.Do(func() {
		e.cancel()
		waitErr = e.waitAndGetErr()
	})
	return waitErr
}

// ---- FFmpegDecoder: raw Opus → PCM (s16le/48к/моно) ----

// FFmpegDecoder запускает ffmpeg-decode. Получает raw Opus через WriteSample
// (оборачивает в OGG через ogg.Writer), кормит ffmpeg-stdin, читает PCM с
// ffmpeg-stdout → внешний io.Writer. Реализует AudioSink.
//
// ВНИМАНИЕ (review замечание 5): один FFmpegDecoder = один ffmpeg-субпроцесс =
// один PCM-поток. НЕ потокобезопасен на WriteSample (race на общий stdin
// pipe). В mesh (Task 2.8) инстанцируется ОТДЕЛЬНЫЙ decoder на каждого
// remote-пира.
//
// pcmW получает PCM-байты сразу из ffmpeg-stdout (cmd.Stdout = pcmW).
// Когда ffmpeg-stdin закрывается (Close()), ffmpeg финиширует encode,
// записывает остаток PCM, выходит; cmd.Wait() дожидается.
type FFmpegDecoder struct {
	cmd       *exec.Cmd
	cancel    context.CancelFunc
	stdin     io.WriteCloser
	writer    *ogg.Writer
	closeOnce sync.Once

	// mu сериализует WriteSample и Close (review замечание 2). Без мьютекса
	// параллельные WriteSample (из peer.decodeLoop, пока pion RTPReceiver ещё
	// не остановлен) и Close (из agent.removePeer/reconcile) дают race на
	// внутреннем состоянии ogg.Writer (granule/pageSeq) и интерливинг байтов
	// в stdin-pipe ffmpeg. peer.Close() не дожидается выхода decodeLoop
	// (pion останавливает RTPReceiver асинхронно после pc.Close) — без этой
	// защиты корректность shutdown'а зависит от таймингов pion.
	mu sync.Mutex
}

// NewFFmpegDecoder запускает ffmpeg-decode (спека §8). pcmW — приёмник PCM
// (s16le/48к/моно) из ffmpeg-stdout.
//
// ОТКЛОНЕНИЕ ОТ БРИФА/СПЕКИ §8: в ffmpeg 7.x demuxer с именем "opus"
// НЕ существует (только muxer). Используем `-f ogg` — ffmpeg по
// capture-pattern'у и OpusHead сам определяет Opus-in-OGG. Был `-f opus -i -`
// → теперь `-f ogg -i -`. Это исправление нужно внести в спеку.
func NewFFmpegDecoder(pcmW io.Writer) (*FFmpegDecoder, error) {
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner",
		"-f", "ogg", // demuxer "ogg" (НЕ "opus" — его нет в ffmpeg 7.x)
		"-i", "-",
		"-f", PCMSampleFormat,
		"-ar", fmt.Sprintf("%d", PCMSampleRate),
		"-ac", fmt.Sprintf("%d", PCMChannels),
		"-",
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("media: stdin pipe: %w", err)
	}
	cmd.Stdout = pcmW
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
	cmd.WaitDelay = 5 * time.Second

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("media: запуск ffmpeg-decode: %w", err)
	}

	// ogg.Writer ПЕРВЫМ делом пишет BOS с OpusHead + vorbis-comment. Без них
	// ffmpeg `-f ogg -i -` падает «Invalid OpusHead» (review замечание 4).
	w, err := ogg.NewWriter(stdin)
	if err != nil {
		cancel()
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("media: ogg.Writer init: %w", err)
	}

	return &FFmpegDecoder{
		cmd:    cmd,
		cancel: cancel,
		stdin:  stdin,
		writer: w,
	}, nil
}

// WriteSample оборачивает payload в OGG-страницу и пишет в ffmpeg-stdin.
// Под мьютексом — чтобы избежать race с параллельным Close (review замечание 2).
func (d *FFmpegDecoder) WriteSample(payload []byte, duration time.Duration) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.writer.WritePacket(payload)
}

// Close пишет EOS-страницу (Flush), закрывает stdin → ffmpeg финиширует,
// ждёт cmd.Wait. Безопасен для повторного вызова. Берёт тот же мьютекс, что
// WriteSample — in-flight WriteSample завершится до того, как stdin закроется.
func (d *FFmpegDecoder) Close() error {
	var waitErr error
	d.closeOnce.Do(func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		// Пытаемся мягко закрыть: EOS + close-stdin → ffmpeg финиширует сам.
		// При ошибке — отменяем (Kill).
		if err := d.writer.Flush(); err != nil {
			d.cancel()
			waitErr = err
		}
		if err := d.stdin.Close(); err != nil && waitErr == nil {
			d.cancel()
			waitErr = err
		}
		werr := d.cmd.Wait()
		if waitErr == nil {
			waitErr = werr
		}
	})
	return waitErr
}
