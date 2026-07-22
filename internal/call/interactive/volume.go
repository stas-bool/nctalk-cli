// internal/call/interactive/volume.go — PCM gain на вывод в динамик.
// Спека 2026-07-21 §4.3, §7. Перехватывает io.Writer (PCM s16le от Mixer'а),
// масштабирует int16-семплы на gain/100 c clipping на [-32768, 32767], перед inner.Write.
package interactive

import (
	"encoding/binary"
	"io"
	"sync/atomic"
)

// volumeWriter — перехватывает io.Writer (PCM s16le на вывод). Масштабирует
// каждый int16-семпл на gain (×gain/100), с clipping на [-32768, 32767], перед inner.Write.
// Применяется к PCM ПОСЛЕ Mixer'а, перед playback-ffmpeg => влияет только на
// динамик (в сеть уходит как было).
type volumeWriter struct {
	inner io.Writer
	gain  atomic.Int32 // %, default 100; крутит TUI +/-
}

// Write масштабирует PCM s16le семплы и пишет в inner. Нечётный len(p) обрезаем
// до чётного (целое число int16) — остаток дропаем (на практике не возникает:
// Mixer выдаёт только целые кадры).
func (w *volumeWriter) Write(p []byte) (int, error) {
	gain := int(w.gain.Load())
	if gain == 100 {
		// identity — пропускаем без копирования.
		return w.inner.Write(p)
	}
	n := len(p) &^ 1 // чётное число байт
	if n == 0 {
		return 0, nil
	}
	out := make([]byte, n)
	for i := 0; i < n; i += 2 {
		s := int16(binary.LittleEndian.Uint16(p[i : i+2]))
		scaled := int32(s) * int32(gain) / 100
		switch {
		case scaled > 32767:
			scaled = 32767
		case scaled < -32768:
			scaled = -32768
		}
		binary.LittleEndian.PutUint16(out[i:i+2], uint16(int16(scaled)))
	}
	return w.inner.Write(out)
}

// SetGain устанавливает gain в процентах (clamped [0, 200]).
func (v *volumeWriter) SetGain(pct int) {
	if pct < 0 {
		pct = 0
	}
	if pct > 200 {
		pct = 200
	}
	v.gain.Store(int32(pct))
}

// Gain возвращает текущий gain в процентах.
func (v *volumeWriter) Gain() int { return int(v.gain.Load()) }

// Inc/Dec — шаг ±delta (для TUI +/-). Clamped [0, 200].
func (v *volumeWriter) Inc(delta int) { v.SetGain(v.Gain() + delta) }
func (v *volumeWriter) Dec(delta int) { v.SetGain(v.Gain() - delta) }

// compile-time: volumeWriter реализует io.Writer.
var _ io.Writer = (*volumeWriter)(nil)
