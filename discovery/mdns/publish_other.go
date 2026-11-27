//go:build (!darwin && !linux) || (!amd64 && !arm64)

package mdns

import (
	"context"
	"errors"
)

func startPublisher(context.Context, advert, int) (func(context.Context) error, error) {
	return nil, errors.New("DNS-SD publishing requires macOS or Linux on amd64 or arm64")
}
