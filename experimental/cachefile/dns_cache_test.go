package cachefile

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/logger"
	"github.com/stretchr/testify/require"
)

func newDNSCacheFile(t *testing.T) *CacheFile {
	t.Helper()
	cache := New(context.Background(), logger.NOP(), option.CacheFileOptions{
		Path:     t.TempDir() + "/cache.db",
		StoreDNS: true,
	})
	require.NoError(t, cache.Start(adapter.StartStateInitialize))
	t.Cleanup(func() { require.NoError(t, cache.Close()) })
	return cache
}

func TestDeleteDNSCacheRemovesMatchingCorruptEntry(t *testing.T) {
	cache := newDNSCacheFile(t)
	expireAt := time.Now().Add(time.Hour)
	corrupt := []byte("not a DNS message")
	require.NoError(t, cache.SaveDNSCache("dns", "example.com.", 1, corrupt, expireAt))

	cache.DeleteDNSCache("dns", "example.com.", 1, corrupt)
	cache.Flush()

	_, _, loaded := cache.LoadDNSCache("dns", "example.com.", 1)
	require.False(t, loaded)
}

func TestDeleteDNSCacheDoesNotDeleteNewerPendingValue(t *testing.T) {
	cache := newDNSCacheFile(t)
	expireAt := time.Now().Add(time.Hour)
	corrupt := []byte("not a DNS message")
	valid := []byte("newer DNS message")
	require.NoError(t, cache.SaveDNSCache("dns", "example.com.", 1, corrupt, expireAt))

	cache.DeleteDNSCache("dns", "example.com.", 1, corrupt)
	cache.SaveDNSCacheAsync("dns", "example.com.", 1, valid, expireAt, logger.NOP())
	cache.Flush()

	rawMessage, _, loaded := cache.LoadDNSCache("dns", "example.com.", 1)
	require.True(t, loaded)
	require.True(t, bytes.Equal(valid, rawMessage))
}

func TestDeleteDNSCacheDoesNotDeleteDifferentStoredValue(t *testing.T) {
	cache := newDNSCacheFile(t)
	expireAt := time.Now().Add(time.Hour)
	stale := []byte("stale corrupt message")
	valid := []byte("newer stored DNS message")
	require.NoError(t, cache.SaveDNSCache("dns", "example.com.", 1, valid, expireAt))

	cache.DeleteDNSCache("dns", "example.com.", 1, stale)
	cache.Flush()

	rawMessage, _, loaded := cache.LoadDNSCache("dns", "example.com.", 1)
	require.True(t, loaded)
	require.Equal(t, valid, rawMessage)
}

func TestDeleteDNSCacheQueuesAfterMatchingWritingValue(t *testing.T) {
	cache := newDNSCacheFile(t)
	key := saveCacheKey{"dns", "example.com.", 1}
	corrupt := []byte("not a DNS message")
	value := make([]byte, 8+len(corrupt))
	copy(value[8:], corrupt)

	cache.pendingAccess.Lock()
	cache.writing = newPendingWrites()
	cache.writing.dnsCache[key] = saveDNSCacheEntry{value: value}
	cache.pendingAccess.Unlock()

	cache.DeleteDNSCache(key.TransportName, key.QuestionName, key.QType, corrupt)

	cache.pendingAccess.RLock()
	entry, loaded := cache.pending.dnsCache[key]
	cache.pendingAccess.RUnlock()
	require.True(t, loaded)
	require.True(t, entry.delete)
	require.Equal(t, corrupt, entry.expected)
	require.Equal(t, 1, cache.pending.count)
	require.Equal(t, len(key.QuestionName)+len(corrupt), cache.pending.size)
}
