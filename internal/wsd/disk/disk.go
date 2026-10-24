// Package disk measures logical workspace usage without following symlinks.
package disk

import (
	"os"
	"path/filepath"
)

type Usage struct {
	UsedBytes     int64   `json:"used_bytes"`
	LimitBytes    int64   `json:"limit_bytes"`
	HighWatermark float64 `json:"high_watermark,omitempty"`
}

func Measure(root string, limit int64, highWatermark float64) (Usage, error) {
	usage := Usage{LimitBytes: limit, HighWatermark: highWatermark}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		usage.UsedBytes += info.Size()
		return nil
	})
	return usage, err
}

func (u Usage) AboveHighWatermark() bool {
	return u.LimitBytes > 0 && u.HighWatermark > 0 && u.UsedBytes >= int64(float64(u.LimitBytes)*u.HighWatermark)
}
