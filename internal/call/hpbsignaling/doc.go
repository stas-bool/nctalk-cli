// Package hpbesignaling — WebSocket-транспорт signaling для серверов
// Nextcloud Talk с настроенным High Performance Backend (внешний
// nextcloud-spreed-signaling). Дельта-спека 2026-09-23 (к базовой
// 2026-07-19 §7): при signalingMode=="external" OCS-polling не раздаёт
// участников и сообщения — клиент «слеп» в звонке; этот пакет — замена
// транспорта, peer/media-слои не меняются.
//
// Реализует все 4 метода agent.sigClient: PollLoop/Send — WebSocket (hello c
// ticket → room → события/исходящие), LeaveCall — делегирование OCS-клиенту
// call/signaling (Call API v4 работает при любом signalingMode), JoinCall —
// подключение WS-комнаты с бюджетом + делегирование OCS (canonical flow).
//
// Инварианты (дельта §2/§3/§4):
//   - ticket живёт только в памяти и в WS-фрейме hello: не в URL, не в логах;
//   - собственный sessionId — из hello-response (ДРУГОЕ id-пространство, чем
//     OCS-sessionId из JoinRoom) — доставляется агенту событием EvOwnSession;
//   - JoinCall устанавливает WS-комнату ДО делегирования OCS (canonical flow
//     §2: participants-update с inCall-флагами должен находить нашу сессию
//     уже в комнате — join-event флагов не несёт);
//   - первичное подключение — ограниченный бюджет (половина NCTALK_ICE_TIMEOUT,
//     дефолт 15с), per-attempt deadline: исчерпание/зависание → EvError{exit 1}
//     ДО ICE-таймера; переподключение
//     после установления — бесконечный backoff 1с→30с, новый ticket на каждое;
//   - Send пропускает signaling.Message с ЛЮБЫМ Type (unmute — критичен);
//   - импорт coder/websocket живёт только в этом пакете (граница удаления
//     звонков: rm -rf internal/call cmd/nctalk-call cmd/nctalk-talk && go mod tidy).
package hpbesignaling
