#!/bin/bash
# Runs inside the LocalStack container once the gateway accepts traffic.
# Creates the bucket and queues the pipeline expects. Not re-runnable after an
# attribute change (SQS rejects CreateQueue with different attributes) — retuning
# means `make nuke && make up`, which is the workflow anyway at PERSISTENCE=0.
set -euo pipefail

BUCKET=watermarker
JOBS=watermarker-jobs
RESULTS=watermarker-results

# Must exceed worst-case single-image processing, with margin (see PLAN.md).
VISIBILITY_TIMEOUT=120
MAX_RECEIVES=3

awslocal s3api create-bucket --bucket "$BUCKET" >/dev/null

# Both job and result failures need somewhere to land after repeated retries.
for queue in "$JOBS" "$RESULTS"; do
  awslocal sqs create-queue --queue-name "$queue-dlq" >/dev/null
  dlq_arn=$(awslocal sqs get-queue-attributes \
    --queue-url "$(awslocal sqs get-queue-url --queue-name "$queue-dlq" --output text)" \
    --attribute-names QueueArn --query 'Attributes.QueueArn' --output text)

  awslocal sqs create-queue --queue-name "$queue" --attributes "$(cat <<JSON
{
  "VisibilityTimeout": "$VISIBILITY_TIMEOUT",
  "RedrivePolicy": "{\"deadLetterTargetArn\":\"$dlq_arn\",\"maxReceiveCount\":\"$MAX_RECEIVES\"}"
}
JSON
)" >/dev/null
done

echo "localstack init: bucket=$BUCKET queues=$JOBS,$RESULTS,$JOBS-dlq,$RESULTS-dlq"
