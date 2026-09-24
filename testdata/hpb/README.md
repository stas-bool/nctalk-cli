# `testdata/hpb/` — фикстуры WS-протокола nextcloud-spreed-signaling (HPB)

**Источник:** спайк Task 1 (2026-09-23) на живом HPB-сервере (external-режим,
`internal/call/hpbsignaling/integration_test.go`, build-tag `integration`);
welcome/hello/room/event_join — реальные кадры, event_leave/participants_update/
message_offer/message_candidate — канон исходников `strukturag/nextcloud-spreed-signaling`
+ JS-клиента spreed (не сняты со спайка, помечено в `_comment` каждого файла).
**Обезличено:** хост signaling-сервера, ticket/token auth, HPB-sessionid (64 симв.),
OCS-roomsessionid (255 симв.), resumeid, userId/displayname → `alice`/`bob`, roomid → `tok-test`.

Формат обязателен к сверке при правках `hpbesignaling` (инвариант фикстур CLAUDE.md:
фикстуры отражают реальный wire-формат каждого эндпоинта). Контракт — дельта-спека
`docs/superpowers/specs/2026-09-23-nctalk-hpb-signaling-design.md` §2.
