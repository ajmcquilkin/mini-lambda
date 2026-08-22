# 01-echo-python

The minimal example: a one-file Python handler that echoes the event back wrapped in `{"echo": <event>}`. It runs on the AWS Lambda Python base image with no code changes, which is the whole point of mini-lambda: images built for real Lambda run here unmodified.

## Files

- `app.py` — the handler. `handler(event, context)` returns `{"echo": event}`.
- `Dockerfile` — `public.ecr.aws/lambda/python:3.12` base, copies the handler, sets `CMD` to `app.handler`.

## Run it

Build the image (from this directory):

```sh
docker build -t echo-python:latest .
```

Start the daemon if it is not already running:

```sh
mini-lambda serve &
```

Register the function from the local image and invoke it:

```sh
mini-lambda function create echo --image echo-python:latest --memory 256 --timeout 10
mini-lambda function invoke echo --payload '{"name":"ada"}'
```

Expected output:

```json
{"echo": {"name": "ada"}}
```

The invoke is just sugar over the public HTTP API; the raw equivalent is:

```sh
curl -s -XPOST http://127.0.0.1:9000/2015-03-31/functions/echo/invocations -d '{"name":"ada"}'
# => {"echo": {"name": "ada"}}
```

## Clean up

```sh
mini-lambda function rm echo
```
