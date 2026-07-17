package config

import (
	"strings"
	"testing"
	"time"
)

// envVars — все переменные окружения, читаемые Load. Используется для
// детерминированного сброса между тест-кейсами, чтобы соседние тесты не
// влияли друг на друга через остатки окружения.
var envVars = []string{
	"NEXTCLOUD_URL",
	"NEXTCLOUD_LOGIN",
	"NEXTCLOUD_PASS",
	"NEXTCLOUD_TIMEOUT",
}

// clearEnv сбрасывает все читаемые env-переменные в пустую строку и
// регистрирует автоматическое восстановление через t.Setenv.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range envVars {
		t.Setenv(k, "")
	}
}

// TestLoad — table-driven проверка Load(): валидные наборы дают ожидаемую
// Config, невалидные — понятную ошибку, содержащую ИМЯ переменной, но не её
// значение (спека §5, §9).
func TestLoad(t *testing.T) {
	cases := []struct {
		name       string
		env        map[string]string
		wantCfg    Config
		wantErr    bool
		wantErrSub string // подстрока, обязательно присутствующая в тексте ошибки
	}{
		{
			name: "полный валидный набор",
			env: map[string]string{
				"NEXTCLOUD_URL":   "https://nc.example.com",
				"NEXTCLOUD_LOGIN": "alice",
				"NEXTCLOUD_PASS":  "secret",
			},
			wantCfg: Config{
				BaseURL:  "https://nc.example.com",
				Login:    "alice",
				Password: "secret",
				Timeout:  30 * time.Second,
			},
		},
		{
			name: "trailing slash обрезан",
			env: map[string]string{
				"NEXTCLOUD_URL":   "https://nc.example.com/",
				"NEXTCLOUD_LOGIN": "alice",
				"NEXTCLOUD_PASS":  "secret",
			},
			wantCfg: Config{
				BaseURL:  "https://nc.example.com",
				Login:    "alice",
				Password: "secret",
				Timeout:  30 * time.Second,
			},
		},
		{
			name: "двойной слэш в пути схлопнут",
			env: map[string]string{
				"NEXTCLOUD_URL":   "https://nc.example.com//ocs/",
				"NEXTCLOUD_LOGIN": "alice",
				"NEXTCLOUD_PASS":  "secret",
			},
			wantCfg: Config{
				BaseURL:  "https://nc.example.com/ocs",
				Login:    "alice",
				Password: "secret",
				Timeout:  30 * time.Second,
			},
		},
		{
			name: "URL без схемы — ошибка",
			env: map[string]string{
				"NEXTCLOUD_URL":   "nc.example.com",
				"NEXTCLOUD_LOGIN": "alice",
				"NEXTCLOUD_PASS":  "secret",
			},
			wantErr:    true,
			wantErrSub: "NEXTCLOUD_URL",
		},
		{
			name: "URL с userinfo — ошибка",
			env: map[string]string{
				"NEXTCLOUD_URL":   "https://user:pass@nc.example.com",
				"NEXTCLOUD_LOGIN": "alice",
				"NEXTCLOUD_PASS":  "secret",
			},
			wantErr:    true,
			wantErrSub: "NEXTCLOUD_URL",
		},
		{
			name: "невалидная схема (ftp) — ошибка",
			env: map[string]string{
				"NEXTCLOUD_URL":   "ftp://nc.example.com",
				"NEXTCLOUD_LOGIN": "alice",
				"NEXTCLOUD_PASS":  "secret",
			},
			wantErr:    true,
			wantErrSub: "NEXTCLOUD_URL",
		},
		{
			name: "пустой NEXTCLOUD_URL — ошибка",
			env: map[string]string{
				"NEXTCLOUD_LOGIN": "alice",
				"NEXTCLOUD_PASS":  "secret",
			},
			wantErr:    true,
			wantErrSub: "NEXTCLOUD_URL",
		},
		{
			name: "пустой NEXTCLOUD_LOGIN — ошибка",
			env: map[string]string{
				"NEXTCLOUD_URL":  "https://nc.example.com",
				"NEXTCLOUD_PASS": "secret",
			},
			wantErr:    true,
			wantErrSub: "NEXTCLOUD_LOGIN",
		},
		{
			name: "пустой NEXTCLOUD_PASS — ошибка",
			env: map[string]string{
				"NEXTCLOUD_URL":   "https://nc.example.com",
				"NEXTCLOUD_LOGIN": "alice",
			},
			wantErr:    true,
			wantErrSub: "NEXTCLOUD_PASS",
		},
		{
			name: "пустой NEXTCLOUD_TIMEOUT — дефолт 30s",
			env: map[string]string{
				"NEXTCLOUD_URL":   "https://nc.example.com",
				"NEXTCLOUD_LOGIN": "alice",
				"NEXTCLOUD_PASS":  "secret",
			},
			wantCfg: Config{
				BaseURL:  "https://nc.example.com",
				Login:    "alice",
				Password: "secret",
				Timeout:  30 * time.Second,
			},
		},
		{
			name: "кастомный NEXTCLOUD_TIMEOUT — применён",
			env: map[string]string{
				"NEXTCLOUD_URL":     "https://nc.example.com",
				"NEXTCLOUD_LOGIN":   "alice",
				"NEXTCLOUD_PASS":    "secret",
				"NEXTCLOUD_TIMEOUT": "15s",
			},
			wantCfg: Config{
				BaseURL:  "https://nc.example.com",
				Login:    "alice",
				Password: "secret",
				Timeout:  15 * time.Second,
			},
		},
		{
			name: "невалидный NEXTCLOUD_TIMEOUT — ошибка",
			env: map[string]string{
				"NEXTCLOUD_URL":     "https://nc.example.com",
				"NEXTCLOUD_LOGIN":   "alice",
				"NEXTCLOUD_PASS":    "secret",
				"NEXTCLOUD_TIMEOUT": "abc",
			},
			wantErr:    true,
			wantErrSub: "NEXTCLOUD_TIMEOUT",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}

			got, err := Load()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Load() error = nil, want ошибка; got config: %+v", got)
				}
				if tc.wantErrSub != "" && !strings.Contains(err.Error(), tc.wantErrSub) {
					t.Errorf("Load() error = %q, хотим подстроку %q", err.Error(), tc.wantErrSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() неожиданная ошибка: %v", err)
			}
			if got != tc.wantCfg {
				t.Errorf("Load() mismatch:\n got  = %+v\n want = %+v", got, tc.wantCfg)
			}
		})
	}
}

// TestLoadNoSecretLeak — redact-гарантия (спека §5, §9): значения
// NEXTCLOUD_PASS / NEXTCLOUD_LOGIN / NEXTCLOUD_URL не должны попадать в текст
// ошибок. Используем «маркеры» и провоцируем ошибки разных типов.
func TestLoadNoSecretLeak(t *testing.T) {
	const (
		passMarker  = "ZZ-PASS-MARKER-ZZ"
		loginMarker = "ZZ-LOGIN-MARKER-ZZ"
		urlMarker   = "ZZ-URL-MARKER-ZZ"
	)

	// Сценарий 1: маркеры в login/pass, ошибка провоцируется невалидным URL.
	t.Run("login/pass не утекают через ошибку URL", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("NEXTCLOUD_URL", "nc.example.com") // нет схемы → ошибка
		t.Setenv("NEXTCLOUD_LOGIN", loginMarker)
		t.Setenv("NEXTCLOUD_PASS", passMarker)

		_, err := Load()
		if err == nil {
			t.Fatal("Load() error = nil, want ошибка")
		}
		msg := err.Error()
		if strings.Contains(msg, passMarker) {
			t.Errorf("ошибка утекла значением NEXTCLOUD_PASS: %q", msg)
		}
		if strings.Contains(msg, loginMarker) {
			t.Errorf("ошибка утекла значением NEXTCLOUD_LOGIN: %q", msg)
		}
	})

	// Сценарий 2: само значение URL (маркер) не должно утекать через ошибку
	// парсинга/схемы. time.ParseDuration и url.Parse включают ввод в свои
	// сообщения — убедимся, что мы их не прокидываем.
	t.Run("значение URL не утекает через ошибку схемы", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("NEXTCLOUD_URL", urlMarker) // нет схемы → ошибка
		t.Setenv("NEXTCLOUD_LOGIN", "alice")
		t.Setenv("NEXTCLOUD_PASS", "x")

		_, err := Load()
		if err == nil {
			t.Fatal("Load() error = nil, want ошибка")
		}
		if strings.Contains(err.Error(), urlMarker) {
			t.Errorf("ошибка утекла значением NEXTCLOUD_URL: %q", err.Error())
		}
	})

	// Сценарий 3: значение NEXTCLOUD_TIMEOUT не утекает через ошибку парсинга.
	t.Run("значение TIMEOUT не утекает через ошибку парсинга", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("NEXTCLOUD_URL", "https://nc.example.com")
		t.Setenv("NEXTCLOUD_LOGIN", "alice")
		t.Setenv("NEXTCLOUD_PASS", "x")
		t.Setenv("NEXTCLOUD_TIMEOUT", urlMarker) // невалидная duration

		_, err := Load()
		if err == nil {
			t.Fatal("Load() error = nil, want ошибка")
		}
		if strings.Contains(err.Error(), urlMarker) {
			t.Errorf("ошибка утекла значением NEXTCLOUD_TIMEOUT: %q", err.Error())
		}
	})
}

// TestDefaultTimeout — константа дефолтного таймаута совпадает со спекой §5 (30s).
func TestDefaultTimeout(t *testing.T) {
	if DefaultTimeout != 30*time.Second {
		t.Errorf("DefaultTimeout = %v, want 30s", DefaultTimeout)
	}
}
