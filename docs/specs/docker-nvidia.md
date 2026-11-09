---
id: docker-nvidia
type: module
title: Docker and NVIDIA container runtime
status: active
parent: architecture
depends-on:
  - engine
paths:
  - internal/module/docker.go
  - internal/module/daemonjson.go
  - internal/module/nvidia.go
---

# Docker and NVIDIA container runtime

## Purpose

The `docker` module installs Docker CE with the compose and buildx plugins and manages a small set of `/etc/docker/daemon.json` keys. The `nvidia` module installs the NVIDIA Container Toolkit, registers the `nvidia` Docker runtime and keeps the driver-side daemons (`nvidia-persistenced`, `nvidia-fabricmanager`) running where they are installed. Both are prerequisites for [gpu-allocation](gpu-allocation.md), the dcgm exporter in [monitoring-exporters](monitoring-exporters.md) and [tenant](tenant.md) containers.

## Contracts and invariants

- **daemon.json is shared.** `readDaemonJSON` / `writeDaemonJSON` (`internal/module/daemonjson.go`) treat the file as a generic map; every key rootfiles does not manage (nvidia-ctk `runtimes`, DGX OS defaults, operator edits) survives a rewrite. A missing or empty file is an empty map; an unparsable file is an error, never overwritten. Never marshal daemon.json from a typed struct.
- **Managed keys only.** `DockerModule.daemonUpdates` enforces `data-root` = `modules.docker.storage_dir` and, only when no `log-driver` is set, adds `json-file` with `max-size: 10m`, `max-file: 3`. With `storage_dir` empty the module touches no daemon.json key. An operator-chosen log driver is never replaced.
- **No data migration.** Changing `data-root` does not move images or volumes; Apply warns when the old root has entries (`hasEntries`) and restarts Docker once after the write.
- **Runtime registration is independent of installation.** `NvidiaModule.needsRuntime` checks daemon.json for `runtimes.nvidia` whenever Docker exists, so a preinstalled toolkit (DGX OS) with a rewritten daemon.json is still repaired. Hosts without Docker have nothing to register.
- **Reload, not restart, for running hosts.** After `nvidia-ctk runtime configure --runtime=docker`, Apply reloads Docker when the toolkit was already installed (running containers keep running) and restarts only when it installed the toolkit in this run.
- **Driver daemons are opt-in and never installed.** `services` manages `nvidia-persistenced` / `nvidia-fabricmanager` only when `modules.nvidia.persistenced` / `fabric_manager` is set and the unit file exists (`unitInstalled`). rootfiles does not install or change GPU drivers.
- **Check and Apply agree** on: Docker install, compose compatibility symlink, each daemon.json key, toolkit install, runtime registration, inactive driver daemons. Check declares Docker service enable/start when it is inactive or disabled, and a restart when daemon configuration changes. Apply enables/starts only pending service work; a successful operation sets `Changed`, while failure produces a warning. A current service is not enabled again.
- All writes go through `rc.Runner` (`WriteFile`, `MkdirAll`, `Symlink`, `Remove`) so dry-run and backups apply.

## Behaviour

- Docker install (Apply, when `docker` is missing): adds the Docker APT keyring and source for the host codename/arch (`rc.APT.AddKeyring`, `AddSourceList`), then installs `docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin`.
- `/usr/local/bin/docker-compose` (`dockerComposePath`, overridable in tests) compatibility symlink points at the first existing compose plugin in `composePluginPaths`; Check names that same source and a dangling symlink there is replaced. A missing plugin is not reported as an available symlink operation.
- Toolkit install (Apply, when `nvidia-container-toolkit` is not installed): NVIDIA libnvidia-container APT keyring and source, then the package.
- No CLI surface of its own: run through `rootfiles apply|check --module docker|nvidia`.

## Distro support

- Ubuntu and DGX OS only. Both modules use APT repositories and `rc.APT`; Rocky capability validation (`config.ValidateCapabilities`, [distro-rocky](distro-rocky.md)) rejects them before any module runs.
- Install hints use `packageInstallCommand` and include the same package set passed to the backend. This does not add Rocky vendor support or RPM repository installation.

## Safety

- Never remove or reorder unmanaged daemon.json keys, never change Docker's `default-runtime` (tenants select `--runtime=nvidia` per container), never restart Docker when a reload suffices.
- A daemon.json parse error stops the module instead of rewriting the file.

## Tests that pin it

- `internal/module/docker_test.go`: `TestDockerModule_ApplyPreservesNvidiaRuntime`, `TestDockerModule_DaemonUpdatesRespectsOperatorLogDriver`, `TestReadDaemonJSON_InvalidIsError`, `TestReadDaemonJSON_Missing`.
- `internal/module/agreement_test.go`: complete Docker/NVIDIA installation package lists, pending/current/failed Docker service work, and continued Rocky capability rejection.
- `internal/module/nvidia_test.go`: `TestNvidiaRuntimeRegistrationReloadsDocker`.
- No CI `module` matrix entry exists for `docker` or `nvidia`; they run only under the `unit` job and in profile dry-runs (`tests/scenarios/dry-run-all-profiles.sh`).

## Open issues

- [#94](https://github.com/STAIxBWLB/rootfiles/issues/94): shared write-if-changed and `systemdReload` helpers.
- [#95](https://github.com/STAIxBWLB/rootfiles/issues/95): drop the docker-compose v1 symlink (owner approved, Action needed); `AddKeyring`/`AddSourceList` move to an APT-only interface.
