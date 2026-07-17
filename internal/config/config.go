// Package config загружает и валидирует переменные окружения Nextcloud Talk.
//
// Контракт безопасности (спека §5, §9): сообщения об ошибках содержат только
// ИМЕНА переменных окружения, никогда их ЗНАЧЕНИЯ. Значения NEXTCLOUD_PASS,
// NEXTCLOUD_LOGIN и NEXTCLOUD_URL чувствительны и не должны утекать через
// диагностический вывод.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// Имена env-переменных (спека §5).
const (
	envURL     = "NEXTCLOUD_URL"
	envLogin   = "NEXTCLOUD_LOGIN"
	envPass    = "NEXTCLOUD_PASS"
	envTimeout = "NEXTCLOUD_TIMEOUT"
)

// DefaultTimeout — HTTP-таймаут по умолчанию (спека §5: 30s). Применяется,
// когда NEXTCLOUD_TIMEOUT не задан или пуст.
const DefaultTimeout = 30 * time.Second

// Config — нормализованная конфигурация для подключения к Nextcloud Talk.
//
// Поле Password чувствительно: оно используется ТОЛЬКО как Basic-auth в
// HTTP-запросах и никогда не должно попадать в вывод/ошибки/логи.
type Config struct {
	BaseURL  string        // нормализованный, без trailing slash и двойных слэшей в пути
	Login    string        // логин пользователя Nextcloud
	Password string        // app-password; обрабатывается как секрет
	Timeout  time.Duration // HTTP-таймаут; дефолт DefaultTimeout
}

// Load читает env-переменные, валидирует и нормализует их, возвращая готовую
// Config. Сообщения об ошибках содержат только имена переменных, но не их
// значения (см. спека §5, §9).
func Load() (Config, error) {
	var cfg Config

	rawURL := os.Getenv(envURL)
	cfg.Login = os.Getenv(envLogin)
	cfg.Password = os.Getenv(envPass)

	// Обязательные поля. Проверяем до нормализации, чтобы сообщение было
	// максимально понятным; в тексте только имя переменной.
	if rawURL == "" {
		return Config{}, fmt.Errorf("config: %s обязателен", envURL)
	}
	if cfg.Login == "" {
		return Config{}, fmt.Errorf("config: %s обязателен", envLogin)
	}
	if cfg.Password == "" {
		return Config{}, fmt.Errorf("config: %s обязателен", envPass)
	}

	// Нормализация и валидация URL (спека §5). Ошибка содержит имя переменной,
	// но не её значение.
	baseURL, err := normalizeURL(rawURL)
	if err != nil {
		return Config{}, err
	}
	cfg.BaseURL = baseURL

	// Таймаут: пусто → дефолт; иначе парсим через time.ParseDuration.
	// ВНИМАНИЕ: сообщение time.ParseDuration включает само значение в ввод —
	// поэтому НЕ прокидываем underlying error наружу, формируем свой текст.
	rawTimeout := os.Getenv(envTimeout)
	if rawTimeout == "" {
		cfg.Timeout = DefaultTimeout
	} else {
		d, perr := time.ParseDuration(rawTimeout)
		if perr != nil {
			return Config{}, fmt.Errorf("config: %s: некорректное значение (ожидалась duration, например \"30s\")", envTimeout)
		}
		cfg.Timeout = d
	}

	return cfg, nil
}

// normalizeURL парсит и нормализует NEXTCLOUD_URL:
//   - схема строго http или https (иначе ошибка — schema обязательна);
//   - host обязателен;
//   - userinfo в URL не допускается: креды передаются только через заголовок
//     Authorization, иначе они светятся в ps/логах (спека §5);
//   - подряд идущие слэши в пути схлопываются в один;
//   - trailing slash обрезается.
//
// Возвращает каноническую строку вида scheme://host[/path]. Никаких HTTP-вызовов
// не делает — это чисто лексическая валидация (canonical-форма после 301 НЕ
// перезаписывается, см. спека §5). Ошибка содержит имя переменной NEXTCLOUD_URL,
// но НЕ её значение (underlying parse-error намеренно не прокидывается —
// url.Parse включает ввод в сообщение).
func normalizeURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("config: %s: некорректный URL", envURL)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("config: %s: схема обязательна (http или https)", envURL)
	}
	if u.User != nil {
		return "", fmt.Errorf("config: %s: userinfo в URL не допускается", envURL)
	}
	if u.Host == "" {
		return "", fmt.Errorf("config: %s: host обязателен", envURL)
	}

	// Схлопываем подряд идущие слэши в пути и тримим trailing slash.
	u.Path = normalizePath(u.Path)
	// Сбрасываем RawPath, чтобы String() пересобрал его из Path без мусора.
	u.RawPath = ""

	return u.String(), nil
}

// normalizePath приводит путь к каноническому виду:
//   - несколько подряд идущих '/' схлопываются в одну;
//   - убирается trailing '/' (поэтому корневой путь становится пустой строкой,
//     и итоговый URL имеет вид scheme://host без завершающего слэша).
func normalizePath(p string) string {
	if p == "" {
		return ""
	}
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	return strings.TrimRight(p, "/")
}
