// Command nctalk-call — pipe-режим для аудио-звонков Nextcloud Talk.
//
// Спека 2026-07-19 §6/§10/§12. Тонкая точка входа: связывает
// config → client → room.ResolveRoom → capability.Settings → signaling →
// agent.Run. PCM s16le/48к/моно через stdin (--in) и stdout (--out).
//
// НЕ импортирует internal/cli, internal/render — это бинарник агента (спека
// §4). Печать candidates при неоднозначном ResolveRoom делается вручную
// (построчно, без render.Candidates).
//
// Контракт безопасности (§5/§9): креды только в env (через config.Load), в URL
// не светятся, в вывод не попадают. Все сетевые ошибки приходят sanitized из
// client/signaling/transport (без userinfo, без Authorization, без query).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"

	"github.com/stas/nctalk/internal/call/agent"
	"github.com/stas/nctalk/internal/call/capability"
	"github.com/stas/nctalk/internal/call/signaling"
	"github.com/stas/nctalk/internal/client"
	"github.com/stas/nctalk/internal/config"
	"github.com/stas/nctalk/internal/exit"
	"github.com/stas/nctalk/internal/room"
	"github.com/stas/nctalk/internal/transport"
)

// InCall-флаги Nextcloud Talk (спека §6). Локальные константы — agent хранит
// свои unexported, нам достаточно совпадения чисел (1 и 3).
const (
	inFlagRecvOnly = 1 // IN_CALL
	inFlagSendRecv = 3 // IN_CALL | WITH_AUDIO
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Stdin))
}

// run связывает слои по схеме brief Task 2.9. Возвращает exit-код:
//   - 0 — штатный выход (ctx отменён, agent.Run вернул nil);
//   - 1 — общая ошибка (config, network, unknown);
//   - 2 — not found (room.ResolveRoom дал 0 совпадений по --name);
//   - 3 — ambiguous (>1 совпадения по --name).
//
// Ошибки config.Load, ResolveRoom и agent.Run пишутся в stderr одной строкой с
// префиксом "nctalk-call:". Тексты приходят sanitized из соответствующих слоёв;
// здесь НЕ формируем строки с URL/кредами.
//
// args/stdout/stderr/stdin передаются параметрами для testability (e2e-тесты в
// main_test.go подменяют потоки на *bytes.Buffer и подают args напрямую).
func run(args []string, stdout, stderr io.Writer, stdin io.Reader) int {
	// 1. Парсинг флагов: <room> (позиционный, это token), --name (поиск по
	//    DisplayName), --in/--out (пути PCM-файлов или `-` для stdin/stdout).
	fs := flag.NewFlagSet("nctalk-call", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		name    = fs.String("name", "", "искать комнату по имени (case-insensitive подстрока DisplayName)")
		inPath  = fs.String("in", "-", "PCM s16le/48к/моно для отправки: путь файла или `-` для stdin")
		outPath = fs.String("out", "-", "куда писать входящий PCM: путь файла или `-` для stdout")
	)
	if err := fs.Parse(args); err != nil {
		return 1
	}
	positional := ""
	if fs.NArg() > 0 {
		positional = fs.Arg(0)
	}

	// 2. Конфигурация из env. Сообщения об ошибках — только имена env-переменных.
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, "nctalk-call: "+err.Error())
		return 1
	}

	// 3. HTTP-клиент (Timeout + SameHostRedirectPolicy) + Auth для capability/signaling.
	httpClient := &http.Client{
		Transport:     http.DefaultTransport,
		Timeout:       cfg.Timeout,
		CheckRedirect: transport.SameHostRedirectPolicy,
	}
	// config.Load гарантировал валидность URL; panic при url.Parse здесь —
	// баг регресса config (см. client.NewTalkClientWithDoer). Обрабатываем
	// консервативно: лог + exit 1, без паники.
	baseURL, err := url.Parse(cfg.BaseURL)
	if err != nil {
		fmt.Fprintf(stderr, "nctalk-call: невалидный BaseURL после config.Load: %v\n", err)
		return 1
	}
	auth := transport.Auth{BaseURL: baseURL, Login: cfg.Login, Password: cfg.Password}
	talkClient := client.NewTalkClient(cfg)

	// 4. Разрешение <room>/--name → token. Сетевая ошибка → exit.FromClientErr.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	result, err := room.ResolveRoom(ctx, talkClient, positional, *name)
	if err != nil {
		// FromClientErr возвращает *exit.ExitError (404 → 2, прочее → 1).
		mapped := exit.FromClientErr(err)
		var ee *exit.ExitError
		if errors.As(mapped, &ee) {
			fmt.Fprintln(stderr, "nctalk-call: "+err.Error())
			return ee.Code
		}
		// На практике ветка недостижима (FromClientErr всегда возвращает ExitError),
		// но для robustness: unknown → 1.
		fmt.Fprintln(stderr, "nctalk-call: "+err.Error())
		return 1
	}
	switch result.Status {
	case room.StatusEmptyInput:
		fmt.Fprintln(stderr, "nctalk-call: укажите <room> (token) или --name <имя>")
		return 1
	case room.StatusNotFound:
		fmt.Fprintf(stderr, "nctalk-call: комната не найдена по имени %q\n", result.Query)
		return exit.ExitNotFound
	case room.StatusAmbiguous:
		// Печать candidates построчно БЕЗ render (инвариант §4 — cmd/nctalk-call
		// не импортирует internal/render). Формат: "  <token>\t<displayName>".
		fmt.Fprintf(stderr, "nctalk-call: найдено %d комнат по имени %q, уточните:\n", len(result.Candidates), result.Query)
		for _, r := range result.Candidates {
			fmt.Fprintf(stderr, "  %s\t%s\n", r.Token, r.DisplayName)
		}
		return exit.ExitAmbiguous
	case room.StatusResolved:
		// ok — продолжаем с result.Token.
	}

	// 5. Capability (STUN/TURN из signaling-settings). Best-effort: при ошибке
	//    логируем и продолжаем с пустым списком — pion примет пустой []ICEServer
	//    (host candidates хватит для same-network spike).
	capClient := capability.New(auth, httpClient)
	iceServers, err := capClient.Settings(ctx, result.Token)
	if err != nil {
		fmt.Fprintf(stderr, "nctalk-call: capability: %v (продолжаем без STUN/TURN)\n", err)
		iceServers = nil
	}

	// 6. InFlags и --in источник.
	//
	// Семантика pipe-режима (спека §6): по умолчанию sendrecv — stdin
	// подключён к PCM-источнику (pipe), stdout — к PCM-приёмнику. Пользователь
	// может явно открыть файл через --in <path>; "-" означает stdin (default).
	//
	// recvonly (InFlags=1) сейчас не доступен отдельным флагом — pipe-режим
	// агента по умолчанию sendrecv (stdin подключён). TODO Task 3.x: добавить
	// --recvonly для случая «только запись чужого аудио, без отправки своего»
	// (полезно для one-way мониторинга). На spike это не требуется.
	var pcmIn io.Reader = stdin // default: stdin (pipe-mode)
	if *inPath != "" && *inPath != "-" {
		f, err := os.Open(*inPath)
		if err != nil {
			fmt.Fprintf(stderr, "nctalk-call: --in: %v\n", err)
			return 1
		}
		defer f.Close()
		pcmIn = f
	}
	inFlags := inFlagSendRecv

	// 7. --out: путь файла или `-` (default) для stdout.
	var pcmOut io.Writer = stdout
	if *outPath != "" && *outPath != "-" {
		f, err := os.Create(*outPath)
		if err != nil {
			fmt.Fprintf(stderr, "nctalk-call: --out: %v\n", err)
			return 1
		}
		defer f.Close()
		pcmOut = f
	}

	// 8. Signaling-клиент. SetSessionId НЕ зовём — ownSessionId остаётся ""
	// (фильтр own-session в agent'е работает по sessionId из signaling-ответа;
	// для spike-режима фильтр не критичен, сервер всё равно сообщает всем).
	// TODO Task 3.x: вытащить ownSessionId из signaling-settings / первого
	// usersInRoom и выставить до agent.Run.
	sigClient := signaling.New(auth, httpClient)

	// 9. Контекст с signal.NotifyContext (SIGINT, SIGTERM). На сигнале ctx
	//    отменяется, agent выходит по ctx.Done → штатный leave (LeaveCall в
	//    best-effort 3с, см. agent.leaveBestEffort).
	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 10. agent.Run. ownSessionId="" — spike-допустимо (см. TODO выше).
	agentErr := agent.Run(sigCtx, agent.Config{
		Signaling:    sigClient,
		Token:        result.Token,
		InFlags:      inFlags,
		ICEServers:   iceServers,
		Stdin:        pcmIn,
		Stdout:       pcmOut,
		Stderr:       stderr,
		OwnSessionId: "",
	})

	// 11. Маппинг ошибки. nil → 0; *exit.ExitError → Code; прочее → 1.
	if agentErr == nil {
		return 0
	}
	var ee *exit.ExitError
	if errors.As(agentErr, &ee) {
		fmt.Fprintln(stderr, "nctalk-call: "+agentErr.Error())
		return ee.Code
	}
	fmt.Fprintln(stderr, "nctalk-call: "+agentErr.Error())
	return 1
}
