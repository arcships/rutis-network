//go:build (darwin || linux) && (amd64 || arm64)

package mdns

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"
)

// Use the system DNS-SD client ABI so the responder that owns the host's
// .local name also probes, advertises and withdraws the service records.
type client struct {
	register func(*uintptr, uint32, uint32, string, string, string, uintptr, uint16, uint16, *byte, uintptr, uintptr) int32
	update   func(uintptr, uintptr, uint32, uint16, *byte, uint32) int32
	fd       func(uintptr) int32
	process  func(uintptr) int32
	free     func(uintptr)
	callback uintptr
}

type registration struct {
	instance string
	ready    bool
	err      error
}

var registrations sync.Map // DNSServiceRef -> *registration, used only during ProcessResult

var loadClient = sync.OnceValues(func() (*client, error) {
	path := "libdns_sd.so.1"
	if runtime.GOOS == "darwin" {
		path = "/usr/lib/libSystem.B.dylib"
	}
	lib, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, fmt.Errorf("load system DNS-SD client %s: %w", path, err)
	}
	api := &client{}
	for name, target := range map[string]any{
		"DNSServiceRegister": &api.register, "DNSServiceUpdateRecord": &api.update, "DNSServiceRefSockFD": &api.fd,
		"DNSServiceProcessResult": &api.process, "DNSServiceRefDeallocate": &api.free,
	} {
		sym, err := purego.Dlsym(lib, name)
		if err != nil {
			purego.Dlclose(lib)
			return nil, err
		}
		purego.RegisterFunc(target, sym)
	}
	api.callback = purego.NewCallback(func(ref uintptr, _ uint32, code int32, name *byte, _, _, _ uintptr) {
		v, ok := registrations.Load(ref)
		if !ok {
			return
		}
		r := v.(*registration)
		r.ready = true
		if code != 0 {
			r.err = fmt.Errorf("DNS-SD registration callback: %d", code)
			return
		}
		if name == nil || unix.BytePtrToString(name) != r.instance {
			r.err = fmt.Errorf("DNS-SD instance %q is already in use", r.instance)
		}
	})
	return api, nil
})

// startPublisher waits for the daemon to confirm the configured instance.
// The returned row task owns the reference until cancellation or daemon loss.
func startPublisher(ctx context.Context, p advert, index int) (*publication, error) {
	api, err := loadClient()
	if err != nil {
		return nil, err
	}
	var ref uintptr
	port := binary.NativeEndian.Uint16([]byte{byte(p.Port >> 8), byte(p.Port)})
	code := api.register(&ref, 0, uint32(index), p.Instance, p.Type, "local.", 0, port, uint16(len(p.txt)), unsafe.SliceData(p.txt), api.callback, 0)
	if code != 0 {
		return nil, fmt.Errorf("DNSServiceRegister on interface %d: %d", index, code)
	}
	r := &registration{instance: p.Instance}
	registrations.Store(ref, r)
	closeRef := func() { registrations.Delete(ref); api.free(ref) }
	fd := api.fd(ref)
	if fd < 0 {
		closeRef()
		return nil, errors.New("DNSServiceRefSockFD returned an invalid socket")
	}
	poll := func() error {
		fds := []unix.PollFd{{Fd: fd, Events: unix.POLLIN}}
		n, err := unix.Poll(fds, 100)
		if errors.Is(err, unix.EINTR) {
			return nil
		}
		if err != nil || n == 0 {
			return err
		}
		if fds[0].Revents&unix.POLLIN == 0 {
			return errors.New("DNS-SD daemon disconnected")
		}
		if code := api.process(ref); code != 0 {
			return fmt.Errorf("DNSServiceProcessResult: %d", code)
		}
		return r.err
	}
	startup, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for !r.ready {
		if err := startup.Err(); err != nil {
			closeRef()
			return nil, err
		}
		if err := poll(); err != nil {
			closeRef()
			return nil, err
		}
	}
	var mu sync.Mutex // Serialize all operations on this native DNSServiceRef.
	pub := &publication{}
	pub.update = func(txt []byte) error {
		mu.Lock()
		defer mu.Unlock()
		if ref == 0 {
			return errors.New("registration has ended")
		}
		if code := api.update(ref, 0, 0, uint16(len(txt)), unsafe.SliceData(txt), 0); code != 0 {
			return fmt.Errorf("DNSServiceUpdateRecord: %d", code)
		}
		return nil
	}
	pub.run = func(ctx context.Context) error {
		defer func() { mu.Lock(); defer mu.Unlock(); closeRef(); ref = 0 }()
		for ctx.Err() == nil {
			mu.Lock()
			err := poll()
			mu.Unlock()
			if err != nil {
				return err
			}
		}
		return ctx.Err()
	}
	return pub, nil
}
