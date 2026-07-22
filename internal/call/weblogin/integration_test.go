//go:build integration

// integration_test.go — реальная проверка weblogin.Login на Docker Talk сервере.
// state-файл (2026-07-19): «обязательно проверять каждый шаг на Docker, НЕ
// compile-only — unit-тесты на httptest не покрывают реальный Spreed API contract».
//
// Запуск:
//
//	NCTALK_INTEGRATION_CALL=1 \
//	NEXTCLOUD_URL=http://localhost:8484 NEXTCLOUD_LOGIN=admin NEXTCLOUD_PASS=adminpass \
//	CGO_ENABLED=0 go test -tags=integration -run=TestIntegration ./internal/call/weblogin/ -count=1 -v
package weblogin_test

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/stas/nctalk/internal/call/weblogin"
	"github.com/stas/nctalk/internal/transport"
)

// TestIntegration_Login_Docker проверяет, что weblogin.Login реально получает
// PHP-session на Talk-сервере: (1) Login с правильными кредами → nil; (2) session
// валидна — GET /ocs/v2.php/cloud/user через тот же cookiejar (без Basic-auth)
// возвращает залогиненного user (маркер, что $_SESSION установлена).
func TestIntegration_Login_Docker(t *testing.T) {
	if os.Getenv("NCTALK_INTEGRATION_CALL") != "1" {
		t.Skip("requires NCTALK_INTEGRATION_CALL=1 + Docker Talk server")
	}
	base := os.Getenv("NEXTCLOUD_URL")
	login := os.Getenv("NEXTCLOUD_LOGIN")
	pass := os.Getenv("NEXTCLOUD_PASS")
	if base == "" || login == "" || pass == "" {
		t.Skip("requires NEXTCLOUD_URL/LOGIN/PASS")
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse base %q: %v", base, err)
	}

	// 1. weblogin.Login (loginClient: no-redirect + cookiejar).
	loginClient := noRedirectClient(t)
	auth := transport.Auth{BaseURL: baseURL, Login: login, Password: pass}
	if err := weblogin.Login(context.Background(), loginClient, auth); err != nil {
		t.Fatalf("weblogin.Login: %v (ожидался успех на Docker)", err)
	}

	// 2. Session работает: follow-client с тем же jar, БЕЗ Basic-auth. Если session
	// валидна — /cloud/user вернёт залогиненного user. Без session → guest/401.
	follow := &http.Client{Jar: loginClient.Jar}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		strings.TrimRight(base, "/")+"/ocs/v2.php/cloud/user", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("OCS-APIRequest", "true")
	req.Header.Set("Accept", "application/json")
	resp, err := follow.Do(req)
	if err != nil {
		t.Fatalf("cloud/user GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	bs := string(body)
	if !strings.Contains(bs, login) {
		snippet := bs
		if len(snippet) > 300 {
			snippet = snippet[:300]
		}
		t.Errorf("cloud/user не содержит залогиненного user %q — session не установлена. body: %s", login, snippet)
	}
}
