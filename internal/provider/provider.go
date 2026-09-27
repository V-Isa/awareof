package provider

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"

	"github.com/V-Isa/awareof/internal/scope"
	"github.com/V-Isa/awareof/internal/vocabulary"
)

// Repository is the inspected repository or project directory.
type Repository struct {
	Root string
}

// EvaluationContext carries core-owned context that can affect effective
// provider semantics. It is intentionally empty until a provider needs a
// concrete, typed field.
type EvaluationContext struct{}

// Instance identifies one independently evaluated provider scope. Concrete
// provider packages may retain private, typed evaluation facts on the value.
type Instance interface {
	Descriptor() InstanceDescriptor
}

// InstanceDescriptor is the core-owned identity of a provider instance.
type InstanceDescriptor struct {
	Provider scope.ProviderID
	ID       scope.InstanceID
	Label    string
}

// Descriptor lets a plain descriptor serve as an instance when a provider
// does not need to carry additional facts between discovery and evaluation.
func (d InstanceDescriptor) Descriptor() InstanceDescriptor {
	return d
}

// Provider supplies provider-specific effective-scope facts. Paths are
// normalized, deduplicated, and ordered by core before Evaluate is called.
type Provider interface {
	ID() scope.ProviderID
	Detect(context.Context, Repository) ([]Instance, error)
	Evaluate(context.Context, EvaluationContext, Repository, Instance, []scope.Path) ([]scope.Result, error)
}

// IDs validates provider IDs and returns them in deterministic order.
func IDs(providers []Provider) ([]scope.ProviderID, error) {
	ordered, err := orderedProviders(providers)
	if err != nil {
		return nil, err
	}
	ids := make([]scope.ProviderID, len(ordered))
	for index, current := range ordered {
		ids[index] = current.ID()
	}
	return ids, nil
}

// Select returns the requested providers in deterministic provider order.
func Select(providers []Provider, requested []scope.ProviderID) ([]Provider, error) {
	ordered, err := orderedProviders(providers)
	if err != nil {
		return nil, err
	}
	wanted := make(map[scope.ProviderID]struct{}, len(requested))
	for _, id := range requested {
		wanted[id] = struct{}{}
	}
	selected := make([]Provider, 0, len(wanted))
	for _, current := range ordered {
		if _, ok := wanted[current.ID()]; ok {
			selected = append(selected, current)
			delete(wanted, current.ID())
		}
	}
	if len(wanted) != 0 {
		missing := make([]scope.ProviderID, 0, len(wanted))
		for id := range wanted {
			missing = append(missing, id)
		}
		sort.Slice(missing, func(i, j int) bool { return missing[i] < missing[j] })
		return nil, fmt.Errorf("requested provider %q is not registered", missing[0])
	}
	return selected, nil
}

// Evaluate discovers provider instances and evaluates every path against each
// instance. It enforces the provider contract before returning any facts.
func Evaluate(
	ctx context.Context,
	repo Repository,
	paths []scope.Path,
	providers []Provider,
) ([]scope.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("evaluate providers: %w", err)
	}

	ordered, err := orderedProviders(providers)
	if err != nil {
		return nil, err
	}

	var results []scope.Result
	for _, current := range ordered {
		instances, err := current.Detect(ctx, repo)
		if err != nil {
			return nil, fmt.Errorf("detect provider %q: %w", current.ID(), err)
		}
		detectedInstances, err := orderedInstances(current.ID(), instances)
		if err != nil {
			return nil, err
		}

		for _, detected := range detectedInstances {
			facts, err := current.Evaluate(ctx, EvaluationContext{}, repo, detected.instance, paths)
			if err != nil {
				return nil, fmt.Errorf("evaluate provider %q instance %q: %w", current.ID(), detected.descriptor.ID, err)
			}
			if err := validateResults(current.ID(), detected.descriptor.ID, paths, facts); err != nil {
				return nil, err
			}
			results = append(results, facts...)
		}
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].Path != results[j].Path {
			return results[i].Path < results[j].Path
		}
		if results[i].Provider != results[j].Provider {
			return results[i].Provider < results[j].Provider
		}
		return results[i].Instance < results[j].Instance
	})
	return results, nil
}

func orderedProviders(providers []Provider) ([]Provider, error) {
	ordered := append([]Provider(nil), providers...)
	seen := make(map[scope.ProviderID]struct{}, len(ordered))
	for _, current := range ordered {
		if current == nil {
			return nil, errors.New("provider is nil")
		}
		id := current.ID()
		if id == "" {
			return nil, errors.New("provider ID is empty")
		}
		if !vocabulary.ValidIdentifier(string(id)) {
			return nil, fmt.Errorf("provider ID %q is invalid", id)
		}
		if _, ok := seen[id]; ok {
			return nil, fmt.Errorf("provider ID %q is duplicated", id)
		}
		seen[id] = struct{}{}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID() < ordered[j].ID() })
	return ordered, nil
}

type describedInstance struct {
	instance   Instance
	descriptor InstanceDescriptor
}

func orderedInstances(providerID scope.ProviderID, instances []Instance) ([]describedInstance, error) {
	ordered := make([]describedInstance, 0, len(instances))
	seen := make(map[scope.InstanceID]struct{}, len(instances))
	for _, instance := range instances {
		descriptor, err := describeInstance(instance)
		if err != nil {
			return nil, fmt.Errorf("provider %q returned invalid instance: %w", providerID, err)
		}
		if descriptor.Provider != providerID {
			return nil, fmt.Errorf("provider %q returned instance %q for provider %q", providerID, descriptor.ID, descriptor.Provider)
		}
		if descriptor.ID == "" {
			return nil, fmt.Errorf("provider %q returned an empty instance ID", providerID)
		}
		if _, ok := seen[descriptor.ID]; ok {
			return nil, fmt.Errorf("provider %q returned duplicate instance ID %q", providerID, descriptor.ID)
		}
		seen[descriptor.ID] = struct{}{}
		ordered = append(ordered, describedInstance{instance: instance, descriptor: descriptor})
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].descriptor.ID < ordered[j].descriptor.ID })
	return ordered, nil
}

func describeInstance(instance Instance) (InstanceDescriptor, error) {
	if instance == nil {
		return InstanceDescriptor{}, errors.New("instance is nil")
	}
	value := reflect.ValueOf(instance)
	if value.Kind() == reflect.Pointer && value.IsNil() {
		return InstanceDescriptor{}, errors.New("instance is nil")
	}
	return instance.Descriptor(), nil
}

func validateResults(
	providerID scope.ProviderID,
	instanceID scope.InstanceID,
	paths []scope.Path,
	results []scope.Result,
) error {
	if len(results) != len(paths) {
		return fmt.Errorf(
			"provider %q instance %q returned %d results for %d paths",
			providerID,
			instanceID,
			len(results),
			len(paths),
		)
	}
	for i, result := range results {
		switch {
		case result.Path != paths[i]:
			return fmt.Errorf("provider %q instance %q returned path %q at index %d, want %q", providerID, instanceID, result.Path, i, paths[i])
		case result.Provider != providerID:
			return fmt.Errorf("provider %q instance %q returned provider %q at index %d", providerID, instanceID, result.Provider, i)
		case result.Instance != instanceID:
			return fmt.Errorf("provider %q instance %q returned instance %q at index %d", providerID, instanceID, result.Instance, i)
		case !result.State.Valid():
			return fmt.Errorf("provider %q instance %q returned invalid state %q for path %q", providerID, instanceID, result.State, result.Path)
		case result.Explanation.Code == "":
			return fmt.Errorf("provider %q instance %q returned an empty explanation code for path %q", providerID, instanceID, result.Path)
		case result.Explanation.Summary == "":
			return fmt.Errorf("provider %q instance %q returned an empty explanation for path %q", providerID, instanceID, result.Path)
		case !result.Explanation.Valid():
			return fmt.Errorf("provider %q instance %q returned an invalid explanation for path %q", providerID, instanceID, result.Path)
		case !result.Provenance.Valid(result.State):
			return fmt.Errorf("provider %q instance %q returned invalid provenance %q for path %q", providerID, instanceID, result.Provenance.Method, result.Path)
		}
	}
	return nil
}
