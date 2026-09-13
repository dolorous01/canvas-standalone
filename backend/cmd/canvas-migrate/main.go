package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dolorous01/canvas-standalone/backend/internal/buildinfo"
	"github.com/dolorous01/canvas-standalone/backend/internal/config"
	"github.com/dolorous01/canvas-standalone/backend/internal/database"
	"github.com/dolorous01/canvas-standalone/backend/internal/migrate"
	"github.com/dolorous01/canvas-standalone/backend/internal/secretfile"
	"github.com/dolorous01/canvas-standalone/backend/internal/transfer"
)

const usage = `usage:
  canvas-migrate up
  canvas-migrate verify
  canvas-migrate audit-source --source-dsn-file FILE --object-root DIR [--report FILE]
  canvas-migrate export --source-dsn-file FILE --object-root DIR --output DIR [--resolution-file FILE]
  canvas-migrate import --target-dsn-file FILE --input DIR
  canvas-migrate copy-objects --input DIR --source-root DIR --target-root DIR
  canvas-migrate verify-transfer --target-dsn-file FILE --input DIR --target-object-root DIR [--report FILE]`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	if len(arguments) == 0 {
		return errors.New(usage)
	}
	switch arguments[0] {
	case "up", "verify":
		if len(arguments) != 1 {
			return errors.New(usage)
		}
		return runSchemaCommand(arguments[0])
	case "audit-source":
		return runAuditSource(arguments[1:])
	case "export":
		return runExport(arguments[1:])
	case "import":
		return runImport(arguments[1:])
	case "copy-objects":
		return runCopyObjects(arguments[1:])
	case "verify-transfer":
		return runVerifyTransfer(arguments[1:])
	default:
		return errors.New(usage)
	}
}

func runSchemaCommand(command string) error {
	configuration, err := config.Load(config.RoleMigrate)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, err := database.Open(ctx, configuration.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	var status migrate.Status
	if command == "up" {
		status, err = migrate.Apply(ctx, db)
	} else {
		status, err = migrate.Verify(ctx, db)
	}
	if err != nil {
		return err
	}
	return writeResult(status, "")
}

func runAuditSource(arguments []string) error {
	flags := newFlagSet("audit-source")
	dsnFile := flags.String("source-dsn-file", "", "private file containing the read-only source PostgreSQL URL")
	objectRoot := flags.String("object-root", "", "legacy object-store root")
	reportPath := flags.String("report", "", "optional private JSON report path")
	if err := parseFlags(flags, arguments); err != nil {
		return err
	}
	if *dsnFile == "" || *objectRoot == "" {
		return errors.New("audit-source requires --source-dsn-file and --object-root")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	db, err := openDatabaseSecret(ctx, *dsnFile)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := transfer.VerifySourceReadOnlyRole(ctx, db); err != nil {
		return err
	}
	report, err := transfer.AuditSource(ctx, db, *objectRoot)
	if err != nil {
		return err
	}
	return writeResult(report, *reportPath)
}

func runExport(arguments []string) error {
	flags := newFlagSet("export")
	dsnFile := flags.String("source-dsn-file", "", "private file containing the read-only source PostgreSQL URL")
	objectRoot := flags.String("object-root", "", "legacy object-store root")
	output := flags.String("output", "", "new private export directory")
	resolution := flags.String("resolution-file", "", "private unresolved-job decision file")
	if err := parseFlags(flags, arguments); err != nil {
		return err
	}
	if *dsnFile == "" || *objectRoot == "" || *output == "" {
		return errors.New("export requires --source-dsn-file, --object-root, and --output")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	db, err := openDatabaseSecret(ctx, *dsnFile)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := transfer.VerifySourceReadOnlyRole(ctx, db); err != nil {
		return err
	}
	info := buildinfo.Current()
	manifest, err := transfer.Export(ctx, db, *objectRoot, *output, transfer.ExportOptions{
		ExporterVersion: info.Version + "@" + info.Revision,
		ResolutionPath:  *resolution,
	})
	if err != nil {
		return err
	}
	return writeResult(manifest, "")
}

func runImport(arguments []string) error {
	flags := newFlagSet("import")
	dsnFile := flags.String("target-dsn-file", "", "private file containing the target PostgreSQL URL")
	input := flags.String("input", "", "private export directory")
	if err := parseFlags(flags, arguments); err != nil {
		return err
	}
	if *dsnFile == "" || *input == "" {
		return errors.New("import requires --target-dsn-file and --input")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	db, err := openDatabaseSecret(ctx, *dsnFile)
	if err != nil {
		return err
	}
	defer db.Close()
	report, err := transfer.Import(ctx, db, *input)
	if err != nil {
		return err
	}
	return writeResult(report, "")
}

func runCopyObjects(arguments []string) error {
	flags := newFlagSet("copy-objects")
	input := flags.String("input", "", "private export directory")
	sourceRoot := flags.String("source-root", "", "legacy object-store root")
	targetRoot := flags.String("target-root", "", "standalone object-store root")
	if err := parseFlags(flags, arguments); err != nil {
		return err
	}
	if *input == "" || *sourceRoot == "" || *targetRoot == "" {
		return errors.New("copy-objects requires --input, --source-root, and --target-root")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	report, err := transfer.CopyObjects(ctx, *input, *sourceRoot, *targetRoot)
	if err != nil {
		return err
	}
	return writeResult(report, "")
}

func runVerifyTransfer(arguments []string) error {
	flags := newFlagSet("verify-transfer")
	dsnFile := flags.String("target-dsn-file", "", "private file containing the target PostgreSQL URL")
	input := flags.String("input", "", "private export directory")
	objectRoot := flags.String("target-object-root", "", "standalone object-store root")
	reportPath := flags.String("report", "", "optional private JSON report path")
	if err := parseFlags(flags, arguments); err != nil {
		return err
	}
	if *dsnFile == "" || *input == "" || *objectRoot == "" {
		return errors.New("verify-transfer requires --target-dsn-file, --input, and --target-object-root")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	db, err := openDatabaseSecret(ctx, *dsnFile)
	if err != nil {
		return err
	}
	defer db.Close()
	report, err := transfer.VerifyTransfer(ctx, db, *input, *objectRoot)
	if err != nil {
		return err
	}
	return writeResult(report, *reportPath)
}

func newFlagSet(name string) *flag.FlagSet {
	result := flag.NewFlagSet(name, flag.ContinueOnError)
	result.SetOutput(io.Discard)
	return result
}

func parseFlags(flags *flag.FlagSet, arguments []string) error {
	if err := flags.Parse(arguments); err != nil {
		return fmt.Errorf("%s: %w", usage, err)
	}
	if flags.NArg() != 0 {
		return errors.New(usage)
	}
	return nil
}

func openDatabaseSecret(ctx context.Context, path string) (*sql.DB, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("database secret file must be an absolute path")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect database secret file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("database secret must be a private regular file")
	}
	payload, err := secretfile.Read(path, 16<<10)
	if err != nil {
		return nil, fmt.Errorf("read database secret file: %w", err)
	}
	defer secretfile.Zero(payload)
	value := strings.TrimSpace(string(payload))
	if value == "" {
		return nil, errors.New("database secret file is empty")
	}
	return database.Open(ctx, value)
}

func writeResult(value any, path string) error {
	if strings.TrimSpace(path) == "" {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetEscapeHTML(false)
		encoder.SetIndent("", "  ")
		return encoder.Encode(value)
	}
	if !filepath.IsAbs(path) {
		return errors.New("report path must be absolute")
	}
	path = filepath.Clean(path)
	if path == string(filepath.Separator) {
		return errors.New("report path must not be the filesystem root")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create report directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".canvas-report-")
	if err != nil {
		return fmt.Errorf("create report: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0600); err != nil {
		return err
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish report: %w", err)
	}
	committed = true
	return nil
}
