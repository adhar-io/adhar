# Common KCL configuration for Adhar platform applications
# This file contains shared configurations and types used across all platform apps

schema GlobalConfig:
    """Global configuration for all platform applications"""
    enableHAMode: bool = True
    namespace: str = "adhar-system"
    clusterDomain: str = "cluster.local"

schema ResourceConstraints:
    """Resource constraints for containers"""
    requests: {str: str}
    limits: {str: str}

schema OverlayConfig:
    """Configuration for deployment overlays"""
    replicas: int = 1
    resources: ResourceConstraints

# Default resource constraints for local development
localResourceConstraints = ResourceConstraints {
    requests = {
        cpu = "100m"
        memory = "256Mi"
    }
    limits = {
        cpu = "500m"
        memory = "512Mi"
    }
}

# Production resource constraints
productionResourceConstraints = ResourceConstraints {
    requests = {
        cpu = "500m"
        memory = "1Gi"
    }
    limits = {
        cpu = "2"
        memory = "4Gi"
    }
}
