package mdns

import (
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

func interfaceControl(iface *net.Interface) func(string, string, syscall.RawConn) error {
	return func(network, _ string, c syscall.RawConn) error {
		var bindErr error
		err := c.Control(func(fd uintptr) {
			level, opt := unix.IPPROTO_IP, unix.IP_BOUND_IF
			if network == "tcp6" {
				level, opt = unix.IPPROTO_IPV6, unix.IPV6_BOUND_IF
			}
			bindErr = unix.SetsockoptInt(int(fd), level, opt, iface.Index)
		})
		if err != nil {
			return err
		}
		return bindErr
	}
}
