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
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *debug {
		os.Setenv("NCTALK_DEBUG", "1")
	}
	positional := ""
	if fs.NArg() > 0 {
		positional = fs.Arg(0)
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

	if err := weblogin.Login(ctx, loginClient, auth); err != nil {
		fmt.Fprintln(stderr, "nctalk-talk: "+err.Error())
		return 1
	}

	capClient := capability.New(auth, httpClient)
	iceServers, err := capClient.Settings(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "nctalk-talk: capability: %v (продолжаем без STUN/TURN)\n", err)
		iceServers = nil
	}

	sigClient := signaling.New(auth, httpClient)
	sessionId, err := sigClient.JoinRoom(ctx, result.Token)
	if err != nil {
		fmt.Fprintln(stderr, "nctalk-talk: "+err.Error())
		mapped := exit.FromClientErr(err)
		var jee exit.ExitError
		if errors.As(mapped, &jee) {
			return jee.Code
		}
		return 1
	}
	sigClient.SetSessionId(sessionId)

	iceTimeout, err := parseDurationEnv("NCTALK_ICE_TIMEOUT")
	if err != nil {
		fmt.Fprintln(stderr, "nctalk-talk: "+err.Error())
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

	deviceIn := os.Getenv("NCTALK_AUDIO_DEVICE_IN")  // default ":0" — внутри NewMicSource
	deviceOut := os.Getenv("NCTALK_AUDIO_DEVICE_OUT") // default "-1" — внутри NewSpeakerWriter

	err = interactive.Run(sigCtx, interactive.Config{
		Cfg:          cfg,
		Token:        result.Token,
		Signaling:    sigClient,
		ICEServers:   iceServers,
		OwnUserId:    cfg.Login,
		OwnSessionId: sessionId,
		ICETimeout:   iceTimeout,
		DeviceIn:     deviceIn,
		DeviceOut:    deviceOut,
		LogFile:      logFile,
	})
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
