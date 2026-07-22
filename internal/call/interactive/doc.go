// Package interactive реализует TUI-режим аудио-звонка Nextcloud Talk (Этап 4):
// микрофон→Opus (MicSource), PCM→динамик (speakerWriter), локальный mute,
// громкость вывода, ANSI-отрисовка списка участников. Спека 2026-07-21.
// Тонкий слой над agent.Run: интерактивные обёртки (mute/volume) подставляются
// в agent.Config.AudioIn / Stdout, OnState-сallback обогащается в interactive
// и гонится в View (ansiView сейчас, bubbleteaView — future §13).
package interactive
