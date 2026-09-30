locals {
  db_address = var.use_aurora_serverless ? one(aws_rds_cluster.this[*].endpoint) : one(aws_db_instance.this[*].address)
  db_instance_identifiers = (
    var.use_aurora_serverless ? [for i in range(var.multi_az ? 2 : 1) : "${var.name}-${i + 1}"] : [var.name]
  )
  final_snapshot_identifier = var.skip_final_snapshot ? null : "${var.name}-final"
}

resource "aws_db_subnet_group" "this" {
  name        = var.name
  description = "Stackorder database subnets"
  subnet_ids  = local.private_subnet_ids

  tags = var.tags
}

resource "aws_db_parameter_group" "this" {
  count = var.use_aurora_serverless ? 0 : 1

  name_prefix = "${var.name}-"
  description = "Stackorder PostgreSQL ${local.db_major}: TLS required"
  family      = "postgres${local.db_major}"

  parameter {
    name         = "rds.force_ssl"
    value        = "1"
    apply_method = "pending-reboot"
  }

  tags = var.tags

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_db_instance" "this" {
  count = var.use_aurora_serverless ? 0 : 1

  identifier     = local.db_instance_identifiers[0]
  engine         = "postgres"
  engine_version = var.engine_version
  instance_class = var.instance_class

  allow_major_version_upgrade = var.allow_major_version_upgrade
  apply_immediately           = var.apply_immediately

  db_name             = local.db_name
  username            = local.db_username
  password_wo         = ephemeral.random_password.db.result
  password_wo_version = local.db_password_version
  port                = local.db_port

  allocated_storage     = var.allocated_storage
  max_allocated_storage = var.max_allocated_storage
  storage_type          = "gp3"
  storage_encrypted     = true
  kms_key_id            = var.kms_key_arn

  db_subnet_group_name   = aws_db_subnet_group.this.name
  vpc_security_group_ids = [aws_security_group.db.id]
  parameter_group_name   = aws_db_parameter_group.this[0].name
  publicly_accessible    = false
  multi_az               = var.multi_az

  backup_retention_period    = var.backup_retention_days
  copy_tags_to_snapshot      = true
  deletion_protection        = var.deletion_protection
  skip_final_snapshot        = var.skip_final_snapshot
  final_snapshot_identifier  = local.final_snapshot_identifier
  auto_minor_version_upgrade = true

  performance_insights_enabled          = var.performance_insights
  performance_insights_retention_period = var.performance_insights ? 7 : null
  performance_insights_kms_key_id       = var.performance_insights ? var.kms_key_arn : null

  tags = var.tags
}

data "aws_rds_engine_version" "aurora" {
  count = var.use_aurora_serverless && !strcontains(var.engine_version, ".") ? 1 : 0

  engine       = "aurora-postgresql"
  version      = var.engine_version
  default_only = true
}

resource "aws_rds_cluster_parameter_group" "this" {
  count = var.use_aurora_serverless ? 1 : 0

  name_prefix = "${var.name}-"
  description = "Stackorder Aurora PostgreSQL ${local.db_major}: TLS required"
  family      = "aurora-postgresql${local.db_major}"

  parameter {
    name         = "rds.force_ssl"
    value        = "1"
    apply_method = "pending-reboot"
  }

  tags = var.tags

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_rds_cluster" "this" {
  count = var.use_aurora_serverless ? 1 : 0

  cluster_identifier = var.name
  engine             = "aurora-postgresql"
  engine_mode        = "provisioned"
  engine_version     = strcontains(var.engine_version, ".") ? var.engine_version : one(data.aws_rds_engine_version.aurora[*].version_actual)

  allow_major_version_upgrade = var.allow_major_version_upgrade
  apply_immediately           = var.apply_immediately

  database_name              = local.db_name
  master_username            = local.db_username
  master_password_wo         = ephemeral.random_password.db.result
  master_password_wo_version = local.db_password_version
  port                       = local.db_port

  storage_encrypted = true
  kms_key_id        = var.kms_key_arn

  db_subnet_group_name            = aws_db_subnet_group.this.name
  vpc_security_group_ids          = [aws_security_group.db.id]
  db_cluster_parameter_group_name = aws_rds_cluster_parameter_group.this[0].name

  backup_retention_period   = var.backup_retention_days
  copy_tags_to_snapshot     = true
  deletion_protection       = var.deletion_protection
  skip_final_snapshot       = var.skip_final_snapshot
  final_snapshot_identifier = local.final_snapshot_identifier

  serverlessv2_scaling_configuration {
    min_capacity = var.aurora_min_acu
    max_capacity = var.aurora_max_acu
  }

  tags = var.tags
}

resource "aws_rds_cluster_instance" "this" {
  count = var.use_aurora_serverless ? length(local.db_instance_identifiers) : 0

  identifier           = local.db_instance_identifiers[count.index]
  cluster_identifier   = aws_rds_cluster.this[0].id
  engine               = aws_rds_cluster.this[0].engine
  instance_class       = "db.serverless"
  db_subnet_group_name = aws_db_subnet_group.this.name
  publicly_accessible  = false
  promotion_tier       = count.index
  apply_immediately    = var.apply_immediately

  auto_minor_version_upgrade            = true
  performance_insights_enabled          = var.performance_insights
  performance_insights_retention_period = var.performance_insights ? 7 : null
  performance_insights_kms_key_id       = var.performance_insights ? var.kms_key_arn : null

  tags = var.tags
}
