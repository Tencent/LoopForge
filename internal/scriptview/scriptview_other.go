//go:build !linux

package scriptview

import "fmt"

func execute(Config) error {
	return fmt.Errorf("workspace script views require Linux inside the Docker helper")
}
