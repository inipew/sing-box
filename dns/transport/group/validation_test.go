package group

import (
	"context"
	"testing"

	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/stretchr/testify/require"
)

func TestNewGroupRejectsDuplicateAndSelfMembers(t *testing.T) {
	testCases := []struct {
		name    string
		servers []string
		message string
	}{
		{"duplicate", []string{"a", "a"}, "dns group[test]: duplicate member tag: a"},
		{"self", []string{"a", "test"}, "dns group[test]: group cannot contain itself"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := NewGroupTransport(context.Background(), log.NewNOPFactory().NewLogger("test"), "test", option.GroupDNSServerOptions{
				Servers: testCase.servers,
				Policy:  "reliable",
			})
			require.EqualError(t, err, testCase.message)
		})
	}
}
