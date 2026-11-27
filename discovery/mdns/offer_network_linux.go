package mdns

import (
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

func interfaceControl(iface *net.Interface) func(string, string, syscall.RawConn) error {
	return func(_, _ string, c syscall.RawConn) error {
		var bindErr error
		err := c.Control(func(fd uintptr) { bindErr = unix.BindToDevice(int(fd), iface.Name) })
		if err != nil {
			return err
		}
		return bindErr
	}
}
