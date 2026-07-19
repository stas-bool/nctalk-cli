package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"sync"
	"testing"
	"time"
)

// ---- helpers: генерация и анализ PCM (s16le/48к/моно) ----

// genSineS16LE генерирует синусоиду заданной частоты и длительности в формате
// s16le/моно. Используется для тестового PCM-входа в FFmpegEncoder.
func genSineS16LE(freq float64, dur time.Duration, sampleRate int) []byte {
	n := int(dur * time.Duration(sampleRate) / time.Second)
	out := make([]byte, n*2)
	for i := 0; i < n; i++ {
		// амплитуда 0.7 — ниже clipping, выше шума квантования.
		x := 0.7 * math.Sin(2*math.Pi*freq*float64(i)/float64(sampleRate))
		v := int16(x * 32767)
		binary.LittleEndian.PutUint16(out[i*2:], uint16(v))
	}
	return out
}

// decodeS16LE превращает []byte s16le в []int16. Если длина нечётная —
// последний байт игнорируется.
func decodeS16LE(b []byte) []int16 {
	n := len(b) / 2
	out := make([]int16, n)
	for i := 0; i < n; i++ {
		out[i] = int16(binary.LittleEndian.Uint16(b[i*2:]))
	}
	return out
}

// goertzelPower считает power-of-frequency для блока семплов через алгоритм
// Goertzel. Классическая реализация без нормализации — возвращает абсолютное
// значение power (пропорциональное энергии частоты freq в блоке).
//
// NB: для SNR важны только отношения power, не их абсолютные значения.
func goertzelPower(samples []int16, freq float64, sampleRate int) float64 {
	N := len(samples)
	if N == 0 {
		return 0
	}
	k := math.Round(float64(N) * freq / float64(sampleRate))
	w := 2 * math.Pi * k / float64(N)
	coeff := 2 * math.Cos(w)
	var sPrev, sPrev2 float64
	for _, x := range samples {
		xf := float64(x)
		s := xf + coeff*sPrev - sPrev2
		sPrev2 = sPrev
		sPrev = s
	}
	return sPrev2*sPrev2 + sPrev*sPrev - coeff*sPrev*sPrev2
}

// snrDB считает SNR в децибелах между power@targetFreq и average power@offFreqs.
func snrDB(target, offAvg float64) float64 {
	if offAvg <= 0 {
		return math.Inf(1)
	}
	if target <= 0 {
		return math.Inf(-1)
	}
	return 10 * math.Log10(target/offAvg)
}

// detectToneDuration режет samples на окна windowDur и считает количество
// окон, где SNR@440 > snrThresholdDB. Возвращает суммарную длительность
// таких окон.
func detectToneDuration(samples []int16, freq float64, sampleRate int, windowDur time.Duration, snrThresholdDB float64) time.Duration {
	windowSize := int(windowDur * time.Duration(sampleRate) / time.Second)
	if windowSize <= 0 || len(samples) < windowSize {
		return 0
	}
	offFreqs := []float64{200, 1000, 2000, 3000, 4000}
	var detectedWindows int
	for off := 0; off+windowSize <= len(samples); off += windowSize {
		block := samples[off : off+windowSize]
		tgt := goertzelPower(block, freq, sampleRate)
		var sumOff float64
		for _, f := range offFreqs {
			sumOff += goertzelPower(block, f, sampleRate)
		}
		avgOff := sumOff / float64(len(offFreqs))
		if snrDB(tgt, avgOff) > snrThresholdDB {
			detectedWindows++
		}
	}
	return time.Duration(detectedWindows) * windowDur
}

// findPeakFrequency ищет «пик спектра» в samples среди freqs — возвращает
// частоту с максимальной goertzelPower. Грубый, но достаточный для теста
// детектор.
func findPeakFrequency(samples []int16, freqs []float64, sampleRate int) float64 {
	bestFreq := 0.0
	bestPow := -1.0
	for _, f := range freqs {
		p := goertzelPower(samples, f, sampleRate)
		if p > bestPow {
			bestPow = p
			bestFreq = f
		}
	}
	return bestFreq
}

// ---- Тесты ----

// TestRoundTrip_PCM_Opus_PCM — критерий DoD риска R2 (framing raw Opus↔OGG +
// корректные OpusHead/comment). Сценарий:
//  1. Сгенерировать sine 440Гц, 2с, s16le/48к/моно.
//  2. PCM → FFmpegEncoder → ogg.Reader режет raw Opus → FFmpegDecoder
//     (через ogg.Writer) → PCM.
//  3. Goertzel-анализ: SNR@440 > 10дБ, длительность тона > 1.5с, пик на 440Гц.
//  4. ffmpeg-decode финишировал без ошибок.
//
// НЕ побайтно — Opus lossy.
func TestRoundTrip_PCM_Opus_PCM(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg не установлен — пропуск round-trip теста")
	}

	// 1. Генерируем тестовый PCM: sine 440Гц / 2с / s16le / 48к / моно.
	const inFreq = 440.0
	const inDur = 2 * time.Second
	inPCM := genSineS16LE(inFreq, inDur, PCMSampleRate)
	t.Logf("входной PCM: %d байт (%.2fs)", len(inPCM), float64(len(inPCM)/2)/float64(PCMSampleRate))

	// 2. Запускаем encoder и decoder.
	pcmIn := bytes.NewReader(inPCM)
	encoder, err := NewFFmpegEncoder(pcmIn)
	if err != nil {
		t.Fatalf("NewFFmpegEncoder: %v", err)
	}
	var pcmOut bytes.Buffer
	decoder, err := NewFFmpegDecoder(&pcmOut)
	if err != nil {
		_ = encoder.Close()
		t.Fatalf("NewFFmpegDecoder: %v", err)
	}

	// 3. Pump: encoder.ReadSample → decoder.WriteSample, пока io.EOF.
	pumpErrCh := make(chan error, 1)
	go func() {
		defer close(pumpErrCh)
		for {
			pkt, _, err := encoder.ReadSample()
			if err != nil {
				if errors.Is(err, io.EOF) {
					return
				}
				pumpErrCh <- err
				return
			}
			if err := decoder.WriteSample(pkt, PCMFrameDuration); err != nil {
				pumpErrCh <- fmt.Errorf("WriteSample: %w", err)
				return
			}
		}
	}()

	// 4. Ждём завершения pump, потом мягко закрываем decoder (EOS + close
	//    stdin → ffmpeg-decode финиширует). На любой сбой — таймаут 30с.
	pipelineDone := make(chan struct{})
	var pumpErr error
	go func() {
		defer close(pipelineDone)
		err, ok := <-pumpErrCh
		if ok && err != nil {
			pumpErr = err
		}
		// Закрываем encoder (он точно уже не нужен — pump вышел).
		_ = encoder.Close()
		// Закрываем decoder — это запишет EOS и дождётся ffmpeg-decode.
		_ = decoder.Close()
	}()

	select {
	case <-pipelineDone:
	case <-time.After(30 * time.Second):
		_ = encoder.Close()
		_ = decoder.Close()
		t.Fatal("pipeline не уложился в 30с timeout — зависание (возможно deadlock)")
	}

	if pumpErr != nil {
		t.Fatalf("pump error: %v", pumpErr)
	}

	outPCM := pcmOut.Bytes()
	if len(outPCM) < PCMSampleRate/2 { // хотя бы 0.5с выхода
		t.Fatalf("слишком короткий PCM-вывод: %d байт (хотя бы %d)", len(outPCM), PCMSampleRate)
	}
	t.Logf("выходной PCM: %d байт (%.2fs)", len(outPCM), float64(len(outPCM)/2)/float64(PCMSampleRate))

	samples := decodeS16LE(outPCM)

	// 5. Анализ: пик@440, SNR@440 vs off-frequencies, длительность тона.
	offFreqs := []float64{200, 1000, 2000, 3000, 4000}
	pow440 := goertzelPower(samples, inFreq, PCMSampleRate)
	var sumOff float64
	for _, f := range offFreqs {
		sumOff += goertzelPower(samples, f, PCMSampleRate)
	}
	avgOff := sumOff / float64(len(offFreqs))
	snr := snrDB(pow440, avgOff)
	t.Logf("SNR@440 vs %v: %.2f dB", offFreqs, snr)
	if snr <= 10 {
		t.Errorf("SNR %.2f дБ ≤ 10 дБ — тон 440Гц не детектирован (R2 провален)", snr)
	}

	// Пик спектра среди набора {100,200,300,400,440,500,600,1000,2000}.
	candidates := []float64{100, 200, 300, 400, 440, 500, 600, 800, 1000, 2000}
	peak := findPeakFrequency(samples, candidates, PCMSampleRate)
	t.Logf("пик спектра: %.0f Гц", peak)
	if peak != inFreq {
		t.Errorf("пик спектра %.0f Гц, ожидался 440 Гц", peak)
	}

	// Длительность детектируемого тона — 100мс окна, SNR>10 в каждом.
	const winDur = 100 * time.Millisecond
	const snrThreshold = 10.0
	toneDur := detectToneDuration(samples, inFreq, PCMSampleRate, winDur, snrThreshold)
	t.Logf("длительность тона 440Гц: %v", toneDur)
	if toneDur <= 1500*time.Millisecond {
		t.Errorf("длительность тона %v ≤ 1.5с — круглыйtrip не прошёл (R2 провален)", toneDur)
	}
}

// TestFFmpegEncoder_EmptyInput_EOF — на пустом входе ReadSample должен дать
// io.EOF без ошибок ffmpeg.
func TestFFmpegEncoder_EmptyInput_EOF(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg не установлен")
	}
	enc, err := NewFFmpegEncoder(bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("NewFFmpegEncoder: %v", err)
	}
	defer enc.Close()

	// Даём немного времени на выход ffmpeg.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("encoder не вышёл на EOF за 5с")
		default:
		}
		_, _, err := enc.ReadSample()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("неожиданная ошибка: %v", err)
		}
	}
}

// TestFFmpegEncoderDecoder_Minimal — упрощённый smoke: 100мс тишины → encode →
// decode → на выходе >0 байт. Используется как быстрый sanity-check без
// анализа спектра.
func TestFFmpegEncoderDecoder_Minimal(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg не установлен")
	}
	// 100мс тишины.
	inPCM := make([]byte, PCMSampleRate/10*2)
	enc, err := NewFFmpegEncoder(bytes.NewReader(inPCM))
	if err != nil {
		t.Fatalf("NewFFmpegEncoder: %v", err)
	}
	var out bytes.Buffer
	dec, err := NewFFmpegDecoder(&out)
	if err != nil {
		_ = enc.Close()
		t.Fatalf("NewFFmpegDecoder: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			pkt, _, err := enc.ReadSample()
			if err != nil {
				return
			}
			if err := dec.WriteSample(pkt, PCMFrameDuration); err != nil {
				return
			}
		}
	}()
	wg.Wait()
	if err := enc.Close(); err != nil {
		t.Fatalf("encoder.Close: %v", err)
	}
	if err := dec.Close(); err != nil {
		t.Fatalf("decoder.Close: %v", err)
	}
	if out.Len() == 0 {
		t.Error("PCM-вывод пуст — round-trip не сработал даже на тишине")
	}
}
