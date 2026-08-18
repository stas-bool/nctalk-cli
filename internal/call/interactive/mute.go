// internal/call/interactive/mute.go — wrapper media.AudioSource для локального mute.
// Спека 2026-07-21 §4.3, §7. Review findings #3 (scope-squeeze: mute только свой),
// #4 (Close ownership: interactive НЕ закрывает mic, agent делает это через encoder.Close).
package interactive

import (
	"sync/atomic"
	"time"

	"github.com/stas-bool/nctalk-cli/internal/call/media"
)

// muteSource — перехватывает media.AudioSource. При muted — крутит inner.ReadSample
// и ДРОПАЕТ payload (не возвращает), => encodeLoop не зовёт WriteSample => pion
// перестаёт слать RTP. Ровно семантика мьюта базовой спеки §6 «перестать WriteSample».
//
// РЕАЛИЗУЕТ ПОЛНЫЙ media.AudioSource (ReadSample + Close) — finding #4 (compile gap):
// muteSource подставляется в agent.Config.AudioIn, тип которого — media.AudioSource.
//
// Mute других участников — НЕ входит в MVP (scope-squeeze, review #3): в call-слое
// нет источника мьют-статуса других участников (InCall-flags не дают mute-state,
// signaling mute/unmute events не верифицированы → future §13).
type muteSource struct {
	inner media.AudioSource // = *MicSource в прод; *fakeSource в тестах
	muted atomic.Bool       // крутит TUI-хоткеем M
}

// ReadSample — если muted, drain inner (не блокируем устройство) и loop без возврата.
// Если unmuted — пропускает payload. Ошибки inner проходят наружу (io.EOF, ctx.Cancel).
func (m *muteSource) ReadSample() ([]byte, time.Duration, error) {
	for {
		p, d, err := m.inner.ReadSample()
		if err != nil {
			return nil, 0, err
		}
		if !m.muted.Load() {
			return p, d, nil
		}
		// muted: payload дропнут, крутим дальше. Устройство НЕ блокируется
		// (inner.ReadSample потребляет capture-буфер ffmpeg).
	}
}

// Close делегирует в inner.Close (MicSource). Agent.Run зовёт encoder.Close()
// (encoder = cfg.AudioIn = muteSource), а muteSource.Close → MicSource.Close.
// Idempotent (MicSource.Close под sync.Once, Task 4.6).
func (m *muteSource) Close() error {
	return m.inner.Close()
}

// Mute включает mute — ReadSample начинает дропать payload.
func (m *muteSource) Mute() { m.muted.Store(true) }

// Unmute выключает mute.
func (m *muteSource) Unmute() { m.muted.Store(false) }

// IsMuted — текущее состояние (для View).
func (m *muteSource) IsMuted() bool { return m.muted.Load() }

// Toggle — переключает mute и возвращает новое состояние.
func (m *muteSource) Toggle() bool {
	for {
		old := m.muted.Load()
		newVal := !old
		if m.muted.CompareAndSwap(old, newVal) {
			return newVal
		}
	}
}

// compile-time: muteSource реализует полный media.AudioSource.
var _ media.AudioSource = (*muteSource)(nil)
