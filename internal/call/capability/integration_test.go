//go:build integration

// integration_test.go — реальная проверка capability.Settings на Docker Talk.
// баг #2: signaling/settings БЕЗ token (curl с token → 404, без token → 200 STUN).
// Unit-тесты на httptest фиксируют path, но не реальный контракт Spreed —
// здесь проверяем end-to-end: web-login → capability.Settings → непустой STUN.
//
// Запуск:
//
//	NCTALK_INTEGRATION_CALL=1 \
//	NEXTCLOUD_URL=http://localhost:8484 NEXTCLOUD_LOGIN=admin NEXTCLOUD_PASS=adminpass \
//	CGO_ENABLED=0 go test -tags=integration -run=TestIntegration ./internal/call/capability/ -count=1 -v
package capability_test

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"testing"

	"github.com/stas-bool/nctalk-cli/internal/call/capability"
	"github.com/stas-bool/nctalk-cli/internal/call/weblogin"
	"github.com/stas-bool/nctalk-cli/internal/transport"
)

// TestIntegration_Settings_Docker — после фикса бага #2 capability.Settings
// (path без token) возвращает STUN/TURN с реального сервера.
func TestIntegration_Settings_Docker(t *testing.T) {
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
	auth := transport.Auth{BaseURL: baseURL, Login: login, Password: pass}

	// web-login → PHP-session в shared jar (как в cmd/nctalk-call).
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	loginClient := &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	if err := weblogin.Login(context.Background(), loginClient, auth); err != nil {
		t.Fatalf("weblogin.Login: %v", err)
	}

	// capability через follow-client с тем же jar (session).
	c := capability.New(auth, &http.Client{Jar: jar})
	servers, err := c.Settings(context.Background())
	if err != nil {
		t.Fatalf("Settings: %v (баг #2: path без token должен дать 200, не ошибку)", err)
	}
	if len(servers) == 0 {
		t.Error("Settings вернул 0 ICE servers — ожидается хотя бы STUN (stun.nextcloud.com)")
	}
	t.Logf("ICE servers: %d", len(servers))
}
