//go:build integration

// Файл интеграционного теста signaling на боевом сервере Nextcloud Talk
// (Task 2.3, спека 2026-07-19 §7). Компилируется ТОЛЬКО с тегом
// `-tags=integration` — заголовок //go:build integration выше исключает его из
// обычного `go test ./...`. Таким образом CI/дефолтный прогон этот тест не
// видит; запуск делает человек (см. docs/integration-run.md, раздел
// «Signaling integration (Task 2.3)»).
//
// Источник кредов — окружение процесса (NEXTCLOUD_URL/LOGIN/PASS), читается
// через config.Load(). Никаких хардкодов и t.Setenv — подразумевается, что
// креды уже в env. При отсутствии любого обязательного env-переменного тест
// скипается через t.Skip (НЕ падает) — это позволяет прогонять соседние тесты
// на машине без кредов без ложных сбоев.
//
// Сценарий — мутационный: JoinCall меняет состояние сервера (добавляет нас в
// список участников звонка). Поэтому тест защищён двухслойно (по аналогии с
// TestIntegration_SendMessage в internal/client):
//  1. env NCTALK_INTEGRATION_ROOM — token тестовой комнаты;
//  2. env NCTALK_INTEGRATION_CALL=1 — явный opt-in (как NCTALK_INTEGRATION_SEND
//     для SendMessage).
//
// Без любого из них — t.Skip (НЕ падает). Без NEXTCLOUD_URL/LOGIN/PASS — тоже
// t.Skip (через config.Load).
//
// Чего тест НЕ делает: не закрывает PeerConnection (WebRTC-слоя здесь нет —
// это spike signaling'а, не audio). Не удаляет «мусор» после себя (состояние
// «был в звонке» само очистится по LeaveCall; если LeaveCall не отработал —
// сервер приберёт участника по ping-timeout, обычно ~60–90с).
package signaling

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/stas/nctalk/internal/config"
	"github.com/stas/nctalk/internal/exit"
	"github.com/stas/nctalk/internal/transport"
)

// signalingPollTimeout — потолок ожидания signaling-событий в тесте. Spreed
// держит long-poll открытым до ~30с при отсутствии событий; но ПЕРВЫЙ poll
// сразу после JoinCall обычно отдаёт немедленный snapshot usersInRoom (наш
// собственный вход в комнату триггерит рассылку). 5с достаточно для боевого
// сервера в OK-сети; если за 5с ничего не пришло — это диагностический сигнал
// (см. возможные причины в t.Fatalf ниже).
const signalingPollTimeout = 5 * time.Second

// signalingCallTimeout — пер-операционный потолок на JoinCall/LeaveCall
// (мутации). 30с — стандартный OCS-потолок в проекте (см. integrationTimeout
// в internal/client/integration_test.go).
const signalingCallTimeout = 30 * time.Second

// TestSignalingPollLoop_JoinGetOneEventLeave — минимальный e2e-сценарий
// (Task 2.3, спека §7):
//
//  1. JoinCall(token, flags=3) — войти в звонок (IN_CALL|WITH_AUDIO = sendrecv).
//  2. PollLoop в goroutine'е с 5с потолком по ctx.
//  3. Ожидаем хотя бы одно Event (любого Kind; цель — собрать наблюдения о
//     реальном формате, не строгая валидация протокола). Логируем каждое
//     наблюдённое событие с деталями (для последующей сверки с фикстурами
//     Task 2.1 и, при необходимости, обновления testdata/signaling/*).
//  4. LeaveCall(token) — выйти из звонка (deferred, отдельный ctx).
//
// Сценарий НЕ проверяет полный signaling-обмен (answer/candidate) — для этого
// нужен второй участник с браузером. Цель — подтвердить, что (а) v4-эндпоинт
// signaling'а отвечает (или выявить, что нужен v3), (б) формат usersInRoom
// разбирается парсером Task 2.2 без ошибок, (в) message.data это действительно
// JSON-строка (а не объект).
//
// БЕЗОПАСНОСТЬ: креды не логируем. t.Logf выводит только: token комнаты, Kind
// события, количественные метрики (inCall flags, длину SDP, count users).
// actorId/sessionId могут фигурировать — это идентификаторы собеседников по
// тестовой комнате, видимые только оператору прогона (не в CI, не в публичных
// логах). NEXTCLOUD_PASS и URL с userinfo никогда не попадают в вывод
// (transport.SanitizeErr чистит URL; config.Load в ошибках светит только имена
// env-переменных).
func TestSignalingPollLoop_JoinGetOneEventLeave(t *testing.T) {
	// Двухслойная защита от случайного мутационного запуска (JoinCall меняет
	// состояние сервера — добавляет нас в звонок). Без NCTALK_INTEGRATION_ROOM
	// или без NCTALK_INTEGRATION_CALL=1 — пропуск (НЕ падаем), как для
	// TestIntegration_SendMessage.
	token := os.Getenv("NCTALK_INTEGRATION_ROOM")
	if token == "" {
		t.Skip("NCTALK_INTEGRATION_ROOM не задан — пропуск signaling integration test (нет целевой комнаты)")
	}
	if got := os.Getenv("NCTALK_INTEGRATION_CALL"); got != "1" {
		t.Skip("NCTALK_INTEGRATION_CALL != 1 — пропуск signaling integration test (мутация: JoinCall)")
	}

	// config.Load возвращает ИМЯ недостающего env-переменной, не значение (спека
	// §5, §9). t.Skip (не t.Fatal) — отсутствие кредов трактуем как «не наш
	// контур», а не «тест упал».
	cfg, err := config.Load()
	if err != nil {
		t.Skipf("пропуск signaling integration test: %v", err)
	}

	// Парсим BaseURL. config.Load уже валидировал URL, но отдаёт строку, а не
	// *url.URL. Ошибка здесь — баг config.Load; тестируем как t.Fatalf, не skip.
	baseURL, err := url.Parse(cfg.BaseURL)
	if err != nil {
		t.Fatalf("парсинг BaseURL %q (должен быть валиден после config.Load): %v", cfg.BaseURL, err)
	}

	// http.Client с same-host redirect-политикой (блокирует cross-host auth-leak
	// и https→http downgrade — спека §5). Таймаут на клиент = cfg.Timeout
	// (дефолт 30с из config.DefaultTimeout). Любой redirect анализируется
	// ДО отправки вторичного запроса с Authorization.
	hc := &http.Client{
		Transport:     http.DefaultTransport,
		Timeout:       cfg.Timeout,
		CheckRedirect: transport.SameHostRedirectPolicy,
	}
	auth := transport.Auth{BaseURL: baseURL, Login: cfg.Login, Password: cfg.Password}
	c := New(auth, hc)

	// JoinCall: flags=3 = IN_CALL|WITH_AUDIO (Spreed-константы; см. тип User).
	// Мы НЕ отправляем реальный аудиопоток — WebRTC-слоя здесь нет, флаг это
	// метаданные для других участников (peer-слой Task 2.4+ будет делать
	// реальный audio через pion). Для signaling-loop на стороне сервера флаги
	// значения не имеют — достаточно того, что мы в звонке.
	t.Logf("JoinCall token=%s flags=3 (IN_CALL|WITH_AUDIO = sendrecv)", token)
	joinCtx, joinCancel := context.WithTimeout(context.Background(), signalingCallTimeout)
	defer joinCancel()
	if err := c.JoinCall(joinCtx, token, 3); err != nil {
		t.Fatalf("JoinCall(%s, flags=3): %v — проверьте, что token существует и у пользователя есть права на звонок",
			token, err)
	}

	// Гарантированный leave при любом исходе PollLoop. ОТДЕЛЬНЫЙ ctx —
	// pollCtx к этому моменту уже отменён (5с истекли), для LeaveCall нужен
	// свежий. t.Errorf (не Fatalf) — даже при ошибке leave тест должен
	// продолжить собрать корректный отчёт по событиям.
	defer func() {
		leaveCtx, leaveCancel := context.WithTimeout(context.Background(), signalingCallTimeout)
		defer leaveCancel()
		if err := c.LeaveCall(leaveCtx, token); err != nil {
			t.Errorf("LeaveCall(%s): %v — звонок мог остаться висеть; проверьте dashboard Nextcloud "+
				"(участник должен уйти по ping-timeout ~60–90с)", token, err)
		} else {
			t.Logf("LeaveCall token=%s: OK", token)
		}
	}()

	// PollLoop крутится в goroutine'е. Канал событий буферизован (8) — на случай
	// пачки сообщений (offer + candidate + usersInRoom) PollLoop не блокирует
	// на send и принимает все записи от сервера. errCh — принимает return-value
	// PollLoop (nil на штатном ctx.Done, error на фатальной 401/403/404).
	events := make(chan Event, 8)
	pollCtx, pollCancel := context.WithTimeout(context.Background(), signalingPollTimeout)
	defer pollCancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- c.PollLoop(pollCtx, token, events)
	}()

	// Собираем события до истечения pollCtx (5с). Копим ВСЕ наблюдённые события
	// для итогового лога — это данные для сверки формата с фикстурами Task 2.1.
	// Если PollLoop завершился раньше ctx (фатальная 401/403/404) — продолжаем
	// крутить select до конца pollCtx: буфер events может ещё содержать EvError
	// (PollLoop отправляет его перед return), и мы хотим его вытащить и залогить.
	var observed []Event
	var pollErr error
loop:
	for {
		select {
		case ev := <-events:
			observed = append(observed, ev)
		case err := <-errCh:
			// PollLoop завершился (nil = штатный ctx.Done; error = фатальная
			// 401/403/404). Не выходим из loop'а — продолжаем собирать остаток
			// events из буфера до истечения pollCtx: если PollLoop успел
			// отправить EvError в канал перед return'ом, мы хотим его вытащить,
			// а не потерять из-за случайного порядка выбора case'ов в select.
			pollErr = err
		case <-pollCtx.Done():
			break loop
		}
	}
	if pollErr != nil {
		t.Errorf("PollLoop завершился раньше контекста: %v — вероятно фатальная ошибка signaling'а (401/403/404)",
			pollErr)
	}

	// Горутина PollLoop к этому моменту либо уже вышла (тогда errCh уже пуст),
	// либо вот-вот выйдет по ctx (PollLoop возвращает nil на ctx.Done — см.
	// signaling.go). errCh буферизован (1), send не блокирует — утечки нет.
	// Состояние observed / pollErr трогаем только из main-goroutine теста,
	// data-race нет. Специально дожидаться выхода goroutine не нужно: defer
	// pollCancel() в конце теста освободит ctx, goroutine корректно завершится.

	// Hard assert: хотя бы одно Event должно прийти. Пользователь только что
	// JoinCall'нул — сервер обязан включить его в usersInRoom snapshot на
	// ближайшем poll'е. 0 событий означает: (1) путь v4 неверен и серв вернул
	// не-OCS-тело (HTML 404), но transport классифицировал как backoff без
	// fatal → PollLoop крутился 5с и вышел по ctx; (2) long-poll держался > 5с;
	// (3) сетевая проблема (но тогда errCh быстрее сработал бы).
	if len(observed) == 0 {
		t.Fatalf("PollLoop за %s не вернул ни одного Event'а — сервер не отдал usersInRoom snapshot. "+
			"Возможные причины: (1) путь /api/v4/signaling/ неверен → 404 (см. comment в types.go pathSignalingFmt — попробовать v3); "+
			"(2) long-poll держится дольше %s — увеличить signalingPollTimeout; "+
			"(3) JoinCall прошёл, но signaling отключён на сервере (hpb_mode?). "+
			"Запустите с Diagnostic: curl -u $LOGIN -H 'OCS-APIRequest: true' $NEXTCLOUD_URL/ocs/v2.php/apps/spreed/api/v3/signaling/%s",
			signalingPollTimeout, signalingPollTimeout, token)
	}

	// Логируем каждое событие с деталями для последующей сверки с фикстурами
	// Task 2.1. Это ДЛЯ ЧЕЛОВЕКА данные — что реально пришло с сервера.
	for i, ev := range observed {
		t.Logf("Event[%d]: Kind=%s", i, kindName(ev.Kind))
		switch ev.Kind {
		case EvUsersUpdated:
			// usersInRoom snapshot. Поле raw.sessionId — ключ для собственного
			// фильтра в peer-слое (Task 2.4). InCall bitmask: 1=IN_CALL,
			// 2=WITH_AUDIO, 4=WITH_VIDEO (Spreed-константы).
			t.Logf("  usersInRoom: %d participant(s)", len(ev.Users))
			for j, u := range ev.Users {
				t.Logf("  user[%d]: sessionId=%s actorType=%s actorId=%s inCall=%d",
					j, u.SessionId, u.ActorType, u.ActorId, u.InCall)
			}
		case EvOffer, EvAnswer:
			// SDP от удалённого пира. Логируем длину и первые 80 символов для
			// быстрой сверки структуры (m-line, codec numbers).
			t.Logf("  %s from=%s SDPlen=%d (первые 80 chars): %.80s",
				kindName(ev.Kind), ev.From, len(ev.SDP), ev.SDP)
		case EvCandidate:
			// Trickle ICE candidate от удалённого пира. End-of-candidates
			// marker — payload.candidate === "" (фикстура candidate.json,
			// 3-я запись). SDPMLineIndex/SDPMid — указатели (%v печатает <nil>
			// для nil, само значение иначе).
			t.Logf("  candidate from=%s: candidate=%q sdpMLineIndex=%v sdpMid=%v",
				ev.From, ev.Candidate.Candidate, ev.Candidate.SDPMLineIndex, ev.Candidate.SDPMid)
		case EvError:
			// Фатальная ошибка signaling-loop. ExitError — ЗНАЧИМЫЙ тип (value),
			// не указатель: exit.Exit возвращает значение, errors.As ищет именно
			// exit.ExitError в цепочке (см. signaling_test.go line 509–513).
			var ee exit.ExitError
			if errors.As(ev.Err, &ee) {
				t.Errorf("  EvError: code=%d (1=network/auth, 2=not found) err=%v",
					ee.Code, ee.Err)
			} else {
				t.Errorf("  EvError (не *exit.ExitError в цепочке): %v", ev.Err)
			}
		}
	}

	// Мягкая валидация протокола: первый Event обычно EvUsersUpdated (snapshot
	// на вход в комнату) ИЛИ EvError (если путь/креды неверны — это и есть
	// сигнал к обновлению pathSignalingFmt). EvOffer первым был бы странным
	// (мы только JoinCall'нули, никто не успел нам послать offer), но и это
	// данные для анализа, не жёсткий fail.
	first := observed[0].Kind
	if first != EvUsersUpdated && first != EvError {
		t.Logf("INFO: первый Event Kind=%s — не EvUsersUpdated и не EvError; "+
			"это необычно, но не ошибка. Возможно, в комнате уже был активный пир.",
			kindName(first))
	}
}

// kindName — человекочитаемое имя EventKind для логов. Не используется вне теста.
func kindName(k EventKind) string {
	switch k {
	case EvUsersUpdated:
		return "EvUsersUpdated"
	case EvOffer:
		return "EvOffer"
	case EvAnswer:
		return "EvAnswer"
	case EvCandidate:
		return "EvCandidate"
	case EvError:
		return "EvError"
	default:
		return "EvUnknown"
	}
}
