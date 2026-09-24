// Command nctalk-talk — интерактивный TUI для аудио-звонков Nextcloud Talk.
//
// Спека 2026-07-21 §4.7, §9. Тонкая точка входа: связывает
// config → client → room.ResolveRoom → capability.Settings → signaling →
// interactive.Run. stdin/stdout = терминал (TUI raw-mode + отрисовка); микрофон
// и динамик — отдельные ffmpeg-субпроцессы (см. interactive.NewMicSource /
// NewSpeakerWriter). Логи → call.log.
//
// НЕ импортирует internal/cli, internal/render — это бинарник звонков (как nctalk-call).
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

	"github.com/stas-bool/nctalk-cli/internal/call/capability"
	"github.com/stas-bool/nctalk-cli/internal/call/hpbsignaling"
	"github.com/stas-bool/nctalk-cli/internal/call/interactive"
	"github.com/stas-bool/nctalk-cli/internal/call/signaling"
	"github.com/stas-bool/nctalk-cli/internal/call/weblogin"
	"github.com/stas-bool/nctalk-cli/internal/client"
	"github.com/stas-bool/nctalk-cli/internal/config"
	"github.com/stas-bool/nctalk-cli/internal/exit"
	"github.com/stas-bool/nctalk-cli/internal/room"
	"github.com/stas-bool/nctalk-cli/internal/transport"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Stdin))
}

// run — зеркально cmd/nctalk-call, но без --in/--out/--recvonly (TUI всегда sendrecv),
// и с env NCTALK_AUDIO_DEVICE_IN / NCTALK_AUDIO_DEVICE_OUT / NCTALK_CALL_LOG.
func run(args []string, stdout, stderr io.Writer, stdin io.Reader) int {
	fs := flag.NewFlagSet("nctalk-talk", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("name", "", "искать комнату по имени (case-insensitive подстрока DisplayName)")
	debug := fs.Bool("debug", false, "подробные логи signaling (ставит NCTALK_DEBUG=1)")
	// Парсинг через parseArgs (паритет с nctalk-call, фикс ef7b748 — ревью HPB
	// #1): flag.Parse останавливается на первом не-flag аргументе, а флаги
	// после <room> («nctalk-talk myroom --debug») обязаны работать — без
	// перестановки хвостовые флаги молча игнорировались.
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

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, "nctalk-talk: "+err.Error())
		return 1
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	jar, err := cookiejar.New(nil)
	if err != nil {
		fmt.Fprintf(stderr, "nctalk-talk: cookiejar: %v\n", err)
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
	baseURL, err := url.Parse(cfg.BaseURL)
	if err != nil {
		fmt.Fprintf(stderr, "nctalk-talk: невалидный BaseURL после config.Load: %v\n", err)
		return 1
	}
	auth := transport.Auth{BaseURL: baseURL, Login: cfg.Login, Password: cfg.Password}
	talkClient := client.NewTalkClient(cfg)

	result, err := room.ResolveRoom(ctx, talkClient, positional, *name)
	if err != nil {
		mapped := exit.FromClientErr(err)
		var ee exit.ExitError
		if errors.As(mapped, &ee) {
			fmt.Fprintln(stderr, "nctalk-talk: "+err.Error())
			return ee.Code
		}
		fmt.Fprintln(stderr, "nctalk-talk: "+err.Error())
		return 1
	}
	switch result.Status {
	case room.StatusEmptyInput:
		fmt.Fprintln(stderr, "nctalk-talk: укажите <room> (token) или --name <имя>")
		return 1
	case room.StatusNotFound:
		fmt.Fprintf(stderr, "nctalk-talk: комната не найдена по имени %q\n", result.Query)
		return exit.ExitNotFound
	case room.StatusAmbiguous:
		fmt.Fprintf(stderr, "nctalk-talk: найдено %d комнат по имени %q, уточните:\n", len(result.Candidates), result.Query)
		for _, r := range result.Candidates {
			fmt.Fprintf(stderr, "  %s\t%s\n", r.Token, r.DisplayName)
		}
		return exit.ExitAmbiguous
	case room.StatusResolved:
	}

	// 4a. NCTALK_ICE_TIMEOUT парсим ДО ветвления транспорта: бюджет подключения
	//     hpbesignaling = iceTimeout/2 (дельта §3). 0/пусто → default 30s
	//     подставит agent. Невалидное → exit 1 (как NEXTCLOUD_TIMEOUT).
	//     Сообщение содержит только имя env (§5/§9 redact).
	iceTimeout, err := parseDurationEnv("NCTALK_ICE_TIMEOUT")
	if err != nil {
		fmt.Fprintln(stderr, "nctalk-talk: "+err.Error())
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
		fmt.Fprintln(stderr, "nctalk-talk: signaling-settings: "+err.Error())
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
			fmt.Fprintln(stderr, "nctalk-talk: "+err.Error())
			return 1
		}
	}

	// 6. Signaling-клиент (OCS) + JoinRoom (canonical flow, дельта §2):
	//     participant-session нужна Call API (JoinCall) и серверному списку
	//     участников — ОБЕИМ режимам; разница — в использовании sessionId
	//     (ветка выбора транспорта ниже, перед interactive.Run).
	//     OwnUserId передаём в interactive/agent для извлечения ownSessionId из
	//     usersInRoom — актуально для internal; у external own придёт
	//     EvOwnSession из hello-response (другое id-пространство).
	ocsSig := signaling.New(auth, httpClient)
	sessionId, err := ocsSig.JoinRoom(ctx, result.Token)
	if err != nil {
		fmt.Fprintln(stderr, "nctalk-talk: "+err.Error())
		mapped := exit.FromClientErr(err)
		var jee exit.ExitError
		if errors.As(mapped, &jee) {
			return jee.Code
		}
		return 1
	}

	// Logfile: $NCTALK_CALL_LOG или ./call.log.
	logPath := os.Getenv("NCTALK_CALL_LOG")
	if logPath == "" {
		logPath = "call.log"
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fmt.Fprintf(stderr, "nctalk-talk: call.log: %v\n", err)
		return 1
	}
	defer logFile.Close()

	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	deviceIn := os.Getenv("NCTALK_AUDIO_DEVICE_IN")   // default ":0" — внутри NewMicSource
	deviceOut := os.Getenv("NCTALK_AUDIO_DEVICE_OUT") // default "-1" — внутри NewSpeakerWriter

	iCfg := interactive.Config{
		Cfg:        cfg,
		Token:      result.Token,
		ICEServers: st.ICEServers,
		OwnUserId:  cfg.Login,
		ICETimeout: iceTimeout,
		DeviceIn:   deviceIn,
		DeviceOut:  deviceOut,
		LogFile:    logFile,
	}

	if st.SignalingMode == "external" {
		// external (HPB): weblogin пропущен выше, own — из EvOwnSession.
		// OCS-sessionId идёт ТОЛЬКО в room-join WS (проверка прав в NC), в
		// own-фильтр НЕ подмешивается (другое id-пространство). WS-комнату
		// клиент установит сам в JoinCall (canonical flow, ревью #4).
		iCfg.Signaling = hpbesignaling.New(hpbesignaling.Config{
			Auth:          auth,
			Doer:          httpClient,
			Server:        st.Server,
			Ticket:        st.Ticket,
			Userid:        st.Userid,
			RoomSessionId: sessionId,
			ConnectBudget: connectBudget(iceTimeout),
			Stderr:        logFile, // диагностика reconnect — в call.log (TUI-терминал занят отрисовкой)
		})
	} else {
		// internal: own статически из JoinRoom (порядок пути:
		// weblogin → JoinRoom → SetSessionId → OwnSessionId — ревью плана #6).
		ocsSig.SetSessionId(sessionId) // исходящий POST signaling требует own sessionId
		iCfg.Signaling = ocsSig
		iCfg.OwnSessionId = sessionId // own-фильтр (приоритетный источник)
	}

	err = interactive.Run(sigCtx, iCfg)
	if err == nil {
		fmt.Fprintln(stderr, "nctalk-talk: подробности в "+logPath)
		return 0
	}
	var ee exit.ExitError
	if errors.As(err, &ee) {
		fmt.Fprintln(stderr, "nctalk-talk: "+err.Error()+" (подробнее в "+logPath+")")
		return ee.Code
	}
	fmt.Fprintln(stderr, "nctalk-talk: "+err.Error()+" (подробнее в "+logPath+")")
	return 1
}

// parseArgs парсит флаги в ЛЮБОМ порядке относительно позиционных аргументов
// (копия хелпера из cmd/nctalk-call — cmd-пакеты не импортируют друг другу;
// фикс ef7b748, ревью HPB #1). flag.Parse останавливается на первом не-flag
// аргументе; алгоритм: парсим, пока флаги разбираются; первый не-flag аргумент
// из хвоста забираем в позиционные и парсим остаток дальше (в цикле —
// позиционных может быть несколько). Терминатор «--» flag-пакет съедает сам.
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
// спека §5/§9 redact).
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
// Копия хелпера из cmd/nctalk-call (cmd-пакеты не экспортируют друг другу).
func connectBudget(iceTimeout time.Duration) time.Duration {
	return iceTimeout / 2
}
