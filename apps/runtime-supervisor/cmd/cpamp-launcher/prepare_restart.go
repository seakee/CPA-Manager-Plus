//go:build unix

package main

import "context"

// supervisorPreparation reuses the complete preparation path before every
// Supervisor start. Pin the established layout and token across attempts;
// journal, selection and finalized artifact trust remain Supervisor checks.
func supervisorPreparation(prepared *PreparedRuntime, env map[string]string, sys System) func(context.Context, ChildSpec) (ChildSpec, error) {
	return supervisorPreparationWithReaper(prepared, env, nil, sys)
}

func supervisorPreparationWithReaper(prepared *PreparedRuntime, env map[string]string, reapChildren func(), sys System) func(context.Context, ChildSpec) (ChildSpec, error) {
	cfg := PrepareConfig{
		DataDir:                 prepared.DataDir,
		RuntimeRoot:             prepared.RuntimeRoot,
		TokenFile:               prepared.TokenFile,
		ExplicitRuntimeToken:    prepared.RuntimeToken,
		RuntimeAddr:             prepared.RuntimeAddr,
		RuntimeIdentity:         prepared.RuntimeIdentity,
		RuntimeCPAAddr:          prepared.RuntimeCPAAddr,
		CPAExecutablePath:       prepared.CPAExecutablePath,
		CPAArtifactManifestPath: prepared.CPAArtifactManifestPath,
		ReapChildren:            reapChildren,
	}
	return func(ctx context.Context, spec ChildSpec) (ChildSpec, error) {
		refreshed, err := prepareContext(ctx, cfg, sys)
		if err != nil {
			return spec, err
		}
		spec.Dir = refreshed.GatewayDir
		spec.Env = BuildSupervisorChildEnv(refreshed, env)
		return spec, nil
	}
}
