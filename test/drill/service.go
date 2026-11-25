//go:build drill

package drill

// The key service of the drill (OpenBao with integrated storage): part C6.

type transitConf struct {
	address, tokenFile string
}

func (c *transitConf) env() []string { return nil }

type service struct {
	version string
}

func (s *service) conf() *transitConf { return &transitConf{} }

func (s *service) restoreConf(w *World, r *Restored, spec RestoreSpec) keyConf {
	w.notImplemented("the restore of a key service", "C6")
	return keyConf{}
}

func (s *service) beforeDisaster() {}

func (w *World) startService()  { w.notImplemented("the key service", "C6") }
func (w *World) rotateService() { w.notImplemented("the rotation in the key service", "C6") }
