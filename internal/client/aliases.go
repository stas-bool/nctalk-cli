package client

import "github.com/stas-bool/nctalk-cli/internal/transport"

// aliases.go — type alias для обратной совместимости существующих тестов и
// кода, который ссылается на client.OCSError. После выноса типа в
// internal/transport (спека 2026-07-19 §3) реальное определение живёт там;
// здесь — алиас, чтобы ссылки на client.OCSError продолжали работать без
// правок вызывающего кода (cli/exit.go, cli-тесты, client-тесты).
//
// Аlias в Go — это ТОТ ЖЕ тип (не wrapper): errors.As(err, &client.OCSError{})
// и errors.As(err, &transport.OCSError{}) работают идентично. Поэтому маппинг
// Code:404 → exit 2 в cli.exitFromClientErr сохраняется без изменений.
//
// ВНИМАНИЕ: alias для generic-типа OCSEnvelope НЕ добавлен — Go 1.21 не
// поддерживает generic type aliases (появилось в Go 1.23). Единственное
// использование OCSEnvelope внутри client (helper ocsBody в client_test.go)
// обновлено на прямую ссылку transport.OCSEnvelope. Потребителей OCSEnvelope
// вне transport-слоя больше нет (ресурсные методыы клиента работают через
// DoOCS, который сам распаковывает конверт внутри).

// OCSError — alias для transport.OCSError (см. комментарий выше).
type OCSError = transport.OCSError
