# Disposable local test credential; never reads or alters production credentials.
import hashlib,json,os,pathlib,secrets,subprocess,tempfile
root=pathlib.Path.cwd()
values={}
for line in (root/'.env.worktree').read_text().splitlines():
 if '=' in line and not line.startswith('#'):
  k,v=line.split('=',1);values[k]=v.strip('\"\'')
assert 'multica_dene_1803_' in values['DATABASE_URL']
token='mul_'+secrets.token_hex(20)
hashed=hashlib.sha256(token.encode()).hexdigest()
def sql(statement):
 p=subprocess.run(['psql',values['DATABASE_URL'],'-X','-At','-v','ON_ERROR_STOP=1'],input=statement,text=True,capture_output=True)
 if p.returncode: raise RuntimeError(p.stderr)
 return p.stdout.strip()
id=sql("INSERT INTO personal_access_token(user_id,name,token_hash,token_prefix,expires_at) SELECT id,'local overview acceptance','"+hashed+"','"+token[:12]+"',now()+interval '10 minutes' FROM \"user\" WHERE email='dev@localhost' RETURNING id;").splitlines()[0]
try:
 with tempfile.TemporaryDirectory(prefix='overview-local-cli-') as cwd:
  env={k:v for k,v in os.environ.items() if not k.startswith('MULTICA_')}
  env.update(MULTICA_SERVER_URL='http://localhost:18957',MULTICA_TOKEN=token,MULTICA_WORKSPACE_ID='18030000-0000-4000-8000-000000000001')
  outputs={}
  commands=[['overview','--output','json'],['overview','--compact','--output','json'],['overview','--project','Mobile release','--output','json'],['overview','--agent','QA Builder','--output','json'],['overview','--compact']]
  for i,args in enumerate(commands):
   p=subprocess.run([str(root/'.scratch/overview/multica'),*args],cwd=cwd,env=env,capture_output=True,text=True)
   (root/f'.scratch/overview/live-cli-{i}.txt').write_text(p.stdout)
   print('multica '+' '.join(args), 'exit='+str(p.returncode),'bytes='+str(len(p.stdout.encode())))
   if p.returncode: raise RuntimeError(p.stderr)
   if args[-1]=='json': outputs[i]=json.loads(p.stdout)
  assert outputs[0]['counts']==outputs[1]['counts']
  for args,section,envelope in [(['agent','list'],'agents',None),(['project','list'],'projects','projects'),(['autopilot','list'],'automations','autopilots'),(['skill','list'],'skills',None),(['squad','list'],'squads',None)]:
   p=subprocess.run([str(root/'.scratch/overview/multica'),*args,'--output','json'],cwd=cwd,env=env,capture_output=True,text=True)
   if p.returncode: raise RuntimeError(p.stderr)
   data=json.loads(p.stdout)
   if isinstance(data,dict): data=data.get(envelope or section,data.get(section,[]))
   assert len(data)==outputs[0]['counts'][section],(section,len(data),outputs[0]['counts'])
   print(section, 'overview/list count='+str(len(data)))
finally:
 sql("DELETE FROM personal_access_token WHERE id='"+id+"';")
