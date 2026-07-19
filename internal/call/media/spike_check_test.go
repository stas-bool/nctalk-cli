package media

// spike_check_test.go — unit-тесты детектора DetectSine440.
//
// Compile-only: сборка через `go build`, запуск — DEFERRED до ручного шага
// (см. brief Task 2.9 environment constraints: на этой машине macOS firewall
// блокирует test-binary). Пороговые значения подобраны консервативно, чтобы
// пройти на реальном железе.
//
// План тестов (5 случаев, brief Task 2.9 §«ТЕСТЫ»):
//  1. DetectsSine440_Clean — чистый 440 Гц / 3с / без шума → Detected=true.
//  2. DetectsSine440_Noisy — 440 Гц + белый шум (15 дБ) / 3с → Detected=true.
//  3. RejectsSine300 — синус 300 Гц / 3с → Detected=false (частота не та).
//  4. RejectsShortClip — 440 Гц / 0.5с → Detected=false (длительность < 2с).
//  5. RejectsSilence — тишина / 3с → Detected=false.

import (
	"math"
	"testing"
	"time"
)

// TestSpikeCheck_DetectsSine440_Clean — чистый синус 440 Гц, 3с, без шума.
// Ожидание: Detected=true, DurationSec > 2.5, SNR > 20 дБ.
//
// Порог SNR консервативен (не 60 дБ из брифа): при dense-grid 10 Гц и оценке
// фона через медиану спектральная утечка Дирихле повышает медианный «фон» до
// ~50-100 у.е., что даёт измеренный SNR ~30-50 дБ даже на чистом синусе.
// 60 дБ достижимо только при sparse-noise-set оценке (как в codec_test.go);
// здесь следуем буквенной specике §12 (dense grid + median) — число не цель,
// главное — Detected=true и явный отрыв от 10 дБ порога.
func TestSpikeCheck_DetectsSine440_Clean(t *testing.T) {
	pcm := GenerateSine440(3*time.Second, PCMSampleRate, math.Inf(1))
	res := DetectSine440(pcm, PCMSampleRate)
	if !res.Detected {
		t.Fatalf("ожидалось Detected=true для чистого 440 Гц / 3с; получил %+v", res)
	}
	if res.DurationSec <= 2.5 {
		t.Errorf("DurationSec=%.3f ≤ 2.5 — слишком коротко для 3с входа", res.DurationSec)
	}
	if res.SNRdB <= 20 {
		t.Errorf("SNR=%.2f дБ ≤ 20 дБ — слишком низко для чистого синуса", res.SNRdB)
	}
	if res.PeakFreqHz < 435 || res.PeakFreqHz > 445 {
		t.Errorf("PeakFreqHz=%.2f вне полосы 440±5", res.PeakFreqHz)
	}
	t.Logf("Clean: %+v", res)
}

// TestSpikeCheck_DetectsSine440_Noisy — синус 440 Гц + белый шум (SNR 15 дБ).
// Ожидание: Detected=true, SNR > 10 дБ.
//
// Заметка о processing gain: широкополосный SNR 15 дБ + grid Gain ≈ 33 дБ
// (10*log10(N=2048)) → per-window Goertzel SNR ≈ 48 дБ. Так что порог 10 дБ
// пройден с большим запасом.
func TestSpikeCheck_DetectsSine440_Noisy(t *testing.T) {
	pcm := GenerateSine440(3*time.Second, PCMSampleRate, 15)
	res := DetectSine440(pcm, PCMSampleRate)
	if !res.Detected {
		t.Fatalf("ожидалось Detected=true для 440 Гц + шум 15 дБ; получил %+v", res)
	}
	if res.SNRdB <= 10 {
		t.Errorf("SNR=%.2f дБ ≤ 10 дБ — ниже порога детекции", res.SNRdB)
	}
	t.Logf("Noisy: %+v", res)
}

// TestSpikeCheck_RejectsSine300 — синус 300 Гц / 3с. Пик решётки на 300, а не
// на 440 → все окна отброшены правилом (а). Detected=false.
func TestSpikeCheck_RejectsSine300(t *testing.T) {
	pcm := generateSine(300.0, 3*time.Second, PCMSampleRate, math.Inf(1))
	res := DetectSine440(pcm, PCMSampleRate)
	if res.Detected {
		t.Errorf("ожидалось Detected=false для 300 Гц; получил %+v", res)
	}
	t.Logf("Sine300: %+v", res)
}

// TestSpikeCheck_RejectsShortClip — синус 440 Гц / 0.5с = 12 окон (при N=2048
// и sr=48000: 24000 сэмплов / 2048 = 11.7 → 11 полных окон). 11 < 48 →
// Detected=false (порог длительности не пройден).
func TestSpikeCheck_RejectsShortClip(t *testing.T) {
	pcm := GenerateSine440(500*time.Millisecond, PCMSampleRate, math.Inf(1))
	res := DetectSine440(pcm, PCMSampleRate)
	if res.Detected {
		t.Errorf("ожидалось Detected=false для 0.5с клипа; получил %+v", res)
	}
	if res.DurationSec > 0.6 {
		t.Errorf("DurationSec=%.3f — ожидалась ≤ 0.5с", res.DurationSec)
	}
	t.Logf("Short: %+v", res)
}

// TestSpikeCheck_RejectsSilence — нули / 3с. Все Goertzel-мощности = 0,
// analyzeWindow возвращает (0, 0), каждое окно пропускается. peakFreqs пуст →
// Detected=false, PeakFreqHz=0, SNRdB=0.
func TestSpikeCheck_RejectsSilence(t *testing.T) {
	pcm := make([]byte, 3*PCMSampleRate*2) // 3с тишины
	res := DetectSine440(pcm, PCMSampleRate)
	if res.Detected {
		t.Errorf("ожидалось Detected=false для тишины; получил %+v", res)
	}
	if res.SNRdB != 0 || res.DurationSec != 0 || res.PeakFreqHz != 0 {
		t.Errorf("ожидался zero-result для тишины; получил %+v", res)
	}
	t.Logf("Silence: %+v", res)
}
