package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const root = "../../.."

// TestGeneratedFilesAreFresh fails when iam/permissions.json changed without
// `make gen`.
func TestGeneratedFilesAreFresh(t *testing.T) {
	files, err := generate(root)
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range files {
		got, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s is stale: run make gen", path)
		}
	}
}

func TestCheckRefusesBadManifests(t *testing.T) {
	cases := map[string]string{
		"prefix":          `{"resource":{"name":"R","prefix":"odyssey"},"permissions":[{"name":"other:x:read","label":"x"}]}`,
		"format":          `{"resource":{"name":"R","prefix":"odyssey"},"permissions":[{"name":"odyssey:X:read","label":"x"}]}`,
		"duplicate":       `{"resource":{"name":"R","prefix":"odyssey"},"permissions":[{"name":"odyssey:x:read","label":"x"},{"name":"odyssey:x:read","label":"x"}]}`,
		"label":           `{"resource":{"name":"R","prefix":"odyssey"},"permissions":[{"name":"odyssey:x:read","label":""}]}`,
		"outside catalog": `{"resource":{"name":"R","prefix":"odyssey"},"permissions":[{"name":"odyssey:x:read","label":"x"}],"roles":[{"name":"A","permissions":["odyssey:y:read"]}]}`,
		"empty role":      `{"resource":{"name":"R","prefix":"odyssey"},"permissions":[{"name":"odyssey:x:read","label":"x"}],"roles":[{"name":"A","permissions":[]}]}`,
		"service":         `{"resource":{"name":"R","prefix":"odyssey"},"permissions":[{"name":"odyssey:x:read","label":"x"}],"service_account":{"name":"s","iam_permissions":["odyssey:x:read"]}}`,
		"unknown field":   `{"resource":{"name":"R","prefix":"odyssey"},"permissions":[],"scopes":[]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, "iam"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, manifestPath), []byte(raw), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := generate(dir); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestIdent(t *testing.T) {
	for in, want := range map[string]string{
		"odyssey:projects:write":  "ProjectsWrite",
		"odyssey:mcp-tools:read":  "MCPToolsRead",
		"odyssey:api-keys:read":   "APIKeysRead",
		"odyssey:ai-models:write": "AIModelsWrite",
	} {
		if got := ident(in); got != want {
			t.Errorf("ident(%q) = %q, want %q", in, got, want)
		}
	}
	if !strings.HasPrefix(header, "Code generated") {
		t.Error("header must mark the files as generated")
	}
}
