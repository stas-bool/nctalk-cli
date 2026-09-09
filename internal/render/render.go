package render

// Текстовые (табличные) рендереры структур client.* по спеке §6.
// JSON-рендереры живут в render_json.go; FormatTime — в format_time.go.
//
// Таблицы строятся через text/tabwriter (stdlib, без внешних libs).
// Многобайтовые символы (кириллица, эмодзи) влияют на выравнивание
// (tabwriter считает байты), но таблицы остаются читаемыми — MVP не делает
// полноценной шириночной раскладки.

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/stas-bool/nctalk-cli/internal/client"
)

// newTabwriter создаёт табрайтер с общими для всех таблиц параметрами:
// мин. ширина 0, таб 0, паддинг 2 символа, разделитель — пробел.
func newTabwriter(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
}

// RoomsTable выводит список комнат табличным форматом (спека §6 rooms list):
//
//	ТИП | ЧАТ | TOKEN | НЕПРОЧИТАНО | АКТЁР | ПОСЛЕДНЕЕ (время, автор, превью)
//
// actorId комнаты всегда присутствует отдельной колонкой (для type=1 —
// собеседник, спека §6, §12).
func RoomsTable(w io.Writer, rooms []client.Room) error {
	tw := newTabwriter(w)
	if _, err := fmt.Fprintln(tw, "ТИП\tЧАТ\tTOKEN\tНЕПРОЧИТАНО\tАКТЁР\tПОСЛЕДНЕЕ"); err != nil {
		return err
	}
	for _, r := range rooms {
		if _, err := fmt.Fprintf(tw, "%d\t%s\t%s\t%d\t%s\t%s\n",
			int(r.Type), r.DisplayName, r.Token, r.UnreadMessages, r.ActorId,
			lastMessagePreview(r.LastMessage)); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// lastMessagePreview собирает превью последнего сообщения комнаты в формате
// "время автор: текст". При отсутствии сообщений (LastMessage == nil) — "—".
// Если текст пуст, берётся systemMessage (актуально для системных сообщений).
func lastMessagePreview(m *client.Message) string {
	if m == nil {
		return "—"
	}
	preview := m.Message
	if preview == "" {
		preview = m.SystemMessage
	}
	return fmt.Sprintf("%s %s: %s", FormatTime(m.Timestamp), m.ActorDisplayName, preview)
}

// MessagesTable выводит сообщения построчно (спека §6 chat show):
//
//	[id] время автор: текст   👍×2 ✅×1
//
// Реакции-счётчики берутся из Message.Reactions БЕЗ N+1 (печать через range).
// Подстановка messageParameters НЕ выполняется здесь — это слой CLI (Task 3.2);
// рендерится поле Message как есть.
func MessagesTable(w io.Writer, msgs []client.Message) error {
	for _, m := range msgs {
		if _, err := fmt.Fprintf(w, "[%d] %s %s: %s%s\n",
			m.Id, FormatTime(m.Timestamp), m.ActorDisplayName, m.Message,
			reactionsSuffix(m.Reactions)); err != nil {
			return err
		}
	}
	return nil
}

// reactionsSuffix формирует суффикс вида "   👍×2 ✅×1" из мапы реакций
// (спека §6). Эмодзи отсортированы для детерминированного вывода. Пустая
// мапа → пустая строка. Счётчик — unicode-знак × (U+00D7), как в спеке.
func reactionsSuffix(reactions map[string]int) string {
	if len(reactions) == 0 {
		return ""
	}
	keys := make([]string, 0, len(reactions))
	for k := range reactions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString("   ")
		b.WriteString(k)
		b.WriteString("×")
		b.WriteString(strconv.Itoa(reactions[k]))
	}
	return b.String()
}

// ConversationResultsTable выводит результаты rooms search (спека §6):
// НАЗВАНИЕ | TOKEN. ConversationResult несёт только Title+Token.
func ConversationResultsTable(w io.Writer, rs []client.ConversationResult) error {
	tw := newTabwriter(w)
	if _, err := fmt.Fprintln(tw, "НАЗВАНИЕ\tTOKEN"); err != nil {
		return err
	}
	for _, r := range rs {
		if _, err := fmt.Fprintf(tw, "%s\t%s\n", r.Title, r.Token); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// MessageResultsTable выводит результаты search (спека §6):
//
//	время | автор | chat#messageId | текст
//
// Автор — Title (имя автора из Unified-поиска), chat#messageId —
// token#messageId, текст — Subline.
func MessageResultsTable(w io.Writer, rs []client.MessageResult) error {
	tw := newTabwriter(w)
	if _, err := fmt.Fprintln(tw, "ВРЕМЯ\tАВТОР\tЧАТ#ID\tТЕКСТ"); err != nil {
		return err
	}
	for _, r := range rs {
		chatID := fmt.Sprintf("%s#%d", r.Attributes.Conversation, r.Attributes.MessageId)
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			FormatTime(r.Attributes.Timestamp), r.Title, chatID, r.Subline); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// ReactionsText выводит реакции построчно (спека §6 reactions get):
//
//	реакция → [авторы]
//
// Эмодзи отсортированы для детерминированного вывода. Пустая map → строка
// "реакций нет" (exit 0, спека §6).
func ReactionsText(w io.Writer, rs map[string][]client.ReactionActor) error {
	if len(rs) == 0 {
		_, err := fmt.Fprintln(w, "реакций нет")
		return err
	}
	keys := make([]string, 0, len(rs))
	for k := range rs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		actors := rs[k]
		names := make([]string, 0, len(actors))
		for _, a := range actors {
			// Предпочитаем отображаемое имя; fallback на actorId при пустом.
			name := a.ActorDisplayName
			if name == "" {
				name = a.ActorId
			}
			names = append(names, name)
		}
		if _, err := fmt.Fprintf(w, "%s → [%s]\n", k, strings.Join(names, ", ")); err != nil {
			return err
		}
	}
	return nil
}

// NewMessageID выводит id отправленного сообщения (текст) — спека §6 chat send.
func NewMessageID(w io.Writer, id int) error {
	_, err := fmt.Fprintf(w, "%d\n", id)
	return err
}

// Candidates выводит список комнат для exit 3 (неоднозначное разрешение <room>,
// спека §7): ТИП | имя | token.
func Candidates(w io.Writer, rooms []client.Room) error {
	tw := newTabwriter(w)
	for _, r := range rooms {
		if _, err := fmt.Fprintf(tw, "%d\t%s\t%s\n", int(r.Type), r.DisplayName, r.Token); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// participantRoleNames — тексты ролей по participantType (спека-дельта
// 2026-09-09 §2–3: 1–6; значения 4–6 — из официальной документации Talk,
// живой выборкой подтверждены только 1–3). Маппинг живёт в render — это
// представление, не модель.
var participantRoleNames = map[int]string{
	1: "владелец",
	2: "модератор",
	3: "участник",
	4: "гость",
	5: "по ссылке",
	6: "гость-модератор",
}

// participantRole — текст роли; неизвестное число (format-drift: новые
// значения сервера) печатается самим числом, чтобы не ломать вывод.
func participantRole(t int) string {
	if s, ok := participantRoleNames[t]; ok {
		return s
	}
	return strconv.Itoa(t)
}

// participantName — итоговое имя колонки ИМЯ: displayName, при пустом
// (гость без имени) — actorId (тот же fallback, что в reactions get).
func participantName(p client.Participant) string {
	if p.DisplayName != "" {
		return p.DisplayName
	}
	return p.ActorId
}

// ParticipantsTable выводит участников табличным форматом (спека-дельта
// 2026-09-09 §2):
//
//	ИМЯ | РОЛЬ | ОНЛАЙН | ID
//
// Сортировку делает вызывающий (cli-слой) — здесь только представление.
// ОНЛАЙН: непустой sessionIds = есть живая сессия.
func ParticipantsTable(w io.Writer, ps []client.Participant) error {
	tw := newTabwriter(w)
	if _, err := fmt.Fprintln(tw, "ИМЯ\tРОЛЬ\tОНЛАЙН\tID"); err != nil {
		return err
	}
	for _, p := range ps {
		online := "нет"
		if len(p.SessionIds) > 0 {
			online = "да"
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			participantName(p), participantRole(p.ParticipantType), online, p.ActorId); err != nil {
			return err
		}
	}
	return tw.Flush()
}
