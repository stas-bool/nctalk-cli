// internal/call/isolation_test.go
package call_test

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestCmdNctalkDoesNotDependOnPion — инвариант спеки §3: базовый бинарник
// cmd/nctalk НЕ должен транзитивно тащить github.com/pion/webrtc. Если тест
// падает — какой-то из фундаментальных пакетов (client/cli/render/transport/room)
// случайно начал импортировать internal/call/*.
func TestCmdNctalkDoesNotDependOnPion(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "./cmd/nctalk")
	// Тест запускается из internal/call/, поэтому репо-рут — на два уровня выше.
	cmd.Dir = "../.."
	// На этой машине (macOS + Go 1.21.4) `go list` падает с dyld без CGO_ENABLED=0.
	// Явно прокидываем env, чтобы тест был устойчив к способу запуска `go test`.
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("go list -deps ./cmd/nctalk: %v", err)
	}
	for _, line := range strings.Split(out.String(), "\n") {
		// Точный префикс строки импорта (с пробелом после пути модуля) — чтобы
		// избежать false-positive на сторонние модули вроде pion/webrtc-extras
		// или pion/mediadevices. `go list -deps` печатает полный путь импорта,
		// поэтому проверяем префикс "github.com/pion/webrtc/v4 " (с пробелом,
		// отделяющим путь от версии в выводе `go list -deps`).
		if strings.HasPrefix(line, "github.com/pion/webrtc/v4 ") || line == "github.com/pion/webrtc/v4" {
			t.Fatalf("cmd/nctalk косвенно зависит от %s — нарушен инвариант изоляции (спека §3)", line)
		}
	}
}
