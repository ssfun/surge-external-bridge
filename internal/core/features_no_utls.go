//go:build !with_utls || !with_grpc

package core

import "errors"

func ValidateBuildFeatures() error {
	return errors.New("this binary was built without the required with_utls and with_grpc tags; Reality, uTLS, or race-safe standard gRPC is unavailable")
}
