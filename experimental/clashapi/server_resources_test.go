package clashapi

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func createExternalUIArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for name, content := range files {
		entry, err := writer.Create(name)
		require.NoError(t, err)
		_, err = entry.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	return archive.Bytes()
}

func TestDownloadExternalUIZIPRejectsTraversal(t *testing.T) {
	server := &Server{ctx: context.Background()}
	output := t.TempDir()
	archive := createExternalUIArchive(t, map[string]string{
		"ui/index.html":       "safe",
		"ui/../../escaped.js": "unsafe",
	})

	err := server.downloadZIP(bytes.NewReader(archive), output)
	require.Error(t, err)
	_, statErr := os.Stat(filepath.Join(filepath.Dir(output), "escaped.js"))
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestInstallExternalUIPreservesCurrentVersionOnInvalidArchive(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "ui")
	require.NoError(t, os.Mkdir(output, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(output, "index.html"), []byte("current"), 0o644))
	server := &Server{ctx: context.Background(), externalUI: output}

	err := server.installExternalUI(bytes.NewReader([]byte("not a zip archive")))
	require.Error(t, err)
	content, readErr := os.ReadFile(filepath.Join(output, "index.html"))
	require.NoError(t, readErr)
	require.Equal(t, "current", string(content))
}

func TestInstallExternalUIReplacesCurrentVersion(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "ui")
	require.NoError(t, os.Mkdir(output, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(output, "old.html"), []byte("old"), 0o644))
	server := &Server{ctx: context.Background(), externalUI: output}
	archive := createExternalUIArchive(t, map[string]string{"ui/index.html": "new"})

	require.NoError(t, server.installExternalUI(bytes.NewReader(archive)))
	content, err := os.ReadFile(filepath.Join(output, "index.html"))
	require.NoError(t, err)
	require.Equal(t, "new", string(content))
	_, err = os.Stat(filepath.Join(output, "old.html"))
	require.ErrorIs(t, err, os.ErrNotExist)
}
