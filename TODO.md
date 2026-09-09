# TODO

- Нет отметки прочитанным: чтение через CLI не сбрасывает unread (`setReadMarker=0`), счётчик живёт вечно — нужен `rooms mark-read <token>` или флаг у `chat show`.
- Реакции только читаются (`reactions get`): поставить/убрать реакцию CLI не умеет — нужен `reactions add/remove <room> <messageId> <emoji>`.
