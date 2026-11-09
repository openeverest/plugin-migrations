package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// keyPrefix starts every key the agent writes: the heartbeat keys on the
// source (which RedisShake replicates) and the state key on the target.
const keyPrefix = "__openeverest_migration:"

const commandTimeout = 10 * time.Second

// server is a connected source or target, standalone or cluster.
type server struct {
	endpoint Endpoint
	client   redis.UniversalClient
	cluster  bool
}

func connect(ctx context.Context, endpoint Endpoint) (*server, error) {
	tlsConfig, err := endpoint.tlsConfig()
	if err != nil {
		return nil, err
	}
	plain := redis.NewClient(&redis.Options{
		Addr: endpoint.Address(), Username: endpoint.User, Password: endpoint.Password, TLSConfig: tlsConfig,
		DialTimeout: commandTimeout, ReadTimeout: commandTimeout, WriteTimeout: commandTimeout, MaxRetries: 1,
		Protocol: 2, DisableIdentity: true,
	})
	info, err := plain.Info(ctx, "cluster").Result()
	if err != nil {
		_ = plain.Close()
		return nil, err
	}
	if !strings.Contains(info, "cluster_enabled:1") {
		return &server{endpoint: endpoint, client: plain}, nil
	}
	_ = plain.Close()
	clusterClient := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs: []string{endpoint.Address()}, Username: endpoint.User, Password: endpoint.Password, TLSConfig: tlsConfig,
		DialTimeout: commandTimeout, ReadTimeout: commandTimeout, WriteTimeout: commandTimeout, MaxRetries: 1,
		Protocol: 2, DisableIdentity: true,
	})
	return &server{endpoint: endpoint, client: clusterClient, cluster: true}, nil
}

func (s *server) Close() error { return s.client.Close() }

// forEachPrimary runs fn on the server, or on every primary of a cluster.
func (s *server) forEachPrimary(ctx context.Context, fn func(context.Context, *redis.Client) error) error {
	if cluster, ok := s.client.(*redis.ClusterClient); ok {
		cluster.ReloadState(ctx)
		return cluster.ForEachMaster(ctx, fn)
	}
	client, ok := s.client.(*redis.Client)
	if !ok {
		return errors.New("unexpected client type")
	}
	return fn(ctx, client)
}

// serverFacts is what preflight needs; for a cluster, keyspace counts are
// summed over the primaries.
type serverFacts struct {
	Valkey  bool
	Version string
	Role    string
	// ClusterOK is false for a cluster whose cluster_state is not ok.
	ClusterOK bool
	RunIDs    []string
	// Keys and VolatileItems per database number.
	Keys map[int]int64
	// VolatileItems counts keys with expiring hash fields: subexpiry on
	// Redis, keys_with_volatile_items on Valkey.
	VolatileItems int64
}

func (f serverFacts) product() string {
	if f.Valkey {
		return "Valkey " + f.Version
	}
	return "Redis " + f.Version
}

func (f serverFacts) totalKeys() int64 {
	var total int64
	for _, keys := range f.Keys {
		total += keys
	}
	return total
}

func (s *server) inspect(ctx context.Context) (serverFacts, error) {
	facts := serverFacts{Keys: map[int]int64{}, ClusterOK: true, Role: "master"}
	var mu sync.Mutex
	err := s.forEachPrimary(ctx, func(ctx context.Context, client *redis.Client) error {
		info, err := client.Info(ctx, "server", "replication", "keyspace", "cluster").Result()
		if err != nil {
			return err
		}
		fields := parseInfo(info)
		mu.Lock()
		defer mu.Unlock()
		facts.Valkey = fields["server_name"] == "valkey" || fields["valkey_version"] != ""
		facts.Version = fields["redis_version"]
		if facts.Valkey && fields["valkey_version"] != "" {
			facts.Version = fields["valkey_version"]
		}
		if fields["role"] != "master" {
			facts.Role = fields["role"]
		}
		facts.RunIDs = append(facts.RunIDs, fields["run_id"])
		for name, value := range fields {
			db, found := strings.CutPrefix(name, "db")
			number, err := strconv.Atoi(db)
			if !found || err != nil {
				continue
			}
			stats := parseInfoList(value)
			keys, _ := strconv.ParseInt(stats["keys"], 10, 64)
			facts.Keys[number] += keys
			for _, field := range []string{"subexpiry", "keys_with_volatile_items"} {
				volatile, _ := strconv.ParseInt(stats[field], 10, 64)
				facts.VolatileItems += volatile
			}
		}
		return nil
	})
	if err != nil {
		return serverFacts{}, err
	}
	if s.cluster {
		state, err := s.client.ClusterInfo(ctx).Result()
		if err != nil {
			return serverFacts{}, err
		}
		facts.ClusterOK = parseInfo(state)["cluster_state"] == "ok"
	}
	slices.Sort(facts.RunIDs)
	return facts, nil
}

// parseInfo reads INFO (or CLUSTER INFO) name:value lines.
func parseInfo(info string) map[string]string {
	fields := map[string]string{}
	for _, line := range strings.Split(info, "\n") {
		name, value, found := strings.Cut(strings.TrimSpace(line), ":")
		if found && !strings.HasPrefix(name, "#") {
			fields[name] = value
		}
	}
	return fields
}

// parseInfoList reads "keys=1,expires=0" values.
func parseInfoList(value string) map[string]string {
	fields := map[string]string{}
	for _, item := range strings.Split(value, ",") {
		if name, value, found := strings.Cut(item, "="); found {
			fields[name] = value
		}
	}
	return fields
}

// flushAll deletes every key: on every primary of a cluster.
func (s *server) flushAll(ctx context.Context) error {
	return s.forEachPrimary(ctx, func(ctx context.Context, client *redis.Client) error {
		return client.FlushAll(ctx).Err()
	})
}

// slotRange is an inclusive range of cluster slots.
type slotRange struct{ start, end int }

// shardRanges returns the slot ranges of every primary; a standalone server
// is one shard owning every slot.
func (s *server) shardRanges(ctx context.Context) ([][]slotRange, error) {
	if !s.cluster {
		return [][]slotRange{{{0, clusterSlots - 1}}}, nil
	}
	reply, err := s.client.ClusterSlots(ctx).Result()
	if err != nil {
		return nil, err
	}
	index := map[string]int{}
	var shards [][]slotRange
	for _, owned := range reply {
		if len(owned.Nodes) == 0 {
			continue
		}
		id := owned.Nodes[0].ID
		i, seen := index[id]
		if !seen {
			i = len(shards)
			index[id] = i
			shards = append(shards, nil)
		}
		shards[i] = append(shards[i], slotRange{owned.Start, owned.End})
	}
	if len(shards) == 0 {
		return nil, errors.New("the cluster reports no slots")
	}
	return shards, nil
}

// heartbeatSlots picks one slot for every pair of a source shard and a
// target shard that share slots. RedisShake sends one source shard's changes
// for one target shard through one ordered connection, so a heartbeat in
// every pair proves everything sent before it was applied. Pairs that share
// no slot exchange no data.
func heartbeatSlots(source, target [][]slotRange) []int {
	var slots []int
	for _, sourceShard := range source {
		for _, targetShard := range target {
			if slot, ok := firstSharedSlot(sourceShard, targetShard); ok {
				slots = append(slots, slot)
			}
		}
	}
	slices.Sort(slots)
	return slices.Compact(slots)
}

func firstSharedSlot(a, b []slotRange) (int, bool) {
	best, found := 0, false
	for _, x := range a {
		for _, y := range b {
			start, end := max(x.start, y.start), min(x.end, y.end)
			if start <= end && (!found || start < best) {
				best, found = start, true
			}
		}
	}
	return best, found
}

// nowMillis is the server's clock, which also timestamps the heartbeats.
func (s *server) nowMillis(ctx context.Context) (int64, error) {
	now, err := s.client.Time(ctx).Result()
	if err != nil {
		return 0, err
	}
	return now.UnixMilli(), nil
}

func heartbeatKey(name string, slot int) string {
	return fmt.Sprintf("%s{%s}:%s:heartbeat", keyPrefix, slotTag(slot), name)
}

func stateKey(name string) string { return keyPrefix + name + ":state" }

// deleteKeys removes keys one at a time, so they may live on different
// cluster slots.
func (s *server) deleteKeys(ctx context.Context, keys []string) error {
	for _, key := range keys {
		if err := s.client.Del(ctx, key).Err(); err != nil {
			return err
		}
	}
	return nil
}

// scanKeys lists keys matching pattern on every primary.
func (s *server) scanKeys(ctx context.Context, pattern string) ([]string, error) {
	var (
		mu   sync.Mutex
		keys []string
	)
	err := s.forEachPrimary(ctx, func(ctx context.Context, client *redis.Client) error {
		iter := client.Scan(ctx, 0, pattern, 1000).Iterator()
		for iter.Next(ctx) {
			mu.Lock()
			keys = append(keys, iter.Val())
			mu.Unlock()
		}
		return iter.Err()
	})
	return keys, err
}
