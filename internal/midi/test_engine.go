package midi

import (
	"gitlab.com/gomidi/midi/v2/drivers"
	"gitlab.com/gomidi/midi/v2/drivers/testdrv"
)

// NewTestEngine returns an engine backed by a private gomidi testdrv
// instance: two fake ports ("tuiper-test-in"/"-out") that are wired to each
// other — anything sent to the out port arrives on the in port. No CGO, no
// hardware. It is used for the -demo mode and for tests.
func NewTestEngine() (*Engine, error) {
	drv := testdrv.New("tuiper-test")

	// testdrv is also auto-registered globally by its own init(), but we use
	// our own instance to keep it fully private.
	var _ drivers.Driver = drv
	return &Engine{drv: drv}, nil
}
