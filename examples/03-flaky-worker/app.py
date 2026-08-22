import os
import time


def handler(event, context):
    # Behavior is driven entirely by environment variables, which mini-lambda
    # bakes into the container at cold start from the function's --env config.
    mode = os.environ.get("MODE", "ok")
    sleep_seconds = float(os.environ.get("SLEEP_SECONDS", "0"))

    if sleep_seconds > 0:
        time.sleep(sleep_seconds)

    if mode == "fail":
        raise RuntimeError("flaky worker asked to fail (MODE=fail)")

    return {
        "mode": mode,
        "sleptSeconds": sleep_seconds,
        "requestId": context.aws_request_id,
        "event": event,
    }
