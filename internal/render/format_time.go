package render

import "time"

// FormatTime форматирует unix-секунды (ts) в локальной TZ процесса как
// "2006-01-02 15:04:05" (спека §6: вывод времени — локальный).
// Для ts==0 (сообщение без timestamp) возвращается "--".
func FormatTime(ts int64) string {
	if ts == 0 {
		return "--"
	}
	return time.Unix(ts, 0).Local().Format("2006-01-02 15:04:05")
}
