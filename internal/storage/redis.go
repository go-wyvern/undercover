package storage

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

type RedisSessions struct {
	client *redis.Client
	prefix string
}

func OpenRedis(ctx context.Context, rawURL string) (*RedisSessions, error) {
	if rawURL == "" {
		return nil, errors.New("REDIS_URL is required")
	}
	options, err := redis.ParseURL(rawURL)
	if err != nil {
		return nil, errors.New("invalid REDIS_URL")
	}
	options.DialTimeout = 3 * time.Second
	options.ReadTimeout = 3 * time.Second
	options.WriteTimeout = 3 * time.Second
	options.MaxRetries = 1
	client := redis.NewClient(options)
	if err = client.Ping(ctx).Err(); err != nil {
		client.Close()
		return nil, errors.New("cannot connect to Redis")
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
