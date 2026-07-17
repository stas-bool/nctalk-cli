// Package client реализует HTTP-клиент Nextcloud Talk: базовый TalkClient,
// OCS-конверт, sanitize URL и same-host redirect-политику.
//
// Контракт безопасности (спека §5, §9):
//   - креды берутся из config.Config (источник — env), никогда из аргументов;
//   - заголовок Authorization собирается на каждый запрос и не кэшируется;
//   - все ошибки пропускаются через sanitizeErr — в тексте не должно быть
//     userinfo, query и пароля.
//
// БЕЗОПАСНОСТЬ — ЗАПРЕЩЕНО использовать httputil.DumpRequestOut и
// httputil.DumpResponse (включая через любой debug-вывод): они печатают
// заголовки запроса/ответа целиком, вместе с Authorization. Аналогично —
// любой лог raw-запроса/ответа под запретом.
package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/stas/nctalk/internal/config"
)

// httpDoer — минимальный интерфейс, которому TalkClient делегирует HTTP-вызовы.
// В production используется *http.Client; в тестах — httptest-фикстуры.
type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// TalkClient — HTTP-клиент Nextcloud Talk.
//
// Поля unexported: cfg (креды/таймаут), doer (транспорт, подменяется в тестах),
// baseURL (распарсенный один раз на старте, без userinfo/query).
type TalkClient struct {
	cfg     config.Config
	doer    httpDoer
	baseURL *url.URL
}

// NewTalkClient — production-конструктор: создаёт http.Client на базе
// http.DefaultTransport (пулы соединений, прокси из env) с таймаутом из cfg и
// same-host redirect-политикой. Cross-host редиректы блокируются (см.
// sameHostRedirectPolicy).
func NewTalkClient(cfg config.Config) *TalkClient {
	hc := &http.Client{
		Transport:     http.DefaultTransport,
		Timeout:       cfg.Timeout,
		CheckRedirect: sameHostRedirectPolicy,
	}
	return NewTalkClientWithDoer(cfg, hc)
}

// NewTalkClientWithDoer — конструктор для тестов: принимает любой httpDoer.
// baseURL парсится один раз при конструировании; config.Load уже валидирует
// URL, поэтому ошибка парсинга здесь — баг вызывающего, падаем громко.
func NewTalkClientWithDoer(cfg config.Config, doer httpDoer) *TalkClient {
	u, err := url.Parse(cfg.BaseURL)
	if err != nil {
		panic(fmt.Sprintf("client: невалидный BaseURL после config.Load: %v", err))
	}
	return &TalkClient{
		cfg:     cfg,
		doer:    doer,
		baseURL: u,
	}
}

// sameHostRedirectPolicy реализует политику редиректов (спека §5):
//   - same-host → следуем (возвращаем nil). Authorization переотправляется на
//     тот же хост стандартным механизмом http.Client — это безопасно;
//   - cross-host → блокируем, возвращаем ошибку (НЕ следуем). http.Client
//     обернёт её в *url.Error и вернёт вызывающему; doOCS прокинет её через
//     sanitizeErr.
//
// http.ErrUseLastResponse НЕ используем: он оставляет редирект «висеть» без
// ошибки и требует ручной обработки тела. Лимит редиректов — стандартный (10).
//
// Аргументы: req — запрос, который БУДЕТ отправлен на новый URL после редиректа;
// via — уже отправленные запросы (последний — via[len-1]).
func sameHostRedirectPolicy(req *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	prev := via[len(via)-1]
	if req.URL.Host == prev.URL.Host {
		return nil
	}
	return errors.New("cross-host redirect blocked: " + req.URL.Host)
}

// doOCS выполняет OCS-запрос и распаковывает конверт в out.
//
// method — HTTP-метод; p — путь относительно baseURL (начинается с '/'),
// обычно берётся из именованной константы (см. paths.go); body — тело запроса
// (nil для GET); mutate=true добавляет заголовок Content-Type: application/json
// (для POST/PUT/DELETE); out — указатель на целевой тип для поля ocs.data
// (может быть nil, если нужен только статус).
//
// Контракт ошибок:
//   - при meta.statusCode >= 400 возвращается ошибка с meta.message;
//   - любая transport/decode-ошибка пропускается через sanitizeErr (без userinfo
//     и query в тексте).
func (c *TalkClient) doOCS(ctx context.Context, method, p string, body io.Reader, mutate bool, out any) error {
	// Склеиваем baseURL и путь без двойных слэшей. path.Join нормирует слэши
	// и убирает trailing '/'; для абсолютного пути результат начинаем с '/'.
	fullURL := *c.baseURL
	joined := path.Join(c.baseURL.Path, p)
	if !strings.HasPrefix(joined, "/") {
		joined = "/" + joined
	}
	fullURL.Path = joined
	// RawPath сбрасываем, чтобы url.String() пересобрал путь из Path без мусора.
	fullURL.RawPath = ""

	req, err := http.NewRequestWithContext(ctx, method, fullURL.String(), body)
	if err != nil {
		return sanitizeErr(err)
	}

	// Authorization: Basic base64(login:password) (RFC 7617). На каждый запрос
	// собираем заново — не кэшируем.
	creds := c.cfg.Login + ":" + c.cfg.Password
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(creds)))
	req.Header.Set("OCS-APIRequest", "true")
	req.Header.Set("Accept", "application/json")
	if mutate {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.doer.Do(req)
	if err != nil {
		// При redirect-ошибке http.Client может вернуть предыдущий Response с
		// уже закрытым Body. Закрываем идемпотентно — на случай, если Body
		// ещё открыт (например, при сетевой ошибке до редиректа).
		if resp != nil {
			_ = resp.Body.Close()
		}
		return sanitizeErr(err)
	}
	defer resp.Body.Close()

	// Декодируем конверт в две фазы: сначала meta + RawMessage для data, потом
	// (при успехе) распаковываем data в out. Так ошибка API не ломает типизацию
	// out и не приводит к двойному декодированию.
	var env OCSEnvelope[json.RawMessage]
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return sanitizeErr(fmt.Errorf("client: не удалось декодировать OCS-ответ: %w", err))
	}

	if env.OCS.Meta.StatusCode >= 400 {
		msg := env.OCS.Meta.Message
		if msg == "" {
			msg = fmt.Sprintf("OCS statusCode=%d", env.OCS.Meta.StatusCode)
		}
		return errors.New("client: " + msg)
	}

	// out может быть nil (нужен только статус) или data может отсутствовать —
	// тогда не трогаем out.
	if out != nil && len(env.OCS.Data) > 0 {
		if err := json.Unmarshal(env.OCS.Data, out); err != nil {
			return sanitizeErr(fmt.Errorf("client: не удалось распаковать ocs.data: %w", err))
		}
	}
	return nil
}
