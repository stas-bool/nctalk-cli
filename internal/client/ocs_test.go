package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ocs_test.go — тесты типа OCSError и его использования в doOCS.
//
// Контракт (спека §7/§9): любой OCS meta.statusCode >= 400 должен
// возвращаться из doOCS как *OCSError с заполненным Code; вышележащий
// cli-слой по Code различает 404 (NotFound → exit 2) и прочие (exit 1).
// Раньше doOCS возвращал errors.New("client: "+msg), и код терялся —
// поэтому e2e на боевом сервере давал exit 1 вместо 2 на OCS 404.

// TestOCSError_Error — метод Error формирует текст по правилу:
//   - непустое Message → «client: <Message>»;
//   - пустое Message → «client: OCS statusCode=<Code>»;
//   - nil-приёмник → безопасная строка без паники (структурная защита).
func TestOCSError_Error(t *testing.T) {
	cases := []struct {
		name string
		err  *OCSError
		want string
	}{
		{
			name: "code+message",
			err:  &OCSError{Code: 404, Message: "room not found"},
			want: "client: room not found",
		},
		{
			name: "пустое message — код в канонической форме",
			err:  &OCSError{Code: 404, Message: ""},
			want: "client: OCS statusCode=404",
		},
		{
			name: "auth 401 с message",
			err:  &OCSError{Code: http.StatusUnauthorized, Message: "bad credentials"},
			want: "client: bad credentials",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Errorf("Error(): got %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("nil-приёмник не паникует", func(t *testing.T) {
		var e *OCSError
		// Должен вернуть какую-то строку, а не паниковать.
		got := e.Error()
		if got == "" {
			t.Errorf("Error() на nil-приёмнике: пустая строка, want непустая")
		}
	})
}

// TestOCSError_ErrorsAs — *OCSError извлекается из ошибок через errors.As
// (в т.ч. через обёртку fmt.Errorf("…: %w", ocsErr)) — это и есть тот самый
// механизм, по которому cli.exitFromClientErr различает 404 и прочие.
func TestOCSError_ErrorsAs(t *testing.T) {
	src := &OCSError{Code: 404, Message: "room not found"}

	t.Run("прямой *OCSError", func(t *testing.T) {
		var oe *OCSError
		if !errors.As(src, &oe) {
			t.Fatalf("errors.As: не извлёк *OCSError из самого себя")
		}
		if oe.Code != 404 || oe.Message != "room not found" {
			t.Errorf("извлечённый OCSError = %+v, want {Code:404 Message:%q}", oe, "room not found")
		}
	})

	t.Run("оборачнутый через fmt.Errorf %w", func(t *testing.T) {
		wrapped := fmt.Errorf("GetChat: %w", src)
		var oe *OCSError
		if !errors.As(wrapped, &oe) {
			t.Fatalf("errors.As: не прошел по цепочке Unwrap")
		}
		if oe.Code != 404 {
			t.Errorf("Code: got %d, want 404", oe.Code)
		}
	})
}

// newOCSServer — httptest-сервер, отвечающий OCS-конвертом с указанным
// statusCode/message. Используется в тестах doOCS → *OCSError маппинга.
func newOCSServer(t *testing.T, statusCode int, msg string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(ocsBody(t, statusCode, msg, nil))
	}))
}

// TestDoOCS_ReturnsTypedOCSError — главный регрессионный тест баги e2e:
// при meta.statusCode>=400 doOCS обязан возвращать именно *OCSError (с Code),
// а не errors.New — иначе cli.exitFromClientErr не сможет различить 404 и 401.
//
// Воспроизводим сценарий из баги: httptest-сервер отдаёт OCS 404 с message,
// и проверяем, что errors.As(*OCSError) извлекает Code=404 из ошибки.
func TestDoOCS_ReturnsTypedOCSError(t *testing.T) {
	ts := newOCSServer(t, 404, "room not found")
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	var out []Message
	_, err := c.doOCS(context.Background(), http.MethodGet, pathRooms, nil, nil, false, &out)
	if err == nil {
		t.Fatal("err = nil, want *OCSError")
	}
	var oe *OCSError
	if !errors.As(err, &oe) {
		t.Fatalf("errors.As(*OCSError): не извлечён; err имеет тип %T (%v)", err, err)
	}
	if oe.Code != 404 {
		t.Errorf("OCSError.Code: got %d, want 404", oe.Code)
	}
	if !strings.Contains(oe.Error(), "room not found") {
		t.Errorf("Error(): got %q, want содержит 'room not found'", oe.Error())
	}
}

// TestDoOCS_OCSError_401_NotConfusedWith404 — регресс: 401 (auth) должен
// возвращаться как *OCSError{Code:401}, и его Code НЕ должен случайно
// совпадать с 404 (иначе cli-маппинг дал бы exit 2 вместо 1 на 401).
func TestDoOCS_OCSError_401_NotConfusedWith404(t *testing.T) {
	ts := newOCSServer(t, 401, "bad credentials")
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	var out []Message
	_, err := c.doOCS(context.Background(), http.MethodGet, pathRooms, nil, nil, false, &out)
	if err == nil {
		t.Fatal("err = nil, want *OCSError")
	}
	var oe *OCSError
	if !errors.As(err, &oe) {
		t.Fatalf("errors.As(*OCSError): не извлечён; err = %T (%v)", err, err)
	}
	if oe.Code != 401 {
		t.Errorf("OCSError.Code: got %d, want 401 (НЕ 404)", oe.Code)
	}
}

// TestDoOCS_HTTP404_with_OCS998_NormalizedTo404 — воспроизводит реальный кейс
// боевого сервера Nextcloud: HTTP 404 + OCS meta.statusCode=998 («Invalid query»)
// при запросе chat/{token}, где token содержит кириллицу (не подпадает под маршрут).
// Пользователь видит «комната не найдена» → cli обязан дать exit 2 (NotFound),
// поэтому doOCS нормализует Code до 404 когда HTTP-статус 404, даже если OCS
// meta.statusCode говорит 998.
func TestDoOCS_HTTP404_with_OCS998_NormalizedTo404(t *testing.T) {
	// Сервер отвечает HTTP 404, в теле — OCS-конверт с meta.statusCode=998.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write(ocsBody(t, 998, "Invalid query, please check the syntax. API specifications are here: http://www.freedesktop.org/wiki/Specifications/open-collaboration-services.\n", nil))
	}))
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	var out []Message
	_, err := c.doOCS(context.Background(), http.MethodGet, pathRooms, nil, nil, false, &out)
	if err == nil {
		t.Fatal("err = nil, want *OCSError")
	}
	var oe *OCSError
	if !errors.As(err, &oe) {
		t.Fatalf("errors.As(*OCSError): не извлечён; err = %T (%v)", err, err)
	}
	if oe.Code != http.StatusNotFound {
		t.Errorf("OCSError.Code: got %d, want 404 (HTTP 404 нормализует OCS 998 → 404)", oe.Code)
	}
}

// TestDoOCS_HTTP404_NonOCSBody_TypedAsNotFound — защита: если на HTTP 404 сервер
// (или reverse-proxy) отдал HTML вместо OCS-конверта, decode падает, но мы всё
// равно трактуем это как NotFound и возвращаем *OCSError{Code:404} — чтобы
// cli-маппинг дал exit 2, а не generic exit 1.
func TestDoOCS_HTTP404_NonOCSBody_TypedAsNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("<html><body>404 not found</body></html>"))
	}))
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	var out []Message
	_, err := c.doOCS(context.Background(), http.MethodGet, pathRooms, nil, nil, false, &out)
	if err == nil {
		t.Fatal("err = nil, want *OCSError")
	}
	var oe *OCSError
	if !errors.As(err, &oe) {
		t.Fatalf("errors.As(*OCSError): не извлечён; err = %T (%v)", err, err)
	}
	if oe.Code != http.StatusNotFound {
		t.Errorf("OCSError.Code: got %d, want 404 (HTTP 404 + не-OCS тело → NotFound)", oe.Code)
	}
}

// TestDoOCS_HTTP500_DecodeFailure_StaysGeneric — регресс: HTTP 500 + не-OCS
// тело (HTML от reverse-proxy) не должно превращаться в OCSError{Code:404}
// из-за новой логики; остаётся обычная transport-подобная ошибка → cli даёт
// exit 1 (Generic). Защита от слишком широкой трактовки 404-нормализации.
func TestDoOCS_HTTP500_DecodeFailure_StaysGeneric(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("<html><body>500 server error</body></html>"))
	}))
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	var out []Message
	_, err := c.doOCS(context.Background(), http.MethodGet, pathRooms, nil, nil, false, &out)
	if err == nil {
		t.Fatal("err = nil, want error")
	}
	// Не должно быть *OCSError с Code=404 — это generic-ошибка.
	var oe *OCSError
	if errors.As(err, &oe) && oe.Code == http.StatusNotFound {
		t.Errorf("HTTP 500 не должен классифицироваться как NotFound; got *OCSError{Code:404}")
	}
}
