package clashapi

import (
	"archive/zip"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/filemanager"
)

const (
	maxExternalUIDownloadSize = 32 * 1024 * 1024
	maxExternalUIExtractSize  = 128 * 1024 * 1024
)

func (s *Server) checkAndDownloadExternalUI(update bool) error {
	if s.externalUI == "" {
		return nil
	}
	entries, err := filemanager.ReadDir(s.ctx, s.externalUI)
	if err != nil {
		filemanager.MkdirAll(s.ctx, s.externalUI, 0o755)
	}
	if len(entries) != 0 && s.lastUpdated.IsZero() {
		info, _ := os.Stat(s.externalUI)
		s.lastUpdated = info.ModTime()
	}
	if len(entries) == 0 || update {
		if len(entries) == 0 && s.lastEtag != "" {
			s.lastEtag = ""
		}
		err = s.downloadExternalUI()
		if err != nil {
			s.logger.Error("download external UI error: ", err)
			return err
		}
	}
	return nil
}

func (s *Server) downloadExternalUI() error {
	var downloadURL string
	if s.externalUIDownloadURL != "" {
		downloadURL = s.externalUIDownloadURL
	} else {
		downloadURL = "https://github.com/MetaCubeX/Yacd-meta/archive/gh-pages.zip"
	}
	transport, err := s.resolveExternalUITransport()
	if err != nil {
		return E.Cause(err, "create external UI http client")
	}
	httpClient := &http.Client{Transport: transport}
	defer httpClient.CloseIdleConnections()
	s.logger.Info("downloading external UI")
	request, err := http.NewRequest("GET", downloadURL, nil)
	if err != nil {
		return err
	}
	if s.lastEtag != "" {
		request.Header.Set("If-None-Match", s.lastEtag)
	}
	response, err := httpClient.Do(request.WithContext(s.ctx))
	if err != nil {
		return err
	}
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusNotModified:
		s.lastUpdated = time.Now()
		os.Chtimes(s.externalUI, s.lastUpdated, s.lastUpdated)
		if s.cacheFile != nil {
			if savedExternalUI := s.cacheFile.LoadExternalUI("ExternalUI"); savedExternalUI != nil {
				savedExternalUI.LastUpdated = s.lastUpdated
				err = s.cacheFile.SaveExternalUI("ExternalUI", savedExternalUI)
				if err != nil {
					s.logger.Error("save external UI updated time: ", err)
					return nil
				}
			}
		}
		s.logger.Info("update external UI: not modified")
		return nil
	default:
		return E.New("download external UI failed: ", response.Status)
	}
	defer response.Body.Close()
	removeAllInDirectory(s.ctx, s.externalUI)
	err = s.downloadZIP(response.Body, s.externalUI)
	if err != nil {
		removeAllInDirectory(s.ctx, s.externalUI)
		return err
	}
	eTagHeader := response.Header.Get("Etag")
	if eTagHeader != "" {
		s.lastEtag = eTagHeader
	}
	s.lastUpdated = time.Now()
	if s.cacheFile != nil {
		err = s.cacheFile.SaveExternalUI("ExternalUI", &adapter.SavedBinary{
			LastEtag:    s.lastEtag,
			LastUpdated: s.lastUpdated,
		})
		if err != nil {
			s.logger.Error("save external UI cache file: ", err)
		}
	}
	s.logger.Info("updated external UI")
	return nil
}

func (s *Server) resolveExternalUITransport() (adapter.HTTPTransport, error) {
	httpClientManager := service.FromContext[adapter.HTTPClientManager](s.ctx)
	contextLogger := s.logger.(log.ContextLogger)
	if s.externalUIHTTPClient != nil && !s.externalUIHTTPClient.IsEmpty() {
		if s.externalUIDownloadDetour != "" { //nolint:staticcheck
			return nil, E.New("external_ui_http_client is conflict with deprecated external_ui_download_detour field")
		}
		return httpClientManager.ResolveTransport(s.ctx, contextLogger, *s.externalUIHTTPClient)
	}
	if s.externalUIDownloadDetour != "" { //nolint:staticcheck
		return httpClientManager.ResolveTransport(s.ctx, contextLogger, option.HTTPClientOptions{
			DialerOptions: option.DialerOptions{
				Detour: s.externalUIDownloadDetour, //nolint:staticcheck
			},
			DisableEmptyDirectCheck: true,
		})
	}
	defaultTransport := httpClientManager.DefaultTransport()
	if defaultTransport == nil {
		return nil, E.New("default http client transport is not initialized")
	}
	return defaultTransport, nil
}

func (s *Server) downloadZIP(body io.Reader, output string) error {
	tempFile, err := filemanager.CreateTemp(s.ctx, "external-ui.zip")
	if err != nil {
		return err
	}
	defer filemanager.Remove(s.ctx, tempFile.Name())
	written, err := io.Copy(tempFile, io.LimitReader(body, maxExternalUIDownloadSize+1))
	closeErr := tempFile.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if written > maxExternalUIDownloadSize {
		return E.New("external UI archive exceeds maximum size")
	}
	reader, err := zip.OpenReader(tempFile.Name())
	if err != nil {
		return err
	}
	defer reader.Close()
	trimDir := zipIsInSingleDirectory(reader.File)
	var extractedSize uint64
	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		if file.FileInfo().Mode()&os.ModeSymlink != 0 {
			return E.New("external UI archive contains symlink: ", file.Name)
		}
		if file.UncompressedSize64 > maxExternalUIExtractSize-extractedSize {
			return E.New("external UI archive exceeds maximum extracted size")
		}
		extractedSize += file.UncompressedSize64
		pathElements := strings.Split(file.Name, "/")
		if trimDir {
			pathElements = pathElements[1:]
		}
		if len(pathElements) == 0 {
			return E.New("external UI archive contains empty path")
		}
		relativePath := filepath.Clean(filepath.FromSlash(strings.Join(pathElements, "/")))
		if relativePath == "." || filepath.IsAbs(relativePath) || filepath.VolumeName(relativePath) != "" || relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
			return E.New("external UI archive contains unsafe path: ", file.Name)
		}
		savePath := filepath.Join(output, relativePath)
		containedPath, relErr := filepath.Rel(output, savePath)
		if relErr != nil || containedPath == ".." || strings.HasPrefix(containedPath, ".."+string(filepath.Separator)) {
			return E.New("external UI archive path escapes output directory: ", file.Name)
		}
		saveDirectory := filepath.Dir(savePath)
		err = filemanager.MkdirAll(s.ctx, saveDirectory, 0o755)
		if err != nil {
			return err
		}
		err = downloadZIPEntry(s.ctx, file, savePath)
		if err != nil {
			return err
		}
	}
	return nil
}

func downloadZIPEntry(ctx context.Context, zipFile *zip.File, savePath string) error {
	saveFile, err := filemanager.Create(ctx, savePath)
	if err != nil {
		return err
	}
	defer saveFile.Close()
	reader, err := zipFile.Open()
	if err != nil {
		return err
	}
	defer reader.Close()
	return common.Error(io.Copy(saveFile, reader))
}

func removeAllInDirectory(ctx context.Context, directory string) {
	dirEntries, err := filemanager.ReadDir(ctx, directory)
	if err != nil {
		return
	}
	for _, dirEntry := range dirEntries {
		filemanager.RemoveAll(ctx, filepath.Join(directory, dirEntry.Name()))
	}
}

func zipIsInSingleDirectory(files []*zip.File) bool {
	var singleDirectory string
	for _, file := range files {
		if file.FileInfo().IsDir() {
			continue
		}
		pathElements := strings.Split(file.Name, "/")
		if len(pathElements) == 0 {
			return false
		}
		if singleDirectory == "" {
			singleDirectory = pathElements[0]
		} else if singleDirectory != pathElements[0] {
			return false
		}
	}
	return true
}
