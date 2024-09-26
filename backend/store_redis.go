package main

import (
	"bytes"
	"context"
	"encoding/gob"
	"fmt"
	"github.com/redis/go-redis/v9"
)

const (
	ActiveKegKey       = "active_keg"
	MeasurementListKey = "measurements"
)

type RedisStore struct {
	Client *redis.Client
}

func NewRedisStore(config *Config) *RedisStore {
	return &RedisStore{
		Client: redis.NewClient(&redis.Options{
			Addr: config.RedisAddr,
			DB:   config.RedisDB,
		}),
	}
}

func (s *RedisStore) SetActiveKeg(keg int) error {
	return s.Client.Set(context.Background(), ActiveKegKey, keg, 0).Err()
}

func (s *RedisStore) GetActiveKeg() (int, error) {
	return s.Client.Get(context.Background(), ActiveKegKey).Int()
}

func (s *RedisStore) AddMeasurement(m Measurement) error {
	ctx := context.Background()

	var buf bytes.Buffer        // Stand-in for a network connection
	enc := gob.NewEncoder(&buf) // Will write to network.
	if err := enc.Encode(m); err != nil {
		return err
	}

	fmt.Println(buf.String())
	if err := s.Client.RPush(ctx, MeasurementListKey, buf.String()).Err(); err != nil {
		return err
	}

	return s.Client.LTrim(ctx, MeasurementListKey, -1000, -1).Err() // keep last 1000 items
}

func (s *RedisStore) GetMeasurements() ([]Measurement, error) {
	ctx := context.Background()

	vals, err := s.Client.LRange(ctx, MeasurementListKey, 0, -1).Result()
	if err != nil {
		return nil, err
	}

	var measurements []Measurement
	for _, val := range vals {
		var m Measurement
		dec := gob.NewDecoder(bytes.NewBufferString(val))
		if err := dec.Decode(&m); err != nil {
			return nil, err
		}
		measurements = append(measurements, m)
	}

	return measurements, nil
}
