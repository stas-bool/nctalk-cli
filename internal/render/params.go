package render

import (
	"regexp"

	"github.com/stas/nctalk/internal/client"
)

// placeholderRe находит плейсхолдеры вида {key}, где key начинается с буквы/цифры
// и может содержать буквы, цифры и дефисы (спека §6 «Подстановка»: {actor},
// {file}, {mention-userN}). Скобки в match сохранены, ключ — capture-группа 1.
var placeholderRe = regexp.MustCompile(`\{(\w[\w-]*)\}`)

// SubstituteParams заменяет плейсхолдеры {actor}/{file}/{mention-*} в msg на
// человекочитаемое имя (поле Name) из карты messageParameters. Нераспознанные
// плейсхолдеры (ключа нет в params) остаются как есть.
//
// Правила подстановки (спека §6 chat show «Подстановка»):
//   - type=="file"  → подставить Name (ссылка прикладывается в render-слое);
//   - type=="user"  → подставить Name (covers {actor} и {mention-*});
//   - прочие типы   → подставить Name, если оно непусто, иначе оставить плейсхолдер;
//   - ключа нет в params → оставить {key} как есть.
//
// Функция работает только с текстом; вопросы --json (ссылки на файлы) тут не
// затрагиваются — это уровень таблиц/JSON-renderer'а.
func SubstituteParams(msg string, params map[string]client.MsgParam) string {
	if params == nil {
		return msg
	}
	return placeholderRe.ReplaceAllStringFunc(msg, func(match string) string {
		// Вынимаем ключ между скобками: match = "{key}".
		key := match[1 : len(match)-1]
		p, ok := params[key]
		if !ok {
			// Нераспознанный плейсхолдер — оставляем как есть.
			return match
		}
		switch p.Type {
		case "file", "user":
			return p.Name
		default:
			// mention-* и прочие: подставляем Name только если оно есть.
			if p.Name != "" {
				return p.Name
			}
			return match
		}
	})
}
