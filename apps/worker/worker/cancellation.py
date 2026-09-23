"""Read-only worker authorization; API failures leave the SQS job unacknowledged."""
import json
from http.client import HTTPException
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen


class CancellationUnavailable(Exception):
    """Dependency failure: pause this worker, rather than retrying the job."""


def is_cancelled(api_url, token, batch_id):
    request = Request(
        api_url.rstrip("/") + "/internal/batches/" + batch_id + "/cancellation",
        headers={"Authorization": "Bearer " + token},
    )
    try:
        with urlopen(request, timeout=5) as response:
            body = json.load(response)
        if not isinstance(body, dict) or type(body.get("cancelled")) is not bool:
            raise ValueError("invalid cancellation response")
        return body["cancelled"]
    except HTTPError as exc:
        # Unknown/malformed batch requests are job failures, not a global outage.
        if exc.code in (400, 404):
            raise
        raise CancellationUnavailable(f"cancellation API HTTP {exc.code}") from exc
    except (URLError, OSError, HTTPException, ValueError) as exc:
        raise CancellationUnavailable("cancellation API unavailable or invalid response") from exc
