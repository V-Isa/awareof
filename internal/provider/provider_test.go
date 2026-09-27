package provider

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/V-Isa/awareof/internal/scope"
)

func TestEvaluateOrdersProvidersInstancesAndResults(t *testing.T) {
	t.Parallel()

	paths := []scope.Path{"b", "a"}
	providers := []Provider{
		&fakeProvider{id: "z", instances: []Instance{
			InstanceDescriptor{Provider: "z", ID: "two"},
			InstanceDescriptor{Provider: "z", ID: "one"},
		}},
		&fakeProvider{id: "a", instances: []Instance{InstanceDescriptor{Provider: "a", ID: "a"}}},
	}

	got, err := Evaluate(context.Background(), Repository{Root: "/repo"}, paths, providers)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, result := range got {
		order = append(order, string(result.Path)+":"+string(result.Provider)+":"+string(result.Instance))
	}
	want := []string{"a:a:a", "a:z:one", "a:z:two", "b:a:a", "b:z:one", "b:z:two"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("Evaluate() order = %v, want %v", order, want)
	}
}

func TestEvaluateRejectsInvalidProviderContracts(t *testing.T) {
	t.Parallel()

	valid := func(id scope.ProviderID, instance scope.InstanceID, paths []scope.Path) []scope.Result {
		results := make([]scope.Result, 0, len(paths))
		for _, path := range paths {
			results = append(results, scope.Result{
				Path: path, Provider: id, Instance: instance, State: scope.In,
				Explanation: scope.Explanation{Code: "test/in", Summary: "included"},
				Provenance:  scope.Provenance{Method: scope.SafeParser},
			})
		}
		return results
	}
	paths := []scope.Path{"a"}
	base := func() *fakeProvider {
		return &fakeProvider{id: "test", instances: []Instance{InstanceDescriptor{Provider: "test", ID: "test"}}}
	}

	tests := []struct {
		name      string
		providers func() []Provider
		want      string
	}{
		{name: "nil provider", providers: func() []Provider { return []Provider{nil} }, want: "provider is nil"},
		{name: "empty provider id", providers: func() []Provider { return []Provider{&fakeProvider{}} }, want: "provider ID is empty"},
		{name: "invalid provider id", providers: func() []Provider { return []Provider{&fakeProvider{id: "Git/provider"}} }, want: "provider ID"},
		{name: "duplicate provider id", providers: func() []Provider { return []Provider{base(), base()} }, want: "duplicated"},
		{name: "detect error", providers: func() []Provider { p := base(); p.detectErr = errors.New("detect failed"); return []Provider{p} }, want: "detect provider"},
		{name: "nil instance", providers: func() []Provider { p := base(); p.instances[0] = nil; return []Provider{p} }, want: "instance is nil"},
		{name: "typed nil instance", providers: func() []Provider { p := base(); p.instances[0] = (*configuredInstance)(nil); return []Provider{p} }, want: "instance is nil"},
		{name: "wrong instance provider", providers: func() []Provider {
			p := base()
			p.instances[0] = InstanceDescriptor{Provider: "other", ID: "test"}
			return []Provider{p}
		}, want: "for provider"},
		{name: "empty instance id", providers: func() []Provider {
			p := base()
			p.instances[0] = InstanceDescriptor{Provider: "test"}
			return []Provider{p}
		}, want: "empty instance ID"},
		{name: "duplicate instance id", providers: func() []Provider {
			p := base()
			p.instances = append(p.instances, p.instances[0])
			return []Provider{p}
		}, want: "duplicate instance ID"},
		{name: "evaluate error", providers: func() []Provider { p := base(); p.evaluateErr = errors.New("evaluate failed"); return []Provider{p} }, want: "evaluate provider"},
		{name: "wrong result count", providers: func() []Provider {
			p := base()
			p.results = []scope.Result{}
			p.useResults = true
			return []Provider{p}
		}, want: "returned 0 results"},
		{name: "wrong path", providers: func() []Provider {
			p := base()
			p.results = valid("test", "test", []scope.Path{"wrong"})
			p.useResults = true
			return []Provider{p}
		}, want: "returned path"},
		{name: "wrong result provider", providers: func() []Provider {
			p := base()
			p.results = valid("other", "test", paths)
			p.useResults = true
			return []Provider{p}
		}, want: "returned provider"},
		{name: "wrong result instance", providers: func() []Provider {
			p := base()
			p.results = valid("test", "other", paths)
			p.useResults = true
			return []Provider{p}
		}, want: "returned instance"},
		{name: "invalid state", providers: func() []Provider {
			p := base()
			p.results = valid("test", "test", paths)
			p.results[0].State = "MIXED"
			p.useResults = true
			return []Provider{p}
		}, want: "invalid state"},
		{name: "empty explanation code", providers: func() []Provider {
			p := base()
			p.results = valid("test", "test", paths)
			p.results[0].Explanation.Code = ""
			p.useResults = true
			return []Provider{p}
		}, want: "empty explanation code"},
		{name: "empty explanation", providers: func() []Provider {
			p := base()
			p.results = valid("test", "test", paths)
			p.results[0].Explanation.Summary = ""
			p.useResults = true
			return []Provider{p}
		}, want: "empty explanation"},
		{name: "invalid explanation code", providers: func() []Provider {
			p := base()
			p.results = valid("test", "test", paths)
			p.results[0].Explanation.Code = "Test/in"
			p.useResults = true
			return []Provider{p}
		}, want: "invalid explanation"},
		{name: "invalid provenance", providers: func() []Provider {
			p := base()
			p.results = valid("test", "test", paths)
			p.results[0].Provenance.Method = ""
			p.useResults = true
			return []Provider{p}
		}, want: "invalid provenance"},
		{name: "unavailable provenance with known state", providers: func() []Provider {
			p := base()
			p.results = valid("test", "test", paths)
			p.results[0].Provenance = scope.Provenance{Method: scope.Unavailable, Tool: "test"}
			p.useResults = true
			return []Provider{p}
		}, want: "invalid provenance"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := Evaluate(context.Background(), Repository{}, paths, test.providers())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Evaluate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestEvaluatePreservesProviderInstance(t *testing.T) {
	t.Parallel()
	current := &fakeProvider{
		id: "test",
		instances: []Instance{configuredInstance{
			descriptor: InstanceDescriptor{Provider: "test", ID: "configured"},
			config:     "provider-private",
		}},
		wantConfig: "provider-private",
	}
	if _, err := Evaluate(context.Background(), Repository{}, []scope.Path{"a"}, []Provider{current}); err != nil {
		t.Fatal(err)
	}
}

func TestEvaluateHonorsCanceledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Evaluate(ctx, Repository{}, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Evaluate() error = %v, want context.Canceled", err)
	}
}

func TestIDs(t *testing.T) {
	t.Parallel()
	got, err := IDs([]Provider{&fakeProvider{id: "z"}, &fakeProvider{id: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []scope.ProviderID{"a", "z"}) {
		t.Fatalf("IDs() = %v", got)
	}
	if _, err := IDs([]Provider{nil}); err == nil {
		t.Fatal("IDs() error = nil, want invalid provider error")
	}
}

func TestSelect(t *testing.T) {
	t.Parallel()
	providers := []Provider{&fakeProvider{id: "z"}, &fakeProvider{id: "a"}, &fakeProvider{id: "m"}}
	got, err := Select(providers, []scope.ProviderID{"z", "a", "z"})
	if err != nil {
		t.Fatal(err)
	}
	if ids, err := IDs(got); err != nil || !reflect.DeepEqual(ids, []scope.ProviderID{"a", "z"}) {
		t.Fatalf("Select() IDs = %v, %v", ids, err)
	}
	if _, err := Select(providers, []scope.ProviderID{"missing"}); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("Select() error = %v, want missing provider", err)
	}
	if _, err := Select([]Provider{nil}, nil); err == nil {
		t.Fatal("Select() error = nil, want invalid provider registry")
	}
}

type fakeProvider struct {
	id          scope.ProviderID
	instances   []Instance
	detectErr   error
	evaluateErr error
	results     []scope.Result
	useResults  bool
	wantConfig  string
}

func (p *fakeProvider) ID() scope.ProviderID { return p.id }

func (p *fakeProvider) Detect(context.Context, Repository) ([]Instance, error) {
	return append([]Instance(nil), p.instances...), p.detectErr
}

func (p *fakeProvider) Evaluate(_ context.Context, _ EvaluationContext, _ Repository, instance Instance, paths []scope.Path) ([]scope.Result, error) {
	if p.evaluateErr != nil {
		return nil, p.evaluateErr
	}
	if p.useResults {
		return append([]scope.Result(nil), p.results...), nil
	}
	if p.wantConfig != "" {
		configured, ok := instance.(configuredInstance)
		if !ok || configured.config != p.wantConfig {
			return nil, errors.New("provider-private instance facts were not preserved")
		}
	}
	descriptor := instance.Descriptor()
	results := make([]scope.Result, 0, len(paths))
	for _, path := range paths {
		results = append(results, scope.Result{
			Path: path, Provider: p.id, Instance: descriptor.ID, State: scope.In,
			Explanation: scope.Explanation{Code: "test/in", Summary: "included"},
			Provenance:  scope.Provenance{Method: scope.SafeParser},
		})
	}
	return results, nil
}

type configuredInstance struct {
	descriptor InstanceDescriptor
	config     string
}

func (i configuredInstance) Descriptor() InstanceDescriptor {
	return i.descriptor
}
