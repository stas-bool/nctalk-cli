package weblogin

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/stas-bool/nctalk-cli/internal/transport"
)

// requestTokenRe извлекает requesttoken из HTML логин-формы Nextcloud. Формат —
// HTML-атрибут requesttoken="..." (на <body>/<input>/meta), НЕ name="requesttoken" value=.
// Подтверждено curl-спайком на Talk 20.1.11 (89-символьный base64-подобный токен).
var requestTokenRe = regexp.MustCompile(`requesttoken="([^"]+)"`)

// Login выполняет web-login flow Nextcloud (см. doc.go): GET /login → parse
// requesttoken → POST /login (form user/password/requesttoken) → проверка 303
// Location. Session-cookie сохраняется в cookiejar переданного doer (doer НЕ
// следует редиректам — CheckRedirect → http.ErrUseLastResponse).
//
// Возвращает nil на success. На неудачу (wrong creds / no requesttoken / network)
// — error с каноническим текстом БЕЗ пароля (пароль в form-body, не в тексте).
func Login(ctx context.Context, doer transport.Doer, auth transport.Auth) error {
	loginURL := strings.TrimRight(auth.BaseURL.String(), "/") + "/login"

	// 1. GET /login — получаем requesttoken + пред-session cookie в jar.
	getReq, err := http.NewRequestWithContext(ctx, http.MethodGet, loginURL, nil)
	if err != nil {
		return transport.SanitizeErr(err)
	}
	getResp, err := doer.Do(getReq)
	if err != nil {
		return transport.SanitizeErr(err)
	}
	body, readErr := io.ReadAll(getResp.Body)
	_ = getResp.Body.Close()
	if readErr != nil {
		return transport.SanitizeErr(readErr)
	}

	// 2. Парсим requesttoken из HTML.
	m := requestTokenRe.FindSubmatch(body)
	if m == nil {
		return fmt.Errorf("weblogin: requesttoken не найден в /login")
	}

	// 3. POST /login — form-encoded user/password/requesttoken. Cookiejar прикладывает
	// пред-session cookie; сервер валидирует креды и CSRF-токен.
	form := url.Values{}
	form.Set("user", auth.Login)
	form.Set("password", auth.Password)
	form.Set("requesttoken", string(m[1]))
	postReq, err := http.NewRequestWithContext(ctx, http.MethodPost, loginURL, strings.NewReader(form.Encode()))
	if err != nil {
		return transport.SanitizeErr(err)
	}
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postResp, err := doer.Do(postReq)
	if err != nil {
		return transport.SanitizeErr(err)
	}
	_ = postResp.Body.Close()

	// 4. Success = 303 Location без "direct" (canonical → /apps/dashboard;
	// провал → /login?direct=1). Session-cookie уже в jar от этого ответа.
	loc := postResp.Header.Get("Location")
	if strings.Contains(loc, "direct") {
		return fmt.Errorf("weblogin: вход не удался (проверьте NEXTCLOUD_LOGIN/NEXTCLOUD_PASS)")
	}
	if postResp.StatusCode != http.StatusSeeOther {
		return fmt.Errorf("weblogin: неожиданный статус /login: HTTP %d", postResp.StatusCode)
	}
	return nil
}
