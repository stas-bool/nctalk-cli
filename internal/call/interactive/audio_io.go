// internal/call/interactive/audio_io.go — device-IO: микрофон → Opus (MicSource),
// PCM s16le → динамик (speakerWriter). Спека 2026-07-21 §3, §4.2.
// Review finding #1 (CRITICAL): input=avfoundation (D avfoundation muxer),
// output=audiotoolbox (E audiotoolbox muxer) — НЕ avfoundation для output.
package interactive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/stas-bool/nctalk-cli/internal/call/media"
	"github.com/stas-bool/nctalk-cli/internal/call/media/ogg"
)

// ---- MicSource: микрофон → raw Opus ----

// MicSource — микрофон → raw Opus. Реализует media.AudioSource. Один ffmpeg-процесс
// «capture+encode»: avfoundation → libopus → OGG/Opus в pipe → ogg-парсер → ReadSample.
// НЕ требует PCM-stdin (вход — устройство, не pipe).
//
// LAZY START (review #8): ffmpeg-capture НЕ запускается в NewMicSource. Захват
// стартует при первом ReadSample (sync.Once) — внутри encodeLoop, ПОСЛЕ JoinCall + ICE
// setup (сотни мс). Если стартовать в конструкторе, packets-канал cap≈1с переполнится
// до того, как encodeLoop начнёт читать (consumer ещё не готов).
//
// Close IDEMPOTENT (sync.Once, review #4): agent.Run зовёт encoder.Close()
// (= cfg.AudioIn.Close = muteSource.Close = MicSource.Close); повторный вызов не падает.
type MicSource struct {
	device string // avfoundation-строка формата ":<audio_idx>"

	startOnce sync.Once
	startErr  error
	cmd       *exec.Cmd
	ctx       context.Context
	cancel    context.CancelFunc
	stdout    io.ReadCloser
	packets   chan []byte // буферизованный канал raw Opus от pump
	done      chan error

	closeOnce sync.Once
	waitOnce  sync.Once
	waitErr   error
}

// NewMicSource — НЕ запускает ffmpeg (lazy, см. выше). Device — avfoundation-строка
// формата ":<audio_idx>" (default ":0" = первый системный audio-input / микрофон).
// Из env NCTALK_AUDIO_DEVICE_IN (см. §3). Пустой device → default ":0".
func NewMicSource(device string) *MicSource {
	if device == "" {
		device = ":0"
	}
	return &MicSource{device: device}
}

// start запускает ffmpeg-capture+encode. Идемпотентно через startOnce.
// Команда: ffmpeg -f avfoundation -i "<device>" -ac 1 -ar 48000 -c:a libopus
//          -application voip -f opus -
// muxer "opus" = Ogg-Opus (подтверждено в media/codec.go для FFmpegEncoder).
func (m *MicSource) start() error {
	m.startOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		cmd := exec.CommandContext(ctx, "ffmpeg",
			"-hide_banner",
			"-f", "avfoundation",
			"-i", m.device,
			"-ac", fmt.Sprintf("%d", media.PCMChannels),
			"-ar", fmt.Sprintf("%d", media.PCMSampleRate),
			"-c:a", "libopus",
			"-application", "voip",
			"-f", "opus",
			"-",
		)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			cancel()
			m.startErr = fmt.Errorf("audio_io: stdout pipe: %w", err)
			return
		}
		cmd.Cancel = func() error {
			if cmd.Process == nil {
				return nil
			}
			return cmd.Process.Kill()
		}
		cmd.WaitDelay = 5 * time.Second
		if err := cmd.Start(); err != nil {
			cancel()
			m.startErr = fmt.Errorf("audio_io: запуск ffmpeg-avfoundation: %w", err)
			return
		}
		m.cmd = cmd
		m.ctx = ctx
		m.cancel = cancel
		m.stdout = stdout
		m.packets = make(chan []byte, 50)
		m.done = make(chan error, 1)
		go m.pump()
	})
	return m.startErr
}

// pump — аналог FFmpegEncoder.pump: ogg.Reader → packets-канал.
func (m *MicSource) pump() {
	defer close(m.packets)
	r := ogg.NewReader(m.stdout)
	for {
		pkt, err := r.NextPacket()
		if err != nil {
			if errors.Is(err, io.EOF) {
				if m.ctx.Err() != nil {
					m.done <- nil
					return
				}
				waitErr := m.waitAndGetErr()
				if waitErr == nil {
					m.done <- nil
				} else {
					m.done <- fmt.Errorf("audio_io: ffmpeg-capture упал: %w", waitErr)
				}
				return
			}
			m.done <- err
			return
		}
		dup := make([]byte, len(pkt))
		copy(dup, pkt)
		select {
		case m.packets <- dup:
		case <-m.ctx.Done():
			return
		}
	}
}

func (m *MicSource) waitAndGetErr() error {
	m.waitOnce.Do(func() { m.waitErr = m.cmd.Wait() })
	return m.waitErr
}

// ReadSample — lazy-start + чтение из packets-канала. Возвращает raw Opus-пакет
// и его длительность (20мс = libopus voip default при 48к/моно).
func (m *MicSource) ReadSample() ([]byte, time.Duration, error) {
	if err := m.start(); err != nil {
		return nil, 0, err
	}
	select {
	case pkt, ok := <-m.packets:
		if !ok {
			select {
			case err := <-m.done:
				if err != nil {
					return nil, 0, err
				}
			default:
			}
			return nil, 0, io.EOF
		}
		return pkt, media.PCMFrameDuration, nil
	case <-m.ctx.Done():
		return nil, 0, m.ctx.Err()
	}
}

// Close — idempotent (review #4). Kill ffmpeg + Wait.
func (m *MicSource) Close() error {
	var waitErr error
	m.closeOnce.Do(func() {
		// Если start не отработал — закрывать нечего.
		if m.cmd == nil {
			return
		}
		m.cancel()
		waitErr = m.waitAndGetErr()
	})
	return waitErr
}

// compile-time: MicSource реализует полный media.AudioSource.
var _ media.AudioSource = (*MicSource)(nil)

// ---- speakerWriter: PCM s16le → динамик (audiotoolbox) ----

// speakerWriter — PCM s16le → динамик через audiotoolbox-muxer. io.WriteCloser
// (НЕ AudioSink — спека §3: device-out это io.Writer, а не AudioSink).
// N: из env NCTALK_AUDIO_DEVICE_OUT (int-индекс или "default"/"-1").
//
// Команда: ffmpeg -f s16le -ar 48000 -ac 1 -i - -f audiotoolbox -audio_device_index <N> -
// review finding #1 (CRITICAL): output muxer = audiotoolbox (НЕ avfoundation — он input-only).
type speakerWriter struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	stdin  io.WriteCloser

	firstErr  error // первый error от ffmpeg-stdin (review M-4: silent crash detection)
	errOnce   sync.Once
	closeOnce sync.Once
	waitOnce  sync.Once
	waitErr   error
}

// NewSpeakerWriter запускает playback-ffmpeg и возвращает stdin-pipe как io.WriteCloser.
// deviceOut — int-индекс audiotoolbox или строка "default"/"-1" (default "-1" =
// системное устройство вывода). Пустая строка → "-1".
func NewSpeakerWriter(deviceOut string) (*speakerWriter, error) {
	if deviceOut == "" || deviceOut == "default" {
		deviceOut = "-1"
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner",
		"-f", media.PCMSampleFormat,
		"-ar", fmt.Sprintf("%d", media.PCMSampleRate),
		"-ac", fmt.Sprintf("%d", media.PCMChannels),
		"-i", "-",
		"-f", "audiotoolbox",
		"-audio_device_index", deviceOut,
		"-",
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("audio_io: speaker stdin pipe: %w", err)
	}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("audio_io: запуск ffmpeg-audiotoolbox: %w", err)
	}
	return &speakerWriter{cmd: cmd, cancel: cancel, stdin: stdin}, nil
}

// Write пробрасывает PCM s16le в stdin процесса. При ошибке (playback-ffmpeg
// упал — невалидный audio_device_index, устройство пропало) запоминает первый
// error для LastError(): mixerDrain в agent'е молча проглатывает Write-ошибку,
// иначе пользователь видит «работающий» TUI без звука и без причины (review M-4).
func (s *speakerWriter) Write(p []byte) (int, error) {
	n, err := s.stdin.Write(p)
	if err != nil {
		s.errOnce.Do(func() { s.firstErr = err })
	}
	return n, err
}

// LastError возвращает первый error от ffmpeg-stdin (nil если не было).
// interactive.Run логирует его после agent.Run (review M-4).
func (s *speakerWriter) LastError() error { return s.firstErr }

// Close — idempotent. Close stdin → ffmpeg финиширует → Wait.
func (s *speakerWriter) Close() error {
	var waitErr error
	s.closeOnce.Do(func() {
		if err := s.stdin.Close(); err != nil {
			s.cancel()
			waitErr = err
		}
		werr := s.waitAndGetErr()
		if waitErr == nil {
			waitErr = werr
		}
	})
	return waitErr
}

func (s *speakerWriter) waitAndGetErr() error {
	s.waitOnce.Do(func() { s.waitErr = s.cmd.Wait() })
	return s.waitErr
}
