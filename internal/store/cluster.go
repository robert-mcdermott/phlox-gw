package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

type ClusterNode struct {
	InstanceID     string    `json:"instance_id"`
	Hostname       string    `json:"hostname"`
	Version        string    `json:"version"`
	Addr           string    `json:"addr"`
	DeploymentMode string    `json:"deployment_mode"`
	DBDriver       string    `json:"db_driver"`
	Status         string    `json:"status"`
	StartedAt      time.Time `json:"started_at"`
	LastSeenAt     time.Time `json:"last_seen_at"`
	Metadata       string    `json:"metadata"`
}

func (s *Store) UpsertClusterNode(ctx context.Context, node ClusterNode) error {
	now := node.LastSeenAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	started := node.StartedAt
	if started.IsZero() {
		started = now
	}
	if strings.TrimSpace(node.Status) == "" {
		node.Status = "ready"
	}
	_, err := s.exec(ctx, `
		INSERT INTO cluster_nodes
		(instance_id, hostname, version, addr, deployment_mode, db_driver, status, started_at, last_seen_at, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(instance_id) DO UPDATE SET
			hostname = excluded.hostname,
			version = excluded.version,
			addr = excluded.addr,
			deployment_mode = excluded.deployment_mode,
			db_driver = excluded.db_driver,
			status = excluded.status,
			started_at = excluded.started_at,
			last_seen_at = excluded.last_seen_at,
			metadata = excluded.metadata`,
		node.InstanceID, node.Hostname, node.Version, node.Addr, node.DeploymentMode, node.DBDriver, node.Status,
		formatTime(started), formatTime(now), valueOr(node.Metadata, "{}"))
	return err
}

func (s *Store) DeleteClusterNode(ctx context.Context, instanceID string) error {
	res, err := s.exec(ctx, `DELETE FROM cluster_nodes WHERE instance_id = ?`, instanceID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// PruneClusterNodes removes node rows whose lease has long expired. The
// registry reflects current membership, not history, so rows not refreshed
// within the retention window are garbage-collected.
func (s *Store) PruneClusterNodes(ctx context.Context, olderThan time.Time) (int64, error) {
	res, err := s.exec(ctx, `DELETE FROM cluster_nodes WHERE last_seen_at < ?`, formatTime(olderThan))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func (s *Store) ListClusterNodes(ctx context.Context) ([]ClusterNode, error) {
	rows, err := s.query(ctx, `
		SELECT instance_id, hostname, version, addr, deployment_mode, db_driver, status, started_at, last_seen_at, metadata
		FROM cluster_nodes
		ORDER BY last_seen_at DESC, instance_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClusterNode
	for rows.Next() {
		node, err := scanClusterNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, node)
	}
	return out, rows.Err()
}

func scanClusterNode(row scanner) (ClusterNode, error) {
	var node ClusterNode
	var started, lastSeen string
	err := row.Scan(&node.InstanceID, &node.Hostname, &node.Version, &node.Addr, &node.DeploymentMode, &node.DBDriver, &node.Status, &started, &lastSeen, &node.Metadata)
	if errors.Is(err, sql.ErrNoRows) {
		return ClusterNode{}, ErrNotFound
	}
	if err != nil {
		return ClusterNode{}, err
	}
	node.StartedAt = parseTime(started)
	node.LastSeenAt = parseTime(lastSeen)
	if strings.TrimSpace(node.Metadata) == "" {
		node.Metadata = "{}"
	}
	return node, nil
}
