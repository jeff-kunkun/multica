from pathlib import Path
import subprocess,json,hashlib,os,shutil,difflib
root=Path.cwd(); out=root/'.scratch/dene-1787/transaction';out.mkdir(exist_ok=True)
original=root/'.scratch/dene-1787/cmd_agent.baseline.go';baseline=out/'BASELINE.go';shutil.copyfile(original,baseline)
modified=out/'MODIFIED_FILE.go';shutil.copyfile(root/'server/cmd/multica/cmd_agent.go',modified)
patch=out/'DIFF_FILE.patch';patch.write_text(''.join(difflib.unified_diff(baseline.read_text().splitlines(True),modified.read_text().splitlines(True),fromfile='BASELINE.go',tofile='MODIFIED_FILE.go')))
rollback=out/'ROLLBACK.sh';rollback.write_text('#!/bin/sh\nset -eu\n[ "$#" -eq 1 ] || { echo "usage: ROLLBACK.sh target-copy" >&2; exit 2; }\nscript_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)\ncp "$script_dir/BASELINE.go" "$1"\n');rollback.chmod(0o755)
restored=out/'restored.go';shutil.copyfile(modified,restored)
log=[]
def run(args,cwd=root):
 p=subprocess.run(args,cwd=cwd,text=True,capture_output=True);log.extend(['COMMAND '+json.dumps([str(x) for x in args]),'STDOUT\n'+p.stdout,'STDERR\n'+p.stderr,'EXIT '+str(p.returncode)]);assert p.returncode==0,p.stderr;return p
run([str(rollback),str(restored)])
assert restored.read_bytes()==baseline.read_bytes()
for role,source in [('BASELINE',baseline),('MODIFIED',modified),('ROLLBACK',restored)]:
 overlay=out/(role+'.json');overlay.write_text(json.dumps({'Replace':{str(root/'server/cmd/multica/cmd_agent.go'):str(source)}}))
 binary=out/(role.lower()+'-cli');run(['go','build','-overlay',str(overlay),'-o',str(binary),'./cmd/multica'],root/'server')
 log.append('ROLE '+role+' HASH '+hashlib.sha256(source.read_bytes()).hexdigest())
 result=run([str(binary),'agent','update','--help'])
 present='--series string' in result.stdout
 log.append('OBSERVED series_flag='+str(present));assert present==(role=='MODIFIED')
 binary.unlink();overlay.unlink()
reconstructed=out/'reconstructed.go';shutil.copyfile(baseline,reconstructed)
run(['patch',str(reconstructed),str(patch)])
assert reconstructed.read_bytes()==modified.read_bytes()
(out/'VERIFICATION.txt').write_text('\n'.join(log)+'\nRestored SHA256 matches original; reconstructed SHA256 matches modified.\n')
for p in [modified,patch,out/'VERIFICATION.txt',rollback]:
 assert p.read_bytes();print(str(p),hashlib.sha256(p.read_bytes()).hexdigest())
