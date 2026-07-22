package client

import (
	"net/http"

	"github.com/stas/nctalk/internal/transport"
)

// redact.go — thin re-exports после переезда санитайз-функций в internal/transport
// (спека 2026-07-19 §3). Сохранены как локальные обёртки над transport.* для
// минимизации правок: существующие вызовы внутри пакета (client.go, chat.go) и
// тесты (client_test.go) используют эти имена без изменений. Ведут себя
// идентично transport-оригиналам (делегируют напрямую).
//
// БЕЗОПАСНОСТЬ: см. контракт в transport.go — креды не попадают в тексты ошибок.

// SanitizeURL делегирует в transport.SanitizeURL.
func SanitizeURL(s string) string { return transport.SanitizeURL(s) }

// sanitizeErr делегирует в transport.SanitizeErr.
func sanitizeErr(err error) error { return transport.SanitizeErr(err) }

// sameHostRedirectPolicy делегирует в transport.SameHostRedirectPolicy.
// Сохранена как lower-case wrapper, т.к. на неё ссылается client.NewTalkClient
// (CheckRedirect) и существующие тесты политики редиректов в client_test.go.
func sameHostRedirectPolicy(req *http.Request, via []*http.Request) error {
	return transport.SameHostRedirectPolicy(req, via)
}
