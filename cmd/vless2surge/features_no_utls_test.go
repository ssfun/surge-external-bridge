//go:build !with_utls || !with_grpc

package main

import (
	"strings"
	"testing"
)

func TestServeRejectsBuildWithoutRequiredUTLSFeature(t *testing.T) {
	err := serve([]string{"--data-dir", t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "without the required with_utls and with_grpc tags") {
		t.Fatalf("untagged serve did not fail with build guidance: %v", err)
	}
}
