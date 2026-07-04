package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestClusterNodeUpsertAndList(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	started := time.Now().UTC().Add(-time.Minute)
	if err := s.UpsertClusterNode(ctx, ClusterNode{
		InstanceID:     "node-1",
		Hostname:       "host-a",
		Version:        "test",
		Addr:           "127.0.0.1:8081",
		DeploymentMode: "cluster-postgres",
		DBDriver:       "postgres",
		Status:         "starting",
		StartedAt:      started,
		LastSeenAt:     started,
		Metadata:       `{"role":"test"}`,
	}); err != nil {
		t.Fatalf("UpsertClusterNode insert: %v", err)
	}
	seen := started.Add(time.Minute)
	if err := s.UpsertClusterNode(ctx, ClusterNode{
		InstanceID:     "node-1",
		Hostname:       "host-a",
		Version:        "test",
		Addr:           "127.0.0.1:8082",
		DeploymentMode: "cluster-postgres",
		DBDriver:       "postgres",
		Status:         "ready",
		StartedAt:      started,
		LastSeenAt:     seen,
		Metadata:       `{"role":"test"}`,
	}); err != nil {
		t.Fatalf("UpsertClusterNode update: %v", err)
	}
	nodes, err := s.ListClusterNodes(ctx)
	if err != nil {
		t.Fatalf("ListClusterNodes: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("node count = %d, want 1", len(nodes))
	}
	if nodes[0].Status != "ready" || nodes[0].Addr != "127.0.0.1:8082" || !nodes[0].LastSeenAt.Equal(seen) {
		t.Fatalf("unexpected node: %#v", nodes[0])
	}
	if err := s.DeleteClusterNode(ctx, "node-1"); err != nil {
		t.Fatalf("DeleteClusterNode: %v", err)
	}
	if err := s.DeleteClusterNode(ctx, "node-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteClusterNode missing = %v, want ErrNotFound", err)
	}
	nodes, err = s.ListClusterNodes(ctx)
	if err != nil {
		t.Fatalf("ListClusterNodes after delete: %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("node count after delete = %d, want 0", len(nodes))
	}
}

func TestPruneClusterNodes(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	for _, node := range []ClusterNode{
		{InstanceID: "node-old", Status: "ready", StartedAt: now.Add(-2 * time.Hour), LastSeenAt: now.Add(-time.Hour)},
		{InstanceID: "node-fresh", Status: "ready", StartedAt: now.Add(-time.Minute), LastSeenAt: now},
	} {
		if err := s.UpsertClusterNode(ctx, node); err != nil {
			t.Fatalf("UpsertClusterNode %s: %v", node.InstanceID, err)
		}
	}
	pruned, err := s.PruneClusterNodes(ctx, now.Add(-10*time.Minute))
	if err != nil {
		t.Fatalf("PruneClusterNodes: %v", err)
	}
	if pruned != 1 {
		t.Fatalf("pruned = %d, want 1", pruned)
	}
	nodes, err := s.ListClusterNodes(ctx)
	if err != nil {
		t.Fatalf("ListClusterNodes: %v", err)
	}
	if len(nodes) != 1 || nodes[0].InstanceID != "node-fresh" {
		t.Fatalf("unexpected surviving nodes: %#v", nodes)
	}
}
