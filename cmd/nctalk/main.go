// Command nctalk — CLI для Nextcloud Talk.
package main

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/stas/nctalk/internal/cli"
	"github.com/stas/nctalk/internal/client"
	"github.com/stas/nctalk/internal/config"
)

// main — точка входа. Делегирует в run и оборачивает результат в os.Exit.
// Вся логика вынесена в run, чтобы её можно было тестировать без os.Exit
// (os.Exit в тестах убивает goroutine теста и не даёт проверить вывод —
// план Task 5.1, спека §5).
func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Stdin))
}

// run — связка config → client → cli (план Task 5.1, спека §5/§9).
//
// Возвращает exit-код для os.Exit. Ошибки config.Load печатаются в stderr
// одной строкой с префиксом "nctalk:". Сообщения config.Load содержат только
// ИМЕНА env-переменных, но не их значения — поэтому вывод безопасен (креды не
// утекают). Все сетевые/OCS-ошибки приходят из client-слоя уже sanitized
// (без URL userinfo/query, без заголовка Authorization) — см. client.sanitizeErr.
//
// args/stdout/stderr/stdin передаются параметрами (а не берутся из os.* прямо
// в теле) — это позволяет e2e-тестам в main_test.go подменять потоки на
// *bytes.Buffer и подавать любые аргументы CLI, не трогая реальные os.Stdout.
func run(args []string, stdout, stderr io.Writer, stdin io.Reader) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, "nctalk: "+err.Error())
		return 1
	}
	c := client.NewTalkClient(cfg)
	return cli.Run(args, cli.Deps{
		Client: c,
		Stdout: stdout,
		Stderr: stderr,
		Stdin:  stdin,
		Now:    time.Now,
	})
}
