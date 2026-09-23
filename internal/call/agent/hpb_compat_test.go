package agent

// hpb_compat_test.go — compile-time гарантия (дельта §3, аналог var _
// sigClient = (*signaling.Client)(nil) в agent.go): hpbesignaling.Client
// реализует agent.sigClient ЦЕЛИКОМ (4 метода). Рассинхрон сигнатур роняет
// сборку тестов agent'а ещё до wiring'а в cmd.
import "github.com/stas-bool/nctalk-cli/internal/call/hpbsignaling"

var _ sigClient = (*hpbesignaling.Client)(nil)
