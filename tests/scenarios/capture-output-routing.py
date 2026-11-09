#!/usr/bin/env python3
"""Capture full CLI previews in a dev container with controlled detectSystem binaries."""
import argparse
import difflib
import sys
import hashlib
import json
import os
import re
from pathlib import Path
import subprocess
import tempfile


def capture(before, after, destination, host_fixture):
    if not Path('/.dockerenv').is_file() or os.environ.get('ROOTFILES_OUTPUT_SANDBOX') != '1':
        raise SystemExit('output capture requires an isolated dev container; set ROOTFILES_OUTPUT_SANDBOX=1 there')
    destination.mkdir(parents=True, exist_ok=True)
    host_fixture.mkdir(parents=True, exist_ok=True)
    (host_fixture / 'sshd.d').mkdir(exist_ok=True)
    (host_fixture / 'sshd_config').write_text('Port 22\n')
    # Only data-root drifts, avoiding the existing daemonUpdates map order.
    (host_fixture / 'daemon.json').write_text('{"log-driver":"json-file"}\n')
    def snapshot():
        return {str(p.relative_to(host_fixture)): ('directory' if p.is_dir() else hashlib.sha256(p.read_bytes()).hexdigest()) for p in host_fixture.rglob('*')}
    host_before = snapshot()
    with tempfile.TemporaryDirectory(prefix='rootfiles-output-') as temporary:
        fixture = Path(temporary)
        commands = fixture / 'bin'
        commands.mkdir()
        # No package queries, NSS or service probes reach tools in the dev image.
        bodies = {
            'dpkg-query': 'exit 1', 'rpm': 'exit 1', 'getent': 'exit 0',
            'systemctl': 'exit 1', 'timedatectl': 'exit 1',
            'localectl': "printf 'en_US.UTF-8\\n'",
            'python3': "printf 'rootfiles-updateinfo-ok:appstream,baseos\\n'",
        }
        for name, body in bodies.items():
            target = commands / name
            target.write_text('#!/bin/sh\n' + body + '\n')
            target.chmod(0o755)
        environment = dict(os.environ)
        environment.update(PATH=str(commands), HOME=str(fixture), NO_COLOR='1', TERM='dumb')
        for key in list(environment):
            if key.startswith('ROOTFILES_'):
                environment.pop(key)
        environment.update(ROOTFILES_STATE_DIR=str(fixture / 'state'), ROOTFILES_LOCK_FILE=str(fixture / 'lock'), ROOTFILES_LOG_FILE=str(fixture / 'audit'))
        records = []
        for profile in ('base', 'minimal', 'dgx', 'gpu-server', 'full', 'rocky'):
            pair = []
            for stage, binary in (('before', before), ('after', after)):
                profile_environment = dict(environment, ROOTFILES_OUTPUT_DISTRO='rocky' if profile == 'rocky' else 'ubuntu')
                run = subprocess.run([str(binary), 'apply', '--profile', profile, '--home-base', '/home', '--yes', '--dry-run'], env=profile_environment, capture_output=True, text=True, timeout=30)
                text = run.stdout.replace(str(fixture), '<fixture>').replace(str(host_fixture), '<fixture>')
                errors = run.stderr.replace(str(fixture), '<fixture>').replace(str(host_fixture), '<fixture>')
                # Query logs carry the live clock; retain every other stderr byte.
                errors = re.sub(r'(?m)^time=[^ ]+ ', 'time=<clock> ', errors)
                if f'Profile: {profile}\n' not in text or 'Modules: ' not in text:
                    raise SystemExit(f'{profile}: preview never reached the module plan; inspect the controlled detector fixture: {errors}')
                selected = next(line.removeprefix('Modules: ') for line in text.splitlines() if line.startswith('Modules: ')).split(', ')
                for name in selected:
                    if not any(line.startswith('  ') and f'{name}:' in line for line in text.splitlines()):
                        raise SystemExit(f'{profile}: {name} never reached RunAll; inspect the fixture')
                if run.returncode != 0:
                    raise SystemExit(f'{profile}: controlled preview failed; repair the fixture before comparing output: {errors}\n{text}')
                (destination / f'{profile}.{stage}.golden').write_text(text)
                records.append(dict(profile=profile, stage=stage, system_fixture=profile_environment['ROOTFILES_OUTPUT_DISTRO'], stderr_clock_normalized=True, exit=run.returncode, stderr=errors))
                pair.append((run.returncode, text, errors))
            # Only reviewed presentation whitespace may differ. Words, marker
            # characters, error channel and process result must be unchanged.
            if pair[0][0] != pair[1][0] or pair[0][2] != pair[1][2] or pair[0][1].split() != pair[1][1].split():
                print(json.dumps(dict(before_exit=pair[0][0],after_exit=pair[1][0],before_stderr=pair[0][2],after_stderr=pair[1][2]),indent=2),file=sys.stderr)
                print(''.join(difflib.unified_diff(pair[0][1].splitlines(True),pair[1][1].splitlines(True),fromfile='before',tofile='after')),file=sys.stderr)
                raise SystemExit(f'{profile}: preview changed beyond presentation whitespace; use the printed diff to repair the fixture or output')
        for name in ('state', 'lock', 'audit'):
            if (fixture / name).exists():
                raise SystemExit(f'dry-run created {name}; inspect the preview before continuing')
        if snapshot() != host_before:
            raise SystemExit('preview changed the private managed-file fixture; inspect the output routing before continuing')
        (destination / 'results.json').write_text(json.dumps(records, indent=2) + '\n')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('before', type=lambda value: Path(value).resolve())
    parser.add_argument('after', type=lambda value: Path(value).resolve())
    parser.add_argument('destination', type=Path)
    parser.add_argument('host_fixture', type=lambda value: Path(value).resolve())
    arguments = parser.parse_args()
    capture(arguments.before, arguments.after, arguments.destination, arguments.host_fixture)
