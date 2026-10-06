locals {
  bucket_arn = aws_s3_bucket.images.arn
  # containers.tf attaches these policies to matching ECS task roles.
  # ListBucket is bucket-scoped: prefix conditions would hide missing-object HEAD responses as 403s.
  service_permissions = {
    api = [
      { Effect = "Allow", Action = ["s3:ListBucket"], Resource = [local.bucket_arn] },
      { Effect = "Allow", Action = ["s3:GetObject"], Resource = ["${local.bucket_arn}/uploads/*", "${local.bucket_arn}/sources/*", "${local.bucket_arn}/processed/*"] },
      { Effect = "Allow", Action = ["s3:PutObject"], Resource = ["${local.bucket_arn}/uploads/*", "${local.bucket_arn}/sources/*"] },
      { Effect = "Allow", Action = ["s3:DeleteObject"], Resource = ["${local.bucket_arn}/uploads/*"] }
    ]
    worker = [
      { Effect = "Allow", Action = ["s3:ListBucket"], Resource = [local.bucket_arn] },
      { Effect = "Allow", Action = ["s3:GetObject"], Resource = ["${local.bucket_arn}/sources/*", "${local.bucket_arn}/processed/*"] },
      { Effect = "Allow", Action = ["s3:PutObject"], Resource = ["${local.bucket_arn}/processed/*"] },
      { Effect = "Allow", Action = ["sqs:ReceiveMessage", "sqs:DeleteMessage", "sqs:ChangeMessageVisibility"], Resource = [aws_sqs_queue.pipeline["jobs"].arn] },
      { Effect = "Allow", Action = ["sqs:SendMessage"], Resource = [aws_sqs_queue.pipeline["results"].arn] }
    ]
    consumer = [
      { Effect = "Allow", Action = ["sqs:SendMessage"], Resource = [aws_sqs_queue.pipeline["jobs"].arn] },
      { Effect = "Allow", Action = ["sqs:ReceiveMessage", "sqs:DeleteMessage"], Resource = [aws_sqs_queue.pipeline["results"].arn, aws_sqs_queue.dlq["jobs"].arn] }
    ]
    monitor = [
      { Effect = "Allow", Action = ["sqs:GetQueueAttributes"], Resource = concat([for queue in aws_sqs_queue.pipeline : queue.arn], [for queue in aws_sqs_queue.dlq : queue.arn]) }
    ]
    cleanup = [
      { Effect = "Allow", Action = ["s3:GetBucketVersioning"], Resource = [local.bucket_arn] },
      { Effect = "Allow", Action = ["s3:DeleteObject"], Resource = ["${local.bucket_arn}/uploads/*", "${local.bucket_arn}/sources/*", "${local.bucket_arn}/processed/*"] }
    ]
  }
}

resource "aws_iam_policy" "service" {
  for_each = local.service_permissions
  name     = "${var.name_prefix}-${each.key}"
  policy   = jsonencode({ Version = "2012-10-17", Statement = each.value })
}
