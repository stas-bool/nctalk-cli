package client

// search.go — Unified-поиск комнат через provider talk-conversations (Task 2.4,
// спека §8). Файл локально-самодостаточный: НЕ модифицирует общий paths.go
// (константа пути держится здесь) и НЕ модифицирует client.go (запрос с query
// выполняется локальным методом — общий doOCS не поддерживает RawQuery, см.
// комментарий к doSearchOCSGet).

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

// pathSearchRooms — канонический путь Unified-поиска по conversations
// (спека §8). Локальная константа (НЕ в paths.go), чтобы не конфликтовать с
// параллельными задачами Task 2.5/2.7 при правке общего файла; после
// стабилизации может быть перенесена в paths.go.
const pathSearchRooms = "/ocs/v2.php/search/providers/talk-conversations/search"

// ConversationResult — результат поиска комнаты через Unified talk-conversations
// provider (спека §8). Title — отображаемое имя комнаты; Token — идентификатор
// conversation, берётся из attributes.conversation ответа Unified-поиска.
type ConversationResult struct {
	Title string
	Token string // из attributes.conversation
}

// searchConversationsData — тип-посредник для распаковки ocs.data ответа
// pathSearchRooms. В отличие от большинства OCS-эндпоинтов (где data — массив),
// здесь data — ОБЪЕКТ с полем entries[], а у каждой entry поле attributes —
// вложенный объект. Прямого маппинга JSON → ConversationResult нет, поэтому
// разбор идёт в два этапа: OCSEnvelope → searchConversationsData → []ConversationResult.
type searchConversationsData struct {
	Entries []struct {
		// Title — отображаемое имя найденной комнаты (title в ответе).
		Title string `json:"title"`
		// Attributes — вложенный объект; его поле conversation содержит токен.
		Attributes struct {
			Conversation string `json:"conversation"`
		} `json:"attributes"`
	} `json:"entries"`
}

// SearchRooms ищет комнаты по term через Unified talk-conversations provider
// (спека §8): GET pathSearchRooms?term=…&limit=….
//
// term обязателен непустым (мин длина 1 — спека §8): при пустом возвращается
// клиентская ошибка "term не может быть пустым" БЕЗ сетевого вызова.
//
// limit: при limit <= 0 параметр не передаётся (серверный дефолт); при > 0 —
// передаётся в query как есть.
//
// Разбор: ocs.data.entries[] → title, attributes.conversation (это token).
// Пустые entries → ненулевой пустой срез, nil error. Серверный 4xx (если пустой
// term каким-то образом прошёл бы сквозь guard) доходит как стандартная
// OCS-ошибка с meta.message (см. doSearchOCSGet).
func (c *TalkClient) SearchRooms(ctx context.Context, term string, limit int) ([]ConversationResult, error) {
	// Клиентский guard: пустой term отсекаем ДО любого HTTP-вызова (спека §8).
	if term == "" {
		return nil, errors.New("term не может быть пустым")
	}

	// Собираем query: term обязателен, limit — только при > 0 (иначе серверный
	// дефолт).
	q := url.Values{}
	q.Set("term", term)
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}

	var data searchConversationsData
	if err := c.doSearchOCSGet(ctx, pathSearchRooms, q, &data); err != nil {
		return nil, err
	}

	// Маппим entries → ConversationResult. При отсутствии entries возвращаем
	// ненулевой пустой срез (контракт: «пустой результат — не nil»).
	out := make([]ConversationResult, 0, len(data.Entries))
	for _, e := range data.Entries {
		out = append(out, ConversationResult{
			Title: e.Title,
			Token: e.Attributes.Conversation,
		})
	}
	return out, nil
}

// doSearchOCSGet выполняет OCS GET с query-параметрами — локальный аналог doOCS
// (client.go) для случая, когда запрос несёт query.
//
// НЕОБХОДИМОСТЬ отдельного метода: общий doOCS собирает URL через
// path.Join(baseURL.Path, p) и кладёт результат в URL.Path, а RawQuery не
// выставляет. Если передать путь с '?' в doOCS, path.Join оставит '?' в строке
// пути, URL.String() закодирует его в %3F, и сервер получит литеральный '?' в
// path — query до эндпоинта не дойдёт. Этот метод, в отличие от doOCS,
// выставляет RawQuery отдельно от Path.
//
// Контракт ошибок — идентичен doOCS (стандартная обработка, спека §8):
//   - заголовки Authorization (Basic, собирается на каждый запрос), OCS-APIRequest,
//     Accept — те же, что в doOCS;
//   - декодирование OCSEnvelope; при meta.statusCode >= 400 возвращается ошибка с
//     meta.message (серверный 400 по пустому term покажет именно серверный message);
//   - transport/decode-ошибки пропускаются через sanitizeErr (без userinfo/query).
//
// Метод локален для Task 2.4 и НЕ должен конфликтовать с параллельными
// Task 2.5/2.7 (у тех — свои файлы chat.go/reactions.go со своими именами).
// После завершения параллельных задач может быть объединён с doOCS (добавлением
// query-параметра в сигнатуру doOCS), чтобы убрать дублирование.
func (c *TalkClient) doSearchOCSGet(ctx context.Context, p string, query url.Values, out any) error {
	// Склеиваем baseURL и путь так же, как в doOCS (нормализация слэшей через
	// path.Join), НО query выставляем в RawQuery, а не дописываем в путь.
	fullURL := *c.baseURL
	joined := path.Join(c.baseURL.Path, p)
	if !strings.HasPrefix(joined, "/") {
		joined = "/" + joined
	}
	fullURL.Path = joined
	fullURL.RawPath = ""
	fullURL.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL.String(), nil)
	if err != nil {
		return sanitizeErr(err)
	}

	// Authorization: Basic base64(login:password) (RFC 7617). На каждый запрос
	// собираем заново — не кэшируем. Идентично doOCS.
	creds := c.cfg.Login + ":" + c.cfg.Password
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(creds)))
	req.Header.Set("OCS-APIRequest", "true")
	req.Header.Set("Accept", "application/json")
	// Content-Type не ставим: GET без тела (mutate=false по аналогии с doOCS).

	resp, err := c.doer.Do(req)
	if err != nil {
		// На случай redirect-ошибки с открытым Body — закрываем идемпотентно.
		if resp != nil {
			_ = resp.Body.Close()
		}
		return sanitizeErr(err)
	}
	defer resp.Body.Close()

	// Декодируем конверт в две фазы: сначала meta + RawMessage для data, затем
	// (при успехе) распаковываем data в out. Так ошибка API не ломает типизацию.
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
	// out может быть nil (нужен только статус) или data отсутствовать — тогда не
	// трогаем out.
	if out != nil && len(env.OCS.Data) > 0 {
		if err := json.Unmarshal(env.OCS.Data, out); err != nil {
			return sanitizeErr(fmt.Errorf("client: не удалось распаковать ocs.data: %w", err))
		}
	}
	return nil
}

// pathSearchMessages — канонический путь Unified-поиска по сообщениям
// (спека §8, talk-message provider). Локальная константа по тем же причинам,
// что и pathSearchRooms (см. выше) — чтобы не трогать общий paths.go.
const pathSearchMessages = "/ocs/v2.php/search/providers/talk-message/search"

// maxSearchMessagesPages — потолок числа страниц при All=true (спека §6 search:
// защита от зацикливания пагинации). При All=false всегда запрашивается ровно
// одна страница.
const maxSearchMessagesPages = 5

// MessageResult — результат поиска сообщения через Unified talk-message provider
// (спека §8, §12). Поля маппятся из entry Unified-ответа:
//   - Title       ← title (имя автора);
//   - Subline     ← subline (текст сообщения);
//   - ResourceUrl ← resourceUrl (…/call/{token}#message_{id}).
//
// Attributes — вложенный объект, поля маппятся из attributes:
//   - Conversation ← conversation (это token);
//   - MessageId    ← messageId (берётся НАПРЯМУЮ из attributes, без разбора
//     resourceUrl);
//   - ActorType    ← actorType;
//   - ActorId      ← actorId;
//   - Timestamp    ← timestamp, НО в JSON это СТРОКА — нормализуется в int64
//     (спека §12) при маппинге в SearchMessages.
type MessageResult struct {
	Title       string
	Subline     string
	ResourceUrl string
	Attributes struct {
		Conversation string
		MessageId    int
		ActorType    string
		ActorId      string
		Timestamp    int64 // нормализуется из строки ответа (спека §12)
	}
}

// SearchMessagesOpts — параметры поиска сообщений (спека §6, §8).
type SearchMessagesOpts struct {
	// From — фильтр по автору: маппится в query-параметр person (спека §8).
	// Пусто — без фильтра (параметр person не передаётся).
	From string
	// Limit — размер страницы. Нормализуется: <1 → 10 (дефолт), >25 → 25
	// (спека §6 search).
	Limit int
	// All — собрать несколько страниц. При false (дефолт) возвращается ОДНА
	// страница; при true перебираем cursor до isPaginated=false/пустого cursor,
	// но не более maxSearchMessagesPages (cap 5, спека §6).
	All bool
}

// searchMessagesEntry — тип-посредник для распаковки одной entry ответа
// pathSearchMessages. Отличается от MessageResult тем, что Attributes.Timestamp
// здесь СТРОКА (как в JSON-ответе), а в MessageResult — int64. Конверсия
// выполняется в SearchMessages (спека §12).
type searchMessagesEntry struct {
	Title       string `json:"title"`
	Subline     string `json:"subline"`
	ResourceUrl string `json:"resourceUrl"`
	Attributes  struct {
		Conversation string `json:"conversation"`
		MessageId    int    `json:"messageId"`
		ActorType    string `json:"actorType"`
		ActorId      string `json:"actorId"`
		Timestamp    string `json:"timestamp"` // СТРОКА — парсим в int64 в SearchMessages
	} `json:"attributes"`
}

// searchMessagesData — тип-посредник для распаковки ocs.data ответа
// pathSearchMessages. Как и у talk-conversations, data — ОБЪЕКТ с полем
// entries[], плюс поля пагинации cursor/isPaginated (спека §6).
type searchMessagesData struct {
	Cursor      string                `json:"cursor"`
	IsPaginated bool                  `json:"isPaginated"`
	Entries     []searchMessagesEntry `json:"entries"`
}

// SearchMessages ищет сообщения по term через Unified talk-message provider
// (спека §8): GET pathSearchMessages?term=…&person=…&limit=…&cursor=….
//
// Контракт:
//   - term обязателен непустым (мин длина 1, спека §8): при пустом — клиентская
//     ошибка "term не может быть пустым" БЕЗ сетевого вызова;
//   - Limit нормализуется: <1 → 10, >25 → 25 (спека §6 search);
//   - From (actorId) маппится в person= ; пусто → параметр не передаётся;
//   - пагинация: при All=false (дефолт) — ОДНА страница; при All=true перебираем
//     cursor до isPaginated=false или пустого cursor, cap maxSearchMessagesPages=5
//     (спека §6);
//   - attributes.timestamp приходит СТРОКОЙ — нормализуется в int64 (спека §12);
//     при некорректном значении поле остаётся 0 (поиск не валится).
//
// Пустые entries → ненулевой пустой срез, nil error. Использует локальный
// query-aware транспорт doSearchOCSGet (общий doOCS не выставляет RawQuery).
func (c *TalkClient) SearchMessages(ctx context.Context, term string, opts SearchMessagesOpts) ([]MessageResult, error) {
	// Клиентский guard: пустой term отсекаем ДО любого HTTP-вызова (спека §8).
	if term == "" {
		return nil, errors.New("term не может быть пустым")
	}

	// Нормализация limit (спека §6 search): <1 → дефолт 10, >25 → 25.
	limit := opts.Limit
	if limit < 1 {
		limit = 10
	}
	if limit > 25 {
		limit = 25
	}

	// Потолок страниц: одна по умолчанию, до maxSearchMessagesPages при All=true.
	maxPages := 1
	if opts.All {
		maxPages = maxSearchMessagesPages
	}

	out := make([]MessageResult, 0)
	cursor := ""
	for page := 0; page < maxPages; page++ {
		// Собираем query: term и limit — всегда; person — при непустом From;
		// cursor — со второй итерации (после получения cursor из ответа).
		q := url.Values{}
		q.Set("term", term)
		q.Set("limit", strconv.Itoa(limit))
		if opts.From != "" {
			q.Set("person", opts.From)
		}
		if cursor != "" {
			q.Set("cursor", cursor)
		}

		var data searchMessagesData
		if err := c.doSearchOCSGet(ctx, pathSearchMessages, q, &data); err != nil {
			return nil, err
		}

		// Маппим entries → MessageResult, нормализуя timestamp-строку в int64
		// (спека §12). Некорректный/пустой timestamp → поле 0, поиск не валится.
		for _, e := range data.Entries {
			var mr MessageResult
			mr.Title = e.Title
			mr.Subline = e.Subline
			mr.ResourceUrl = e.ResourceUrl
			mr.Attributes.Conversation = e.Attributes.Conversation
			mr.Attributes.MessageId = e.Attributes.MessageId
			mr.Attributes.ActorType = e.Attributes.ActorType
			mr.Attributes.ActorId = e.Attributes.ActorId
			if e.Attributes.Timestamp != "" {
				if ts, err := strconv.ParseInt(e.Attributes.Timestamp, 10, 64); err == nil {
					mr.Attributes.Timestamp = ts
				}
			}
			out = append(out, mr)
		}

		// Стоп пагинации (спека §6): сервер сообщил конец (isPaginated=false)
		// или не вернул cursor. Иначе — следующая итерация с новым cursor.
		if !data.IsPaginated || data.Cursor == "" {
			break
		}
		cursor = data.Cursor
	}

	return out, nil
}
