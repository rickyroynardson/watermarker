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
  bucket_name    = "watermarker-github-test"
  app_origins    = ["https://watermarker.example.com"]
}

run "disabled_by_default" {
  command = apply
  assert {
    condition = (length(aws_iam_openid_connect_provider.github) == 0 &&
      length(aws_iam_role.github_plan) == 0 &&
    length(aws_iam_role_policy.github_plan) == 0 && output.github_plan_role_arn == null)
    error_message = "GitHub AWS integration must remain opt-in."
  }
}

run "restricted_plan_role" {
  command = apply
  variables {
    github_plan = {
      subject      = "repo:rickyroynardson/watermarker:ref:refs/heads/main"
      state_bucket = "watermarker-state-test"
      state_key    = "environments/dev/terraform.tfstate"
    }
  }
  assert {
    condition = (aws_iam_openid_connect_provider.github[0].url == "https://token.actions.githubusercontent.com" &&
      toset(aws_iam_openid_connect_provider.github[0].client_id_list) == toset(["sts.amazonaws.com"]) &&
      jsondecode(aws_iam_role.github_plan[0].assume_role_policy).Statement == [{
        Effect    = "Allow"
        Action    = "sts:AssumeRoleWithWebIdentity"
        Principal = { Federated = aws_iam_openid_connect_provider.github[0].arn }
        Condition = { StringEquals = {
          "token.actions.githubusercontent.com:aud" = "sts.amazonaws.com"
          "token.actions.githubusercontent.com:sub" = "repo:rickyroynardson/watermarker:ref:refs/heads/main"
        } }
    }])
    error_message = "Only the exact main-branch GitHub subject and STS audience may assume the role."
  }
  assert {
    condition = (length(jsondecode(aws_iam_role_policy.github_plan[0].policy).Statement) == 5 &&
      alltrue([for s in jsondecode(aws_iam_role_policy.github_plan[0].policy).Statement :
        s.Effect == "Allow" && alltrue([for a in s.Action :
          startswith(split(":", a)[1], "Get") || startswith(split(":", a)[1], "List") || startswith(split(":", a)[1], "Describe") || s.Sid == "StateLock"
      ])]) &&
      !contains(jsondecode(aws_iam_role_policy.github_plan[0].policy).Statement[0].Action, "secretsmanager:GetSecretValue") &&
      jsondecode(aws_iam_role_policy.github_plan[0].policy).Statement[3].Action == ["s3:GetObject"] &&
      jsondecode(aws_iam_role_policy.github_plan[0].policy).Statement[3].Resource == ["arn:aws:s3:::watermarker-state-test/environments/dev/terraform.tfstate"] &&
      jsondecode(aws_iam_role_policy.github_plan[0].policy).Statement[4].Action == ["s3:GetObject", "s3:PutObject", "s3:DeleteObject"] &&
    jsondecode(aws_iam_role_policy.github_plan[0].policy).Statement[4].Resource == ["arn:aws:s3:::watermarker-state-test/environments/dev/terraform.tfstate.tflock"])
    error_message = "Plan may only read metadata/state and write the exact lock object; no state writes or deployment permissions."
  }
}

run "reject_wildcard_subject" {
  command = plan
  variables {
    github_plan = {
      subject      = "repo:rickyroynardson/*:ref:refs/heads/main"
      state_bucket = "watermarker-state-test"
      state_key    = "environments/dev/terraform.tfstate"
    }
  }
  expect_failures = [var.github_plan]
}

run "reject_other_branch" {
  command = plan
  variables {
    github_plan = {
      subject      = "repo:rickyroynardson/watermarker:ref:refs/heads/feature"
      state_bucket = "watermarker-state-test"
      state_key    = "environments/dev/terraform.tfstate"
    }
  }
  expect_failures = [var.github_plan]
}
