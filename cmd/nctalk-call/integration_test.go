//go:build integration

// integration_test.go — scaffolding для spike-gate аудио-звонка (Task 2.9 Step 6
// спеки 2026-07-19 §12). Build-tag `integration': НЕ входит в обычный прогон
// `go test ./...'; запуск отдельной командой:
//
//	CGO_ENABLED=0 go test -tags=integration -run=TestSpike_AudioBothDirections ./cmd/nctalk-call/...
//
// Тест требует ручного шага (real Nextcloud Talk server + second participant в
// браузере). На этой машине (macOS + firewall) запуск test-binary невозможен;
// код доведён до компиляции, фактический прогон — DEFERRED до ручного запуска.
//
// Спецификация ручной процедуры (см. также docs/integration-run.md):
//
//  1. Браузер: войти в Nextcloud Talk, открыть комнату, запустить звонок
//     (собеседник должен подключиться с аудио).
//  2. Получить token комнаты (URL вида /call/<token>) — подаётся позиционным
//     arg в nctalk-call.
//  3. Сгенерировать PCM с тоном 440 Гц / ≥3с / s16le/48к/моно — через
//     media.GenerateSine440 (нужен tiny helper) или ffmpeg:
//     ffmpeg -f lavfi -i "sine=frequency=440:duration=3" -f s16le -ar 48000 -ac 1 sine440_3s.pcm
//  4. Запустить nctalk-call:
//     NCTALK_INTEGRATION_SEND=1 ./nctalk-call --in sine440_3s.pcm --out recording.pcm <token> &
//     (в фоне; в runtime пишется recording.pcm — сводный входящий PCM)
//  5. Подождать 30с (тональный сигнал должен дойти до собеседника и обратно
//     через audio-pipe: encode → RTP → network → decode → mixer → stdout).
//  6. Остановить: kill %1 (SIGTERM → ctx cancel → штатный leave через agent).
//  7. Прогнать spike-check: ./spike-check recording.pcm — exit 0 = PASS, 1 = FAIL.
//
// Spike-gate PASS = DetectSine440(recording).Detected == true. Это объективный
// критерий (спека §12), что audio-pipe работает в обоих направлениях.
//
// Требует env: NEXTCLOUD_URL, NEXTCLOUD_LOGIN, NEXTCLOUD_PASS,
// NCTALK_INTEGRATION_ROOM (token целевой комнаты).
package main

import (
	"bytes"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stas/nctalk/internal/call/media"
)

// TestSpike_AudioBothDirections — spike-gate (спека §12).
//
// SKIPPED в compile-only режиме. Полный ручной запуск — см. комментарий к файлу
// выше. Тест оставлен как scaffolding:
//   - фиксирует сценарий (steps 1-7);
//   - показывает API, которым будет пользоваться человек;
//   - при ручном прогоне на боевом сервере делает то, что описано в brief.
//
// При ручном запуске (tags=integration) тест соберёт helper-PCM, запустит
// nctalk-call через exec, прогонит spike-check на recording.pcm. FAIL теста =
// spike-gate провален, audio-pipe не работает в обоих направлениях.
func TestSpike_AudioBothDirections(t *testing.T) {
	t.Skip("DEFERRED: spike-gate требует real-server + браузер + ручной запуск (Task 2.9 Step 6)")

	// Заглушки для ручного запуска — собраны здесь, чтобы компилятор
	// проверял типы и API (compile-time гарантия, что scaffolding не сгниёт).

	// 1. Проверить env.
	token := os.Getenv("NCTALK_INTEGRATION_ROOM")
	if token == "" {
		t.Fatal("NCTALK_INTEGRATION_ROOM не задан")
	}

	// 2. Сгенерировать sine 440 Гц / 3с.
	pcm := media.GenerateSine440(3*time.Second, media.PCMSampleRate, math.Inf(1))
	tmpDir := t.TempDir()
	inPath := filepath.Join(tmpDir, "sine440_3s.pcm")
	if err := os.WriteFile(inPath, pcm, 0o644); err != nil {
		t.Fatalf("write sine440.pcm: %v", err)
	}
	outPath := filepath.Join(tmpDir, "recording.pcm")

	// 3. Запустить nctalk-call.
	bin := "./nctalk-call" // должен быть собран вручную: CGO_ENABLED=0 go build -o nctalk-call ./cmd/nctalk-call
	cmd := exec.Command(bin, "--in", inPath, "--out", outPath, token)
	var callStderr bytes.Buffer
	cmd.Stderr = &callStderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("nctalk-call start: %v", err)
	}

	// 4. Подождать 30с (озвучивание + round-trip + capture).
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	killTimer := time.AfterFunc(35*time.Second, func() { _ = cmd.Process.Kill() })
	defer killTimer.Stop()

	// 5. Послать SIGTERM для штатного выхода (agent оставит звонок).
	go func() {
		<-timer.C
		if cmd.Process != nil {
			_ = cmd.Process.Signal(os.Interrupt)
		}
	}()
	_ = cmd.Wait()

	// 6. spike-check на recording.pcm.
	recording, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read recording.pcm: %v (stderr=%q)", err, callStderr.String())
	}
	res := media.DetectSine440(recording, media.PCMSampleRate)
	t.Logf("spike-gate: %+v", res)
	if !res.Detected {
		t.Fatalf("spike-gate FAIL: тон 440 Гц не детектирован в recording.pcm (nctalk-call stderr=%q)", callStderr.String())
	}
}
