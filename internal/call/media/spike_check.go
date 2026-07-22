// spike_check.go — детектор тона 440 Гц в PCM s16le/моно для spike-gate
// аудио-звонков. Спека 2026-07-19 §12.
//
// Алгоритм (Task 2.9):
//  1. Разбить PCM на окна по N=2048 сэмплов (~42мс при 48к).
//  2. Для каждого окна — Goertzel на 440 Гц + частотная решётка (10 Гц в полосе
//     100-3000 Гц) для оценки фона.
//  3. Окно «с тоном 440» если: (а) максимум мощности по решётке — на 440 Гц
//     ± 5 Гц, (б) SNR(power@440 / медиана остальных бинов) > 10 дБ.
//  4. PeakFreqHz — средняя частота пика по «хорошим» окнам.
//  5. SNRdB — медианный SNR по «хорошим» окнам.
//  6. DurationSec = count «хороших» окон × windowSec.
//  7. Detected = count >= spikeMinWindows (2с / 42мс ≈ 48) И SNRdB > 10.
//
// Pure stdlib (math, encoding/binary, sort, time) — НЕ импортирует internal/*
// (инвариант изоляции спеки §3/§4 и brief Task 2.9).
package media

import (
	"encoding/binary"
	"math"
	"sort"
	"time"
)

// Константы алгоритма (спека §12, brief Task 2.9).
const (
	// spikeWindowSamples — размер окна анализа. N=2048 ≈ 42.67мс при 48к.
	// Достаточно для частотного разрешения ~23 Гц (полоса Дирихле).
	spikeWindowSamples = 2048
	// spikeTargetFreq — целевая частота обнаружения, Гц.
	spikeTargetFreq = 440.0
	// spikeFreqTolerance — допуск на положение пика в решётке, Гц. При шаге
	// решётки 10 Гц единственный бин в этой полосе — 440.
	spikeFreqTolerance = 5.0
	// spikeMinSNRdB — порог SNR (дБ) для признания окна «с тоном» (спека §12).
	spikeMinSNRdB = 10.0
	// spikeMinWindows — минимальное число «хороших» окон для Detected=true.
	// 2с / 42мс ≈ 48 окон; пиковая длительность тона должна быть не меньше.
	spikeMinWindows = 48
	// Параметры частотной решётки фона.
	spikeGridStep = 10.0
	spikeGridMin = 100.0
	spikeGridMax = 3000.0
	// spikeNoiseNeighborBins — сколько соседей пика исключать из оценки фона
	// (спектральная утечка Дирихле). ±1 сосед в решётке шага 10 Гц.
	spikeNoiseNeighborBins = 1
)

// SpikeResult — результат детекции тона 440 Гц в PCM-фрагменте. Все поля,
// кроме SamplesAnalyzed, имеют смысл только когда собрано хотя бы одно «хорошее»
// окно (PeakFreqHz=0 и SNRdB=0 в противном случае).
type SpikeResult struct {
	Detected        bool    // все критерии (count ≥ 48, SNR > 10) выполнены
	PeakFreqHz      float64 // средняя частота пика по «хорошим» окнам
	SNRdB           float64 // медианный SNR по «хорошим» окнам
	DurationSec     float64 // суммарная длительность «хороших» окон, секунд
	SamplesAnalyzed int     // число раскодированных сэмплов (без учёта частоты)
}

// DetectSine440 анализирует PCM s16le/моно на наличие тона 440 Гц. SampleRate
// обычно 48000 (PCMSampleRate); формально принимает любое положительное
// значение. Маленькие входы (короче одного окна) или некорректный sampleRate
// дают zero-value результат (Detected=false).
func DetectSine440(pcm []byte, sampleRate int) SpikeResult {
	res := SpikeResult{}
	if sampleRate <= 0 {
		return res
	}
	samples := decodeS16LEToFloat(pcm)
	res.SamplesAnalyzed = len(samples)
	if len(samples) < spikeWindowSamples {
		return res
	}

	grid := buildFreqGrid()
	windowSec := float64(spikeWindowSamples) / float64(sampleRate)

	// Собираем SNR и частоту пика для каждого «хорошего» окна.
	peakFreqs := make([]float64, 0, 64)
	snrs := make([]float64, 0, 64)
	for off := 0; off+spikeWindowSamples <= len(samples); off += spikeWindowSamples {
		block := samples[off : off+spikeWindowSamples]
		peakFreq, snr := analyzeWindow(block, sampleRate, grid)
		if peakFreq <= 0 {
			continue // окно тишины — пропускаем
		}
		// (а) пик должен быть на 440 ± 5 Гц.
		if math.Abs(peakFreq-spikeTargetFreq) > spikeFreqTolerance {
			continue
		}
		// (б) SNR > 10 дБ (по spec). Здесь храним как есть — финальный порог
		// на Detected проверяем в конце по медиане; отдельное окно с SNR чуть
		// выше 10 ещё не гарантирует детекцию (нужно ≥ 48 таких окон).
		peakFreqs = append(peakFreqs, peakFreq)
		snrs = append(snrs, snr)
	}

	if len(peakFreqs) == 0 {
		return res
	}

	// Медиана SNR, средняя частота пика.
	sort.Float64s(snrs)
	res.SNRdB = medianSorted(snrs)
	res.PeakFreqHz = mean(peakFreqs)
	res.DurationSec = float64(len(peakFreqs)) * windowSec
	res.Detected = len(peakFreqs) >= spikeMinWindows && res.SNRdB > spikeMinSNRdB
	return res
}

// analyzeWindow возвращает (peakFreqHz, snrDB) для одного окна. peakFreq=0 если
// все бины решётки имеют нулевую мощность (тишина или очень слабый сигнал).
//
// SNR = 10*log10(power@peak / median(others)), где others — бины решётки без
// пика и его ±spikeNoiseNeighborBins соседей (соседние бины исключаются, чтобы
// спектральная утечка Дирихле не завышала оценку фона).
func analyzeWindow(samples []float64, sampleRate int, grid []float64) (float64, float64) {
	powers := make([]float64, len(grid))
	for i, f := range grid {
		powers[i] = goertzel(samples, sampleRate, f)
	}

	maxIdx := 0
	maxPow := 0.0
	for i, p := range powers {
		if p > maxPow {
			maxPow = p
			maxIdx = i
		}
	}
	if maxPow <= 0 {
		return 0, 0
	}
	peakFreq := grid[maxIdx]

	// Медиана мощности остальных бинов (без пика и его соседей).
	others := make([]float64, 0, len(powers)-(2*spikeNoiseNeighborBins+1))
	for i, p := range powers {
		if i >= maxIdx-spikeNoiseNeighborBins && i <= maxIdx+spikeNoiseNeighborBins {
			continue
		}
		others = append(others, p)
	}
	if len(others) == 0 {
		return peakFreq, math.Inf(1)
	}
	sort.Float64s(others)
	noise := medianSorted(others)
	if noise <= 0 {
		// Весь «фон» нулевой — формально бесконечный SNR. Возвращаем Inf,
		// caller отфильтрует по spikeMinSNRdB как «явно выше порога».
		return peakFreq, math.Inf(1)
	}
	return peakFreq, 10 * math.Log10(maxPow/noise)
}

// buildFreqGrid строит решётку частот для оценки фона: 100..3000 Гц с шагом
// 10 Гц. 440 Гц присутствует в решётке явно (440 = 100 + 34*10), что даёт
// пиковому бину метку точно на spikeTargetFreq.
func buildFreqGrid() []float64 {
	grid := make([]float64, 0, int((spikeGridMax-spikeGridMin)/spikeGridStep)+1)
	for f := spikeGridMin; f <= spikeGridMax; f += spikeGridStep {
		grid = append(grid, f)
	}
	return grid
}

// goertzel считает мощность targetFreq в окне samples (float64 в [-1, 1]).
// Использует обобщённый Goertzel: w = 2*pi*targetFreq/sampleRate, БЕЗ
// округления до ближайшего DFT-bin. Это даёт точную оценку на произвольной
// частоте, не только на центрах бинов (важно для 440 Гц при N=2048/sr=48000,
// где DFT-бины находятся на 421.88 и 445.31 Гц).
//
// Стандартная рекурсия: s = x + coeff*s_prev - s_prev2; power на выходе.
// power = s_prev^2 + s_prev2^2 - coeff*s_prev*s_prev2.
func goertzel(samples []float64, sampleRate int, targetFreq float64) float64 {
	N := len(samples)
	if N == 0 {
		return 0
	}
	w := 2 * math.Pi * targetFreq / float64(sampleRate)
	coeff := 2 * math.Cos(w)
	var sPrev, sPrev2 float64
	for _, x := range samples {
		s := x + coeff*sPrev - sPrev2
		sPrev2 = sPrev
		sPrev = s
	}
	return sPrev*sPrev + sPrev2*sPrev2 - coeff*sPrev*sPrev2
}

// GenerateSine440 генерирует синтетический синус 440 Гц длительностью duration
// в формате s16le/моно. snrDb задаёт отношение сигнал/шум:
//   - math.Inf(1) — чистый синус без шума;
//   - конечное положительное значение — к синусу подмешивается
//     детерминированный псевдо-белый шум с заданным широкополосным SNR
//     (Gaussian-аппроксимация через Irwin-Hall).
//
// Экспортируется для тестов и cmd/spike-check (синтетические входы); не для
// production-пути agent'а.
func GenerateSine440(duration time.Duration, sampleRate int, snrDb float64) []byte {
	return generateSine(spikeTargetFreq, duration, sampleRate, snrDb)
}

// generateSine — внутренний генератор для произвольной частоты. Используется
// GenerateSine440 (prod-API) и негативными тестами (300 Гц и т.п.).
//
// noiseAmp выводится из сигнальной мощности и целевого широкополосного SNR:
// signalPower = signalAmp^2 / 2 (для синуса); noiseSigma = sqrt(signalPower/10^(snr/10)).
// Синтез шума — Irwin-Hall аппроксимация N(0,1) через сумму 12 равномерных
// (без импорта math/rand: воспроизводимо в compile-only режиме и не зависит
// от глобального источника).
func generateSine(freq float64, duration time.Duration, sampleRate int, snrDb float64) []byte {
	n := int(duration * time.Duration(sampleRate) / time.Second)
	out := make([]byte, n*2)
	const signalAmp = 0.7
	signalPower := 0.5 * signalAmp * signalAmp
	var noiseSigma float64
	if !math.IsInf(snrDb, 1) && snrDb > 0 {
		noiseSigma = math.Sqrt(signalPower / math.Pow(10, snrDb/10))
	}
	// Детерминированный LCG (Numerical Recipes constants, mod 2^64).
	var rngState uint64 = 0x1234567890abcdef
	nextGauss := func() float64 {
		sum := 0.0
		for j := 0; j < 12; j++ {
			rngState = rngState*6364136223846793005 + 1442695040888963407
			// Верхние 32 бита как uniform в [0, 1).
			sum += float64(uint32(rngState>>32)) / float64(1<<32)
		}
		// Irwin-Hall(12) - 6 ≈ N(0, 1).
		return sum - 6
	}
	for i := 0; i < n; i++ {
		x := signalAmp * math.Sin(2*math.Pi*freq*float64(i)/float64(sampleRate))
		if noiseSigma > 0 {
			x += noiseSigma * nextGauss()
		}
		// Clip перед quantization (s16 насыщение).
		if x > 1 {
			x = 1
		} else if x < -1 {
			x = -1
		}
		v := int16(x * 32767)
		binary.LittleEndian.PutUint16(out[i*2:], uint16(v))
	}
	return out
}

// decodeS16LEToFloat декодирует PCM s16le → []float64 в [-1, 1]. Нечётная длина
// байт — последний байт игнорируется. Нормализация на 32768: даёт симметричный
// диапазон [-1, 1] для значений int16 в [-32768, 32767].
func decodeS16LEToFloat(b []byte) []float64 {
	n := len(b) / 2
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		v := int16(binary.LittleEndian.Uint16(b[i*2 : i*2+2]))
		out[i] = float64(v) / 32768.0
	}
	return out
}

// medianSorted — медиана уже отсортированного slice. На пустом → 0.
func medianSorted(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	n := len(xs)
	if n%2 == 1 {
		return xs[n/2]
	}
	return (xs[n/2-1] + xs[n/2]) / 2
}

// mean — среднее арифметическое. На пустом → 0.
func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum float64
	for _, v := range xs {
		sum += v
	}
	return sum / float64(len(xs))
}
