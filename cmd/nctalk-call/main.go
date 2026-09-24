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
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/stas-bool/nctalk-cli/internal/call/agent"
	"github.com/stas-bool/nctalk-cli/internal/call/capability"
	"github.com/stas-bool/nctalk-cli/internal/call/hpbsignaling"
	"github.com/stas-bool/nctalk-cli/internal/call/signaling"
	"github.com/stas-bool/nctalk-cli/internal/call/weblogin"
	"github.com/stas-bool/nctalk-cli/internal/client"
	"github.com/stas-bool/nctalk-cli/internal/config"
	"github.com/stas-bool/nctalk-cli/internal/exit"
	"github.com/stas-bool/nctalk-cli/internal/room"
	"github.com/stas-bool/nctalk-cli/internal/transport"
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
	//    DisplayName), --in/--out (пути PCM-файлов или `-` для stdin/stdout),
	//    --recvonly (listening-only, спека §6).
	fs := flag.NewFlagSet("nctalk-call", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		name     = fs.String("name", "", "искать комнату по имени (case-insensitive подстрока DisplayName)")
		inPath   = fs.String("in", "-", "PCM s16le/48к/моно для отправки: путь файла или `-` для stdin")
		outPath  = fs.String("out", "-", "куда писать входящий PCM: путь файла или `-` для stdout")
		recvOnly = fs.Bool("recvonly", false, "только приём чужого аудио (своего не отправлять); InFlags=1 (спека §6)")
		debug    = fs.Bool("debug", false, "подробные логи signaling (ставит NCTALK_DEBUG=1)")
	)
	// Парсинг через parseArgs: flag.Parse останавливается на первом не-flag
	// аргументе, а канонический синтаксис спеки §6 ставит флаги ПОСЛЕ <room>
	// («nctalk-call <room> --in rec.pcm --out play.pcm») — без перестановки
	// хвостовые флаги молча игнорировались (recvonly не применялся → sendrecv).
	posArgs, err := parseArgs(fs, args)
	if err != nil {
		return 1
	}
	if *debug {
		os.Setenv("NCTALK_DEBUG", "1")
	}
	positional := ""
	if len(posArgs) > 0 {
		positional = posArgs[0]
	}

	// 2. Конфигурация из env. Сообщения об ошибках — только имена env-переменных.
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, "nctalk-call: "+err.Error())
		return 1
	}

	// 3. HTTP-клиенты с SHARED cookiejar: loginClient (no-redirect — для web-login,
	//    чтобы прочитать 303 Location) и httpClient (SameHostRedirectPolicy — для
	//    capability/signaling). Session-cookie от weblogin.Login живёт в jar;
	//    capability/signaling его подхватывают. Без PHP-session signaling pull → 404
	//    (баг #5, см. internal/call/weblogin/doc.go).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	jar, err := cookiejar.New(nil)
	if err != nil {
		fmt.Fprintf(stderr, "nctalk-call: cookiejar: %v\n", err)
		return 1
	}
	loginClient := &http.Client{
		Transport:     http.DefaultTransport,
		Timeout:       cfg.Timeout,
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	httpClient := &http.Client{
		Transport:     http.DefaultTransport,
		Timeout:       cfg.Timeout,
		Jar:           jar,
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
	result, err := room.ResolveRoom(ctx, talkClient, positional, *name)
	if err != nil {
		// FromClientErr возвращает exit.ExitError-value (404 → 2, прочее → 1).
		mapped := exit.FromClientErr(err)
		var ee exit.ExitError
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

	// 4a. NCTALK_ICE_TIMEOUT парсим ДО ветвления транспорта: бюджет подключения
	//     hpbesignaling = iceTimeout/2 (дельта §3). 0/пусто → default 30s
	//     подставит agent. Невалидное → exit 1 (как NEXTCLOUD_TIMEOUT).
	//     Сообщение содержит только имя env (§5/§9 redact).
	iceTimeout, err := parseDurationEnv("NCTALK_ICE_TIMEOUT")
	if err != nil {
		fmt.Fprintln(stderr, "nctalk-call: "+err.Error())
		return 1
	}

	// 5. Capability: signaling-settings — режим транспорта + STUN/TURN.
	//    ОДИН запрос на старт; ошибка ФАТАЛЬНА (дельта §3): без settings
	//    транспорт не выбрать, а молчаливый fallback на internal воспроизводил
	//    бы исходный баг «слепого» звонка на HPB (§1). Практический риск мал —
	//    settings ходит на тот же сервер, что JoinRoom/JoinCall следом.
	capClient := capability.New(auth, httpClient)
	st, err := capClient.Settings(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "nctalk-call: signaling-settings: "+err.Error())
		mapped := exit.FromClientErr(err)
		var cee exit.ExitError
		if errors.As(mapped, &cee) {
			return cee.Code
		}
		return 1
	}

	// 5a. internal: web-login → PHP-session в shared jar (баг #5 базовой спеки:
	//     без session signaling pull → 404). Идёт ДО JoinRoom — порядок
	//     внутреннего пути сохранён «байт-в-байт» (weblogin → JoinRoom →
	//     SetSessionId → OwnSessionId), как обещает Global Constraints (ревью
	//     плана #6). external НЕ зовёт weblogin: PHP-session нужна только
	//     OCS-pull, который там не используется (дельта §2). Запускается ПОСЛЕ
	//     ResolveRoom (незачем логиниться, если room не задан/не найден/
	//     ambiguous); loginClient — no-redirect (чтобы прочитать 303 Location,
	//     см. weblogin.doc.go); session-cookie попадает в jar, его подхватывает
	//     signaling через httpClient.
	if st.SignalingMode != "external" {
		if err := weblogin.Login(ctx, loginClient, auth); err != nil {
			fmt.Fprintln(stderr, "nctalk-call: "+err.Error())
			return 1
		}
	}

	// 6. InFlags и --in источник.
	//
	// Семантика pipe-режима (спека §6):
	//   - По умолчанию sendrecv: stdin подключён к PCM-источнику, stdout — к
	//     PCM-приёмнику.
	//   - --in <path>: открыть файл как источник PCM (вместо stdin).
	//   - --recvonly: listening-only. InFlags=1 (IN_CALL без WITH_AUDIO), SDP
	//     несёт только recvonly m-line аудио. Полезно для записи чужого аудио
	//     без отправки своего (спека §6 пример «nctalk-call <room> --out rec.pcm»).
	//
	// InFlags выбирается так:
	//   - --recvonly → 1 (recvonly);
	//   - иначе → 3 (sendrecv) с PCM-источником из stdin/--in.
	//
	// Контракт §6: «только --out → flags = IN_CALL без WITH_AUDIO». Флаг
	// --recvonly делает этот режим явным (и не требует пустого --in).
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
	var inFlags int
	switch {
	case *recvOnly:
		inFlags = inFlagRecvOnly
		pcmIn = nil // не нужно — encoder не запустится
	default:
		inFlags = inFlagSendRecv
	}

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

	// 8. Signaling-клиент (OCS) + JoinRoom (canonical flow, дельта §2):
	//     participant-session нужна Call API (JoinCall) и серверному списку
	//     участников — ОБЕИМ режимам; разница — в использовании sessionId (8a).
	//     OwnUserId передаём в agent для извлечения ownSessionId из usersInRoom
	//     (review замечание 3) — актуально для internal; у external own придёт
	//     EvOwnSession из hello-response (другое id-пространство).
	ocsSig := signaling.New(auth, httpClient)

	// 8a. joinRoom → participant session (баг #3). Без joinRoom signaling pull
	//     даст 404 (CallController требует session; canonical flow web-login →
	//     joinRoom → pull → joinCall — баг #4, порядок pull/joinCall некритичен,
	//     оба 200 — см. signaling.JoinRoom doc). sessionId используется ПО РЕЖИМУ:
	//     internal — SetSessionId + own-фильтр, external — ТОЛЬКО room-join WS.
	sessionId, err := ocsSig.JoinRoom(ctx, result.Token)
	if err != nil {
		fmt.Fprintln(stderr, "nctalk-call: "+err.Error())
		mapped := exit.FromClientErr(err)
		var jee exit.ExitError
		if errors.As(mapped, &jee) {
			return jee.Code
		}
		return 1
	}

	agentCfg := agent.Config{
		Token:      result.Token,
		InFlags:    inFlags,
		ICEServers: st.ICEServers,
		Stdin:      pcmIn,
		Stdout:     pcmOut,
		Stderr:     stderr,
		OwnUserId:  cfg.Login,
		ICETimeout: iceTimeout,
	}

	if st.SignalingMode == "external" {
		// external (HPB, дельта §2/§3): weblogin НЕ нужен (PHP-session живёт
		// только в OCS-pull, который не используется); OCS-sessionId идёт
		// ТОЛЬКО в room-join WS (проверка прав в NC), в own-фильтр НЕ
		// подмешивается (другое id-пространство) — own придёт EvOwnSession.
		// WS-комнату клиент установит сам в JoinCall — ДО делегирования OCS
		// (canonical flow, ревью плана #4).
		agentCfg.Signaling = hpbesignaling.New(hpbesignaling.Config{
			Auth:          auth,
			Doer:          httpClient,
			Server:        st.Server,
			Ticket:        st.Ticket,
			Userid:        st.Userid,
			RoomSessionId: sessionId,
			ConnectBudget: connectBudget(iceTimeout),
			Stderr:        stderr,
		})
	} else {
		// internal: прежний путь (дельта §3): SetSessionId + own из JoinRoom.
		ocsSig.SetSessionId(sessionId) // исходящий POST signaling требует own sessionId
		agentCfg.Signaling = ocsSig
		agentCfg.OwnSessionId = sessionId // own-фильтр (приоритетный источник)
	}

	// 9. Контекст с signal.NotifyContext (SIGINT, SIGTERM). На сигнале ctx
	//    отменяется, agent выходит по ctx.Done → штатный leave (LeaveCall в
	//    best-effort 3с, см. agent.leaveBestEffort).
	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 10. agent.Run. Конфиг собран в 8a по режиму транспорта: internal —
	//     OCS-polling + OwnSessionId из joinRoom (точнее, чем извлечение по
	//     userId из EvUsersUpdated — review замечание 3 теперь имеет
	//     приоритетный источник); external — hpbesignaling (own придёт
	//     EvOwnSession, OwnSessionId не задаём).
	agentErr := agent.Run(sigCtx, agentCfg)

	// 11. Маппинг ошибки. nil → 0; exit.ExitError-value → Code; прочее → 1.
	if agentErr == nil {
		return 0
	}
	var ee exit.ExitError
	if errors.As(agentErr, &ee) {
		fmt.Fprintln(stderr, "nctalk-call: "+agentErr.Error())
		return ee.Code
	}
	fmt.Fprintln(stderr, "nctalk-call: "+agentErr.Error())
	return 1
}

// parseArgs парсит флаги в ЛЮБОМ порядке относительно позиционных аргументов.
// flag.Parse останавливается на первом не-flag аргументе; канонический синтаксис
// спеки §6 — «nctalk-call <room> --in rec.pcm --out play.pcm» (флаги после
// позиционного). Алгоритм: парсим, пока флаги разбираются; первый не-flag
// аргумент из хвоста забираем в позиционные и парсим остаток дальше (в цикле —
// позиционных может быть несколько: «a b --flag» и «--flag a --flag2 b»).
// Терминатор «--» flag-пакет съедает сам; всё после него — позиционные.
// Возвращает позиционные аргументы в порядке встречи (run использует первый).
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
	return positional, nil
}

// parseDurationEnv читает env-переменную как time.Duration. Пусто → 0 (вызывающий
// подставит default). Невалидное значение → ошибка с именем env (без значения —
// спека §5/§9 redact, как config.Load для NEXTCLOUD_TIMEOUT). Звонки-specific
// (NCTALK_ICE_TIMEOUT), не входит в общий config.Load.
func parseDurationEnv(name string) (time.Duration, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: невалидная длительность (ожидалось напр. «30s», «1m30s»)", name)
	}
	return d, nil
}

// connectBudget — бюджет первичного подключения hpbesignaling: половина
// ICE-таймаута (дельта §3: заведомо меньше, чтобы EvError успел ДО
// ICE-таймера). iceTimeout==0 → 0 → hpbesignaling подставит дефолт 15с.
func connectBudget(iceTimeout time.Duration) time.Duration {
	return iceTimeout / 2
}
