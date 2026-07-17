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
//   - type=="file"  → подставить Name;
//   - type=="user"  → подставить Name (covers {actor} и {mention-*});
//   - прочие типы   → подставить Name, если оно непусто, иначе оставить плейсхолдер;
//   - ключа нет в params → оставить {key} как есть.
//
// Ограничение MVP: ссылка на файл (MsgParam.Link) в TEXT-таблицы не подмешивается
// — выводится только Name (формат таблицы `[id] время автор: текст` без ссылки,
// спека §6). В --json-режиме Link доступен через исходные MessageParameters
// (поле messageParameters.file.link сериализуется как есть). Подмешивание ссылки
// в текстовый вывод — осознанно отложено (спека §6 хеджит «опционально»).
func SubstituteParams(msg string, params client.MsgParams) string {
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
