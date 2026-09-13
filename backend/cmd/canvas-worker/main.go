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

	"github.com/dolorous01/canvas-standalone/backend/internal/asset"
	"github.com/dolorous01/canvas-standalone/backend/internal/config"
	"github.com/dolorous01/canvas-standalone/backend/internal/credential"
	"github.com/dolorous01/canvas-standalone/backend/internal/database"
	"github.com/dolorous01/canvas-standalone/backend/internal/gateway"
	"github.com/dolorous01/canvas-standalone/backend/internal/health"
	"github.com/dolorous01/canvas-standalone/backend/internal/job"
	"github.com/dolorous01/canvas-standalone/backend/internal/logging"
	"github.com/dolorous01/canvas-standalone/backend/internal/migrate"
	"github.com/dolorous01/canvas-standalone/backend/internal/objectstore"
	"github.com/dolorous01/canvas-standalone/backend/internal/policy"
	"github.com/dolorous01/canvas-standalone/backend/internal/project"
	"github.com/dolorous01/canvas-standalone/backend/internal/publicid"
)

func main() {
	logger := logging.New(os.Stdout, slog.LevelInfo)
	if err := run(logger); err != nil {
		logger.Error("canvas worker stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	configuration, err := config.Load(config.RoleWorker)
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
	status, err := migrate.Verify(ctx, db)
	if err != nil {
		return err
	}
	objects, err := objectstore.NewLocal(configuration.ObjectRoot)
	if err != nil {
		return err
	}
	if err := objects.Probe(ctx); err != nil {
		return err
	}
	logger.Info("canvas worker ready", "release", configuration.Release, "schema_version", status.SchemaVersion)

	healthMux := http.NewServeMux()
	health.Handler{DB: db, Objects: objects, Logger: logger, Release: configuration.Release, Prefix: "/canvas-worker"}.Register(healthMux)
	healthServer := &http.Server{
		Addr: configuration.ListenAddress, Handler: healthMux,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second,
	}
	healthResult := make(chan error, 1)
	go func() {
		logger.Info("canvas worker health listening", "address", configuration.ListenAddress)
		healthResult <- healthServer.ListenAndServe()
	}()

	workerResult := make(chan error, 1)
	if !configuration.WritesEnabled {
		logger.Info("canvas worker is idle while writes are disabled")
		go func() {
			<-ctx.Done()
			workerResult <- nil
		}()
	} else {
		keyring, loadErr := credential.LoadKeyring(configuration.CredentialActiveKeyVersion, configuration.CredentialKeyFiles)
		if loadErr != nil {
			return loadErr
		}
		defer keyring.Close()
		official, clientErr := gateway.NewClient(configuration.OfficialBaseURL, &http.Client{Timeout: 10 * time.Minute})
		if clientErr != nil {
			return clientErr
		}
		assetService := asset.NewService(db, objects)
		credentialRepository := credential.NewRepository(db)
		projectRepository := project.NewRepository(db)
		policyRepository := policy.NewRepository(db)
		jobService := job.NewService(db, credentialRepository, projectRepository, policyRepository, assetService)
		workerID, idErr := publicid.New("worker")
		if idErr != nil {
			return idErr
		}
		processor := job.NewProcessor(jobService, credentialRepository, keyring, official, assetService, workerID)
		go func() { workerResult <- processor.Run(ctx) }()
	}

	var result error
	select {
	case result = <-workerResult:
		stop()
	case result = <-healthResult:
		if errors.Is(result, http.ErrServerClosed) {
			result = nil
		} else {
			stop()
		}
	case <-ctx.Done():
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	shutdownErr := healthServer.Shutdown(shutdownContext)
	if result != nil {
		return result
	}
	return shutdownErr
}
