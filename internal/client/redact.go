package client

import (
	"errors"
	"net/url"
)

// SanitizeURL возвращает URL в виде scheme://host[path] — БЕЗ userinfo и БЕЗ
// query. Применяется к URL, попавшим в сообщения ошибок (например *url.Error),
// чтобы не светить креды (спека §5, §9). При ошибке парсинга возвращает
// "<invalid url>".
func SanitizeURL(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		return "<invalid url>"
	}
	// Чувствительные компоненты убираем на месте.
	u.User = nil
	u.RawQuery = ""
	u.ForceQuery = false
	return u.String()
}

// sanitizeErr рекурсивно (через errors.As по цепочке Unwrap) ищет *url.Error и
// заменяет URL в нём на SanitizeURL(URL). Если ошибки типа *url.Error в цепочке
// нет — возвращает err как есть. Nil → nil.
//
// Гарантия (спека §9): в тексте возвращаемой ошибки не остаётся userinfo/query,
// даже если исходный URL содержал креды (https://user:pass@host/...).
func sanitizeErr(err error) error {
	if err == nil {
		return nil
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		ue.URL = SanitizeURL(ue.URL)
		return ue
	}
	return err
}
