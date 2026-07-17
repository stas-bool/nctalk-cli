package render

import (
	"testing"
	"time"
)

// TestParseSince — table-driven проверка разбора форматов --since (спека §6
// «Форматы --since»). Относительные и дата-без-зоны интерпретируются в
// локальной TZ процесса; ISO-с-зоной — как есть.
func TestParseSince(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    int64
		absErr  int64 // допустимое отклонение в секундах (для относительных)
		wantErr bool
	}{
		// Относительные: now-N, проверяем с допуском (±2s недостаточно из-за
		// гонки между time.Now() в тесте и в ParseSince — берём ±5s).
		{name: "30s → now-30s", in: "30s", want: time.Now().Add(-30 * time.Second).Unix(), absErr: 5},
		{name: "15m → now-15m", in: "15m", want: time.Now().Add(-15 * time.Minute).Unix(), absErr: 5},
		{name: "2h → now-2h", in: "2h", want: time.Now().Add(-2 * time.Hour).Unix(), absErr: 5},
		{name: "1d → now-24h", in: "1d", want: time.Now().Add(-24 * time.Hour).Unix(), absErr: 5},

		// Дата без времени → начало дня 00:00:00 локальной TZ.
		{
			name: "2026-07-17 → 00:00:00 local",
			in:   "2026-07-17",
			want: time.Date(2026, time.July, 17, 0, 0, 0, 0, time.Local).Unix(),
		},
		// Дата-время без зоны → локальная TZ.
		{
			name: "2026-07-17T13:00:00 → 13:00 local",
			in:   "2026-07-17T13:00:00",
			want: time.Date(2026, time.July, 17, 13, 0, 0, 0, time.Local).Unix(),
		},
		// RFC3339 с зоной → как есть; +03:00 даёт 10:00 UTC.
		{
			name: "2026-07-17T13:00:00+03:00 → 10:00 UTC",
			in:   "2026-07-17T13:00:00+03:00",
			want: time.Date(2026, time.July, 17, 10, 0, 0, 0, time.UTC).Unix(),
		},
		// RFC3339 с Z.
		{
			name: "2026-07-17T10:00:00Z → 10:00 UTC",
			in:   "2026-07-17T10:00:00Z",
			want: time.Date(2026, time.July, 17, 10, 0, 0, 0, time.UTC).Unix(),
		},

		// Ошибки.
		{name: "abc → ошибка", in: "abc", wantErr: true},
		{name: "пустая строка → ошибка", in: "", wantErr: true},
		{name: "31x → неизвестный суффикс → ошибка", in: "31x", wantErr: true},
		{name: "неполная дата → ошибка", in: "2026-7-3", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSince(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseSince(%q): err = nil, want error", tt.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseSince(%q): неожиданная ошибка: %v", tt.in, err)
			}
			if tt.absErr > 0 {
				if diff := got - tt.want; diff < -tt.absErr || diff > tt.absErr {
					t.Errorf("ParseSince(%q): got %d, want ~%d (допуск ±%ds, отклонение %ds)",
						tt.in, got, tt.want, tt.absErr, diff)
				}
				return
			}
			if got != tt.want {
				t.Errorf("ParseSince(%q): got %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}
