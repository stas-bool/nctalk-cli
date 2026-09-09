# Code-review: реализация `rooms participants`

- **Дата:** 2026-09-09
- **Ревьюер:** glm-5.3 (opencode, named-сессия `s2c-code`)
- **Объект:** дифф `9ffff84..4d9bc5a` — ветка `feat/rooms-participants`, 5 коммитов
  (client → cli → роутинг/help → integration-тест → docs)
- **Спека:** `docs/superpowers/specs/2026-09-09-nctalk-rooms-participants-design.md`
- **Проверки:** `go vet` + `go test ./...` (CGO_ENABLED=0) — зелёные

## Вердикт

Критичных и major-багов корректности **нет**. Контракт спеки соблюдён: exit-коды,
сортировка (роль → имя с fallback ДО сортировки, stable), нормализация `nil → []`
для `--json`, пустой вывод при пустом списке, анти-drift каноны
(Order/FlagsExactly/buildPositionalArgs/оба Run-level), фикстуры реального формата +
отдельная синтетика гостя. PathEscape-решение корректно (тест
`TestGetParticipants_PathEscape` подтверждает одиночный эскейп транспортом).

**Счётчик замечаний: 4 (все minor).**

## Замечания

### minor

1. **internal/client/participants_test.go:95-96** — устаревший doc-комментарий:
   «эскейпится в path-сегменте (url.PathEscape)» — но `url.PathEscape` из реализации
   убран (эскейп делает transport). Имя теста `TestGetParticipants_PathEscape` ещё
   терпимо (проверяет что эскейп есть), но комментарий вводит в заблуждение о том,
   *кто* эскейпит. Фикс: переформулировать «эскейпится сериализацией URL в
   transport.DoOCS» (док в participants.go:31-36 это уже формулирует правильно —
   рассинхрон только в тесте).

2. **internal/cli/handlers_rooms.go:262-267** — `strings.ToLower(participantSortName(...))`
   пересчитывается на каждое сравнение: O(n log n) вызовов ToLower с аллокациями. Для
   реальных размеров комнаты (<сотни) несущественно, но прекомпьют ключей (слайс
   `type`+`lowerName` + `sort.Sort`) убрал бы и аллокации, и повторный fallback. Фикс
   опционален; если делать — не `sort.Slice`, чтобы не потерять stable-семантику (или
   `slices.SortStableFunc` на Go 1.21 с компаратором по предвычисленному ключу).

3. **internal/cli/handlers_rooms_test.go** — тестовый пробел: форма `--name=Команда`
   (equals-форма) для `rooms participants` не покрыта (все кейсы через
   `--name Значение`). Ветка `strings.HasPrefix(a, "--name=")`
   (handlers_rooms.go:227-228) не покрыта тестом. Фикс: добавить кейс в
   `TestRoomsParticipantsHandlerNameResolution` или отдельный однострочный тест.

4. **CLAUDE.md:5** — счётчик «9 команд» обновлён, но перечень «эталонов контракта» в
   первом абзаце не упоминает новую дельту
   `2026-09-09-nctalk-rooms-participants-design.md` (упомянуты только базовая и
   `chat edit`). Спека §5 этого не требовала, но по той же логике, по которой упомянут
   `chat edit`, стоит добавить — иначе «читай при любой правке поведения» не приведёт к
   дельте participants. Фикс: дописать «и дельта `2026-09-09-...-rooms-participants-design.md`».

## Принято к сведению (не замечания)

- Дублирование `participantSortName`/`participantName` — план-мандатировано (известное).
- `--name=` (пустое значение equals-формы) → exit 1 «укажите token» — идентично
  поведению `chat show/send/edit`, консистентно.
- `--json`-вывод при пустом списке — ранний return до render, `[]` не печатается —
  точно по спеке §2.
- Сортировка мутирует слайс, возвращённый клиентом, — безопасно (свежий slice из decode).
