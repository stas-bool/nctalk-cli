package transport

// dav.go — WebDAV-транспорт (спека-дельта 2026-10-09 §4.1). Гарантии
// безопасности — те же, что у DoOCS (шапка transport.go): Authorization
// собирается на каждый запрос, все ошибки — через SanitizeErr, dump
// запросов/ответов запрещён. OCS-конверта и OCS-заголовков нет: HTTP-статус
// интерпретирует вызывающий (у HEAD 404 — норма, «имя свободно»). Транспорт
// пригодится будущим files ls/get/find (TODO.md).

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
)

// davBodyLimit — потолок чтения тела ответа WebDAV (спека §3.4): XML-ошибка
// Sabre — сотни байт; лимит защищает от неожиданно большого тела (HTML-страница
// прокси на 413/507 и т.п.).
const davBodyLimit = 64 << 10

// DAVResponse — итог WebDAV-запроса: статус интерпретирует вызывающий.
type DAVResponse struct {
	StatusCode int
	Header     http.Header
	Message    string // s:message из XML-ошибки Sabre (только при StatusCode >= 400)
}

// DoDAV выполняет WebDAV-запрос (спека-дельта 2026-10-09 §4.1).
//
// Параметры:
//   - p — путь относительно auth.BaseURL (начинается с '/'), НЕэкранированный:
//     сегменты экранирует url.URL при сборке (пробел, кириллица, '#', '?', '%');
//     склейка — path.Join, как у DoOCS: он схлопывает '.'/'..', поэтому имена
//     сегментов проверяет вызывающий;
//   - header — дополнительные заголовки (If-None-Match, Content-Type), может
//     быть nil; Authorization из header перекрывается собранным здесь;
//   - body — тело запроса (nil для HEAD/MKCOL/DELETE). DoDAV НЕ закрывает body
//     и не даёт закрыть его Transport-у: net/http закрывает тело-io.Closer
//     (например *os.File) после отправки и даже при ошибке, а вызывающий
//     перематывает тот же файл для повтора PUT после 409/412;
//   - contentLength — длина body (>= 0; без тела — 0), её знает только
//     вызывающий. Уходит заголовком Content-Length, без chunked
//     transfer-encoding: за php-fpm chunked-тело в части конфигураций
//     приходит пустым. Отрицательная длина (у net/http — «длина неизвестна»,
//     т.е. chunked) — ошибка до сети. Из body уходит ровно contentLength байт
//     — снимок на момент Stat: хвост, дописанный после (растущий лог), не
//     отправляется; тело короче contentLength (файл укоротили) — ошибка
//     транспорта net/http. При 0 body не читается: уходит http.NoBody
//     (пустой файл).
//
// error — транспортные сбои (сеть, отмена ctx, редирект на чужой хост —
// политика doer-а SameHostRedirectPolicy, чтение тела ответа) и отрицательная
// contentLength (до сети); всегда через SanitizeErr. HTTP-статусы, включая
// 4xx/5xx, возвращаются в DAVResponse.
// Редиректы PUT: на 307/308 тело без GetBody Go не переотправляет (вернётся
// сам 307), а 301/302 превращает PUT в GET — в обоих случаях статус не совпадёт
// с ожидаемым вызывающим, молчаливого успеха нет.
//
// Тело ответа дочитывается не дальше davBodyLimit и закрывается.
func DoDAV(ctx context.Context, doer Doer, auth Auth, method, p string, header http.Header, body io.Reader, contentLength int64) (*DAVResponse, error) {
	// Отрицательная длина: net/http отправил бы тело chunked, а за php-fpm в
	// части конфигураций оно приходит пустым — сервер создал бы пустой файл с
	// 201, молчаливая потеря данных. Отказ до сети (code-review 2026-10-10).
	if contentLength < 0 {
		return nil, SanitizeErr(fmt.Errorf("transport: DoDAV: отрицательная длина тела %d (нужна точная длина: chunked transfer-encoding запрещён)", contentLength))
	}

	// Сборка URL — как в DoOCS: path.Join без двойных слэшей, RawPath сброшен,
	// чтобы url.String() экранировал сегменты из Path. Query у WebDAV нет.
	fullURL := *auth.BaseURL
	joined := path.Join(auth.BaseURL.Path, p)
	if !strings.HasPrefix(joined, "/") {
		joined = "/" + joined
	}
	fullURL.Path = joined
	fullURL.RawPath = ""
	fullURL.RawQuery = ""

	var reqBody io.Reader
	explicitLength := false
	switch {
	case body == nil:
		// HEAD/MKCOL/DELETE — без тела.
	case contentLength == 0:
		// Пустой файл: http.NoBody → Content-Length: 0, body не читается.
		// Не-nil тело с нулевой ContentLength Go считает «длина неизвестна» и
		// шлёт chunked.
		reqBody = http.NoBody
	default:
		// Снимок: ровно contentLength байт. Файл, выросший после Stat (лог),
		// иначе дал бы ошибку net/http «ContentLength=N with Body length M»
		// уже после 201 сервера — файл-сирота. Обёртка — на каждый вызов:
		// повтор PUT после Seek(0) читает файл заново. Не-закрывающая
		// обёртка: Transport закрыл бы *os.File, и Seek перед повтором PUT
		// упал бы «file already closed».
		reqBody = io.NopCloser(io.LimitReader(body, contentLength))
		explicitLength = true
	}

	req, err := http.NewRequestWithContext(ctx, method, fullURL.String(), reqBody)
	if err != nil {
		return nil, SanitizeErr(err)
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if explicitLength {
		// Тело неизвестного net/http типа: длину задаём явно, иначе chunked.
		req.ContentLength = contentLength
	}
	// Authorization: Basic — на каждый запрос заново и ПОСЛЕ копирования
	// header (перекрывает значение вызывающего), как в DoOCS.
	creds := auth.Login + ":" + auth.Password
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(creds)))

	resp, err := doer.Do(req)
	if err != nil {
		// При redirect-ошибке http.Client может вернуть предыдущий Response —
		// закрываем идемпотентно (как в DoOCS).
		if resp != nil {
			_ = resp.Body.Close()
		}
		return nil, SanitizeErr(err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, davBodyLimit))
	if err != nil {
		return nil, SanitizeErr(err)
	}
	out := &DAVResponse{StatusCode: resp.StatusCode, Header: resp.Header}
	if resp.StatusCode >= 400 {
		out.Message = sabreMessage(raw)
	}
	return out, nil
}

// sabreError — XML-ошибка Sabre:
//
//	<d:error xmlns:d="DAV:" xmlns:s="http://sabredav.org/ns">
//	  <s:exception>…</s:exception>
//	  <s:message>…</s:message>
//	</d:error>
//
// Поле сопоставляется по локальному имени (пространство имён любое).
type sabreError struct {
	Message string `xml:"message"`
}

// sabreMessage извлекает s:message из тела ошибки WebDAV. Не-XML (HTML прокси),
// обрезанное лимитом или пустое тело → "".
func sabreMessage(raw []byte) string {
	var e sabreError
	if err := xml.Unmarshal(raw, &e); err != nil {
		return ""
	}
	return strings.TrimSpace(e.Message)
}
