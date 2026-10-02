variable "region" {
  type    = string
  default = "ap-south-1"
}
variable "environment" {
  type    = string
  default = "prod"
}
variable "vpc_cidr" {
  type    = string
  default = "10.40.0.0/16"
}
variable "kubernetes_version" {
  type    = string
  default = "1.31"
}
variable "public_api_endpoint" {
  type    = bool
  default = false
}
variable "node_instance_types" {
  type    = list(string)
  default = ["m6i.2xlarge"]
}
variable "node_count" {
  type    = number
  default = 6
}
variable "node_max" {
  type    = number
  default = 30
}
variable "db_instance_class" {
  type    = string
  default = "db.r6g.xlarge"
}
variable "redis_node_type" {
  type    = string
  default = "cache.r6g.large"
}
variable "kafka_instance_type" {
  type    = string
  default = "kafka.m5.2xlarge"
}
variable "kafka_volume_gb" {
  type    = number
  default = 2000
}
