// Command nctalk-talk — точка входа режима «человек/TUI» для аудио-звонков
// Nextcloud Talk. Заглушка: реальная связка слоёв в последующих задачах
// (спека 2026-07-19 §4). Не импортирует ничего из internal/call/*.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "nctalk-talk: not implemented")
	os.Exit(1)
}
