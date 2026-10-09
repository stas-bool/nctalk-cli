# Дизайн: `nctalk` — отправка файлов (`chat send --attach`)

- **Дата:** 2026-10-09
- **Статус:** дизайн одобрен пользователем (брейншторм 2026-10-02…09); далее — план реализации
- **Базовая спека:** `docs/superpowers/specs/2026-07-17-nctalk-cli-design.md` (эталон контракта
  базового CLI; этот документ — дельта, добавляющая флаг `--attach` к существующей команде
  `chat send`, новых команд нет). Предыдущие дельты: `chat edit` (2026-09-08), `rooms participants`
  (2026-09-09), треды (2026-09-29).
- **Стек:** Go 1.21+, только stdlib, `CGO_ENABLED=0`

## 1. Цель и контекст

Дать `nctalk` отправлять локальный файл в чат Talk — аналог «Загрузить с устройства» веб-клиента.
Потребитель — агент через skill-обёртку: «скинь лог/скрин/отчёт в чат», «ответь файлом в тред».
Сегодня `chat send --file` читает из файла **текст** сообщения; вложений клиент не умеет.

Решения пользователя по развилкам (брейншторм):

1. **Охват** — локальный файл с диска. Шаринг файла, уже лежащего в Nextcloud, — out of scope.
2. **Форма** — флаг `--attach` у `chat send`, а не отдельный verb: текст из stdin/`--file`
   становится подписью к файлу.
3. **Механизм сервера** — классический: WebDAV-загрузка в папку вложений + OCS-шаринг
   `shareType=10`. Новый механизм Talk 24 (conversation subfolders) отклонён (обоснование ниже).
4. **Вывод** — id сообщения с файлом, как у `chat send`; сервер его не возвращает — клиент находит
   сообщение по `referenceId`.
5. **`--reply-to` / `--thread` с файлом проверяются до загрузки**: для файлов сервер их не
   валидирует.
6. **Один файл за вызов.**

**Сервер:** Nextcloud 34.0.3 + Talk (spreed) 24.0.4. Живой запрос capabilities 2026-10-02:
`config.attachments.allowed=true`, `config.attachments.folder="/Talk"`,
`config.attachments.conversation-subfolders=true`; features `chat-reference-id`, `media-caption`,
`threads`. WebDAV с app-password отвечает (PROPFIND `207`). Логин учётки **не совпадает** с uid.

API сверен по исходникам spreed **v24.0.4**: `docs/chat.md` («Share a file to the chat»),
`lib/Chat/SystemMessage/Listener.php`, `lib/Share/RoomShareProvider.php`,
`lib/Controller/ChatController.php`, `lib/Controller/ThreadController.php`,
`lib/Chat/Parser/SystemMessage.php`, `lib/Chat/ChatManager.php`, веб-клиент
`src/stores/upload.ts` и `src/utils/fileUpload.ts`; Nextcloud server **v34.0.3**:
`apps/files_sharing/lib/Controller/ShareAPIController.php`.

### Почему классический механизм, а не conversation subfolders Talk 24

Talk 24 ввёл второй путь: `POST chat/{token}/attachment/folder` (сервер создаёт папку беседы и
Draft-папку) → загрузка файла в Draft → `POST chat/{token}/attachment` (сервер переносит файл в
`Talk/<Чат>-<token>/<Имя>-<uid>/` и публикует). Им пользуется веб-клиент при
`conversation-subfolders=true`. Отклонён, потому что:

- не задокументирован в `docs/chat.md` (есть только упоминание capability, расходящееся с кодом),
  появился в марте 2026 и с тех пор правился — нестабильный контракт;
- работает только на Talk 24+;
- `threadTitle` из `talkMetaData` игнорируется (тред с файлом не создать), `threadId` не
  валидируется (пишется родителем сообщения как есть).

Классический путь задокументирован, работает на любой версии Talk, поддерживает caption /
`replyTo` / `threadId` / `threadTitle` / `silent`. Цена: файлы ложатся плоско в папку вложений
(в исходниках Talk это «legacy shares»), уникальность имени обеспечивает клиент.

### Модель (инварианты сервера, неочевидные)

- Файл должен лежать в хранилище отправителя; в чат он попадает **шарингом** (`shareType=10`,
  `shareWith=<token>`). Сообщение в чате создаёт listener Talk на событие создания шара — ответ
  шаринга **не содержит id сообщения**.
- Параметры сообщения передаются в `talkMetaData` — **JSON-строке** внутри тела запроса: `caption`,
  `replyTo`, `threadId`, `threadTitle`, `silent` (и `messageType` — не используем). `referenceId` —
  отдельный параметр верхнего уровня. Listener ставит этот `referenceId` на **все**
  system-сообщения, созданные в рамках запроса, в т.ч. на `thread_created` при `threadTitle`.
- Сервер нормализует `referenceId`: `trim(substr(ref, 0, 64))`.
- Для файла сервер **молча** игнорирует невалидные `replyTo` (нет сообщения или оно не replyable →
  без цитаты) и `threadId` (нет треда → общий чат). Для текста (`POST chat/{token}`) те же ошибки
  дают HTTP 400. Отсюда pre-check в клиенте (§2.4).
- `threadTitle` создаёт тред, только если сообщение не является ответом
  (`topmostParentId == 0`).
- В chat-API сообщение с файлом: `messageType="comment"`, `systemMessage=""`,
  `messageParameters.file` — объект файла, `referenceId` — переданный клиентом. Поле `message` —
  `"{file}"`, а при непустой подписи — **текст подписи без плейсхолдера `{file}`**.
- Логин ≠ uid (на боевом логин — e-mail): путь WebDAV строится по uid из `GET /cloud/user`.
- Папка вложений — пользовательская настройка Talk (`config.attachments.folder`, дефолт `/Talk`).
  Классический путь её **не создаёт** (`ensureAttachmentFolderExists` вызывается только новым
  механизмом).
- Уникальное имя — правило веб-клиента (`findUniquePath`): при занятости к имени перед
  расширением добавляется ` (N)`: `report.pdf` → `report (2).pdf` → `report (3).pdf`;
  `x (3).pdf` → `x (4).pdf`. Расширение — совпадение `\.[0-9a-z]+$` без учёта регистра
  (`a.tar.gz` → `a.tar (2).gz`, `Makefile` → `Makefile (2)`).
- Комната не найдена / отправитель не участник / нет права писать в чат → OCS **404**
  «Conversation not found» (`RoomShareProvider` бросает `GenericShareException(…, 404)`,
  `ShareAPIController` превращает его в `OCSException(hint, 404)`).

### Out of scope

- Шаринг файла, уже лежащего в Nextcloud (по пути в облаке).
- Несколько файлов за вызов.
- Голосовые сообщения (`messageType=voice-message`).
- Chunked upload (докачка больших файлов) — файл уходит одним PUT.
- Механизм conversation subfolders Talk 24, в т.ч. как fallback.
- Пометка вложения в текстовом `chat show`: при непустой подписи текст сообщения = подпись, имя
  файла видно только в `--json` (`messageParameters.file`).
- Листинг и скачивание файлов (`files ls/get/find` — TODO.md).

## 2. Контракт

### 2.1 Синтаксис

```
nctalk chat send <room> --attach <путь>
    [--file <подпись> | подпись из stdin]
    [--reply-to <id> | --thread <id> | --thread-title "<тема>"]
    [--silent] [--reference-id <id>] [--name "<имя>"] [--json]
```

- `--attach <путь>` / `--attach=<путь>` — локальный файл. Повтор флага → exit `1` до сети
  («один файл за вызов»): молча брать последний нельзя — повтор означает, что ждали несколько
  файлов. Пустое значение (`--attach=`) → exit `1`.
- Остальные флаги — существующие флаги `chat send` без изменения семантики: `<room>` или
  `--name` (правило разрешения базовой спеки §7), взаимоисключаемость
  `--reply-to`/`--thread`/`--thread-title` и guard пустого `--thread-title` — прежние.
- **Без `--attach` поведение `chat send` не меняется ни в чём.**

### 2.2 Подпись (caption)

- Источник — тот же, что у тела `chat send`: `--file <путь>` или stdin; срезается ровно один
  завершающий `\n`/`\r\n` (общий блок чтения с `readMessageBody`).
- Подпись **необязательна**: пустой результат = файл без подписи. Без `--attach` пустое тело —
  по-прежнему exit `1`.
- stdin при `--attach` читается, **только если он не терминал** (не char device). Иначе
  `nctalk chat send x --attach f` в терминале ждал бы Ctrl-D. `/dev/null` — тоже char device: не
  читается, результат тот же (пусто). Пайп, файл, heredoc — читаются. В тестах `Deps.Stdin` — не
  `*os.File`, читается всегда.
- Подпись из одних пробелов сервер отбрасывает (`trim`) — клиент это не дублирует.

### 2.3 Файл

- До сети: `os.Open` + `Stat`. Не существует / не обычный файл (каталог, устройство) / нет прав
  чтения → exit `1`. Пустой (0 байт) файл допустим.
- Имя на сервере — `filepath.Base` локального пути. При занятости — ` (N)` по правилу веб-клиента
  (§1, модель). **Существующие файлы не перезаписываются никогда** (PUT с `If-None-Match: *`).
- Размер клиент не ограничивает. Отказ сервера или прокси (413, 507) → exit `1` с понятным
  текстом. Таймаут загрузки растёт с размером файла (§4.2).

### 2.4 Ответ / тред с файлом и pre-check

- `--thread <id>` → `talkMetaData.threadId`. **До загрузки** — `GET chat/{token}/threads/{id}`:
  404 → exit `2` («тред <id> не найден»), файл не загружается.
- `--reply-to <id>` → `talkMetaData.replyTo`. **До загрузки** —
  `GET chat/{token}/{id}/context?limit=0`: в ответе нет сообщения с этим id → exit `2`
  («сообщение <id> не найдено»); сообщение есть, но `isReplyable=false` → exit `1`
  («на сообщение <id> нельзя ответить»). Файл в обоих случаях не загружается.
- `--thread-title "<тема>"` → `talkMetaData.threadTitle`: сообщение с файлом становится корнем
  нового треда, его id = id треда (сразу пригоден для `--thread`).
- **Отличие от текстового `chat send`:** там неверные `--thread`/`--reply-to` отвергает сервер
  (HTTP 400 → exit `1`); здесь их проверяет клиент, и «не найдено» даёт exit `2` — общая семантика
  CLI «не найдено = 2» (как у `chat show --thread`, `chat edit`).
- `--silent` → `talkMetaData.silent=true` (поле только при `true`).
- `--reference-id` → параметр `referenceId` шаринга. Не задан или пуст после нормализации →
  клиент генерирует случайный: 64 hex-символа из `crypto/rand` (docs: «should be a random
  sha256»).

### 2.5 Вывод

- Текст — `<id>\n`, `--json` — `{"id": <id>}`: то же, что у `chat send`.
- id не найден (§3.6) или ошибка поиска: файл **уже отправлен** → exit `0`, stdout пуст в тексте и
  `{"id": null}` в `--json`, в stderr — `chat send: файл отправлен, но id сообщения не определён:
  <причина>`. Ошибкой это не считается: повтор вызова агентом создал бы дубликат.

### 2.6 Exit-коды (наследуют базовый контракт §7, без новых кодов)

| Ситуация | Код |
|---|---|
| успех, в т.ч. id не определён (с предупреждением) | `0` |
| флаги: повтор или пустой `--attach`, конфликт, неположительный id, пустой `--thread-title`, неизвестный флаг · файл не найден / не обычный / не читается · ошибка чтения подписи · вложения запрещены (`attachments.allowed=false`) · сообщение `--reply-to` не replyable · ошибки WebDAV (401/403/409/413/423/507/5xx, сеть) · шаринг 400/403/5xx · сеть, 401 | `1` |
| комната не найдена (OCS 404 на любом шаге, в т.ч. «Conversation not found» шаринга) · тред `--thread` или сообщение `--reply-to` не найдены · 0 совпадений `--name` | `2` |
| >1 совпадение `--name` | `3` |

## 3. API-контракт

Все OCS-запросы — существующий транспорт `doOCS` (Basic-auth + `OCS-APIRequest: true` +
`Accept: application/json`, конверт `ocs.data`). WebDAV — новый транспорт `DoDAV` (§4) с теми же
гарантиями безопасности.

### 3.1 Пользователь

`GET /ocs/v2.php/cloud/user` → `ocs.data.id` (uid). Пустой id → ошибка (drift формата).

### 3.2 Настройки вложений

`GET /ocs/v2.php/cloud/capabilities` →
`ocs.data.capabilities.spreed.config.attachments`: `allowed` (bool), `folder` (string, напр.
`/Talk`). `allowed=false` → exit `1` до pre-check и загрузки (порядок шагов — §5). `folder` пуст
или отсутствует → дефолт `/Talk`. Нет `spreed` в capabilities → ошибка («Talk недоступен на
сервере»).

### 3.3 Pre-check

- **Тред:** `GET /ocs/v2.php/apps/spreed/api/v1/chat/{token}/threads/{id}` → 200 `TalkThreadInfo`
  (тело не разбирается) | 404 `{"error":"thread"}` (или комната не найдена).
- **Сообщение:** `GET /ocs/v2.php/apps/spreed/api/v1/chat/{token}/{id}/context?limit=0` → массив
  сообщений. При `limit=0` сервер отдаёт одно ближайшее сообщение с id ≤ запрошенного
  (`getHistory(…, limit 1, includeLastKnown)`): для несуществующего id это **соседнее** сообщение,
  а не 404. Поэтому клиент сверяет `id` и `isReplyable`. 304 или пустой массив → не найдено.
  Read-marker эндпоинт не трогает.

### 3.4 WebDAV

База — `/remote.php/dav/files/{uid}`. Путь файла = база + папка вложений + имя; каждый сегмент
percent-кодируется (пробелы, кириллица, `#`, `?`, `%`).

- `HEAD <файл>` → 200 — имя занято | 404 — свободно | прочее → ошибка.
- `PUT <файл>`: заголовки `If-None-Match: *`, `Content-Type: application/octet-stream`,
  `Content-Length` = размер (без chunked transfer-encoding: за php-fpm chunked-тело в части
  конфигураций приходит пустым), тело — содержимое файла.
  - 201 → успех (204 при `If-None-Match: *` невозможен — Sabre отвечает 412 до записи; если
    придёт, трактуется как успех);
  - 412 → имя заняли между HEAD и PUT → следующее имя;
  - 409 → нет папки вложений → `MKCOL` и повтор PUT (один раз);
  - 413 / 507 / 401 / 403 / 423 / 5xx → ошибка.
- `MKCOL <папка>` → 201 | 405 (уже существует — гонка) → ок | прочее → ошибка «папка вложений
  <folder> недоступна» (в т.ч. 409 — нет родителя у вложенной папки).
- `DELETE <файл>` → 204 (cleanup после неудачного шаринга).
- Ошибки сервера — XML Sabre
  `<d:error><s:exception>…</s:exception><s:message>…</s:message></d:error>`: в текст ошибки идёт
  `s:message`; тело читается с лимитом 64 КиБ.
- Цикл подбора имени ограничен 1000 кандидатов (защита от зацикливания на неисправном сервере) →
  ошибка «не удалось подобрать свободное имя».

### 3.5 Публикация

`POST /ocs/v2.php/apps/files_sharing/api/v1/shares`, JSON:

```json
{
  "shareType": 10,
  "shareWith": "<token>",
  "path": "/Talk/report (2).pdf",
  "referenceId": "<64 hex>",
  "talkMetaData": "{\"caption\":\"Отчёт\",\"threadId\":1234,\"silent\":true}"
}
```

- `talkMetaData` — JSON-строка только с заданными полями (`caption` непуст, `replyTo` > 0,
  `threadId` > 0, `threadTitle` непуст, `silent` = true); при пустом наборе — `"{}"` (как у
  веб-клиента).
- `path` — папка вложений + имя, с ведущим `/`.
- Ответ — share-объект; клиент проверяет только OCS-статус, тело не разбирает.
- Ошибки: OCS 404 «Conversation not found» (нет комнаты / не участник / нет права писать) →
  exit `2`; 403 и прочее → exit `1`.
- После **любой** ошибки публикации — cleanup: `DELETE` загруженного файла (best-effort, тот же
  ctx). Неудача cleanup дописывается в текст ошибки с путём файла, чтобы его можно было удалить
  вручную.

### 3.6 Поиск id сообщения

`GET /ocs/v2.php/apps/spreed/api/v1/chat/{token}?limit=50&lookIntoFuture=0&setReadMarker=0` —
существующий `GetChat` с `Limit: 50`. Кандидаты — сообщения, у которых
`referenceId == нормализованный ref` и `messageType != "system"`: `thread_created` и прочие
system-сообщения того же запроса несут тот же `referenceId`. Из кандидатов берётся сообщение с
**наибольшим id** — выбор не зависит от порядка сообщений в теле ответа (тот же принцип, что у
пагинации `GetChat`) и устойчив к повторно использованному пользователем `--reference-id`.
Нормализация — как у сервера: первые 64 байта, затем trim по `" \t\n\r\0\x0B"`.

Окно — одна страница: 50 самых свежих сообщений комнаты, system-сообщения тоже в счёт. Это
осознанный компромисс: сообщение-файл создаётся синхронно, в рамках запроса шаринга (listener на
событие создания шара — §1, модель), а поиск идёт следующим запросом сразу после ответа шаринга,
так что к этому моменту сообщение уже среди самых свежих. Вытеснить его из окна могут лишь 50 и
более сообщений новее него — поток в десятки сообщений за время одного round-trip. Окно не
расширяется (ни второй страницей, ни до серверного максимума 200), поиск не повторяется: любое
окно конечно, а промах — не ошибка, файл уже отправлен (§2.5: exit `0` с предупреждением).

## 4. Слой transport

### 4.1 `DoDAV`

Новый файл `internal/transport/dav.go`:

```go
// DAVResponse — итог WebDAV-запроса: статус интерпретирует вызывающий.
type DAVResponse struct {
    StatusCode int
    Header     http.Header
    Message    string // s:message из XML-ошибки Sabre (только при StatusCode >= 400)
}

func DoDAV(ctx context.Context, doer Doer, auth Auth, method, p string,
    header http.Header, body io.Reader, contentLength int64) (*DAVResponse, error)
```

- `error` — только транспортные сбои (сеть, отмена ctx, редирект на чужой хост); HTTP-статусы
  возвращаются в `DAVResponse`, их смысл знает вызывающий (у HEAD 404 — норма).
- Инварианты те же, что у `DoOCS`: `Authorization: Basic` собирается на каждый запрос; путь
  клеится через `path.Join` + `url.URL.Path` (экранирование сегментов); все ошибки — через
  `SanitizeErr`; тело ответа дочитывается (с лимитом) и закрывается; dump запросов и ответов
  запрещён. OCS-заголовки не ставятся.
- Редиректы — общая политика `SameHostRedirectPolicy` клиента. PUT с телом без `GetBody` Go на
  307/308 не переотправляет, а 301/302 превращает в GET — в обоих случаях статус не совпадёт с
  ожидаемым, и вызывающий вернёт ошибку. Молчаливого успеха нет.
- Транспорт пригодится будущим `files ls/get/find` (TODO.md).

### 4.2 Таймаут загрузки

`http.Client.Timeout` (= `NEXTCLOUD_TIMEOUT`, дефолт 30 с) покрывает весь обмен, включая
передачу тела: файл в 50 МБ на медленном канале не успел бы загрузиться. Поэтому для PUT —
отдельный doer: `http.Client` с тем же `Transport` и `CheckRedirect`, но без `Timeout`. Срок
задаётся контекстом: `NEXTCLOUD_TIMEOUT + 1 с на каждые 64 КиБ` (с округлением вверх; минимальная
скорость около 512 кбит/с). Остальные запросы (OCS, HEAD, MKCOL, DELETE) идут через обычный doer
с прежним таймаутом. В `NewTalkClientWithDoer` (тесты) upload-doer = переданный doer.

## 5. Слой client

Новый файл `internal/client/files.go`:

```go
// FileUpload — загружаемый файл. Body перематывается (Seek 0) перед каждой
// попыткой PUT: повторы после 409/412.
type FileUpload struct {
    Name string        // имя на сервере (filepath.Base локального пути)
    Size int64
    Body io.ReadSeeker
}

type SendFileOpts struct {
    Caption     string // пусто → без подписи
    ReplyTo     int
    ThreadId    int
    ThreadTitle string
    Silent      bool
    ReferenceId string // пусто → генерируется
}

type SendFileResult struct {
    MessageId int    // 0 — сообщение не найдено (файл отправлен), см. LookupErr
    Path      string // итоговый путь на сервере, напр. "/Talk/report (2).pdf"
    LookupErr error  // причина, если MessageId == 0
}

func (c *TalkClient) SendFile(ctx context.Context, token string, f FileUpload,
    opts SendFileOpts) (SendFileResult, error)
```

Шаги `SendFile`:

1. клиентская валидация до сети (дубль cli-guard-ов, защищает прямые вызовы — прецедент
   `EditMessage`): пустое имя или имя с `/` → ошибка; больше одного из
   `ReplyTo`/`ThreadId`/`ThreadTitle` → ошибка; отрицательные id → ошибка;
2. uid (§3.1) и настройки вложений (§3.2) — до pre-check: при `allowed=false` или без `spreed` в
   capabilities файл не отправить ни с какими аргументами, и эта причина не должна маскироваться
   ошибкой аргумента («тред не найден»);
3. pre-check (§3.3): `ThreadId > 0` → тред; `ReplyTo > 0` → сообщение. Ошибки «не найдено»
   возвращаются как `*OCSError{Code: 404, Message: …}` с понятным текстом — существующий маппинг
   `exitFromClientErr` даёт exit `2` без новых правил; «не replyable» — обычная ошибка (exit `1`);
4. подбор имени + PUT (+ MKCOL) (§3.4);
5. шаринг (§3.5); при ошибке — cleanup `DELETE`;
6. поиск id (§3.6); промах или ошибка → `MessageId: 0` + `LookupErr`, `error == nil`.

- Токен кладётся в chat-пути **как есть**, без `url.PathEscape` (инвариант пакета: экранирует
  транспорт).
- `SendFile` добавляется в интерфейс `cli.TalkClient` (11-й метод; комментарий «10 методов» в
  `cli.go` правится) — compile-time проверка ловит рассинхрон. Все шесть тест-моков интерфейса
  получают метод: `chatSpyClient` — полноценный spy (параметры, результат, ошибка, факт вызова),
  остальные пять — errMock-стабы.

## 6. Слой cli, render, help

- `chatSendHandler`: флаг `--attach` (обе формы, guard повтора и пустого значения). Ветка
  `--attach`: открыть файл и проверить `Stat` → прочитать подпись (необязательная, правило
  терминала §2.2) → `ResolveRoom` → `SendFile` → вывод. Порядок «всё локальное до сети» — как у
  существующих guard-ов. Файл закрывается по завершении.
- Чтение подписи — общий с `readMessageBody` блок (источник и трим), с режимом «необязательно»
  (пустое → без подписи) и правилом терминала (`*os.File` с `ModeCharDevice` не читается).
- `render`: новая `NewMessageIDNullJSON(w)` → `{"id": null}`; текстовый режим при неизвестном id
  ничего не печатает в stdout.
- Help (`internal/cli/help.go`, `cmdSpecs`): `chat send` +=
  `{Name: "--attach", Value: "<путь>", Desc: "приложить файл; тело из stdin/--file станет подписью (необязательной)"}`;
  `Short` — «отправить сообщение или файл»; пример
  `echo "Отчёт" | nctalk chat send abc123 --attach report.pdf`. Канон
  `TestCmdSpecs_FlagsExactly` (`help_test.go`): `"chat send"` += `--attach`.

## 7. Тестирование

- **Фикстуры — только с живого API, до написания тестов** (урок CLAUDE.md). Протокол — в
  integration-комнате через curl: загрузить файл, расшарить с подписью и без, с `threadTitle`;
  снять обезличенные ответы: `cloud/user`, capabilities (усечённые до
  `spreed.config.attachments`), ответ шаринга, страницу `chat` с сообщением-файлом (с подписью и
  без) и `thread_created` с тем же `referenceId`, `threads/{id}` 200/404,
  `context?limit=0` (существующее сообщение, несуществующий id, system-сообщение с
  `isReplyable=false`), XML-ошибки Sabre для 409 (PUT без папки) и 412 (`If-None-Match` на
  существующий файл). Особо сверить: `messageType`/`message` сообщения-файла (плейсхолдер `{file}`
  vs подпись), формат `messageParameters` (объект или пустой массив — урок CLAUDE.md), наличие
  `referenceId`. Фикстуры — в корневой `testdata/`, **обезличенные** (репозиторий публичный: без
  адреса сервера, имён, uid, токенов).
- **transport (`dav_test.go`):** метод, путь и заголовки (`Authorization`, `If-None-Match`,
  `Content-Length`, без chunked), экранирование сегментов (пробел, кириллица, `#`, `?`, `%`),
  извлечение `s:message`, лимит чтения тела, `SanitizeErr` (пароль-маркер не попадает в ошибку).
- **client (`files_test.go`, httptest с маршрутизацией по методу и пути):**
  - счастливый путь: HEAD 404 → PUT 201 → тело шаринга (`shareType=10`, `shareWith`, `path`,
    `referenceId` из 64 hex, `talkMetaData` — строка только с заданными полями, `"{}"` при пустом
    наборе) → поиск по фикстуре → `MessageId`;
  - имена: занятые `report.pdf` и `report (2).pdf` → PUT `report (3).pdf`; `x (3).pdf` →
    `x (4).pdf`; `a.tar.gz`, имя без расширения, расширение в верхнем регистре; лимит 1000;
  - гонка: PUT 412 → следующее имя; нет папки: PUT 409 → MKCOL 201 → PUT 201; MKCOL 405 → ок;
    MKCOL 409 → ошибка;
  - ошибки DAV 413/507/403 → текст с `s:message`;
  - шаринг: 404 → `*OCSError{Code:404}` + вызван DELETE; DELETE неудачен → путь файла в тексте
    ошибки;
  - поиск: system-сообщение с тем же `referenceId` пропускается; промах → `MessageId 0`, `nil`;
    сетевой сбой поиска → `MessageId 0` + `LookupErr`;
  - pre-check: тред 200 → загрузка идёт; 404 → `*OCSError{404}` и **ни одного** HEAD/PUT; context:
    id совпал и replyable → идёт; соседний id / пусто / 304 → 404-ошибка без загрузки;
    `isReplyable=false` → обычная ошибка без загрузки;
  - `attachments.allowed=false` → ошибка без загрузки и без pre-check (с заданным `ThreadId` — ни
    одного запроса `threads/{id}`); нестандартная папка (`/Docs/Chat`) → пути собраны верно; uid
    со спецсимволами экранирован;
  - `referenceId`: заданный — передан и нормализован при поиске (длиннее 64 байт, пробелы по
    краям); пустой → сгенерирован;
  - редирект PUT на чужой хост → ошибка без пароля в тексте; таймаут PUT считается по размеру.
- **cli (`handlers_chat_test.go`, table-driven):** `--attach` в обеих формах; повтор и пустое
  значение → exit `1` без сетевых вызовов (spy: ноль вызовов); файл не найден / каталог → exit `1`
  без сети; подпись из stdin и из `--file`; пустая подпись допустима; без `--attach` пустое тело —
  по-прежнему exit `1`; правило терминала (хелпер на `/dev/null`: char device → не читается);
  конфликты флагов с `--attach` → exit `1`; `--attach --thread-title` → `ThreadTitle` в
  `SendFileOpts`; вывод id текстом и JSON; `MessageId 0` → пустой stdout / `{"id": null}` +
  предупреждение в stderr, exit `0`; `*OCSError{404}` → exit `2`; прочие ошибки → exit `1`.
- **render:** `NewMessageIDNullJSON`.
- **help:** канон `TestCmdSpecs_FlagsExactly`.
- **Интеграционный** (`internal/client/integration_test.go`, build-тег `integration`,
  мутационный — `NCTALK_INTEGRATION_SEND=1` + `NCTALK_INTEGRATION_ROOM`, по образцу
  `TestIntegration_Threads`) — `TestIntegration_SendFile`:
  1. временный файл с уникальным именем и содержимым → `SendFile` с подписью → id > 0;
  2. `GetChat` → сообщение с этим id: `messageParameters.file.name` = имя, `message` = подпись;
  3. повтор с тем же именем → `Path` с ` (2)`;
  4. `SendFile` с `ThreadTitle` → `ListThreads` содержит тред с id сообщения и названием;
  5. `SendFile` с `ThreadId` этого треда → сообщение несёт `threadId` корня;
  6. pre-check: несуществующий `ThreadId` → 404, файл не загружен;
  7. cleanup: загруженные файлы удаляются через DAV (best-effort; сообщения в integration-комнате
     остаются с «файл больше недоступен» — та же семантика допуска, что у мутационных тестов).

## 8. Документация (входит в задачу)

- README: в секции `chat send` — `--attach`, правило подписи, пример, оговорка про таймаут.
- CLAUDE.md: в списке методов client — `SendFile`; в «Неочевидные инварианты» — блок про вложения:
  классический шаринг (`shareType=10`) + WebDAV; id сообщения — поиском по `referenceId`
  (system-сообщения пропускаются); сервер для файлов молча игнорирует невалидные
  `replyTo`/`threadId` → pre-check в клиенте; `If-None-Match: *` (никогда не перезаписывать);
  логин ≠ uid; папку вложений классический путь не создаёт (MKCOL по 409); правило терминала для
  подписи; cleanup после неудачного шаринга. В разделах «эталон контракта» и «Документы» —
  ссылка на эту спеку.
- `docs/integration-run.md`: `TestIntegration_SendFile` в таблицу тестов и в перечень мутационных.
- Skill-обёртка `nctalk` (`~/.claude/skills/nctalk/SKILL.md`, вне репозитория): `--attach` в
  шпаргалку и таблицу команд, примеры с подписью и в тред. Без этой правки агент не узнает о
  возможности.

## 9. Будущее (явно за рамками)

- Несколько файлов за вызов; голосовые сообщения; шаринг файла из Nextcloud по пути.
- Chunked upload для больших файлов.
- Пометка `📎 <имя>` у сообщений с вложением в текстовом `chat show`.
- Механизм conversation subfolders, когда его задокументируют.
- `files ls/get/find` поверх `DoDAV` (TODO.md).
