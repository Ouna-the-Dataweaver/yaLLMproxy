This file contains rules and guidelines for working with this repository. 

0. For any spawned persistent process provide command to kill it. Do not leave hanging running services unless it's explicitly asked for.
1. This is UV repository, to run python code use `uv run <command>`.
2. After introducing new dependencies, update `pyproject.toml` file. 
3. This repository uses Taskfile.yml to simplify running tests or start app, keep it up to date after changes.
4. Use `task test` to run tests. 
5. This proxy tries to be as transparent as possible, so try to avoid things which can break any requests.
6. Configs are stored in `configs/config_default.yaml` and `configs/config_added.yaml`, with keys in `configs/.env_default` and `configs/.env_added`. There are special utility scripts for streamlined config (and key) loading.
7. `task run` to run proxy, `task run:reload` to run proxy with autoreload for autoreloading app on changes, task forwarder to run forwarder. 
8. By default `forwarder` in not needed, but also by default it's using different .venv(.venv_fwd).
