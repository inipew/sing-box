package ratelimit

type Manager struct {
	shared map[string]*Limiter
}

func NewManager(sharedConfigs map[string]Config) *Manager {
	m := &Manager{
		shared: make(map[string]*Limiter, len(sharedConfigs)),
	}
	for tag, cfg := range sharedConfigs {
		m.shared[tag] = NewLimiter(cfg)
	}
	return m
}

func (m *Manager) Get(tag string) (*Limiter, bool) {
	if m == nil || m.shared == nil {
		return nil, false
	}
	limiter, ok := m.shared[tag]
	return limiter, ok
}

func (m *Manager) NewForConnection(cfg Config) *Limiter {
	return NewLimiter(cfg)
}

func (m *Manager) NewForPacketConnection(cfg Config) *Limiter {
	return NewUDPLimiter(cfg)
}
