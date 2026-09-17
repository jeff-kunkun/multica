package service

import (
	"context"
	"strings"
	"testing"
)

// The refusal a too-new bundle gets has to name both numbers. "unsupported
// transfer schema_version" alone sent a user hunting through the exporter for
// a fault that lived in the target's build (DENE-240).
func TestImportTransferConfig_VersionRefusalNamesBothVersions(t *testing.T) {
	req := TransferConfigRequest{}
	req.Manifest.Format = TransferBundleFormat
	req.Manifest.SchemaVersion = TransferBundleSchemaVersion + 1

	_, err := ImportTransferConfig(context.Background(), TransferImportEnv{}, req)
	if err == nil {
		t.Fatal("a bundle newer than this build was accepted")
	}
	ie, ok := err.(*ImportError)
	if !ok || ie.Code != "transfer_bundle_version_unsupported" {
		t.Fatalf("err = %v, want *ImportError with code transfer_bundle_version_unsupported", err)
	}
	for _, want := range []string{"schema_version 3", "up to 2", "upgrade this target server"} {
		if !strings.Contains(ie.Msg, want) {
			t.Errorf("message %q does not contain %q", ie.Msg, want)
		}
	}
}
