# rootfiles-v2 개발 계획

## 1. 프로젝트 개요

**목적**: 서버 최초 부트스트래핑 도구 (root 권한 시스템 설정 전용)

**대상 환경**:
- NVIDIA DGX OS (DGX H100, A100 320GB, H200 NVL)
- Ubuntu 22.04 / 24.04 일반 서버
- 제주한라대 AI 컴퓨팅센터 인프라

**역할 분담**:
```
rootfiles-v2 (root)          →  dotfiles-v2 (user)
━━━━━━━━━━━━━━━━━━━━         ━━━━━━━━━━━━━━━━━━━
시스템 패키지 설치              사용자 dotfiles (chezmoi)
사용자 계정 생성/관리            zsh + oh-my-zsh + starship
  커스텀 홈 (/raid/home/)       git, gh, fzf, ripgrep 등
  사용자 백업/복원               Homebrew (Linuxbrew)
SSH 서버 설정                  개발 도구 (fnm, uv, pipx)
Docker 엔진 + NVIDIA toolkit   workspace 심링크
cloudflared 터널 + VLAN        AI 도구 설정 (Claude Code)
locale/timezone                 secrets (age)
방화벽/네트워크
스토리지 마운트/심링크
```

**핵심 원칙**:
1. **Root-only**: sudo/root로 해야만 하는 작업에 집중
2. **Single binary**: `curl | sh`로 fresh 시스템에 바로 실행
3. **Idempotent**: 여러 번 실행해도 동일 결과
4. **Unattended**: `--yes` 플래그로 무인 설치 가능
5. **Selective**: 프로필/모듈 단위로 범위 선택

---

## 2. 기술 스택

| 영역 | 선택 | 이유 |
|------|------|------|
| **언어** | Go 1.22+ | 단일 바이너리, 크로스컴파일, 시스템 도구 생태계 |
| **CLI** | cobra | kubectl, docker, gh가 사용하는 표준 |
| **설정** | viper + YAML | 프로필 상속, 환경변수 오버라이드 |
| **TUI** | huh (Charm) | interactive 프롬프트, unattended 시 스킵 |
| **로깅** | slog (stdlib) | 구조화 로깅, Go 1.21+ 표준 |
| **템플릿** | text/template | v1과 동일 엔진, 추가 의존성 없음 |
| **테스트** | testing + testify | 표준 + assertion 헬퍼 |
| **빌드** | GoReleaser | GitHub Actions → 멀티플랫폼 바이너리 릴리스 |

---

## 3. 프로젝트 구조

```
rootfiles-v2/
├── cmd/
│   └── rootfiles/
│       └── main.go              # 진입점
├── internal/
│   ├── cli/                     # cobra 명령어 정의
│   │   ├── root.go              # rootfiles (루트 명령)
│   │   ├── apply.go             # rootfiles apply [--profile] [--module] [--yes]
│   │   ├── check.go             # rootfiles check (현재 상태 점검)
│   │   ├── init.go              # rootfiles init (설정 생성)
│   │   ├── tunnel.go            # rootfiles tunnel {install|setup|status|restart}
│   │   └── user.go              # rootfiles user {add|list|backup|restore|rehome}
│   ├── config/
│   │   ├── config.go            # 설정 구조체 (YAML 매핑)
│   │   ├── loader.go            # 프로필 로딩 + 상속 + 환경변수 머지
│   │   ├── detector.go          # OS/GPU/하드웨어 자동 감지
│   │   └── defaults.go          # 프로필별 기본값
│   ├── module/                  # 기능 모듈 (각각 독립 실행 가능)
│   │   ├── module.go            # Module 인터페이스
│   │   ├── packages.go          # APT 패키지 설치
│   │   ├── users.go             # 사용자 계정 생성/복원/백업
│   │   ├── ssh.go               # SSH 서버 설정
│   │   ├── docker.go            # Docker + NVIDIA Container Toolkit
│   │   ├── cloudflared.go       # cloudflared 설치/터널/VLAN private network
│   │   ├── locale.go            # locale + timezone
│   │   ├── storage.go           # 데이터 스토리지 마운트/심링크
│   │   └── network.go           # 방화벽, VLAN (cloudflared private net)
│   ├── exec/
│   │   ├── runner.go            # shell 명령 실행 (dry-run 지원)
│   │   └── apt.go               # APT 작업 래퍼
│   └── ui/
│       ├── prompt.go            # interactive 프롬프트 (huh)
│       └── progress.go          # 진행 표시
├── profiles/                    # 내장 프로필 (embed)
│   ├── base.yaml                # 공통 기반 (locale, ssh, 기본 패키지)
│   ├── minimal.yaml             # base 확장: 최소 서버
│   ├── dgx.yaml                 # minimal 확장: DGX OS 전용
│   ├── gpu-server.yaml          # minimal 확장: 일반 GPU 서버
│   └── full.yaml                # 전체 설치
├── templates/                   # 설정 파일 템플릿 (embed)
│   ├── etc/
│   │   ├── ssh/sshd_config.d/
│   │   └── docker/daemon.json
│   └── systemd/
│       └── cloudflared.service
├── scripts/
│   └── install.sh               # curl -fsSL | sh 부트스트랩
├── tests/
│   ├── module_test.go
│   ├── integration/
│   │   ├── Dockerfile.ubuntu-22.04
│   │   ├── Dockerfile.ubuntu-24.04
│   │   ├── Dockerfile.dgx-22.04
│   │   ├── entrypoint.sh
│   │   └── mock/
│   │       └── nvidia-smi
│   └── scenarios/
│       ├── fresh-install-minimal.sh
│       ├── fresh-install-dgx.sh
│       ├── user-backup-restore.sh
│       ├── user-rehome.sh
│       ├── tunnel-setup-teardown.sh
│       ├── os-reinstall-recovery.sh
│       └── dry-run-all-profiles.sh
├── .github/
│   └── workflows/
│       ├── test.yaml
│       └── release.yaml         # GoReleaser
├── .goreleaser.yaml
├── go.mod
├── go.sum
├── Makefile
├── PLAN.md
├── README.md
└── AGENTS.md
```

---

## 4. 모듈 설계

### 4.1 Module 인터페이스

```go
type Module interface {
    Name() string
    // Check: 현재 상태 확인, 필요한 변경 목록 반환
    Check(ctx context.Context, cfg *config.Config) (*CheckResult, error)
    // Apply: 실제 변경 적용 (idempotent)
    Apply(ctx context.Context, cfg *config.Config) (*ApplyResult, error)
}

type CheckResult struct {
    Satisfied bool      // 이미 완료 상태인지
    Changes   []Change  // 필요한 변경 목록 (dry-run 출력용)
}

type ApplyResult struct {
    Changed  bool
    Messages []string
}
```

### 4.2 모듈 목록 및 프로필 매핑

| 모듈 | 설명 | base | minimal | dgx | gpu-server | full |
|------|------|:----:|:-------:|:---:|:----------:|:----:|
| **locale** | locale, timezone 설정 | O | O | O | O | O |
| **packages** | 기본 APT 패키지 | O | O | O | O | O |
| **ssh** | sshd 설정, 보안 강화 | O | O | O | O | O |
| **users** | 사용자 계정 생성/관리 | - | O | O | O | O |
| **docker** | Docker CE + compose | - | - | O | O | O |
| **nvidia** | NVIDIA Container Toolkit | - | - | O | O | - |
| **gpu** | 유저별 GPU 할당 (env/cgroup/Docker wrapper) | - | - | O | O | - |
| **cloudflared** | Cloudflare Tunnel + VLAN | - | O | O | O | O |
| **storage** | RAID/NVMe 마운트, 심링크 | - | - | O | O | O |
| **network** | 방화벽, VLAN private net | - | - | O | O | O |

> 모듈 실행 순서는 `internal/module/module.go` 의 `defaultOrder` 로 고정되어 있으며, `NewRegistry()` 와의 동기화는 `TestRegistryDefaultOrderSync` 로 강제된다.

### 4.3 패키지 목록 (APT)

**base** (어떤 프로필이든 설치):
```
ca-certificates, curl, wget, gnupg, lsb-release,
git, git-lfs, vim, tmux, tree, jq, htop, unzip, zip,
build-essential, locales, zsh, software-properties-common
```

**minimal** (base +):
```
openssh-server, ufw, fail2ban
```

**docker** (docker 모듈 활성 시):
```
docker-ce, docker-ce-cli, containerd.io,
docker-buildx-plugin, docker-compose-plugin
```

**nvidia** (nvidia 모듈 활성 시):
```
nvidia-container-toolkit
```

---

## 5. 핵심 모듈 상세

### 5.1 cloudflared + VLAN (Private Network)

cloudflared 터널과 VLAN은 하나의 연결된 시스템: cloudflared가 Cloudflare Zero Trust에 터널을 열고, VLAN 인터페이스가 private network CIDR을 로컬에 바인딩하여 외부에서 private IP로 서버에 접근 가능하게 한다.

**아키텍처**:
```
[외부 사용자 + WARP 클라이언트]
    │
    ▼
[Cloudflare Zero Trust]
    │ tunnel (connector)
    ▼
[서버: cloudflared service]
    │ routes private network CIDR
    ▼
[서버: VLAN interface (예: vlan0, 172.16.x.x/32)]
    │
    ▼
[서버 내부 서비스: SSH, Docker, etc.]
```

**CLI**:
```
rootfiles tunnel install              # cloudflared 바이너리 설치
rootfiles tunnel setup <TOKEN>        # 터널 연결 + systemd 서비스 + VLAN 인터페이스 생성
rootfiles tunnel status               # 터널 서비스 + VLAN 상태 확인
rootfiles tunnel restart              # 서비스 재시작
rootfiles tunnel update               # cloudflared 바이너리 업데이트
rootfiles tunnel uninstall            # 서비스 제거 + VLAN 해제 + 바이너리 삭제
```

**`rootfiles tunnel setup` 흐름**:
1. GitHub releases에서 최신 `cloudflared-linux-amd64` 다운로드 → `/usr/local/bin/cloudflared`
2. `cloudflared service install <TOKEN>` 실행 → systemd 서비스 등록
3. `systemctl enable --now cloudflared`
4. VLAN 인터페이스 생성 (dummy type, private network IP 할당)
5. systemd-networkd 영구 설정 배포 → 리부트 후에도 유지
6. IP route 확인 (private CIDR → VLAN 인터페이스)

**VLAN 설정** (`/etc/systemd/network/` 영구 배포):
```ini
# /etc/systemd/network/10-cloudflared-vlan.netdev
[NetDev]
Name=vlan0
Kind=dummy

# /etc/systemd/network/10-cloudflared-vlan.network
[Match]
Name=vlan0

[Network]
Address=172.16.229.32/32
```

**프로필 설정**:
```yaml
modules:
  cloudflared:
    enabled: true
    tunnel_token: "${ROOTFILES_TUNNEL_TOKEN}"  # 또는 interactive 프롬프트
    private_network:
      enabled: true
      interface: vlan0
      address: "172.16.229.32/32"  # 서버별 고유 IP
```

**unattended**: `--tunnel-token` 플래그 또는 `ROOTFILES_TUNNEL_TOKEN` 환경변수 + `--vlan-address` 플래그 또는 `ROOTFILES_VLAN_ADDRESS` 환경변수

### 5.2 users (사용자 관리 — 커스텀 홈 + 백업/복원)

GPU 서버는 OS 재설치가 잦다. 사용자 데이터는 `/raid/home/`(NVMe/RAID)에 보존하고, OS만 교체하면 기존 사용자를 복원할 수 있어야 한다.

**핵심 개념**:
```
기본 홈 (/home/)        →  커스텀 홈 (/raid/home/)
  OS 디스크에 위치           RAID/NVMe에 위치
  OS 재설치 시 삭제          OS 재설치 후에도 보존
```

**CLI**:
```
rootfiles user add <name>             # 사용자 생성 (커스텀 홈)
rootfiles user add <name> --pubkey "ssh-ed25519 ..."
rootfiles user list                   # 관리 중인 사용자 목록 (--system, --names)
rootfiles user backup [--output path] # 사용자 목록 + 메타 → JSON 백업
rootfiles user restore <backup.json>  # 백업에서 사용자 복원 (기존 홈 연결)
rootfiles user rehome <name>          # 기존 /home/user → 커스텀 홈으로 이동
rootfiles user id <name>              # UID/GID/그룹 조회
rootfiles user groups [<name>]        # 전체 그룹 또는 특정 사용자 소속 그룹
rootfiles user group-add <name> --groups g1,g2 [--docker] [--sudo]
rootfiles user group-del <name> --groups g1,g2 [--docker] [--sudo]
rootfiles user passwd [<name>...] [--all] [--file path] [--password X] [--suffix '!@']
```

**사용자 생성 흐름** (`rootfiles user add`):
1. `useradd --home-dir /raid/home/<name> --create-home --shell /usr/bin/zsh <name>`
2. `usermod -aG sudo,docker <name>` (설정에 따라)
3. sudoers 설정 (NOPASSWD 옵션)
4. SSH authorized_keys 배포 (공개키 지정 시)
5. 사용자 메타데이터를 `/raid/home/.rootfiles/users.json`에 기록

**사용자 메타데이터** (`/raid/home/.rootfiles/users.json`):
```json
{
  "version": 1,
  "home_base": "/raid/home",
  "created_by": "rootfiles-v2",
  "users": [
    {
      "name": "yjlee",
      "uid": 1001,
      "gid": 1001,
      "shell": "/usr/bin/zsh",
      "groups": ["sudo", "docker"],
      "sudo_nopasswd": true,
      "ssh_pubkeys": ["ssh-ed25519 AAAA..."],
      "created_at": "2026-03-15T10:00:00Z",
      "home": "/raid/home/yjlee"
    }
  ]
}
```

**백업** (`rootfiles user backup`):
- `/raid/home/.rootfiles/users.json` 내용 + 각 사용자의 `/etc/passwd`, `/etc/shadow`, `/etc/group` 엔트리 추출
- 출력: `rootfiles-users-<hostname>-<date>.json`
- `/raid/home/.rootfiles/`에 자동 보관 (최근 5개 유지)

**복원** (`rootfiles user restore`) — OS 재설치 후:
1. 백업 JSON 로드 (또는 `/raid/home/.rootfiles/users.json` 자동 감지)
2. 각 사용자에 대해:
   - `/raid/home/<name>` 존재 확인 → 홈 디렉토리 보존됨
   - `useradd --home-dir /raid/home/<name> --no-create-home --uid <saved_uid> --shell <shell> <name>`
   - 그룹, sudoers, SSH 키 복원
   - 파일 소유권 확인 (`chown -R <uid>:<gid> /raid/home/<name>`)
3. UID/GID 충돌 시 경고 + 대안 제시

**rehome** (`rootfiles user rehome`) — 기존 사용자를 커스텀 홈으로 이동:
1. `/home/<name>` → `/raid/home/<name>` 복사 (rsync -a)
2. `usermod --home /raid/home/<name> <name>`
3. `/home/<name>` → `/raid/home/<name>` 심링크 생성 (호환성)
4. 메타데이터 업데이트

**초기 설정 시 홈 기본 경로 설정**:
```yaml
# profiles/dgx.yaml
users:
  home_base: /raid/home          # 모든 신규 사용자 홈 기본 위치
  default_shell: /usr/bin/zsh
  default_groups: [sudo, docker]
  sudo_nopasswd: true
```

`rootfiles init` 또는 `rootfiles apply` 시 `--home-base /raid/home` 플래그로도 지정 가능. `/etc/default/useradd`의 `HOME` 값도 업데이트.

### 5.3 docker (Docker + NVIDIA)

- Docker 공식 APT repo 등록 + 설치
- NVIDIA Container Toolkit repo 등록 + 설치 (nvidia 모듈)
- `/etc/docker/daemon.json` 설정 (nvidia runtime, storage driver, log 제한)
- Docker 스토리지 디렉토리 이동 (NVMe/RAID → 심링크)
- DGX OS: Docker 이미 설치된 경우 스킵, toolkit만 확인

### 5.4 storage (스토리지 관리)

DGX/GPU 서버의 NVMe/RAID 스토리지 설정:
- 데이터 디렉토리 생성 (예: `/raid/data`)
- 사용자 홈 기본 경로 생성 (예: `/raid/home/`)
- 심링크 생성 (예: `/data` → `/raid/data`)
- Docker 이미지 디렉토리 이동 (`/var/lib/docker` → `/raid/docker`)
- 마운트 포인트 자동 감지
- `/raid/home/.rootfiles/` 메타데이터 디렉토리 생성

### 5.5 ssh (SSH 보안)

- `/etc/ssh/sshd_config.d/` 커스텀 설정 배포
- root 로그인 비활성화 (옵션)
- 패스워드 인증 비활성화 (옵션)
- 포트 변경 (옵션)
- sshd 리로드

---

## 6. CLI 사용법

### 기본 사용

```bash
# 설치 (fresh 시스템)
curl -fsSL https://github.com/STAIxBWLB/rootfiles/releases/latest/download/install.sh | sudo bash

# interactive 모드 (기본) — 프로필, 모듈, 사용자, 터널 등 순차 프롬프트
sudo rootfiles apply

# 프로필 지정
sudo rootfiles apply --profile dgx

# 특정 모듈만
sudo rootfiles apply --module cloudflared,docker

# unattended (CI/자동화용) — 모든 프롬프트 스킵
sudo rootfiles apply --profile dgx --yes \
  --home-base /raid/home \
  --tunnel-token "$CF_TUNNEL_TOKEN" \
  --vlan-address "172.16.229.32/32" \
  --user admin --ssh-pubkey "ssh-ed25519 AAAA..."

# 상태 점검
sudo rootfiles check
sudo rootfiles check --module docker

# 통합 대시보드 — 시스템·프로필·모듈·GPU·터널·사용자를 한 번에 조회
rootfiles status
rootfiles status --profile dgx          # 특정 프로필 기준으로 모듈 상태 평가
NO_COLOR=1 rootfiles status             # 색상 제거 (파이프/CI 는 자동 제거됨)
```

### 사용자 관리

```bash
# 사용자 추가 (커스텀 홈 경로에 생성)
sudo rootfiles user add yjlee --pubkey "ssh-ed25519 AAAA..."
sudo rootfiles user add guest --groups sudo --no-docker

# 사용자 목록
sudo rootfiles user list

# 기존 /home/user → 커스텀 홈으로 이동
sudo rootfiles user rehome yjlee

# 사용자 백업 (OS 재설치 전)
sudo rootfiles user backup
sudo rootfiles user backup --output /tmp/users-backup.json

# 사용자 복원 (OS 재설치 후 — /raid/home/ 보존 상태)
sudo rootfiles user restore                          # /raid/home/.rootfiles/users.json 자동 감지
sudo rootfiles user restore /tmp/users-backup.json   # 명시적 백업 파일

# 그룹 조회 및 관리
sudo rootfiles user id yjlee                         # UID/GID + 소속 그룹
sudo rootfiles user groups                           # 전체 그룹 나열
sudo rootfiles user groups yjlee                     # 특정 사용자 소속 그룹
sudo rootfiles user group-add yjlee --docker --sudo  # 그룹 추가 (별칭 플래그)
sudo rootfiles user group-del yjlee --groups tmpgrp  # 그룹 제거

# 일괄 비밀번호 설정
sudo rootfiles user passwd yjlee                     # 단일 유저 (username!@ 기본 규칙)
sudo rootfiles user passwd --all --suffix '2026!'    # 시스템 유저 전체
sudo rootfiles user passwd --file users.txt          # 파일에서 username 또는 username,password
```

### GPU 할당

```bash
# 사용자에게 특정 GPU 인덱스 할당 (환경변수 방식 기본)
sudo rootfiles gpu assign yjlee 0,1
sudo rootfiles gpu assign alice 2,3 --method cgroup    # cgroup 제한
sudo rootfiles gpu assign bob   4   --method both      # env + cgroup + Docker 래퍼

# 현재 할당 현황 / nvidia-smi 교차 참조
sudo rootfiles gpu list
sudo rootfiles gpu status

# 할당 해제
sudo rootfiles gpu revoke yjlee
```

할당 DB (`<home-base>/.rootfiles/gpu-allocations.json`) 의 읽기-수정-쓰기는 `syscall.Flock` + atomic rename 으로 보호되어, 동시 `assign`/`revoke` 호출에도 할당이 유실되지 않는다.

### cloudflared 터널 + VLAN

```bash
# 터널 + private network 한번에 설정
sudo rootfiles tunnel setup "$TOKEN" --vlan-address "172.16.229.32/32"

# 개별 관리
sudo rootfiles tunnel install
sudo rootfiles tunnel status          # 터널 서비스 + VLAN 인터페이스 상태
sudo rootfiles tunnel restart
sudo rootfiles tunnel update          # cloudflared 바이너리 업데이트
sudo rootfiles tunnel uninstall       # 서비스 + VLAN 인터페이스 제거
```

### 환경변수 오버라이드

| 변수 | 설명 | 기본값 |
|------|------|--------|
| `ROOTFILES_PROFILE` | 프로필 | `minimal` |
| `ROOTFILES_YES` | unattended 모드 | `false` |
| `ROOTFILES_HOME_BASE` | 사용자 홈 기본 경로 | `/home` |
| `ROOTFILES_USER` | 생성할 사용자명 | (프롬프트) |
| `ROOTFILES_TUNNEL_TOKEN` | cloudflared 터널 토큰 | (프롬프트) |
| `ROOTFILES_VLAN_ADDRESS` | VLAN private network IP | `172.16.229.32/32` |
| `ROOTFILES_VLAN_INTERFACE` | VLAN 인터페이스 이름 | `vlan0` |
| `ROOTFILES_SSH_PUBKEY` | SSH 공개키 | (스킵) |
| `ROOTFILES_TIMEZONE` | 타임존 | `Asia/Seoul` |
| `ROOTFILES_DOCKER_ROOT` | Docker 스토리지 경로 | `/var/lib/docker` |
| `ROOTFILES_DATA_DIR` | 데이터 디렉토리 | (자동감지) |

---

## 7. 프로필 설정 예시

```yaml
# profiles/base.yaml — 모든 프로필의 공통 기반
locale: en_US.UTF-8
timezone: Asia/Seoul

modules:
  locale:
    enabled: true
  packages:
    enabled: true
  ssh:
    enabled: true

packages:
  - ca-certificates
  - curl
  - wget
  - gnupg
  - lsb-release
  - git
  - git-lfs
  - vim
  - tmux
  - tree
  - jq
  - htop
  - unzip
  - zip
  - build-essential
  - locales
  - zsh
  - software-properties-common
```

```yaml
# profiles/minimal.yaml — 최소 서버
extends: base

modules:
  users:
    enabled: true
  cloudflared:
    enabled: true

packages_extra:
  - openssh-server
  - ufw
  - fail2ban

users:
  home_base: /home
  default_shell: /usr/bin/zsh
  default_groups: [sudo]
  sudo_nopasswd: true

ssh:
  disable_root_login: true
  disable_password_auth: false
```

```yaml
# profiles/dgx.yaml — DGX OS (H100, A100, H200 NVL)
extends: minimal

users:
  home_base: /raid/home            # NVMe/RAID에 사용자 홈 — OS 재설치 시 보존
  default_groups: [sudo, docker]

modules:
  docker:
    enabled: true
    storage_dir: /raid/docker
  nvidia:
    enabled: true
  cloudflared:
    enabled: true
    private_network:
      enabled: true
      interface: vlan0
      address: "172.16.229.32/32"  # 서버별 고유 IP (setup 시 프롬프트)
  storage:
    enabled: true
    data_dir: /raid/data
    symlinks:
      /data: /raid/data
  network:
    enabled: true
    ufw: true
    allowed_ports: [22, 80, 443]

ssh:
  disable_root_login: true
  disable_password_auth: true
```

```yaml
# profiles/gpu-server.yaml — 일반 GPU 서버 (non-DGX)
extends: minimal

users:
  home_base: /data/home
  default_groups: [sudo, docker]

modules:
  docker:
    enabled: true
  nvidia:
    enabled: true
  cloudflared:
    enabled: true
    private_network:
      enabled: true
      interface: vlan0
      address: ""                  # setup 시 프롬프트
  storage:
    enabled: true
  network:
    enabled: true
    ufw: true

ssh:
  disable_password_auth: true
```

---

## 8. 시스템 감지

`internal/config/detector.go`가 자동으로 판별:

```go
type SystemInfo struct {
    OS            string // "ubuntu", "dgx-os"
    Version       string // "22.04", "24.04"
    Codename      string // "jammy", "noble"
    Arch          string // "amd64", "arm64"
    IsDGX         bool   // /etc/dgx-release 존재 여부
    HasNVIDIAGPU  bool   // nvidia-smi 실행 가능 여부
    GPUCount      int    // GPU 개수
    GPUModel      string // "H100", "A100", "H200"
    CPUCores      int
    MemoryGB      int
    StorageLayout []MountPoint // /raid, /data 등 감지
}
```

DGX OS 감지 시 자동으로 `dgx` 프로필 제안.

---

## 9. dotfiles-v2 연계

rootfiles-v2가 준비하는 것 → dotfiles-v2가 기대하는 것:

| rootfiles-v2 (root) | dotfiles-v2 (user) 기대 |
|---------------------|------------------------|
| `zsh` 패키지 설치 | `chsh -s /usr/bin/zsh` 가능 |
| `git`, `git-lfs` 설치 | chezmoi init 가능 |
| `curl`, `build-essential` | Homebrew(Linuxbrew) 설치 가능 |
| 사용자 계정 + sudo | `brew install` 가능 |
| Docker + nvidia-toolkit | Docker 사용 가능 |
| SSH 서버 설정 | 원격 접속 가능 |
| cloudflared 터널 | 외부에서 접근 가능 |

**자동 연결** (옵션):
```bash
# rootfiles-v2 완료 후 dotfiles-v2 부트스트랩 안내 출력
echo "System ready. Run as user:"
echo "  curl -fsSL https://raw.githubusercontent.com/STAIxBWLB/dotfiles/main/scripts/bootstrap.sh | bash"
```

---

## 10. CI/CD 테스트 전략

### 10.1 테스트 레이어

```
Layer 1: Unit Tests        → go test ./...  (모든 PR, 빠름)
Layer 2: Integration Tests → Docker 컨테이너 내 실제 적용 (매트릭스)
Layer 3: E2E Scenarios     → 복합 시나리오 (user backup→restore 등)
```

### 10.2 Integration Test 매트릭스

GitHub Actions에서 Docker 컨테이너로 각 OS × 프로필 × 모듈 조합 테스트.

**OS 이미지 (Docker)**:

| 이미지 | 설명 | 시뮬레이션 |
|--------|------|-----------|
| `ubuntu:22.04` | Ubuntu Jammy | 일반 서버 |
| `ubuntu:24.04` | Ubuntu Noble | 일반 서버 |
| `ubuntu:22.04` + DGX mock | DGX OS 시뮬레이션 | `/etc/dgx-release` + fake `nvidia-smi` |

**테스트 매트릭스**:

```yaml
# .github/workflows/test.yaml
name: Test
on: [push, pull_request]

jobs:
  unit:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.22"
      - run: go test ./... -race -coverprofile=coverage.out
      - uses: codecov/codecov-action@v4

  integration:
    needs: unit
    runs-on: ubuntu-latest
    strategy:
      fail-fast: false
      matrix:
        os: [ubuntu-22.04, ubuntu-24.04, dgx-22.04]
        profile: [base, minimal, dgx, gpu-server, full]
        exclude:
          # base 프로필은 ubuntu만
          - os: dgx-22.04
            profile: base
          # dgx 프로필은 dgx OS에서만
          - os: ubuntu-22.04
            profile: dgx
          - os: ubuntu-24.04
            profile: dgx
          # gpu-server는 일반 ubuntu에서
          - os: dgx-22.04
            profile: gpu-server
    steps:
      - uses: actions/checkout@v4
      - name: Build rootfiles binary
        run: go build -o rootfiles ./cmd/rootfiles/
      - name: Run integration test
        run: |
          docker build -t rootfiles-test:${{ matrix.os }} \
            -f tests/integration/Dockerfile.${{ matrix.os }} .
          docker run --rm --privileged \
            -e ROOTFILES_YES=true \
            -e ROOTFILES_PROFILE=${{ matrix.profile }} \
            -e ROOTFILES_USER=testuser \
            -e ROOTFILES_TUNNEL_TOKEN=test-token-dummy \
            -e ROOTFILES_VLAN_ADDRESS=172.16.0.1/32 \
            rootfiles-test:${{ matrix.os }}

  module:
    needs: unit
    runs-on: ubuntu-latest
    strategy:
      fail-fast: false
      matrix:
        module:
          - locale
          - packages
          - ssh
          - users
          - docker
          - nvidia
          - cloudflared
          - storage
          - network
        os: [ubuntu-22.04, ubuntu-24.04]
    steps:
      - uses: actions/checkout@v4
      - name: Build
        run: go build -o rootfiles ./cmd/rootfiles/
      - name: Test single module
        run: |
          docker build -t rootfiles-test:${{ matrix.os }} \
            -f tests/integration/Dockerfile.${{ matrix.os }} .
          docker run --rm --privileged \
            -e ROOTFILES_YES=true \
            -e ROOTFILES_MODULE=${{ matrix.module }} \
            -e ROOTFILES_USER=testuser \
            rootfiles-test:${{ matrix.os }}

  scenario:
    needs: unit
    runs-on: ubuntu-latest
    strategy:
      fail-fast: false
      matrix:
        scenario:
          - fresh-install-minimal
          - fresh-install-dgx
          - user-backup-restore
          - user-rehome
          - tunnel-setup-teardown
          - os-reinstall-recovery
          - dry-run-all-profiles
    steps:
      - uses: actions/checkout@v4
      - name: Build
        run: go build -o rootfiles ./cmd/rootfiles/
      - name: Run scenario
        run: tests/scenarios/${{ matrix.scenario }}.sh
```

### 10.3 매트릭스 요약 (총 테스트 수)

| 레이어 | 조합 | 예상 수 |
|--------|------|---------|
| **Unit** | 모든 패키지 | 1 job |
| **Integration (OS×Profile)** | 3 OS × 5 profiles - 4 제외 = **11 jobs** |
| **Module (OS×Module)** | 2 OS × 9 modules = **18 jobs** |
| **Scenario** | 7 시나리오 = **7 jobs** |
| **합계** | | **37 jobs** |

### 10.4 시나리오 테스트 상세

| 시나리오 | 검증 내용 |
|----------|----------|
| `fresh-install-minimal` | 빈 Ubuntu → minimal 프로필 적용 → 패키지/SSH/사용자/cloudflared 확인 |
| `fresh-install-dgx` | DGX mock 환경 → dgx 프로필 → Docker+NVIDIA+VLAN+커스텀 홈 확인 |
| `user-backup-restore` | 사용자 3명 생성 → backup → 사용자 삭제 → restore → UID/GID/홈 일치 확인 |
| `user-rehome` | /home/user 생성 → rehome /raid/home → 심링크+소유권 확인 |
| `tunnel-setup-teardown` | cloudflared install → setup(mock token) → VLAN 생성 → status → uninstall → 정리 확인 |
| `os-reinstall-recovery` | 사용자 생성(/raid/home) → OS 재설치 시뮬(사용자 삭제, /raid/home 보존) → restore → 홈 연결 확인 |
| `dry-run-all-profiles` | 모든 프로필 `--dry-run` → 실제 변경 0건 확인 + 출력 검증 |

### 10.5 Docker 테스트 이미지

```dockerfile
# tests/integration/Dockerfile.ubuntu-22.04
FROM ubuntu:22.04
RUN apt-get update && apt-get install -y sudo systemctl curl
# /raid 시뮬레이션
RUN mkdir -p /raid/home /raid/data /raid/docker
COPY rootfiles /usr/local/bin/rootfiles
COPY tests/integration/entrypoint.sh /entrypoint.sh
ENTRYPOINT ["/entrypoint.sh"]
```

```dockerfile
# tests/integration/Dockerfile.dgx-22.04
FROM ubuntu:22.04
RUN apt-get update && apt-get install -y sudo systemctl curl
# DGX OS 시뮬레이션
RUN echo "DGX_OS_VERSION=6.0" > /etc/dgx-release
# fake nvidia-smi (GPU 8개 시뮬)
COPY tests/integration/mock/nvidia-smi /usr/local/bin/nvidia-smi
RUN chmod +x /usr/local/bin/nvidia-smi
RUN mkdir -p /raid/home /raid/data /raid/docker
COPY rootfiles /usr/local/bin/rootfiles
COPY tests/integration/entrypoint.sh /entrypoint.sh
ENTRYPOINT ["/entrypoint.sh"]
```

### 10.6 테스트 검증 함수

각 모듈 테스트 후 검증하는 공통 assert:

```bash
# tests/integration/entrypoint.sh (발췌)
assert_package_installed() { dpkg -l "$1" | grep -q "^ii" || fail "$1 not installed"; }
assert_user_exists()       { id "$1" >/dev/null 2>&1 || fail "user $1 not found"; }
assert_user_home()         { [ "$(getent passwd "$1" | cut -d: -f6)" = "$2" ] || fail "$1 home != $2"; }
assert_user_shell()        { [ "$(getent passwd "$1" | cut -d: -f7)" = "$2" ] || fail "$1 shell != $2"; }
assert_user_in_group()     { groups "$1" | grep -qw "$2" || fail "$1 not in group $2"; }
assert_service_active()    { systemctl is-active "$1" >/dev/null || fail "$1 not active"; }
assert_file_exists()       { [ -f "$1" ] || fail "file $1 missing"; }
assert_symlink()           { [ -L "$1" ] && [ "$(readlink -f "$1")" = "$2" ] || fail "$1 !-> $2"; }
assert_interface_exists()  { ip link show "$1" >/dev/null 2>&1 || fail "interface $1 missing"; }
assert_interface_addr()    { ip addr show "$1" | grep -q "$2" || fail "$1 missing addr $2"; }
```

### 10.7 Release 워크플로우

```yaml
# .github/workflows/release.yaml
name: Release
on:
  push:
    tags: ["v*"]

jobs:
  goreleaser:
    runs-on: ubuntu-latest
    permissions:
      contents: write
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - uses: actions/setup-go@v5
        with:
          go-version: "1.22"
      - uses: goreleaser/goreleaser-action@v6
        with:
          args: release --clean
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

```yaml
# .goreleaser.yaml
builds:
  - main: ./cmd/rootfiles
    binary: rootfiles
    goos: [linux]
    goarch: [amd64, arm64]
    ldflags:
      - -s -w
      - -X main.version={{.Version}}
      - -X main.commit={{.Commit}}

archives:
  - format: tar.gz
    name_template: "rootfiles_{{ .Os }}_{{ .Arch }}"

# install.sh에서 다운로드할 수 있는 단독 바이너리도 배포
extra_files:
  - glob: scripts/install.sh

checksum:
  name_template: checksums.txt
```

---

## 11. 개발 단계

> Phase 1에 CI 기반 포함, Phase 4에서 전체 매트릭스 완성

### Phase 1: Skeleton + Core (1주)
- [ ] Go 프로젝트 초기화 (go.mod, cobra, Makefile)
- [ ] config 구조체 + YAML 로딩 + 프로필 상속
- [ ] 시스템 감지 (OS, DGX, GPU)
- [ ] Module 인터페이스 + 실행 엔진 (순서, 의존성)
- [ ] exec runner (dry-run 지원)
- [ ] `rootfiles apply` + `rootfiles check` 기본 동작
- [ ] GitHub Actions: unit test + lint 워크플로우
- [ ] Docker 테스트 이미지 기반 (ubuntu-22.04, ubuntu-24.04, dgx-mock)

### Phase 2: Essential Modules (1주)
- [ ] locale 모듈
- [ ] packages 모듈 (APT repo 추가 + 설치)
- [ ] ssh 모듈
- [ ] users 모듈 — 커스텀 홈, add/list 기본
- [ ] cloudflared 모듈 (install + tunnel + systemd 서비스)

### Phase 3: Advanced Features (1주)
- [ ] docker 모듈 (설치 + daemon.json + 스토리지 이동)
- [ ] nvidia 모듈 (container toolkit)
- [ ] storage 모듈 (마운트, 심링크, /raid/home 준비)
- [ ] network 모듈 — cloudflared VLAN private network + ufw
- [ ] users: backup/restore/rehome + users.json 메타데이터

### Phase 4: Polish + Release (1주)
- [ ] interactive TUI (huh 프롬프트, unattended 분기)
- [ ] `rootfiles tunnel` 서브커맨드 (setup/status/update/uninstall)
- [ ] `rootfiles user` 서브커맨드 (add/list/backup/restore/rehome)
- [ ] install.sh 부트스트랩 스크립트
- [ ] GoReleaser + release 워크플로우 (tag → 멀티플랫폼 바이너리)
- [ ] 전체 CI 매트릭스 완성: integration (OS×Profile 11), module (OS×Module 18), scenario (7)
- [ ] 시나리오 테스트 스크립트 (fresh-install, backup-restore, os-reinstall-recovery 등)
- [ ] README

---

## 12. v1 → v2 변경 요약

| 항목 | v1 (rootfiles) | v2 (rootfiles-v2) |
|------|----------------|-------------------|
| **언어** | Shell + Go 템플릿 (Chezmoi) | Go |
| **배포** | git clone + chezmoi | 단일 바이너리 (`curl \| sh`) |
| **설정** | `.chezmoidata.yaml` 단일 파일 | 프로필 YAML (상속, 머지) |
| **프롬프트** | Chezmoi interactive | Charm huh (또는 `--yes` 스킵) |
| **cloudflared** | optional, script 설치 | 필수, 전용 서브커맨드 + VLAN private network |
| **VLAN** | 독립 기능 (용도 불명확) | cloudflared private network 전용 |
| **사용자 관리** | 단순 생성 (기본 /home/) | 커스텀 홈 + 백업/복원/rehome |
| **OS 재설치** | 미지원 | user restore로 /raid/home 재연결 |
| **대상 OS** | Ubuntu 20.04/22.04 | Ubuntu 22.04/24.04 + DGX OS |
| **Docker** | 설치 + 스토리지 이동 | + NVIDIA toolkit + DGX 감지 |
| **테스트** | CI full integration만 | unit + integration 매트릭스 (37 jobs) + E2E 시나리오 |
| **사용자 도구** | 혼재 (root + user 도구) | root 전용 → dotfiles-v2에 위임 |
