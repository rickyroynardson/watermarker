data "aws_availability_zones" "available" {
  state = "available"
  filter {
    name   = "zone-type"
    values = ["availability-zone"]
  }
  lifecycle {
    postcondition {
      condition     = length(self.names) >= 2
      error_message = "Networking requires at least two available availability zones."
    }
  }
}

locals {
  subnet_zones = { a = 0, b = 1 }
}

resource "aws_vpc" "app" {
  cidr_block           = "10.20.0.0/16"
  enable_dns_support   = true
  enable_dns_hostnames = true
  tags                 = { Name = "${var.name_prefix}-vpc" }
}

resource "aws_subnet" "public" {
  for_each                = local.subnet_zones
  vpc_id                  = aws_vpc.app.id
  availability_zone       = sort(data.aws_availability_zones.available.names)[each.value]
  cidr_block              = cidrsubnet(aws_vpc.app.cidr_block, 8, each.value)
  map_public_ip_on_launch = false
  tags                    = { Name = "${var.name_prefix}-public-${each.key}" }
}

resource "aws_subnet" "private" {
  for_each                = local.subnet_zones
  vpc_id                  = aws_vpc.app.id
  availability_zone       = aws_subnet.public[each.key].availability_zone
  cidr_block              = cidrsubnet(aws_vpc.app.cidr_block, 8, each.value + 10)
  map_public_ip_on_launch = false
  tags                    = { Name = "${var.name_prefix}-private-${each.key}" }
}

resource "aws_internet_gateway" "app" {
  vpc_id = aws_vpc.app.id
  tags   = { Name = "${var.name_prefix}-igw" }
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.app.id
  tags   = { Name = "${var.name_prefix}-public" }
}

resource "aws_route" "internet" {
  route_table_id         = aws_route_table.public.id
  destination_cidr_block = "0.0.0.0/0"
  gateway_id             = aws_internet_gateway.app.id
}

resource "aws_route_table_association" "public" {
  for_each       = aws_subnet.public
  subnet_id      = each.value.id
  route_table_id = aws_route_table.public.id
}

# No NAT or endpoints yet: these subnets are isolated until the compute step.
resource "aws_route_table" "private" {
  vpc_id = aws_vpc.app.id
  tags   = { Name = "${var.name_prefix}-private" }
}

resource "aws_route_table_association" "private" {
  for_each       = aws_subnet.private
  subnet_id      = each.value.id
  route_table_id = aws_route_table.private.id
}

resource "aws_security_group" "service" {
  for_each    = toset(["load_balancer", "api", "worker"])
  name        = "${var.name_prefix}-${replace(each.key, "_", "-")}"
  description = "Network boundary for ${each.key}"
  vpc_id      = aws_vpc.app.id
  tags        = { Name = "${var.name_prefix}-${each.key}" }
}

resource "aws_vpc_security_group_ingress_rule" "https" {
  security_group_id = aws_security_group.service["load_balancer"].id
  description       = "Public HTTPS to the future load balancer"
  ip_protocol       = "tcp"
  from_port         = 443
  to_port           = 443
  cidr_ipv4         = "0.0.0.0/0"
}

resource "aws_vpc_security_group_egress_rule" "to_api" {
  security_group_id            = aws_security_group.service["load_balancer"].id
  description                  = "Forward requests and health checks to API tasks"
  ip_protocol                  = "tcp"
  from_port                    = 8080
  to_port                      = 8080
  referenced_security_group_id = aws_security_group.service["api"].id
}

resource "aws_vpc_security_group_ingress_rule" "api" {
  security_group_id            = aws_security_group.service["api"].id
  description                  = "API accepts traffic only from the load balancer"
  ip_protocol                  = "tcp"
  from_port                    = 8080
  to_port                      = 8080
  referenced_security_group_id = aws_security_group.service["load_balancer"].id
}

resource "aws_vpc_security_group_egress_rule" "https" {
  for_each          = toset(["api", "worker"])
  security_group_id = aws_security_group.service[each.key].id
  description       = "HTTPS to AWS services and external providers; routing is required separately"
  ip_protocol       = "tcp"
  from_port         = 443
  to_port           = 443
  cidr_ipv4         = "0.0.0.0/0"
}
