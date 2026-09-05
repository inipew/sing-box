package log

import (
	"strings"
)

type Filter interface {
	ShouldMute(level Level, tag string, message string) bool
}

type FilterFunc func(level Level, tag string, message string) bool

func (f FilterFunc) ShouldMute(level Level, tag string, message string) bool {
	return f(level, tag, message)
}

type NoiseFilter struct {
	patterns []string
	minLevel Level
}

var defaultNoisePatterns = []string{
	"i/o timeout",
	"tls: protocol is shutdown",
	"handle stream request: read request: EOF",
	"ws closed: 1000",
	"keepalive timeout",
	"connection timed out",
	"read multiplex stream request: EOF",
	"name error",
	"connection refused",
	"drop connections by rule",
	"drop by rule",
}

func NewNoiseFilter(minLevel Level, customPatterns ...string) *NoiseFilter {
	patterns := make([]string, len(defaultNoisePatterns), len(defaultNoisePatterns)+len(customPatterns))
	copy(patterns, defaultNoisePatterns)
	if len(customPatterns) > 0 {
		patterns = append(patterns, customPatterns...)
	}
	return &NoiseFilter{
		patterns: patterns,
		minLevel: minLevel,
	}
}

func (f *NoiseFilter) ShouldMute(level Level, tag string, message string) bool {
	if f == nil || level < f.minLevel {
		return false
	}
	for _, pattern := range f.patterns {
		if strings.Contains(message, pattern) {
			return true
		}
	}
	return false
}
