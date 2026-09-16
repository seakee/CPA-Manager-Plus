package runtime

import (
	"context"
	"reflect"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

type fakeRuntimeClient struct {
	status          model.RuntimeObservedStatus
	operationResult model.RuntimeOperationResult
	ctx             context.Context
}

func (f *fakeRuntimeClient) Status(ctx context.Context) (model.RuntimeObservedStatus, error) {
	f.ctx = ctx
	return f.status, nil
}

func (f *fakeRuntimeClient) Start(ctx context.Context, _ model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	f.ctx = ctx
	return f.operationResult, nil
}

func (f *fakeRuntimeClient) Stop(ctx context.Context, _ model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	f.ctx = ctx
	return f.operationResult, nil
}

func (f *fakeRuntimeClient) Restart(ctx context.Context, _ model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	f.ctx = ctx
	return f.operationResult, nil
}

var _ RuntimeClient = (*fakeRuntimeClient)(nil)

func TestRuntimeClientStatusContract(t *testing.T) {
	want := model.RuntimeObservedStatus{
		Identity:           "runtime-01",
		Generation:         3,
		ProtocolVersion:    "v1",
		State:              model.RuntimeStateReady,
		CPAObservedVersion: "v7.1.18",
	}
	client := &fakeRuntimeClient{status: want}
	ctx := context.WithValue(context.Background(), struct{}{}, "request")

	got, err := client.Status(ctx)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("status = %#v, want %#v", got, want)
	}
	if client.ctx != ctx {
		t.Fatal("Status did not receive the caller context")
	}

	contract := reflect.TypeOf((*RuntimeClient)(nil)).Elem()
	wantMethods := map[string]bool{"Restart": true, "Start": true, "Status": true, "Stop": true}
	if contract.NumMethod() != len(wantMethods) {
		t.Fatalf("RuntimeClient methods = %v, want %v", contract.NumMethod(), wantMethods)
	}
	for index := 0; index < contract.NumMethod(); index++ {
		delete(wantMethods, contract.Method(index).Name)
	}
	if len(wantMethods) != 0 {
		t.Fatalf("RuntimeClient missing methods: %v", wantMethods)
	}
}
