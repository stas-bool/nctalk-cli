// Package client реализует HTTP-клиент Nextcloud Talk: базовый TalkClient
// (ресурсные методы над OCS). Чистый HTTP-транспорт, OCS-конверт, sanitize URL
// и same-host redirect-политика вынесены в internal/transport (спека
// 2026-07-19 §3) — здесь только ресурсные методы и тонкая связка cfg→Auth.
//
// Контракт безопасности (спека §5, §9):
//   - креды берутся из config.Config (источник — env), никогда из аргументов;
//   - заголовок Authorization собирается transport.DoOCS на каждый запрос и
//     не кэшируется;
//   - все ошибки пропускаются через transport.SanitizeErr (см. redact.go) —
//     в тексте не должно быть userinfo, query и пароля.
//
// БЕЗОПАСНОСТЬ — ЗАПРЕЩЕНО использовать httputil.DumpRequestOut и
// httputil.DumpResponse (включая через любой debug-вывод): они печатают
// заголовки запроса/ответа целиком, вместе с Authorization. Аналогично —
// любой лог raw-запроса/ответа под запретом.
package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/stas-bool/nctalk-cli/internal/config"
	"github.com/stas-bool/nctalk-cli/internal/transport"
)

// TalkClient — HTTP-клиент Nextcloud Talk.
//
// Поля unexported: cfg (креды/таймаут — источник правды о конфигурации),
// doer (транспорт, подменяется в тестах), auth (распарсенный BaseURL + креды
// в формате, готовом для transport.DoOCS — без timeout, он выставлен на
// http.Client в NewTalkClient).
type TalkClient struct {
	cfg  config.Config
	doer transport.Doer
	auth transport.Auth
}

// NewTalkClient — production-конструктор: создаёт http.Client на базе
// http.DefaultTransport (пулы соединений, прокси из env) с таймаутом из cfg и
// same-host redirect-политикой (transport.SameHostRedirectPolicy блокирует
// cross-host auth-leak и https→http downgrade).
func NewTalkClient(cfg config.Config) *TalkClient {
	hc := &http.Client{
		Transport:     http.DefaultTransport,
		Timeout:       cfg.Timeout,
		CheckRedirect: transport.SameHostRedirectPolicy,
	}
	return NewTalkClientWithDoer(cfg, hc)
}

// NewTalkClientWithDoer — конструктор для тестов: принимает любой transport.Doer.
// BaseURL парсится один раз при конструировании; config.Load уже валидирует URL,
// поэтому ошибка парсинга здесь — баг вызывающего, падаем громко (review 8).
//
// Паника — намеренная: config.Load гарантировал валидность URL, и тихое
// игнорирование err от url.Parse скрыло бы баг. Текст паники содержит причину
// (только err, без значений кред — они в cfg, но в строку не попадают).
func NewTalkClientWithDoer(cfg config.Config, doer transport.Doer) *TalkClient {
	u, err := url.Parse(cfg.BaseURL)
	if err != nil {
		panic(fmt.Sprintf("client: невалидный BaseURL после config.Load: %v", err))
	}
	return &TalkClient{
		cfg:  cfg,
		doer: doer,
		auth: transport.Auth{BaseURL: u, Login: cfg.Login, Password: cfg.Password},
	}
}

// doOCS делегирует в transport.DoOCS — единый транспортный метод (контракт
// идентичен прежней реализации: сборка URL, Basic-auth + OCS-заголовки, разворот
// конверта, типизированная *transport.OCSError при meta.statusCode>=400).
//
// Все параметры и контракт ошибок описаны в transport.DoOCS. Возвращает
// http.Header ответа — нужен chat-пагинации (X-Chat-Last-Given).
func (c *TalkClient) doOCS(ctx context.Context, method, p string, query url.Values, body io.Reader, mutate bool, out any) (http.Header, error) {
	return transport.DoOCS(ctx, c.doer, c.auth, method, p, query, body, mutate, out)
}
