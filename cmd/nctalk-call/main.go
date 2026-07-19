// Command nctalk-call — точка входа режима «агент/pipe» для аудио-звонков
// Nextcloud Talk. Заглушка: реальная связка слоёв в последующих задачах
// (спека 2026-07-19 §4). Не импортирует ничего из internal/call/*.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "nctalk-call: not implemented")
	os.Exit(1)
}
