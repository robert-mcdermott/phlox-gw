package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/robert-mcdermott/phlox-gw/internal/config"
	"github.com/robert-mcdermott/phlox-gw/internal/store"
)

func TestClusterStatusAndReadyEndpoints(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if err := st.EnsureSeedData("admin-hash"); err != nil {
		t.Fatalf("EnsureSeedData: %v", err)
	}
	admin, err := st.GetUserByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	handler, err := New(Options{
		Config: config.Config{
			Addr:          "127.0.0.1:9090",
			SessionSecret: "test-secret",
			Database:      config.DatabaseConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "test.db")},
			Deployment: config.DeploymentConfig{
				Mode:              "single-sqlite",
				InstanceID:        "node-test",
				HeartbeatInterval: time.Second,
				NodeStaleAfter:    3 * time.Second,
			},
		},
		Store: st,
		Frontend: fstest.MapFS{
			"frontend/dist/index.html": &fstest.MapFile{Data: []byte("<html></html>")},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ready := httptest.NewRecorder()
	handler.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if ready.Code != http.StatusOK {
		t.Fatalf("ready status = %d body = %s", ready.Code, ready.Body.String())
	}
	statusResp := jsonRequest(t, handler, http.MethodGet, "/api/admin/cluster/status", sessionToken(t, admin), nil)
	if statusResp.Code != http.StatusOK {
		t.Fatalf("cluster status = %d body = %s", statusResp.Code, statusResp.Body.String())
	}
	var status clusterStatusResponse
	decodeRecorder(t, statusResp, &status)
	if status.InstanceID != "node-test" || status.DeploymentMode != "single-sqlite" || status.Status != "disabled" {
		t.Fatalf("unexpected cluster status: %#v", status)
	}
	if len(status.Nodes) != 1 || !status.Nodes[0].Current {
		t.Fatalf("unexpected cluster nodes: %#v", status.Nodes)
	}
}
