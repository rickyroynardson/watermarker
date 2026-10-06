variable "aws_region" {
  description = "AWS region for networking, the bucket, and queues."
  type        = string
  default     = "us-east-1"
}

variable "aws_account_id" {
  description = "Expected AWS account; the provider rejects credentials for another account."
  type        = string
  validation {
    condition     = can(regex("^[0-9]{12}$", var.aws_account_id))
    error_message = "Use a 12-digit AWS account ID."
  }
}

variable "name_prefix" {
  description = "Resource name prefix; use a different prefix for each environment."
  type        = string
  default     = "watermarker"
  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,39}$", var.name_prefix))
    error_message = "Use 2-40 lowercase letters, digits, or hyphens, starting with a letter."
  }
}

variable "bucket_name" {
  description = "Globally unique S3 bucket name for this environment."
  type        = string
  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$", var.bucket_name)) && !startswith(var.bucket_name, "xn--") && !endswith(var.bucket_name, "-s3alias") && !endswith(var.bucket_name, "--ol-s3") && !endswith(var.bucket_name, "--x-s3") && !endswith(var.bucket_name, "--table-s3") && !startswith(var.bucket_name, "sthree-") && !startswith(var.bucket_name, "amzn-s3-demo-")
    error_message = "Use a valid globally unique S3 bucket name (3-63 lowercase letters, digits, hyphens)."
  }
}

variable "app_origins" {
  description = "Exact frontend origins allowed to upload and fetch images in the browser."
  type        = set(string)
  validation {
    condition     = length(var.app_origins) > 0 && alltrue([for origin in var.app_origins : can(regex("^https://[a-zA-Z0-9.-]+(:[0-9]+)?$|^http://(localhost|127\\.0\\.0\\.1)(:[0-9]+)?$", origin))])
    error_message = "Provide exact HTTPS origins without paths or trailing slashes; HTTP is allowed only for localhost."
  }
}

variable "github_plan" {
  description = "Optional GitHub OIDC plan access. Null leaves it disabled. Subject must match the repository's exact main-branch OIDC claim."
  type = object({
    subject      = string
    state_bucket = string
    state_key    = string
  })
  default = null
  validation {
    condition = var.github_plan == null ? true : (
      can(regex("^repo:[A-Za-z0-9_.-]+(@[0-9]+)?/[A-Za-z0-9_.-]+(@[0-9]+)?:ref:refs/heads/main$", var.github_plan.subject)) &&
      can(regex("^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$", var.github_plan.state_bucket)) &&
      can(regex("^[A-Za-z0-9_-]+(/[A-Za-z0-9_.-]+)*\\.tfstate$", var.github_plan.state_key))
    )
    error_message = "Use an exact repository main-branch subject, a bucket name, and a relative .tfstate key; wildcards are forbidden."
  }
}
