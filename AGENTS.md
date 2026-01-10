This file contains rules and guidelines for working with this repository. 

0. For any spawned persistent process provide command to kill it. Do not leave hanging running services unless it's explicitly asked for.
1. This is UV repository, to run python code use `uv run <command>`.
2. After introducing new dependencies, update `pyproject.toml` file. 
3. This repository uses Taskfile.yml to simplify running tests or start app, keep it up to date after changes.
4. Use `task test` to run tests. 
5. This proxy tries to be as transparent as possible, so try to avoid things which can break any requests.
6. Configs are stored in `configs/config.yaml`, with keys in `configs/.env`. There are special utility scripts for streamlined config (and key) loading.
7. `task run` to run proxy, `task run:reload` to run proxy with autoreload for autoreloading app on changes, task forwarder to run forwarder. 
8. By default `forwarder` in not needed; by default it's using different .venv(.venv_fwd).
9. Ask user if they want to update `README.md` and `docs/` files to reflect changes after work is done. 
10. For big tasks, write unit tests as you go and run them in the process, instead of doing everything at once and then testing everything at once. 
11. When unsure about any library usage you can search web for docs of the library, or check library installed in the project venv.
