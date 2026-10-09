package transport

// dav_test.go — тесты WebDAV-транспорта DoDAV (спека-дельта 2026-10-09 §4.1,
// §7): метод, путь и заголовки, экранирование сегментов, Content-Length без
// chunked, тело не закрывается (повтор PUT), s:message из живых XML-ошибок
// Sabre, лимит чтения тела, SanitizeErr и пароль-маркер.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// davFixture читает живую фикстуру из корневого testdata/ (Task 1).
func davFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// fixtureSabreMessage — текст <s:message> живой фикстуры, вырезанный строковым
// поиском: эталон для сверки парсера, независимый от encoding/xml.
func fixtureSabreMessage(t *testing.T, raw []byte) string {
	t.Helper()
	s := string(raw)
	i := strings.Index(s, "<s:message>")
	j := strings.Index(s, "</s:message>")
	if i < 0 || j < i {
		t.Fatalf("в фикстуре нет <s:message>: %s", s)
	}
	return strings.TrimSpace(s[i+len("<s:message>") : j])
}

// closeCountingReader — ReadSeeker с Close-счётчиком: тип, длину которого
// net/http не выводит, и io.Closer — Transport закрыл бы его, будь он передан
// телом запроса как есть.
type closeCountingReader struct {
	r      io.ReadSeeker
	closes int32
}

func (c *closeCountingReader) Read(p []byte) (int, error) { return c.r.Read(p) }

func (c *closeCountingReader) Seek(off int64, whence int) (int64, error) {
	return c.r.Seek(off, whence)
}

func (c *closeCountingReader) Close() error {
	atomic.AddInt32(&c.closes, 1)
	return nil
}

// TestDoDAV_RequestShape — метод, путь от BaseURL с префиксом, экранирование
// сегментов (пробел, кириллица, '#', '?', '%'), Authorization: Basic поверх
// значения вызывающего, его прочие заголовки; OCS-заголовков у WebDAV нет;
// '?' в имени не превратился в query. HTTP 404 — статус, не ошибка.
func TestDoDAV_RequestShape(t *testing.T) {
	var (
		gotMethod, gotEscaped, gotQuery string
		gotHeader                       http.Header
	)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotEscaped = r.URL.EscapedPath()
		gotQuery = r.URL.RawQuery
		gotHeader = r.Header.Clone()
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	h := http.Header{}
	h.Set("If-None-Match", "*")
	h.Set("Authorization", "Basic подмена")
	resp, err := DoDAV(context.Background(), http.DefaultClient, authFromURL(t, ts.URL+"/nc"),
		http.MethodHead, "/remote.php/dav/files/anna smith/Talk/отчёт #1?%.pdf", h, nil, 0)
	if err != nil {
		t.Fatalf("DoDAV: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("StatusCode: got %d, want 404 (статус отдаётся вызывающему)", resp.StatusCode)
	}
	if gotMethod != http.MethodHead {
		t.Errorf("method: got %q, want HEAD", gotMethod)
	}
	const wantEscaped = "/nc/remote.php/dav/files/anna%20smith/Talk/%D0%BE%D1%82%D1%87%D1%91%D1%82%20%231%3F%25.pdf"
	if gotEscaped != wantEscaped {
		t.Errorf("escaped path:\n got %q\nwant %q", gotEscaped, wantEscaped)
	}
	if gotQuery != "" {
		t.Errorf("RawQuery: got %q, want пусто ('?' — часть имени)", gotQuery)
	}
	if got := gotHeader.Get("Authorization"); got != wantAuthHeader() {
		t.Errorf("Authorization: got %q, want %q", got, wantAuthHeader())
	}
	if got := gotHeader.Get("If-None-Match"); got != "*" {
		t.Errorf("If-None-Match: got %q, want *", got)
	}
	if got := gotHeader.Get("OCS-APIRequest"); got != "" {
		t.Errorf("OCS-APIRequest: got %q, want пусто (у WebDAV нет OCS-заголовков)", got)
	}
}

// TestDoDAV_ContentLengthNoChunked — PUT уходит с Content-Length = размер и
// без chunked: и пустой файл (http.NoBody), и тело неизвестного net/http типа
// (спека §3.4: chunked-тело за php-fpm теряется). Тело не закрыто.
func TestDoDAV_ContentLengthNoChunked(t *testing.T) {
	for _, content := range []string{"", "hello", strings.Repeat("x", 70000)} {
		var (
			gotLen  int64
			gotTE   []string
			gotBody []byte
		)
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotLen = r.ContentLength
			gotTE = r.TransferEncoding
			gotBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusCreated)
		}))
		body := &closeCountingReader{r: strings.NewReader(content)}
		resp, err := DoDAV(context.Background(), http.DefaultClient, authFromURL(t, ts.URL),
			http.MethodPut, "/f.bin", nil, body, int64(len(content)))
		ts.Close()
		if err != nil {
			t.Fatalf("len=%d: DoDAV: %v", len(content), err)
		}
		if resp.StatusCode != http.StatusCreated {
			t.Errorf("len=%d: StatusCode %d, want 201", len(content), resp.StatusCode)
		}
		if gotLen != int64(len(content)) || len(gotTE) != 0 {
			t.Errorf("len=%d: сервер видит ContentLength=%d TransferEncoding=%v, want %d и без chunked",
				len(content), gotLen, gotTE, len(content))
		}
		if string(gotBody) != content {
			t.Errorf("len=%d: тело на сервере отличается (got %d байт)", len(content), len(gotBody))
		}
		if n := atomic.LoadInt32(&body.closes); n != 0 {
			t.Errorf("len=%d: тело закрыто %d раз, want 0", len(content), n)
		}
	}
}

// TestDoDAV_BodyReusableAfterSend — после PUT тело не закрыто: вызывающий
// перематывает его и шлёт тот же файл повторно (сценарий 409/412 → повтор).
// Реальный *os.File: без не-закрывающей обёртки Seek упал бы «file already closed».
func TestDoDAV_BodyReusableAfterSend(t *testing.T) {
	var bodies []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		w.WriteHeader(http.StatusPreconditionFailed)
	}))
	defer ts.Close()

	path := filepath.Join(t.TempDir(), "report.pdf")
	if err := os.WriteFile(path, []byte("file-content"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	auth := authFromURL(t, ts.URL)
	for i := 0; i < 2; i++ {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			t.Fatalf("попытка %d: Seek: %v (тело закрыто транспортом?)", i+1, err)
		}
		if _, err := DoDAV(context.Background(), http.DefaultClient, auth, http.MethodPut, "/f", nil, f, 12); err != nil {
			t.Fatalf("попытка %d: DoDAV: %v", i+1, err)
		}
	}
	if len(bodies) != 2 || bodies[0] != "file-content" || bodies[1] != "file-content" {
		t.Errorf("тела попыток: got %q, want дважды «file-content»", bodies)
	}
}

// TestDoDAV_SabreMessage — s:message извлекается из живых XML-ошибок Sabre
// (Task 1: 404 PUT без папки — живой сервер отвечает 404, не 409; 412 If-None-Match на существующий файл); не-XML
// тело (HTML прокси) → пусто; у 2xx Message не заполняется.
func TestDoDAV_SabreMessage(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   []byte
		want   string
	}{
		{"404 живой (PUT без папки)", http.StatusNotFound, davFixture(t, "dav_error_409.xml"), fixtureSabreMessage(t, davFixture(t, "dav_error_409.xml"))},
		{"412 живой", http.StatusPreconditionFailed, davFixture(t, "dav_error_412.xml"), fixtureSabreMessage(t, davFixture(t, "dav_error_412.xml"))},
		{"413 HTML прокси", http.StatusRequestEntityTooLarge, []byte("<html><body><h1>413 Request Entity Too Large</h1></body></html>"), ""},
		{"201 с телом", http.StatusCreated, davFixture(t, "dav_error_412.xml"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/xml; charset=utf-8")
				w.WriteHeader(tc.status)
				_, _ = w.Write(tc.body)
			}))
			defer ts.Close()
			resp, err := DoDAV(context.Background(), http.DefaultClient, authFromURL(t, ts.URL), http.MethodPut, "/f", nil, strings.NewReader("x"), 1)
			if err != nil {
				t.Fatalf("DoDAV: %v", err)
			}
			if resp.StatusCode != tc.status {
				t.Errorf("StatusCode: got %d, want %d", resp.StatusCode, tc.status)
			}
			if resp.Message != tc.want {
				t.Errorf("Message: got %q, want %q", resp.Message, tc.want)
			}
		})
	}
	if fixtureSabreMessage(t, davFixture(t, "dav_error_412.xml")) == "" {
		t.Fatal("живая фикстура 412 без текста s:message — эталон пуст")
	}
}

// stubDoer — Doer с заранее заданным ответом (без сети).
type stubDoer struct{ resp *http.Response }

func (d *stubDoer) Do(*http.Request) (*http.Response, error) { return d.resp, nil }

// countingBody — тело ответа: считает прочитанные байты и факт Close.
type countingBody struct {
	r      io.Reader
	n      int64
	closed bool
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	b.n += int64(n)
	return n, err
}

func (b *countingBody) Close() error {
	b.closed = true
	return nil
}

// TestDoDAV_BodyReadLimit — тело ответа читается не дальше davBodyLimit
// (64 КиБ, спека §3.4) и закрывается: 1 МиБ «s:message» не тянется целиком.
func TestDoDAV_BodyReadLimit(t *testing.T) {
	huge := `<d:error xmlns:d="DAV:" xmlns:s="http://sabredav.org/ns"><s:message>` +
		strings.Repeat("x", 1<<20) + `</s:message></d:error>`
	body := &countingBody{r: strings.NewReader(huge)}
	d := &stubDoer{resp: &http.Response{StatusCode: http.StatusInsufficientStorage, Header: http.Header{}, Body: body}}
	resp, err := DoDAV(context.Background(), d, authFromURL(t, "https://nc.example.org"), http.MethodPut, "/f", nil, strings.NewReader("x"), 1)
	if err != nil {
		t.Fatalf("DoDAV: %v", err)
	}
	if body.n > davBodyLimit {
		t.Errorf("прочитано %d байт тела, want <= %d", body.n, davBodyLimit)
	}
	if !body.closed {
		t.Error("тело ответа не закрыто")
	}
	if resp.Message != "" {
		t.Errorf("Message: got %d символов, want пусто (обрезанный XML не разбирается)", len(resp.Message))
	}
}

// errDoer — Doer, возвращающий заданную транспортную ошибку.
type errDoer struct{ err error }

func (d *errDoer) Do(*http.Request) (*http.Response, error) { return nil, d.err }

// TestDoDAV_TransportErrorSanitized — транспортная ошибка проходит SanitizeErr:
// ни userinfo с паролем-маркером, ни query в тексте нет.
func TestDoDAV_TransportErrorSanitized(t *testing.T) {
	d := &errDoer{err: &url.Error{Op: "Put", URL: "https://user:SECRET_MARKER@nc.example.org/f?x=1", Err: errors.New("connection refused")}}
	_, err := DoDAV(context.Background(), d, authFromURL(t, "https://nc.example.org"), http.MethodPut, "/f", nil, bytes.NewReader([]byte("x")), 1)
	if err == nil {
		t.Fatal("err = nil, want транспортную ошибку")
	}
	if s := err.Error(); strings.Contains(s, "SECRET_MARKER") || strings.Contains(s, "x=1") {
		t.Errorf("утечка в тексте ошибки: %q", s)
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("исходная причина потеряна: %q", err.Error())
	}
}

// TestDoDAV_CrossHostRedirectBlocked — PUT с 302 на чужой хост: http.Client с
// SameHostRedirectPolicy блокирует (302 превращает PUT в GET, политика
// срабатывает до запроса), чужой хост не получает ни одного запроса, пароль-
// маркер в тексте ошибки отсутствует.
func TestDoDAV_CrossHostRedirectBlocked(t *testing.T) {
	var evilHits int32
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&evilHits, 1)
	}))
	defer evil.Close()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL+"/leak", http.StatusFound)
	}))
	defer ts.Close()

	hc := &http.Client{CheckRedirect: SameHostRedirectPolicy}
	auth := authFromURL(t, ts.URL)
	auth.Password = "SECRET_MARKER"
	_, err := DoDAV(context.Background(), hc, auth, http.MethodPut, "/f", nil, strings.NewReader("x"), 1)
	if err == nil {
		t.Fatal("err = nil, want cross-host redirect blocked")
	}
	if !strings.Contains(err.Error(), "cross-host redirect blocked") {
		t.Errorf("err: got %q, want «cross-host redirect blocked»", err.Error())
	}
	if strings.Contains(err.Error(), "SECRET_MARKER") {
		t.Errorf("УТЕЧКА пароля в тексте ошибки: %q", err.Error())
	}
	if n := atomic.LoadInt32(&evilHits); n != 0 {
		t.Errorf("чужой хост получил %d запросов, want 0", n)
	}
}
