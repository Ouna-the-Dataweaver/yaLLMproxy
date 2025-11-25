#!/bin/bash

source .venv/bin/activate
uvicorn proxy:app --host 0.0.0.0 --port 17771