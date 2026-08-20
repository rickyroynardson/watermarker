#!/bin/bash
# Runs inside the LocalStack container once the gateway accepts traffic.
# Creates the bucket and queues the pipeline expects. Not re-runnable after an
# attribute change (SQS rejects CreateQueue with different attributes) — retuning
# means `make nuke && make up`, which is the workflow anyway at PERSISTENCE=0.
set -euo pipefail

BUCKET=watermarker
JOBS=watermarker-jobs
RESULTS=watermarker-results
JOBS_DLQ=watermarker-jobs-dlq

# Must exceed worst-case single-image processing, with margin (see PLAN.md).
VISIBILITY_TIMEOUT=120
MAX_RECEIVES=3

awslocal s3api create-bucket --bucket "$BUCKET" >/dev/null

# DLQ first: the jobs queue's redrive policy needs its ARN.
awslocal sqs create-queue --queue-name "$JOBS_DLQ" >/dev/null
dlq_arn=$(awslocal sqs get-queue-attributes \
  --queue-url "$(awslocal sqs get-queue-url --queue-name "$JOBS_DLQ" --output text)" \
  --attribute-names QueueArn --query 'Attributes.QueueArn' --output text)

# A poison image is retried MAX_RECEIVES times, then parked in the DLQ
# instead of blocking the queue forever.
awslocal sqs create-queue --queue-name "$JOBS" --attributes "$(cat <<JSON
{
  "VisibilityTimeout": "$VISIBILITY_TIMEOUT",
  "RedrivePolicy": "{\"deadLetterTargetArn\":\"$dlq_arn\",\"maxReceiveCount\":\"$MAX_RECEIVES\"}"
}
JSON
)" >/dev/null

awslocal sqs create-queue --queue-name "$RESULTS" \
  --attributes "VisibilityTimeout=$VISIBILITY_TIMEOUT" >/dev/null

echo "localstack init: bucket=$BUCKET queues=$JOBS,$RESULTS,$JOBS_DLQ"
