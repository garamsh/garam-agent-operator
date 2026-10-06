package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/yaml"

	"github.com/garamsh/garam-agent-operator/internal/definition"
	"github.com/garamsh/garam-agent-operator/internal/definition/repository"
)

// publishProfileCommand is the subcommand a deployment publishes its profile versions with
// (ADR 0056).
const publishProfileCommand = "publish-profile"

// Exit codes of publish-profile: every file published or unchanged, a file refused or the store
// unreachable, and a command line or a file that is not one.
const (
	exitPublished = 0
	exitRefused   = 1
	exitUsage     = 2
)

// profileFile is one profile version as a deployment declares it, in the read route's names.
type profileFile struct {
	Organization         string                      `json:"organization"`
	Name                 string                      `json:"name"`
	Version              int64                       `json:"version"`
	Resources            corev1.ResourceRequirements `json:"resources"`
	StorageSize          resource.Quantity           `json:"storageSize"`
	StorageClassName     *string                     `json:"storageClassName,omitempty"`
	WorkspaceStorageSize *resource.Quantity          `json:"workspaceStorageSize,omitempty"`
}

// publishProfiles publishes each --file in order, through the definition domain's service, into
// the store databaseURL names, and stops at the first that is refused. It says on out whether each
// file was published, unchanged or refused, and on errOut why, and answers the exit code.
func publishProfiles(ctx context.Context, args []string, databaseURL string, out, errOut io.Writer) int {
	flags := flag.NewFlagSet(publishProfileCommand, flag.ContinueOnError)
	flags.SetOutput(errOut)
	var files []string
	flags.Func("file", "A profile version to publish, as YAML. Repeat for each; they are published in order.",
		func(path string) error {
			files = append(files, path)
			return nil
		})
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	if len(files) == 0 || flags.NArg() > 0 {
		_, _ = fmt.Fprintln(errOut, "usage: control publish-profile --file <profile.yaml> [--file ...]")
		return exitUsage
	}
	profiles := make([]profileFile, 0, len(files))
	for _, path := range files {
		p, err := readProfileFile(path)
		if err != nil {
			_, _ = fmt.Fprintf(errOut, "%s: %v\n", path, err)
			return exitUsage
		}
		profiles = append(profiles, p)
	}
	if databaseURL == "" {
		_, _ = fmt.Fprintf(errOut, "%s is not set\n", databaseURLVariable)
		return exitUsage
	}

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "open store: %v\n", err)
		return exitRefused
	}
	defer pool.Close()
	// The schema is the server's to apply, never this command's.
	definitions := definition.NewService(repository.NewPostgres(pool), nil, nil)

	for i, p := range profiles {
		published, created, err := definitions.PublishProfileVersion(ctx, p.Organization, definition.Profile{
			Name:    p.Name,
			Version: definition.Version(p.Version),
			Settings: definition.ExecutionSettings{
				Resources: p.Resources, StorageSize: p.StorageSize,
				StorageClassName: p.StorageClassName, WorkspaceStorageSize: p.WorkspaceStorageSize,
			},
		})
		if err != nil {
			_, _ = fmt.Fprintf(out, "refused %s/%s version %d\n", p.Organization, p.Name, p.Version)
			_, _ = fmt.Fprintf(errOut, "%s: %v\n", files[i], err)
			return exitRefused
		}
		outcome := "unchanged"
		if created {
			outcome = "published"
		}
		_, _ = fmt.Fprintf(out, "%s %s/%s version %d\n", outcome, p.Organization, published.Name, published.Version)
	}
	return exitPublished
}

// readProfileFile reads one profile version, refusing a field the file format does not name.
func readProfileFile(path string) (profileFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return profileFile{}, err
	}
	var p profileFile
	if err := yaml.UnmarshalStrict(raw, &p); err != nil {
		return profileFile{}, fmt.Errorf("not a profile file: %w", err)
	}
	if p.Organization == "" || p.Name == "" {
		return profileFile{}, errors.New("not a profile file: organization and name are required")
	}
	return p, nil
}
