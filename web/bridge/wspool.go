package bridge

// poolManager keeps warm containers per published template version.
type poolManager struct {
	f *Fleet
}

func newPoolManager(f *Fleet) *poolManager { return &poolManager{f: f} }

func (p *poolManager) fillAsync(name string, n int) {}
func (p *poolManager) drop(name string)             {}
func (p *poolManager) resize(name string)           {}
func (p *poolManager) status(m TemplateMeta) any    { return nil }
func (p *poolManager) startMedians(name string) any { return nil }
