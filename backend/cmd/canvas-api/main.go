package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dolorous01/canvas-standalone/backend/internal/api"
	"github.com/dolorous01/canvas-standalone/backend/internal/asset"
	"github.com/dolorous01/canvas-standalone/backend/internal/authn"
	"github.com/dolorous01/canvas-standalone/backend/internal/config"
	"github.com/dolorous01/canvas-standalone/backend/internal/credential"
	"github.com/dolorous01/canvas-standalone/backend/internal/database"
	"github.com/dolorous01/canvas-standalone/backend/internal/editor"
	"github.com/dolorous01/canvas-standalone/backend/internal/gateway"
	"github.com/dolorous01/canvas-standalone/backend/internal/health"
	"github.com/dolorous01/canvas-standalone/backend/internal/job"
	"github.com/dolorous01/canvas-standalone/backend/internal/library"
	"github.com/dolorous01/canvas-standalone/backend/internal/logging"
	"github.com/dolorous01/canvas-standalone/backend/internal/migrate"
	"github.com/dolorous01/canvas-standalone/backend/internal/objectstore"
	"github.com/dolorous01/canvas-standalone/backend/internal/policy"
	"github.com/dolorous01/canvas-standalone/backend/internal/project"
)

func main() {
	logger := logging.New(os.Stdout, slog.LevelInfo)
	if err := run(logger); err != nil {
		logger.Error("canvas API stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	configuration, err := config.Load(config.RoleAPI)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	db, err := database.Open(ctx, configuration.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := migrate.Verify(ctx, db); err != nil {
		return err
	}
	objects, err := objectstore.NewLocal(configuration.ObjectRoot)
	if err != nil {
		return err
	}
	keyring, err := credential.LoadKeyring(configuration.CredentialActiveKeyVersion, configuration.CredentialKeyFiles)
	if err != nil {
		return err
	}
	defer keyring.Close()
	official, err := gateway.NewClient(configuration.OfficialBaseURL, &http.Client{Timeout: 5 * time.Second})
	if err != nil {
		return err
	}
	assetService := asset.NewService(db, objects)
	credentialRepository := credential.NewRepository(db)
	projectRepository := project.NewRepository(db)
	policyRepository := policy.NewRepository(db)
	canvasHandler, err := api.New(api.Dependencies{
		Authenticator:          authn.New(official, configuration.AuthCacheTTL, 2048),
		Official:               official,
		Credentials:            credentialRepository,
		Projects:               projectRepository,
		Assets:                 assetService,
		Libraries:              library.NewRepository(db),
		Editors:                editor.NewService(db, assetService),
		Jobs:                   job.NewService(db, credentialRepository, projectRepository, policyRepository, assetService),
		Policies:               policyRepository,
		Keyring:                keyring,
		WritesEnabled:          configuration.WritesEnabled,
		Release:                configuration.Release,
		Logger:                 logger,
		AllowedExternalUserIDs: configuration.AllowedExternalUserIDs,
	})
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	health.Handler{DB: db, Objects: objects, Logger: logger, Release: configuration.Release}.Register(mux)
	mux.Handle("/canvas-api/v1/", canvasHandler)
	server := &http.Server{
		Addr:              configuration.ListenAddress,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0, // Job SSE streams own their lifetime through request cancellation.
		IdleTimeout:       90 * time.Second,
	}
	result := make(chan error, 1)
	go func() {
		logger.Info("canvas API listening", "address", configuration.ListenAddress, "release", configuration.Release)
		result <- server.ListenAndServe()
	}()
	select {
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return server.Shutdown(shutdownContext)
	}
}
