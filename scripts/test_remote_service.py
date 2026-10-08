#!/usr/bin/env python3
"""Real binary acceptance with isolated ledger and credentials; no live state."""
import concurrent.futures
import hashlib
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

binary = str(Path(sys.argv[1]).resolve())
with tempfile.TemporaryDirectory(prefix='squad-remote-test-') as tmp:
    root = Path(tmp)
    repo = root / 'ledger'
    repo.mkdir()
    home = root / 'home'
    home.mkdir()
    env = {'PATH': os.environ['PATH'], 'HOME': str(home), 'SQUAD_HOME': str(home),
           'SQUAD_NO_AUTO_DAEMON': '1', 'SQUAD_NO_BROWSER': '1', 'SQUAD_NO_HYGIENE': '1'}
    def local(*args):
        return subprocess.run(args, cwd=repo, env=env, text=True, capture_output=True, timeout=30)
    assert local('git', 'init', '-q').returncode == 0
    init = local(binary, 'init', '--yes')
    assert init.returncode == 0, init.stderr
    tokens = [secrets.token_urlsafe(32) for _ in range(3)]
    config = {'workspace': str(repo), 'home': str(home), 'receipts': str(root/'receipts'), 'clients': [
        {'id': f'client-{i}', 'token_sha256': hashlib.sha256(token.encode()).hexdigest(),
         'agent': f'agent-{i}', 'session': f'native-{i}', 'role': 'controller' if i == 2 else 'worker'}
        for i, token in enumerate(tokens)]}
    conf = root/'service.json'
    conf.write_text(json.dumps(config))
    with socket.socket() as sock:
        sock.bind(('127.0.0.1',0))
        port = sock.getsockname()[1]
    address=f'http://127.0.0.1:{port}'
    def start():
        p = subprocess.Popen([binary,'service','--config',str(conf),'--listen',f'127.0.0.1:{port}'],cwd=repo,env=env,stdout=subprocess.DEVNULL,stderr=subprocess.PIPE)
        for _ in range(100):
            try:
                with urllib.request.urlopen(address+'/healthz',timeout=1) as r:
                    assert json.load(r)['status']=='ok'
                return p
            except (OSError, urllib.error.URLError):
                if p.poll() is not None: raise AssertionError(p.stderr.read().decode())
                time.sleep(.05)
        p.terminate()
        raise AssertionError('service readiness timeout')
    def command(index,*args,key=None):
        child=dict(env,SQUAD_REMOTE_URL=address,SQUAD_REMOTE_TOKEN=tokens[index])
        if key: child['SQUAD_REQUEST_ID']=key
        return subprocess.run([binary,*args],cwd=root,env=child,text=True,capture_output=True,timeout=20)
    def mcp(index,name,args):
        request={'jsonrpc':'2.0','id':1,'method':'tools/call','params':{'name':name,'arguments':args}}
        req=urllib.request.Request(address+'/mcp',data=json.dumps(request,indent=2).encode(),headers={'Authorization':'Bearer '+tokens[index],'Content-Type':'application/json'})
        with urllib.request.urlopen(req,timeout=20) as r:return json.load(r)
    proc=start()
    try:
        for i in range(3):
            r=command(i,'register'); assert r.returncode==0,r.stderr
            r=command(i,'whoami'); assert f'agent-{i}' in r.stdout,(r.stdout,r.stderr)
        created=command(0,'new','task','remote race','--ready',key='create-once-123456')
        assert created.returncode==0,created.stderr
        import re
        item=re.search(r'(TASK-\d+)',created.stdout).group(1)
        replay=command(0,'new','task','remote race','--ready',key='create-once-123456')
        assert replay.stdout==created.stdout
        reserve=command(2,'dispatch','reserve','TEST-REMOTE-RESERVATION','--source','test:remote','--json')
        assert reserve.returncode==0,reserve.stderr
        reservation=json.loads(reserve.stdout)
        attached=command(2,'dispatch','attach','TEST-REMOTE-RESERVATION','--item',item,'--generation',str(reservation['generation']))
        assert attached.returncode==0,attached.stderr
        with concurrent.futures.ThreadPoolExecutor(2) as pool:
            results=list(pool.map(lambda i:command(i,'claim',item,'--intent','remote atomic race'),[0,1]))
        assert sum(r.returncode==0 for r in results)==1,[(r.returncode,r.stdout,r.stderr) for r in results]
        winner=next(i for i,r in enumerate(results) if r.returncode==0)
        write=mcp(winner,'squad_say',{'message':'remote MCP acceptance','to':item})
        assert 'error' not in write,write
        who=mcp(1-winner,'squad_whoami',{})
        assert 'error' not in who,who
        raw='{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"squad_release","arguments":{"item_id":"'+item+'","agent_id":"agent-'+str(winner)+'","agent_id":null}}}'
        req=urllib.request.Request(address+'/mcp',data=raw.encode(),headers={'Authorization':'Bearer '+tokens[1-winner],'Content-Type':'application/json'})
        try:
            urllib.request.urlopen(req,timeout=10)
            raise AssertionError('duplicate/null identity bypass admitted')
        except urllib.error.HTTPError as denied:
            assert denied.code==403
        inspected=command(0,'claim-inspect',item)
        assert f'agent-{winner}' in inspected.stdout,'duplicate/null attack released victim claim'
        denied=command(1-winner,'release',item)
        assert denied.returncode!=0,'nonowner released claim'
        # Distinct token cannot forge an actor even with a valid command shape.
        denied=command(0,'register','--as','agent-1')
        assert denied.returncode!=0
        stale=command(2,'dispatch','attach','TEST-REMOTE-RESERVATION','--item',item,'--generation',str(reservation['generation']+1))
        assert stale.returncode!=0,'stale generation admitted'
        # Exercise event pointers through remote transports under real custody.
        bound=command(2,'dispatch','bind','TEST-REMOTE-RESERVATION','--thread-id',f'native-{winner}','--generation',str(reservation['generation']))
        assert bound.returncode==0,bound.stderr
        bound=command(2,'dispatch','controller-bind','--native-session','native-2')
        assert bound.returncode==0,bound.stderr
        epoch=json.loads(bound.stdout)['epoch']
        receiver=command(2,'dispatch','receiver-bind','--native-session','native-2','--incarnation','test-receiver','--epoch',str(epoch))
        assert receiver.returncode==0,receiver.stderr
        import sqlite3
        with sqlite3.connect(next(home.rglob('global.db'))) as db:
            outcome=db.execute("SELECT max(id) FROM messages WHERE agent_id=?",(f'agent-{winner}',)).fetchone()[0]
        event_args=['terminal-events','publish','--reservation','TEST-REMOTE-RESERVATION','--generation',str(reservation['generation']),'--worker-session',f'native-{winner}','--kind','handoff-complete','--outcome',str(outcome)]
        published=command(winner,*event_args)
        assert published.returncode==0,published.stderr
        event=json.loads(published.stdout)['event_id']
        again=command(winner,*event_args)
        assert again.returncode==0 and json.loads(again.stdout)['event_id']==event
        early=command(2,'terminal-events','ack',event,'--native-session','native-2','--note','fixture handled')
        assert early.returncode!=0,'undelivered event was acknowledged'
        polled=mcp(2,'squad_terminal_events_poll',{'native_session':'native-2','delivery_session':'test-receiver'})
        assert event in str(polled) and 'error' not in polled,polled
        stale=command(2,'terminal-events','poll','--native-session','native-2','--delivery-session','stale-receiver')
        assert stale.returncode!=0,'stale receiver read accepted'
        delivered=command(2,'terminal-events','delivered',event,'--native-session','native-2','--delivery-session','test-receiver')
        assert delivered.returncode==0,delivered.stderr
        for _ in range(2):
            ack=command(2,'terminal-events','ack',event,'--native-session','native-2','--note','fixture handled')
            assert ack.returncode==0,ack.stderr
        polled=command(2,'terminal-events','poll','--native-session','native-2','--delivery-session','test-receiver')
        assert polled.returncode==0 and json.loads(polled.stdout)==[],polled.stderr
        proc.terminate();proc.wait(timeout=10)
        proc=start()
        after=command(0,'new','task','remote race','--ready',key='create-once-123456')
        assert after.stdout==created.stdout
        inspect=command(0,'claim-inspect',item)
        assert inspect.returncode==0 and f'agent-{winner}' in inspect.stdout,(inspect.stdout,inspect.stderr)
        # Both transports enforce administrator-defined verification gates.
        cfgfile=repo/'.squad/config.yaml'
        cfgfile.write_text('agent:\n  claim_concurrency: 10\nverification:\n  pre_commit:\n    - cmd: "false"\ndefaults:\n  evidence_required: []\n')
        closed=command(winner,'done',item)
        assert closed.returncode!=0,'CLI skipped false verification gate'
        closed=mcp(winner,'squad_done',{'item_id':item})
        assert 'error' in closed and 'verification gates failed' in str(closed),'MCP skipped false verification gate'
        released=command(winner,'release',item)
        assert released.returncode==0,released.stderr
        print('PASS: real CLI/MCP identities, writes, atomic claim race, nonowner and identity rejection, reservation fencing, event publish/poll/delivered/ack fences and idempotency, durable restart and request replay')
    finally:
        proc.terminate();proc.wait(timeout=10)
