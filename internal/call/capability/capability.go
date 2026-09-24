// Package capability читает STUN/TURN-конфигурацию сервера Nextcloud Talk
// для настройки WebRTC ICE. Спека 2026-07-19 §5 («STUN/TURN — берётся из
// capability сервера, не из env»). Никаких кредов в env пользователя — TURN-
// credentials приходят в signaling-config от сервера и живут только в памяти
// процесса (не логируются, не пишутся в argv).
//
// Источник данных — эндпоинт SignalingController::getSettings (Spreed):
//
//	GET /ocs/v2.php/apps/spreed/api/v3/signaling/settings
//
// БЕЗ token: route getSettings не принимает {token}, настройки signaling
// глобальны, не per-room (баг #2: curl с token → 404, без token → 200).
//
// apiVersion **v3** — как и pull-signaling в соседнем internal/call/signaling
// (pathSignalingFmt): оба signaling-эндпоинта Spreed restricted к v3
// (подтверждено spike-gate 2026-07-20); Call API — v4 (pathCallFmt).
// Cloud-capabilities endpoint (/ocs/v2.php/cloud/capabilities) здесь НЕ подходит:
// спред-секция там не несёт STUN/TURN, только фичи-флаги (см. _note_on_cloud_
// capabilities в testdata/signaling/capability.json).
//
// Контракт безопасности (спека §5, §9): все ошибки пропускаются через
// transport.DoOCS → transport.SanitizeErr; Authorization собирается на каждый
// запрос заново (внутри DoOCS); в URL никогда не попадают креды (только в
// заголовке Authorization). Same-host redirect-политика проставляется вызывающим
// (http.Client.CheckRedirect = transport.SameHostRedirectPolicy).
package capability

import (
	"context"
	"net/http"

	"github.com/pion/webrtc/v4"
	"github.com/stas-bool/nctalk-cli/internal/transport"
)

// Auth — псевдоним для transport.Auth (как в signaling): потребителям capability
// (cmd/nctalk-call в Task 2.9) не нужно тащить internal/transport только ради
// типа кред.
type Auth = transport.Auth

// Client — read-only клиент STUN/TURN-конфигурации сервера. Не знает про
// WebRTC-движок (pion): возвращает []webrtc.ICEServer как структуру данных,
// которую потребитель скармливает своему PeerConnection'у.
//
// Поля unexported: doer (HTTP-транспорт; *http.Client в production, мок в
// тестах), auth (BaseURL + Basic-auth креды). Методы реентранабельны.
type Client struct {
	doer transport.Doer
	auth transport.Auth
}

// New — production-style конструктор: doer обычно это *http.Client с
// настроенным Timeout и CheckRedirect = transport.SameHostRedirectPolicy
// (см. internal/client.NewTalkClient, signaling.New).
func New(auth Auth, doer transport.Doer) *Client {
	return &Client{doer: doer, auth: auth}
}

// pathSignalingSettings — путь GET /signaling/settings эндпоинта Spreed
// (SignalingController::getSettings). БЕЗ token: route не принимает {token},
// настройки signaling глобальны (баг #2: curl с token → 404, без token → 200).
// apiVersion = v3 (как и pull-signaling в signaling/types.go).
const pathSignalingSettings = "/ocs/v2.php/apps/spreed/api/v3/signaling/settings"

// Settings запрашивает signaling-settings и возвращает ICE-конфигурацию +
// поля выбора транспорта (дельта 2026-09-23 §3). ОДИН HTTP-запрос на старт —
// второй запрос settings не появляется; ticket из этого же ответа уходит в
// hpbesignaling (первичное подключение), переподключения пере-запрашивают
// settings сами (свежий ticket).
//
// Контракт:
//   - GET /ocs/v2.php/apps/spreed/api/v3/signaling/settings (БЕЗ token);
//   - stunservers/turnservers → []webrtc.ICEServer (как раньше, без изменений
//     маппинга; пустые → nil, это НЕ ошибка);
//   - SignalingMode: "" и "internal" → internal (polling-путь cmd); "external" →
//     HPB. Прочие значения не ожидаются — трактуются как internal;
//   - Server/Ticket/Userid заполняются только в external (в internal пустые);
//     TURN-credentials и ticket НЕ логируются (спека §5, дельта §4).
func (c *Client) Settings(ctx context.Context) (Settings, error) {
	var data settingsData
	if _, err := transport.DoOCS(ctx, c.doer, c.auth, http.MethodGet, pathSignalingSettings, nil, nil, false, &data); err != nil {
		return Settings{}, err
	}

	out := Settings{SignalingMode: data.SignalingMode, Server: data.Server}
	if out.SignalingMode != "external" {
		out.SignalingMode = "internal" // "" и неизвестные → internal (консервативно)
	}
	out.Userid = data.UserId
	if v := data.HelloAuthParams.V1; v.Ticket != "" {
		out.Ticket = v.Ticket // приоритет helloAuthParams["1.0"] (спайк Task 1)
		if v.Userid != "" {
			out.Userid = v.Userid
		}
		// ПУСТОЙ userid v1-блока корневой userId НЕ затирает (ревью HPB #4):
		// hello с userid="" уходит в invalid_ticket → вечный reconnect-цикл
		// без фатала (звонок висит молча).
	} else {
		out.Ticket = data.Ticket
	}

	servers := make([]webrtc.ICEServer, 0, len(data.Stunservers)+len(data.Turnservers))
	for _, s := range data.Stunservers {
		servers = append(servers, webrtc.ICEServer{URLs: s.URLs})
	}
	for _, s := range data.Turnservers {
		servers = append(servers, webrtc.ICEServer{
			URLs:           s.URLs,
			Username:       s.Username,
			Credential:     s.Credential,
			CredentialType: webrtc.ICECredentialTypePassword,
		})
	}
	if len(servers) > 0 {
		out.ICEServers = servers
	}
	return out, nil
}

// Settings — результат единственного стартового запроса signaling-settings.
// Потребители: cmd/nctalk-call, cmd/nctalk-talk (выбор транспорта + ICE).
type Settings struct {
	ICEServers    []webrtc.ICEServer // STUN+TURN (любой режим)
	SignalingMode string             // "internal" (default) | "external"
	Server        string             // URL signaling-сервера (только external)
	Ticket        string             // ticket первого подключения (только external)
	Userid        string             // userid для hello-params (обычно == NEXTCLOUD_LOGIN)
}

// settingsData — фрагмент ocs.data ответа signaling-settings. Расширен полями
// выбора транспорта (дельта 2026-09-23): signalingMode/server/ticket/
// helloAuthParams. Federation/sipDialinInfo по-прежнему не декодируются.
type settingsData struct {
	SignalingMode   string `json:"signalingMode"`
	Server          string `json:"server"`
	Ticket          string `json:"ticket"`
	UserId          string `json:"userId"`
	HelloAuthParams struct {
		V1 struct {
			Userid string `json:"userid"`
			Ticket string `json:"ticket"`
		} `json:"1.0"`
	} `json:"helloAuthParams"`
	Stunservers []struct {
		URLs []string `json:"urls"`
	} `json:"stunservers"`
	Turnservers []struct {
		URLs       []string `json:"urls"`
		Username   string   `json:"username"`
		Credential string   `json:"credential"`
	} `json:"turnservers"`
}
