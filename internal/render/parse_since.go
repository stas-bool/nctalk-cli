package render

import (
	"fmt"
	"strconv"
	"time"
)

// Отдельные layout-константы (а не голые строки в ParseSince) — для читаемости.
const (
	layoutDateOnly    = "2006-01-02"             // дата → начало дня, локальная TZ
	layoutDateTime    = "2006-01-02T15:04:05"    // дата-время без зоны → локальная TZ
	// layoutRFC3339 = time.RFC3339              // с зоной → как есть; используем константу stdlib
)

// ParseSince разбирает форматы спеки §6 «Форматы --since» и возвращает unix-секунды.
//
//	"30s"/"15m"/"2h"/"1d"          — относительные от now, локальная TZ;
//	"2026-07-17"                   — начало дня 00:00:00, локальная TZ;
//	"2026-07-17T13:00:00"          — локальная TZ;
//	"2026-07-17T13:00:00+03:00"    — RFC3339 с зоной, как есть.
//
// Порядок разбора: относительные → дата → дата-время без зоны → RFC3339 → ошибка.
// Относительные значения и дата-без-зоны интерпретируются в локальном времени
// процесса (TZ из окружения); ISO-с-зоной — как есть. При ошибке парсинга
// возвращается понятная ошибка с перечнем допустимых форматов.
func ParseSince(s string) (int64, error) {
	// 1) Относительные форматы: <N><s|m|h|d> — от реального time.Now().
	if ts, ok := ParseRelativeAt(s, time.Now()); ok {
		return ts, nil
	}
	// 2) Дата без времени → начало дня, локальная TZ.
	if t, err := time.ParseInLocation(layoutDateOnly, s, time.Local); err == nil {
		return t.Unix(), nil
	}
	// 3) Дата-время без зоны → локальная TZ.
	if t, err := time.ParseInLocation(layoutDateTime, s, time.Local); err == nil {
		return t.Unix(), nil
	}
	// 4) RFC3339 с зоной → как есть.
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Unix(), nil
	}
	// 5) Всё остальное — ошибка.
	return 0, fmt.Errorf(
		"неверный --since %q: ожидалось Ns/Nm/Nh/Nd, YYYY-MM-DD, YYYY-MM-DDTHH:MM:SS или RFC3339",
		s,
	)
}

// ParseRelativeAt разбирает относительные форматы вида "<N><суффикс>", где суффикс
// s — секунды, m — минуты, h — часы, d — 24 часа. Возвращает unix-секунды now-N
// и флаг успешного разбора; false — если строка не соответствует формату.
//
// Экспортируется, чтобы CLI-слой мог прокинуть своё deps.Now (детерминизм в
// тестах): единственная реализация набора суффиксов (s/m/h/d) — здесь, в
// render-слое; раньше в cli дублировалась копия.
func ParseRelativeAt(s string, now time.Time) (int64, bool) {
	if len(s) < 2 { // минимум "1s"
		return 0, false
	}
	suffix := s[len(s)-1]
	num := s[:len(s)-1]
	n, err := strconv.Atoi(num)
	if err != nil || n < 0 {
		return 0, false
	}
	var d time.Duration
	switch suffix {
	case 's':
		d = time.Duration(n) * time.Second
	case 'm':
		d = time.Duration(n) * time.Minute
	case 'h':
		d = time.Duration(n) * time.Hour
	case 'd':
		d = time.Duration(n) * 24 * time.Hour
	default:
		return 0, false
	}
	return now.Add(-d).Unix(), true
}
