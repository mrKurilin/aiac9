import http.server, os, pty, select, subprocess, tempfile, termios, threading, time

started = threading.Event()
class Slow(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        started.set()
        time.sleep(5)
        try:
            self.send_response(200); self.end_headers(); self.wfile.write(b'{}')
        except BrokenPipeError: pass
    def log_message(self, *args): pass
server = http.server.HTTPServer(('127.0.0.1',0),Slow)
threading.Thread(target=server.serve_forever,daemon=True).start()
master,slave=pty.openpty()
initial=termios.tcgetattr(slave)
env=os.environ.copy();env['DATA_DIR']=tempfile.mkdtemp(prefix='aiac9-d24-')
env['DEEPSEEK_API_KEY']='placeholder';env['DEEPSEEK_BASE_URL']='http://127.0.0.1:'+str(server.server_port)
process=subprocess.Popen(['./mrkai'],stdin=slave,stdout=slave,stderr=slave,env=env)
output=bytearray()
def read_for(seconds):
    end=time.monotonic()+seconds
    while time.monotonic()<end:
        ready,_,_=select.select([master],[],[],0.05)
        if ready:
            try: output.extend(os.read(master,65536))
            except OSError: break
try:
    deadline=time.monotonic()+15
    while b'/help' not in output and time.monotonic()<deadline: read_for(.2)
    assert b'/help' in output and process.poll() is None, 'entrypoint did not reach idle shell'
    assert b'\x1b[?25h' in output, 'cursor not visible'
    os.write(master,b'/rag st\t\r');read_for(.5)
    assert b'RAG: false' in output, 'nested completion failed'
    os.write(master,'тест два'.encode()+b'\x1b\x7f');read_for(.3)
    assert 'тест '.encode() in output, 'Unicode word deletion failed'
    os.write(master,b'\x1b[27u');read_for(.15)
    os.write(master,b'\x1b[<64;1;1M')
    os.write(master,b'\x1b[1;2C')
    os.write(master,'вопрос'.encode()+b'\r')
    deadline=time.monotonic()+4
    while not started.is_set() and time.monotonic()<deadline: read_for(.2)
    assert started.is_set(), 'fake slow request did not start: '+repr(output.decode('utf-8','replace')[-800:])
    os.write(master,b'\x1b[27u');read_for(.15)
    os.write(master,b'\x1b[27u');read_for(.6)
    assert process.poll() is not None, 'double Esc did not exit'
    assert 'останавливаю'.encode() in output, 'missing cancellation log'
    after=termios.tcgetattr(slave)
    for index in (0,1,2): assert initial[index]==after[index], 'terminal flags not restored'
    for bit in (termios.ECHO,termios.ICANON,termios.ISIG): assert bool(initial[3]&bit)==bool(after[3]&bit), 'terminal local mode not restored'
    print('PTY: idle, cursor, nested completion, Unicode word deletion, mouse/extended keys, slow-call cancellation, double Esc, terminal restoration OK')
finally:
    if process.poll() is None:
        process.terminate(); process.wait(timeout=3)
    server.shutdown();os.close(master);os.close(slave)
