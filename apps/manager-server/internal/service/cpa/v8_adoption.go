package cpa

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"time"
)

// VerifyV8Adoption checks both persisted native keys and actual client auth on
// the new destination. It cannot open public Gateway routing or grant a profile.
// One fixed DNS/direct transport and a single five-second total budget cover
// readback, anonymous/invalid rejection and acceptance of every migrated key.
func VerifyV8Adoption(ctx context.Context, baseURL, managementKey string, keys []string) error {
	baseURL, err := NormalizeV8BaseURL(baseURL)
	if err != nil {
		return err
	}
	if len(keys) == 0 || len(keys) > 128 || managementKey == "" {
		return V8InvalidConfig
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	client, closeClient, err := directReadClient(ctx, baseURL)
	if err != nil {
		return err
	}
	defer closeClient()
	var actual []string
	if _, err = readV8ConfigWithClient(ctx, client, baseURL, managementKey, &actual); err != nil || !reflect.DeepEqual(keys, actual) {
		return V8InvalidConfig
	}
	for i, key := range append([]string{"", managementKey}, keys...) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v1/models", nil)
		if err != nil {
			return V8InvalidConnection
		}
		if key != "" {
			request.Header.Set("Authorization", "Bearer "+key)
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("Cache-Control", "no-store")
		response, err := client.Do(request)
		if err != nil {
			return V8ReadUnavailable
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, safeReadLimit+1))
		_ = response.Body.Close()
		if readErr != nil || len(data) > safeReadLimit {
			return V8InvalidConfig
		}
		if i < 2 {
			if response.StatusCode != http.StatusUnauthorized {
				return V8InvalidConfig
			}
			continue
		}
		var models struct {
			Object string            `json:"object"`
			Data   []json.RawMessage `json:"data"`
		}
		if response.StatusCode != http.StatusOK || json.Unmarshal(data, &models) != nil || models.Object != "list" || models.Data == nil {
			return V8InvalidConfig
		}
	}
	return nil
}
