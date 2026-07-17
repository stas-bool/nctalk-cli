package cli

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stas/nctalk/internal/client"
)

// TestExitError — table-driven проверка контракта ExitError: метод Error()
// возвращает текст обёрнутой ошибки (или пустую строку при nil), а Code несёт
// exit-код из спеки §7. Хелпер Exit(code, err) собирает ExitError как ожидалось.
func TestExitError(t *testing.T) {
	cases := []struct {
		name    string
		ee      ExitError
		wantMsg string
		wantCode int
	}{
		{
			name:     "OK с nil-ошибкой — пустая строка, код 0",
			ee:       ExitError{Code: ExitOK, Err: nil},
			wantMsg:  "",
			wantCode: ExitOK,
		},
		{
			name:     "общая ошибка сеть/авторизация — код 1",
			ee:       ExitError{Code: ExitGeneric, Err: errors.New("сеть недоступна")},
			wantMsg:  "сеть недоступна",
			wantCode: ExitGeneric,
		},
		{
			name:     "not found — код 2",
			ee:       ExitError{Code: ExitNotFound, Err: errors.New("комната не найдена")},
			wantMsg:  "комната не найдена",
			wantCode: ExitNotFound,
		},
		{
			name:     "ambiguous — код 3",
			ee:       ExitError{Code: ExitAmbiguous, Err: errors.New("несколько комнат подходят")},
			wantMsg:  "несколько комнат подходят",
			wantCode: ExitAmbiguous,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ee.Code; got != tc.wantCode {
				t.Errorf("Code: got %d, want %d", got, tc.wantCode)
			}
			if got := tc.ee.Error(); got != tc.wantMsg {
				t.Errorf("Error(): got %q, want %q", got, tc.wantMsg)
			}
		})
	}
}

// TestExitHelper — хелпер Exit(code, err) упаковывает аргументы в ExitError как есть.
func TestExitHelper(t *testing.T) {
	src := errors.New("boom")
	ee := Exit(ExitNotFound, src)

	if ee.Code != ExitNotFound {
		t.Errorf("Code: got %d, want %d", ee.Code, ExitNotFound)
	}
	if !errors.Is(ee, src) {
		t.Errorf("ожидалось, что ExitError оборачивает исходную ошибку; got %v", ee.Err)
	}
	if got := ee.Error(); got != "boom" {
		t.Errorf("Error(): got %q, want %q", got, "boom")
	}
}

// TestExitCodesSpecSection7 — коды совпадают со спекой §7 (0/1/2/3).
// Константа для кода 1 названа ExitGeneric (а не ExitError), т.к. ExitError —
// имя типа в этом же пакете; подробности см. в exit.go.
func TestExitCodesSpecSection7(t *testing.T) {
	if ExitOK != 0 {
		t.Errorf("ExitOK: got %d, want 0", ExitOK)
	}
	if ExitGeneric != 1 {
		t.Errorf("ExitGeneric: got %d, want 1", ExitGeneric)
	}
	if ExitNotFound != 2 {
		t.Errorf("ExitNotFound: got %d, want 2", ExitNotFound)
	}
	if ExitAmbiguous != 3 {
		t.Errorf("ExitAmbiguous: got %d, want 3", ExitAmbiguous)
	}
}

// TestExitFromClientErr — маппинг ошибки клиентского слоя в exit-код (контракт
// §7/§9). Table-driven: каждый кейс — пара (входная ошибка, ожидаемый exit-код).
//
// Покрывает:
//   - nil → ExitOK (для поисковых команд пустой результат уже обнулил ошибку);
//   - *client.OCSError{Code:404} → ExitNotFound (2) — комната/сообщение/реакция
//     не найдены (ключевой кейс e2e-баги);
//   - *client.OCSError{Code:401/403/400/500} → ExitGeneric (1) — auth/доступ/
//     невалидный ввод/сбой сервера;
//   - оборачнутый через %w *OCSError тоже ловится (errors.As проходит по цепочке
//     Unwrap) — клиенты могут добавлять контекст через fmt.Errorf("…: %w", err);
//   - сетевая ошибка без OCS-кода (например *url.Error от transport) → exit 1.
func TestExitFromClientErr(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode int
		// wantOCSCode: если не 0 — errors.As должен извлечь *client.OCSError с
		// таким Code (проверяем, что типизированная ошибка сохранена, а не
		// потеряна/переупакована при маппинге).
		wantOCSCode int
	}{
		{
			name:     "nil → ExitOK",
			err:      nil,
			wantCode: ExitOK,
		},
		{
			name:        "OCS 404 → ExitNotFound (2) — комната/сообщение/реакция не найдены",
			err:         &client.OCSError{Code: http.StatusNotFound, Message: "room not found"},
			wantCode:    ExitNotFound,
			wantOCSCode: http.StatusNotFound,
		},
		{
			name:     "OCS 401 (auth) → ExitGeneric (1)",
			err:      &client.OCSError{Code: http.StatusUnauthorized, Message: "bad credentials"},
			wantCode: ExitGeneric,
		},
		{
			name:     "OCS 403 (доступ) → ExitGeneric (1)",
			err:      &client.OCSError{Code: http.StatusForbidden, Message: "forbidden"},
			wantCode: ExitGeneric,
		},
		{
			name:     "OCS 400 (невалидный ввод, напр. плохой replyTo) → ExitGeneric (1)",
			err:      &client.OCSError{Code: http.StatusBadRequest, Message: "invalid replyTo"},
			wantCode: ExitGeneric,
		},
		{
			name:     "OCS 500 (сбой сервера) → ExitGeneric (1)",
			err:      &client.OCSError{Code: http.StatusInternalServerError, Message: "internal"},
			wantCode: ExitGeneric,
		},
		{
			name:     "OCS 404 без message — код виден в тексте, exit 2",
			err:      &client.OCSError{Code: http.StatusNotFound, Message: ""},
			wantCode: ExitNotFound,
		},
		{
			name:        "оборачнутый %w OCS 404 → ExitNotFound (errors.As проходит цепочку)",
			err:         fmt.Errorf("GetChat: %w", &client.OCSError{Code: http.StatusNotFound, Message: "room not found"}),
			wantCode:    ExitNotFound,
			wantOCSCode: http.StatusNotFound,
		},
		{
			name:     "сетевая ошибка (не OCS) → ExitGeneric (1)",
			err:      errors.New("connection refused"),
			wantCode: ExitGeneric,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ee := exitFromClientErr(tc.err)
			if ee.Code != tc.wantCode {
				t.Errorf("Code: got %d, want %d (err=%v)", ee.Code, tc.wantCode, tc.err)
			}
			// nil-кейс: ExitError{ExitOK, nil} — обе стороны nil.
			if tc.err == nil {
				if ee.Err != nil {
					t.Errorf("Err: got %v, want nil", ee.Err)
				}
				return
			}
			if ee.Err == nil {
				t.Fatalf("Err: got nil, want non-nil (потеряли ошибку при маппинге)")
			}
			if tc.wantOCSCode != 0 {
				var oe *client.OCSError
				if !errors.As(ee.Err, &oe) {
					t.Fatalf("errors.As(*client.OCSError): не извлечён из %v", ee.Err)
				}
				if oe.Code != tc.wantOCSCode {
					t.Errorf("OCSError.Code: got %d, want %d", oe.Code, tc.wantOCSCode)
				}
			}
		})
	}
}

// TestExitFromClientErr_StdLibErrors — специфичный кейс: *url.Error от net/http
// (типичный транспортный сбой: cross-host redirect, timeout, DNS) не должен
// falsely классифроваться как NotFound — это всегда exit 1. Регрессия на
// гипотетическую потерю типа *OCSError при санитайзе в client.sanitizeErr.
func TestExitFromClientErr_StdLibErrors(t *testing.T) {
	// sanitizeErr возвращает исходный *url.Error (не оборачивая в OCSError),
	// поэтому errors.As(*OCSError) даёт false → exit 1.
	ee := exitFromClientErr(errors.New("Get https://host/p: dial tcp: connection refused"))
	if ee.Code != ExitGeneric {
		t.Errorf("Code: got %d, want %d", ee.Code, ExitGeneric)
	}
}
