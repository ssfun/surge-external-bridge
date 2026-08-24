package revision

import (
	"testing"
	"time"

	"github.com/ssfun/vless2surge/internal/domain"
)

func TestIdentityStableAndScopedBySubscription(t *testing.T) {
	config := domain.DefaultConfig()
	config.Subscriptions = []domain.Subscription{
		{ID: "one", Name: "One", URL: "https://one.example/sub", Enabled: true},
		{ID: "two", Name: "Two", URL: "https://two.example/sub", Enabled: true},
	}
	node := domain.Node{Type: "vless", Name: "HK", Server: "example.com", Port: 443, UUID: "11111111-1111-4111-8111-111111111111", Network: "tcp"}
	state := domain.DefaultRuntimeState()
	state.Snapshots["one"] = domain.Snapshot{SubscriptionID: "one", Nodes: []domain.Node{node}, FetchedAt: time.Now()}
	state.Snapshots["two"] = domain.Snapshot{SubscriptionID: "two", Nodes: []domain.Node{node}, FetchedAt: time.Now()}
	builder := NewBuilder()
	first, err := builder.Build(config, &state, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Nodes) != 2 {
		t.Fatalf("expected two nodes, got %d", len(first.Nodes))
	}
	if first.Nodes[0].DisplayName != "One · HK" || first.Nodes[1].DisplayName != "Two · HK" {
		t.Fatalf("multiple enabled subscriptions were not prefixed: %+v", first.Nodes)
	}
	if first.Nodes[0].NodeID == first.Nodes[1].NodeID || first.Nodes[0].OutboundTag == first.Nodes[1].OutboundTag || first.Nodes[0].Password == first.Nodes[1].Password {
		t.Fatalf("cross-subscription identities must be unique: %+v", first.Nodes)
	}
	second, err := builder.Build(config, &state, "test")
	if err != nil {
		t.Fatal(err)
	}
	for index := range first.Nodes {
		if first.Nodes[index].AuthUser != second.Nodes[index].AuthUser || first.Nodes[index].Password != second.Nodes[index].Password {
			t.Fatalf("identity was not stable: first=%+v second=%+v", first.Nodes[index], second.Nodes[index])
		}
	}
}

func TestSingleSubscriptionDoesNotAddRedundantPrefix(t *testing.T) {
	config := domain.DefaultConfig()
	config.Subscriptions = []domain.Subscription{{ID: "one", Name: "One", URL: "https://example.com/sub", Enabled: true}}
	state := domain.DefaultRuntimeState()
	state.Snapshots["one"] = domain.Snapshot{Nodes: []domain.Node{{
		Type: "vless", Name: "HK", Server: "example.com", Port: 443, UUID: "11111111-1111-4111-8111-111111111111",
	}}}
	revision, err := NewBuilder().Build(config, &state, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(revision.Nodes) != 1 || revision.Nodes[0].DisplayName != "HK" {
		t.Fatalf("single subscription received a redundant prefix: %+v", revision.Nodes)
	}
}

func TestDropThresholdMarksRiskyDraft(t *testing.T) {
	config := domain.DefaultConfig()
	config.DropThresholdPercent = 50
	config.Subscriptions = []domain.Subscription{{ID: "one", Name: "One", URL: "https://example.com/sub", Enabled: true}}
	state := domain.DefaultRuntimeState()
	state.Applied = &domain.Revision{Nodes: make([]domain.RuntimeNode, 10)}
	state.Snapshots["one"] = domain.Snapshot{Nodes: []domain.Node{{Type: "vless", Name: "only", Server: "example.com", Port: 443, UUID: "11111111-1111-4111-8111-111111111111"}}}
	draft, err := NewBuilder().Build(config, &state, "test")
	if err != nil {
		t.Fatal(err)
	}
	if !draft.Risky {
		t.Fatal("expected a risky draft")
	}
}

func TestDropThresholdDoesNotLosePrecisionForSmallSets(t *testing.T) {
	config := domain.DefaultConfig()
	config.DropThresholdPercent = 50
	config.Subscriptions = []domain.Subscription{{ID: "one", Name: "One", URL: "https://example.com/sub", Enabled: true}}
	state := domain.DefaultRuntimeState()
	state.Applied = &domain.Revision{Nodes: make([]domain.RuntimeNode, 3)}
	state.Snapshots["one"] = domain.Snapshot{Nodes: []domain.Node{{Type: "vless", Name: "only", Server: "example.com", Port: 443, UUID: "11111111-1111-4111-8111-111111111111"}}}
	draft, err := NewBuilder().Build(config, &state, "test")
	if err != nil {
		t.Fatal(err)
	}
	if !draft.Risky {
		t.Fatal("3 to 1 node drop (66%) was hidden by integer rounding")
	}
}

func TestIdentityRegistryConflictIsReportedAndExcluded(t *testing.T) {
	config := domain.DefaultConfig()
	config.Subscriptions = []domain.Subscription{{ID: "one", Name: "One", URL: "https://example.com/sub", Enabled: true}}
	nodes := []domain.Node{
		{Type: "vless", Name: "A", Server: "a.example.com", Port: 443, UUID: "11111111-1111-4111-8111-111111111111"},
		{Type: "vless", Name: "B", Server: "b.example.com", Port: 443, UUID: "22222222-2222-4222-8222-222222222222"},
	}
	state := domain.DefaultRuntimeState()
	state.Snapshots["one"] = domain.Snapshot{Nodes: nodes}
	builder := NewBuilder()
	first, err := builder.Build(config, &state, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Nodes) != 2 {
		t.Fatalf("expected two initial nodes: %+v", first)
	}
	var firstKey, secondKey string
	for key := range state.Registry {
		if firstKey == "" {
			firstKey = key
		} else {
			secondKey = key
		}
	}
	firstIdentity := state.Registry[firstKey]
	secondIdentity := state.Registry[secondKey]
	secondIdentity.AuthUser = firstIdentity.AuthUser
	state.Registry[secondKey] = secondIdentity

	revision, err := builder.Build(config, &state, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(revision.Nodes) != 1 || len(revision.Dropped) != 1 || revision.Dropped[0].Reason != "节点身份冲突" {
		t.Fatalf("identity conflict was not reported: %+v", revision)
	}
}
