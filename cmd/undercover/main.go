package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-wyvern/undercover/internal/auth"
	"github.com/go-wyvern/undercover/internal/linx"
	"github.com/go-wyvern/undercover/internal/server"
	"github.com/go-wyvern/undercover/internal/storage"
	"github.com/joho/godotenv"
)

func main() {
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		fatal(err)
	}
	addr := flag.String("addr", env("UNDERCOVER_ADDR", "127.0.0.1:18080"), "HTTP listen address")
	importRooms := flag.String("import-rooms", "", "one-time import of a legacy rooms JSON backup")
	flag.Parse()
	host, _, err := net.SplitHostPort(*addr)
	if err != nil {
		fatal(err)
	}
	ip := net.ParseIP(host)
	local := host == "localhost" || (ip != nil && ip.IsLoopback())
	allowGuest := os.Getenv("XBUILDER_ACCOUNT_CLIENT_ID") == "" && local
	if raw := os.Getenv("UNDERCOVER_ALLOW_GUEST"); raw != "" {
		allowGuest, err = strconv.ParseBool(raw)
		if err != nil {
			fatal(err)
		}
	}
	if !local && os.Getenv("XBUILDER_ACCOUNT_CLIENT_ID") == "" && !allowGuest {
		fatal(fmt.Errorf("configure XBuilder Account or explicitly set UNDERCOVER_ALLOW_GUEST=true before listening outside loopback"))
	}
	ids := strings.TrimSpace(env("LINX_DEVICE_IDS", os.Getenv("LINX_DEVICE_ID")))
	var devices []string
	for _, id := range strings.Split(ids, ",") {
		if id = strings.TrimSpace(id); id != "" {
			devices = append(devices, id)
		}
	}
	setup, setupCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer setupCancel()
	rooms, err := storage.OpenPostgres(setup, os.Getenv("DATABASE_URL"))
	if err != nil {
		fatal(err)
	}
	defer rooms.Close()
	sessions, err := storage.OpenRedis(setup, os.Getenv("REDIS_URL"))
	if err != nil {
		fatal(err)
	}
	defer sessions.Close()
	if *importRooms != "" {
		n, err := rooms.ImportLegacy(setup, *importRooms)
		if err != nil {
			fatal(err)
		}
		slog.Info("旧房间导入完成", "imported", n)
	}
	app, err := server.New(server.Config{
		RoomStore: rooms,
		Auth: auth.Config{
			SessionStore:  sessions,
			BaseURL:       os.Getenv("XBUILDER_ACCOUNT_BASE_URL"),
			ClientID:      os.Getenv("XBUILDER_ACCOUNT_CLIENT_ID"),
			ClientSecret:  os.Getenv("XBUILDER_ACCOUNT_CLIENT_SECRET"),
			RedirectURL:   os.Getenv("XBUILDER_ACCOUNT_REDIRECT_URL"),
			SessionSecret: os.Getenv("UNDERCOVER_SESSION_SECRET"),
			AllowGuest:    allowGuest,
		},
		Linx: linx.Config{
			DeviceIDs: devices,
			WSURL:     os.Getenv("LINX_WS_URL"),
			Token:     os.Getenv("LINX_WS_TOKEN"),
			OTAURL:    os.Getenv("LINX_OTA_URL"),
			APIKey:    os.Getenv("LINX_API_KEY"),
		},
	})
	if err != nil {
		fatal(err)
	}
	defer app.Close()
	srv := &http.Server{Addr: *addr, Handler: app.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 75 * time.Second, MaxHeaderBytes: 32 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		app.Close()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	slog.Info("卧底俱乐部已启动", "url", "http://"+*addr, "guest", allowGuest, "linxConfigured", len(devices) > 0, "aiConfigured", len(devices) > 0)
	if err = srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fatal(err)
	}
}
func env(key, fallback string) string {
	if s := os.Getenv(key); s != "" {
		return s
	}
	return fallback
}
func fatal(err error) { slog.Error("启动失败", "error", err); os.Exit(1) }
