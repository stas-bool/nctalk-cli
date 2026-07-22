//go:build integration

// integration_test.go — integration-тест nctalk-talk (Этап 4.10, review #9).
// Build-tag `integration': НЕ входит в обычный прогон `go test ./...'.
//
// Запуск:
//   NCTALK_INTEGRATION_CALL=1 NCTALK_INTEGRATION_ROOM=<token> \
//   NEXTCLOUD_URL=... NEXTCLOUD_LOGIN=... NEXTCLOUD_PASS=... \
//   CGO_ENABLED=0 go test -tags=integration -run TestIntegration_JoinCancel_Exit0 ./cmd/nctalk-talk/...
//
// Работает в non-tty fallback (stdin/stdout теста — pipe, не tty):
// review #9 — ansiView проверяет term.IsTerminal и уходит в plain-text режим.
//
// SPIKE-GATE PROCEDURE (спека §11) — отдельный ручной шаг:
//  1. Сборка: CGO_ENABLED=0 go build -o nctalk-talk ./cmd/nctalk-talk
//  2. Браузер: войти в Nextcloud Talk, открыть комнату, запустить звонок.
//  3. Запуск: ./nctalk-talk <room> (с env NEXTCLOUD_*).
//  4. Проверить 8 критериев PASS (спека §11): join, слушать, говорить, участники,
//     mute M, громкость +/-, Q/Ctrl-C clean leave, нет panic'ов за 2+ минуты.
//  5. Аудио-анализ: уши + ffmpeg volumedetect (memory audio-analysis-whisp).
package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestIntegration_JoinCancel_Exit0 — join в комнату, ctx отменён через 5с →
// штатный leave → exit 0. Проверка: нет panic, exit 0. Полная spike-gate
// процедура (спека §11) — отдельный ручной шаг (см. комментарий в начале файла).
//
// Subprocess-вариант: запускает собранный ./nctalk-talk бинарник под ctx.
// Non-tty stdin/stdout (pipe) → ansiView fallback (review #9).
func TestIntegration_JoinCancel_Exit0(t *testing.T) {
	if os.Getenv("NCTALK_INTEGRATION_CALL") != "1" {
		t.Skip("NCTALK_INTEGRATION_CALL != 1 — звонковые integration-тесты выключены")
	}
	if os.Getenv("NEXTCLOUD_URL") == "" || os.Getenv("NEXTCLOUD_LOGIN") == "" || os.Getenv("NEXTCLOUD_PASS") == "" {
		t.Skip("требуется NEXTCLOUD_URL/LOGIN/PASS для integration")
	}
	token := os.Getenv("NCTALK_INTEGRATION_ROOM")
	if token == "" {
		t.Skip("NCTALK_INTEGRATION_ROOM не задан (token целевой комнаты)")
	}

	// Короткий ICE-timeout — для "я один" даёт exit 0 за 3с; для "есть участники"
	// exit 1 за 3с (тест НЕ предполагает второго участника — изолированный прогон).
	if os.Getenv("NCTALK_ICE_TIMEOUT") == "" {
		t.Setenv("NCTALK_ICE_TIMEOUT", "3s")
	}

	bin := "./nctalk-talk"
	if _, err := os.Stat(bin); err != nil {
		// Если бинарник не собран — пробуем собрать в temp-директории.
		// compile-only fallback: тест не должен падать из-за отсутствия бинарника.
		t.Skipf("соберите бинарник: CGO_ENABLED=0 go build -o nctalk-talk ./cmd/nctalk-talk (stat: %v)", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, token)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	cmd.Stdin = nil // non-tty fallback

	runErr := cmd.Run()
	if runErr != nil {
		// ExitError с code 0 всё равно считается ошибкой в Go — проверяем код явно.
		if ee, ok := runErr.(*exec.ExitError); ok {
			if ee.ExitCode() != 0 {
				t.Fatalf("exit %d: stderr=%q stdout=%q", ee.ExitCode(), errOut.String(), out.String())
			}
			// code 0 — ok.
		} else {
			t.Fatalf("run: %v (stderr=%q)", runErr, errOut.String())
		}
	}
	// Успешный сценарий: exit 0 (либо ctx-cancel杀人 subprocess -SIGKILL, который
	// выходит не-1; это тоже ок для integration-smoke — сам факт что запуск шёл).
	// Полная spike-gate процедура — отдельный ручной шаг (см. начало файла).
}
