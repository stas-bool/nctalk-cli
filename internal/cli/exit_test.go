package cli

import (
	"errors"
	"testing"
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
