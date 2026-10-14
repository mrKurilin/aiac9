"""Run the installer against fake commands only: no host installs, Docker, or network."""
from pathlib import Path
import os
import subprocess
import tempfile
import unittest

SCRIPT = Path(__file__).resolve().parents[1] / 'deploy' / 'bootstrap-vps.sh'


class BootstrapTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='garage33-bootstrap-test-')
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.app = self.root / 'app'
        (self.app / 'deploy').mkdir(parents=True)
        (self.root / 'terminal').mkdir()
        (self.root / 'terminal' / 'go.mod').write_text('module aiac9-terminal\n')
        (self.app / 'compose.yaml').write_text('services: {}\n')
        (self.app / 'Dockerfile').write_text('# test fixture\n')
        release = self.root / 'os-release'
        release.write_text('ID=ubuntu\nVERSION_ID=24.04\n')
        memory = self.root / 'meminfo'
        memory.write_text('MemTotal: 4194304 kB\n')
        source = SCRIPT.read_text()
        self.assertIn('((EUID == 0))', source)
        source = source.replace('((EUID == 0))', '((1 == 1))')
        source = source.replace('/etc/os-release', str(release)).replace('/proc/meminfo', str(memory))
        # Simulate missing Docker without allowing a real host CLI to be selected.
        prefix = '''command() {
    if [[ "$1" == -v && "$2" == docker && "$MOCK_FRESH" == 1 && ! -f "$MOCK_DIR/installed" ]]; then return 1; fi
    builtin command "$@"
}
'''
        self.script = self.app / 'deploy' / 'bootstrap-vps.sh'
        self.script.write_text(source.replace('set +x\n', 'set +x\n' + prefix, 1))
        self.bin = self.root / 'bin'
        self.bin.mkdir()
        self.env = {
            'PATH': str(self.bin) + ':/usr/bin:/bin',
            'MOCK_DIR': str(self.root), 'MOCK_FRESH': '0', 'MOCK_UNREACHABLE': '0',
            'DEEPSEEK_API_KEY': 'placeholder-api', 'CRM_ADMIN_PASSWORD': 'placeholder-password',
            'MRKAI_SERVICE_KEY': 'placeholder-service-key',
        }
        self.stub('uname', 'if [ "$1" = -s ]; then echo Linux; else echo x86_64; fi')
        self.stub('df', "printf 'Filesystem 1024-blocks Used Available Capacity Mounted\\nfixture 50000000 1 45000000 1%% /\\n'")
        self.stub('systemctl', 'printf "systemctl %s\\n" "$*" >> "$MOCK_DIR/commands"')
        self.stub('sleep', ':')
        self.stub('apt-cache', 'exit 0')
        self.stub('add-apt-repository', 'printf "repository %s\\n" "$*" >> "$MOCK_DIR/commands"')
        self.stub('dpkg-query', 'exit 1')
        self.stub('apt-get', 'printf "apt-get %s\\n" "$*" >> "$MOCK_DIR/commands"; touch "$MOCK_DIR/installed"')
        self.stub('docker', '''printf 'docker %s\n' "$*" >> "$MOCK_DIR/commands"
printf 'site=%s\n' "${SITE_DOMAIN:-}" >> "$MOCK_DIR/commands"
case "$*" in
  *"compose version --short"*) echo 2.40.3 ;;
  *"buildx version"*) echo 'buildx v0.25.0' ;;
  *"--deployment-check"*) [ "$MOCK_UNREACHABLE" = 0 ] ;;
  *) : ;;
esac''')

    def stub(self, name, code):
        path = self.bin / name
        path.write_text('#!/bin/bash\nset -eu\n' + code + '\n')
        path.chmod(0o755)

    def run_script(self, *args, trace=False):
        return subprocess.run(['bash'] + (['-x'] if trace else []) + [str(self.script)] + list(args),
                              env=self.env, text=True, capture_output=True, timeout=10)

    def commands(self):
        path = self.root / 'commands'
        return path.read_text() if path.exists() else ''

    def test_http_default_prints_verified_public_urls(self):
        result = self.run_script('--address', 'garage.example.com', trace=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('Сайт для записи видео: http://garage.example.com/', result.stdout)
        self.assertIn('CRM: http://garage.example.com/crm', result.stdout)
        calls = self.commands()
        self.assertIn('site=http://garage.example.com', calls)
        self.assertIn('up -d --wait --wait-timeout 180', calls)
        self.assertIn('--deployment-check http://garage.example.com', calls)
        self.assertNotIn('apt-get', calls)
        for name in ('DEEPSEEK_API_KEY', 'CRM_ADMIN_PASSWORD', 'MRKAI_SERVICE_KEY'):
            self.assertNotIn(self.env[name], result.stdout + result.stderr + calls)

    def test_fresh_ubuntu_installs_only_mocked_packages(self):
        self.env['MOCK_FRESH'] = '1'
        result = self.run_script('--address', 'garage.example.com')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('apt-get install -y docker.io docker-compose-v2 docker-buildx ca-certificates', self.commands())
        self.assertNotIn('apt-get remove', self.commands())

    def test_https_for_public_ip_uses_sslip(self):
        result = self.run_script('--address', '8.8.8.8', '--https', '--project', 'garage33-video')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('https://8.8.8.8.sslip.io/', result.stdout)
        self.assertIn('-p garage33-video', self.commands())
        self.assertIn('site=8.8.8.8.sslip.io', self.commands())

    def test_failed_public_probe_never_claims_ready(self):
        self.env['MOCK_UNREACHABLE'] = '1'
        result = self.run_script('--address', 'garage.example.com')
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn('Сайт для записи видео:', result.stdout)
        self.assertIn('публичный адрес пока не прошёл проверку', result.stderr)

    def test_check_never_installs_or_launches(self):
        self.env['MOCK_FRESH'] = '1'
        result = self.run_script('--check')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('Установки и запуск не выполнялись', result.stdout)
        self.assertEqual(self.commands(), '')

    def test_rejects_invalid_addresses_before_mutation(self):
        for value in ('0.0.0.0', '256.1.1.1', 'http://garage.example.com', 'garage.example.com/path', 'garage.example.com:8080'):
            result = self.run_script('--address', value)
            self.assertNotEqual(result.returncode, 0)
            self.assertNotIn('systemctl', self.commands())
            self.assertNotIn('apt-get', self.commands())

    def test_actual_help_has_no_side_effects(self):
        result = subprocess.run(['bash', str(SCRIPT), '--help'], text=True, capture_output=True)
        self.assertEqual(result.returncode, 0)
        self.assertIn('По умолчанию: публичный HTTP', result.stdout)


if __name__ == '__main__':
    unittest.main()
