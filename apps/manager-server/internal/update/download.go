package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func DownloadBytes(ctx context.Context, client HTTPDoer, asset ReleaseAsset, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, errors.New("download limit must be positive")
	}
	res, err := downloadResponse(ctx, client, asset)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("download exceeded size limit")
	}
	if err := verifyAssetDigest(data, asset.Digest); err != nil {
		return nil, err
	}
	return data, nil
}

func DownloadFile(ctx context.Context, client HTTPDoer, asset ReleaseAsset, destination string, limit int64) (string, error) {
	res, err := downloadResponse(ctx, client, asset)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	if err := restrictPrivateFile(destination); err != nil {
		file.Close()
		return "", err
	}
	defer func() {
		file.Close()
	}()
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(res.Body, limit+1))
	if err != nil {
		return "", err
	}
	if written > limit {
		return "", errors.New("download exceeded size limit")
	}
	if asset.Size > 0 && written != asset.Size {
		return "", fmt.Errorf("download size mismatch: expected %d, received %d", asset.Size, written)
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if err := verifyDigestString(digest, asset.Digest); err != nil {
		return "", err
	}
	return digest, nil
}

func downloadResponse(ctx context.Context, client HTTPDoer, asset ReleaseAsset) (*http.Response, error) {
	if !trustedAssetURL(asset.BrowserDownloadURL) {
		return nil, errors.New("release asset URL is not trusted")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.BrowserDownloadURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "CPA-Manager-Plus")
	if client == nil {
		client = trustedHTTPClient(10 * time.Minute)
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		res.Body.Close()
		return nil, fmt.Errorf("download release asset: %s", res.Status)
	}
	return res, nil
}

func verifyAssetDigest(data []byte, expected string) error {
	hash := sha256.Sum256(data)
	return verifyDigestString(hex.EncodeToString(hash[:]), expected)
}

func verifyDigestString(actual, expected string) error {
	expected = strings.TrimSpace(strings.TrimPrefix(expected, "sha256:"))
	if !validSHA256(expected) {
		return errors.New("release asset has no valid SHA-256 digest")
	}
	if !strings.EqualFold(actual, expected) {
		return errors.New("release asset SHA-256 digest mismatch")
	}
	return nil
}
