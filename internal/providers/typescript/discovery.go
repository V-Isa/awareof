package typescript

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"

	"github.com/V-Isa/awareof/internal/provider"
	"github.com/V-Isa/awareof/internal/scope"
)

const maxScanEntries = 100_000

var errScanLimit = errors.New("TypeScript project discovery limit reached")

type typescriptInstance struct {
	descriptor provider.InstanceDescriptor
	configs    []scope.Path
	discovery  *scope.Explanation
}

func (i typescriptInstance) Descriptor() provider.InstanceDescriptor {
	return i.descriptor
}

func (p *Provider) Detect(ctx context.Context, repo provider.Repository) (_ []provider.Instance, returnErr error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("detect TypeScript projects: %w", err)
	}
	if repo.Root == "" {
		return nil, errors.New("repository root is empty")
	}
	root, err := os.OpenRoot(repo.Root)
	if err != nil {
		return nil, fmt.Errorf("open repository root: %w", err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close repository root: %w", err))
		}
	}()

	limit := p.scanLimit
	if limit <= 0 {
		limit = maxScanEntries
	}
	configs, discoveryIssues, err := discoverConfigs(ctx, root.FS(), limit)
	var discovery *scope.Explanation
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("detect TypeScript projects: %w", ctxErr)
		}
		code := "typescript/discovery-unavailable"
		summary := "TypeScript project discovery could not be completed safely"
		if errors.Is(err, errScanLimit) {
			code = "typescript/discovery-limit"
			summary = fmt.Sprintf("TypeScript project discovery exceeds %d repository entries", limit)
		}
		discovery = &scope.Explanation{Code: code, Summary: summary, Evidence: err.Error()}
	} else if len(discoveryIssues) != 0 {
		discovery = explainDiscoveryIssues(discoveryIssues)
	}
	if len(configs) == 0 && discovery == nil {
		return nil, nil
	}
	slices.Sort(configs)
	return []provider.Instance{typescriptInstance{
		descriptor: provider.InstanceDescriptor{
			Provider: providerID,
			ID:       rootInstanceID,
			Label:    "TypeScript Programs",
		},
		configs:   configs,
		discovery: discovery,
	}}, nil
}

func discoverConfigs(ctx context.Context, filesystem fs.FS, limit int) ([]scope.Path, []discoveryIssue, error) {
	configs := []scope.Path{}
	entries := 0
	discoveryIssues := []discoveryIssue{}
	err := fs.WalkDir(filesystem, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			discoveryIssues = append(discoveryIssues, discoveryIssue{
				code:     "typescript/discovery-unavailable",
				summary:  "TypeScript project discovery could not read a repository entry",
				evidence: name + ": " + walkErr.Error(),
			})
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		entries++
		if entries > limit {
			return errScanLimit
		}
		if entry.IsDir() && name != "." && skippedDirectory(entry.Name()) {
			return fs.SkipDir
		}
		if entry.Name() != "tsconfig.json" {
			return nil
		}
		if entry.Type().IsRegular() {
			configs = append(configs, scope.Path(path.Clean(name)))
			return nil
		}
		discoveryIssues = append(discoveryIssues, discoveryIssue{
			code:     "typescript/config-uninspectable",
			summary:  "a discovered tsconfig.json is not a regular file",
			evidence: name,
		})
		return nil
	})
	return configs, discoveryIssues, err
}

func skippedDirectory(name string) bool {
	switch name {
	case ".git", ".hg", ".svn", "node_modules":
		return true
	default:
		return false
	}
}

type discoveryIssue struct {
	code     string
	summary  string
	evidence string
}

func explainDiscoveryIssues(issues []discoveryIssue) *scope.Explanation {
	first := issues[0]
	common := true
	for _, issue := range issues {
		if issue.code != first.code || issue.summary != first.summary {
			common = false
		}
	}
	details := make([]string, 0, len(issues))
	for _, issue := range issues {
		detail := issue.evidence
		if !common && issue.summary != "" {
			detail += " (" + issue.summary + ")"
		}
		details = append(details, detail)
	}
	if common {
		return &scope.Explanation{Code: first.code, Summary: first.summary, Evidence: summarizeDetails(details)}
	}
	return &scope.Explanation{
		Code:     "typescript/discovery-unavailable",
		Summary:  "TypeScript project discovery could not be completed safely",
		Evidence: summarizeDetails(details),
	}
}
