package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dolorous01/canvas-standalone/backend/internal/gateway"
	"github.com/dolorous01/canvas-standalone/backend/internal/secretfile"
)

type config struct {
	baseURL       string
	bearerFile    string
	apiKeyFile    string
	output        string
	expectVersion string
	paidProbe     bool
	model         string
}

type checkResult struct {
	Name       string `json:"name"`
	Passed     bool   `json:"passed"`
	ErrorKind  string `json:"error_kind,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

type report struct {
	ContractVersion string        `json:"contract_version"`
	CheckedAt       string        `json:"checked_at"`
	OfficialVersion string        `json:"official_version,omitempty"`
	Checks          []checkResult `json:"checks"`
	Passed          bool          `json:"passed"`
}

func main() {
	configuration := parseFlags()
	if err := run(configuration); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func parseFlags() config {
	var result config
	flag.StringVar(&result.baseURL, "base-url", "", "operator-configured official Sub2API base URL")
	flag.StringVar(&result.bearerFile, "bearer-file", "", "absolute path to a mode-0600 user bearer file")
	flag.StringVar(&result.apiKeyFile, "api-key-file", "", "absolute path to a mode-0600 low-quota API key file")
	flag.StringVar(&result.output, "output", "", "absolute path for the redacted JSON report")
	flag.StringVar(&result.expectVersion, "expect-version", "", "optional exact official version")
	flag.BoolVar(&result.paidProbe, "paid-probe", false, "perform one explicitly authorized image generation")
	flag.StringVar(&result.model, "model", "", "model for an explicitly authorized paid probe")
	flag.Parse()
	return result
}

func run(configuration config) error {
	if configuration.baseURL == "" || !filepath.IsAbs(configuration.output) {
		return errors.New("--base-url and an absolute --output are required")
	}
	if configuration.paidProbe && (configuration.apiKeyFile == "" || strings.TrimSpace(configuration.model) == "") {
		return errors.New("--paid-probe requires --api-key-file and --model")
	}
	client, err := gateway.NewClient(configuration.baseURL, &http.Client{Timeout: 10 * time.Second})
	if err != nil {
		return err
	}
	result := report{ContractVersion: "1", CheckedAt: time.Now().UTC().Format(time.RFC3339), Passed: true}
	ctx := context.Background()

	version, passed := executeCheck(&result, "public_version", func() error {
		value, checkErr := client.PublicVersion(ctx)
		if checkErr != nil {
			return checkErr
		}
		if configuration.expectVersion != "" && value.Version != configuration.expectVersion {
			return &gateway.ContractError{Kind: gateway.ErrorInvalidResponse}
		}
		result.OfficialVersion = value.Version
		return nil
	})
	_ = version
	if !passed {
		return writeFailedReport(configuration.output, result)
	}

	if configuration.bearerFile != "" {
		bearer, readErr := secretfile.Read(configuration.bearerFile, 4096)
		if readErr != nil {
			return readErr
		}
		defer secretfile.Zero(bearer)
		var principal gateway.Principal
		executeCheck(&result, "profile", func() error {
			var checkErr error
			principal, checkErr = client.Profile(ctx, string(bearer))
			return checkErr
		})
		var keys []gateway.APIKeySummary
		executeCheck(&result, "key_list", func() error {
			var checkErr error
			keys, checkErr = client.ListKeys(ctx, string(bearer))
			return checkErr
		})
		if result.Passed && len(keys) > 0 {
			executeCheck(&result, "key_detail", func() error {
				secret, checkErr := client.GetKey(ctx, string(bearer), keys[0].ID)
				if checkErr == nil && secret.UserID != principal.ExternalUserID {
					checkErr = &gateway.ContractError{Kind: gateway.ErrorInvalidResponse}
				}
				secretfile.Zero(secret.Key)
				return checkErr
			})
		}
	}

	if configuration.paidProbe {
		apiKey, readErr := secretfile.Read(configuration.apiKeyFile, 4096)
		if readErr != nil {
			return readErr
		}
		defer secretfile.Zero(apiKey)
		requestBody, marshalErr := json.Marshal(map[string]string{
			"model":           configuration.model,
			"prompt":          "A single neutral gray square on a white background.",
			"response_format": "b64_json",
		})
		if marshalErr != nil {
			return marshalErr
		}
		executeCheck(&result, "image_generation_paid", func() error {
			response, checkErr := client.DoImageRequest(
				ctx,
				"/v1/images/generations",
				apiKey,
				"application/json",
				bytes.NewReader(requestBody),
				"canvas-contract-"+time.Now().UTC().Format("20060102T150405Z"),
			)
			for index := range response.Body {
				response.Body[index] = 0
			}
			return checkErr
		})
	}

	if err := writeReport(configuration.output, result); err != nil {
		return err
	}
	if !result.Passed {
		return errors.New("one or more official contract checks failed; see redacted report")
	}
	return nil
}

func executeCheck(target *report, name string, check func() error) (checkResult, bool) {
	started := time.Now()
	err := check()
	item := checkResult{Name: name, Passed: err == nil, DurationMS: time.Since(started).Milliseconds()}
	if err != nil {
		item.ErrorKind = string(gateway.ErrorKindOf(err))
		target.Passed = false
	}
	target.Checks = append(target.Checks, item)
	return item, item.Passed
}

func writeFailedReport(path string, target report) error {
	if err := writeReport(path, target); err != nil {
		return err
	}
	return errors.New("official public-version contract failed; see redacted report")
}

func writeReport(path string, target report) error {
	payload, err := json.MarshalIndent(target, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".canvas-contract-*.tmp")
	if err != nil {
		return fmt.Errorf("create report: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure report: %w", err)
	}
	if _, err := temporary.Write(payload); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write report: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync report: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close report: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("install report: %w", err)
	}
	committed = true
	return nil
}
