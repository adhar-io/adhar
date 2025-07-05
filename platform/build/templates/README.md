# Adhar Platform Template System

This directory contains the KCL-based template system for Adhar platform applications. This system replaces the previous inline YAML and Kustomize patch approach with a more maintainable and configurable solution.

## Architecture

### Directory Structure

```text
platform/build/templates/platform-apps/
├── config.k                    # Main KCL configuration file
├── common.k                    # Common schemas and utilities (placeholder)
├── gitea.k                     # Gitea-specific KCL configuration (placeholder)
└── platform-apps/
    ├── gitea/
    │   └── base.yaml           # Base Gitea YAML manifests
    ├── argocd/
    │   └── base.yaml           # Base ArgoCD YAML manifests
    ├── nginx/
    │   └── base.yaml           # Base Nginx YAML manifests
    ├── cilium/
    │   └── base.yaml           # Base Cilium YAML manifests
    └── crossplane/
        └── base.yaml           # Base Crossplane YAML manifests
```

### Template Engine

The `TemplateEngine` in `template_engine.go` handles:

1. **KCL Configuration Loading**: Loads KCL configuration for specific apps and deployment modes
2. **Manifest Generation**: Combines KCL configuration with base YAML templates
3. **Overlay Application**: Applies environment-specific patches (local vs production mode)
4. **Fallback Support**: Provides hardcoded fallback when KCL is not available

## Configuration

### Global Configuration (`config.k`)

The main configuration file defines:

- Global settings (namespace, cluster domain)
- Resource constraints for local and production modes
- Service-specific configurations for each platform app

### Service Configurations

Each platform application has specific configuration sections:

- **Gitea**: Replicas, PostgreSQL/Redis replicas, resource constraints
- **ArgoCD**: Server/controller replicas, resource constraints  
- **Nginx**: Replicas, resource constraints
- **Cilium**: Operator replicas, Hubble settings, encryption options
- **Crossplane**: Replicas, resource constraints

## Usage

### From Go Code

```go
// Create template engine
templateEngine := NewTemplateEngine()

// Generate manifests for a service with HA mode
manifests, err := templateEngine.GenerateManifests(ctx, "gitea", true)
if err != nil {
    return err
}

// Apply manifests to cluster
// ... kubectl apply logic
```

### KCL Integration

When KCL is available, the system:
1. Runs `kcl run config.k -d <service>_config.<mode>` to extract configuration
2. Parses the YAML/JSON output into Go structs
3. Applies the configuration to base YAML templates using Kustomize patches

### Fallback Mode

When KCL is not available:
1. Uses hardcoded configuration in Go code
2. Applies the same patch-based approach using Kustomize
3. Logs a warning but continues operation

## Environment Modes

### Local/Development Mode (`enableHAMode: false`)
- Single replicas for most services
- Resource constraints (CPU: 100m-500m, Memory: 256Mi-512Mi)
- Disabled advanced features (encryption, L7 proxy)
- Optimized for development environments

### Production/HA Mode (`enableHAMode: true`)
- Multiple replicas for high availability
- Higher resource allocations (CPU: 500m-2, Memory: 1Gi-4Gi)
- Advanced features enabled
- Optimized for production workloads

## Migration from Old System

The new template system replaces:
- ✅ **Removed**: Inline YAML generation in Go code
- ✅ **Removed**: `createServiceOverlay()` and related methods
- ✅ **Removed**: `createGenericLocalOverlay()` and `createGenericProductionOverlay()`
- ✅ **Removed**: `createCiliumLocalPatches()` and similar patch methods
- ✅ **Added**: Centralized KCL configuration
- ✅ **Added**: Template engine with fallback support
- ✅ **Added**: Organized template directory structure

## Benefits

1. **Separation of Concerns**: Configuration is separate from Go code
2. **Maintainability**: Templates are easier to modify and understand
3. **Consistency**: Standardized approach across all platform apps
4. **Flexibility**: KCL provides powerful configuration capabilities
5. **Fallback Support**: System works with or without KCL installed
6. **Version Control**: Templates and configurations are tracked separately

## Future Enhancements

1. **Full KCL Templates**: Convert base YAML to KCL for complete template-based approach
2. **Advanced KCL Features**: Use KCL's validation, schema checking, and composition features
3. **External Configuration**: Support for external KCL configuration sources
4. **Template Validation**: Pre-deployment validation of generated manifests
5. **Multi-Environment Support**: Environment-specific template variants

## Development Guidelines

### Adding New Platform Apps

1. Create directory: `platform-apps/<app-name>/`
2. Add base YAML: `platform-apps/<app-name>/base.yaml`
3. Update `config.k` with app-specific configuration
4. Add app to platform apps list in `build.go`
5. Implement app-specific patch generation in `template_engine.go`

### Modifying Existing Apps

1. Update base YAML in respective `base.yaml` file
2. Modify configuration in `config.k`
3. Update patch generation logic in `template_engine.go` if needed
4. Test with both local and production modes

### Testing

1. Test with KCL available: `kcl --version`
2. Test fallback mode: temporarily rename KCL binary
3. Verify manifests: `kubectl apply --dry-run=client`
4. Validate resources: `kubectl describe` after deployment
