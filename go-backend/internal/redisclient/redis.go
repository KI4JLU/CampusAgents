package redisclient

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/KI4JLU/CampusAgents/go-backend/internal/auth"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/config"
)

type Client struct {
	*redis.Client
}

func New(cfg config.RedisConfig) *Client {
	// Cap PoolSize explicitly. go-redis defaults to 10×GOMAXPROCS, which on a
	// 16-CPU container is 160 — out of proportion with the pgxpool cap and
	// the actual concurrency budget.
	//
	// Total Redis connection budget for the application: this client (cap 32)
	// plus the asynq worker client (also cap 32) → 64 connections per app
	// instance. Redis's default `maxclients` is 10 000, so this is far below
	// the ceiling, but operators sizing Redis for a multi-instance deployment
	// should know both pools exist.
	poolSize := cfg.PoolSize
	if poolSize <= 0 {
		poolSize = 32
	}
	rdb := redis.NewClient(&redis.Options{
		Addr:            fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Password:        cfg.Password,
		DB:              cfg.DB,
		DialTimeout:     5 * time.Second,
		ReadTimeout:     3 * time.Second,
		WriteTimeout:    3 * time.Second,
		PoolTimeout:     4 * time.Second,
		PoolSize:        poolSize,
		MinIdleConns:    2,
		MaxIdleConns:    poolSize,
		MaxRetries:      3,
		MinRetryBackoff: 200 * time.Millisecond,
		MaxRetryBackoff: 2 * time.Second,
	})

	return &Client{Client: rdb}
}

func (c *Client) Ping(ctx context.Context) error {
	return c.Client.Ping(ctx).Err()
}

func (c *Client) Close() error {
	slog.Info("closing Redis connection")
	return c.Client.Close()
}

// BlacklistAdapter wraps Client to satisfy the auth.BlacklistStore interface.
// The embedded *redis.Client has Get/Set/Exists with different return types than
// what BlacklistStore requires, so this adapter provides conforming signatures.
type BlacklistAdapter struct {
	c *Client
}

// NewBlacklistAdapter returns a BlacklistAdapter that satisfies auth.BlacklistStore.
func (c *Client) NewBlacklistAdapter() *BlacklistAdapter {
	return &BlacklistAdapter{c: c}
}

func (a *BlacklistAdapter) Get(ctx context.Context, key string) (string, error) {
	val, err := a.c.Client.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return "", auth.ErrKeyNotFound
	}
	return val, err
}

func (a *BlacklistAdapter) Set(ctx context.Context, key string, value any, expiration time.Duration) error {
	return a.c.Client.Set(ctx, key, value, expiration).Err()
}

func (a *BlacklistAdapter) Exists(ctx context.Context, keys ...string) (int64, error) {
	return a.c.Client.Exists(ctx, keys...).Result()
}

func (a *BlacklistAdapter) Pipeline(ctx context.Context, fn func(pipe auth.PipelineExecer) error) error {
	pipe := a.c.Client.Pipeline()
	wrapper := &pipelineWrapper{pipe: pipe}
	if err := fn(wrapper); err != nil {
		return err
	}
	_, err := pipe.Exec(ctx)
	// go-redis returns redis.Nil from Exec when any pipelined GET misses
	// (e.g. the user-invalidation key is absent for most requests). This is
	// normal, not a pipeline failure — individual command results carry their
	// own errors which CheckAll inspects per-command.
	if err != nil && !errors.Is(err, redis.Nil) {
		return err
	}
	return nil
}

// pipelineWrapper adapts redis.Pipeliner to auth.PipelineExecer by wrapping
// the concrete *redis.StringCmd and *redis.IntCmd return types into the
// auth.StringCmd and auth.IntCmd interfaces.
type pipelineWrapper struct {
	pipe redis.Pipeliner
}

func (pw *pipelineWrapper) Get(ctx context.Context, key string) auth.StringCmd {
	return stringCmdAdapter{cmd: pw.pipe.Get(ctx, key)}
}

func (pw *pipelineWrapper) Exists(ctx context.Context, keys ...string) auth.IntCmd {
	return pw.pipe.Exists(ctx, keys...)
}

// stringCmdAdapter translates redis.Nil from a pipelined GET into the
// auth.ErrKeyNotFound sentinel so CheckAll can distinguish a missing key
// from a transport error.
type stringCmdAdapter struct {
	cmd *redis.StringCmd
}

func (s stringCmdAdapter) Result() (string, error) {
	val, err := s.cmd.Result()
	if errors.Is(err, redis.Nil) {
		return "", auth.ErrKeyNotFound
	}
	return val, err
}
