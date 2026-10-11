import importlib.util, pathlib, json, sys
root=pathlib.Path(sys.argv[1]); sys.path.insert(0,str(root/'scripts'))
spec=importlib.util.spec_from_file_location('export_agents',root/'scripts/export-multica-agents.py')
m=importlib.util.module_from_spec(spec); sys.modules[spec.name]=m; spec.loader.exec_module(m)
errors=m.gate_i17_profile(pathlib.Path(sys.argv[2]).read_text(),live_description=json.loads(pathlib.Path(sys.argv[3]).read_text())['profile_description'])
print('I17: '+ ('PASS' if not errors else 'FAIL'))
for error in errors: print(error)
sys.exit(bool(errors))
