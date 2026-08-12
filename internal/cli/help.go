// Package cli (файл help.go) — перехват --help/-h/`help` и тексты помощи.
//
// Декомпозиция: cmdSpecs (этот файл) — единый источник правды для имён команд
// и их флагов; по этой декларации рендерятся help-тексты (renderGeneral/
// renderResource/renderDetailed) И сверяются anti-drift тесты обоих типов
// (help↔декларация и декларация↔handler, спека §6).
//
// Перехват help-запроса идет в cmd/nctalk/main.go.run() ДО config.Load(),
// поэтому help работает без NEXTCLOUD_* (спека §5).
package cli

// flagSpec — описание одного флага в декларации команды.
// Поле Value пусто для boolean-флагов (--unread, --system, --all, ...).
type flagSpec struct {
	Name  string
	Value string
	Desc  string
}

// cmdSpec — декларация одной команды базового CLI для help и anti-drift.
// Path = ["rooms","list"] для двухуровневых команд; ["search"] для leaf-команд
// (special-case роутинга, не входит в таблицу routes).
type cmdSpec struct {
	Path        []string
	Short       string // для общего help и списка команд ресурса
	UsageExtras string // позиционные аргументы usage ("<room>", "<room> <messageId>")
	Flags       []flagSpec
	Examples    []string
}

// cmdSpecs — единый источник правды: 7 команд базового CLI.
// Порядок соответствует общему help (спека §3). Флаги — строго по handler-ам
// (handlers_rooms.go, handlers_chat.go, handlers_reactions.go, handlers_search.go);
// --name включён только в тех командах, где handler его парсит.
//
// ВНИЗУ: каждая правка флагов в handler-е обязана сопровождаться правкой здесь;
// regression-защита — TestCmdSpecs_FlagsExactly и TestAntiDrift_Handler (Task 4).
var cmdSpecs = []cmdSpec{
	{
		Path:        []string{"rooms", "list"},
		Short:       "список комнат",
		UsageExtras: "",
		Flags: []flagSpec{
			{Name: "--type", Value: "<1|2|3>", Desc: "фильтр по типу комнаты"},
			{Name: "--unread", Desc: "только комнаты с непрочитанными"},
			{Name: "--include-former", Desc: "показывать former-комнаты (типы 4/5/6)"},
		},
		Examples: []string{
			"nctalk rooms list --unread",
			"nctalk rooms list --type 2",
		},
	},
	{
		Path:        []string{"rooms", "find"},
		Short:       "найти комнату по фильтру",
		UsageExtras: "<запрос>",
		Flags: []flagSpec{
			{Name: "--user", Value: "<actorId>", Desc: "точный фильтр по actorId собеседника"},
		},
		Examples: []string{
			"nctalk rooms find \"команда\"",
			"nctalk rooms find \"\" --user bob",
		},
	},
	{
		Path:        []string{"rooms", "search"},
		Short:       "поиск комнат на сервере",
		UsageExtras: "<term>",
		Flags:       nil, // без флагов; handler отвергает любое --* (limit фиксируется в 0)
		Examples: []string{
			"nctalk rooms search foo",
		},
	},
	{
		Path:        []string{"chat", "show"},
		Short:       "история чата комнаты",
		UsageExtras: "<room>",
		Flags: []flagSpec{
			{Name: "--name", Value: "<имя>", Desc: "разрешить комнату по имени (вместо token)"},
			{Name: "--last", Value: "N", Desc: "количество сообщений (по умолчанию 20)"},
			{Name: "--from", Value: "<actorId>", Desc: "только сообщения автора"},
			{Name: "--since", Value: "<время>", Desc: "с момента времени (1h, 2d или ISO)"},
			{Name: "--system", Desc: "показывать system-сообщения (скрыты по умолчанию)"},
		},
		Examples: []string{
			"nctalk chat show abc123 --last 50",
			"nctalk chat show --name \"Команда\" --since 1h",
		},
	},
	{
		Path:        []string{"chat", "send"},
		Short:       "отправить сообщение (stdin или --file)",
		UsageExtras: "<room>",
		Flags: []flagSpec{
			{Name: "--name", Value: "<имя>", Desc: "разрешить комнату по имени (вместо token)"},
			{Name: "--reply-to", Value: "<id>", Desc: "id сообщения, на которое это ответ"},
			{Name: "--reference-id", Value: "<uuid>", Desc: "клиентский dedup-идентификатор"},
			{Name: "--silent", Desc: "отправить без уведомления получателей"},
			{Name: "--file", Value: "<путь>", Desc: "взять тело из файла (иначе stdin)"},
		},
		Examples: []string{
			"echo \"hi\" | nctalk chat send abc123",
			"nctalk chat send --name \"Команда\" --file msg.txt",
		},
	},
	{
		Path:        []string{"reactions", "get"},
		Short:       "реакции на сообщение",
		UsageExtras: "<room> <messageId>",
		Flags: []flagSpec{
			{Name: "--name", Value: "<имя>", Desc: "разрешить комнату по имени (вместо token)"},
		},
		Examples: []string{
			"nctalk reactions get abc123 100",
		},
	},
	{
		Path:        []string{"search"},
		Short:       "глобальный поиск по сообщениям",
		UsageExtras: "<term>",
		Flags: []flagSpec{
			{Name: "--from", Value: "<actorId>", Desc: "фильтр по автору"},
			{Name: "--limit", Value: "N", Desc: "размер выборки (по умолчанию 10)"},
			{Name: "--all", Desc: "без ограничения размера (в пределах сервера)"},
		},
		Examples: []string{
			"nctalk search foo",
			"nctalk search foo --from alice --limit 25",
		},
	},
}

// findSpec возвращает *cmdSpec по пути path (1-2 токена) или nil, если пути нет.
// Используется HandleHelp, renderGeneral/renderDetailed и anti-drift тестами.
func findSpec(path []string) *cmdSpec {
	for i := range cmdSpecs {
		if pathsEqual(cmdSpecs[i].Path, path) {
			return &cmdSpecs[i]
		}
	}
	return nil
}

// pathsEqual — почленное сравнение слайсов строк.
func pathsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// joinPath — "rooms list" для ["rooms","list"], "search" для ["search"].
// Используется в render-функциях и тестах.
func joinPath(p []string) string {
	out := ""
	for i, s := range p {
		if i > 0 {
			out += " "
		}
		out += s
	}
	return out
}
