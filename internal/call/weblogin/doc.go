// Package weblogin реализует web-login flow Nextcloud для получения PHP-session.
//
// В отличие от Basic-auth (app-password, stateless), internal-signaling Spreed
// требует PHP $_SESSION: pullMessages (GET /api/v3/signaling/{token}) читает
// session, инициализируемую ТОЛЬКО web-login'ом (POST /login с user/password).
// Basic-auth её не даёт → pull возвращает 404 (баг #5, см.
// docs/session-state/2026-07-19-nctalk-audio-calls.md).
//
// Flow (canonical, подтверждён curl-спайком на Docker Talk 20.1.11):
//
//	1. GET  /login → HTML с requesttoken (атрибут requesttoken="...", НЕ name=value).
//	2. POST /login (form: user, password, requesttoken) → 303 Location /apps/dashboard
//	   (success) или 303 Location /login?direct=1 (fail — CSRF/креды).
//	3. Session-cookie (oc{random}, oc_sessionPassphrase) сохраняется в cookiejar
//	   переданного doer; последующие запросы через этот же jar несут session.
//
// doer должен НЕ следовать редиректам (CheckRedirect → http.ErrUseLastResponse),
// чтобы Login увидел 303 + Location напрямую. В cmd/nctalk-call для этого
// создаётся отдельный loginClient с shared cookiejar; signaling/capability
// используют другой client (SameHostRedirectPolicy) с тем же jar.
//
// Контракт безопасности (спека §5/§9): креды — в form-body POST /login (это
// web-login endpoint Nextcloud, не OCS-API); в URL не светятся; ошибки
// пропускаются через transport.SanitizeErr. Пароль НЕ должен попадать в тексты
// ошибок (login-failure формирует каноническое сообщение без кредов).
package weblogin
