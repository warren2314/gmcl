package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"cricket-ground-feedback/internal/db"
	"cricket-ground-feedback/internal/portal"
)

type preflightResult struct {
	Status                string                             `json:"status"`
	Environment           *portal.PortalEnvironmentPreflight `json:"environment,omitempty"`
	Database              *portal.PortalDatabasePreflight    `json:"database,omitempty"`
	OIDCProviderReachable *bool                              `json:"oidc_provider_reachable,omitempty"`
	Issues                []string                           `json:"issues,omitempty"`
	Warnings              []string                           `json:"warnings,omitempty"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("portal-preflight", flag.ContinueOnError)
	flags.SetOutput(stderr)
	mode := flags.String(
		"mode",
		portal.PortalPreflightModePilot,
		"preflight mode: pilot or schema",
	)
	timeout := flags.Duration("timeout", 30*time.Second, "overall preflight timeout")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	result := preflightResult{Status: "failed"}
	if *timeout <= 0 || *timeout > 2*time.Minute {
		result.Issues = append(result.Issues, "timeout must be greater than zero and no more than two minutes")
		return emitResult(stdout, stderr, result, 1)
	}

	policy, err := portal.LoadSessionPolicyFromEnv()
	if err != nil {
		result.Issues = append(result.Issues, "portal session policy is invalid")
		return emitResult(stdout, stderr, result, 1)
	}
	oidcConfig := portal.LoadOIDCConfigFromEnv()
	normalizedMode := strings.ToLower(strings.TrimSpace(*mode))
	environment, environmentIssues := portal.InspectPortalEnvironment(
		normalizedMode,
		policy,
		oidcConfig,
	)
	result.Environment = &environment
	result.Issues = append(result.Issues, environmentIssues...)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	pool, err := db.NewFromEnv(ctx)
	if err != nil {
		result.Issues = append(result.Issues, "database connection could not be initialized")
		return emitResult(stdout, stderr, result, 1)
	}
	defer pool.Close()
	store, err := portal.NewStore(pool, policy)
	if err != nil {
		result.Issues = append(result.Issues, "portal store could not be initialized")
		return emitResult(stdout, stderr, result, 1)
	}
	if err := store.InitializeSecurity(ctx); err != nil {
		result.Issues = append(result.Issues, "effective database role is not proven to enforce portal RLS")
		return emitResult(stdout, stderr, result, 1)
	}
	databaseReport, err := store.InspectDatabasePreflight(ctx)
	if err != nil {
		var integrityErr *portal.AuditIntegrityError
		if errors.As(err, &integrityErr) {
			result.Issues = append(result.Issues, integrityErr.Error())
		} else {
			result.Issues = append(result.Issues, "portal database preflight could not be completed")
		}
		return emitResult(stdout, stderr, result, 1)
	}
	result.Database = &databaseReport
	result.Issues = append(
		result.Issues,
		portal.PortalDatabasePreflightIssues(databaseReport)...,
	)
	if databaseReport.Audit != nil && databaseReport.Audit.LegacyHashEvents > 0 {
		result.Warnings = append(
			result.Warnings,
			"legacy audit events are position/link verified but cannot be independently recomputed",
		)
	}

	if normalizedMode == portal.PortalPreflightModePilot &&
		environment.OIDCEnabled &&
		environment.OIDCConfigurationValid {
		reachable := false
		client, clientErr := portal.NewOIDCClient(store, oidcConfig)
		if clientErr == nil {
			reachable = client.CheckProvider(ctx) == nil
		}
		result.OIDCProviderReachable = &reachable
		if !reachable {
			result.Issues = append(result.Issues, "portal identity provider discovery failed")
		}
	}

	if len(result.Issues) == 0 {
		result.Status = "ready"
		return emitResult(stdout, stderr, result, 0)
	}
	return emitResult(stdout, stderr, result, 1)
}

func emitResult(
	output io.Writer,
	errorOutput io.Writer,
	result preflightResult,
	exitCode int,
) int {
	sort.Strings(result.Issues)
	sort.Strings(result.Warnings)
	if err := writeResult(output, result); err != nil {
		_, _ = fmt.Fprintln(
			errorOutput,
			`{"status":"failed","issues":["preflight output could not be encoded"]}`,
		)
		return 1
	}
	return exitCode
}

func writeResult(output io.Writer, result preflightResult) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}
