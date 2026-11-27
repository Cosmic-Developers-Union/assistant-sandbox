package instance

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"testing"
)

type stubBackendInstance struct {
	closes   int
	closeErr error
}

func (s *stubBackendInstance) Close(context.Context) error {
	s.closes++
	return s.closeErr
}

type stubBackendFactory struct {
	kind     Backend
	instance BackendInstance
	err      error
	calls    int
	options  Options
}

func (s *stubBackendFactory) Backend() Backend { return s.kind }

func (s *stubBackendFactory) New(_ context.Context, opts Options) (BackendInstance, error) {
	s.calls++
	s.options = opts
	return s.instance, s.err
}

func TestBackendSelectionTable(t *testing.T) {
	full := BackendCapabilities{
		GOOS: "linux", GOARCH: "amd64",
		Namespaces: true, Seccomp: true, CgroupV2: true, CgroupDelegated: true,
	}
	missingCgroup := full
	missingCgroup.CgroupDelegated = false
	tests := []struct {
		name          string
		requested     Backend
		environment   backendEnvironment
		capabilities  BackendCapabilities
		probeErr      error
		nativeFactory bool
		want          Backend
		wantErr       error
		wantProbe     bool
	}{
		{
			name: "amd64 auto with capabilities", requested: BackendAuto,
			environment: backendEnvironment{"linux", "amd64"}, capabilities: full,
			nativeFactory: true, want: BackendNativeAMD64, wantProbe: true,
		},
		{
			name: "amd64 auto without cgroup delegation", requested: BackendAuto,
			environment: backendEnvironment{"linux", "amd64"}, capabilities: missingCgroup,
			nativeFactory: true, want: BackendRISC64, wantProbe: true,
		},
		{
			name: "amd64 auto probe error", requested: BackendAuto,
			environment: backendEnvironment{"linux", "amd64"}, capabilities: full,
			probeErr: errors.New("probe failed"), nativeFactory: true,
			want: BackendRISC64, wantProbe: true,
		},
		{
			name: "other architecture auto", requested: BackendAuto,
			environment:   backendEnvironment{"linux", "arm64"},
			nativeFactory: true, want: BackendRISC64,
		},
		{
			name: "explicit riscv", requested: BackendRISC64,
			environment: backendEnvironment{"linux", "amd64"}, capabilities: full,
			nativeFactory: true, want: BackendRISC64,
		},
		{
			name: "explicit native missing capability", requested: BackendNativeAMD64,
			environment: backendEnvironment{"linux", "amd64"}, capabilities: missingCgroup,
			nativeFactory: true, wantErr: ErrNativeCapability, wantProbe: true,
		},
		{
			name: "explicit native other architecture", requested: BackendNativeAMD64,
			environment:   backendEnvironment{"linux", "arm64"},
			nativeFactory: true, wantErr: ErrNativeArchitecture,
		},
		{
			name: "explicit native unregistered", requested: BackendNativeAMD64,
			environment: backendEnvironment{"linux", "amd64"}, capabilities: full,
			wantErr: ErrBackendUnavailable,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			riscv := &stubBackendFactory{kind: BackendRISC64, instance: new(stubBackendInstance)}
			native := &stubBackendFactory{kind: BackendNativeAMD64, instance: new(stubBackendInstance)}
			factories := map[Backend]BackendFactory{BackendRISC64: riscv}
			if test.nativeFactory {
				factories[BackendNativeAMD64] = native
			}
			probeCalls := 0
			probe := func(context.Context) (BackendCapabilities, error) {
				probeCalls++
				return test.capabilities, test.probeErr
			}
			selection, err := selectBackend(t.Context(), Options{Backend: test.requested}, probe, test.environment, factories)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("error=%v want %v", err, test.wantErr)
				}
			} else if err != nil {
				t.Fatal(err)
			} else if selection.Backend != test.want {
				t.Fatalf("backend=%s want %s", selection.Backend, test.want)
			}
			if (probeCalls > 0) != test.wantProbe {
				t.Fatalf("probe calls=%d want=%t", probeCalls, test.wantProbe)
			}
			if test.want == BackendRISC64 && riscv.options.Backend != BackendRISC64 {
				t.Fatalf("factory backend=%q", riscv.options.Backend)
			}
			if test.want == BackendNativeAMD64 && native.options.Backend != BackendNativeAMD64 {
				t.Fatalf("factory backend=%q", native.options.Backend)
			}
		})
	}
}

func TestBackendCreationFailureClosesPartialInstance(t *testing.T) {
	createErr := errors.New("create failed")
	partial := new(stubBackendInstance)
	factory := &stubBackendFactory{kind: BackendRISC64, instance: partial, err: createErr}
	_, err := selectBackend(t.Context(), Options{Backend: BackendRISC64}, nil,
		backendEnvironment{"linux", "amd64"}, map[Backend]BackendFactory{BackendRISC64: factory})
	if !errors.Is(err, createErr) {
		t.Fatalf("error=%v want create error", err)
	}
	if partial.closes != 1 {
		t.Fatalf("partial instance closes=%d want 1", partial.closes)
	}
}

func TestNativeBackendProbe(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("native probe requires linux/amd64")
	}
	capabilities, err := probeNativeBackend(t.Context())
	if err != nil {
		// CI 或受限宿主可以缺少能力；选择层会把它转换为具名错误。
		t.Logf("native capabilities unavailable: %v", err)
		return
	}
	if !capabilities.available() {
		t.Fatalf("successful probe returned unavailable capabilities: %+v", capabilities)
	}
}

func TestOpenSelectsAvailableBackend(t *testing.T) {
	selection, err := Open(t.Context(), Options{Deterministic: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = selection.Close(t.Context()) }()
	switch selection.Backend {
	case BackendRISC64:
		if _, ok := selection.Instance.(*VM); !ok {
			t.Fatalf("instance type=%T want *instance.VM", selection.Instance)
		}
	case BackendNativeAMD64:
		if fmt.Sprintf("%T", selection.Instance) != "*instance.NativeVM" {
			t.Fatalf("instance type=%T want *instance.NativeVM", selection.Instance)
		}
	default:
		t.Fatalf("unexpected backend %s", selection.Backend)
	}
}
