package ratelimit

type Config struct {
	Upload   int64 // bytes per second (0 = unlimited)
	Download int64 // bytes per second (0 = unlimited)
}
