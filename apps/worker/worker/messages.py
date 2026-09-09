"""Version 1 messages shared with the Go API."""

import json
from dataclasses import dataclass
from uuid import UUID


def canonical_uuid(value: str) -> str:
    if not isinstance(value, str) or str(UUID(value)) != value:
        raise ValueError("expected a canonical UUID")
    return value


def source_owner(key: str) -> str:
    if not isinstance(key, str):
        raise TypeError("expected a source key")
    parts = key.split("/")
    if len(parts) != 3 or parts[0] != "sources":
        raise ValueError("expected sources/<owner>/<upload> key")
    canonical_uuid(parts[2])
    return canonical_uuid(parts[1])


@dataclass(frozen=True)
class Job:
    batch_id: str
    image_id: str
    source_key: str
    watermark_key: str

    @classmethod
    def parse(cls, body: str) -> "Job":
        data = json.loads(body)
        if (
            not isinstance(data, dict)
            or type(data.get("version")) is not int
            or data["version"] != 1
        ):
            raise ValueError("unsupported job version")
        if data.get("job_type") != "composite":
            raise ValueError("unsupported job type")
        job = cls(
            canonical_uuid(data.get("batch_id")),
            canonical_uuid(data.get("image_id")),
            data.get("source_key"),
            data.get("watermark_key"),
        )
        if source_owner(job.source_key) != source_owner(job.watermark_key):
            raise ValueError("source and watermark must belong to the same owner")
        if UUID(job.batch_id).int == 0 or UUID(job.image_id).int == 0:
            raise ValueError("job IDs must not be nil")
        return job

    @property
    def output_key(self) -> str:
        return f"processed/{self.batch_id}/{self.image_id}.png"

    def result(self, error: str | None = None) -> dict:
        result = {
            "version": 1,
            "job_type": "composite",
            "batch_id": self.batch_id,
            "image_id": self.image_id,
            "status": "failed" if error else "done",
        }
        if error:
            result["error"] = error
        else:
            result["output_key"] = self.output_key
        return result
