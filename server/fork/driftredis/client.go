// SPDX-License-Identifier: Apache-2.0

package driftredis

import (
	"context"
	"crypto/tls"
	"fmt"
	"strings"

	"github.com/redis/go-redis/v9"
)

// Config holds the Redis connection settings (mirrors the --redis-* flags).
type Config struct {
	Hostname           string
	Port               int
	Username           string
	Password           string
	DB                 int
	TLSEnabled         bool
	InsecureSkipVerify bool
	// ClusterAddresses selects cluster mode when non-empty.
	ClusterAddresses []string
}

// NewClient builds a Redis client and verifies connectivity.
func NewClient(cfg Config) (redis.Cmdable, error) {
	var tlsConfig *tls.Config
	if cfg.TLSEnabled {
		tlsConfig = &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: cfg.InsecureSkipVerify, //nolint:gosec // operator opt-in
		}
	}

	var client redis.Cmdable
	var desc string
	if len(cfg.ClusterAddresses) > 0 {
		client = redis.NewClusterClient(&redis.ClusterOptions{
			Addrs:     cfg.ClusterAddresses,
			Username:  cfg.Username,
			Password:  cfg.Password,
			TLSConfig: tlsConfig,
		})
		desc = "cluster nodes " + strings.Join(cfg.ClusterAddresses, ", ")
	} else {
		desc = fmt.Sprintf("%s:%d", cfg.Hostname, cfg.Port)
		client = redis.NewClient(&redis.Options{
			Addr:      desc,
			Username:  cfg.Username,
			Password:  cfg.Password,
			DB:        cfg.DB,
			TLSConfig: tlsConfig,
		})
	}
	if err := client.Ping(context.Background()).Err(); err != nil {
		return nil, fmt.Errorf("connecting to redis at %s: %w", desc, err)
	}
	return client, nil
}
