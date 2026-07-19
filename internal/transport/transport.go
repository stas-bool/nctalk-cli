// Package transport — чистый HTTP-транспорт поверх OCS-конверта Nextcloud.
//
// Не знает про WebRTC и про специфику ресурсов Spreed (rooms/chat/call/signaling)
// — только HTTP, OCS-конверт и OCS-ошибки. Спека 2026-07-19 §3/§4/§5.
//
// Контракт безопасности (спека §5, §9):
//   - креды передаются через Auth (источник — config.Config у вызывающего), в
//     transport нигде не логируются и не попадают в URL (исключительно в
//     заголовок Authorization, собираемый на каждый запрос);
//   - заголовок Authorization собирается на каждый запрос заново, не кэшируется;
//   - все ошибки пропускаются через SanitizeErr — в тексте не должно быть
//     userinfo, query и пароля.
//
// БЕЗОПАСНОСТЬ — ЗАПРЕЩЕНО использовать httputil.DumpRequestOut и
// httputil.DumpResponse (включая через любой debug-вывод): они печатают
// заголовки запроса/ответа целиком, вместе с Authorization. Аналогично —
// любой лог raw-запроса/ответа под запретом.
package transport

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
)

// Doer — минимальный HTTP-интерфейс, которому transport делегирует вызовы.
// Переименован из httpDoer (бывший internal/client), чтобы не коллидировать
// при одновременном импорте. *http.Client реализует его; моки — в тестах.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Auth — креды для Basic-auth + распарсенный BaseURL. Через структуру (а не
// config.Config напрямую), чтобы transport не зависел от internal/config —
// это позволяет использовать его из любого потребителя (internal/client и
// внутренние пакеты звонков).
//
// Timeout сюда НЕ входит: таймаут выставляется на http.Client (см.
// client.NewTalkClient), а не на запрос. Поле BaseURL обязано быть не-nil —
// валидация выполняется вызывающим (см. panic в client.NewTalkClientWithDoer).
type Auth struct {
	BaseURL  *url.URL // распарсенный один раз; nil невалиден
	Login    string
	Password string
}

// SameHostRedirectPolicy реализует политику редиректов (спека §5):
//   - same-host → следуем (возвращаем nil). Authorization переотправляется на
//     тот же хост стандартным механизмом http.Client — это безопасно;
//   - cross-host → блокируем, возвращаем ошибку (НЕ следуем). http.Client
//     обернёт её в *url.Error и вернёт вызывающему; DoOCS прокинет её через
//     SanitizeErr.
//
// Дополнительно блокируется даунгрейд схемы https→http: при same-host редиректе
// с https на http заголовок Authorization (с паролем) уплыл бы в открытом виде.
// Спека §5 («same-host follows») про схему умалчивает — закрываем явный пробел.
//
// http.ErrUseLastResponse НЕ используем: он оставляет редирект «висящ» без
// ошибки и требует ручной обработки тела. Лимит редиректов — стандартный (10).
//
// Аргументы: req — запрос, который БУДЕТ отправлен на новый URL после редиректа;
// via — уже отправленные запросы (последний — via[len-1]).
//
// Экспортируемая, т.к. её размещают в http.Client.CheckRedirect оба потребителя
// (internal/client и internal/call/signaling).
func SameHostRedirectPolicy(req *http.Request, via []*http.Request) error {
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

// DoOCS выполняет OCS-запрос и распаковывает конверт в out. Это ЕДИНСТВЕННЫЙ
// транспортный метод (раньше в client было три копии: doOCS/doOCSGet/
// doSearchOCSGet — собраны в одну, чтобы убрать рассинхрон redact/заголовков).
//
// Параметры:
//   - ctx — контекст отмены/timeout запроса;
//   - doer — транспорт (http.Client в production, мок в тестах);
//   - auth — BaseURL + Basic-auth креды;
//   - method — HTTP-метод;
//   - p — путь относительно auth.BaseURL (начинается с '/'), обычно из именованной
//     константы вызывающего;
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
//   - при meta.statusCode >= 400 возвращается *OCSError с meta.message;
//   - HTTP 304 Not Modified / 204 No Content трактуются как «пустой ответ» —
//     декодирования нет, out не трогается, возвращается (header, nil). Для chat
//     (lookIntoFuture=0) 304 = «старых сообщений больше нет» → чистый стоп
//     пагинации без ошибки;
//   - HTTP 404 (включая серверный 998 «Invalid query» или HTML от reverse-proxy)
//     нормализуется в *OCSError{Code:404} — даёт exit 2 (NotFound) в cli;
//   - любая transport/decode-ошибка пропускается через SanitizeErr (без userinfo
//     и query в тексте); в текст decode-ошибки включается HTTP-статус, чтобы
//     не-OCS тело (HTML login-page, 5xx reverse-proxy) не давало непрозрачное
//     «invalid character '<'».
func DoOCS(ctx context.Context, doer Doer, auth Auth, method, p string, query url.Values, body io.Reader, mutate bool, out any) (http.Header, error) {
	// Склеиваем baseURL и путь без двойных слэшей. path.Join нормирует слэши
	// и убирает trailing '/'; для абсолютного пути результат начинаем с '/'.
	fullURL := *auth.BaseURL
	joined := path.Join(auth.BaseURL.Path, p)
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
		return nil, SanitizeErr(err)
	}

	// Authorization: Basic base64(login:password) (RFC 7617). На каждый запрос
	// собираем заново — не кэшируем.
	creds := auth.Login + ":" + auth.Password
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(creds)))
	req.Header.Set("OCS-APIRequest", "true")
	req.Header.Set("Accept", "application/json")
	if mutate {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := doer.Do(req)
	if err != nil {
		// При redirect-ошибке http.Client может вернуть предыдущий Response с
		// уже закрытым Body. Закрываем идемпотентно — на случай, если Body
		// ещё открыт (например, при сетевой ошибке до редиректа).
		if resp != nil {
			_ = resp.Body.Close()
		}
		return nil, SanitizeErr(err)
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
		return nil, SanitizeErr(fmt.Errorf("client: не удалось декодировать OCS-ответ (HTTP %d): %w", resp.StatusCode, err))
	}

	if env.OCS.Meta.StatusCode >= 400 {
		// Возвращаем типизированную ошибку, чтобы вышележащий слой (cli) мог
		// различать 404 (NotFound → exit 2) и прочие statusCode (exit 1) —
		// спека §7/§9. Текст формируется в OCSError.Error.
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
			return nil, SanitizeErr(fmt.Errorf("client: не удалось распаковать ocs.data: %w", err))
		}
	}
	return resp.Header, nil
}

// SanitizeURL возвращает URL в виде scheme://host[path] — БЕЗ userinfo и БЕЗ
// query. Применяется к URL, попавшим в сообщения ошибок (например *url.Error),
// чтобы не светить креды (спека §5, §9). При ошибке парсинга возвращает
// "<invalid url>".
func SanitizeURL(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		return "<invalid url>"
	}
	// Чувствительные компоненты убираем на месте.
	u.User = nil
	u.RawQuery = ""
	u.ForceQuery = false
	return u.String()
}

// SanitizeErr рекурсивно (через errors.As по цепочке Unwrap) ищет *url.Error и
// заменяет URL в нём на SanitizeURL(URL). Если ошибки типа *url.Error в цепочке
// нет — возвращает err как есть. Nil → nil.
//
// Гарантия (спека §9): в тексте возвращаемой ошибки не остаётся userinfo/query,
// даже если исходный URL содержал креды (https://user:pass@host/...).
//
// Экспортируемая, т.к. её использует внутренний код пакетов caller (internal/call)
// при обработке сетевых ошибок.
func SanitizeErr(err error) error {
	if err == nil {
		return nil
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		ue.URL = SanitizeURL(ue.URL)
		return ue
	}
	return err
}
