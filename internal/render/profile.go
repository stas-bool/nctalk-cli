package render

// profile.go — вывод профиля пользователя (спека-дельта 2026-10-22
// `users show`, §2): текстовый блок «ключ: значение» и --json.

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/stas-bool/nctalk-cli/internal/client"
)

// ProfileText выводит профиль блоком «ключ: значение» (спека-дельта §2):
//
//	Имя / ID / Должность / Заголовок / Организация / Город / Местоимения /
//	Часовой пояс / контакты (<type>: <value>) / О себе (последним блоком)
//
// Строки с пустыми значениями не печатаются. Однострочные значения
// схлопывают пробельные символы (\n, \t, повторы) в один пробел — иначе
// tabwriter принял бы их за разделители ячеек/строк. Биография печатается
// ПОСЛЕ tw.Flush() прямо в w, как есть (табы и переводы строк сохраняются).
// Местное время пояса считается от now (инъекция времени для тестов).
func ProfileText(w io.Writer, p *client.Profile, now time.Time) error {
	tw := newTabwriter(w)
	row := func(label, value string) error {
		label, value = collapseSpace(label), collapseSpace(value)
		if label == "" || value == "" {
			return nil
		}
		_, err := fmt.Fprintf(tw, "%s:\t%s\n", label, value)
		return err
	}

	rows := [][2]string{
		{"Имя", p.DisplayName},
		{"ID", p.UserId},
		{"Должность", p.Role},
		{"Заголовок", p.Headline},
		{"Организация", p.Organisation},
		{"Город", p.Address},
		{"Местоимения", p.Pronouns},
		{"Часовой пояс", profileTimezone(p, now)},
	}
	for _, c := range p.Contacts {
		rows = append(rows, [2]string{c.Type, c.Value})
	}
	for _, r := range rows {
		if err := row(r[0], r[1]); err != nil {
			return err
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	if strings.TrimSpace(p.Biography) == "" {
		return nil
	}
	bio := p.Biography
	if !strings.HasSuffix(bio, "\n") {
		bio += "\n"
	}
	_, err := fmt.Fprintf(w, "О себе:\n%s", bio)
	return err
}

// ProfileJSON выводит каноническую модель Profile (спека-дельта §2, --json).
// Contacts nil → [] (детерминированный --json; клиент и так отдаёт non-nil,
// guard — защита от вызова с вручную собранной моделью).
func ProfileJSON(w io.Writer, p *client.Profile) error {
	out := *p
	if out.Contacts == nil {
		out.Contacts = []client.ProfileContact{}
	}
	return writeJSON(w, out)
}

// profileTimezone — значение строки «Часовой пояс»:
// "<timezone> (UTC±H[:MM]), сейчас <YYYY-MM-DD HH:MM>". Пустой timezone →
// "" (строка не печатается). Местное время — через фиксированную зону по
// timezoneOffset, БЕЗ tzdata (статический бинарник без /usr/share/zoneinfo).
func profileTimezone(p *client.Profile, now time.Time) string {
	tz := collapseSpace(p.Timezone)
	if tz == "" {
		return ""
	}
	local := now.In(time.FixedZone("", p.TimezoneOffset))
	return fmt.Sprintf("%s (%s), сейчас %s", tz, formatUTCOffset(p.TimezoneOffset), local.Format("2006-01-02 15:04"))
}

// formatUTCOffset — смещение в секундах → "UTC", "UTC+2", "UTC-3",
// "UTC+5:30", "UTC-3:30" (минуты — только если ненулевые; секунды
// отбрасываются).
func formatUTCOffset(sec int) string {
	if sec == 0 {
		return "UTC"
	}
	sign := "+"
	if sec < 0 {
		sign = "-"
		sec = -sec
	}
	h, m := sec/3600, sec%3600/60
	if m == 0 {
		return fmt.Sprintf("UTC%s%d", sign, h)
	}
	return fmt.Sprintf("UTC%s%d:%02d", sign, h, m)
}

// collapseSpace — схлопывает пробельные символы в один пробел и обрезает
// края, БЕЗ усечения длины (oneLine не подходит: режет по threadCellLimit).
func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
