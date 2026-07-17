package render

import (
	"regexp"
	"testing"
	"time"
)

// TestFormatTime — table-driven проверка FormatTime (спека §6):
//   - ts==0 → "--" (сообщение без timestamp);
//   - фиксированный ts → строка в локальной TZ процесса по шаблону.
func TestFormatTime(t *testing.T) {
	const layout = "2006-01-02 15:04:05"
	cases := []struct {
		name string
		ts   int64
		want string
	}{
		{"ноль → плейсхолдер", 0, "--"},
		{"фиксированный ts → локальное время", 1700000000, time.Unix(1700000000, 0).Local().Format(layout)},
		{"другой ts", 1699999999, time.Unix(1699999999, 0).Local().Format(layout)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := FormatTime(c.ts)
			if got != c.want {
				t.Fatalf("FormatTime(%d) = %q, want %q", c.ts, got, c.want)
			}
		})
	}
}

// TestFormatTimeFormat проверяет, что для ненулевого ts строка соответствует
// шаблону "YYYY-MM-DD HH:MM:SS" (независимо от локальной TZ).
func TestFormatTimeFormat(t *testing.T) {
	got := FormatTime(1700000000)
	ok, err := regexp.MatchString(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$`, got)
	if err != nil {
		t.Fatalf("regexp compile: %v", err)
	}
	if !ok {
		t.Fatalf("FormatTime(1700000000) = %q, не соответствует формату YYYY-MM-DD HH:MM:SS", got)
	}
}
