from pathlib import Path
import subprocess,hashlib,difflib,shutil,json
root=Path.cwd();out=root/'.scratch/DENE-1808/transaction';out.mkdir(parents=True,exist_ok=True)
if (out/'VERIFICATION.txt').exists():shutil.copyfile(out/'VERIFICATION.txt',out/'VERIFICATION.superseded.txt')
target='server/internal/workspacelink/managed.go'
base=subprocess.check_output(['git','show','HEAD:'+target]);modified=(root/target).read_bytes()
(out/'BASELINE.go').write_bytes(base);(out/'MODIFIED_FILE.go').write_bytes(modified)
(out/'DIFF_FILE.patch').write_text(''.join(difflib.unified_diff(base.decode().splitlines(True),modified.decode().splitlines(True),fromfile='BASELINE.go',tofile='MODIFIED_FILE.go')))
(out/'ROLLBACK.sh').write_text('#!/bin/sh\nset -eu\n[ "$#" -eq 1 ] || { echo "usage: ROLLBACK.sh target-copy" >&2; exit 2; }\nscript_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)\ncp "$script_dir/BASELINE.go" "$1"\n');(out/'ROLLBACK.sh').chmod(0o755)
probe='''package workspacelink
import("fmt";"testing")
func TestDeliveryProbe(t *testing.T){
 _,_,send:=matchManagedRoute("POST","/api/chat/sessions/10000000-0000-0000-0000-000000000001/messages")
 _,_,list:=matchManagedRoute("GET","/api/chat/directory")
 _,_,create:=matchManagedRoute("POST","/api/chat/sessions")
 fmt.Printf("managed_chat_send=%v managed_chat_list=%v create_chat=%v\\n",send,list,create)
}
'''
runroot=root/'server/internal/.scratch/DENE-1808-transaction'
ledger=['TARGET='+str(root/target),'BASE_COMMIT='+subprocess.check_output(['git','rev-parse','HEAD'],text=True).strip(),'changed_symbol=managedRoutes chat.list/chat.get/chat.messages/chat.send + own-chat gate','original_sha256='+hashlib.sha256(base).hexdigest(),'modified_sha256='+hashlib.sha256(modified).hexdigest(),'Newest issue direction 2026-10-11T08:47:58Z: managed delivery; supersedes earlier standalone OpDeliver experiment.']
for role,content in [('BASELINE',base),('MODIFIED',modified),('ROLLBACK',modified)]:
 d=runroot/role;d.mkdir(parents=True,exist_ok=True)
 for source in (root/'server/internal/workspacelink').glob('*.go'):
  if not source.name.endswith('_test.go'):shutil.copyfile(source,d/source.name)
 (d/'managed.go').write_bytes(content);(d/'probe_test.go').write_text(probe)
 if role=='ROLLBACK':
  r=subprocess.run([str(out/'ROLLBACK.sh'),str(d/'managed.go')],capture_output=True,text=True);ledger.append('ROLLBACK_COMMAND='+json.dumps(r.args)+'\nstdout='+r.stdout+'\nstderr='+r.stderr+'\nexit='+str(r.returncode))
 cmd=['go','test','-v','./'+str(d.relative_to(root/'server')),'-run','^TestDeliveryProbe$','-count=1']
 r=subprocess.run(cmd,cwd=root/'server',capture_output=True,text=True)
 ledger.append(role+'\ncommand='+json.dumps(cmd)+'\ninput=POST own chat messages; GET chat directory; POST create chat\nstdout:\n'+r.stdout+'stderr:\n'+r.stderr+'exit='+str(r.returncode)+'\nsha256='+hashlib.sha256((d/'managed.go').read_bytes()).hexdigest())
 if r.returncode:raise RuntimeError(r.stderr)
assert (runroot/'ROLLBACK/managed.go').read_bytes()==base
rebuild=out/'managed-patch-check';rebuild.mkdir(exist_ok=True);(rebuild/'BASELINE.go').write_bytes(base)
p=subprocess.run(['patch','-o',str(rebuild/'MODIFIED_FILE.go'),str(rebuild/'BASELINE.go'),str(out/'DIFF_FILE.patch')],capture_output=True,text=True)
assert p.returncode==0 and (rebuild/'MODIFIED_FILE.go').read_bytes()==modified
ledger.append('patch_reconstruction=verified\nrestored_hash_equals_original=true\nMODIFIED_FILE retains requested GOAL=true')
(out/'VERIFICATION.txt').write_text('\n\n'.join(ledger))
for name in ['MODIFIED_FILE.go','DIFF_FILE.patch','VERIFICATION.txt','ROLLBACK.sh']:
 data=(out/name).read_bytes();print(str(out/name),len(data),hashlib.sha256(data).hexdigest())
print((out/'VERIFICATION.txt').read_text())
