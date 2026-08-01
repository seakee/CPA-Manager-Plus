package cpaauthfiles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
)

const (
	DefaultTimeout                  = 30 * time.Second
	defaultMaxAuthFilesResponseSize = 64 * 1024 * 1024
	maxActionResponseSize           = 4 * 1024 * 1024
)

var ErrAuthFileNotFound = errors.New("CPA auth file not found")

var ErrIdentityMismatch = errors.New("CPA auth file identity mismatch")

var ErrStatusMutationScopeAmbiguous = errors.New("CPA auth file status mutation scope is ambiguous")

var ErrDeleteMutationScopeAmbiguous = errors.New("CPA auth file delete mutation scope is ambiguous")

var ErrResponseTooLarge = errors.New("CPA response too large")

const cpaPluginVirtualMutationConflict = "plugin virtual auth cannot be modified directly; edit or delete the source auth file"

type actionHTTPError struct {
	method     string
	path       string
	statusCode int
	body       string
}

func (e *actionHTTPError) Error() string {
	if e == nil {
		return "CPA action failed"
	}
	return fmt.Sprintf("%s %s: HTTP %d %s", e.method, e.path, e.statusCode, strings.TrimSpace(e.body))
}

type Client struct {
	httpClient       *http.Client
	timeout          time.Duration
	maxResponseBytes int64
}

type File struct {
	ID              string
	Name            string
	AuthIndex       string
	Provider        string
	AccountSnapshot string
	AccountID       string
	Disabled        bool
	Raw             map[string]any
}

type StatusMutationTarget struct {
	Selector      string
	File          File
	Scope         StatusMutationScope
	AffectedFiles []File
}

type DeleteMutationTarget struct {
	Selector      string
	File          File
	AffectedFiles []File
}

type StatusMutationScope string

const (
	StatusMutationScopeCredential StatusMutationScope = "credential"
	StatusMutationScopeSourceFile StatusMutationScope = "source-file"
)

type Identity struct {
	AuthFileName      string
	RuntimeID         string
	AuthIndex         string
	Provider          string
	AccountSnapshot   string
	AccountIDSnapshot string
}

func New(client *http.Client, timeout ...time.Duration) *Client {
	if client == nil {
		client = http.DefaultClient
	}
	d := DefaultTimeout
	if len(timeout) > 0 && timeout[0] > 0 {
		d = timeout[0]
	}
	return &Client{
		httpClient:       client,
		timeout:          d,
		maxResponseBytes: defaultMaxAuthFilesResponseSize,
	}
}

const authFilesPath = "/v0/management/auth-files"
const authFilesStatusPath = "/v0/management/auth-files/status"
const authFilesDownloadPath = "/v0/management/auth-files/download"

func authFilesEndpoint(baseURL string, fileName string, authIndex string) string {
	endpoint := baseURL + authFilesPath
	query := url.Values{}
	if fileName = strings.TrimSpace(fileName); fileName != "" {
		query.Set("name", fileName)
	}
	if authIndex = strings.TrimSpace(authIndex); authIndex != "" {
		query.Set("auth_index", authIndex)
	}
	if encoded := query.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}
	return endpoint
}

func authFileDownloadEndpoint(baseURL string, fileName string) string {
	query := url.Values{}
	query.Set("name", strings.TrimSpace(fileName))
	return baseURL + authFilesDownloadPath + "?" + query.Encode()
}

func (c *Client) Download(ctx context.Context, baseURL string, managementKey string, fileName string) ([]byte, error) {
	fileName = strings.TrimSpace(fileName)
	if fileName == "" {
		return nil, fmt.Errorf("%w: download selector is empty", ErrAuthFileNotFound)
	}
	base := cpa.NormalizeBaseURL(baseURL)
	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(
		reqCtx,
		http.MethodGet,
		authFileDownloadEndpoint(base, fileName),
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", authFilesDownloadPath, err)
	}
	req.Header.Set("Authorization", "Bearer "+managementKey)
	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", authFilesDownloadPath, err)
	}
	defer res.Body.Close()
	if res.StatusCode < http.StatusOK || res.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		if res.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf(
				"%w: GET %s: HTTP %d %s",
				ErrAuthFileNotFound,
				authFilesDownloadPath,
				res.StatusCode,
				strings.TrimSpace(string(body)),
			)
		}
		return nil, fmt.Errorf(
			"GET %s: HTTP %d %s",
			authFilesDownloadPath,
			res.StatusCode,
			strings.TrimSpace(string(body)),
		)
	}
	limit := c.maxResponseBytes
	if limit <= 0 {
		limit = defaultMaxAuthFilesResponseSize
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", authFilesDownloadPath, err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf(
			"GET %s: %w",
			authFilesDownloadPath,
			responseTooLargeError("auth-file download", limit),
		)
	}
	return body, nil
}

func (c *Client) Fetch(ctx context.Context, baseURL string, managementKey string) ([]File, error) {
	files := make([]File, 0)
	if err := c.Visit(ctx, baseURL, managementKey, func(file File) (bool, error) {
		files = append(files, file)
		return false, nil
	}); err != nil {
		return nil, err
	}
	return files, nil
}

func (c *Client) Visit(ctx context.Context, baseURL string, managementKey string, visit func(File) (bool, error)) error {
	return c.visit(ctx, baseURL, managementKey, "", "", visit)
}

func (c *Client) visit(
	ctx context.Context,
	baseURL string,
	managementKey string,
	fileName string,
	authIndex string,
	visit func(File) (bool, error),
) error {
	base := cpa.NormalizeBaseURL(baseURL)
	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, authFilesEndpoint(base, fileName, authIndex), nil)
	if err != nil {
		return fmt.Errorf("GET %s: %w", authFilesPath, err)
	}
	req.Header.Set("Authorization", "Bearer "+managementKey)
	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", authFilesPath, err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return fmt.Errorf("GET %s: HTTP %d %s", authFilesPath, res.StatusCode, strings.TrimSpace(string(body)))
	}
	if visit == nil {
		return errors.New("CPA auth file visitor is required")
	}
	body, limit := c.limitedAuthFilesResponse(res.Body)
	if err := scanFiles(body, visit); err != nil {
		if body.N == 0 {
			return fmt.Errorf("GET %s: %w", authFilesPath, responseTooLargeError("auth-files response", limit))
		}
		return fmt.Errorf("GET %s: %w", authFilesPath, err)
	}
	if body.N == 0 {
		return fmt.Errorf("GET %s: %w", authFilesPath, responseTooLargeError("auth-files response", limit))
	}
	return nil
}

func (c *Client) Find(ctx context.Context, baseURL string, managementKey string, fileName string, authIndex string) (File, bool, error) {
	var matched File
	found := false
	err := c.visit(ctx, baseURL, managementKey, fileName, authIndex, func(file File) (bool, error) {
		if !matches(file, fileName, authIndex) {
			return false, nil
		}
		matched = file
		found = true
		return true, nil
	})
	if err != nil {
		return File{}, false, err
	}
	return matched, found, nil
}

func (c *Client) limitedAuthFilesResponse(body io.Reader) (*io.LimitedReader, int64) {
	limit := c.maxResponseBytes
	if limit <= 0 {
		limit = defaultMaxAuthFilesResponseSize
	}
	return &io.LimitedReader{R: body, N: limit + 1}, limit
}

func responseTooLargeError(label string, limit int64) error {
	return fmt.Errorf("%w: %s exceeds %d bytes", ErrResponseTooLarge, label, limit)
}

func (c *Client) Verify(ctx context.Context, baseURL string, managementKey string, identity Identity) (File, error) {
	file, ok, err := c.Find(ctx, baseURL, managementKey, identity.AuthFileName, identity.AuthIndex)
	if err != nil {
		return File{}, err
	}
	if !ok {
		return File{}, ErrAuthFileNotFound
	}
	if err := verifyFileIdentity(file, identity); err != nil {
		return File{}, err
	}
	return file, nil
}

func (c *Client) ResolveVerifiedStatusMutationTarget(
	ctx context.Context,
	baseURL string,
	managementKey string,
	identity Identity,
) (StatusMutationTarget, error) {
	target, err := c.ResolveStatusMutationTarget(
		ctx,
		baseURL,
		managementKey,
		identity.AuthFileName,
		identity.AuthIndex,
	)
	if err == nil {
		if identityErr := verifyFileIdentity(target.File, identity); identityErr != nil {
			return StatusMutationTarget{}, identityErr
		}
		return target, nil
	} else if !errors.Is(err, ErrAuthFileNotFound) && !errors.Is(err, ErrStatusMutationScopeAmbiguous) {
		return StatusMutationTarget{}, err
	}

	files, fetchErr := c.fetchStatusMutationCandidates(
		ctx,
		baseURL,
		managementKey,
		strings.TrimSpace(identity.AuthFileName),
	)
	if fetchErr != nil {
		return StatusMutationTarget{}, fetchErr
	}
	candidates := statusMutationMatches(
		files,
		strings.TrimSpace(identity.AuthFileName),
		strings.TrimSpace(identity.AuthIndex),
	)
	verified := make([]File, 0, 1)
	var identityErr error
	for _, candidate := range candidates {
		if candidateErr := verifyFileIdentity(candidate, identity); candidateErr != nil {
			if identityErr == nil {
				identityErr = candidateErr
			}
			continue
		}
		verified = append(verified, candidate)
	}
	if len(verified) == 0 {
		if len(candidates) == 1 && identityErr != nil {
			return StatusMutationTarget{}, identityErr
		}
		return StatusMutationTarget{}, fmt.Errorf(
			"%w: selector %q auth_index %q credential identity changed",
			ErrIdentityMismatch,
			strings.TrimSpace(identity.AuthFileName),
			strings.TrimSpace(identity.AuthIndex),
		)
	}
	if len(verified) > 1 {
		return StatusMutationTarget{}, fmt.Errorf(
			"%w: selector %q auth_index %q identity maps to multiple credentials",
			ErrStatusMutationScopeAmbiguous,
			strings.TrimSpace(identity.AuthFileName),
			strings.TrimSpace(identity.AuthIndex),
		)
	}

	matched := verified[0]
	runtimeID := strings.TrimSpace(matched.ID)
	if runtimeID == "" {
		return StatusMutationTarget{}, fmt.Errorf(
			"%w: %q has no runtime id",
			ErrStatusMutationScopeAmbiguous,
			strings.TrimSpace(matched.Name),
		)
	}
	target, err = c.ResolveStatusMutationTarget(
		ctx,
		baseURL,
		managementKey,
		runtimeID,
		strings.TrimSpace(matched.AuthIndex),
	)
	if err != nil {
		return StatusMutationTarget{}, err
	}
	if strings.TrimSpace(target.File.ID) != runtimeID {
		return StatusMutationTarget{}, fmt.Errorf(
			"%w: runtime target changed (expected %q, got %q)",
			ErrIdentityMismatch,
			runtimeID,
			strings.TrimSpace(target.File.ID),
		)
	}
	if err := verifyFileIdentity(target.File, identity); err != nil {
		return StatusMutationTarget{}, err
	}
	return target, nil
}

func (c *Client) ResolveVerifiedDeleteMutationTarget(
	ctx context.Context,
	baseURL string,
	managementKey string,
	identity Identity,
) (DeleteMutationTarget, error) {
	return c.resolveVerifiedPhysicalFileDeleteTarget(ctx, baseURL, managementKey, []Identity{identity}, false)
}

func (c *Client) ResolveVerifiedPhysicalFileDeleteTarget(
	ctx context.Context,
	baseURL string,
	managementKey string,
	identities []Identity,
) (DeleteMutationTarget, error) {
	return c.resolveVerifiedPhysicalFileDeleteTarget(ctx, baseURL, managementKey, identities, true)
}

func (c *Client) resolveVerifiedPhysicalFileDeleteTarget(
	ctx context.Context,
	baseURL string,
	managementKey string,
	identities []Identity,
	allowShared bool,
) (DeleteMutationTarget, error) {
	if len(identities) == 0 {
		return DeleteMutationTarget{}, fmt.Errorf("%w: delete identity is required", ErrAuthFileNotFound)
	}
	physicalName := strings.TrimSpace(identities[0].AuthFileName)
	if physicalName == "" {
		return DeleteMutationTarget{}, fmt.Errorf("%w: delete selector is empty", ErrAuthFileNotFound)
	}
	for _, identity := range identities[1:] {
		if strings.TrimSpace(identity.AuthFileName) != physicalName {
			return DeleteMutationTarget{}, fmt.Errorf("%w: delete identities span multiple physical files", ErrDeleteMutationScopeAmbiguous)
		}
	}

	files, err := c.fetchStatusMutationCandidates(ctx, baseURL, managementKey, physicalName)
	if err != nil {
		return DeleteMutationTarget{}, err
	}
	members := statusMutationFilesByPhysicalName(files, physicalName)
	if len(members) == 0 {
		return DeleteMutationTarget{}, fmt.Errorf("%w: physical file %q", ErrAuthFileNotFound, physicalName)
	}
	if allowShared && statusMutationPhysicalSelectorCollides(files, physicalName) {
		return DeleteMutationTarget{}, fmt.Errorf("%w: physical file %q collides with another runtime id", ErrDeleteMutationScopeAmbiguous, physicalName)
	}
	if !allowShared && len(members) != 1 {
		return DeleteMutationTarget{}, fmt.Errorf("%w: physical file %q contains %d credentials", ErrDeleteMutationScopeAmbiguous, physicalName, len(members))
	}
	if len(members) != len(identities) {
		return DeleteMutationTarget{}, fmt.Errorf("%w: physical file %q membership changed (%d current, %d expected)", ErrDeleteMutationScopeAmbiguous, physicalName, len(members), len(identities))
	}

	matched := make([]File, 0, len(identities))
	used := make([]bool, len(members))
	for _, identity := range identities {
		locatorMatches := make([]int, 0, 1)
		verifiedMatches := make([]int, 0, 1)
		var identityErr error
		for index, member := range members {
			if used[index] || (strings.TrimSpace(identity.AuthIndex) != "" && strings.TrimSpace(member.AuthIndex) != strings.TrimSpace(identity.AuthIndex)) {
				continue
			}
			locatorMatches = append(locatorMatches, index)
			if err := verifyFileIdentity(member, identity); err != nil {
				if identityErr == nil {
					identityErr = err
				}
				continue
			}
			verifiedMatches = append(verifiedMatches, index)
		}
		if len(verifiedMatches) == 0 {
			if len(locatorMatches) == 1 && identityErr != nil {
				return DeleteMutationTarget{}, identityErr
			}
			return DeleteMutationTarget{}, fmt.Errorf("%w: physical file %q credential identity changed", ErrIdentityMismatch, physicalName)
		}
		if len(verifiedMatches) > 1 {
			return DeleteMutationTarget{}, fmt.Errorf("%w: physical file %q identity maps to multiple credentials", ErrDeleteMutationScopeAmbiguous, physicalName)
		}
		matchIndex := verifiedMatches[0]
		used[matchIndex] = true
		matched = append(matched, members[matchIndex])
	}

	target := matched[0]
	if len(members) > 1 {
		sourceRows := statusMutationSourceRows(members, physicalName)
		if len(sourceRows) > 1 {
			return DeleteMutationTarget{}, fmt.Errorf("%w: physical file %q has multiple source runtime ids", ErrDeleteMutationScopeAmbiguous, physicalName)
		}
		if len(sourceRows) == 1 {
			target = sourceRows[0]
		}
		// CPA deletes plugin-expanded credentials by their shared physical source
		// file name. A plugin is not required to expose a synthetic runtime row
		// whose ID equals that file name, so a child runtime ID is not a valid
		// fallback selector for a verified multi-credential delete.
		return DeleteMutationTarget{Selector: physicalName, File: target, AffectedFiles: members}, nil
	}
	runtimeID := strings.TrimSpace(target.ID)
	if runtimeID == "" {
		return DeleteMutationTarget{}, fmt.Errorf("%w: physical file %q has no runtime id", ErrDeleteMutationScopeAmbiguous, physicalName)
	}
	return DeleteMutationTarget{Selector: runtimeID, File: target, AffectedFiles: members}, nil
}

func (c *Client) EnsureStatusMutationScope(ctx context.Context, baseURL string, managementKey string, fileName string) error {
	_, err := c.ResolveStatusMutationTarget(ctx, baseURL, managementKey, fileName, "")
	return err
}

func (c *Client) ResolveStatusMutationTarget(
	ctx context.Context,
	baseURL string,
	managementKey string,
	selector string,
	authIndex string,
) (StatusMutationTarget, error) {
	selector = strings.TrimSpace(selector)
	authIndex = strings.TrimSpace(authIndex)
	if selector == "" {
		return StatusMutationTarget{}, fmt.Errorf("%w: status selector is empty", ErrAuthFileNotFound)
	}

	files, err := c.fetchStatusMutationCandidates(ctx, baseURL, managementKey, selector)
	if err != nil {
		return StatusMutationTarget{}, err
	}
	candidates := statusMutationMatches(files, selector, authIndex)
	if len(candidates) == 0 {
		return StatusMutationTarget{}, fmt.Errorf("%w: selector %q auth_index %q", ErrAuthFileNotFound, selector, authIndex)
	}
	if len(candidates) > 1 {
		return StatusMutationTarget{}, fmt.Errorf("%w: selector %q auth_index %q maps to multiple credentials", ErrStatusMutationScopeAmbiguous, selector, authIndex)
	}

	target := candidates[0]
	runtimeID := strings.TrimSpace(target.ID)
	physicalName := strings.TrimSpace(target.Name)
	if runtimeID == "" {
		return StatusMutationTarget{}, fmt.Errorf("%w: %q has no runtime id", ErrStatusMutationScopeAmbiguous, physicalName)
	}
	if physicalName != selector && !statusMutationResponseLooksUnfiltered(files, selector) {
		siblings, fetchErr := c.fetchStatusMutationCandidates(ctx, baseURL, managementKey, physicalName)
		if fetchErr != nil {
			return StatusMutationTarget{}, fetchErr
		}
		files = mergeStatusMutationFiles(files, siblings)
		candidates = statusMutationMatches(files, selector, authIndex)
		if len(candidates) == 0 {
			return StatusMutationTarget{}, fmt.Errorf("%w: selector %q auth_index %q", ErrAuthFileNotFound, selector, authIndex)
		}
		if len(candidates) > 1 {
			return StatusMutationTarget{}, fmt.Errorf("%w: selector %q auth_index %q maps to multiple credentials", ErrStatusMutationScopeAmbiguous, selector, authIndex)
		}
		target = candidates[0]
		runtimeID = strings.TrimSpace(target.ID)
		physicalName = strings.TrimSpace(target.Name)
	}

	affectedFiles := statusMutationFilesByPhysicalName(files, physicalName)
	scope := StatusMutationScopeCredential
	if len(affectedFiles) > 1 {
		sourceCount := 0
		for _, file := range affectedFiles {
			if strings.TrimSpace(file.ID) == physicalName {
				sourceCount++
			}
		}
		if sourceCount > 1 {
			return StatusMutationTarget{}, fmt.Errorf("%w: physical file %q has multiple source runtime ids", ErrStatusMutationScopeAmbiguous, physicalName)
		}
		if sourceCount == 1 {
			if runtimeID != physicalName {
				return StatusMutationTarget{}, fmt.Errorf("%w: runtime id %q is an expanded child of source file %q", ErrStatusMutationScopeAmbiguous, runtimeID, physicalName)
			}
			scope = StatusMutationScopeSourceFile
		}
	}
	if scope == StatusMutationScopeCredential {
		affectedFiles = []File{target}
	}

	return StatusMutationTarget{
		Selector:      runtimeID,
		File:          target,
		Scope:         scope,
		AffectedFiles: affectedFiles,
	}, nil
}

// ResolveSourceFileStatusMutationTarget resolves an explicitly requested
// physical-file status mutation. This path is reserved for callers that have
// already received CPA's plugin-virtual conflict for an exact runtime target;
// ordinary same-name credentials must continue to use credential scope.
func (c *Client) ResolveSourceFileStatusMutationTarget(
	ctx context.Context,
	baseURL string,
	managementKey string,
	physicalName string,
	authIndex string,
) (StatusMutationTarget, error) {
	return c.resolveSourceFileStatusMutationTarget(ctx, baseURL, managementKey, physicalName, authIndex, nil)
}

func (c *Client) ResolveVerifiedSourceFileStatusMutationTarget(
	ctx context.Context,
	baseURL string,
	managementKey string,
	physicalName string,
	authIndex string,
	expectedIdentities []Identity,
) (StatusMutationTarget, error) {
	if len(expectedIdentities) == 0 {
		return StatusMutationTarget{}, fmt.Errorf("%w: source-file member identities are required", ErrIdentityMismatch)
	}
	return c.resolveSourceFileStatusMutationTarget(ctx, baseURL, managementKey, physicalName, authIndex, expectedIdentities)
}

func (c *Client) resolveSourceFileStatusMutationTarget(
	ctx context.Context,
	baseURL string,
	managementKey string,
	physicalName string,
	authIndex string,
	expectedIdentities []Identity,
) (StatusMutationTarget, error) {
	physicalName = strings.TrimSpace(physicalName)
	authIndex = strings.TrimSpace(authIndex)
	if physicalName == "" {
		return StatusMutationTarget{}, fmt.Errorf("%w: source-file selector is empty", ErrAuthFileNotFound)
	}

	files, err := c.fetchStatusMutationCandidates(ctx, baseURL, managementKey, physicalName)
	if err != nil {
		return StatusMutationTarget{}, err
	}
	if statusMutationPhysicalSelectorCollides(files, physicalName) {
		return StatusMutationTarget{}, fmt.Errorf("%w: physical file %q collides with another runtime id", ErrStatusMutationScopeAmbiguous, physicalName)
	}
	affectedFiles := statusMutationFilesByPhysicalName(files, physicalName)
	if len(affectedFiles) == 0 {
		return StatusMutationTarget{}, fmt.Errorf("%w: physical file %q", ErrAuthFileNotFound, physicalName)
	}
	seenRuntimeIDs := make(map[string]struct{}, len(affectedFiles))
	for _, file := range affectedFiles {
		runtimeID := strings.TrimSpace(file.ID)
		if runtimeID == "" {
			return StatusMutationTarget{}, fmt.Errorf("%w: physical file %q contains a credential without a runtime id", ErrStatusMutationScopeAmbiguous, physicalName)
		}
		if _, exists := seenRuntimeIDs[runtimeID]; exists {
			return StatusMutationTarget{}, fmt.Errorf("%w: physical file %q contains duplicate runtime id %q", ErrStatusMutationScopeAmbiguous, physicalName, runtimeID)
		}
		seenRuntimeIDs[runtimeID] = struct{}{}
	}
	if expectedIdentities != nil {
		if err := verifySourceFileStatusMutationMembers(affectedFiles, physicalName, expectedIdentities); err != nil {
			return StatusMutationTarget{}, err
		}
	}
	candidates := statusMutationPhysicalNameMatches(files, physicalName, authIndex)
	if len(candidates) == 0 {
		return StatusMutationTarget{}, fmt.Errorf("%w: selector %q auth_index %q", ErrAuthFileNotFound, physicalName, authIndex)
	}
	if len(candidates) > 1 {
		return StatusMutationTarget{}, fmt.Errorf("%w: selector %q auth_index %q maps to multiple credentials", ErrStatusMutationScopeAmbiguous, physicalName, authIndex)
	}

	target := candidates[0]
	if strings.TrimSpace(target.Name) != physicalName {
		return StatusMutationTarget{}, fmt.Errorf(
			"%w: selector %q resolved to physical file %q",
			ErrStatusMutationScopeAmbiguous,
			physicalName,
			strings.TrimSpace(target.Name),
		)
	}
	if strings.TrimSpace(target.ID) == "" {
		return StatusMutationTarget{}, fmt.Errorf("%w: %q has no runtime id", ErrStatusMutationScopeAmbiguous, physicalName)
	}
	return StatusMutationTarget{
		Selector:      physicalName,
		File:          target,
		Scope:         StatusMutationScopeSourceFile,
		AffectedFiles: affectedFiles,
	}, nil
}

func verifySourceFileStatusMutationMembers(files []File, physicalName string, expected []Identity) error {
	if len(files) != len(expected) {
		return fmt.Errorf(
			"%w: physical file %q member count changed (expected %d, got %d)",
			ErrIdentityMismatch,
			physicalName,
			len(expected),
			len(files),
		)
	}
	used := make([]bool, len(files))
	for _, identity := range expected {
		if strings.TrimSpace(identity.AuthFileName) != physicalName {
			return fmt.Errorf("%w: source member belongs to a different physical file", ErrIdentityMismatch)
		}
		matches := make([]int, 0, 1)
		for index, file := range files {
			if used[index] || verifyFileIdentity(file, identity) != nil {
				continue
			}
			matches = append(matches, index)
		}
		if len(matches) == 0 {
			return fmt.Errorf("%w: physical file %q member identity changed", ErrIdentityMismatch, physicalName)
		}
		if len(matches) > 1 {
			return fmt.Errorf("%w: physical file %q member identity is ambiguous", ErrStatusMutationScopeAmbiguous, physicalName)
		}
		used[matches[0]] = true
	}
	return nil
}

func (c *Client) fetchStatusMutationCandidates(
	ctx context.Context,
	baseURL string,
	managementKey string,
	selector string,
) ([]File, error) {
	files := make([]File, 0, 1)
	if err := c.visit(ctx, baseURL, managementKey, selector, "", func(file File) (bool, error) {
		files = append(files, file)
		return false, nil
	}); err != nil {
		return nil, err
	}
	return files, nil
}

func statusMutationMatches(files []File, selector string, authIndex string) []File {
	idMatches := make([]File, 0, 1)
	nameMatches := make([]File, 0, 1)
	selectorMatchesRuntimeID := false
	for _, file := range files {
		if strings.TrimSpace(file.ID) == selector {
			selectorMatchesRuntimeID = true
			if authIndex == "" || strings.TrimSpace(file.AuthIndex) == authIndex {
				idMatches = append(idMatches, file)
			}
			continue
		}
		if strings.TrimSpace(file.Name) == selector &&
			(authIndex == "" || strings.TrimSpace(file.AuthIndex) == authIndex) {
			nameMatches = append(nameMatches, file)
		}
	}

	if selectorMatchesRuntimeID {
		return idMatches
	}
	return nameMatches
}

func statusMutationPhysicalNameMatches(files []File, physicalName string, authIndex string) []File {
	result := make([]File, 0, 1)
	for _, file := range files {
		if strings.TrimSpace(file.Name) == physicalName &&
			(authIndex == "" || strings.TrimSpace(file.AuthIndex) == authIndex) {
			result = append(result, file)
		}
	}
	return result
}

func statusMutationResponseLooksUnfiltered(files []File, selector string) bool {
	for _, file := range files {
		if strings.TrimSpace(file.ID) != selector && strings.TrimSpace(file.Name) != selector {
			return true
		}
	}
	return false
}

func mergeStatusMutationFiles(primary []File, additional []File) []File {
	result := append([]File(nil), primary...)
	seen := make(map[string]struct{}, len(primary))
	for _, file := range primary {
		seen[statusMutationFileKey(file)] = struct{}{}
	}
	for _, file := range additional {
		key := statusMutationFileKey(file)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, file)
	}
	return result
}

func statusMutationFileKey(file File) string {
	return strings.Join([]string{
		strings.TrimSpace(file.ID),
		strings.TrimSpace(file.Name),
		strings.TrimSpace(file.AuthIndex),
	}, "\x00")
}

func statusMutationFilesByPhysicalName(files []File, physicalName string) []File {
	result := make([]File, 0, 1)
	for _, file := range files {
		if strings.TrimSpace(file.Name) == physicalName {
			result = append(result, file)
		}
	}
	return result
}

func statusMutationSourceRows(files []File, physicalName string) []File {
	result := make([]File, 0, 1)
	for _, file := range files {
		if strings.TrimSpace(file.Name) == physicalName && strings.TrimSpace(file.ID) == physicalName {
			result = append(result, file)
		}
	}
	return result
}

func statusMutationPhysicalSelectorCollides(files []File, physicalName string) bool {
	for _, file := range files {
		if strings.TrimSpace(file.ID) == physicalName && strings.TrimSpace(file.Name) != physicalName {
			return true
		}
	}
	return false
}

func Parse(body []byte) ([]File, error) {
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	files := filesFromJSON(decoded)
	if files == nil {
		return []File{}, nil
	}
	return files, nil
}

func scanFiles(body io.Reader, visit func(File) (bool, error)) error {
	decoder := json.NewDecoder(body)
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return fmt.Errorf("expected JSON object or array")
	}
	switch delimiter {
	case '[':
		_, err := scanFileArray(decoder, visit)
		return err
	case '{':
		raw := make(map[string]any)
		scannedList := false
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("expected object key")
			}
			if !isAuthFilesListKey(key) {
				var value any
				if err := decoder.Decode(&value); err != nil {
					return err
				}
				raw[key] = value
				continue
			}
			valueToken, err := decoder.Token()
			if err != nil {
				return err
			}
			valueDelimiter, ok := valueToken.(json.Delim)
			if !ok || valueDelimiter != '[' {
				value, err := decodeValueAfterToken(decoder, valueToken)
				if err != nil {
					return err
				}
				raw[key] = value
				continue
			}
			scannedList = true
			stopped, err := scanFileArray(decoder, visit)
			if err != nil || stopped {
				return err
			}
		}
		if _, err := decoder.Token(); err != nil {
			return err
		}
		if !scannedList && stringField(raw, "name", "file_name", "fileName", "id") != "" {
			_, err := visit(FromMap(raw))
			return err
		}
		return nil
	default:
		return fmt.Errorf("expected JSON object or array")
	}
}

func decodeValueAfterToken(decoder *json.Decoder, token json.Token) (any, error) {
	delimiter, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delimiter {
	case '{':
		value := make(map[string]any)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, fmt.Errorf("expected object key")
			}
			var child any
			if err := decoder.Decode(&child); err != nil {
				return nil, err
			}
			value[key] = child
		}
		_, err := decoder.Token()
		return value, err
	case '[':
		value := make([]any, 0)
		for decoder.More() {
			var child any
			if err := decoder.Decode(&child); err != nil {
				return nil, err
			}
			value = append(value, child)
		}
		_, err := decoder.Token()
		return value, err
	default:
		return nil, fmt.Errorf("unexpected delimiter %q", delimiter)
	}
}

func scanFileArray(decoder *json.Decoder, visit func(File) (bool, error)) (bool, error) {
	for decoder.More() {
		var raw map[string]any
		if err := decoder.Decode(&raw); err != nil {
			return false, err
		}
		if len(raw) == 0 {
			continue
		}
		stop, err := visit(FromMap(raw))
		if err != nil || stop {
			return stop, err
		}
	}
	_, err := decoder.Token()
	return false, err
}

func isAuthFilesListKey(key string) bool {
	switch key {
	case "auth_files", "authFiles", "files", "items", "data":
		return true
	default:
		return false
	}
}

func Find(files []File, fileName string, authIndex string) (File, bool) {
	fileName = strings.TrimSpace(fileName)
	authIndex = strings.TrimSpace(authIndex)
	for _, file := range files {
		if matches(file, fileName, authIndex) {
			return file, true
		}
	}
	return File{}, false
}

func matches(file File, fileName string, authIndex string) bool {
	fileName = strings.TrimSpace(fileName)
	authIndex = strings.TrimSpace(authIndex)
	if fileName != "" && file.Name != fileName {
		return false
	}
	if authIndex != "" && file.AuthIndex != authIndex {
		return false
	}
	return true
}

func VerifyIdentity(files []File, identity Identity) (File, error) {
	file, ok := Find(files, identity.AuthFileName, identity.AuthIndex)
	if !ok {
		return File{}, ErrAuthFileNotFound
	}
	if err := verifyFileIdentity(file, identity); err != nil {
		return File{}, err
	}
	return file, nil
}

func VerifyResolvedIdentity(file File, identity Identity) error {
	return verifyFileIdentity(file, identity)
}

func verifyFileIdentity(file File, identity Identity) error {
	if identity.AuthFileName != "" && strings.TrimSpace(file.Name) != strings.TrimSpace(identity.AuthFileName) {
		return fmt.Errorf("%w: auth file name mismatch (expected %q, got %q)", ErrIdentityMismatch, strings.TrimSpace(identity.AuthFileName), strings.TrimSpace(file.Name))
	}
	if identity.RuntimeID != "" && strings.TrimSpace(file.ID) != strings.TrimSpace(identity.RuntimeID) {
		return fmt.Errorf("%w: runtime id mismatch (expected %q, got %q)", ErrIdentityMismatch, strings.TrimSpace(identity.RuntimeID), strings.TrimSpace(file.ID))
	}
	if identity.AuthIndex != "" && strings.TrimSpace(file.AuthIndex) != strings.TrimSpace(identity.AuthIndex) {
		return fmt.Errorf("%w: auth_index mismatch (expected %q, got %q)", ErrIdentityMismatch, strings.TrimSpace(identity.AuthIndex), strings.TrimSpace(file.AuthIndex))
	}
	if identity.AccountIDSnapshot != "" && file.AccountID != strings.TrimSpace(identity.AccountIDSnapshot) {
		return fmt.Errorf("%w: account_id mismatch (expected %q, got %q)", ErrIdentityMismatch, strings.TrimSpace(identity.AccountIDSnapshot), file.AccountID)
	}
	if identity.Provider != "" && normalizeAuthProvider(file.Provider) != normalizeAuthProvider(identity.Provider) {
		return fmt.Errorf("%w: provider mismatch (expected %q, got %q)", ErrIdentityMismatch, strings.TrimSpace(identity.Provider), file.Provider)
	}
	if identity.AccountIDSnapshot == "" && identity.AccountSnapshot != "" && file.AccountSnapshot != strings.TrimSpace(identity.AccountSnapshot) {
		return fmt.Errorf("%w: account_snapshot mismatch (expected %q, got %q)", ErrIdentityMismatch, strings.TrimSpace(identity.AccountSnapshot), file.AccountSnapshot)
	}
	return nil
}

func (c *Client) PatchDisabled(ctx context.Context, baseURL string, managementKey string, fileName string, disabled bool, authIndex ...string) error {
	return c.patchDisabled(ctx, baseURL, managementKey, fileName, disabled, false, authIndex...)
}

func (c *Client) PatchDisabledAllowSourceFile(
	ctx context.Context,
	baseURL string,
	managementKey string,
	fileName string,
	disabled bool,
	authIndex ...string,
) error {
	return c.patchDisabled(ctx, baseURL, managementKey, fileName, disabled, true, authIndex...)
}

func (c *Client) patchDisabled(
	ctx context.Context,
	baseURL string,
	managementKey string,
	fileName string,
	disabled bool,
	allowSourceFile bool,
	authIndex ...string,
) error {
	requestedAuthIndex := ""
	if len(authIndex) > 0 {
		requestedAuthIndex = strings.TrimSpace(authIndex[0])
	}
	target, err := c.ResolveStatusMutationTarget(ctx, baseURL, managementKey, fileName, requestedAuthIndex)
	if err != nil {
		return fmt.Errorf("PATCH %s preflight: %w", authFilesStatusPath, err)
	}
	return c.patchDisabledTarget(ctx, baseURL, managementKey, target, disabled, allowSourceFile)
}

func (c *Client) PatchDisabledTarget(
	ctx context.Context,
	baseURL string,
	managementKey string,
	target StatusMutationTarget,
	disabled bool,
) error {
	return c.patchDisabledTarget(ctx, baseURL, managementKey, target, disabled, false)
}

func (c *Client) PatchDisabledTargetAllowSourceFile(
	ctx context.Context,
	baseURL string,
	managementKey string,
	target StatusMutationTarget,
	disabled bool,
) error {
	return c.patchDisabledTarget(ctx, baseURL, managementKey, target, disabled, true)
}

func (c *Client) patchDisabledTarget(
	ctx context.Context,
	baseURL string,
	managementKey string,
	target StatusMutationTarget,
	disabled bool,
	allowSourceFile bool,
) error {
	if target.Scope == StatusMutationScopeSourceFile && !allowSourceFile {
		return fmt.Errorf("PATCH %s preflight: %w: source file %q affects multiple credentials", authFilesStatusPath, ErrStatusMutationScopeAmbiguous, target.File.Name)
	}
	selector := strings.TrimSpace(target.Selector)
	runtimeID := strings.TrimSpace(target.File.ID)
	physicalName := strings.TrimSpace(target.File.Name)
	if selector == "" || runtimeID == "" {
		return fmt.Errorf("PATCH %s preflight: %w: mutation target has no stable runtime id", authFilesStatusPath, ErrStatusMutationScopeAmbiguous)
	}
	if target.Scope == StatusMutationScopeSourceFile {
		if physicalName == "" || selector != physicalName || len(target.AffectedFiles) == 0 {
			return fmt.Errorf("PATCH %s preflight: %w: mutation target has no stable physical source", authFilesStatusPath, ErrStatusMutationScopeAmbiguous)
		}
	} else if selector != runtimeID {
		return fmt.Errorf("PATCH %s preflight: %w: mutation target has no stable runtime id", authFilesStatusPath, ErrStatusMutationScopeAmbiguous)
	}
	payload := map[string]any{"name": selector, "disabled": disabled}
	if resolvedAuthIndex := strings.TrimSpace(target.File.AuthIndex); resolvedAuthIndex != "" {
		payload["auth_index"] = resolvedAuthIndex
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	base := cpa.NormalizeBaseURL(baseURL)
	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPatch, base+authFilesStatusPath, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("PATCH %s: %w", authFilesStatusPath, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+managementKey)
	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("PATCH %s: %w", authFilesStatusPath, err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		if err := ValidateActionResponse(res.Body); err != nil {
			return fmt.Errorf("PATCH %s: %w", authFilesStatusPath, err)
		}
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	return &actionHTTPError{
		method:     http.MethodPatch,
		path:       authFilesStatusPath,
		statusCode: res.StatusCode,
		body:       strings.TrimSpace(string(body)),
	}
}

func (c *Client) DeleteVerifiedSingleCredential(
	ctx context.Context,
	baseURL string,
	managementKey string,
	identity Identity,
) error {
	return c.deleteVerified(ctx, baseURL, managementKey, []Identity{identity}, false)
}

func (c *Client) DeleteVerifiedPhysicalFile(
	ctx context.Context,
	baseURL string,
	managementKey string,
	identities []Identity,
) error {
	return c.deleteVerified(ctx, baseURL, managementKey, identities, true)
}

func (c *Client) deleteVerified(
	ctx context.Context,
	baseURL string,
	managementKey string,
	identities []Identity,
	allowShared bool,
) error {
	target, err := c.resolveVerifiedPhysicalFileDeleteTarget(ctx, baseURL, managementKey, identities, allowShared)
	if err != nil {
		return fmt.Errorf("DELETE %s preflight: %w", authFilesPath, err)
	}
	err = c.delete(ctx, baseURL, managementKey, target.Selector)
	if err == nil || !isPluginVirtualMutationConflict(err) {
		return err
	}

	// CPA rejects direct mutation of plugin-expanded runtime IDs. Revalidate the
	// complete physical membership after the conflict so a newly added sibling
	// cannot be deleted by a stale single-credential confirmation.
	target, resolveErr := c.resolveVerifiedPhysicalFileDeleteTarget(ctx, baseURL, managementKey, identities, allowShared)
	if resolveErr != nil {
		return fmt.Errorf("DELETE %s plugin source fallback preflight: %w", authFilesPath, resolveErr)
	}
	physicalName := strings.TrimSpace(target.File.Name)
	if physicalName == "" || physicalName == strings.TrimSpace(target.Selector) {
		return err
	}
	files, fetchErr := c.fetchStatusMutationCandidates(ctx, baseURL, managementKey, physicalName)
	if fetchErr != nil {
		return fmt.Errorf("DELETE %s plugin source fallback preflight: %w", authFilesPath, fetchErr)
	}
	if statusMutationPhysicalSelectorCollides(files, physicalName) {
		return fmt.Errorf("DELETE %s plugin source fallback preflight: %w: physical file %q collides with another runtime id", authFilesPath, ErrDeleteMutationScopeAmbiguous, physicalName)
	}
	return c.delete(ctx, baseURL, managementKey, physicalName)
}

func (c *Client) Delete(ctx context.Context, baseURL string, managementKey string, fileName string) error {
	return c.delete(ctx, baseURL, managementKey, fileName)
}

func (c *Client) delete(ctx context.Context, baseURL string, managementKey string, fileName string) error {
	base := cpa.NormalizeBaseURL(baseURL)
	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	endpoint := base + authFilesPath + "?name=" + url.QueryEscape(fileName)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return fmt.Errorf("DELETE %s: %w", authFilesPath, err)
	}
	req.Header.Set("Authorization", "Bearer "+managementKey)
	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("DELETE %s: %w", authFilesPath, err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		if err := ValidateActionResponse(res.Body); err != nil {
			return fmt.Errorf("DELETE %s: %w", authFilesPath, err)
		}
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	return &actionHTTPError{
		method:     http.MethodDelete,
		path:       authFilesPath,
		statusCode: res.StatusCode,
		body:       strings.TrimSpace(string(body)),
	}
}

func isPluginVirtualMutationConflict(err error) bool {
	var httpErr *actionHTTPError
	if !errors.As(err, &httpErr) || httpErr.statusCode != http.StatusConflict {
		return false
	}
	var payload struct {
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(httpErr.body), &payload) == nil && strings.TrimSpace(payload.Error) != "" {
		return strings.TrimSpace(payload.Error) == cpaPluginVirtualMutationConflict
	}
	return strings.TrimSpace(httpErr.body) == cpaPluginVirtualMutationConflict
}

func IsPluginVirtualMutationConflict(err error) bool {
	return isPluginVirtualMutationConflict(err)
}

func filesFromJSON(value any) []File {
	switch typed := value.(type) {
	case []any:
		files := make([]File, 0, len(typed))
		for _, item := range typed {
			if m, ok := item.(map[string]any); ok {
				files = append(files, FromMap(m))
			}
		}
		return files
	case map[string]any:
		for _, key := range []string{"auth_files", "authFiles", "files", "items", "data"} {
			if child, ok := typed[key]; ok {
				if files := filesFromJSON(child); files != nil {
					return files
				}
			}
		}
		if name := stringField(typed, "name", "file_name", "fileName", "id"); name != "" {
			return []File{FromMap(typed)}
		}
	}
	return nil
}

func FromMap(file map[string]any) File {
	return File{
		ID:              stringField(file, "id"),
		Name:            stringField(file, "name", "file_name", "fileName", "id"),
		AuthIndex:       stringField(file, "auth_index", "authIndex", "auth-index"),
		Provider:        normalizeAuthProvider(stringField(file, "provider", "type")),
		AccountSnapshot: stringField(file, "account", "email", "display_account", "displayAccount"),
		AccountID:       accountIDField(file),
		Disabled:        disabledField(file),
		Raw:             file,
	}
}

func normalizeAuthProvider(value string) string {
	provider := strings.ToLower(strings.TrimSpace(value))
	provider = strings.ReplaceAll(provider, "_", "-")
	switch provider {
	case "x-ai", "grok":
		return "xai"
	default:
		return provider
	}
}

var accountIDFieldNames = []string{
	"account_id", "accountId", "chatgpt_account_id", "chatgptAccountId",
	"project_id", "projectId", "gemini_virtual_project", "geminiVirtualProject",
	"sub",
}

func accountIDField(file map[string]any) string {
	if value := stringField(file, accountIDFieldNames...); value != "" {
		return value
	}
	for _, key := range []string{"id_token", "idToken", "metadata", "attributes"} {
		if value := accountIDFromValue(file[key]); value != "" {
			return value
		}
	}
	return ""
}

func accountIDFromValue(value any) string {
	child, ok := value.(map[string]any)
	if !ok || child == nil {
		return ""
	}
	if accountID := stringField(child, accountIDFieldNames...); accountID != "" {
		return accountID
	}
	for _, key := range []string{"id_token", "idToken"} {
		if accountID := accountIDFromValue(child[key]); accountID != "" {
			return accountID
		}
	}
	return ""
}

func disabledField(file map[string]any) bool {
	if raw, ok := file["disabled"]; ok {
		switch value := raw.(type) {
		case bool:
			return value
		case json.Number:
			parsed, _ := strconv.ParseFloat(value.String(), 64)
			return parsed != 0
		case float64:
			return value != 0
		case string:
			return strings.EqualFold(strings.TrimSpace(value), "true") || strings.TrimSpace(value) == "1"
		}
	}
	status := strings.ToLower(stringField(file, "status", "state"))
	return status == "disabled" || status == "inactive"
}

func stringField(file map[string]any, keys ...string) string {
	for _, key := range keys {
		if raw, ok := file[key]; ok && raw != nil {
			value := strings.TrimSpace(fmt.Sprint(raw))
			if value != "" && value != "<nil>" {
				return value
			}
		}
	}
	return ""
}

func ValidateActionResponse(body io.Reader) error {
	if body == nil {
		return nil
	}
	limited := &io.LimitedReader{R: body, N: maxActionResponseSize + 1}
	decoder := json.NewDecoder(limited)
	decoder.UseNumber()
	var payload any
	if err := decoder.Decode(&payload); err != nil {
		if limited.N == 0 {
			return responseTooLargeError("action response", maxActionResponseSize)
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		return fmt.Errorf("decode CPA action response: %w", err)
	}
	if limited.N == 0 {
		return responseTooLargeError("action response", maxActionResponseSize)
	}
	var trailing any
	trailingErr := decoder.Decode(&trailing)
	if limited.N == 0 {
		return responseTooLargeError("action response", maxActionResponseSize)
	}
	if !errors.Is(trailingErr, io.EOF) {
		if trailingErr == nil {
			return errors.New("decode CPA action response: multiple JSON values")
		}
		return fmt.Errorf("decode CPA action response trailing data: %w", trailingErr)
	}
	result, ok := payload.(map[string]any)
	if !ok {
		return errors.New("decode CPA action response: expected JSON object")
	}
	if failed, exists := result["failed"]; exists && hasActionFailureValue(failed) {
		return fmt.Errorf("CPA action failed: %s", actionFailureDetail(failed))
	}
	if actionErr, exists := result["error"]; exists && hasActionFailureValue(actionErr) {
		return fmt.Errorf("CPA action failed: %s", actionFailureDetail(actionErr))
	}
	if success, ok := result["success"].(bool); ok && !success {
		return errors.New("CPA action failed: success=false")
	}
	if okValue, ok := result["ok"].(bool); ok && !okValue {
		return errors.New("CPA action failed: ok=false")
	}
	status := strings.ToLower(strings.TrimSpace(fmt.Sprint(result["status"])))
	if status == "error" || status == "failed" || status == "partial" {
		return fmt.Errorf("CPA action failed: status=%s", status)
	}
	if success, ok := result["success"].(bool); ok && success {
		return nil
	}
	if okValue, ok := result["ok"].(bool); ok && okValue {
		return nil
	}
	if status == "ok" || status == "success" {
		return nil
	}
	return errors.New("CPA action response did not confirm success")
}

func hasActionFailureValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return strings.TrimSpace(typed) != ""
	case json.Number:
		parsed, err := typed.Float64()
		return err != nil || parsed != 0
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return strings.TrimSpace(fmt.Sprint(typed)) != ""
	}
}

func actionFailureDetail(value any) string {
	if values, ok := value.([]any); ok && len(values) > 0 {
		value = values[0]
	}
	detail := strings.TrimSpace(fmt.Sprint(value))
	if detail == "" {
		return "unknown failure"
	}
	return detail
}
