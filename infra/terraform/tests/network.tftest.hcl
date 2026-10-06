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
  bucket_name    = "watermarker-network-test"
  app_origins    = ["https://watermarker.example.com"]
}

run "network_boundaries" {
  command = apply

  assert {
    condition = (aws_vpc.app.enable_dns_support && aws_vpc.app.enable_dns_hostnames &&
      toset([for subnet in aws_subnet.public : subnet.cidr_block]) == toset(["10.20.0.0/24", "10.20.1.0/24"]) &&
      toset([for subnet in aws_subnet.private : subnet.cidr_block]) == toset(["10.20.10.0/24", "10.20.11.0/24"]) &&
      length(toset([for subnet in aws_subnet.public : subnet.availability_zone])) == 2 &&
    alltrue([for key, subnet in aws_subnet.private : !subnet.map_public_ip_on_launch && subnet.availability_zone == aws_subnet.public[key].availability_zone]))
    error_message = "Subnets must be non-overlapping, span two zones, and keep private addresses private."
  }

  assert {
    condition = (aws_route.internet.destination_cidr_block == "0.0.0.0/0" &&
      aws_route.internet.gateway_id == aws_internet_gateway.app.id &&
      aws_route.internet.route_table_id == aws_route_table.public.id &&
      length(aws_route_table.private.route) == 0 &&
      alltrue([for key, association in aws_route_table_association.public : association.subnet_id == aws_subnet.public[key].id && association.route_table_id == aws_route_table.public.id]) &&
    alltrue([for key, association in aws_route_table_association.private : association.subnet_id == aws_subnet.private[key].id && association.route_table_id == aws_route_table.private.id]))
    error_message = "Only public subnets may use the internet route; private subnets must remain isolated."
  }

  assert {
    condition = (aws_vpc_security_group_ingress_rule.https.cidr_ipv4 == "0.0.0.0/0" &&
      aws_vpc_security_group_ingress_rule.https.from_port == 443 && aws_vpc_security_group_ingress_rule.https.to_port == 443 &&
      aws_vpc_security_group_ingress_rule.api.security_group_id == aws_security_group.service["api"].id &&
      aws_vpc_security_group_ingress_rule.api.referenced_security_group_id == aws_security_group.service["load_balancer"].id &&
      aws_vpc_security_group_ingress_rule.api.from_port == 8080 && aws_vpc_security_group_ingress_rule.api.to_port == 8080 &&
      aws_vpc_security_group_egress_rule.to_api.referenced_security_group_id == aws_security_group.service["api"].id &&
      length(aws_security_group.service["worker"].ingress) == 0 &&
    alltrue([for rule in aws_vpc_security_group_egress_rule.https : rule.from_port == 443 && rule.to_port == 443]))
    error_message = "Only HTTPS may enter the load balancer, only the load balancer may enter the API, and workers need no inbound access."
  }
}
