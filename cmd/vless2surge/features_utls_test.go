//go:build with_utls && with_grpc

package main

import (
	"strings"
	"testing"

	"github.com/ssfun/vless2surge/internal/core"
)

func TestRequiredUTLSFeatureIsEmbedded(t *testing.T) {
	if err := core.ValidateBuildFeatures(); err != nil {
		t.Fatal(err)
	}
	if core.CoreVersion == "" || strings.EqualFold(core.CoreVersion, "unknown") {
		t.Fatalf("embedded sing-box version was not derived from Go build info: %q", core.CoreVersion)
	}
}
