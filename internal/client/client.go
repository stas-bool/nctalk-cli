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
// Дополнительно блокируется даунгрейд схемы https→http: при same-host редиректе
// с https на http заголовок Authorization (с паролем) уплыл бы в открытом виде.
// Спека §5 («same-host follows») про схему умалчивает — закрываем явный пробел.
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
	if req.URL.Host != prev.URL.Host {
		return errors.New("cross-host redirect blocked: " + req.URL.Host)
	}
	// Даунгрейд https→http на том же хосте — запрещаем: Basic-auth уйдёт открыто.
	if prev.URL.Scheme == "https" && req.URL.Scheme == "http" {
		return errors.New("insecure scheme downgrade blocked: https→http")
	}
	return nil
}

// doOCS выполняет OCS-запрос и распаковывает конверт в out. Это ЕДИНСТВЕННЫЙ
// транспортный метод клиента (раньше было три копии: doOCS/doOCSGet/
// doSearchOCSGet — собраны в одну, чтобы убрать рассинхрон redact/заголовков).
//
// Параметры:
//   - method — HTTP-метод;
//   - p — путь относительно baseURL (начинается с '/'), обычно из именованной
//     константы (см. paths.go);
//   - query — необязательные query-параметры (nil → без RawQuery); для GET с
//     limit/term/cursor и т.п.;
//   - body — тело запроса (nil для GET);
//   - mutate=true добавляет заголовок Content-Type: application/json
//     (для POST/PUT/DELETE);
//   - out — указатель на целевой тип для поля ocs.data (может быть nil, если
//     нужен только статус).
//
// Возвращается http.Header ответа — нужен chat-пагинации для чтения заголовка
// X-Chat-Last-Given; прочие вызывающие его игнорируют. В заголовках ответа нет
// кредов (Authorization — заголовок запроса, не ответа).
//
// Контракт ошибок:
//   - при meta.statusCode >= 400 возвращается ошибка с meta.message;
//   - HTTP 304 Not Modified / 204 No Content трактуются как «пустой ответ» —
//     декодирования нет, out не трогается, возвращается (header, nil). Для chat
//     (lookIntoFuture=0) 304 = «старых сообщений больше нет» → чистый стоп
//     пагинации без ошибки;
//   - любая transport/decode-ошибка пропускается через sanitizeErr (без userinfo
//     и query в тексте); в текст decode-ошибки включается HTTP-статус, чтобы
//     не-OCS тело (HTML login-page, 5xx reverse-proxy) не давало непрозрачное
//     «invalid character '<'».
func (c *TalkClient) doOCS(ctx context.Context, method, p string, query url.Values, body io.Reader, mutate bool, out any) (http.Header, error) {
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
	if len(query) > 0 {
		// RawQuery выставляем отдельно от Path — path.Join не умеет в query
		// (закодировал бы '?' в %3F, и сервер получил бы литерал в пути).
		fullURL.RawQuery = query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, fullURL.String(), body)
	if err != nil {
		return nil, sanitizeErr(err)
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
		return nil, sanitizeErr(err)
	}
	defer resp.Body.Close()

	// 304 Not Modified / 204 No Content: тела нет, декодировать нечего. Трактуем
	// как пустой ответ (out не трогаем) — для chat-пагинации это чистый стоп.
	if resp.StatusCode == http.StatusNotModified || resp.StatusCode == http.StatusNoContent {
		return resp.Header, nil
	}

	// Декодируем конверт в две фазы: сначала meta + RawMessage для data, потом
	// (при успехе) распаковываем data в out. Так ошибка API не ломает типизацию
	// out и не приводит к двойному декодированию.
	var env OCSEnvelope[json.RawMessage]
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		// HTTP-статус в тексте — диагностика не-OCS тела (HTML, 5xx) без потерь.
		// Если HTTP 404 — это всё равно «не найдено» (например, reverse-proxy
		// отдаёт HTML-страницу 404 вместо OCS-конверта), возвращаем типизированную
		// ошибку с Code=404, чтобы cli-маппинг дал exit 2 (контракт §7/§9).
		if resp.StatusCode == http.StatusNotFound {
			return nil, &OCSError{
				Code:    http.StatusNotFound,
				Message: fmt.Sprintf("HTTP 404: %s", err),
			}
		}
		return nil, sanitizeErr(fmt.Errorf("client: не удалось декодировать OCS-ответ (HTTP %d): %w", resp.StatusCode, err))
	}

	if env.OCS.Meta.StatusCode >= 400 {
		// Возвращаем типизированную ошибку, чтобы вышележащий слой (cli) мог
		// различать 404 (NotFound → exit 2) и прочие statusCode (exit 1) —
		// спека §7/§9. Текст формируется в OCSError.Error (с тем же форматом
		// «client: <message>» / «client: OCS statusCode=<N>», что и раньше).
		//
		// Особенность Nextcloud: HTTP-статус 404 может идти ВМЕСТЕ с OCS
		// meta.statusCode=998 («Invalid query») — так сервер отвечает на запрос
		// chat/{token}, когда token синтаксически не подходит под маршрут
		// (например, содержит кириллицу). Для пользователя это всё равно
		// «комната не найдена», поэтому Code нормализуем до 404 при HTTP 404.
		// Спека §7/§9: not found = exit 2, в т.ч. «комната не найдена».
		code := env.OCS.Meta.StatusCode
		if resp.StatusCode == http.StatusNotFound {
			code = http.StatusNotFound
		}
		return nil, &OCSError{
			Code:    code,
			Message: env.OCS.Meta.Message,
		}
	}

	// out может быть nil (нужен только статус) или data может отсутствовать —
	// тогда не трогаем out.
	if out != nil && len(env.OCS.Data) > 0 {
		if err := json.Unmarshal(env.OCS.Data, out); err != nil {
			return nil, sanitizeErr(fmt.Errorf("client: не удалось распаковать ocs.data: %w", err))
		}
	}
	return resp.Header, nil
}
