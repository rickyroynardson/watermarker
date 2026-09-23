"""Read-only worker authorization; API failures leave the SQS job unacknowledged."""
import json
from urllib.request import Request, urlopen


def is_cancelled(api_url, token, batch_id):
    request = Request(
        api_url.rstrip("/") + "/internal/batches/" + batch_id + "/cancellation",
        headers={"Authorization": "Bearer " + token},
    )
    with urlopen(request, timeout=5) as response:
        value = json.load(response).get("cancelled")
    if type(value) is not bool:
        raise ValueError("invalid cancellation response")
    return value
