package bridge

// poolManager keeps warm containers per published template version.
type poolManager struct {
	f *Fleet
}

func newPoolManager(f *Fleet) *poolManager { return &poolManager{f: f} }

func (p *poolManager) fillAsync(name string, n int) {}
