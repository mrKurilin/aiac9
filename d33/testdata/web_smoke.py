"""Two real processes + SQLite + a fake local LLM. No package installation or paid calls."""
import base64
import http.cookiejar
import http.server
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request


def free_port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


class FakeModel(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        assert body.get('response_format', {}).get('type') == 'json_object'
        messages = body['messages']
        question = next(item['content'] for item in reversed(messages) if item['role'] == 'user')
        last = messages[-1]
        if last['role'] == 'user':
            if '79000000001' in question or '79000000002' in question:
                name = 'mcp__crm__access_check'
                phone = '79000000001' if '79000000001' in question else '79000000002'
                args = {'phone': phone}
            elif 'MANN' in question or 'Brembo' in question:
                name = 'mcp__crm__inventory_list'
                args = {}
            else:
                name = None
            if name:
                assert any(tool['function']['name'] == name for tool in body['tools'])
                reply = {'role': 'assistant', 'content': '', 'tool_calls': [{'id': 'fake-call-1', 'type': 'function', 'function': {'name': name, 'arguments': json.dumps(args)}}]}
            else:
                reply = {'role': 'assistant', 'content': json.dumps({'answer': 'По этому вопросу нужен специалист.', 'resolved': False, 'reason': 'Вопрос не описан в FAQ', 'category': 'general'})}
        else:
            assert last['role'] == 'tool'
            result = json.loads(last['content'])
            if 'items' in result:
                sku = 'MANN W 914/2' if 'MANN' in question else 'Brembo P 85 020'
                item = next(item for item in result['items'] if item['sku'] == sku)
                answer = f"{item['name']}: остаток {item['quantity']}, цена {item['price_rub']} рублей."
            elif result.get('blocked_date'):
                answer = 'Сегодня действует ограничение; повторите вход завтра.'
            else:
                answer = 'Причина не подтверждена.'
            # The agent must create the second-phone task even if the model claims solved.
            reply = {'role': 'assistant', 'content': json.dumps({'answer': answer, 'resolved': True})}
        data = json.dumps({'choices': [{'message': reply}]}).encode()
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, *_):
        pass


model = http.server.ThreadingHTTPServer(('127.0.0.1', 0), FakeModel)
threading.Thread(target=model.serve_forever, daemon=True).start()
processes = []
with tempfile.TemporaryDirectory(prefix='garage33-smoke-') as directory:
    root = Path(directory)
    binary = root / 'mrkai'
    environment = os.environ.copy()
    environment.update({'GOPROXY': 'off', 'GOSUMDB': 'off'})
    subprocess.run(['go', 'build', '-o', str(binary), '.'], env=environment, check=True)
    crm_port, support_port = free_port(), free_port()
    crm_url, support_url = f'http://127.0.0.1:{crm_port}', f'http://127.0.0.1:{support_port}'
    environment.update({
        'WEB_ADDR': f'127.0.0.1:{crm_port}', 'SUPPORT_ADDR': f'127.0.0.1:{support_port}',
        'SUPPORT_URL': support_url, 'CRM_URL': crm_url,
        'CRM_DB_PATH': str(root / 'crm.sqlite'), 'SUPPORT_DATA_DIR': str(root / 'support'),
        'MRKAI_PROJECT_ROOT': str(Path.cwd()), 'MRKAI_SERVICE_KEY': 'placeholder-service-key',
        'CRM_ADMIN_PASSWORD': 'placeholder-password', 'DEEPSEEK_API_KEY': 'placeholder',
        'DEEPSEEK_BASE_URL': f'http://127.0.0.1:{model.server_port}',
    })
    visitor = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
    auth = 'Basic ' + base64.b64encode(b'admin:placeholder-password').decode()

    def start(mode):
        process = subprocess.Popen([str(binary), mode], env=environment, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        processes.append(process)
        address = crm_url if mode == 'web' else support_url
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            if process.poll() is not None:
                raise AssertionError(f'{mode} process exited early')
            try:
                with urllib.request.urlopen(address + '/healthz', timeout=1) as response:
                    if response.status == 200:
                        return process
            except (urllib.error.URLError, OSError):
                time.sleep(0.1)
        raise AssertionError(f'{mode} did not become ready')

    def api(path, payload=None, method=None, admin=False):
        data = None if payload is None else json.dumps(payload).encode()
        headers = {'Content-Type': 'application/json'}
        if admin:
            headers['Authorization'] = auth
        request = urllib.request.Request(crm_url + path, data=data, headers=headers, method=method)
        with visitor.open(request, timeout=20) as response:
            return json.load(response)

    try:
        web = start('web')
        start('support')
        with visitor.open(crm_url) as response:
            html = response.read().decode()
            assert 'ГАРАЖ 33' in html and 'support-chat' in html and '#contacts' in html
        try:
            visitor.open(crm_url + '/crm')
            raise AssertionError('CRM did not require authentication')
        except urllib.error.HTTPError as error:
            assert error.code == 401
        request = urllib.request.Request(crm_url + '/crm', headers={'Authorization': auth})
        with visitor.open(request) as response:
            html = response.read().decode()
            assert all(status in html for status in ('TODO', 'IN PROGRESS', 'DONE'))
        first = api('/api/chat', {'message': 'Есть MANN W 914/2?'})
        assert first['resolved'] and '8' in first['answer'] and '790' in first['answer']
        empty = api('/api/chat', {'message': 'Есть Brembo P 85 020?'})
        assert empty['resolved'] and 'остаток 0' in empty['answer']
        today = api('/api/chat', {'message': 'Не работает доступ 79000000001'})
        assert today['resolved'] and not today.get('task_id') and 'после полуночи по Москве' in today['answer']
        assert api('/api/crm/tasks', admin=True) == []
        broken = api('/api/chat', {'message': 'Не работает доступ 79000000002'})
        assert not broken['resolved'] and broken['task_id'] > 0
        repeated = api('/api/chat', {'message': 'Проверь ещё раз 79000000002'})
        assert repeated['task_id'] == broken['task_id']
        task = api(f"/api/crm/tasks/{broken['task_id']}", admin=True)
        assert task['phone'] == '79000000002' and task['status'] == 'TODO'
        assert task['messages'] and any('accessDecision' in hint for hint in task['code_hints'])
        for status in ('IN PROGRESS', 'DONE'):
            task = api(f"/api/crm/tasks/{task['id']}", {'status': status, 'title': task['title'], 'reason': task['reason']}, method='PATCH', admin=True)
            assert task['status'] == status
        unknown = api('/api/chat', {'message': 'Почему потерялась моя запись на обслуживание?'})
        assert not unknown['resolved'] and unknown['task_id'] > 0
        api('/api/chat', method='DELETE')
        assert api('/api/chat') == []
        assert len(api('/api/crm/tasks', admin=True)) == 2
        web.terminate()
        web.wait(timeout=10)
        start('web')
        persisted = api('/api/crm/tasks', admin=True)
        assert len(persisted) == 2 and any(task['status'] == 'DONE' for task in persisted)
        print('WEB: two processes, static site, protected CRM, stock, today-only access, automatic TODO, context/code hints, deduplication, kanban, chat reset and SQLite restart OK')
    finally:
        for process in reversed(processes):
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()
        model.shutdown()
