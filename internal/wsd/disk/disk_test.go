package disk

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMeasureAndWatermark(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a"), []byte("1234"), 0o600); err != nil {
		t.Fatal(err)
	}
	usage, err := Measure(root, 10, 0.4)
	if err != nil {
		t.Fatal(err)
	}
	if usage.UsedBytes != 4 || !usage.AboveHighWatermark() {
		t.Fatalf("usage = %+v", usage)
	}
}
