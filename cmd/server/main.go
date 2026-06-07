// Command server is the Hearth HTTP server. It loads config, opens the SQLite
// database, runs migrations, wires the handlers, and starts listening.
package main

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dblanc/hearth/internal/auth"
	"github.com/dblanc/hearth/internal/connections"
	"github.com/dblanc/hearth/internal/feed"
	"github.com/dblanc/hearth/internal/likes"
	"github.com/dblanc/hearth/internal/media"
	"github.com/dblanc/hearth/internal/notifications"
	"github.com/dblanc/hearth/internal/posts"
	"github.com/dblanc/hearth/internal/profiles"
	"github.com/dblanc/hearth/internal/shared/config"
	"github.com/dblanc/hearth/internal/shared/db"
	"github.com/dblanc/hearth/internal/shared/email"
	"github.com/dblanc/hearth/internal/shared/middleware"
	"github.com/dblanc/hearth/internal/shared/render"
	"github.com/dblanc/hearth/web"
	"github.com/lmittmann/tint"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		// Bootstrap logger before config is fully valid so we can report the error.
		slog.New(slog.NewTextHandler(os.Stderr, nil)).Error("config load", "err", err)
		os.Exit(1)
	}

	var handler slog.Handler
	if cfg.IsProd() {
		handler = slog.NewJSONHandler(os.Stdout, nil)
	} else {
		handler = tint.NewHandler(os.Stdout, &tint.Options{TimeFormat: "15:04:05"})
	}
	logger := slog.New(handler)
	slog.SetDefault(logger)

	logger.Info("starting", "env", cfg.Env, "addr", cfg.Addr)

	database, err := db.Open(cfg.DBPath)
	if err != nil {
		logger.Error("db open", "err", err)
		os.Exit(1)
	}
	defer database.Close()
	if err := db.Migrate(database); err != nil {
		logger.Error("db migrate", "err", err)
		os.Exit(1)
	}

	// In development, ensure a pre-verified user exists so the app is usable
	// without the signup/email-verify round-trip. Never runs in prod.
	if cfg.Env == config.EnvDevelopment {
		seedDevUser(database, logger)
	}

	var sender email.Sender
	if cfg.ResendConfigured() {
		sender = email.NewResend(cfg.ResendAPIKey, cfg.EmailFrom)
	} else {
		sender = &devEmailLogger{Logger: logger}
		logger.Warn("email: Resend API key not configured — using stdout logger")
	}

	r, err := render.NewFromEmbed(web.TemplatesFS, "templates", cfg.Env == config.EnvDevelopment)
	if err != nil {
		logger.Error("render init", "err", err)
		os.Exit(1)
	}

	var mediaStore media.Store
	if cfg.R2Configured() {
		mediaStore = media.NewR2(cfg.R2AccountID, cfg.R2AccessKeyID, cfg.R2SecretAccessKey, cfg.R2Bucket, cfg.R2PublicHost)
		logger.Info("media: R2 configured", "bucket", cfg.R2Bucket)
	} else {
		logger.Warn("media: R2 not configured — photo uploads disabled")
	}

	// Services.
	authSvc := auth.New(database, sender, cfg.BaseURL)
	notifSvc := notifications.New(database)
	connSvc := connections.New(database, notifSvc, cfg.BaseURL)
	postSvc := posts.New(database)
	postSvc.Media = mediaStore
	commentSvc := posts.NewCommentService(database, connSvc, notifSvc)
	feedSvc := feed.New(database, connSvc)
	likeSvc := likes.New(database, connSvc, notifSvc)
	profileSvc := profiles.New(database)
	profileSvc.Media = mediaStore

	// Handlers.
	authH := auth.NewHandlers(authSvc, r, cfg.CookieSecret, cfg.IsProd())
	notifH := notifications.NewHandlers(notifSvc, r)
	connH := connections.NewHandlers(connSvc, r, mediaStore, cfg.CookieSecret, cfg.IsProd())
	postH := posts.NewHandlers(postSvc, r)
	commentH := posts.NewCommentHandlers(commentSvc, r)
	likeH := likes.NewHandlers(likeSvc, r)
	feedH := feed.NewHandlers(feedSvc, r, mediaStore, likeSvc, commentSvc)
	profileH := profiles.NewHandlers(profileSvc, authSvc, connSvc, postSvc, likeSvc, commentSvc, r, mediaStore, cfg.IsProd())

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// Static assets.
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(mustSub(web.StaticFS, "static"))))

	// Each package mounts its own routes, wrapping authenticated handlers with
	// middleware.RequireAuth internally. Public routes (auth pages, the invite
	// opener, and /u/{username}) are registered bare and do their own gating.
	authH.Mount(mux)
	feedH.Mount(mux)
	postH.Mount(mux)
	commentH.Mount(mux)
	likeH.Mount(mux)
	notifH.Mount(mux)
	connH.Mount(mux)
	profileH.Mount(mux)

	// Background sweep: hourly hard-delete of expired soft-deleted users.
	go runHardDeleteSweep(logger, profileSvc)

	chain := middleware.RequestID(
		middleware.Logger(logger)(
			middleware.Recoverer(logger)(
				auth.SessionLoader(authSvc)(
					notifH.LoadUnread(
						connH.LoadPendingDot(mux),
					),
				),
			),
		),
	)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           chain,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("listen", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	logger.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

func mustSub(efs embed.FS, dir string) fs.FS {
	sub, err := fs.Sub(efs, dir)
	if err != nil {
		panic(err)
	}
	return sub
}

// devEmailLogger prints email payloads to the logger instead of sending them.
type devEmailLogger struct{ Logger *slog.Logger }

func (d *devEmailLogger) Send(_ context.Context, m email.Message) error {
	d.Logger.Info("email", "to", m.To, "subject", m.Subject, "body", m.TextBody)
	return nil
}

func runHardDeleteSweep(logger *slog.Logger, svc *profiles.Service) {
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		<-tick.C
		n, err := svc.HardDeleteExpired(context.Background())
		if err != nil {
			logger.Error("hard-delete sweep", "err", err)
			continue
		}
		if n > 0 {
			logger.Info("hard-delete sweep", "removed", n)
		}
	}
}
