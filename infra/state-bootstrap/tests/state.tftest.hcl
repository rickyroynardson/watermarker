mock_provider "aws" {}
variables {
  aws_account_id = "123456789012"
  bucket_name    = "watermarker-state-test"
}
override_resource {
  target = aws_s3_bucket.state
  values = { id = "watermarker-state-test", arn = "arn:aws:s3:::watermarker-state-test" }
}
run "state_security" {
  command = apply
  assert {
    condition = (aws_s3_bucket_versioning.state.versioning_configuration[0].status == "Enabled" &&
      aws_s3_bucket_public_access_block.state.block_public_acls &&
      aws_s3_bucket_public_access_block.state.block_public_policy &&
      aws_s3_bucket_public_access_block.state.ignore_public_acls &&
      aws_s3_bucket_public_access_block.state.restrict_public_buckets &&
      aws_s3_bucket_ownership_controls.state.rule[0].object_ownership == "BucketOwnerEnforced" &&
      one(one(aws_s3_bucket_server_side_encryption_configuration.state.rule).apply_server_side_encryption_by_default).sse_algorithm == "AES256" &&
      !aws_s3_bucket.state.force_destroy &&
    jsondecode(aws_s3_bucket_policy.https.policy).Statement[0].Condition.Bool["aws:SecureTransport"] == "false")
    error_message = "State storage must be private, encrypted, versioned, preserve objects, and require TLS."
  }
}
