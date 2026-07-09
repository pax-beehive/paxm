//go:build !unix

package telemetry

func lockDir(_ string) (func(), error) {
	return func() {}, nil
}
