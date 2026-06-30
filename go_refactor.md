This is a small plan for refactor, with definitions and things like that. This is more "what we want to achieve/have" and less "what we have right now"

## What is this app

This is app for LLM OPS. Basically, it should provide

- proxying real API keys and LLM endpoints
- load balancing 
- reparsing and other different streamed/non-streamed modifications
- priority queue
- limiting: rpm, rps, concurrency etc
- observability: logging, endpoints health, etc

## Definitions

### Node

Node is a singular entitity for a served model, representing just one model endpoint. 
Node can belong to a provider, in this case it resolves its parameters in Provider -> Node specific overrides. 

Node parameters:
- API key
- Limits(RPS, conc etc)
- endpoints type:link 
- model name
- default paratemers (and if they overwrite requests parameters or not)
- Allowed client keys (all `Model` keys by default)

### Provider

Provides is a easy way to group nodes which share some parameters, like

- API key
- RPS, concurrency and other limits
- Supported endpoints

- `openai`: default.
- `responses`: new openai-backed format of requests with websockets
- `anthropic`
- `passthrough`: does minimal HTTP proxying through `/p1/{model_name}/*`.

### API keys

Upstream API keys are defined per-provider or per-node
App api keys (client facing ones) follow "yallmp-{uuid}" schema.
App api keys has short unique aliaces. Keys support complex grouping system of the following schema
"yallmp-{group_uuid}-{key_uuid}", both uuid generate with no "-" symbol in them. 

### Model (client facing)

A model entity is some group of `nodes`.
When a client does a request with some model, the app takes the work of forwarding that request to the node. 
- In case of follow-up request it should try to hit the same node so there is a cache hit
- App tries to balance load according to various rules
- Model has list of client keys which are allowed for the requests, limits are defined per-key

### Limits, groups, priorities

There's a complex system of limits. When a request arrives for some model:

1. Request resolution. 
We resolve get the target model, key group + key, and the pool of nodes inside the model
2. Balancing.
We're picking the node. 
If this is a follow up request, we identify 

naaah idk what else to write all there
For the first pass let's just add 
1) Policy manager - it checks if the request is allowed
2) Admission manager - it checks current queue, priorities etc
Leave admission manager with fat #TODO since I'm not ready to fully define the surface for now


### Request/response modification

1) Request modification
As was described already, the app is able to force some parameters like temperature to stay at certain values. 
2) Response modification
App exposes several ways to modify response, both for streaming and non-streaming requests
- Parse reasoning into content or outside of content
- Tool call parsing on the side of app with
- Translation from one api type to another
These may require jinja templates for the model(node)

### Logging

The basic logging includes:
- In-memory app state for current load, ongoing requests, etc
- Historical data for tokens, time or requests etc
- On-disk full requests saved, with cleanup policy

The webapp part allows looking at logs, it links historical data with still existing on-disk full requests

### Webapp

Frontend enables an easy way to read/modify config, read of logs etc. 
Frontend target stack: Vite + React + shadcn/ui.

### Forwarder
We should add similar to current forwarding options right inside the app

### Running
App uses taskfile for ease of running/testing. 

### Testing

App should have deep testing: 
- Unit tests
- Deep smoke tests, with fake client and upstream node(s) to check how things actually flow through the app

### Refactor focus

The priority should be he same working state as of now, with only drop for multi key support as there's still no full understanding of how this feature should work. 
Antropic and responses endpoints/translations are not the priority and should be left for the follow up passes
During work, create state.md file and write there implemented parts. 
The app design should be modular where possible, without big monolith code files. 
references.md file has links to similar projects which will help 
Feel free to nuke docs alltogether, they're stale and should not be taken as source of truth. 
