This file contains rules and guidelines for working with this repository. 

0. For any spawned persistent process provide command to kill it. Do not leave hanging running services unless it's explicitly asked for.
1. This is a Go repository. Use `go` through the repo toolchain fallback used in `Taskfile.yml` when needed: `GO_BIN="${GO:-$(command -v go || printf .tools/go/bin/go)}"`.
2. After introducing new Go dependencies, update `go.mod` and `go.sum`.
3. This repository uses Taskfile.yml to simplify running tests or start app, keep it up to date after changes.
4. Use `task test` to run tests. 
5. This proxy tries to be as transparent as possible, so try to avoid things which can break any requests.
6. Configs are stored in `configs/config.yaml`, with keys in `configs/.env`; Go config loading handles env substitution.
7. `task run` runs the proxy, `task run:reload` currently runs the same Go server without autoreload, `task forwarder` runs the TCP forwarder, and `task forwarder:http` runs the HTTP reverse forwarder.
8. By default the forwarders are not needed.
11. When unsure about library usage, prefer official Go package docs or the installed module source in the Go module cache.
