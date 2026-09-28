resource "aws_cloudwatch_log_group" "this" {
  name              = "/ecs/${var.name}"
  retention_in_days = var.log_retention_days

  tags = var.tags
}

locals {
  alarm_actions = var.alarms_enabled ? [var.alarm_sns_topic_arn] : []
}

resource "aws_cloudwatch_metric_alarm" "target_5xx" {
  count = var.alarms_enabled ? 1 : 0

  alarm_name          = "${var.name}-target-5xx"
  alarm_description   = "Stackorder returned more than ${var.alarm_thresholds.target_5xx_count} 5xx responses in 5 minutes"
  namespace           = "AWS/ApplicationELB"
  metric_name         = "HTTPCode_Target_5XX_Count"
  statistic           = "Sum"
  period              = 300
  evaluation_periods  = 1
  comparison_operator = "GreaterThanThreshold"
  threshold           = var.alarm_thresholds.target_5xx_count
  treat_missing_data  = "notBreaching"
  alarm_actions       = local.alarm_actions
  ok_actions          = local.alarm_actions

  dimensions = {
    LoadBalancer = aws_lb.this.arn_suffix
  }

  tags = var.tags
}

resource "aws_cloudwatch_metric_alarm" "unhealthy_hosts" {
  count = var.alarms_enabled ? 1 : 0

  alarm_name          = "${var.name}-unhealthy-hosts"
  alarm_description   = "A Stackorder task is failing the /readyz health check"
  namespace           = "AWS/ApplicationELB"
  metric_name         = "UnHealthyHostCount"
  statistic           = "Maximum"
  period              = 60
  evaluation_periods  = 3
  comparison_operator = "GreaterThanOrEqualToThreshold"
  threshold           = 1
  treat_missing_data  = "notBreaching"
  alarm_actions       = local.alarm_actions
  ok_actions          = local.alarm_actions

  dimensions = {
    LoadBalancer = aws_lb.this.arn_suffix
    TargetGroup  = aws_lb_target_group.this.arn_suffix
  }

  tags = var.tags
}

resource "aws_cloudwatch_metric_alarm" "cpu" {
  count = var.alarms_enabled ? 1 : 0

  alarm_name          = "${var.name}-cpu"
  alarm_description   = "Stackorder service CPU above ${var.alarm_thresholds.cpu_percent}% for 15 minutes"
  namespace           = "AWS/ECS"
  metric_name         = "CPUUtilization"
  statistic           = "Average"
  period              = 300
  evaluation_periods  = 3
  comparison_operator = "GreaterThanThreshold"
  threshold           = var.alarm_thresholds.cpu_percent
  treat_missing_data  = "missing"
  alarm_actions       = local.alarm_actions
  ok_actions          = local.alarm_actions

  dimensions = {
    ClusterName = aws_ecs_cluster.this.name
    ServiceName = aws_ecs_service.this.name
  }

  tags = var.tags
}

resource "aws_cloudwatch_metric_alarm" "db_free_storage" {
  count = var.alarms_enabled && !var.use_aurora_serverless ? 1 : 0

  alarm_name          = "${var.name}-db-free-storage"
  alarm_description   = "Stackorder database has less than ${var.alarm_thresholds.db_free_storage_bytes} bytes of free storage"
  namespace           = "AWS/RDS"
  metric_name         = "FreeStorageSpace"
  statistic           = "Minimum"
  period              = 300
  evaluation_periods  = 1
  comparison_operator = "LessThanThreshold"
  threshold           = var.alarm_thresholds.db_free_storage_bytes
  treat_missing_data  = "missing"
  alarm_actions       = local.alarm_actions
  ok_actions          = local.alarm_actions

  dimensions = {
    DBInstanceIdentifier = aws_db_instance.this[0].identifier
  }

  tags = var.tags
}

resource "aws_cloudwatch_metric_alarm" "db_capacity" {
  count = var.alarms_enabled && var.use_aurora_serverless ? length(local.db_instance_identifiers) : 0

  alarm_name          = "${local.db_instance_identifiers[count.index]}-db-capacity"
  alarm_description   = "Stackorder Aurora instance ${local.db_instance_identifiers[count.index]} used more than ${var.alarm_thresholds.db_acu_utilization_percent}% of aurora_max_acu for 15 minutes"
  namespace           = "AWS/RDS"
  metric_name         = "ACUUtilization"
  statistic           = "Average"
  period              = 300
  evaluation_periods  = 3
  comparison_operator = "GreaterThanThreshold"
  threshold           = var.alarm_thresholds.db_acu_utilization_percent
  treat_missing_data  = "missing"
  alarm_actions       = local.alarm_actions
  ok_actions          = local.alarm_actions

  dimensions = {
    DBInstanceIdentifier = aws_rds_cluster_instance.this[count.index].identifier
  }

  tags = var.tags
}
