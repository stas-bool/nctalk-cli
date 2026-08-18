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

// Settings запрашивает signaling-settings для комнаты и извлекает STUN/TURN.
//
// Контракт:
//   - GET /ocs/v2.php/apps/spreed/api/v3/signaling/settings (БЕЗ token, баг #2);
//   - из ocs.data читаются поля stunservers и turnservers (прочие поля —
//     signalingMode / userId / server / federation / hideWarning / sipDialinInfo —
//     игнорируются; они нужны signaling-клиенту, не capability);
//   - маппятся в []webrtc.ICEServer: STUN — только URLs, без кредов;
//     TURN — URLs + Username + Credential + CredentialType=ICECredentialTypePassword
//     (Spreed getTurnSettings возвращает password-style credential, не OAuth);
//   - порядок: STUN сначала, TURN потом — канонический WebRTC-порядок (pion
//     принимает в любом, но сохраняем для предсказуемости трассировки);
//   - при пустых/отсутствующих stunservers+turnservers возвращает (nil, nil) —
//     это НЕ ошибка (signalingMode != internal, либо STUN не нужен для хоста
//     в той же сети); pion принимает пустой список ICEServers;
//   - сетевые/HTTP-ошибки возвращаются через *transport.OCSError (DoOCS уже
//     типизирует и санитизирует их — capability НЕ дублирует sanitize).
//
// TURN-credentials НЕ логируются (спека §5): в коде нет log.Printf / fmt.Fprintln
// для ICEServer.
func (c *Client) Settings(ctx context.Context) ([]webrtc.ICEServer, error) {
	var data settingsData
	if _, err := transport.DoOCS(ctx, c.doer, c.auth, http.MethodGet, pathSignalingSettings, nil, nil, false, &data); err != nil {
		return nil, err
	}

	// Ёмкость режем по сумме длин, чтобы избежать реаллоков при append'е
	// TURN'ов после STUN'ов.
	servers := make([]webrtc.ICEServer, 0, len(data.Stunservers)+len(data.Turnservers))
	for _, s := range data.Stunservers {
		// STUN: только URLs. Username/Credential остаются zero-value (""/nil);
		// CredentialType формально равен ICECredentialTypePassword (это zero-value
		// константы iota), но pion для STUN-серверов его не валидирует —
		// валидируется только при TURN-URL, где Credential==nil дал бы ошибку.
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
	if len(servers) == 0 {
		return nil, nil
	}
	return servers, nil
}

// settingsData — фрагмент ocs.data ответа signaling-settings, нужный capability.
// Полный ответ (фикстура testdata/signaling/capability.json) содержит ещё
// signalingMode/userId/server/federation/hideWarning/sipDialinInfo — их не
// декодируем, чтобы не плодить неиспользуемые поля (и дать signaling-клиенту
// возможность читать тот же ответ независимо в будущем). Json-теги совпадают с
// ключами фикстуры (snake_case, как в PHP-бэкенде Spreed).
type settingsData struct {
	Stunservers []struct {
		URLs []string `json:"urls"`
	} `json:"stunservers"`
	Turnservers []struct {
		URLs       []string `json:"urls"`
		Username   string   `json:"username"`
		Credential string   `json:"credential"`
	} `json:"turnservers"`
}
