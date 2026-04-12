//go:build !linux

package daemon

func mountRequiresMirror(string) (bool, error) {
	return true, nil
}
