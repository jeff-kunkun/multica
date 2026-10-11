from pathlib import Path
import subprocess,hashlib,difflib,shutil,json
root=Path.cwd(); out=root/'.scratch/DENE-1808/transaction';out.mkdir(parents=True,exist_ok=True)
target='server/internal/workspacelink/decide.go'
base=subprocess.check_output(['git','show','HEAD:'+target]); modified=(root/target).read_bytes()
(out/'BASELINE.go').write_bytes(base);(out/'MODIFIED_FILE.go').write_bytes(modified)
(out/'DIFF_FILE.patch').write_text(''.join(difflib.unified_diff(base.decode().splitlines(True),modified.decode().splitlines(True),fromfile='BASELINE.go',tofile='MODIFIED_FILE.go')))
(out/'ROLLBACK.sh').write_text('#!/bin/sh\nset -eu\n[ "$#" -eq 1 ] || { echo "usage: ROLLBACK.sh target-copy" >&2; exit 2; }\nscript_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)\ncp "$script_dir/BASELINE.go" "$1"\n')
(out/'ROLLBACK.sh').chmod(0o755)
probe='''package workspacelink
import("fmt";"testing";"github.com/multica-ai/multica/server/internal/permission")
func TestDeliveryProbe(t *testing.T){
 fmt.Printf("viewer_agent=%v guest=%v source_agent=%v human=%v\\n",Decide(Op("deliver"),SideViewer,Actor{Role:permission.RoleMember,IsAgent:true}),Decide(Op("deliver"),SideViewer,Actor{Role:permission.RoleGuest,IsAgent:true}),Decide(Op("deliver"),SideSource,Actor{Role:permission.RoleOwner,IsAgent:true}),Decide(Op("deliver"),SideViewer,Actor{Role:permission.RoleOwner}))
}
'''
runroot=root/'server/.scratch/DENE-1808-transaction';runroot.mkdir(parents=True,exist_ok=True)
ledger=['TARGET='+str(root/target),'BASE_COMMIT='+subprocess.check_output(['git','rev-parse','HEAD'],text=True).strip(),'changed_symbol=Decide / OpDeliver / OpSetReception','original_sha256='+hashlib.sha256(base).hexdigest(),'modified_sha256='+hashlib.sha256(modified).hexdigest()]
for role,content in [('BASELINE',base),('MODIFIED',modified),('ROLLBACK',modified)]:
 d=runroot/role;d.mkdir(exist_ok=True);(d/'decide.go').write_bytes(content);(d/'probe_test.go').write_text(probe)
 if role=='ROLLBACK':
  r=subprocess.run([str(out/'ROLLBACK.sh'),str(d/'decide.go')],capture_output=True,text=True);ledger.append('ROLLBACK_COMMAND='+json.dumps(r.args)+'\nstdout='+r.stdout+'\nstderr='+r.stderr+'\nexit='+str(r.returncode))
 cmd=['go','test','-v',str(d/'decide.go'),str(d/'probe_test.go'),'-count=1']
 r=subprocess.run(cmd,cwd=root/'server',capture_output=True,text=True)
 ledger.append(role+'\ncommand='+json.dumps(cmd)+'\ninput=Op(deliver), viewer/member agent; guest agent; source owner agent; human owner\nstdout:\n'+r.stdout+'stderr:\n'+r.stderr+'exit='+str(r.returncode)+'\nsha256='+hashlib.sha256((d/'decide.go').read_bytes()).hexdigest())
 if r.returncode: raise RuntimeError(r.stderr)
assert (runroot/'ROLLBACK/decide.go').read_bytes()==base
rebuild=out/'patch-check';rebuild.mkdir(exist_ok=True);(rebuild/'BASELINE.go').write_bytes(base)
p=subprocess.run(['patch','-o',str(rebuild/'MODIFIED_FILE.go'),str(rebuild/'BASELINE.go'),str(out/'DIFF_FILE.patch')],capture_output=True,text=True)
assert p.returncode==0 and (rebuild/'MODIFIED_FILE.go').read_bytes()==modified
ledger.append('patch_reconstruction=verified\nrestored_hash_equals_original=true\nMODIFIED_FILE retains requested GOAL=true')
(out/'VERIFICATION.txt').write_text('\n\n'.join(ledger))
for name in ['MODIFIED_FILE.go','DIFF_FILE.patch','VERIFICATION.txt','ROLLBACK.sh']:
 data=(out/name).read_bytes();print(str(out/name),len(data),hashlib.sha256(data).hexdigest())
print((out/'VERIFICATION.txt').read_text())
