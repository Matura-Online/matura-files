package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFilesManifest(t *testing.T) {
	sourceRoot := t.TempDir()
	jsonFile := filepath.Join(sourceRoot, "Json", "2026", "Ljeto", "Mat", "A.json")
	rawFile := filepath.Join(sourceRoot, "Raw", "2026", "Ljeto", "Mat", "A.pdf")

	for _, file := range []string{jsonFile, rawFile} {
		if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("test"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "programi.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := writeFilesManifest(sourceRoot); err != nil {
		t.Fatal(err)
	}

	contents, err := os.ReadFile(filepath.Join(sourceRoot, "files.json"))
	if err != nil {
		t.Fatal(err)
	}

	var manifest []filesManifestEntry
	if err := json.Unmarshal(contents, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest) != 2 || manifest[0].Name != "Json" || manifest[1].Name != "Raw" {
		t.Fatalf("unexpected root manifest entries: %#v", manifest)
	}
	if manifest[0].Children[0].Children[0].Children[0].Children[0].Name != "A.json" {
		t.Fatalf("JSON file is missing from manifest: %#v", manifest[0])
	}
	if manifest[1].Children[0].Children[0].Children[0].Children[0].Name != "A.pdf" {
		t.Fatalf("raw file is missing from manifest: %#v", manifest[1])
	}
}
