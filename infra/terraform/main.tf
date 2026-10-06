resource "aws_s3_bucket" "images" {
  bucket        = var.bucket_name
  force_destroy = false
  # Never enable versioning: application cleanup requires actual deletion, not delete markers.
}

resource "aws_s3_bucket_public_access_block" "images" {
  bucket                  = aws_s3_bucket.images.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_ownership_controls" "images" {
  bucket = aws_s3_bucket.images.id
  rule { object_ownership = "BucketOwnerEnforced" }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "images" {
  bucket = aws_s3_bucket.images.id
  rule {
    apply_server_side_encryption_by_default { sse_algorithm = "AES256" }
  }
}

resource "aws_s3_bucket_policy" "https" {
  bucket = aws_s3_bucket.images.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Deny", Principal = "*", Action = "s3:*"
      Resource  = [aws_s3_bucket.images.arn, "${aws_s3_bucket.images.arn}/*"]
      Condition = { Bool = { "aws:SecureTransport" = "false" } }
    }]
  })
}

resource "aws_s3_bucket_cors_configuration" "images" {
  bucket = aws_s3_bucket.images.id
  cors_rule {
    allowed_origins = sort(tolist(var.app_origins))
    allowed_methods = ["POST", "GET", "HEAD"]
    allowed_headers = ["*"]
    expose_headers  = ["ETag", "x-amz-request-id"]
    max_age_seconds = 3600
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "staging" {
  bucket = aws_s3_bucket.images.id
  rule {
    id     = "expire-staging-uploads"
    status = "Enabled"
    filter { prefix = "uploads/" }
    expiration { days = 7 }
    abort_incomplete_multipart_upload { days_after_initiation = 1 }
  }
}

resource "aws_sqs_queue" "dlq" {
  for_each                  = toset(["jobs", "results"])
  name                      = "${var.name_prefix}-${each.key}-dlq"
  message_retention_seconds = 1209600
  sqs_managed_sse_enabled   = true
}

resource "aws_sqs_queue" "pipeline" {
  for_each                   = toset(["jobs", "results"])
  name                       = "${var.name_prefix}-${each.key}"
  visibility_timeout_seconds = 120
  receive_wait_time_seconds  = 20
  message_retention_seconds  = 345600
  sqs_managed_sse_enabled    = true
  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.dlq[each.key].arn
    maxReceiveCount     = 3
  })
}

resource "aws_sqs_queue_redrive_allow_policy" "dlq" {
  for_each  = toset(["jobs", "results"])
  queue_url = aws_sqs_queue.dlq[each.key].url
  redrive_allow_policy = jsonencode({
    redrivePermission = "byQueue"
    sourceQueueArns   = [aws_sqs_queue.pipeline[each.key].arn]
  })
}
