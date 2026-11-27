//go:build !darwin && !linux

package mdns

import (
	"errors"
	"net"
	"syscall"
)

func interfaceControl(*net.Interface) func(string, string, syscall.RawConn) error {
	return func(string, string, syscall.RawConn) error {
		return errors.New("interface-bound description HTTP requires macOS or Linux")
	}
}
