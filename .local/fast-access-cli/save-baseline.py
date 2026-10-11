import pathlib,tarfile,hashlib,json
root=pathlib.Path.cwd(); dest=root/'.local/fast-access-cli'
files=['server/cmd/multica/main.go','server/cmd/multica/cmd_capabilities.go','server/internal/service/builtin_skills/multica-platform/references/overview.md']
with tarfile.open(dest/'BASELINE.tar','w') as t:
 for f in files:t.add(f,arcname=f)
(dest/'baseline-hashes.json').write_text(json.dumps({f:hashlib.sha256((root/f).read_bytes()).hexdigest() for f in files},indent=2))
