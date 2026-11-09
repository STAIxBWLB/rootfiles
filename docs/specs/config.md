---
id: config
type: module
title: Configuration
status: active
parent: architecture
paths:
  - internal/config/config.go
  - internal/config/loader.go
  - internal/config/validate.go
  - internal/config/fingerprint.go
  - internal/config/snapshot.go
  - internal/config/detector.go
  - internal/config/profiles/**
  - internal/cli/configcmd.go
---

# Configuration

## Purpose

The config schema (`Config` and its module structs), the embedded profiles and their inheritance, site config files, environment overrides, semantic validation, the config fingerprint that proves what was applied, system detection with profile suggestion and home-base detection, and the `rootfiles config show|validate|init` commands.

## Contracts and invariants

- **Profiles are merged as YAML trees** (`resolveEmbeddedTree`, `resolveFileTree`, `mergeTrees` in `internal/config/loader.go`) before decoding: a key present in the child overrides the parent even when false/0/"" (so a child can disable a module); nested maps merge key by key; lists replace, except `packages_extra`, which accumulates along the chain. Never merge decoded structs; zero values would be lost.
- **Strict keys**: each file is strict-decoded on its own (`parseTree`, so errors name that file and line) and the merged tree again (`decodeTree`). An unknown key is an error, never ignored.
- **extends**: a site config's `extends` names a built-in profile (`AvailableProfiles()`) or another file relative to the including file. Depth is capped at 5 (`maxExtendsDepth`); file cycles are rejected. `-` reads the config from stdin.
- **Built-in profiles** (`internal/config/profiles/*.yaml`, embedded with `go:embed`): `base`; `minimal` extends base; `dgx`, `gpu-server` and `full` extend minimal; `rocky` stands alone with RPM package names. `AvailableProfiles()` is the hard-coded list of names and must match the files. Embedded profiles carry no site-specific values (see #90).
- **Load order** (`LoadWithHomeBase`): resolve profile (empty name means `minimal`) or file; reject `tunnel_token` and `tunnel_token_file` together (before env overrides, so `ROOTFILES_TUNNEL_TOKEN` may still override a file); apply env overrides (`applyEnvOverrides`: `ROOTFILES_HOME_BASE`, `_TIMEZONE`, `_TUNNEL_TOKEN`, `_VLAN_ADDRESS`, `_VLAN_INTERFACE`, `_DOCKER_ROOT`, `_DATA_DIR`); apply `--home-base`; detect the home base only when still unset; `Validate`; attach `System`.
- **LoadSite** is the controller's loader for inventory site configs: no environment overrides, `users.home_base` must be explicit (a host-detected value would read as drift), and the token rules above. See [fleet](fleet.md).
- **Fallback** is the config for commands that can run without a profile: env overrides and `--home-base` only, relative paths dropped, `Users.HomeBase` empty when unknown.
- **Validate** (`internal/config/validate.go`) collects every semantic error in one pass (ports, absolute paths, account and group names, sysctl keys, sizes, CIDRs, interface names, tenant root, sshpiper port not equal to the host SSH port, doctor disk thresholds, monitoring via `validateMonitoring` in the [monitoring-exporters](monitoring-exporters.md) spec). New config keys with constraints get a check here, with a message naming the key.
- **Fingerprint** (`internal/config/fingerprint.go`): SHA-256 of `Effective()`, which is the YAML encoding of the resolved config without `extends` and without the inline tunnel token; runtime `System` is excluded by its `yaml:"-"` tag. `apply` records it as `applied_config_sha256` and keeps the same bytes as the applied copy, so the copy's hash equals the recorded fingerprint. **A new config field must not change the encoding of configs that do not set it**: use `omitempty` and a zero default (as `Doctor` does), or every host reads as drifted and fleet rollouts stop.
- **Detection** (`internal/config/detector.go`): `DetectSystem` reads os-release, DGX markers, GPUs, memory and mounts. `SuggestProfile`: rocky, then dgx, then gpu-server (NVIDIA GPU), else minimal. Suggestion is used only by `apply` when no profile or config is given and by reports; root-mutating subcommands never take it (see [cli-runtime](cli-runtime.md)).
- **Home-base detection** (`defaultHomeBase`, `detectHomeBase`) never silently re-homes a host: an uncommented `HOME=` in `/etc/default/useradd` wins, then an existing root-only `<base>/.rootfiles` under `/home`, `/raid/home`, `/data/home`, `/nvme/home`, then (APT hosts only) a separate, local, root-only data drive under `/raid`, `/data`, `/nvme`, else `/home`. If rootfiles metadata exists under another root-only base, loading fails with `ErrAmbiguousHomeBase` and asks for an explicit `users.home_base`.
- **Root-only checks** (`RootOnlyBase`, `RootOnlyHomeBase`): every existing directory from the trust root down is a non-symlink directory owned by root and not group/other-writable. Used for home bases, the applied copy and backup directories.
- `IsModuleEnabled` maps module names to their toggle (`gpu` is `modules.nvidia.gpu_allocation.enabled`). It is one of several module-name lists that must agree with `defaultOrder` (see #94).

## Behaviour

- `rootfiles config show [--reveal]`: prints the merged config for the same target `check` would use (`loadForInspection` via `resolveTarget`), with env and flag overrides; the tunnel token is masked unless `--reveal`.
- `rootfiles config validate`: same loading, prints the source and exits non-zero on any error. On Rocky it also runs `ValidateCapabilities`.
- `rootfiles config init [--extends P] [--file F] [--force] [--from-system]`: writes a template extending the suggested profile, or a snapshot of the running host (`SnapshotCurrent` in `internal/config/snapshot.go`, works on hosts not set up by rootfiles); files are written 0600 because a capture may hold the tunnel token; an existing file needs `--force`.

## Distro support

- `ResolveDistro`, `SupportedModules` and `ValidateCapabilities` live in `internal/config/distro.go` and are owned by [distro-rocky](distro-rocky.md). Home-base detection skips data drives on non-APT hosts because Rocky lacks SELinux home labeling outside `/home`.

## Safety

- Secrets: the inline `tunnel_token` never enters the fingerprint, the applied copy or `config show` output (masked); prefer `tunnel_token_file`.
- Detection reads the host; tests redirect `useraddDefaultsPath`, `hostRoot` and `rootUID` instead of reading `/etc`.
- `config init --file` writes with `os.WriteFile` directly; it is an operator file, not a managed host file.

## Tests that pin it

- `internal/config/loader_test.go`: `TestResolveProfile_*`, `TestMergeTrees`, `TestLoad_CustomFileExtendsProfileAndDisables`, `TestLoad_CustomFileExtendsRelativeFile`, `TestLoad_ExtendsCycle`, `TestLoad_UnknownKeyRejected`, `TestValidate`, `TestAllProfilesValid`, `TestApplyEnvOverrides`, `TestIsModuleEnabled`, `TestLoad_TunnelTokenFile`, `TestDefaultHomeBase*`, `TestLoad_AmbiguousHomeBase`, `TestLoad_HomeBaseDetectionOnlyWhenUnset`, `TestLoadSite_RequiresExplicitHomeBase`, `TestLoad_DoctorDiskThresholds`.
- `internal/config/fingerprint_test.go`: `TestFingerprintIgnoresRuntimeAndSecretButDetectsIntent`, `TestFingerprintStableAcrossReleases` (pins the digest of a config that sets no new keys), `TestLoadSiteIgnoresControllerEnvironmentAndResolvesFiles`.
- `internal/config/snapshot_test.go`; `internal/cli/configcmd_test.go`: `TestConfigTemplateIsValidForEveryProfile`, `TestConfigShowMasksToken`, `TestConfigValidateFailsOnBadConfig`.
- `tests/scenarios/dry-run-all-profiles.sh` (CI job `scenario`) and `tests/integration/run-profile.sh` (CI job `integration`) apply each profile.

## Open issues

- [#90](https://github.com/STAIxBWLB/rootfiles/issues/90): remove the site-specific private network from `dgx.yaml`, no-op keys from `gpu-server.yaml` and `full.yaml`; `Validate` rejects an enabled private network without an address.
- [#91](https://github.com/STAIxBWLB/rootfiles/issues/91): `config init --overwrite` replaces `--force` (kept as a deprecated alias).
- [#94](https://github.com/STAIxBWLB/rootfiles/issues/94): `config.ModuleNames()` shared by `IsModuleEnabled`, distro and configcmd (which hard-codes 14 names); `UsersConfig.Base()`; one `maskSecret`.
- [#96](https://github.com/STAIxBWLB/rootfiles/issues/96): a config test that every YAML key appears in `docs/configuration.md`; README profile table matched to the YAML.
