// Command spike-check — детектор тона 440 Гц в PCM-фрагменте.
//
// Читает s16le/моно PCM из файла (или stdin через `-') и прогоняет
// media.DetectSine440. Печатает результат (human-readable по умолчанию, JSON с
// --json) и выходит с кодом 0 на PASS (Detected=true) / 1 на FAIL.
//
// Применение — spike-gate аудио-звонков (Task 2.9 спеки 2026-07-19 §12):
// запись звонка → spike-check → PASS/FAIL решение. Считывание 440 Гц тона,
// прошедшего через ffmpeg (encode/decode) + pion (RTP) + network round-trip,
// означает, что audio-pipe работает в обоих направлениях.
//
// Не импортирует internal/cli, internal/render — только stdlib и
// internal/call/media (инвариант изоляции спеки §3/§4).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/stas-bool/nctalk-cli/internal/call/media"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run — отдельная функция для testability (без os.Exit). Возвращает exit-код:
// 0 — PASS (Detected=true), 1 — FAIL или ошибка ввода-вывода/парсинга флагов.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("spike-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		rate    = fs.Int("rate", media.PCMSampleRate, "частота дискретизации PCM, Гц (default 48000)")
		jsonOut = fs.Bool("json", false, "вывести результат как JSON (вместо human-readable)")
	)
	if err := fs.Parse(args); err != nil {
		return 1
	}

	// Источник PCM: позиционный arg (путь файла) или `-`/нет arg → stdin.
	var src io.Reader = stdin
	if fs.NArg() > 0 && fs.Arg(0) != "-" {
		f, err := os.Open(fs.Arg(0))
		if err != nil {
			fmt.Fprintf(stderr, "spike-check: %v\n", err)
			return 1
		}
		defer f.Close()
		src = f
	}

	pcm, err := io.ReadAll(src)
	if err != nil {
		fmt.Fprintf(stderr, "spike-check: чтение PCM: %v\n", err)
		return 1
	}

	res := media.DetectSine440(pcm, *rate)

	if *jsonOut {
		// JSON-вывод: машино-читаемый, для скриптов/CI.
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(res); err != nil {
			fmt.Fprintf(stderr, "spike-check: json encode: %v\n", err)
			return 1
		}
	} else {
		// Human-readable.
		verdict := "FAIL"
		if res.Detected {
			verdict = "PASS"
		}
		fmt.Fprintf(stdout, "spike-check: %s\n", verdict)
		fmt.Fprintf(stdout, "  detected:         %v\n", res.Detected)
		fmt.Fprintf(stdout, "  peak_freq_hz:     %.2f\n", res.PeakFreqHz)
		fmt.Fprintf(stdout, "  snr_db:           %.2f\n", res.SNRdB)
		fmt.Fprintf(stdout, "  duration_sec:     %.3f\n", res.DurationSec)
		fmt.Fprintf(stdout, "  samples_analyzed: %d\n", res.SamplesAnalyzed)
	}

	if res.Detected {
		return 0
	}
	return 1
}
