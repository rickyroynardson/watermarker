mock_provider "aws" {
  mock_resource "aws_iam_policy" {
    defaults = { arn = "arn:aws:iam::123456789012:policy/test-policy" }
  }
  mock_data "aws_availability_zones" {
    defaults = { names = ["us-east-1a", "us-east-1b"] }
  }
}

variables {
  aws_account_id = "123456789012"
  bucket_name    = "watermarker-test-images"
  app_origins    = ["https://watermarker.example.com"]
}

override_resource {
  target = aws_s3_bucket.images
  values = { arn = "arn:aws:s3:::watermarker-test-images", id = "watermarker-test-images" }
}

override_resource {
  target = aws_sqs_queue.pipeline["jobs"]
  values = { arn = "arn:aws:sqs:us-east-1:123456789012:watermarker-jobs", url = "https://sqs.us-east-1.amazonaws.com/123456789012/watermarker-jobs" }
}

override_resource {
  target = aws_sqs_queue.pipeline["results"]
  values = { arn = "arn:aws:sqs:us-east-1:123456789012:watermarker-results", url = "https://sqs.us-east-1.amazonaws.com/123456789012/watermarker-results" }
}

override_resource {
  target = aws_sqs_queue.dlq["jobs"]
  values = { arn = "arn:aws:sqs:us-east-1:123456789012:watermarker-jobs-dlq", url = "https://sqs.us-east-1.amazonaws.com/123456789012/watermarker-jobs-dlq" }
}

override_resource {
  target = aws_sqs_queue.dlq["results"]
  values = { arn = "arn:aws:sqs:us-east-1:123456789012:watermarker-results-dlq", url = "https://sqs.us-east-1.amazonaws.com/123456789012/watermarker-results-dlq" }
}

run "pipeline_contract" {
  command = apply

  assert {
    condition = (alltrue([for name, queue in aws_sqs_queue.pipeline :
      queue.visibility_timeout_seconds == 120 && queue.receive_wait_time_seconds == 20 &&
      queue.sqs_managed_sse_enabled &&
      jsondecode(queue.redrive_policy).maxReceiveCount == 3 &&
      jsondecode(queue.redrive_policy).deadLetterTargetArn == aws_sqs_queue.dlq[name].arn &&
      aws_sqs_queue.dlq[name].message_retention_seconds > queue.message_retention_seconds &&
      jsondecode(aws_sqs_queue_redrive_allow_policy.dlq[name].redrive_allow_policy).sourceQueueArns == [queue.arn]
    ]))
    error_message = "Queue retry, retention, encryption, or DLQ routing differs from the application contract."
  }

  assert {
    condition = (aws_s3_bucket_public_access_block.images.block_public_acls &&
      aws_s3_bucket_public_access_block.images.block_public_policy &&
      aws_s3_bucket_public_access_block.images.ignore_public_acls &&
      aws_s3_bucket_public_access_block.images.restrict_public_buckets &&
      aws_s3_bucket_ownership_controls.images.rule[0].object_ownership == "BucketOwnerEnforced" &&
      !aws_s3_bucket.images.force_destroy &&
    jsondecode(aws_s3_bucket_policy.https.policy).Statement[0].Condition.Bool["aws:SecureTransport"] == "false")
    error_message = "The image bucket must remain private, require TLS, disable ACLs and preserve stored objects on destroy."
  }

  assert {
    condition = (one(aws_s3_bucket_lifecycle_configuration.staging.rule).filter[0].prefix == "uploads/" &&
      one(aws_s3_bucket_lifecycle_configuration.staging.rule).expiration[0].days == 7 &&
    one(aws_s3_bucket_cors_configuration.images.cors_rule).allowed_origins == toset(["https://watermarker.example.com"]))
    error_message = "Only staging uploads should expire; browser access must use explicit origins."
  }

  assert {
    condition = (jsondecode(aws_iam_policy.service["consumer"].policy).Statement[0].Resource == [aws_sqs_queue.pipeline["jobs"].arn] &&
      jsondecode(aws_iam_policy.service["consumer"].policy).Statement[1].Resource == [aws_sqs_queue.pipeline["results"].arn, aws_sqs_queue.dlq["jobs"].arn] &&
      jsondecode(aws_iam_policy.service["api"].policy).Statement[3].Resource == ["arn:aws:s3:::watermarker-test-images/uploads/*"] &&
      jsondecode(aws_iam_policy.service["worker"].policy).Statement[2].Resource == ["arn:aws:s3:::watermarker-test-images/processed/*"] &&
      jsondecode(aws_iam_policy.service["worker"].policy).Statement[3].Resource == [aws_sqs_queue.pipeline["jobs"].arn] &&
      jsondecode(aws_iam_policy.service["worker"].policy).Statement[4].Resource == [aws_sqs_queue.pipeline["results"].arn] &&
      jsondecode(aws_iam_policy.service["monitor"].policy).Statement[0].Action == ["sqs:GetQueueAttributes"] &&
    alltrue([for policy in aws_iam_policy.service : alltrue([for statement in jsondecode(policy.policy).Statement : !contains(statement.Resource, "*") && !contains(statement.Action, "*")])]))
    error_message = "Service permissions must be scoped to the correct bucket prefixes and queues."
  }

  assert {
    condition = (output.application_env.S3_BUCKET == "watermarker-test-images" &&
      output.application_env.SQS_JOBS_QUEUE_URL == aws_sqs_queue.pipeline["jobs"].url &&
    output.application_env.SQS_RESULTS_DLQ_QUEUE_URL == aws_sqs_queue.dlq["results"].url)
    error_message = "Application outputs must reference the provisioned resources."
  }
}

run "reject_unsafe_inputs" {
  command = plan
  variables {
    aws_account_id = "invalid"
    bucket_name    = "INVALID-BUCKET"
    app_origins    = ["*"]
  }
  expect_failures = [var.aws_account_id, var.bucket_name, var.app_origins]
}
