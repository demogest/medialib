package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/demogest/medialib/internal/config"
	"github.com/demogest/medialib/internal/server"
	"github.com/demogest/medialib/web"
)

func isLoopbackHost(h string) bool { return h == "127.0.0.1" || h == "localhost" || h == "::1" }

func cmdServe(cfg *config.Config, args []string, mode string) error {
	if mode == "desktop" && !desktopPreflight() {
		return nil // another window of the app is already running and has been brought to the front
	}
	fs := flag.NewFlagSet(mode, flag.ContinueOnError)
	port := fs.Int("port", 0, "")
	host := fs.String("host", "127.0.0.1", "")
	noBrowser := fs.Bool("no-browser", false, "")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if p := os.Getenv("MEDIALIB_HOST"); p != "" && !flagSet(fs, "host") {
		*host = p
	}
	if p := os.Getenv("MEDIALIB_PORT"); p != "" && !flagSet(fs, "port") {
		if n, err := strconv.Atoi(p); err == nil {
			*port = n
		}
	}
	if *port == 0 {
		*port = cfg.Settings().Port
	}
	if mode == "desktop" {
		*host = "127.0.0.1" // the window talks to this computer only
	}

	ln, err := net.Listen("tcp", net.JoinHostPort(*host, strconv.Itoa(*port)))
	if err != nil && mode == "desktop" {
		ln, err = net.Listen("tcp", net.JoinHostPort(*host, "0")) // the usual port is taken: any free one will do
	}
	if err != nil {
		return err
	}
	app := server.NewApp(cfg, mode)
	app.Loopback = isLoopbackHost(*host)
	if os.Getenv("MEDIALIB_LOG") != "" {
		app.Log = log.New(os.Stderr, "", log.LstdFlags)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	served := make(chan error, 1)
	go func() { served <- app.Serve(ctx, ln, web.FS()) }()
	autoIndex := cfg.Settings().AutoIndex
	if autoIndex > 0 {
		go app.AutoIndex(ctx, time.Duration(autoIndex)*time.Minute)
	}
	time.Sleep(50 * time.Millisecond) // Serve fills in the address
	url := fmt.Sprintf("http://127.0.0.1:%d/", ln.Addr().(*net.TCPAddr).Port)

	if mode == "desktop" {
		err := runDesktop(url)
		stop()
		<-served
		return err
	}

	fmt.Printf("Media library on %s\n", url)
	fmt.Printf("  libraries:   %s\n", names(cfg.Libraries(), func(l config.Library) string { return l.Name }))
	fmt.Printf("  connections: %s\n", orNone(names(cfg.Connections(), func(c config.Connection) string { return c.Name })))
	var ps []string
	for _, p := range app.Players {
		ps = append(ps, p.Name)
	}
	fmt.Printf("  players:     %s\n", strings.Join(ps, ", "))
	if autoIndex > 0 {
		fmt.Printf("  indexing:    every library, every %d minutes (auto_index)\n", autoIndex)
	}
	if !app.Loopback {
		if cfg.Settings().Password != "" {
			fmt.Println("Listening beyond this machine, protected by the password (MEDIALIB_PASSWORD or \"password\" in config.json).")
			fmt.Println("Put it behind HTTPS (a reverse proxy) before exposing it to the internet: Basic authentication is sent in the clear.")
		} else {
			fmt.Println("Warning: listening beyond this machine without a password. Libraries and thumbnails are open to anyone on the network;\n" +
				"         the storage browser and every change stay limited to this computer. Set MEDIALIB_PASSWORD to allow them remotely.")
		}
	}
	if !*noBrowser && app.Loopback && os.Getenv("MEDIALIB_NO_BROWSER") == "" {
		time.AfterFunc(600*time.Millisecond, func() { openBrowser(url) })
	}
	select {
	case err := <-served:
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
	case <-ctx.Done():
		<-served
	}
	return nil
}

func flagSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) { set = set || f.Name == name })
	return set
}

func names[T any](list []T, f func(T) string) string {
	out := make([]string, len(list))
	for i, x := range list {
		out[i] = f(x)
	}
	return strings.Join(out, ", ")
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}
