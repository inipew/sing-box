package ratelimit

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLimiter_BasicAndIsolation(t *testing.T) {
	// 100 KB/s upload, 200 KB/s download
	cfg := Config{
		Upload:   100 * 1024,
		Download: 200 * 1024,
	}
	limiter := NewLimiter(cfg)
	require.True(t, limiter.HasUpload())
	require.True(t, limiter.HasDownload())

	ctx := context.Background()

	// Initial burst consumption should be immediate
	start := time.Now()
	err := limiter.WaitUpload(ctx, 10*1024)
	require.NoError(t, err)
	require.Less(t, time.Since(start), 50*time.Millisecond)

	// Download bucket should still have its full burst available (isolation)
	start = time.Now()
	err = limiter.WaitDownload(ctx, 20*1024)
	require.NoError(t, err)
	require.Less(t, time.Since(start), 50*time.Millisecond)
}

func TestLimiter_ChunkingLargeBuffer(t *testing.T) {
	// 100 KB/s, burst = max(32KB, 10KB) = 32KB
	cfg := Config{
		Download: 100 * 1024,
	}
	limiter := NewLimiter(cfg)
	burst := limiter.DownloadBurst()
	require.Equal(t, 32*1024, burst)

	ctx := context.Background()
	// Consume entire burst first
	err := limiter.WaitDownload(ctx, burst)
	require.NoError(t, err)

	// Consuming 64KB (2 * burst) should succeed via chunking (with measured delay)
	ctxTimeout, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	start := time.Now()
	err = limiter.WaitDownload(ctxTimeout, 64*1024)
	require.NoError(t, err)
	elapsed := time.Since(start)
	// 64 KB at 100 KB/s should take ~640ms (allowing wide tolerance for CI)
	require.GreaterOrEqual(t, elapsed, 400*time.Millisecond)
}

func TestLimiter_ContextCancellation(t *testing.T) {
	cfg := Config{
		Upload: 10 * 1024, // 10 KB/s
	}
	limiter := NewLimiter(cfg)
	ctx := context.Background()

	// Drain burst
	_ = limiter.WaitUpload(ctx, limiter.UploadBurst())

	// Request with already-canceled context
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()

	err := limiter.WaitUpload(canceledCtx, 10*1024)
	require.ErrorIs(t, err, context.Canceled)
}

func TestSharedLimiter_Concurrency(t *testing.T) {
	// Shared pool: 300 KB/s download
	manager := NewManager(map[string]Config{
		"shared-pool": {
			Download: 300 * 1024,
		},
	})

	limiter, ok := manager.Get("shared-pool")
	require.True(t, ok)
	require.NotNil(t, limiter)

	// Drain initial burst
	_ = limiter.WaitDownload(context.Background(), limiter.DownloadBurst())

	// 3 concurrent workers each consuming 100 KB
	var totalBytes atomic.Int64
	var wg sync.WaitGroup
	start := time.Now()

	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := limiter.WaitDownload(context.Background(), 100*1024)
			if err == nil {
				totalBytes.Add(100 * 1024)
			}
		}()
	}

	wg.Wait()
	elapsed := time.Since(start)

	require.Equal(t, int64(300*1024), totalBytes.Load())
	// 300 KB total through a 300 KB/s shared bucket after burst should take ~1.0s
	require.GreaterOrEqual(t, elapsed, 600*time.Millisecond)
}

func TestPerConnectionLimiter_Concurrency(t *testing.T) {
	manager := NewManager(nil)
	cfg := Config{
		Download: 200 * 1024, // 200 KB/s each
	}

	// 3 independent connections
	var wg sync.WaitGroup
	start := time.Now()

	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			connLimiter := manager.NewForConnection(cfg)
			// Drain burst
			_ = connLimiter.WaitDownload(context.Background(), connLimiter.DownloadBurst())

			_ = connLimiter.WaitDownload(context.Background(), 100*1024)
		}()
	}

	wg.Wait()
	elapsed := time.Since(start)

	// Since each has its own 200 KB/s bucket, all 3 run in parallel taking ~500ms (not 1.5s)
	require.Less(t, elapsed, 1200*time.Millisecond)
}

func TestLimitedConn_ReadWrite(t *testing.T) {
	cfg := Config{
		Upload:   200 * 1024,
		Download: 200 * 1024,
	}
	limiter := NewLimiter(cfg)

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	limitedClient := NewLimitedConn(context.Background(), clientConn, limiter)

	// Write test (Download limit)
	testPayload := bytes.Repeat([]byte("A"), 64*1024)
	go func() {
		_, _ = limitedClient.Write(testPayload)
	}()

	recvBuf := make([]byte, 64*1024)
	n, err := io.ReadFull(serverConn, recvBuf)
	require.NoError(t, err)
	require.Equal(t, 64*1024, n)
	require.Equal(t, testPayload, recvBuf)

	// Read test (Upload limit)
	go func() {
		_, _ = serverConn.Write(testPayload)
	}()

	readBuf := make([]byte, 64*1024)
	n, err = io.ReadFull(limitedClient, readBuf)
	require.NoError(t, err)
	require.Equal(t, 64*1024, n)
	require.Equal(t, testPayload, readBuf)
}
