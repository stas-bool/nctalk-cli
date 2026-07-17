package client

// chat.go — чтение истории сообщений комнаты (Task 2.5, спека §6 `chat show`,
// §12). Содержит пагинированный GetChat (limit=200 + перебор
// lastKnownMessageId назад) и ранний стоп по timestamp.
//
// SendMessage (Task 2.6) здесь НЕ реализован — отдельная задача.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
)

// pathChat — канонический путь chat-эндпоинта (спека §6, §12). Локальная
// константа: paths.go намеренно не расширяется в этой задаче, чтобы не
// конфликтовать с параллельными Task 2.4 (search) и Task 2.7 (reactions).
const pathChat = "/ocs/v2.php/apps/spreed/api/v1/chat"

// chatPageSize — размер страницы выборки (серверный limit=). Тянем максимум
// разрешённого спекой — по 200 сообщений за запрос.
const chatPageSize = 200

// GetChatOpts — параметры GetChat.
type GetChatOpts struct {
	// Limit — потолок ВЫБОРКИ (не путать с серверным limit=, который всегда
	// равен chatPageSize, и с cli-флагом --last). <=0 → без потолка: тянем все
	// доступные страницы, пока не достигнем начала чата.
	Limit int
	// LastKnownMessageId — точка старта перебора назад: 0 = с самого свежего
	// сообщения; иначе сервер отдаёт сообщения строго старше этого id.
	LastKnownMessageId int
	// StopBeforeTs — ранний стоп (секунды Unix): при первом сообщении с
	// Timestamp СТРОГО меньше StopBeforeTs обрезаем выборку (само сообщение и
	// всё старше НЕ включаем) и прекращаем пагинацию. 0 = без раннего стопа.
	StopBeforeTs int64
}

// GetChat возвращает сообщения комнаты token, упорядоченные от новых к старым
// (спека §6 `chat show`, §12).
//
// Пагинация: страницы по chatPageSize (200) сообщений, перебор
// lastKnownMessageId назад (lookIntoFuture=0). Следующая страница тянется с
// lastKnownMessageId = id самого старого сообщения предыдущей страницы.
//
// Цикл продолжается, пока (а) предыдущая страница вернула ровно chatPageSize
// и (б) не достигнут потолок opts.Limit и (в) не сработал ранний стоп
// opts.StopBeforeTs. Пустая страница = достигли начала чата — выходим без
// ошибки (и не зацикливаемся).
//
// opts.Limit — потолок ВЫБОРКИ, а не серверный limit=: серверу всегда шлётся
// limit=200, излишек обрезается на клиенте после накопления.
func (c *TalkClient) GetChat(ctx context.Context, token string, opts GetChatOpts) ([]Message, error) {
	result := make([]Message, 0)
	lastKnown := opts.LastKnownMessageId
	for {
		page, err := c.getChatPage(ctx, token, lastKnown)
		if err != nil {
			return nil, err
		}
		// Пустая страница — достигли начала чата (или чат пустой). Без этой
		// проверки пустой чат зациклился бы на повторных запросах.
		if len(page) == 0 {
			break
		}

		// Ранний стоп: режем страницу по первому сообщению строго старше
		// opts.StopBeforeTs. Сообщения упорядочены от новых к старым, поэтому
		// найдя первое «слишком старое», всё после него тоже отбрасываем.
		if opts.StopBeforeTs != 0 {
			cut := -1
			for i := range page {
				if page[i].Timestamp < opts.StopBeforeTs {
					cut = i
					break
				}
			}
			if cut >= 0 {
				result = append(result, page[:cut]...)
				break // ранний стоп сработал — пагинацию прекращаем
			}
		}

		result = append(result, page...)
		lastKnown = page[len(page)-1].Id // самое старое в странице

		// Неполная страница — достигли начала чата.
		if len(page) < chatPageSize {
			break
		}
		// Потолок выборки достигнут — больше не тянем.
		if opts.Limit > 0 && len(result) >= opts.Limit {
			break
		}
	}

	// Применяем потолок выборки (сервер отдаёт страницами по 200, поэтому
	// накопленное может превышать opts.Limit).
	if opts.Limit > 0 && len(result) > opts.Limit {
		result = result[:opts.Limit]
	}
	return result, nil
}

// getChatPage выполняет один GET-запрос страницы chat-истории и декодирует
// OCS-конверт в []Message. query: limit=chatPageSize, lookIntoFuture=0,
// lastKnownMessageId (только если lastKnown > 0 — иначе сервер отдаёт самое
// свежее).
func (c *TalkClient) getChatPage(ctx context.Context, token string, lastKnown int) ([]Message, error) {
	p := pathChat + "/" + url.PathEscape(token)
	q := url.Values{}
	q.Set("limit", strconv.Itoa(chatPageSize))
	q.Set("lookIntoFuture", "0")
	if lastKnown > 0 {
		q.Set("lastKnownMessageId", strconv.Itoa(lastKnown))
	}
	var out []Message
	if err := c.doOCSGet(ctx, p, q, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []Message{}
	}
	return out, nil
}

// doOCSGet — query-aware вариант doOCS для GET-запросов к OCS-эндпоинтам.
//
// doOCS принимает только путь и собирает URL через path.Join, который
// экранирует '?' в '%3F' (query отрезается). Для chat-эндпоинта нужны
// query-параметры (limit/lookIntoFuture/lastKnownMessageId), поэтому здесь
// URL собирается с RawQuery отдельно.
//
// Контракт ошибок, заголовки и sanitize-политика — идентичны doOCS (см.
// client.go). Не вынесено в client.go, чтобы не конфликтовать с параллельными
// задачами Task 2.4/2.7; при необходимости рефакторится в общий хелпер позже.
func (c *TalkClient) doOCSGet(ctx context.Context, p string, q url.Values, out any) error {
	u := *c.baseURL
	joined := path.Join(c.baseURL.Path, p)
	if !strings.HasPrefix(joined, "/") {
		joined = "/" + joined
	}
	u.Path = joined
	u.RawPath = ""
	if len(q) > 0 {
		u.RawQuery = q.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return sanitizeErr(err)
	}
	// Authorization собирается на каждый запрос заново (не кэшируется) — тот же
	// контракт безопасности, что в doOCS (спека §5).
	creds := c.cfg.Login + ":" + c.cfg.Password
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(creds)))
	req.Header.Set("OCS-APIRequest", "true")
	req.Header.Set("Accept", "application/json")

	resp, err := c.doer.Do(req)
	if err != nil {
		// При redirect-ошибке http.Client может вернуть предыдущий Response с
		// уже закрытым Body — закрываем идемпотентно.
		if resp != nil {
			_ = resp.Body.Close()
		}
		return sanitizeErr(err)
	}
	defer resp.Body.Close()

	// Декодирование в две фазы (как в doOCS): сначала meta + RawMessage, при
	// успехе — распаковка data в out.
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
	if out != nil && len(env.OCS.Data) > 0 {
		if err := json.Unmarshal(env.OCS.Data, out); err != nil {
			return sanitizeErr(fmt.Errorf("client: не удалось распаковать ocs.data: %w", err))
		}
	}
	return nil
}
