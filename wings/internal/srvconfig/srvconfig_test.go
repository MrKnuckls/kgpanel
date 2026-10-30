package srvconfig

import "testing"

func TestSaveLoad(t *testing.T) {
	dir := t.TempDir()
	sp := Spec{Image: "python:3.12-slim", Startup: "python main.py", MemoryMB: 512, Ports: []string{"8080/tcp"}}
	if err := Save(dir, "abc123", sp); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir, "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if got.Image != sp.Image || got.Startup != sp.Startup || got.MemoryMB != 512 {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
	if len(got.Ports) != 1 || got.Ports[0] != "8080/tcp" {
		t.Errorf("ports mismatch: %+v", got.Ports)
	}
}

func TestLoadMissing(t *testing.T) {
	if _, err := Load(t.TempDir(), "nope"); err == nil {
		t.Error("expected error for missing config")
	}
}

func TestSaveRequiresImage(t *testing.T) {
	if err := Save(t.TempDir(), "x", Spec{}); err == nil {
		t.Error("expected error for empty image")
	}
}
