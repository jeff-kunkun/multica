import pathlib,subprocess,tarfile,io,json,hashlib,tempfile,shutil,os,threading,http.server
root=pathlib.Path.cwd(); d=root/'.local/fast-access-cli'
files=subprocess.check_output(['git','diff','--cached','--name-only'],text=True).splitlines()
with tarfile.open(d/'MODIFIED_FILE.tar','w') as t:
 for f in files:t.add(root/f,arcname=f)
modified={f:hashlib.sha256((root/f).read_bytes()).hexdigest() for f in files}
baseline=json.loads((d/'baseline-hashes.json').read_text())
copy=pathlib.Path(tempfile.mkdtemp(prefix='fast-access-rollback-'))
with tarfile.open(fileobj=io.BytesIO(subprocess.check_output(['git','archive','HEAD','server']))) as t:t.extractall(copy)
for f in baseline:
 p=copy/f
 assert hashlib.sha256(p.read_bytes()).hexdigest()==baseline[f]
# Reconstruct the modified sources from the original and verify every file.
subprocess.run(['git','apply','--unsafe-paths',str(d/'DIFF_FILE.patch')],cwd=copy,check=True)
for f,h in modified.items():assert hashlib.sha256((copy/f).read_bytes()).hexdigest()==h
subprocess.run([str(d/'ROLLBACK.sh'),str(copy)],check=True)
for f,h in baseline.items():assert hashlib.sha256((copy/f).read_bytes()).hexdigest()==h
for f in set(files)-set(baseline):assert not (copy/f).exists()
rollback_build=subprocess.run(['go','build','-o',str(d/'multica-rollback'),'./cmd/multica'],cwd=copy/'server',capture_output=True,text=True)
assert rollback_build.returncode==0,rollback_build.stderr
# Reapply the goal on the copy after rollback verification, leaving the original pristine.
subprocess.run(['git','apply','--unsafe-paths',str(d/'DIFF_FILE.patch')],cwd=copy,check=True)
for f,h in modified.items():assert hashlib.sha256((copy/f).read_bytes()).hexdigest()==h
fixture={'schema_version':'1','mode':'timestamp_snapshot','as_of':'2026-10-11T17:30:00Z','coverage_from':'2026-10-11T17:00:00Z','scope_revision':'test-scope','changes':[{'workspace_id':'ws-a','kind':'chat','id':'chat-1','title':'中文🙂','unread_count':2},{'workspace_id':'ws-b','kind':'issue','id':'issue-1','new_comment_count':1}],'active_runs':[],'complete':True,'active_runs_complete':True,'next_cursor':None,'checkpoint':'fixture-checkpoint','retention_floor':None}
for i in range(17):fixture['active_runs'].append({'workspace_id':['ws-a','ws-b'][i%2],'id':'run-'+str(i),'source_type':'chat','source_id':'chat-1','status':['queued','dispatched','running','waiting_local_directory','deferred'][i%5],'phase':['queued','executing','executing','waiting','waiting'][i%5]})
requests=[]
class Handler(http.server.BaseHTTPRequestHandler):
 def do_POST(self):
  body=json.loads(self.rfile.read(int(self.headers['Content-Length'])))
  assert self.path=='/api/fast-access/v1/read'
  requests.append({'method':'POST','path':self.path,'body':body})
  self.respond(fixture)
 def do_GET(self):
  requests.append({'method':'GET','path':self.path})
  self.respond(dict(fixture,changes=[]))
 def respond(self,data):
  raw=json.dumps(data,ensure_ascii=False,separators=(',',':')).encode()
  self.send_response(200);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw)
 def log_message(self,*args):pass
srv=http.server.ThreadingHTTPServer(('127.0.0.1',0),Handler)
thread=threading.Thread(target=srv.serve_forever);thread.start()
env=dict(os.environ,MULTICA_SERVER_URL='http://127.0.0.1:'+str(srv.server_port),MULTICA_TOKEN='mat_fixture_only',MULTICA_WORKSPACE_ID='ws-a',MULTICA_CONFIG_DIR=str(copy/'config'))
args=['changes','--since','2026-10-11T17:00:00Z','--all-workspaces','--output','json']
ledger={'target':str(root),'branch':subprocess.check_output(['git','branch','--show-current'],text=True).strip(),'baseline_commit':subprocess.check_output(['git','rev-parse','HEAD'],text=True).strip(),'original_hashes':baseline,'modified_hashes':modified,'rollback_hashes_match_original':True,'patch_reconstructs_modified':True,'rollback_build':{'command':['go','build','-o','multica-rollback','./cmd/multica'],'stdout':rollback_build.stdout,'stderr':rollback_build.stderr,'exit':rollback_build.returncode},'verification_kind':'HTTP fixture, not real API integration','behaviors':{}}
try:
 for role,binary in [('BASELINE','multica-baseline'),('MODIFIED','multica-modified'),('ROLLBACK','multica-rollback')]:
  before=len(requests);run=subprocess.run([str(d/binary)]+args,cwd=copy,env=env,capture_output=True,text=True)
  ledger['behaviors'][role]={'command':[str(d/binary)]+args,'stdout':run.stdout,'stderr':run.stderr,'exit':run.returncode,'business_requests':len(requests)-before,'stdout_bytes':len(run.stdout.encode())}
  if role=='MODIFIED':assert run.returncode==0 and len(requests)-before==1 and len(json.loads(run.stdout)['active_runs'])==17
  else:assert run.returncode!=0 and len(requests)==before
 before=len(requests);run=subprocess.run([str(d/'multica-modified'),'runs','--active','--all-workspaces','--output','json'],cwd=copy,env=env,capture_output=True,text=True)
 assert run.returncode==0 and len(requests)-before==1 and len(json.loads(run.stdout)['active_runs'])==17
 ledger['runs']={'command':['multica','runs','--active','--all-workspaces','--output','json'],'stdout':run.stdout,'stderr':run.stderr,'exit':run.returncode,'business_requests':len(requests)-before,'stdout_bytes':len(run.stdout.encode())}
 ledger['requests']=requests
finally:srv.shutdown();thread.join();srv.server_close()
(d/'VERIFICATION.txt').write_text(json.dumps(ledger,ensure_ascii=False,indent=2)+'\n')
# Reopen all four roles, verifying native parseability and content.
with tarfile.open(d/'MODIFIED_FILE.tar') as t:assert sorted(t.getnames())==sorted(files)
assert (d/'DIFF_FILE.patch').read_text().startswith('diff --git ')
assert json.loads((d/'VERIFICATION.txt').read_text())['rollback_hashes_match_original']
assert (d/'ROLLBACK.sh').read_text().startswith('#!/bin/sh')
subprocess.run(['sh','-n',str(d/'ROLLBACK.sh')],check=True)
print(json.dumps({k:{x:v[x] for x in ['exit','business_requests','stdout_bytes']} for k,v in ledger['behaviors'].items()},indent=2))
print('Source rollback hashes match; patch reconstructs modified; all four roles reopened.')
shutil.rmtree(copy)
