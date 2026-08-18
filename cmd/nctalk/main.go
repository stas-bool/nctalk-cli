// Command nctalk — CLI для Nextcloud Talk.
package main

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/stas-bool/nctalk-cli/internal/cli"
	"github.com/stas-bool/nctalk-cli/internal/client"
	"github.com/stas-bool/nctalk-cli/internal/config"
)

// main — точка входа. Делегирует в run и оборачивает результат в os.Exit.
// Вся логика вынесена в run, чтобы её можно было тестировать без os.Exit
// (os.Exit в тестах убивает goroutine теста и не даёт проверить вывод —
// план Task 5.1, спека §5).
func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Stdin))
}

// run — связка help → config → client → cli (спека 2026-08-12 §5).
//
// help перехватывается СТРОГО ДО config.Load: nctalk --help / nctalk help ... /
// nctalk <cmd> --help / nctalk (no-args) работают без NEXTCLOUD_* и не создают
// клиент. Deps.Client при help-вызове не нужен (HandleHelp его не трогает).
//
// Возвращает exit-код для os.Exit. Ошибки config.Load печатаются в stderr одной
// строкой с префиксом "nctalk:" (без значений env-переменных — см. config.Load).
func run(args []string, stdout, stderr io.Writer, stdin io.Reader) int {
	// Спека §5: HandleHelp решает «help-запрос / no-args / обычный flow».
	// При handled=true — выход с кодом help; config.Load/client/cli.Run не идут.
	if handled, code := cli.HandleHelp(args, cli.Deps{
		Stdout: stdout,
		Stderr: stderr,
	}); handled {
		return code
	}

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
