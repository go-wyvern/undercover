package storage

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type RedisConfig struct {
	URL, Addr, Username, Password string
}

type RedisSessions struct {
	client redis.UniversalClient
	prefix string
}

func OpenRedis(ctx context.Context, cfg RedisConfig) (*RedisSessions, error) {
	var client redis.UniversalClient
	if strings.TrimSpace(cfg.Addr) != "" {
		// Like spxbench, REDIS_ADDR selects Cluster mode, including one seed node.
		var addrs []string
		for _, addr := range strings.Split(cfg.Addr, ",") {
			addr = strings.TrimSpace(addr)
			if addr == "" {
				continue
			}
			host, port, err := net.SplitHostPort(addr)
			if err != nil || host == "" || port == "" {
				return nil, errors.New("REDIS_ADDR must contain comma-separated host:port entries")
			}
			addrs = append(addrs, addr)
		}
		if len(addrs) == 0 {
			return nil, errors.New("REDIS_ADDR contains no cluster nodes")
		}
		client = redis.NewClusterClient(&redis.ClusterOptions{
			Addrs: addrs, Username: cfg.Username, Password: cfg.Password,
			DialTimeout: 3 * time.Second, ReadTimeout: 3 * time.Second,
			WriteTimeout: 3 * time.Second, MaxRetries: 1,
		})
	} else {
		if strings.TrimSpace(cfg.URL) == "" {
			return nil, errors.New("REDIS_URL or REDIS_ADDR is required")
		}
		options, err := redis.ParseURL(strings.TrimSpace(cfg.URL))
		if err != nil {
			return nil, errors.New("invalid REDIS_URL")
		}
		if cfg.Username != "" {
			options.Username = cfg.Username
		}
		if cfg.Password != "" {
			options.Password = cfg.Password
		}
		options.DialTimeout = 3 * time.Second
		options.ReadTimeout = 3 * time.Second
		options.WriteTimeout = 3 * time.Second
		options.MaxRetries = 1
		client = redis.NewClient(options)
	}
	if err := client.Ping(ctx).Err(); err != nil {
		client.Close()
		return nil, fmt.Errorf("connect Redis: %w", err)
	}
	return &RedisSessions{client: client, prefix: "undercover:session:"}, nil
}
func (s *RedisSessions) Get(ctx context.Context, key string) ([]byte, error) {
	value, err := s.client.Get(ctx, s.prefix+key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrNotFound
	}
	return value, err
}
func (s *RedisSessions) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if ttl <= 0 {
		return errors.New("session TTL must be positive")
	}
	return s.client.Set(ctx, s.prefix+key, value, ttl).Err()
}
func (s *RedisSessions) Delete(ctx context.Context, key string) error {
	return s.client.Del(ctx, s.prefix+key).Err()
}
func (s *RedisSessions) Close() error { return s.client.Close() }
