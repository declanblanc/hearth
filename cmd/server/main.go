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
	"github.com/dblanc/hearth/internal/profiles"
	"github.com/dblanc/hearth/internal/shared/config"
	"github.com/dblanc/hearth/internal/shared/db"
	"github.com/dblanc/hearth/internal/shared/email"
	"github.com/dblanc/hearth/internal/shared/middleware"
	"github.com/dblanc/hearth/internal/shared/render"
	"github.com/dblanc/hearth/web"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("config load", "err", err)
		os.Exit(1)
	}
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

	authSvc := auth.New(database, sender, cfg.BaseURL)
	profileSvc := profiles.New(database)
	authH := auth.NewHandlers(authSvc, r, cfg.IsProd())
	profileH := profiles.NewHandlers(profileSvc, authSvc, r, cfg.IsProd())

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, req *http.Request) {
		u := middleware.UserFrom(req.Context())
		if u == nil {
			http.Redirect(w, req, "/login", http.StatusSeeOther)
			return
		}
		r.HTML(w, "home.html", map[string]any{"User": u})
	})

	// Static assets.
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(mustSub(web.StaticFS, "static"))))

	// Public auth routes.
	authH.Mount(mux)

	// Protected routes: wrap with RequireAuth via a sub-mux pattern. http.ServeMux
	// doesn't support middleware groups, so we register handlers wrapped individually.
	protected := http.NewServeMux()
	profileH.Mount(protected)
	mux.Handle("/settings/", middleware.RequireAuth(protected))
	// /{username} stays public — the handler enforces its own 404-for-non-owners.
	// (In Phase 1 this becomes "404 unless connected".)
	mux.Handle("GET /{username}", protected)

	// Background sweep: hourly hard-delete of expired soft-deleted users.
	go runHardDeleteSweep(logger, profileSvc)

	chain := middleware.RequestID(
		middleware.Logger(logger)(
			middleware.Recoverer(logger)(
				auth.SessionLoader(authSvc)(mux),
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
