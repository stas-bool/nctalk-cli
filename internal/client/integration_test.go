//go:build integration

// Файл интеграционных тестов (Task 6.1). Компилируется ТОЛЬКО с тегом
// `-tags=integration` — заголовок //go:build integration выше исключает его из
// обычного `go test ./...`. Таким образом CI/дефолтный прогон эти тесты не
// видит; запуск на боевом сервере делает человек (см. docs/integration-run.md).
//
// Источник кредов — окружение процесса (NEXTCLOUD_URL/LOGIN/PASS), читается
// через config.Load(). Никаких хардкода и t.Setenv — подразумевается, что
// креды уже в env. При отсутствии любого обязательного env-переменного тест
// скипается через t.Skip (НЕ падает) — это позволяет прогонять отдельные тесты
// на машине, гдеEnv пока не настроены, без ложных сбоев.
//
// Читающие сценарии (ListRooms/SearchRooms/GetChat/SearchMessages/GetReactions)
// безопасны и не меняют состояние сервера. Мутационный SendMessage защищён
// отдельным флагом NCTALK_INTEGRATION_SEND=1 и целевым NCTALK_INTEGRATION_ROOM
// (спека §12 «мутация, не проверено»).
package client

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/stas/nctalk/internal/config"
)

// integrationTimeout — пер-тестовый потолок времени на один сетевой сценарий.
// 30s достаточно для listrooms/chat на реальном сервере через интернет; если
// сервер/сеть медленнее — тест, скорее всего, выявляет реальную проблему.
const integrationTimeout = 30 * time.Second

// integrationClient читает креды из env через config.Load и возвращает готовый
// TalkClient. Скипает тест (t.Skip) при отсутствии любого из обязательных
// env-переменных NEXTCLOUD_URL/LOGIN/PASS — это не ошибка теста, а индикатор
// «креды не настроены, интеграционный прогон не предназначен для этого окружения».
func integrationClient(t *testing.T) *TalkClient {
	t.Helper()
	// config.Load возвращает человекочитаемую ошибку с ИМЕНЕМ недостающего env
	// (значения не светятся — спека §5, §9). t.Skip (не t.Fatal) — чтобы
	// отсутствие кредов трактовалось как «пропуск», а не «падение».
	cfg, err := config.Load()
	if err != nil {
		t.Skipf("пропуск интеграционного теста: %v", err)
	}
	return NewTalkClient(cfg)
}

// TestIntegration_ListRooms проверяет базовый читающий эндпоинт /room (спека §6,
// §12): ListRooms без фильтров отдаёт >=1 комнаты; если среди них есть type=1
// (one-to-one), её ActorId должен быть непустым (спека §12: для type=1 это
// собеседник, а не текущий пользователь).
func TestIntegration_ListRooms(t *testing.T) {
	c := integrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	rooms, err := c.ListRooms(ctx, ListRoomsOpts{})
	if err != nil {
		t.Fatalf("ListRooms: %v", err)
	}
	if len(rooms) < 1 {
		t.Fatalf("ListRooms вернул 0 комнат — ожидается хотя бы одна на реальном аккаунте")
	}
	t.Logf("получено комнат: %d", len(rooms))

	// Если есть type=1 (one-to-one), проверяем что ActorId смаппился. Это
	// не assert на уровне всего списка: аккаунт может не иметь ни одного
	// one-to-one-чата — тогда проверка молча пропускается.
	for _, r := range rooms {
		if r.Type != RoomType(1) {
			continue
		}
		if r.ActorId == "" {
			t.Errorf("type=1 комната token=%s: ActorId пуст (должен быть собеседник)", r.Token)
		}
		t.Logf("type=1 найдена: token=%s actorId=%s displayName=%q", r.Token, r.ActorId, r.DisplayName)
		break
	}
}

// TestIntegration_SearchRooms дёргает Unified-поиск talk-conversations по
// короткому term (спека §8). Сценарий безопасный (GET). Не требуем непустого
// результата — реальный аккаунт может не иметь комнат с такой подстрокой; важна
// лишь отстутствие ошибки и валидная структура ответа.
func TestIntegration_SearchRooms(t *testing.T) {
	c := integrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	// Короткий term — одна буква. Серверный Unified-поиск может отдать пустой
	// массив, это нормально; guard на пустой term у нас клиентский (см. search.go).
	results, err := c.SearchRooms(ctx, "a", 0)
	if err != nil {
		t.Fatalf("SearchRooms: %v", err)
	}
	t.Logf("найдено комнат через Unified search: %d", len(results))

	// Если результаты есть — у каждого должно быть непустое Title (согласно
	// контракту ConversationResult). Token может быть пустым в редких случаях,
	// поэтому не assert'им.
	for _, r := range results {
		if r.Title == "" {
			t.Errorf("SearchRooms: entry с пустым Title (token=%q)", r.Token)
		}
	}
}

// TestIntegration_GetChat берёт первый token из ListRooms и тянет последние
// сообщения через GetChat с Limit=5 (спека §6 chat, §12). Проверяем потолок
// выборки (len <= 5) и базовый маппинг Message: Id (int), Timestamp (int64),
// Token совпадает с запрошенным.
func TestIntegration_GetChat(t *testing.T) {
	c := integrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	// ListRooms даёт нам реальный token — тот, на который у пользователя есть
	// права. Так тест не хардкодит token и устойчив к изменениям аккаунта.
	rooms, err := c.ListRooms(ctx, ListRoomsOpts{})
	if err != nil {
		t.Fatalf("ListRooms: %v", err)
	}
	if len(rooms) == 0 {
		t.Skip("нет комнат для выбора token — пропуск GetChat")
	}
	token := rooms[0].Token
	if token == "" {
		t.Fatal("первая комната имеет пустой token")
	}
	t.Logf("выбрана комната token=%s displayName=%q", token, rooms[0].DisplayName)

	msgs, err := c.GetChat(ctx, token, GetChatOpts{Limit: 5})
	if err != nil {
		t.Fatalf("GetChat(%s): %v", token, err)
	}
	if len(msgs) > 5 {
		t.Errorf("GetChat Limit=5: получено %d сообщений, ожидалось <= 5", len(msgs))
	}
	t.Logf("получено сообщений: %d", len(msgs))

	// Проверка маппинга: каждое сообщение должно нести token комнаты и
	// монотонно-положительный Timestamp. Id может быть 0 только у системных
	// сообщений в пограничных случаях — не assert'им.
	for _, m := range msgs {
		if m.Token != "" && m.Token != token {
			t.Errorf("message id=%d: Token=%q, хочу %q", m.Id, m.Token, token)
		}
		if m.Timestamp <= 0 {
			t.Errorf("message id=%d: Timestamp=%d, хочу > 0", m.Id, m.Timestamp)
		}
	}
}

// TestIntegration_SearchMessages проверяет Unified talk-message provider (спека
// §8, §12). Ключевой момент — нормализация attributes.timestamp: в JSON это
// СТРОКА, клиент маппит её в int64. Проверяем, что для каждого результата
// Timestamp либо 0 (сервер не прислал / некорректный), либо положительное число.
func TestIntegration_SearchMessages(t *testing.T) {
	c := integrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	// Короткий term — реальный аккаунт с историей почти всегда что-то отдаёт.
	// Но возможна и пустота (новый аккаунт) — это не ошибка.
	results, err := c.SearchMessages(ctx, "a", SearchMessagesOpts{Limit: 5})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	t.Logf("найдено сообщений через Unified search: %d", len(results))

	for _, mr := range results {
		// Timestamp у MessageResult — int64 в самом типе. Проверяем валидность
		// значения: либо 0 (сервер не вернул timestamp), либо > 0 (распарсенное
		// число). Отрицательных быть не должно.
		if mr.Attributes.Timestamp < 0 {
			t.Errorf("SearchMessages: отрицательный Timestamp=%d (title=%q)",
				mr.Attributes.Timestamp, mr.Title)
		}
	}
}

// TestIntegration_GetReactions проверяет reactions-эндпоинт на реальном
// сообщении (спека §6 `reactions get`, §12). Берём первое сообщение с Id > 0 из
// GetChat — гарантированно существующее. Если у него нет реакций (пустая map),
// содержимое не проверяем — это нормальный сценарий «реакций нет».
func TestIntegration_GetReactions(t *testing.T) {
	c := integrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	rooms, err := c.ListRooms(ctx, ListRoomsOpts{})
	if err != nil {
		t.Fatalf("ListRooms: %v", err)
	}
	if len(rooms) == 0 {
		t.Skip("нет комнат — пропуск GetReactions")
	}
	token := rooms[0].Token

	msgs, err := c.GetChat(ctx, token, GetChatOpts{Limit: 20})
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}

	// Ищем сообщение с Id > 0 — у системных сообщений Id может быть 0/маленьким,
	// нам нужна валидная цель для GET /reaction/{token}/{messageId}.
	var msgId int
	for _, m := range msgs {
		if m.Id > 0 {
			msgId = m.Id
			break
		}
	}
	if msgId == 0 {
		t.Skip("в выбранной комнате нет сообщений с Id > 0 — пропуск GetReactions")
	}
	t.Logf("выбрано сообщение id=%d в token=%s", msgId, token)

	reactions, err := c.GetReactions(ctx, token, msgId)
	if err != nil {
		t.Fatalf("GetReactions(%s, %d): %v", token, msgId, err)
	}
	// GetReactions по контракту возвращает non-nil map даже при пустом data={}.
	if reactions == nil {
		t.Fatal("GetReactions вернул nil map — ожидалась non-nil (возможно пустая)")
	}
	// Если реакций нет — проверка содержимого бессмысленна; логируем и выходим.
	if len(reactions) == 0 {
		t.Logf("у сообщения %d нет реакций — проверка содержимого пропущена", msgId)
		return
	}
	// Реакции есть — проверяем структуру: каждый emoji-ключ ведёт к срезу с
	// хотя бы одним актёром.
	for emoji, actors := range reactions {
		if len(actors) == 0 {
			t.Errorf("реакция %q: пустой список актёров", emoji)
		}
	}
	t.Logf("у сообщения %d реакций: %d типов", msgId, len(reactions))
}

// TestIntegration_SendMessage — ЕДИНСТВЕННЫЙ мутационный сценарий (спека §12
// «не проверено»). Двухслойная защита от случайного запуска:
//  1. env NCTALK_INTEGRATION_SEND=1 — явный opt-in (без него skip);
//  2. env NCTALK_INTEGRATION_ROOM — token тестовой комнаты (без него skip).
//
// Отправляем уникальное сообщение (с временной меткой в тексте, чтобы не
// путать с предыдущими прогонами) и проверяем, что сервер вернул id > 0.
// Чистку (удаление) не делаем — тестовая комната предполагает мусор.
func TestIntegration_SendMessage(t *testing.T) {
	if got := os.Getenv("NCTALK_INTEGRATION_SEND"); got != "1" {
		t.Skip("NCTALK_INTEGRATION_SEND != 1 — пропускаю мутационный тест SendMessage")
	}
	token := os.Getenv("NCTALK_INTEGRATION_ROOM")
	if token == "" {
		t.Skip("NCTALK_INTEGRATION_ROOM не задан — пропускаю SendMessage (нет целевой тестовой комнаты)")
	}

	c := integrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	// Уникальный суффикс через unix-время — чтобы сообщение было отличимо от
	// предыдущих прогонов и не триггерило дедуп по одинаковому тексту.
	text := "nctalk integration test " + strconv.FormatInt(time.Now().Unix(), 10)
	t.Logf("отправка в token=%s: %q", token, text)

	id, err := c.SendMessage(ctx, token, SendMessageOpts{Message: text})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if id <= 0 {
		t.Errorf("SendMessage: сервер вернул id=%d, ожидался > 0", id)
	}
	t.Logf("отправлено, id=%d", id)
}
