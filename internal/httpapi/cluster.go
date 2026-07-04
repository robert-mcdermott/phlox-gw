package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	urlpkg "net/url"
	"os"
	"strings"
	"time"

	"github.com/robert-mcdermott/phlox-gw/internal/store"
)

type clusterStatusResponse struct {
	DeploymentMode           string                `json:"deployment_mode"`
	ClusterEnabled           bool                  `json:"cluster_enabled"`
	DatabaseDriver           string                `json:"database_driver"`
	DatabaseTarget           string                `json:"database_target"`
	InstanceID               string                `json:"instance_id"`
	Hostname                 string                `json:"hostname"`
	Addr                     string                `json:"addr"`
	Version                  string                `json:"version"`
	StartedAt                time.Time             `json:"started_at"`
	LastHeartbeatAt          *time.Time            `json:"last_heartbeat_at,omitempty"`
	LastHeartbeatError       string                `json:"last_heartbeat_error"`
	HeartbeatIntervalSeconds int64                 `json:"heartbeat_interval_seconds"`
	NodeStaleAfterSeconds    int64                 `json:"node_stale_after_seconds"`
	Status                   string                `json:"status"`
	ActiveNodeCount          int                   `json:"active_node_count"`
	StaleNodeCount           int                   `json:"stale_node_count"`
	TotalNodeCount           int                   `json:"total_node_count"`
	SigningKeyShared         bool                  `json:"signing_key_shared"`
	SigningKeyPath           string                `json:"signing_key_path"`
	Notes                    []string              `json:"notes"`
	Nodes                    []clusterNodeResponse `json:"nodes"`
}

type clusterNodeResponse struct {
	InstanceID     string    `json:"instance_id"`
	Hostname       string    `json:"hostname"`
	Version        string    `json:"version"`
	Addr           string    `json:"addr"`
	DeploymentMode string    `json:"deployment_mode"`
	DBDriver       string    `json:"db_driver"`
	Status         string    `json:"status"`
	StartedAt      time.Time `json:"started_at"`
	LastSeenAt     time.Time `json:"last_seen_at"`
	AgeSeconds     int64     `json:"age_seconds"`
	Stale          bool      `json:"stale"`
	Current        bool      `json:"current"`
	Metadata       string    `json:"metadata"`
}

func (s *Server) clusterStatus(w http.ResponseWriter, r *http.Request, _ store.User) {
	status, err := s.buildClusterStatus(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, status)
}

func (s *Server) clusterNodes(w http.ResponseWriter, r *http.Request, _ store.User) {
	status, err := s.buildClusterStatus(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, status.Nodes)
}

func (s *Server) buildClusterStatus(ctx context.Context) (clusterStatusResponse, error) {
	nodes, err := s.store.ListClusterNodes(ctx)
	if err != nil {
		return clusterStatusResponse{}, err
	}
	now := time.Now().UTC()
	out := clusterStatusResponse{
		DeploymentMode:           s.cfg.Deployment.Mode,
		ClusterEnabled:           s.cfg.Deployment.Mode == "cluster-postgres",
		DatabaseDriver:           s.cfg.Database.Driver,
		DatabaseTarget:           s.databaseTarget(),
		InstanceID:               s.cfg.Deployment.InstanceID,
		Hostname:                 s.hostname,
		Addr:                     s.cfg.Addr,
		Version:                  valueOr(s.cfg.Telemetry.ServiceVersion, "dev"),
		StartedAt:                s.startedAt,
		HeartbeatIntervalSeconds: int64(s.cfg.Deployment.HeartbeatInterval.Seconds()),
		NodeStaleAfterSeconds:    int64(s.cfg.Deployment.NodeStaleAfter.Seconds()),
		Status:                   "disabled",
		SigningKeyShared:         strings.TrimSpace(s.cfg.ConfigSigningKeyFile) != "",
		SigningKeyPath:           s.adminConfigSigningKeyPath(),
	}
	last, lastErr := s.clusterHeartbeatState()
	if !last.IsZero() {
		out.LastHeartbeatAt = &last
	}
	out.LastHeartbeatError = lastErr
	if !out.ClusterEnabled {
		out.Notes = append(out.Notes, "Cluster mode is disabled; set PHLOX_GW_DEPLOYMENT_MODE=cluster-postgres with a Postgres database to enable multi-node operation.")
	} else if !out.SigningKeyShared {
		out.Notes = append(out.Notes, "Set PHLOX_GW_CONFIG_SIGNING_KEY_FILE to the same mounted file on every node so signed configuration exports use one shared key.")
	}
	for _, node := range nodes {
		stale := now.Sub(node.LastSeenAt) > s.cfg.Deployment.NodeStaleAfter
		item := clusterNodeResponse{
			InstanceID:     node.InstanceID,
			Hostname:       node.Hostname,
			Version:        valueOr(node.Version, "dev"),
			Addr:           node.Addr,
			DeploymentMode: node.DeploymentMode,
			DBDriver:       node.DBDriver,
			Status:         node.Status,
			StartedAt:      node.StartedAt,
			LastSeenAt:     node.LastSeenAt,
			AgeSeconds:     int64(now.Sub(node.LastSeenAt).Seconds()),
			Stale:          stale,
			Current:        node.InstanceID == s.cfg.Deployment.InstanceID,
			Metadata:       node.Metadata,
		}
		if item.AgeSeconds < 0 {
			item.AgeSeconds = 0
		}
		if stale {
			out.StaleNodeCount++
		} else {
			out.ActiveNodeCount++
		}
		out.Nodes = append(out.Nodes, item)
	}
	out.TotalNodeCount = len(out.Nodes)
	switch {
	case !out.ClusterEnabled:
		out.Status = "disabled"
	case lastErr != "":
		out.Status = "unavailable"
	case out.ActiveNodeCount == 0:
		out.Status = "unavailable"
	case out.StaleNodeCount > 0:
		out.Status = "degraded"
	default:
		out.Status = "healthy"
	}
	return out, nil
}

func (s *Server) clusterHeartbeatLoop() {
	ticker := time.NewTicker(s.cfg.Deployment.HeartbeatInterval)
	defer ticker.Stop()
	for range ticker.C {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := s.updateClusterHeartbeat(ctx, "ready")
		cancel()
		if err != nil {
			s.logger.Warn("cluster heartbeat failed", "instance_id", s.cfg.Deployment.InstanceID, "error", err)
		}
	}
}

func (s *Server) updateClusterHeartbeat(ctx context.Context, status string) error {
	now := time.Now().UTC()
	metadata, _ := json.Marshal(map[string]any{
		"pid":                       os.Getpid(),
		"config_signing_key_shared": strings.TrimSpace(s.cfg.ConfigSigningKeyFile) != "",
	})
	node := store.ClusterNode{
		InstanceID:     s.cfg.Deployment.InstanceID,
		Hostname:       s.hostname,
		Version:        valueOr(s.cfg.Telemetry.ServiceVersion, "dev"),
		Addr:           s.cfg.Addr,
		DeploymentMode: s.cfg.Deployment.Mode,
		DBDriver:       s.cfg.Database.Driver,
		Status:         status,
		StartedAt:      s.startedAt,
		LastSeenAt:     now,
		Metadata:       string(metadata),
	}
	err := s.store.UpsertClusterNode(ctx, node)
	if err == nil {
		if _, pruneErr := s.store.PruneClusterNodes(ctx, now.Add(-s.clusterNodeRetention())); pruneErr != nil {
			s.logger.Warn("prune expired cluster nodes failed", "error", pruneErr)
		}
	}
	s.clusterMu.Lock()
	defer s.clusterMu.Unlock()
	if err != nil {
		s.lastHeartbeatErr = err.Error()
		return err
	}
	s.lastHeartbeatAt = now
	s.lastHeartbeatErr = ""
	return nil
}

func (s *Server) clusterNodeRetention() time.Duration {
	retention := 10 * s.cfg.Deployment.NodeStaleAfter
	if retention < 10*time.Minute {
		retention = 10 * time.Minute
	}
	return retention
}

func (s *Server) clusterHeartbeatState() (time.Time, string) {
	s.clusterMu.RLock()
	defer s.clusterMu.RUnlock()
	return s.lastHeartbeatAt, s.lastHeartbeatErr
}

func (s *Server) databaseTarget() string {
	if s.cfg.Database.Driver != "postgres" {
		return s.cfg.Database.Path
	}
	raw := strings.TrimSpace(s.cfg.Database.URL)
	if raw == "" {
		return "postgres"
	}
	u, err := urlpkg.Parse(raw)
	if err != nil {
		return "postgres"
	}
	if u.User != nil {
		if username := u.User.Username(); username != "" {
			u.User = urlpkg.User(username)
		} else {
			u.User = nil
		}
	}
	return u.String()
}
