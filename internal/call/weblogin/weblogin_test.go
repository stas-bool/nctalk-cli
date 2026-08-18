// Тесты пакета weblogin через httptest: эмуляция Nextcloud /login flow.
// Реального сервера не требуют; проверяют flow, parsing requesttoken, передачу
// кредов и success/fail по Location из 303.
package weblogin_test

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stas-bool/nctalk-cli/internal/call/weblogin"
	"github.com/stas-bool/nctalk-cli/internal/transport"
)

// noRedirectClient строит *http.Client с cookiejar и CheckRedirect, возвращающим
// ErrUseLastResponse — чтобы Login видел 303 + Location напрямую (не следовал).
func noRedirectClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	return &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// authFrom строит transport.Auth с распарсенным BaseURL httptest-сервера.
func authFrom(t *testing.T, rawURL, login, pass string) transport.Auth {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse BaseURL %q: %v", rawURL, err)
	}
	return transport.Auth{BaseURL: u, Login: login, Password: pass}
}

// TestLogin_HappyPath — canonical success: GET /login отдаёт HTML с requesttoken,
// POST /login с user/password/requesttoken → 303 Location /apps/dashboard. Login
// возвращает nil; креды и requesttoken дошли до сервера.
func TestLogin_HappyPath(t *testing.T) {
	const rt = "REQUESTTOKEN_VALUE_123"
	var gotUser, gotPass, gotRT string
	var postSeen bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/login" {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodGet {
			http.SetCookie(w, &http.Cookie{Name: "oc_sessionPassphrase", Value: "pre", Path: "/"})
			w.Header().Set("Content-Type", "text/html")
			// requesttoken как HTML-атрибут (формат Nextcloud 30, НЕ name=value).
			_, _ = w.Write([]byte(`<html><body data-requesttoken="` + rt + `"></body></html>`))
			return
		}
		// POST /login
		postSeen = true
		if err := r.ParseForm(); err != nil {
			t.Fatalf("server ParseForm: %v", err)
		}
		gotUser = r.PostFormValue("user")
		gotPass = r.PostFormValue("password")
		gotRT = r.PostFormValue("requesttoken")
		http.SetCookie(w, &http.Cookie{Name: "oc_sessionid", Value: "SESSION123", Path: "/"})
		w.Header().Set("Location", "/apps/dashboard/")
		w.WriteHeader(http.StatusSeeOther) // 303
	}))
	defer ts.Close()

	client := noRedirectClient(t)
	auth := authFrom(t, ts.URL, "admin", "adminpass")

	if err := weblogin.Login(context.Background(), client, auth); err != nil {
		t.Fatalf("Login: got %v, want nil", err)
	}
	if !postSeen {
		t.Fatal("сервер не получил POST /login")
	}
	if gotUser != "admin" {
		t.Errorf("POST user: got %q, want admin", gotUser)
	}
	if gotPass != "adminpass" {
		t.Errorf("POST password: got %q, want adminpass", gotPass)
	}
	if gotRT != rt {
		t.Errorf("POST requesttoken: got %q, want %q", gotRT, rt)
	}
}

// TestLogin_FailedRedirect — wrong creds: POST /login → 303 Location /login?direct=1.
// Login возвращает ошибку (без утечки пароля в текст).
func TestLogin_FailedRedirect(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/login" || r.Method == http.MethodGet {
			http.SetCookie(w, &http.Cookie{Name: "oc_sessionPassphrase", Value: "pre", Path: "/"})
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<html><body data-requesttoken="RT"></body></html>`))
			return
		}
		// POST → fail redirect
		w.Header().Set("Location", "/login?direct=1&user=admin")
		w.WriteHeader(http.StatusSeeOther)
	}))
	defer ts.Close()

	client := noRedirectClient(t)
	auth := authFrom(t, ts.URL, "admin", "WRONGPASS")

	err := weblogin.Login(context.Background(), client, auth)
	if err == nil {
		t.Fatal("Login: got nil, want error на direct=1 redirect")
	}
	if strings.Contains(err.Error(), "WRONGPASS") {
		t.Errorf("Login error утёк пароль: %q", err.Error())
	}
}

// TestLogin_NoRequestToken — GET /login без requesttoken в HTML → Login возвращает
// ошибку (парсинг не удался), НЕ отправляя POST с пустым токеном.
func TestLogin_NoRequestToken(t *testing.T) {
	postSeen := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			postSeen = true
		}
		if r.URL.Path != "/login" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body>no token here</body></html>`))
	}))
	defer ts.Close()

	client := noRedirectClient(t)
	auth := authFrom(t, ts.URL, "admin", "adminpass")

	err := weblogin.Login(context.Background(), client, auth)
	if err == nil {
		t.Fatal("Login: got nil, want error на отсутствии requesttoken")
	}
	if postSeen {
		t.Error("Login отправил POST без requesttoken (не должен был)")
	}
}
