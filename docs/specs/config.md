---
id: config
type: module
title: Configuration
status: active
parent: architecture
paths:
  - internal/config/config.go
  - internal/config/modules.go
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
- **Built-in profiles** (`internal/config/profiles/*.yaml`, embedded with `go:embed`): `base`; `minimal` extends base; `dgx`, `gpu-server` and `full` extend minimal; `rocky` stands alone with RPM package names. `AvailableProfiles()` is the hard-coded list of names and must match the files. Embedded profiles carry no site-specific private-network addresses; operators put those in a site config extending the profile. Effective-profile goldens pin all six profiles; minimal/base/rocky are unchanged by profile cleanup.
- **Load order** (`LoadWithOverrides`; `LoadWithHomeBase` remains a wrapper): resolve profile (empty name means `minimal`) or file; reject `tunnel_token` and `tunnel_token_file` together (before env overrides, so `ROOTFILES_TUNNEL_TOKEN` may still override a file); apply env overrides (`applyEnvOverrides`: `ROOTFILES_HOME_BASE`, `_TIMEZONE`, `_TUNNEL_TOKEN`, `_VLAN_ADDRESS`, `_VLAN_INTERFACE`, `_DOCKER_ROOT`, `_DATA_DIR`); apply CLI home-base and VLAN address overrides (`config.Overrides`, with CLI address winning over env); detect the home base only when still unset; `Validate`; attach `System`.
- **LoadSite** is the controller's loader for inventory site configs: no environment overrides, `users.home_base` must be explicit (a host-detected value would read as drift), and the token rules above. See [fleet](fleet.md).
- **Fallback** is the config for commands that can run without a profile: env overrides and `--home-base` only, relative paths dropped, `Users.HomeBase` empty when unknown.
- An enabled `modules.cloudflared.private_network` requires an address from config, environment or a declared CLI reader; absence is an error naming `modules.cloudflared.private_network.address`. Apply/check/config inspection and tunnel setup/install fold `--vlan-address` in before validation. Doctor and status do not read that flag and refuse it before host inspection.
- **Validate** (`internal/config/validate.go`) collects every semantic error in one pass (ports, absolute paths, account and group names, sysctl keys, sizes, CIDRs, interface names, tenant root, sshpiper port not equal to the host SSH port, doctor disk thresholds, monitoring via `validateMonitoring` in the [monitoring-exporters](monitoring-exporters.md) spec). New config keys with constraints get a check here, with a message naming the key.
- **Fingerprint** (`internal/config/fingerprint.go`): SHA-256 of `Effective()`, which is the YAML encoding of the resolved config without `extends` and without the inline tunnel token; runtime `System` is excluded by its `yaml:"-"` tag. `apply` records it as `applied_config_sha256` and keeps the same bytes as the applied copy, so the copy's hash equals the recorded fingerprint. **A new config field must not change the encoding of configs that do not set it**: use `omitempty` and a zero default (as `Doctor` does), or every host reads as drifted and fleet rollouts stop.
- **Detection** (`internal/config/detector.go`): `DetectSystem` reads os-release, DGX markers, GPUs, memory and mounts. `SuggestProfile`: rocky, then dgx, then gpu-server (NVIDIA GPU), else minimal. Suggestion is used only by `apply` when no profile or config is given and by reports; root-mutating subcommands never take it (see [cli-runtime](cli-runtime.md)).
- **Home-base detection** (`defaultHomeBase`, `detectHomeBase`) never silently re-homes a host: an uncommented `HOME=` in `/etc/default/useradd` wins, then an existing root-only `<base>/.rootfiles` under `/home`, `/raid/home`, `/data/home`, `/nvme/home`, then (APT hosts only) a separate, local, root-only data drive under `/raid`, `/data`, `/nvme`, else `/home`. If rootfiles metadata exists under another root-only base, loading fails with `ErrAmbiguousHomeBase` and asks for an explicit `users.home_base`.
- **Root-only checks** (`RootOnlyBase`, `RootOnlyHomeBase`): every existing directory from the trust root down is a non-symlink directory owned by root and not group/other-writable. Used for home bases, the applied copy and backup directories.
- `ModuleNames()` returns a fresh slice in the existing execution order. Its config-owned metadata also maps `IsModuleEnabled` toggles (`gpu` is `modules.nvidia.gpu_allocation.enabled`, independent of `nvidia.enabled`). The registry constructor set, distro support and config inspection agree with these names.
- `UsersConfig.Base()` returns `/home` only for an empty home base and otherwise returns the configured value verbatim. Use it only where a default was already intended; empty-as-unknown detection, trust checks and refusals remain distinct. It does not mutate config or change its encoding/fingerprint.

## Behaviour

- `rootfiles config show [--reveal]`: prints the merged config for the same target `check` would use (`loadForInspection` via `resolveTarget`), with env and flag overrides; the tunnel token is masked unless `--reveal`.
- `rootfiles config validate`: same loading, prints the source and exits non-zero on any error. On Rocky it also runs `ValidateCapabilities`.
- `rootfiles config init [--extends P] [--file F] [--overwrite] [--from-system]`: writes a template extending the suggested profile, or a snapshot of the running host (`SnapshotCurrent` in `internal/config/snapshot.go`, works on hosts not set up by rootfiles); a new file is created 0600 because a capture may hold the tunnel token, while an existing destination keeps its mode and owner (`os.WriteFile` semantics; hardening tracked in #119); an existing file needs `--overwrite` (`--force` is a deprecated alias).

## Distro support

- `ResolveDistro`, `SupportedModules` and `ValidateCapabilities` live in `internal/config/distro.go` and are owned by [distro-rocky](distro-rocky.md). Home-base detection skips data drives on non-APT hosts because Rocky lacks SELinux home labeling outside `/home`.

## Safety

- Secrets: the inline `tunnel_token` never enters the fingerprint, the applied copy or `config show` output (masked); prefer `tunnel_token_file`.
- Detection reads the host; tests redirect `useraddDefaultsPath`, `hostRoot` and `rootUID` instead of reading `/etc`.
- `config init --file` writes with `os.WriteFile` directly; it is an operator file, not a managed host file.

## Tests that pin it

- `internal/config/modules_test.go`: exact module order, independent returned slices, every toggle (including GPU/NVIDIA independence), and the unchanged Rocky subset. `TestRegistryDefaultOrderSync` checks the constructor set against these names.

- `internal/config/loader_test.go`: `TestResolveProfile_*`, `TestMergeTrees`, `TestLoad_CustomFileExtendsProfileAndDisables`, `TestLoad_CustomFileExtendsRelativeFile`, `TestLoad_ExtendsCycle`, `TestLoad_UnknownKeyRejected`, `TestValidate`, `TestAllProfilesValid`, `TestApplyEnvOverrides`, `TestIsModuleEnabled`, `TestLoad_TunnelTokenFile`, `TestDefaultHomeBase*`, `TestLoad_AmbiguousHomeBase`, `TestLoad_HomeBaseDetectionOnlyWhenUnset`, `TestLoadSite_RequiresExplicitHomeBase`, `TestLoad_DoctorDiskThresholds`.
- `internal/config/fingerprint_test.go`: `TestFingerprintIgnoresRuntimeAndSecretButDetectsIntent`, `TestFingerprintStableAcrossReleases` (pins the digest of a config that sets no new keys), `TestLoadSiteIgnoresControllerEnvironmentAndResolvesFiles`.
- `internal/config/docs_test.go`: `TestDocsConfigurationCoversEveryKey` (every YAML key path of `Config` appears in `docs/configuration.md`).
- `internal/config/snapshot_test.go`; `internal/cli/configcmd_test.go`: `TestConfigTemplateIsValidForEveryProfile`, `TestConfigShowMasksToken`, `TestConfigValidateFailsOnBadConfig`.
- `tests/scenarios/dry-run-all-profiles.sh` (CI job `scenario`) and `tests/integration/run-profile.sh` (CI job `integration`) apply each profile.

## Open issues

- [#94](https://github.com/STAIxBWLB/rootfiles/issues/94): remaining CLI helper work: one `maskSecret`. Module-name metadata and home-base defaulting are shared.
