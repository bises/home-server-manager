package hostmetrics

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bises/home-server-manager/internal/config"
)

func TestCollectFromProcAndMounts(t *testing.T) {
	proc := t.TempDir()
	if err := os.WriteFile(filepath.Join(proc, "uptime"), []byte("12345.67 40000.00\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proc, "loadavg"), []byte("0.42 0.35 0.28 1/234 5678\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mem := "MemTotal:        8000000 kB\nMemFree:         1000000 kB\nMemAvailable:    4000000 kB\n"
	if err := os.WriteFile(filepath.Join(proc, "meminfo"), []byte(mem), 0o600); err != nil {
		t.Fatal(err)
	}

	r := &Reader{
		procRoot: proc,
		statfsFunc: func(path string) (uint64, uint64, uint64, error) {
			if path == "/" {
				return 1000, 400, 350, nil
			}
			return 0, 0, 0, errors.New("mount not present")
		},
	}

	m := r.Collect("hsm-fixture-host", []config.MountConfig{
		{ID: "root", Label: "Root storage", Path: "/"},
		{ID: "backup", Label: "Backup mount", Path: "/mnt/example-backup"},
	})

	if m.Hostname != "hsm-fixture-host" || m.UptimeSeconds != 12345 {
		t.Fatalf("unexpected host/uptime: %+v", m)
	}
	if m.Memory.TotalBytes != 8000000*1024 || m.Memory.UsedBytes != 4000000*1024 {
		t.Fatalf("unexpected memory: %+v", m.Memory)
	}
	if m.Load.Load1 != 0.42 || m.Load.Load15 != 0.28 {
		t.Fatalf("unexpected load: %+v", m.Load)
	}
	if len(m.Disks) != 2 || !m.Disks[0].Available || m.Disks[0].UsedBytes != 600 {
		t.Fatalf("unexpected root disk: %+v", m.Disks)
	}
	if m.Disks[1].Available || m.Disks[1].Error == "" {
		t.Fatalf("expected unavailable backup mount with error, got %+v", m.Disks[1])
	}
}
