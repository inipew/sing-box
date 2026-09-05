package ratelimit

import (
	"context"
	"time"

	"golang.org/x/time/rate"
)

const (
	minTCPBurst = 32 * 1024       // 32 KiB
	maxTCPBurst = 2 * 1024 * 1024 // 2 MiB

	minUDPBurst = 1500      // 1500 B
	maxUDPBurst = 64 * 1024 // 64 KiB
)

type Limiter struct {
	upload    *rate.Limiter
	download  *rate.Limiter
	upBurst   int
	downBurst int
}

func NewLimiter(cfg Config) *Limiter {
	return newLimiterWithBurst(cfg, false)
}

func NewUDPLimiter(cfg Config) *Limiter {
	return newLimiterWithBurst(cfg, true)
}

func newLimiterWithBurst(cfg Config, isUDP bool) *Limiter {
	var upLimiter, downLimiter *rate.Limiter
	var upBurst, downBurst int

	if cfg.Upload > 0 {
		if isUDP {
			upBurst = calculateUDPBurst(cfg.Upload)
		} else {
			upBurst = calculateTCPBurst(cfg.Upload)
		}
		upLimiter = rate.NewLimiter(rate.Limit(cfg.Upload), upBurst)
	}

	if cfg.Download > 0 {
		if isUDP {
			downBurst = calculateUDPBurst(cfg.Download)
		} else {
			downBurst = calculateTCPBurst(cfg.Download)
		}
		downLimiter = rate.NewLimiter(rate.Limit(cfg.Download), downBurst)
	}

	return &Limiter{
		upload:    upLimiter,
		download:  downLimiter,
		upBurst:   upBurst,
		downBurst: downBurst,
	}
}

func calculateTCPBurst(byteRate int64) int {
	b := byteRate / 10 // rate * 100ms
	if b < minTCPBurst {
		return minTCPBurst
	}
	if b > maxTCPBurst {
		return maxTCPBurst
	}
	return int(b)
}

func calculateUDPBurst(byteRate int64) int {
	b := byteRate / 20 // rate * 50ms
	if b < minUDPBurst {
		return minUDPBurst
	}
	if b > maxUDPBurst {
		return maxUDPBurst
	}
	return int(b)
}

func (l *Limiter) UploadBurst() int {
	return l.upBurst
}

func (l *Limiter) DownloadBurst() int {
	return l.downBurst
}

func (l *Limiter) HasUpload() bool {
	return l.upload != nil
}

func (l *Limiter) HasDownload() bool {
	return l.download != nil
}

func (l *Limiter) WaitUpload(ctx context.Context, n int) error {
	return waitN(ctx, l.upload, l.upBurst, n)
}

func (l *Limiter) WaitDownload(ctx context.Context, n int) error {
	return waitN(ctx, l.download, l.downBurst, n)
}

func (l *Limiter) WaitUploadWithTimeout(ctx context.Context, n int, timeout time.Duration) error {
	return waitNWithTimeout(ctx, l.upload, l.upBurst, n, timeout)
}

func (l *Limiter) WaitDownloadWithTimeout(ctx context.Context, n int, timeout time.Duration) error {
	return waitNWithTimeout(ctx, l.download, l.downBurst, n, timeout)
}

func waitN(ctx context.Context, limiter *rate.Limiter, burst int, n int) error {
	if limiter == nil || n <= 0 {
		return nil
	}
	for n > 0 {
		chunk := n
		if burst > 0 && chunk > burst {
			chunk = burst
		}
		err := limiter.WaitN(ctx, chunk)
		if err != nil {
			return err
		}
		n -= chunk
	}
	return nil
}

func waitNWithTimeout(ctx context.Context, limiter *rate.Limiter, burst int, n int, timeout time.Duration) error {
	if limiter == nil || n <= 0 {
		return nil
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	return waitN(ctx, limiter, burst, n)
}
