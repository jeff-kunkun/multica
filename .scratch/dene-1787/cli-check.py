import os,json,subprocess,pathlib,sys
root=pathlib.Path(__file__).resolve().parents[2]
env={k:v for k,v in os.environ.items() if not k.startswith('MULTICA_')}
env.update(MULTICA_SERVER_URL='http://localhost:18590',MULTICA_TOKEN=(root/'.scratch/dene-1787/local-test-token').read_text())
binary=str(root/'.scratch/dene-1787/multica')
def call(*args):
 r=subprocess.run([binary,*args,'--output','json'],env=env,cwd='/tmp',text=True,capture_output=True)
 if r.returncode: raise RuntimeError(r.stderr)
 return json.loads(r.stdout)
workspaces=call('workspace','list');env['MULTICA_WORKSPACE_ID']=next(w['id'] for w in workspaces if w['slug']=='dev')
if len(sys.argv)>1:
 result=call(*sys.argv[1:]);print(json.dumps(result,ensure_ascii=False));sys.exit()
runtimes=call('runtime','list')
seats=[]
for provider,want in [('claude','Claude'),('codex','Codex')]:
 rt=next(r for r in runtimes if r['provider']==provider)
 a=call('agent','create','--name',want+' verification','--runtime-id',rt['id'])
 assert a['series']==want and a['series_source']=='auto', a
 seats.append(a['id']);print('CREATE',want,a['series'],a['series_source'])
a=call('agent','update',seats[1],'--series','破甲系列');assert a['series']=='破甲系列' and a['series_source']=='manual';print('CLI SET',a['series'],a['series_source'])
a=call('agent','get',seats[1]);assert a['series']=='破甲系列';print('CLI READ',a['series'])
a=call('agent','update',seats[1],'--series','');assert a['series']=='Codex' and a['series_source']=='auto';print('CLI CLEAR',a['series'],a['series_source'])
(root/'.scratch/dene-1787/seat-ids.json').write_text(json.dumps(seats))
