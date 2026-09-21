package dns

import (
	"testing"
	"time"

	"github.com/sagernet/sing/common/logger"
	"github.com/stretchr/testify/require"

	mdns "github.com/miekg/dns"
)

type corruptDNSCacheStore struct {
	rawMessage []byte
	deleted    bool
}

func (s *corruptDNSCacheStore) LoadDNSCache(string, string, uint16) ([]byte, time.Time, bool) {
	return s.rawMessage, time.Now().Add(time.Hour), true
}

func (s *corruptDNSCacheStore) SaveDNSCache(string, string, uint16, []byte, time.Time) error {
	return nil
}

func (s *corruptDNSCacheStore) SaveDNSCacheAsync(string, string, uint16, []byte, time.Time, logger.Logger) {
}

func (s *corruptDNSCacheStore) DeleteDNSCache(_ string, _ string, _ uint16, rawMessage []byte) {
	s.deleted = string(rawMessage) == string(s.rawMessage)
}

func (s *corruptDNSCacheStore) ClearDNSCache() error {
	return nil
}

func TestLoadPersistentResponseDeletesCorruptEntry(t *testing.T) {
	store := &corruptDNSCacheStore{rawMessage: []byte("not a DNS message")}
	client := &Client{dnsCache: store, logger: logger.NOP()}
	key := dnsCacheKey{
		Question: mdns.Question{
			Name:   "example.com.",
			Qtype:  mdns.TypeA,
			Qclass: mdns.ClassINET,
		},
		transportTag: "dns",
	}

	response, _, _ := client.loadPersistentResponse(key)

	require.Nil(t, response)
	require.True(t, store.deleted)
}
