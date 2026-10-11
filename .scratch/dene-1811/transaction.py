from pathlib import Path
import subprocess,hashlib,shutil
root=Path.cwd(); out=root/'.scratch/dene-1811'; probe=out/'probe'; pkg=probe/'internal/workspacelink';pkg.mkdir(parents=True,exist_ok=True)
(probe/'go.mod').write_text('module github.com/multica-ai/multica/server\n\ngo 1.23\n')
(probe/'internal/permission').mkdir(exist_ok=True)
shutil.copyfile(root/'server/internal/permission/permission.go',probe/'internal/permission/permission.go')
original=subprocess.check_output(['git','show','951f3f96cfb94919bff249efbb80a4b34e0dc8ef:server/internal/workspacelink/decide.go'])
modified=(root/'server/internal/workspacelink/decide.go').read_bytes()
(out/'BASELINE.go').write_bytes(original);(out/'MODIFIED_FILE.go').write_bytes(modified)
patch=subprocess.run(['diff','-u',str(out/'BASELINE.go'),str(out/'MODIFIED_FILE.go')],capture_output=True)
(out/'DIFF_FILE.patch').write_bytes(patch.stdout)
rollback=out/'ROLLBACK.sh';rollback.write_text('#!/bin/sh\nset -eu\n[ "$#" -eq 1 ] || exit 2\ncp "$(dirname "$0")/BASELINE.go" "$1"\n');rollback.chmod(0o755)
(pkg/'probe_test.go').write_text('''package workspacelink
import("fmt";"reflect";"testing";"github.com/multica-ai/multica/server/internal/permission")
func TestProbe(t *testing.T){
a:=Actor{Role:permission.RoleAdmin};f:=reflect.ValueOf(&a).Elem().FieldByName("SourceRole");if f.IsValid(){f.SetString("owner")}
fmt.Printf("viewer admin + source owner: %v\\n",Decide(OpUpdateProjects,SideViewer,a))
a.IsAgent=true;fmt.Printf("agent with same roles: %v\\n",Decide(OpUpdateProjects,SideViewer,a))
}
''')
ledger=[]
for label,data in [('BASELINE',original),('MODIFIED',modified),('ROLLBACK',modified)]:
 target=pkg/'decide.go';target.write_bytes(data)
 if label=='ROLLBACK':subprocess.run([str(rollback),str(target)],check=True);assert target.read_bytes()==original
 r=subprocess.run(['go','test','-v','./internal/workspacelink','-run','TestProbe','-count=1'],cwd=probe,capture_output=True,text=True)
 ledger.append(f'{label}\ncommand: go test -v ./internal/workspacelink -run TestProbe -count=1\ninput: viewer admin, source owner; same roles as agent\nsha256: {hashlib.sha256(target.read_bytes()).hexdigest()}\nexit: {r.returncode}\nstdout:\n{r.stdout}\nstderr:\n{r.stderr}')
 assert r.returncode==0
(out/'VERIFICATION.txt').write_text('\n'.join(ledger))
for name in ['MODIFIED_FILE.go','DIFF_FILE.patch','VERIFICATION.txt','ROLLBACK.sh']:
 p=out/name;print(p, len(p.read_bytes()))
print((out/'VERIFICATION.txt').read_text())
